package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/artifact"
	carinabrowser "github.com/Nebutra/carina/go/browser"
	modelrouter "github.com/Nebutra/carina/go/model-router"
)

type daemonBrowserDriver struct {
	mu          sync.Mutex
	snapshot    carinabrowser.RawSnapshot
	launchErr   error
	attachments []carinabrowser.AttachOptions
	sessions    []*daemonBrowserSession
}

type daemonBrowserSession struct {
	mu           sync.Mutex
	snapshot     carinabrowser.RawSnapshot
	snapshotWait bool
	snapshotDone chan struct{}
	snapshotOnce sync.Once
	tabs         []carinabrowser.RawTab
	actions      []carinabrowser.DriverAction
	actionResult carinabrowser.DriverActionResult
	capture      carinabrowser.CaptureResult
	origins      []string
	closed       bool
}

func (d *daemonBrowserDriver) Launch(context.Context, carinabrowser.LaunchOptions) (carinabrowser.DriverSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.launchErr != nil {
		return nil, d.launchErr
	}
	snapshot := d.snapshot
	if snapshot.TabID == "" {
		snapshot.TabID = "tab-1"
	}
	if snapshot.DocumentID == "" {
		snapshot.DocumentID = "doc-1"
	}
	session := &daemonBrowserSession{
		snapshot: snapshot,
		tabs: []carinabrowser.RawTab{{
			ID: snapshot.TabID, URL: snapshot.URL, Title: snapshot.Title, Active: true,
		}},
		capture: carinabrowser.CaptureResult{
			MediaType: "image/png", Bytes: fakePNG("browser-capture"), Width: 640, Height: 480,
		},
	}
	d.sessions = append(d.sessions, session)
	return session, nil
}

func (d *daemonBrowserDriver) Attach(_ context.Context, options carinabrowser.AttachOptions) (carinabrowser.DriverSession, error) {
	d.mu.Lock()
	d.attachments = append(d.attachments, options)
	d.mu.Unlock()
	return d.Launch(context.Background(), carinabrowser.LaunchOptions{})
}

func (d *daemonBrowserDriver) latest() *daemonBrowserSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.sessions) == 0 {
		return nil
	}
	return d.sessions[len(d.sessions)-1]
}

func (s *daemonBrowserSession) AllowOrigins(_ context.Context, origins []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.origins = append(s.origins, origins...)
	return nil
}

func (s *daemonBrowserSession) Navigate(_ context.Context, tabID, target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tabID != s.snapshot.TabID {
		return errors.New("unknown tab")
	}
	s.snapshot.URL = target
	s.snapshot.DocumentID += "-nav"
	return nil
}

func (s *daemonBrowserSession) Snapshot(ctx context.Context, tabID string) (carinabrowser.RawSnapshot, error) {
	s.mu.Lock()
	if tabID != s.snapshot.TabID {
		s.mu.Unlock()
		return carinabrowser.RawSnapshot{}, errors.New("unknown tab")
	}
	snapshot := s.snapshot
	wait := s.snapshotWait
	done := s.snapshotDone
	s.mu.Unlock()
	if done != nil {
		s.snapshotOnce.Do(func() { close(done) })
	}
	if wait {
		<-ctx.Done()
		return carinabrowser.RawSnapshot{}, ctx.Err()
	}
	return snapshot, nil
}

func (s *daemonBrowserSession) InspectNode(_ context.Context, tabID, backendID string) (carinabrowser.NodeInspection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tabID != s.snapshot.TabID {
		return carinabrowser.NodeInspection{}, errors.New("unknown tab")
	}
	for _, node := range s.snapshot.Nodes {
		if node.BackendNodeID == backendID {
			return carinabrowser.NodeInspection{DocumentID: s.snapshot.DocumentID, Node: node}, nil
		}
	}
	return carinabrowser.NodeInspection{}, errors.New("unknown node")
}

func (s *daemonBrowserSession) ListTabs(context.Context) ([]carinabrowser.RawTab, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]carinabrowser.RawTab(nil), s.tabs...), nil
}

