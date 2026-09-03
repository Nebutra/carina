package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Nebutra/carina/go/artifact"
	carinabrowser "github.com/Nebutra/carina/go/browser"
	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/netguard"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	maxBrowserOrigins     = 32
	maxBrowserUploadFiles = 8
	maxBrowserUploadBytes = 8 << 20
)

var errBrowserAuthorization = errors.New("browser authorization failed")

func resetBrowserUploadRoot(stateDir string) error {
	root, err := browserUploadRootPath(stateDir)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove stale browser upload staging: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create browser upload staging: %w", err)
	}
	return os.Chmod(root, 0o700)
}

func browserUploadRootPath(stateDir string) (string, error) {
	stateRoot, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("resolve state directory: %w", err)
	}
	root := filepath.Join(stateRoot, "browser-uploads")
	rel, err := filepath.Rel(stateRoot, root)
	if err != nil || rel != "browser-uploads" {
		return "", fmt.Errorf("invalid browser upload staging root")
	}
	return root, nil
}

type browserActionInput struct {
	Kind           carinabrowser.ActionKind `json:"kind"`
	Ref            string                   `json:"ref,omitempty"`
	Text           string                   `json:"text,omitempty"`
	Values         []string                 `json:"values,omitempty"`
	Checked        *bool                    `json:"checked,omitempty"`
	Key            carinabrowser.Key        `json:"key,omitempty"`
	DeltaX         int                      `json:"delta_x,omitempty"`
	DeltaY         int                      `json:"delta_y,omitempty"`
	Files          []string                 `json:"files,omitempty"`
	DeclaredEffect carinabrowser.Effect     `json:"declared_effect,omitempty"`
}

func browserActionKind(raw json.RawMessage) string {
	var input struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &input) != nil || strings.TrimSpace(input.Kind) == "" {
		return "invalid"
	}
	return input.Kind
}

func decodeBrowserAction(raw json.RawMessage) (carinabrowser.Action, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return carinabrowser.Action{}, fmt.Errorf("action is required")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var input browserActionInput
	if err := decoder.Decode(&input); err != nil {
		return carinabrowser.Action{}, fmt.Errorf("invalid browser action: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return carinabrowser.Action{}, fmt.Errorf("browser action must contain one object")
	}
	return carinabrowser.Action{
		Kind: input.Kind, Ref: input.Ref, Text: input.Text,
		Values: append([]string(nil), input.Values...), Checked: input.Checked,
		Key: input.Key, DeltaX: input.DeltaX, DeltaY: input.DeltaY,
		Files: append([]string(nil), input.Files...), DeclaredEffect: input.DeclaredEffect,
	}, nil
}

func browserSessionKey(sess *sessionstore.Session) carinabrowser.SessionKey {
	return carinabrowser.SessionKey{
		TenantID:  sessionstore.NormalizeTenantID(sess.TenantID),
		SessionID: sess.SessionID,
	}
}

func (d *Daemon) browserCapabilityOutcome(
	sess *sessionstore.Session,
	task *scheduler.ExecutionRun,
	capability, resource, label string,
	mandatoryApproval bool,
) *toolExecutionOutcome {
	decision, err := d.kern.Request(sess.SessionID, capability, resource, task.RunID)
	if err != nil {
		outcome := toolFailed("browser governance did not complete", "governance_error")
		return &outcome
	}
	switch decision.Decision {
	case "denied":
		outcome := toolDenied("browser request denied by policy", "policy_denied")
		return &outcome
	case "requires_approval":
		var approved *kernel.Decision
		var ok bool
		if mandatoryApproval {
			approved, ok = d.resolveMandatoryBrowserApproval(sess, task, decision, label)
		} else {
			approved, ok = d.resolveApprovalOrEscalate(sess, task, decision, capability, resource, label)
		}
		if !ok || approved == nil || approved.Decision != "allowed" {
			outcome := toolDenied("required browser approval was not granted", "approval_denied")
			return &outcome
		}
	}
	if err := d.ensureActiveToolStarted(task.RunID); err != nil {
		outcome := toolFailed("browser governance could not persist execution", "audit_persistence_error")
		return &outcome
	}
	return nil
}

// Browser attach and high-impact browser effects are never agent-approved and
// never consume remembered grants. dont-ask remains a fail-closed no-prompt
// mode; all other product modes require a live operator decision.
func (d *Daemon) resolveMandatoryBrowserApproval(
	sess *sessionstore.Session,
	task *scheduler.ExecutionRun,
	decision *kernel.Decision,
	label string,
) (*kernel.Decision, bool) {
	if err := d.markActiveToolApprovalRequired(task.RunID, decision.DecisionID); err != nil {
		d.closePendingApproval(sess, task, decision, "denied", "approval lifecycle could not be persisted")
		return decision, false
	}
	if d.approvalModeString() == approvalModeDontAsk {
		d.closePendingApproval(sess, task, decision, "denied", "dont-ask mode denies mandatory browser approval")
		return decision, false
	}
	return d.interactiveApproveRequiresApproval(sess, task, decision, label)
}

func (d *Daemon) authorizeBrowserEffect(
	sess *sessionstore.Session,
	task *scheduler.ExecutionRun,
	mode carinabrowser.Mode,
	effect carinabrowser.Effect,
) *toolExecutionOutcome {
	resource := string(mode) + ":" + string(effect)
	return d.browserCapabilityOutcome(
		sess, task, "BrowserInteract", resource,
		"browser "+string(effect)+" in "+string(mode)+" mode",
		effect.RequiresApproval(),
	)
}

func normalizeBrowserTarget(raw string) (target, origin, host string, err error) {
	parsed, err := netguard.NormalizePublicHTTPSURL(raw)
	if err != nil {
		return "", "", "", err
	}
	origin = "https://" + parsed.Hostname()
	return parsed.String(), origin, parsed.Hostname(), nil
}

func normalizeBrowserOrigins(raw []string) ([]string, []string, error) {
	if len(raw) > maxBrowserOrigins {
		return nil, nil, fmt.Errorf("approved_origins must contain at most %d entries", maxBrowserOrigins)
	}
	origins := make([]string, 0, len(raw))
	hosts := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		origin, err := netguard.NormalizePublicHTTPSOrigin(value)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid approved origin")
		}
		if _, exists := seen[origin]; exists {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
		hosts = append(hosts, strings.TrimPrefix(origin, "https://"))
	}
	return origins, hosts, nil
}