func (s *daemonBrowserSession) OpenTab(_ context.Context, target string) (carinabrowser.RawTab, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tab := carinabrowser.RawTab{ID: fmt.Sprintf("tab-%d", len(s.tabs)+1), URL: target, Active: true}
	s.tabs = append(s.tabs, tab)
	return tab, nil
}

func (s *daemonBrowserSession) ActivateTab(_ context.Context, tabID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tabs {
		s.tabs[i].Active = s.tabs[i].ID == tabID
	}
	return nil
}

func (s *daemonBrowserSession) CloseTab(_ context.Context, tabID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tabs {
		if s.tabs[i].ID == tabID {
			s.tabs = append(s.tabs[:i], s.tabs[i+1:]...)
			return nil
		}
	}
	return errors.New("unknown tab")
}

func (s *daemonBrowserSession) Action(_ context.Context, action carinabrowser.DriverAction) (carinabrowser.DriverActionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = append(s.actions, action)
	return s.actionResult, nil
}

func (s *daemonBrowserSession) Capture(context.Context, carinabrowser.CaptureRequest) (carinabrowser.CaptureResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capture, nil
}

func (s *daemonBrowserSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func installDaemonBrowserDriver(t *testing.T, d *Daemon, driver *daemonBrowserDriver) {
	t.Helper()
	if d.browsers != nil {
		_ = d.browsers.CloseAll()
	}
	manager, err := carinabrowser.NewManager(driver, carinabrowser.Config{
		ProfileRoot: filepath.Join(t.TempDir(), "profiles"),
	})
	if err != nil {
		t.Fatal(err)
	}
	d.browsers = manager
}

func TestBrowserToolsManagedJourneyArtifactsAndClose(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	driver := &daemonBrowserDriver{snapshot: carinabrowser.RawSnapshot{
		TabID: "tab-1", DocumentID: "doc-1", URL: "about:blank",
		Nodes: []carinabrowser.RawNode{{BackendNodeID: "node-1", Role: "button", Name: "Continue"}},
	}}
	installDaemonBrowserDriver(t, d, driver)
	sess, err := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "browser journey")

	_, openedOutcome := d.executeActionOutcome(sess, task, &action{Tool: "browser.open", WaitMode: "managed", Intent: "open an isolated browser"})
	if openedOutcome.status != "completed" {
		t.Fatalf("open = %+v", openedOutcome)
	}
	var opened struct {
		BrowserID string              `json:"browser_id"`
		Tabs      []carinabrowser.Tab `json:"tabs"`
	}
	if json.Unmarshal([]byte(openedOutcome.display), &opened) != nil || opened.BrowserID == "" || len(opened.Tabs) != 1 {
		t.Fatalf("open payload = %s", openedOutcome.display)
	}

	_, snapshotOutcome := d.executeActionOutcome(sess, task, &action{
		Tool: "browser.snapshot", BrowserID: opened.BrowserID, TabID: opened.Tabs[0].ID, Intent: "inspect the page",
	})
	var snapshot carinabrowser.Snapshot
	if snapshotOutcome.status != "completed" || json.Unmarshal([]byte(snapshotOutcome.display), &snapshot) != nil || len(snapshot.Nodes) != 1 {
		t.Fatalf("snapshot = %+v payload=%s", snapshotOutcome, snapshotOutcome.display)
	}

	downloadBytes := []byte("quarantined browser download")
	session := driver.latest()
	session.mu.Lock()
	session.actionResult = carinabrowser.DriverActionResult{
		Download: &carinabrowser.Download{SuggestedName: "report.txt", MediaType: "text/plain", Bytes: downloadBytes},
	}
	session.mu.Unlock()
	actionRaw := json.RawMessage(fmt.Sprintf(`{"kind":"click","ref":%q}`, snapshot.Nodes[0].Ref))
	_, actionOutcome := d.executeActionOutcome(sess, task, &action{
		Tool: "browser.action", BrowserID: opened.BrowserID, TabID: opened.Tabs[0].ID,
		Action: actionRaw, Intent: "activate the page control",
	})
	if actionOutcome.status != "completed" || len(actionOutcome.artifactIDs) != 1 || strings.Contains(actionOutcome.display, string(downloadBytes)) {
		t.Fatalf("action = %+v", actionOutcome)
	}
	if raw, _, err := d.artifacts.Read(artifact.Scope{SessionID: sess.SessionID}, actionOutcome.artifactIDs[0]); err != nil || string(raw) != "quarantined browser download" {
		t.Fatalf("download artifact = %q err=%v", raw, err)
	}
	for i, value := range downloadBytes {
		if value != 0 {
			t.Fatalf("download byte %d was not cleared", i)
		}
	}

	_, captureOutcome := d.executeActionOutcome(sess, task, &action{
		Tool: "browser.capture", BrowserID: opened.BrowserID, TabID: opened.Tabs[0].ID, Intent: "capture evidence",
	})
	if captureOutcome.status != "completed" || len(captureOutcome.mediaRefs) != 1 {
		t.Fatalf("capture = %+v", captureOutcome)
	}
	if _, _, err := d.artifacts.Read(artifact.Scope{SessionID: sess.SessionID}, captureOutcome.mediaRefs[0].ArtifactID); err != nil {
		t.Fatalf("capture artifact: %v", err)
	}

	_, tabsOutcome := d.executeActionOutcome(sess, task, &action{
		Tool: "browser.tabs", BrowserID: opened.BrowserID, TabOperation: "list", Intent: "list tabs",
	})
	if tabsOutcome.status != "completed" || !strings.Contains(tabsOutcome.display, `"operation":"list"`) {
		t.Fatalf("tabs = %+v", tabsOutcome)
	}
	_, closeOutcome := d.executeActionOutcome(sess, task, &action{
		Tool: "browser.close", BrowserID: opened.BrowserID, Intent: "close the isolated browser",
	})
	if closeOutcome.status != "completed" {
		t.Fatalf("close = %+v", closeOutcome)
	}
	session.mu.Lock()
	closed := session.closed
	session.mu.Unlock()
	if !closed {
		t.Fatal("driver session remained open")
	}

	events := readAuditEvents(t, d, sess.SessionID)
	assertBrowserArtifactTerminalEvent(t, events, actionOutcome.artifactIDs[0], false)
	assertBrowserArtifactTerminalEvent(t, events, captureOutcome.mediaRefs[0].ArtifactID, true)
}

func assertBrowserArtifactTerminalEvent(t *testing.T, events []map[string]any, artifactID string, wantMedia bool) {
	t.Helper()
	for _, event := range events {
		if event["type"] != "ToolCallCompleted" {
			continue
		}
		payload, _ := event["payload"].(map[string]any)
		ids, _ := payload["artifact_ids"].([]any)
		found := false
		for _, value := range ids {
			if value == artifactID {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		if eventID, _ := event["event_id"].(string); eventID == "" {
			t.Fatalf("artifact %s has no authoritative lifecycle event id", artifactID)
		}
		mediaRefs, _ := payload["media_refs"].([]any)
		if wantMedia && len(mediaRefs) == 0 {
			t.Fatalf("screenshot artifact %s has no media_refs projection", artifactID)
		}
		return
	}
	t.Fatalf("artifact %s is not linked from a ToolCallCompleted event", artifactID)
}

func TestBrowserToolsExecuteInAllRegistryModes(t *testing.T) {
	for _, mode := range []builtinToolRegistryMode{
		builtinToolRegistryDescriptor,
		builtinToolRegistryShadow,
		builtinToolRegistryLegacy,
	} {
		t.Run(string(mode), func(t *testing.T) {
			d, workspace := newLoopDaemon(t)
			defer d.Close()
			d.builtinToolsMode = mode
			driver := &daemonBrowserDriver{snapshot: carinabrowser.RawSnapshot{
				TabID: "tab-1", DocumentID: "doc-1",
				Nodes: []carinabrowser.RawNode{{BackendNodeID: "node-1", Role: "button", Name: "Continue"}},
			}}
			installDaemonBrowserDriver(t, d, driver)
			sess, err := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
			if err != nil {
				t.Fatal(err)
			}
			if err := d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "on_request", nil); err != nil {
				t.Fatal(err)
			}
			task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "browser registry parity")

			_, opened := d.executeActionOutcome(sess, task, &action{Tool: "browser.open", WaitMode: "managed", Intent: "open browser"})
			var openPayload struct {
				BrowserID string              `json:"browser_id"`
				Tabs      []carinabrowser.Tab `json:"tabs"`
			}
			if opened.status != "completed" || json.Unmarshal([]byte(opened.display), &openPayload) != nil || len(openPayload.Tabs) != 1 {
				t.Fatalf("open = %+v", opened)
			}
			_, snapped := d.executeActionOutcome(sess, task, &action{
				Tool: "browser.snapshot", BrowserID: openPayload.BrowserID, TabID: openPayload.Tabs[0].ID, Intent: "snapshot",
			})
			var snapshot carinabrowser.Snapshot
			if snapped.status != "completed" || json.Unmarshal([]byte(snapped.display), &snapshot) != nil || len(snapshot.Nodes) != 1 {
				t.Fatalf("snapshot = %+v", snapped)
			}
			_, acted := d.executeActionOutcome(sess, task, &action{
				Tool: "browser.action", BrowserID: openPayload.BrowserID, TabID: openPayload.Tabs[0].ID,
				Action: json.RawMessage(fmt.Sprintf(`{"kind":"click","ref":%q}`, snapshot.Nodes[0].Ref)), Intent: "click",
			})
			_, tabs := d.executeActionOutcome(sess, task, &action{
				Tool: "browser.tabs", BrowserID: openPayload.BrowserID, TabOperation: "list", Intent: "list tabs",
			})
			_, captured := d.executeActionOutcome(sess, task, &action{
				Tool: "browser.capture", BrowserID: openPayload.BrowserID, TabID: openPayload.Tabs[0].ID, Intent: "capture",
			})
			_, closed := d.executeActionOutcome(sess, task, &action{
				Tool: "browser.close", BrowserID: openPayload.BrowserID, Intent: "close browser",
			})
			if acted.status != "completed" || tabs.status != "completed" || captured.status != "completed" || len(captured.mediaRefs) != 1 || closed.status != "completed" {
				t.Fatalf("mode %s outcomes: action=%+v tabs=%+v capture=%+v close=%+v", mode, acted, tabs, captured, closed)
			}
		})
	}
}