func (d *Daemon) allowBrowserOrigins(
	ctx context.Context,
	sess *sessionstore.Session,
	task *scheduler.ExecutionRun,
	browserID string,
	origins, hosts []string,
) *toolExecutionOutcome {
	if len(origins) == 0 {
		return nil
	}
	for _, host := range hosts {
		if outcome := d.browserCapabilityOutcome(sess, task, "NetworkAccess", host, "allow browser access to "+host, false); outcome != nil {
			return outcome
		}
	}
	if err := d.browsers.AllowOrigins(ctx, browserSessionKey(sess), browserID, origins); err != nil {
		outcome := browserToolFailure(err)
		return &outcome
	}
	return nil
}

func (d *Daemon) allowBrowserTarget(
	ctx context.Context,
	sess *sessionstore.Session,
	task *scheduler.ExecutionRun,
	browserID, rawURL string,
) (string, *toolExecutionOutcome) {
	target, origin, host, err := normalizeBrowserTarget(rawURL)
	if err != nil {
		outcome := browserInvalidRequest("browser URL must be a public HTTPS URL")
		return "", &outcome
	}
	if outcome := d.allowBrowserOrigins(ctx, sess, task, browserID, []string{origin}, []string{host}); outcome != nil {
		return "", outcome
	}
	return target, nil
}