func TestBrowserDestructiveActionIgnoresAlwaysApprove(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	driver := &daemonBrowserDriver{snapshot: carinabrowser.RawSnapshot{
		TabID: "tab-1", DocumentID: "doc-1",
		Nodes: []carinabrowser.RawNode{{BackendNodeID: "node-1", Role: "button", Name: "Delete account"}},
	}}
	installDaemonBrowserDriver(t, d, driver)
	if err := d.SetApprovalMode(approvalModeAlwaysApprove); err != nil {
		t.Fatal(err)
	}
	d.approvalTimeout = 5 * time.Second
	requests := permissionRequests(d)
	sess, err := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "browser approval")
	_, opened := d.executeActionOutcome(sess, task, &action{Tool: "browser.open", WaitMode: "managed", Intent: "open browser"})
	var openPayload struct {
		BrowserID string              `json:"browser_id"`
		Tabs      []carinabrowser.Tab `json:"tabs"`
	}
	if opened.status != "completed" || json.Unmarshal([]byte(opened.display), &openPayload) != nil {
		t.Fatalf("open = %+v", opened)
	}
	_, snapOutcome := d.executeActionOutcome(sess, task, &action{
		Tool: "browser.snapshot", BrowserID: openPayload.BrowserID, TabID: openPayload.Tabs[0].ID, Intent: "snapshot",
	})
	var snapshot carinabrowser.Snapshot
	if json.Unmarshal([]byte(snapOutcome.display), &snapshot) != nil || len(snapshot.Nodes) != 1 {
		t.Fatalf("snapshot = %+v", snapOutcome)
	}
	result := make(chan toolExecutionOutcome, 1)
	go func() {
		_, outcome := d.executeActionOutcome(sess, task, &action{
			Tool: "browser.action", BrowserID: openPayload.BrowserID, TabID: openPayload.Tabs[0].ID,
			Action: json.RawMessage(fmt.Sprintf(`{"kind":"click","ref":%q}`, snapshot.Nodes[0].Ref)), Intent: "delete account",
		})
		result <- outcome
	}()
	var decisionID string
	select {
	case decisionID = <-requests:
	case <-time.After(3 * time.Second):
		t.Fatal("destructive browser action did not request live approval")
	}
	session := driver.latest()
	session.mu.Lock()
	actionCount := len(session.actions)
	session.mu.Unlock()
	if actionCount != 0 {
		t.Fatal("driver action ran before approval")
	}
	if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{"decision_id": decisionID, "approve": true})); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-result:
		if outcome.status != "completed" {
			t.Fatalf("approved action = %+v", outcome)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("approved browser action did not resume")
	}
}

func TestBrowserAttachRequiresLiveApprovalAndKeepsEndpointOutOfOutput(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	driver := &daemonBrowserDriver{snapshot: carinabrowser.RawSnapshot{TabID: "tab-1", DocumentID: "doc-1"}}
	installDaemonBrowserDriver(t, d, driver)
	endpoint := "ws://127.0.0.1:9222/devtools/browser/private-token"
	d.browserAttachEndpoint = endpoint
	if err := d.SetApprovalMode(approvalModeAlwaysApprove); err != nil {
		t.Fatal(err)
	}
	d.approvalTimeout = 5 * time.Second
	requests := permissionRequests(d)
	sess, err := d.store.CreateSessionMode(workspace, "full-workspace", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	d.kern.InitSessionFull(sess.SessionID, workspace, "full-workspace", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "attach browser")
	result := make(chan toolExecutionOutcome, 1)
	go func() {
		_, outcome := d.executeActionOutcome(sess, task, &action{
			Tool: "browser.open", WaitMode: "attach", Intent: "attach the operator browser",
		})
		result <- outcome
	}()
	var decisionID string
	select {
	case decisionID = <-requests:
	case <-time.After(3 * time.Second):
		t.Fatal("attach did not request live approval")
	}
	driver.mu.Lock()
	attachmentsBefore := len(driver.attachments)
	driver.mu.Unlock()
	if attachmentsBefore != 0 {
		t.Fatal("driver attached before approval")
	}
	if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{"decision_id": decisionID, "approve": true})); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-result:
		if outcome.status != "completed" || strings.Contains(outcome.display, endpoint) || strings.Contains(outcome.display, "private-token") {
			t.Fatalf("attach outcome = %+v", outcome)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("approved attach did not resume")
	}
	driver.mu.Lock()
	attachments := append([]carinabrowser.AttachOptions(nil), driver.attachments...)
	driver.mu.Unlock()
	if len(attachments) != 1 || attachments[0].Endpoint != endpoint {
		t.Fatalf("driver attach options = %+v", attachments)
	}
}