func (d *Daemon) browserOpenOutcome(ctx context.Context, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	if d.browsers == nil {
		return browserUnavailable("native browser runtime is unavailable")
	}
	key := browserSessionKey(sess)
	if act.BrowserID != "" {
		if act.WaitMode != "" || strings.TrimSpace(act.TabID) == "" || strings.TrimSpace(act.URL) == "" {
			return browserInvalidRequest("navigating an existing browser requires browser_id, tab_id, and url only")
		}
		origins, hosts, err := normalizeBrowserOrigins(act.ApprovedOrigins)
		if err != nil {
			return browserInvalidRequest(err.Error())
		}
		validatedTarget, targetOrigin, targetHost, err := normalizeBrowserTarget(act.URL)
		if err != nil {
			return browserInvalidRequest("browser URL must be a public HTTPS URL")
		}
		origins, hosts = appendBrowserOrigin(origins, hosts, targetOrigin, targetHost)
		if len(origins) > maxBrowserOrigins {
			return browserInvalidRequest("approved origins and navigation target exceed the origin limit")
		}
		mode, err := d.browsers.BrowserMode(key, act.BrowserID)
		if err != nil {
			return browserToolFailure(err)
		}
		if outcome := d.authorizeBrowserEffect(sess, task, mode, carinabrowser.EffectReversible); outcome != nil {
			return *outcome
		}
		if outcome := d.allowBrowserOrigins(ctx, sess, task, act.BrowserID, origins, hosts); outcome != nil {
			return *outcome
		}
		if err := d.browsers.Navigate(ctx, key, act.BrowserID, act.TabID, validatedTarget); err != nil {
			return browserToolFailure(err)
		}
		return browserJSON(map[string]any{
			"browser_id": act.BrowserID, "tab_id": act.TabID, "mode": mode,
			"navigated": true, "refs_stale": true,
		})
	}

	if act.TabID != "" || (act.WaitMode != string(carinabrowser.ModeManaged) && act.WaitMode != string(carinabrowser.ModeAttach)) {
		return browserInvalidRequest("opening a browser requires mode managed or attach and no tab_id")
	}
	origins, hosts, err := normalizeBrowserOrigins(act.ApprovedOrigins)
	if err != nil {
		return browserInvalidRequest(err.Error())
	}
	validatedTarget := ""
	validatedOrigin := ""
	validatedHost := ""
	if strings.TrimSpace(act.URL) != "" {
		validatedTarget, validatedOrigin, validatedHost, err = normalizeBrowserTarget(act.URL)
		if err != nil {
			return browserInvalidRequest("browser URL must be a public HTTPS URL")
		}
		origins, hosts = appendBrowserOrigin(origins, hosts, validatedOrigin, validatedHost)
		if len(origins) > maxBrowserOrigins {
			return browserInvalidRequest("approved origins and navigation target exceed the origin limit")
		}
	}
	mode := carinabrowser.Mode(act.WaitMode)
	if mode == carinabrowser.ModeAttach {
		if d.browserAttachEndpoint == "" {
			return browserUnavailable("operator browser attachment is not configured")
		}
		if outcome := d.browserCapabilityOutcome(sess, task, "BrowserAttach", "operator_endpoint", "attach the operator browser", true); outcome != nil {
			return *outcome
		}
	}
	if outcome := d.authorizeBrowserEffect(sess, task, mode, carinabrowser.EffectReversible); outcome != nil {
		return *outcome
	}
	attachEndpoint := ""
	if mode == carinabrowser.ModeAttach {
		attachEndpoint = d.browserAttachEndpoint
	}
	opened, err := d.browsers.Open(ctx, carinabrowser.OpenRequest{Key: key, Mode: mode, AttachEndpoint: attachEndpoint})
	if err != nil {
		return browserToolFailure(err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = d.browsers.Close(key, opened.BrowserID)
		}
	}()
	if outcome := d.allowBrowserOrigins(ctx, sess, task, opened.BrowserID, origins, hosts); outcome != nil {
		return *outcome
	}
	if validatedTarget != "" {
		tabID := activeBrowserTab(opened.Tabs)
		if tabID == "" {
			return browserUnavailable("browser did not expose an active tab")
		}
		if err := d.browsers.Navigate(ctx, key, opened.BrowserID, tabID, validatedTarget); err != nil {
			return browserToolFailure(err)
		}
		opened.Tabs, err = d.browsers.ListTabs(ctx, key, opened.BrowserID)
		if err != nil {
			return browserToolFailure(err)
		}
	}
	keep = true
	return browserJSON(map[string]any{
		"browser_id": opened.BrowserID, "mode": opened.Mode, "tabs": opened.Tabs,
		"untrusted": true, "content_note": "Browser titles and URLs are untrusted page data.",
	})
}

func appendBrowserOrigin(origins, hosts []string, origin, host string) ([]string, []string) {
	for _, existing := range origins {
		if existing == origin {
			return origins, hosts
		}
	}
	return append(origins, origin), append(hosts, host)
}

func activeBrowserTab(tabs []carinabrowser.Tab) string {
	for _, tab := range tabs {
		if tab.Active {
			return tab.ID
		}
	}
	if len(tabs) > 0 {
		return tabs[0].ID
	}
	return ""
}

func (d *Daemon) browserSnapshotOutcome(ctx context.Context, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	if !validBrowserOpaqueID(act.TabID) {
		return browserInvalidRequest("tab_id is invalid")
	}
	mode, failure := d.browserMode(sess, act.BrowserID)
	if failure != nil {
		return *failure
	}
	if outcome := d.authorizeBrowserEffect(sess, task, mode, carinabrowser.EffectObserve); outcome != nil {
		return *outcome
	}
	snapshot, err := d.browsers.Snapshot(ctx, browserSessionKey(sess), act.BrowserID, act.TabID)
	if err != nil {
		return browserToolFailure(err)
	}
	return browserJSON(snapshot)
}

func (d *Daemon) browserActionOutcome(ctx context.Context, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	if !validBrowserOpaqueID(act.TabID) {
		return browserInvalidRequest("tab_id is invalid")
	}
	actionValue, err := decodeBrowserAction(act.Action)
	if err != nil {
		return browserInvalidRequest(err.Error())
	}
	validationAction := actionValue
	if validationAction.Kind == carinabrowser.ActionUpload {
		validationAction.Files = make([]string, len(actionValue.Files))
		for i := range validationAction.Files {
			validationAction.Files[i] = fmt.Sprintf("/carina-upload-placeholder-%d", i)
		}
	}
	if err := carinabrowser.ValidateAction(validationAction); err != nil {
		return browserToolFailure(err)
	}
	if _, failure := d.browserMode(sess, act.BrowserID); failure != nil {
		return *failure
	}
	cleanup := func() {}
	if actionValue.Kind == carinabrowser.ActionUpload {
		staged, stageCleanup, failure := d.stageBrowserUploads(sess, task, actionValue.Files)
		if failure != nil {
			return *failure
		}
		actionValue.Files = staged
		cleanup = stageCleanup
	}
	defer cleanup()

	var gateFailure *toolExecutionOutcome
	result, err := d.browsers.Action(ctx, browserSessionKey(sess), act.BrowserID, act.TabID, actionValue, func(plan carinabrowser.ActionPlan) error {
		mode := carinabrowser.ModeManaged
		if plan.Attached {
			mode = carinabrowser.ModeAttach
		}
		gateFailure = d.authorizeBrowserEffect(sess, task, mode, plan.EffectiveEffect)
		if gateFailure != nil {
			return errBrowserAuthorization
		}
		return nil
	})
	if errors.Is(err, errBrowserAuthorization) && gateFailure != nil {
		return *gateFailure
	}
	if err != nil {
		return browserToolFailure(err)
	}
	response := map[string]any{
		"plan": result.Plan, "navigated": result.Navigated, "refs_stale": result.RefsStale,
		"untrusted": true, "content_note": "Dialog and download metadata are untrusted page data.",
	}
	if result.Dialog != nil {
		response["dialog"] = result.Dialog
	}
	if result.Download == nil {
		return browserJSON(response)
	}
	raw := result.Download.Bytes
	result.Download.Bytes = nil
	defer clear(raw)
	mediaType := safeDownloadMediaType(result.Download.MediaType)
	meta, err := d.artifacts.Put(raw, artifact.PutOptions{
		Scope:     artifact.Scope{SessionID: sess.SessionID},
		MediaType: mediaType, Retention: artifact.RetentionNormal,
	})
	if err != nil {
		return browserToolFailure(&carinabrowser.Error{
			Code: carinabrowser.ErrorResourceLimit, Message: "browser download could not be quarantined",
			Recovery: "free artifact quota or request a smaller download", Retryable: true, Cause: err,
		})
	}
	response["download"] = map[string]any{
		"artifact_id": meta.ID, "suggested_name": result.Download.SuggestedName,
		"media_type": mediaType, "bytes": meta.Bytes, "executable": result.Download.Executable,
		"quarantined": true,
	}
	encoded, _ := json.Marshal(response)
	return toolCompletedArtifacts(string(encoded), meta.ID)
}