func TestBrowserUploadStagesAuthorizedFileAndCleansItAfterApproval(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	driver := &daemonBrowserDriver{snapshot: carinabrowser.RawSnapshot{
		TabID: "tab-1", DocumentID: "doc-1",
		Nodes: []carinabrowser.RawNode{{BackendNodeID: "upload-node", Role: "button", Name: "Choose file", InputType: "file"}},
	}}
	installDaemonBrowserDriver(t, d, driver)
	d.SetInteractiveApproval(true)
	d.approvalTimeout = 5 * time.Second
	requests := permissionRequests(d)
	sourceName := "upload.txt"
	sourceContent := "explicit upload content"
	if err := os.WriteFile(filepath.Join(workspace, sourceName), []byte(sourceContent), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, err := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "upload file")
	_, opened := d.executeActionOutcome(sess, task, &action{Tool: "browser.open", WaitMode: "managed", Intent: "open browser"})
	var openPayload struct {
		BrowserID string              `json:"browser_id"`
		Tabs      []carinabrowser.Tab `json:"tabs"`
	}
	if opened.status != "completed" || json.Unmarshal([]byte(opened.display), &openPayload) != nil {
		t.Fatalf("open = %+v", opened)
	}
	_, snapOutcome := d.executeActionOutcome(sess, task, &action{
		Tool: "browser.snapshot", BrowserID: openPayload.BrowserID, TabID: openPayload.Tabs[0].ID, Intent: "snapshot upload control",
	})
	var snapshot carinabrowser.Snapshot
	if json.Unmarshal([]byte(snapOutcome.display), &snapshot) != nil || len(snapshot.Nodes) != 1 {
		t.Fatalf("snapshot = %+v", snapOutcome)
	}
	result := make(chan toolExecutionOutcome, 1)
	go func() {
		_, outcome := d.executeActionOutcome(sess, task, &action{
			Tool: "browser.action", BrowserID: openPayload.BrowserID, TabID: openPayload.Tabs[0].ID,
			Action: json.RawMessage(fmt.Sprintf(`{"kind":"upload","ref":%q,"files":[%q]}`, snapshot.Nodes[0].Ref, sourceName)),
			Intent: "upload the explicit file",
		})
		result <- outcome
	}()
	var decisionID string
	select {
	case decisionID = <-requests:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not request transmission approval")
	}
	session := driver.latest()
	session.mu.Lock()
	actionsBefore := len(session.actions)
	session.mu.Unlock()
	if actionsBefore != 0 {
		t.Fatal("upload reached driver before approval")
	}
	if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{"decision_id": decisionID, "approve": true})); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-result:
		if outcome.status != "completed" || strings.Contains(outcome.display, sourceContent) || strings.Contains(outcome.display, sourceName) {
			t.Fatalf("upload outcome = %+v", outcome)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("approved upload did not resume")
	}
	session.mu.Lock()
	actions := append([]carinabrowser.DriverAction(nil), session.actions...)
	session.mu.Unlock()
	if len(actions) != 1 || len(actions[0].Files) != 1 || !filepath.IsAbs(actions[0].Files[0]) {
		t.Fatalf("driver upload action = %+v", actions)
	}
	stagedPath := actions[0].Files[0]
	stageRoot := filepath.Join(d.stateDir, "browser-uploads")
	if rel, err := filepath.Rel(stageRoot, stagedPath); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		t.Fatalf("staged path escaped private root: %s rel=%s err=%v", stagedPath, rel, err)
	}
	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Fatalf("staged upload survived action: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(workspace, sourceName)); err != nil || string(raw) != sourceContent {
		t.Fatalf("source changed: %q err=%v", raw, err)
	}
}

func TestBrowserToolArgumentsAndModelAuditRedactSensitiveValues(t *testing.T) {
	secret := "BROWSER_SECRET_VALUE"
	path := filepath.Join(string(os.PathSeparator), "private", "secret.txt")
	act := &action{
		Tool: "browser.action", BrowserID: "browser_id", TabID: "tab_id",
		Action: json.RawMessage(fmt.Sprintf(`{"kind":"type","ref":"ref","text":%q,"values":[%q],"files":[%q]}`, secret, secret, path)),
	}
	redacted, _ := json.Marshal(redactedToolArguments(act))
	if strings.Contains(string(redacted), secret) || strings.Contains(string(redacted), path) {
		t.Fatalf("redacted arguments leaked sensitive input: %s", redacted)
	}
	raw := fmt.Sprintf(`{"action":{"tool":"browser.action","browser_id":"browser_id","tab_id":"tab_id","action":{"kind":"type","ref":"ref","text":%q,"values":[%q],"files":[%q]}}}`, secret, secret, path)
	audit := sanitizeModelResponseForAudit(raw)
	if strings.Contains(audit, secret) || strings.Contains(audit, path) || !strings.Contains(audit, "[redacted]") {
		t.Fatalf("model audit redaction = %s", audit)
	}
	nativeArgs := json.RawMessage(fmt.Sprintf(`{"browser_id":"browser_id","tab_id":"tab_id","action":%s}`, act.Action))
	native := nativeToolCallsAuditText([]modelrouter.ToolCall{{Name: "browser.action", Arguments: nativeArgs}})
	nativeAudit := sanitizeModelResponseForAudit(native)
	if strings.Contains(nativeAudit, secret) || strings.Contains(nativeAudit, path) || !strings.Contains(nativeAudit, "[redacted]") {
		t.Fatalf("native model audit redaction = %s", nativeAudit)
	}
	if brief := briefAction(act); strings.Contains(brief, secret) || strings.Contains(brief, path) {
		t.Fatalf("action brief leaked sensitive input: %s", brief)
	}
}