func (d *Daemon) stageBrowserUploads(sess *sessionstore.Session, task *scheduler.ExecutionRun, paths []string) ([]string, func(), *toolExecutionOutcome) {
	if len(paths) == 0 || len(paths) > maxBrowserUploadFiles {
		outcome := browserInvalidRequest("upload requires 1..8 files")
		return nil, func() {}, &outcome
	}
	root, rootErr := browserUploadRootPath(d.stateDir)
	if rootErr != nil {
		outcome := browserUnavailable("private browser upload staging is unavailable")
		return nil, func() {}, &outcome
	}
	if err := os.MkdirAll(root, 0o700); err != nil || os.Chmod(root, 0o700) != nil {
		outcome := browserUnavailable("private browser upload staging is unavailable")
		return nil, func() {}, &outcome
	}
	dir, err := os.MkdirTemp(root, "upload-")
	if err != nil {
		outcome := browserUnavailable("private browser upload staging is unavailable")
		return nil, func() {}, &outcome
	}
	_ = os.Chmod(dir, 0o700)
	cleanup := func() { _ = os.RemoveAll(dir) }
	staged := make([]string, 0, len(paths))
	total := int64(0)
	for i, submitted := range paths {
		path := submitted
		if !filepath.IsAbs(path) {
			path = resolveIn(sess.WorkspaceRoot, path)
		}
		decision, err := d.kern.Request(sess.SessionID, "FileRead", path, task.RunID)
		if err != nil {
			cleanup()
			outcome := toolFailed("browser upload read authorization failed", "governance_error")
			return nil, func() {}, &outcome
		}
		if decision.Decision == "denied" {
			cleanup()
			outcome := toolDenied("browser upload file was denied by policy", "policy_denied")
			return nil, func() {}, &outcome
		}
		if decision.Decision == "requires_approval" {
			approved, ok := d.resolveApprovalOrEscalate(sess, task, decision, "FileRead", path, "read a file for browser upload")
			if !ok || approved == nil || approved.Decision != "allowed" {
				cleanup()
				outcome := toolDenied("browser upload file approval was not granted", "approval_denied")
				return nil, func() {}, &outcome
			}
			decision = approved
		}
		raw, err := readStableRegularFile(path, maxBrowserUploadBytes-total)
		if err != nil {
			cleanup()
			outcome := browserInvalidRequest("browser upload source must be a stable regular file within the size limit")
			return nil, func() {}, &outcome
		}
		total += int64(len(raw))
		if total > maxBrowserUploadBytes {
			clear(raw)
			cleanup()
			outcome := browserResourceLimit("browser uploads exceed the aggregate size limit")
			return nil, func() {}, &outcome
		}
		if err := d.recordChecked(sess.SessionID, "FileRead", task.RunID, "go", map[string]any{
			"path": path, "bytes": len(raw), "purpose": "browser_upload_staging",
		}, decision.DecisionID); err != nil {
			clear(raw)
			cleanup()
			outcome := toolFailed("browser upload audit could not be persisted", "audit_persistence_error")
			return nil, func() {}, &outcome
		}
		base := safeUploadBase(filepath.Base(path), i)
		fileDir := filepath.Join(dir, fmt.Sprintf("%02d", i))
		if os.Mkdir(fileDir, 0o700) != nil {
			clear(raw)
			cleanup()
			outcome := browserUnavailable("private browser upload staging is unavailable")
			return nil, func() {}, &outcome
		}
		stagedPath := filepath.Join(fileDir, base)
		if err := os.WriteFile(stagedPath, raw, 0o600); err != nil {
			clear(raw)
			cleanup()
			outcome := browserUnavailable("private browser upload staging is unavailable")
			return nil, func() {}, &outcome
		}
		clear(raw)
		staged = append(staged, stagedPath)
	}
	return staged, cleanup, nil
}

func readStableRegularFile(path string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("upload limit reached")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || before.Size() != opened.Size() || !before.ModTime().Equal(opened.ModTime()) {
		return nil, fmt.Errorf("file identity changed")
	}
	if opened.Size() > limit {
		return nil, fmt.Errorf("file exceeds limit")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) || int64(len(raw)) > limit {
		clear(raw)
		return nil, fmt.Errorf("file changed or exceeds limit")
	}
	return raw, nil
}

func safeUploadBase(base string, index int) string {
	base = strings.ToValidUTF8(strings.TrimSpace(base), "")
	if base == "" || base == "." || base == ".." || len(base) > 255 {
		return fmt.Sprintf("upload-%02d.bin", index)
	}
	return base
}