func TestBrowserMissingRuntimeReturnsStructuredUnavailable(t *testing.T) {
	outcome := browserToolFailure(&carinabrowser.Error{
		Code: carinabrowser.ErrorUnavailable, Message: "supported browser is not installed", Retryable: false,
	})
	if outcome.status != "failed" || outcome.observationError == nil || outcome.observationError.Category != toolCategoryUnavailable || outcome.observationError.Recovery != toolRecoveryRetry {
		t.Fatalf("unavailable mapping = %+v", outcome)
	}
}

func TestBrowserUploadStagingIsResetOnStartupAndRemovedOnClose(t *testing.T) {
	repoRoot := repoRootFromHere(t)
	kernelBin := firstExistingPath(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	stateDir := t.TempDir()
	uploadRoot := filepath.Join(stateDir, "browser-uploads")
	staleDir := filepath.Join(uploadRoot, "upload-stale")
	if err := os.MkdirAll(staleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "secret.txt"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{
		StateDir: stateDir, KernelBin: kernelBin,
		ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), Offline: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		_ = d.Close()
		t.Fatalf("startup retained stale browser upload staging: %v", err)
	}
	info, err := os.Stat(uploadRoot)
	if err != nil {
		_ = d.Close()
		t.Fatalf("stat startup upload root: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		_ = d.Close()
		t.Fatalf("startup upload root mode = %v", info.Mode())
	}
	activeDir := filepath.Join(uploadRoot, "upload-active")
	if err := os.MkdirAll(activeDir, 0o700); err != nil {
		_ = d.Close()
		t.Fatal(err)
	}
	_ = d.Close()
	if _, err := os.Stat(uploadRoot); !os.IsNotExist(err) {
		t.Fatalf("daemon close retained browser upload staging: %v", err)
	}
}

func TestBrowserNavigationAuthorizesNormalizedOrigin(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	driver := &daemonBrowserDriver{snapshot: carinabrowser.RawSnapshot{TabID: "tab-1", DocumentID: "doc-1"}}
	installDaemonBrowserDriver(t, d, driver)
	if err := d.SetApprovalMode(approvalModeAlwaysApprove); err != nil {
		t.Fatal(err)
	}
	sess, err := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "on_request", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "navigate browser")
	_, opened := d.executeActionOutcome(sess, task, &action{Tool: "browser.open", WaitMode: "managed", Intent: "open browser"})
	var openPayload struct {
		BrowserID string              `json:"browser_id"`
		Tabs      []carinabrowser.Tab `json:"tabs"`
	}
	if opened.status != "completed" || json.Unmarshal([]byte(opened.display), &openPayload) != nil || len(openPayload.Tabs) != 1 {
		t.Fatalf("open = %+v", opened)
	}
	_, navigated := d.executeActionOutcome(sess, task, &action{
		Tool: "browser.open", BrowserID: openPayload.BrowserID, TabID: openPayload.Tabs[0].ID,
		URL: " HTTPS://Example.COM./path?q=1 ", Intent: "navigate",
	})
	if navigated.status != "completed" {
		t.Fatalf("navigate = %+v", navigated)
	}
	session := driver.latest()
	session.mu.Lock()
	origins := append([]string(nil), session.origins...)
	target := session.snapshot.URL
	session.mu.Unlock()
	if len(origins) != 1 || origins[0] != "https://example.com" || target != "https://example.com/path?q=1" {
		t.Fatalf("normalized browser boundary: origins=%v target=%q", origins, target)
	}
	found := false
	for _, event := range readAuditEvents(t, d, sess.SessionID) {
		payload, _ := event["payload"].(map[string]any)
		if payload["capability"] == "NetworkAccess" {
			if payload["resource"] != "example.com" {
				t.Fatalf("NetworkAccess resource was not normalized to the host: %#v", payload)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("navigation did not leave NetworkAccess authorization evidence")
	}
}

func TestBrowserSnapshotCancelsThroughExecutionHandler(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	driver := &daemonBrowserDriver{snapshot: carinabrowser.RawSnapshot{TabID: "tab-1", DocumentID: "doc-1"}}
	installDaemonBrowserDriver(t, d, driver)
	sess, err := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "on_request", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "cancel browser snapshot")
	_, opened := d.executeActionOutcome(sess, task, &action{Tool: "browser.open", WaitMode: "managed", Intent: "open browser"})
	var openPayload struct {
		BrowserID string              `json:"browser_id"`
		Tabs      []carinabrowser.Tab `json:"tabs"`
	}
	if opened.status != "completed" || json.Unmarshal([]byte(opened.display), &openPayload) != nil || len(openPayload.Tabs) != 1 {
		t.Fatalf("open = %+v", opened)
	}
	started := make(chan struct{})
	session := driver.latest()
	session.mu.Lock()
	session.snapshotWait = true
	session.snapshotDone = started
	session.mu.Unlock()
	result := make(chan toolExecutionOutcome, 1)
	go d.withTaskContext(task.RunID, func(context.Context) {
		_, outcome := d.executeActionOutcome(sess, task, &action{
			Tool: "browser.snapshot", BrowserID: openPayload.BrowserID, TabID: openPayload.Tabs[0].ID, Intent: "snapshot",
		})
		result <- outcome
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("browser snapshot did not reach the driver")
	}
	if _, err := d.handleTaskCancel(mustJSON(t, map[string]any{"run_id": task.RunID})); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-result:
		if outcome.status != "cancelled" || outcome.observationError == nil || outcome.observationError.Status != toolObservationCancelled {
			t.Fatalf("cancelled snapshot = %+v", outcome)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("execution cancellation did not stop the browser handler")
	}
	if countEventType(readAuditEvents(t, d, sess.SessionID), "ToolCallCancelled") == 0 {
		t.Fatal("cancelled browser handler has no terminal audit event")
	}
}

func TestBrowserOpenHandlerReportsMissingConfiguredChrome(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	missingChrome := filepath.Join(t.TempDir(), "missing-chrome")
	manager, err := carinabrowser.NewManager(
		carinabrowser.NewChromedpDriver(carinabrowser.ChromedpConfig{ChromePath: missingChrome}),
		carinabrowser.Config{ProfileRoot: filepath.Join(t.TempDir(), "profiles")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if d.browsers != nil {
		_ = d.browsers.CloseAll()
	}
	d.browsers = manager
	sess, err := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "on_request", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "open missing browser")
	_, outcome := d.executeActionOutcome(sess, task, &action{Tool: "browser.open", WaitMode: "managed", Intent: "open browser"})
	if outcome.status != "failed" || outcome.observationError == nil || outcome.observationError.Category != toolCategoryUnavailable || outcome.observationError.Recovery != toolRecoveryRetry {
		t.Fatalf("missing Chrome handler outcome = %+v", outcome)
	}
	if strings.Contains(outcome.display, missingChrome) {
		t.Fatalf("missing Chrome outcome leaked a deployment path: %q", outcome.display)
	}
}