func safeDownloadMediaType(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 255 || !utf8.ValidString(value) {
		return "application/octet-stream"
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || !strings.Contains(mediaType, "/") {
		return "application/octet-stream"
	}
	return strings.ToLower(mediaType)
}

func (d *Daemon) browserTabsOutcome(ctx context.Context, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	if message := validateBrowserTabsAction(act); message != "" {
		return browserInvalidRequest(message)
	}
	mode, failure := d.browserMode(sess, act.BrowserID)
	if failure != nil {
		return *failure
	}
	effect := carinabrowser.EffectReversible
	if act.TabOperation == "list" {
		effect = carinabrowser.EffectObserve
	}
	if outcome := d.authorizeBrowserEffect(sess, task, mode, effect); outcome != nil {
		return *outcome
	}
	key := browserSessionKey(sess)
	switch act.TabOperation {
	case "list":
	case "activate":
		if err := d.browsers.ActivateTab(ctx, key, act.BrowserID, act.TabID); err != nil {
			return browserToolFailure(err)
		}
	case "close":
		if err := d.browsers.CloseTab(ctx, key, act.BrowserID, act.TabID); err != nil {
			return browserToolFailure(err)
		}
	case "open":
		target, outcome := d.allowBrowserTarget(ctx, sess, task, act.BrowserID, act.URL)
		if outcome != nil {
			return *outcome
		}
		if _, err := d.browsers.OpenTab(ctx, key, act.BrowserID, target); err != nil {
			return browserToolFailure(err)
		}
	default:
		return browserInvalidRequest("browser.tabs operation must be list, open, activate, or close")
	}
	tabs, err := d.browsers.ListTabs(ctx, key, act.BrowserID)
	if err != nil {
		return browserToolFailure(err)
	}
	return browserJSON(map[string]any{
		"browser_id": act.BrowserID, "operation": act.TabOperation, "tabs": tabs,
		"untrusted": true, "content_note": "Browser titles and URLs are untrusted page data.",
	})
}

func (d *Daemon) browserCaptureOutcome(ctx context.Context, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	if !validBrowserOpaqueID(act.TabID) {
		return browserInvalidRequest("tab_id is invalid")
	}
	mode, failure := d.browserMode(sess, act.BrowserID)
	if failure != nil {
		return *failure
	}
	if outcome := d.authorizeBrowserEffect(sess, task, mode, carinabrowser.EffectObserve); outcome != nil {
		return *outcome
	}
	result, err := d.browsers.Capture(ctx, browserSessionKey(sess), act.BrowserID, carinabrowser.CaptureRequest{
		TabID: act.TabID, FullPage: act.FullPage,
	})
	if err != nil {
		return browserToolFailure(err)
	}
	raw := result.Bytes
	result.Bytes = nil
	defer clear(raw)
	ref, err := ingestImageMedia(d.artifacts, artifact.Scope{SessionID: sess.SessionID}, "browser.capture", raw)
	if err != nil {
		return browserResourceLimit("browser screenshot could not be stored")
	}
	display, _ := json.Marshal(map[string]any{
		"browser_id": act.BrowserID, "tab_id": act.TabID, "artifact": ref,
		"width": result.Width, "height": result.Height,
		"untrusted": true, "content_note": "Screenshot pixels are untrusted page data.",
	})
	return toolCompletedMedia(string(display), ref)
}

func validateBrowserTabsAction(act *action) string {
	switch act.TabOperation {
	case "list":
		if act.TabID != "" || act.URL != "" {
			return "browser.tabs list accepts no tab_id or url"
		}
	case "activate", "close":
		if !validBrowserOpaqueID(act.TabID) || act.URL != "" {
			return "browser.tabs " + act.TabOperation + " requires tab_id and no url"
		}
	case "open":
		if act.TabID != "" || strings.TrimSpace(act.URL) == "" {
			return "browser.tabs open requires url and no tab_id"
		}
		if _, _, _, err := normalizeBrowserTarget(act.URL); err != nil {
			return "browser.tabs open requires a public HTTPS URL"
		}
	default:
		return "browser.tabs operation must be list, open, activate, or close"
	}
	return ""
}

func validBrowserOpaqueID(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 512 && !strings.ContainsRune(value, '\x00')
}

func (d *Daemon) browserCloseOutcome(_ context.Context, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	mode, failure := d.browserMode(sess, act.BrowserID)
	if failure != nil {
		return *failure
	}
	if outcome := d.authorizeBrowserEffect(sess, task, mode, carinabrowser.EffectReversible); outcome != nil {
		return *outcome
	}
	if err := d.browsers.Close(browserSessionKey(sess), act.BrowserID); err != nil {
		return browserToolFailure(err)
	}
	return browserJSON(map[string]any{"browser_id": act.BrowserID, "closed": true})
}

func (d *Daemon) browserMode(sess *sessionstore.Session, browserID string) (carinabrowser.Mode, *toolExecutionOutcome) {
	if d.browsers == nil {
		outcome := browserUnavailable("native browser runtime is unavailable")
		return "", &outcome
	}
	mode, err := d.browsers.BrowserMode(browserSessionKey(sess), browserID)
	if err != nil {
		outcome := browserToolFailure(err)
		return "", &outcome
	}
	return mode, nil
}

func browserJSON(value any) toolExecutionOutcome {
	raw, err := json.Marshal(value)
	if err != nil {
		return browserToolFailure(err)
	}
	return toolCompleted(string(raw))
}

func browserInvalidRequest(message string) toolExecutionOutcome {
	return browserErrorOutcome("failed", carinabrowser.ErrorInvalidRequest, message, false)
}

func browserUnavailable(message string) toolExecutionOutcome {
	return browserErrorOutcome("failed", carinabrowser.ErrorUnavailable, message, true)
}

func browserResourceLimit(message string) toolExecutionOutcome {
	return browserErrorOutcome("failed", carinabrowser.ErrorResourceLimit, message, true)
}

func browserToolFailure(err error) toolExecutionOutcome {
	if errors.Is(err, context.Canceled) {
		return toolCancelled("browser operation was cancelled", "browser_cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return toolTimedOut("browser operation timed out")
	}
	var browserErr *carinabrowser.Error
	if !errors.As(err, &browserErr) {
		return browserErrorOutcome("failed", carinabrowser.ErrorDriver, "browser operation failed", true)
	}
	if browserErr.Code == carinabrowser.ErrorCancelled {
		if errors.Is(browserErr.Cause, context.DeadlineExceeded) {
			return toolTimedOut("browser operation timed out")
		}
		return toolCancelled("browser operation was cancelled", "browser_cancelled")
	}
	status := "failed"
	if browserErr.Code == carinabrowser.ErrorNetworkBlocked {
		status = "denied"
	}
	return browserErrorOutcome(status, browserErr.Code, browserErr.Message, browserErr.Retryable)
}

func browserErrorOutcome(status string, code carinabrowser.ErrorCode, message string, retryable bool) toolExecutionOutcome {
	message = sanitizeToolObservationMessage(message)
	if message == "" {
		message = "browser operation did not complete"
	}
	var outcome toolExecutionOutcome
	if status == "denied" {
		outcome = toolDenied(message, string(code))
	} else {
		outcome = toolFailed(message, string(code))
	}
	category := toolCategoryInternal
	recovery := toolRecoveryNone
	switch code {
	case carinabrowser.ErrorInvalidRequest:
		category, recovery = toolCategoryInvalidRequest, toolRecoveryRevise
	case carinabrowser.ErrorAlreadyOpen:
		category, recovery = toolCategoryConflict, toolRecoveryRevise
	case carinabrowser.ErrorNotFound:
		category, recovery = toolCategoryInvalidRequest, toolRecoveryRevise
	case carinabrowser.ErrorStaleRef:
		category, recovery = toolCategoryStaleState, toolRecoveryReread
	case carinabrowser.ErrorResourceLimit:
		category, recovery = toolCategoryResourceLimit, toolRecoveryRevise
	case carinabrowser.ErrorNetworkBlocked:
		category, recovery = toolCategoryPermission, toolRecoveryRevise
	case carinabrowser.ErrorUnavailable, carinabrowser.ErrorDriver:
		category, recovery = toolCategoryUnavailable, toolRecoveryRetry
	}
	outcome.observationError = &ToolObservationError{
		Status: ToolObservationStatus(status), Category: category,
		Retryable: retryable, Recovery: recovery, Message: message,
	}
	return outcome
}
