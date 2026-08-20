package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/provider"
)

const (
	grokCLIEventLineLimit      = maxProviderResponseBytes
	grokCLIEventStreamLimit    = 4 * maxProviderResponseBytes
	grokCLIStderrLimit         = 32 << 10
	grokCLI1MaxRetries         = 15
	grokACPPostResultWindow    = 50 * time.Millisecond
	grokCLIReasonerTimeout     = 180 * time.Second
	grokCLIReasonerHighTimeout = 360 * time.Second

	grokACPSystemPrompt = "You are Carina. Follow the JSON ReAct contract in the request and reply with only the next JSON action (or done). Grok vendor tools, skills, and MCP are disabled; JSON is the only action channel. The request is an instruction to execute, not non-instructional data."
	grokACPPromptPrefix = "Carina JSON ReAct request (not a Grok CLI command):\n\n"
)

var grokACPBaselineCommands = []string{"always-approve", "compact", "context", "session-info"}

var (
	errGrokModelsCacheStale      = errors.New("Grok Build model cache is stale")
	errGrokModelsCacheUnreadable = errors.New("Grok Build model cache is unreadable")
)

func grokModelsCacheOptional(err error) bool {
	return errors.Is(err, errGrokModelsCacheStale) || errors.Is(err, errGrokModelsCacheUnreadable)
}

type grokCLIReasoner struct {
	bin           string
	version       string
	isolationRoot string
	workdir       string
	grokHome      string
	timeout       time.Duration
}

type grokCLIError struct {
	message      string
	kind         string
	upstreamKind string
	salvage      string
}

func (e grokCLIError) Error() string {
	if message := boundedMetadata(e.message, 500); message != "" {
		return "grok build reasoner: " + message
	}
	return "grok build reasoner failed"
}

func salvageGrokJSONFallback(err error) (ReasonerResult, bool) {
	var ge grokCLIError
	if err == nil || !errors.As(err, &ge) || ge.kind != "json_fallback" {
		return ReasonerResult{}, false
	}
	text := strings.TrimSpace(ge.salvage)
	if text == "" {
		return ReasonerResult{}, false
	}
	return ReasonerResult{Text: text}, true
}

func grokNativeToolRejected(err error) bool {
	info := classifyProviderError(err)
	if info.Code == "provider_native_tools_rejected" {
		return true
	}
	var ge grokCLIError
	return errors.As(err, &ge) && ge.kind == "json_fallback"
}

func (e grokCLIError) ProviderError() providerErrorInfo {
	switch e.kind {
	case "safety":
		return providerErrorInfo{Code: "reasoner_safety_violation", Category: "internal", Provider: provider.GrokBuildProviderID, UserAction: "choose another model, or see Details"}
	case "protocol":
		return providerErrorInfo{Code: "reasoner_protocol_error", Category: "internal", Provider: provider.GrokBuildProviderID, UserAction: "choose another model, or see Details"}
	case "json_fallback":
		return providerErrorInfo{Code: "provider_native_tools_rejected", Category: "compatibility", Provider: provider.GrokBuildProviderID, UserAction: "retry; the model must reply with JSON instead of calling tools"}
	}
	message := strings.ToLower(e.message)
	switch {
	case strings.Contains(message, "not logged in"), strings.Contains(message, "not signed in"), strings.Contains(message, "grok login"), strings.Contains(message, "unauthorized"), strings.Contains(message, "authentication"):
		return providerErrorInfo{Code: "provider_authentication_failed", Category: "authentication", Provider: provider.GrokBuildProviderID, UserAction: "run `grok login`, then refresh Providers"}
	case strings.Contains(message, "rate limit"), strings.Contains(message, "too many requests"):
		return providerErrorInfo{Code: "provider_rate_limited", Category: "rate_limit", Provider: provider.GrokBuildProviderID, Retryable: true, UserAction: "wait or choose another provider"}
	case strings.Contains(message, "quota"), strings.Contains(message, "usage limit"), strings.Contains(message, "subscription"), strings.Contains(message, "billing"):
		return providerErrorInfo{Code: "provider_quota_exhausted", Category: "rate_limit", Provider: provider.GrokBuildProviderID, UserAction: "check your Grok Build subscription or choose another provider"}
	case strings.Contains(message, "temporarily unavailable"), strings.Contains(message, "overloaded"), strings.Contains(message, "service unavailable"):
		return providerErrorInfo{Code: "provider_unavailable", Category: "unavailable", Provider: provider.GrokBuildProviderID, Retryable: true, UserAction: "retry or choose another provider"}
	default:
		return providerErrorInfo{Code: "reasoner_internal_error", Category: "internal", Provider: provider.GrokBuildProviderID, UserAction: "run `grok doctor`, then refresh Providers"}
	}
}

func newGrokCLIReasoner(binary string) (*grokCLIReasoner, error) {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		var err error
		binary, err = exec.LookPath("grok")
		if err != nil {
			return nil, fmt.Errorf("Grok Build CLI not found: %w", err)
		}
	}
	root, err := os.MkdirTemp("", "carina-grok-reasoner-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return &grokCLIReasoner{bin: binary, isolationRoot: root, timeout: grokCLIReasonerTimeout}, nil
}

func grokThinkTimeout(base time.Duration, effort string) time.Duration {
	if base <= 0 {
		base = grokCLIReasonerTimeout
	}
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "high", "xhigh", "max":
		if base >= grokCLIReasonerTimeout {
			return grokCLIReasonerHighTimeout
		}
	}
	return base
}

func (r *grokCLIReasoner) ThinkRoutedModel(ctx context.Context, model, prompt string) (ReasonerResult, error) {
	model = strings.TrimSpace(model)
	if model == "" || strings.TrimSpace(prompt) == "" {
		return ReasonerResult{}, grokCLIError{message: "model and prompt are required", kind: "protocol"}
	}
	if len(prompt) > maxProviderResponseBytes {
		return ReasonerResult{}, grokCLIError{message: "prompt exceeds size limit", kind: "protocol"}
	}

	callCtx, cancel := context.WithTimeout(ctx, grokThinkTimeout(r.timeout, reasoningEffortFrom(ctx)))
	defer cancel()

	callRoot, workdir, grokHome, err := r.newIsolation()
	if err != nil {
		return ReasonerResult{}, grokCLIError{message: "create isolated Grok Build workspace", kind: "protocol"}
	}
	if callRoot != "" {
		defer os.RemoveAll(callRoot)
	}

	authPath, err := canonicalGrokAuthPath(grokAuthPathFromEnvironment(os.Environ()))
	if err != nil {
		return ReasonerResult{}, grokCLIError{message: "Grok Build OAuth session is unavailable; run `grok login`"}
	}
	configPath, err := prepareGrokIsolationForVersion(grokHome, authPath, r.version)
	if err != nil {
		detail := boundedMetadata(err.Error(), 180)
		if detail == "" {
			detail = "configure isolated Grok Build runtime"
		}
		return ReasonerResult{}, grokCLIError{message: detail, kind: "protocol"}
	}
	env := grokCLIEnvironment(os.Environ(), grokHome, authPath)
	if err := r.verifyPureInferenceSurface(callCtx, env, workdir, configPath); err != nil {
		return ReasonerResult{}, err
	}

	cmd := exec.CommandContext(callCtx, r.bin, r.args(callCtx, model)...)
	configureCLIReasonerCommand(cmd)
	cmd.Dir = workdir
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ReasonerResult{}, grokCLIError{message: "open Grok Build ACP input", kind: "protocol"}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ReasonerResult{}, grokCLIError{message: "open Grok Build ACP output", kind: "protocol"}
	}
	stderr := &boundedCLIWriter{limit: grokCLIStderrLimit}
	cmd.Stderr = stderr
	if err := startGrokCLIReasonerCommand(cmd); err != nil {
		return ReasonerResult{}, grokCLIError{message: "start Grok Build CLI"}
	}
	defer releaseGrokCLIReasonerCommand(cmd)

	publicStream := reasonerStreamFrom(callCtx)
	var publicDecoder actionEnvelopeStreamDecoder
	requestedEffort := reasoningEffortFrom(callCtx)
	client := newGrokACPClient(stdout, stdin, model, workdir, func(delta string) {
		if publicStream != nil {
			publicStream.emit(publicDecoder.Push(delta))
		}
	})
	client.requestedEffort = requestedEffort
	result, runErr := client.run(callCtx, prompt)
	_ = stdin.Close()
	killErr := terminateGrokCLIReasonerCommand(cmd)
	waitErr := cmd.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		resetReasonerStream(publicStream)
		return ReasonerResult{}, ctxErr
	}
	if callCtx.Err() != nil {
		resetReasonerStream(publicStream)
		return ReasonerResult{}, callCtx.Err()
	}
	if salvaged, ok := salvageGrokJSONFallback(runErr); ok {
		salvaged.Usage.EffectiveReasoningEffort = client.effectiveEffort
		if salvaged.Usage.EffectiveReasoningEffort == "" {
			salvaged.Usage.EffectiveReasoningEffort = requestedEffort
		}
		return salvaged, nil
	}
	if runErr != nil {
		resetReasonerStream(publicStream)
		if _, ok := runErr.(grokCLIError); !ok {
			if safe := classifySafeGrokStderr(stderr.String()); safe != "" {
				return ReasonerResult{}, grokCLIError{message: safe}
			}
		}
		return ReasonerResult{}, runErr
	}
	if waitErr != nil && !grokProcessWasKilledByCarina(killErr, waitErr) {
		resetReasonerStream(publicStream)
		if safe := classifySafeGrokStderr(stderr.String()); safe != "" {
			return ReasonerResult{}, grokCLIError{message: safe}
		}
		return ReasonerResult{}, grokCLIError{message: "Grok Build CLI exited unsuccessfully"}
	}
	result.Usage.EffectiveReasoningEffort = client.effectiveEffort
	if result.Usage.EffectiveReasoningEffort == "" {
		result.Usage.EffectiveReasoningEffort = requestedEffort
	}
	return result, nil
}

func (r *grokCLIReasoner) newIsolation() (root, workdir, grokHome string, err error) {
	if r.workdir != "" && r.grokHome != "" {
		if err = os.MkdirAll(r.workdir, 0o700); err != nil {
			return "", "", "", err
		}
		if err = os.MkdirAll(r.grokHome, 0o700); err != nil {
			return "", "", "", err
		}
		return "", r.workdir, r.grokHome, nil
	}
	base := r.isolationRoot
	if base == "" {
		base, err = os.MkdirTemp("", "carina-grok-reasoner-")
		if err != nil {
			return "", "", "", err
		}
		defer func() {
			if err != nil {
				_ = os.RemoveAll(base)
			}
		}()
	}
	root, err = os.MkdirTemp(base, "call-")
	if err != nil {
		return "", "", "", err
	}
	workdir = filepath.Join(root, "cwd")
	grokHome = filepath.Join(root, "home")
	if err = os.Mkdir(workdir, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return "", "", "", err
	}
	if err = os.Mkdir(grokHome, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return "", "", "", err
	}
	return root, workdir, grokHome, nil
}

func (r *grokCLIReasoner) args(ctx context.Context, model string) []string {
	args := []string{"agent", "--no-leader", "--model", strings.TrimSpace(model)}
	if effort := reasoningEffortFrom(ctx); effort != "" {
		args = append(args, "--reasoning-effort", effort)
	}
	return append(args, "stdio")
}

func prepareGrokIsolation(isolatedHome, authPath string) (string, error) {
	return prepareGrokIsolationForVersion(isolatedHome, authPath, "")
}

func prepareGrokIsolationForVersion(isolatedHome, authPath, expectedVersion string) (string, error) {
	if err := os.MkdirAll(isolatedHome, 0o700); err != nil {
		return "", err
	}
	if err := writeGrokSandboxConfig(isolatedHome, authPath); err != nil {
		return "", err
	}
	configPath := filepath.Join(isolatedHome, "config.toml")
	config := []byte("[cli]\nauto_update = false\nuse_leader = false\n\n[features]\nremote_fetch = false\nmanaged_config = false\ntelemetry = false\nfeedback = false\nlsp_tools = false\nweb_fetch = false\nask_user_question = false\nsession_recap = false\nvoice_mode = false\nimage_gen = false\nvideo_gen = false\n\n[managed_mcps]\nenabled = false\ngateway_tools_enabled = false\n")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(configPath, 0o600); err != nil {
		return "", err
	}
	if err := copySanitizedGrokModelsCacheForVersion(isolatedHome, authPath, expectedVersion); err != nil {
		if !grokModelsCacheOptional(err) {
			return "", err
		}
	}
	bundleDir := filepath.Join(isolatedHome, "bundled")
	if err := os.MkdirAll(bundleDir, 0o700); err != nil {
		return "", err
	}
	manifestPath := filepath.Join(bundleDir, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte("{\"version\":\"carina-isolated\",\"checksums\":{}}\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(manifestPath, 0o600); err != nil {
		return "", err
	}
	return configPath, nil
}

func copySanitizedGrokModelsCache(isolatedHome, authPath string) error {
	return copySanitizedGrokModelsCacheForVersion(isolatedHome, authPath, "")
}

func copySanitizedGrokModelsCacheForVersion(isolatedHome, authPath, expectedVersion string) error {
	sourcePath := filepath.Join(filepath.Dir(authPath), "models_cache.json")
	file, err := os.Open(sourcePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	const cacheLimit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(file, cacheLimit+1))
	if err != nil || len(raw) > cacheLimit {
		return errGrokModelsCacheUnreadable
	}
	var cache map[string]json.RawMessage
	if json.Unmarshal(raw, &cache) != nil {
		return errors.New("Grok Build model cache is invalid")
	}
	if !grokACPObjectShape(cache,
		[]string{"fetched_at", "grok_version", "auth_method", "origin", "models"},
		[]string{"etag"}) {
		return errors.New("Grok Build model cache has an unknown field")
	}
	var authMethod, version, fetchedAt, origin string
	if json.Unmarshal(cache["auth_method"], &authMethod) != nil || authMethod != "session" ||
		json.Unmarshal(cache["grok_version"], &version) != nil || strings.TrimSpace(version) == "" {
		return errors.New("Grok Build model cache is not OAuth-scoped")
	}
	if expectedVersion != "" && version != expectedVersion {
		return errors.New("Grok Build model cache version does not match the running CLI")
	}
	if json.Unmarshal(cache["fetched_at"], &fetchedAt) != nil {
		return errors.New("Grok Build model cache is stale")
	}
	fetchedTime, parseErr := time.Parse(time.RFC3339Nano, fetchedAt)
	now := time.Now().UTC()
	if parseErr != nil || fetchedTime.After(now) || now.Sub(fetchedTime) >= 5*time.Minute {
		return errGrokModelsCacheStale
	}
	if json.Unmarshal(cache["origin"], &origin) != nil || origin != "https://cli-chat-proxy.grok.com/v1/models" {
		return errors.New("Grok Build model cache has an unexpected origin")
	}
	var models map[string]json.RawMessage
	if json.Unmarshal(cache["models"], &models) != nil || len(models) == 0 {
		return errors.New("Grok Build model cache has no models")
	}
	for id, rawEntry := range models {
		if !providerModelIDSafe(id) {
			return errors.New("Grok Build model cache has an invalid model")
		}
		var entry map[string]json.RawMessage
		if json.Unmarshal(rawEntry, &entry) != nil || !grokACPObjectKeyShape(entry,
			[]string{"api_base_url", "api_key", "env_key", "info"}, []string{"auth_provider"}) ||
			rawJSONPresent(entry["api_base_url"]) || rawJSONPresent(entry["api_key"]) || rawJSONPresent(entry["env_key"]) {
			return errors.New("Grok Build model cache has an invalid model route")
		}
		if authProvider, exists := entry["auth_provider"]; exists && rawJSONPresent(authProvider) {
			return errors.New("Grok Build model cache has an invalid auth provider")
		}
		var info map[string]json.RawMessage
		if json.Unmarshal(entry["info"], &info) != nil || !grokBuildModelInfoOfficial(info, id) || grokCacheContainsCredential(info) {
			return errors.New("Grok Build model cache contains a non-session model")
		}
	}
	cache["fetched_at"], err = json.Marshal(now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	sanitized, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	destination := filepath.Join(isolatedHome, "models_cache.json")
	if err := os.WriteFile(destination, sanitized, 0o600); err != nil {
		return err
	}
	return os.Chmod(destination, 0o600)
}

func providerModelIDSafe(id string) bool {
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\") {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("._:-", r) {
			return false
		}
	}
	return true
}

func grokBuildModelInfoOfficial(info map[string]json.RawMessage, modelID string) bool {
	if !grokACPObjectKeyShape(info, nil, []string{
		"id", "model", "base_url", "name", "description", "max_completion_tokens", "temperature", "top_p",
		"api_backend", "auth_scheme", "extra_headers", "query_params", "env_http_headers", "context_window",
		"auto_compact_threshold_percent", "system_prompt_label", "use_concise", "agent_type",
		"inference_idle_timeout_secs", "max_retries", "hidden", "supported_in_api", "reasoning_effort",
		"supports_reasoning_effort", "reasoning_efforts", "supports_backend_search", "compactions_remaining",
		"compaction_at_tokens", "show_model_fingerprint", "stream_tool_calls", "laziness_detector",
	}) {
		return false
	}
	var baseURL, authScheme, backend, id, model string
	if json.Unmarshal(info["base_url"], &baseURL) != nil || baseURL != "https://cli-chat-proxy.grok.com/v1" ||
		json.Unmarshal(info["auth_scheme"], &authScheme) != nil || authScheme != "bearer" ||
		json.Unmarshal(info["api_backend"], &backend) != nil || backend != "responses" ||
		json.Unmarshal(info["id"], &id) != nil || id != modelID ||
		json.Unmarshal(info["model"], &model) != nil || model != modelID {
		return false
	}
	for _, field := range []string{"extra_headers", "query_params", "env_http_headers"} {
		if raw, exists := info[field]; exists {
			var values map[string]json.RawMessage
			if json.Unmarshal(raw, &values) != nil || len(values) != 0 {
				return false
			}
		}
	}
	return true
}

func grokCacheContainsCredential(value any) bool {
	switch typed := value.(type) {
	case map[string]json.RawMessage:
		for key, raw := range typed {
			var child any
			if json.Unmarshal(raw, &child) != nil {
				return true
			}
			if grokCacheSecretKey(key) && grokCacheValuePresent(child) || grokCacheContainsCredential(child) {
				return true
			}
		}
	case map[string]any:
		for key, child := range typed {
			if grokCacheSecretKey(key) && grokCacheValuePresent(child) || grokCacheContainsCredential(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if grokCacheContainsCredential(child) {
				return true
			}
		}
	}
	return false
}

func grokCacheSecretKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	switch normalized {
	case "api_key", "env_key", "access_token", "refresh_token", "bearer_token", "auth_token", "session_token", "token", "authorization", "cookie", "password", "credential", "secret", "client_secret", "private_key":
		return true
	default:
		return false
	}
}

func grokCacheValuePresent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) != 0
	case map[string]any:
		return len(typed) != 0
	default:
		return true
	}
}

func writeGrokSandboxConfig(isolatedHome, authPath string) error {
	readWrite, err := grokAuthWritableDirectory(authPath)
	if err != nil {
		return err
	}
	quoted, err := json.Marshal(readWrite)
	if err != nil {
		return err
	}
	config := []byte("[profiles.carina-pure-inference]\nextends = \"strict\"\nrestrict_network = true\nread_write = [" + string(quoted) + "]\n")
	path := filepath.Join(isolatedHome, "sandbox.toml")
	if err := os.WriteFile(path, config, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func canonicalGrokAuthPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("missing auth path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(abs); err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("auth path is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("auth file permissions are not owner-only")
	}
	if _, err := grokAuthWritableDirectory(canonical); err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

func grokAuthWritableDirectory(authPath string) (string, error) {
	authPath = strings.TrimSpace(authPath)
	if authPath == "" {
		return "", errors.New("Grok Build OAuth path is unavailable")
	}
	parent := filepath.Dir(filepath.Clean(authPath))
	if grokAuthDirectoryTooBroad(parent) {
		return "", errors.New("auth directory is too broad")
	}
	return parent, nil
}

func grokAuthDirectoryTooBroad(path string) bool {
	return grokAuthDirectoryTooBroadForOS(path, runtime.GOOS)
}

func grokAuthDirectoryTooBroadForOS(path, goos string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return true
	}
	if goos == "windows" {
		return grokWindowsAuthDirectoryTooBroad(path)
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return true
	}
	volume := filepath.VolumeName(clean)
	remainder := strings.TrimPrefix(clean, volume)
	return remainder == "" || remainder == string(filepath.Separator)
}

func grokWindowsAuthDirectoryTooBroad(path string) bool {
	normalized := strings.ReplaceAll(strings.TrimSpace(path), "/", `\`)
	trimmed := strings.TrimRight(normalized, `\`)
	if trimmed == "" || trimmed == "." {
		return true
	}
	if len(trimmed) >= 2 && trimmed[1] == ':' {
		if len(trimmed) == 2 {
			return true
		}
		return len(normalized) < 3 || normalized[2] != '\\'
	}
	if !strings.HasPrefix(normalized, `\\`) {
		return true
	}
	parts := strings.FieldsFunc(strings.Trim(normalized, `\`), func(r rune) bool { return r == '\\' })
	if len(parts) < 2 {
		return true
	}
	if parts[0] == "?" || parts[0] == "." {
		if len(parts) >= 2 && strings.EqualFold(parts[1], "UNC") {
			return len(parts) <= 4
		}
		return len(parts) <= 2
	}
	return len(parts) <= 2
}

func grokAuthPathFromEnvironment(env []string) string {
	return grokAuthPathFromEnvironmentForOS(env, runtime.GOOS)
}

func grokAuthPathFromEnvironmentForOS(env []string, goos string) string {
	values := map[string]string{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			if goos == "windows" {
				key = strings.ToUpper(key)
			}
			values[key] = value
		}
	}
	path := strings.TrimSpace(values["GROK_AUTH_PATH"])
	if path == "" {
		if home := strings.TrimSpace(values["GROK_HOME"]); home != "" {
			path = filepath.Join(home, "auth.json")
		} else {
			home := strings.TrimSpace(values["HOME"])
			if goos == "windows" {
				home = strings.TrimSpace(values["USERPROFILE"])
				if home == "" {
					home = strings.TrimSpace(values["HOMEDRIVE"] + values["HOMEPATH"])
				}
				if home == "" {
					home = strings.TrimSpace(values["HOME"])
				}
			}
			if home != "" {
				path = filepath.Join(home, ".grok", "auth.json")
			}
		}
	}
	return path
}

func grokCLIEnvironment(env []string, isolatedHome, authPath string) []string {
	return grokCLIEnvironmentForOS(env, isolatedHome, authPath, runtime.GOOS)
}

func grokCLIEnvironmentForOS(env []string, isolatedHome, authPath, goos string) []string {
	allowed := map[string]bool{
		"PATH": true, "TMPDIR": true, "TMP": true, "TEMP": true,
		"LANG": true, "LC_ALL": true, "LC_CTYPE": true,
	}
	if goos == "windows" {
		allowed["SYSTEMROOT"] = true
		allowed["WINDIR"] = true
		allowed["PATHEXT"] = true
	}
	out := make([]string, 0, len(allowed)+48)
	hasPath := false
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if provider.IsSafeGrokBuildProxyKey(key) {
			if provider.IsSafeGrokBuildLoopbackProxy(value) {
				out = append(out, entry)
			}
			continue
		}
		lookupKey := key
		if goos == "windows" {
			lookupKey = strings.ToUpper(key)
		}
		if !allowed[lookupKey] {
			continue
		}
		hasPath = hasPath || lookupKey == "PATH"
		out = append(out, entry)
	}
	if !hasPath && goos != "windows" {
		out = append(out, "PATH=/usr/bin:/bin")
	}
	if goos == "windows" {
		out = append(out, "USERPROFILE="+isolatedHome)
	}
	out = append(out,
		"HOME="+isolatedHome,
		"GROK_HOME="+isolatedHome,
		"GROK_AUTH_PATH="+authPath,
		"GROK_SANDBOX=carina-pure-inference",
		"GROK_DISABLE_AUTOUPDATER=1",
		"GROK_DISABLE_API_KEY_AUTH=1",
		"GROK_MAX_RETRIES=3",
		"GROK_MANAGED_CONFIG=0",
		"GROK_MANAGED_MCPS_ENABLED=0",
		"GROK_MANAGED_MCP_GATEWAY_TOOLS_ENABLED=0",
		"GROK_TELEMETRY_ENABLED=0",
		"GROK_FEEDBACK_ENABLED=0",
		"GROK_SUBAGENTS=0",
		"GROK_GOAL=0",
		"GROK_WORKFLOWS=0",
		"GROK_EXPERIMENTAL_MEMORY=0",
		"GROK_WEB_FETCH_ENABLED=0",
		"GROK_LSP_TOOLS_ENABLED=0",
		"GROK_ASK_USER_QUESTION_ENABLED=0",
		"GROK_SESSION_RECAP_ENABLED=0",
		"GROK_VOICE_MODE=0",
		"GROK_SCHEDULER_BACKGROUND_LOOPS=0",
		"GROK_MARKETPLACE_AUTO_REGISTER=0",
		"GROK_ERROR_REPORTING_ENABLED=0",
		"GROK_TRACE_UPLOAD_ENABLED=0",
		"OTEL_SDK_DISABLED=true",
		"OTEL_TRACES_EXPORTER=none",
		"OTEL_METRICS_EXPORTER=none",
		"OTEL_LOGS_EXPORTER=none",
		"NO_COLOR=1",
		"GROK_CURSOR_SKILLS_ENABLED=0",
		"GROK_CURSOR_RULES_ENABLED=0",
		"GROK_CURSOR_AGENTS_ENABLED=0",
		"GROK_CURSOR_MCPS_ENABLED=0",
		"GROK_CURSOR_HOOKS_ENABLED=0",
		"GROK_CURSOR_SESSIONS_ENABLED=0",
		"GROK_CLAUDE_SKILLS_ENABLED=0",
		"GROK_CLAUDE_RULES_ENABLED=0",
		"GROK_CLAUDE_AGENTS_ENABLED=0",
		"GROK_CLAUDE_MCPS_ENABLED=0",
		"GROK_CLAUDE_HOOKS_ENABLED=0",
		"GROK_CLAUDE_SESSIONS_ENABLED=0",
		"GROK_CODEX_SKILLS_ENABLED=0",
		"GROK_CODEX_RULES_ENABLED=0",
		"GROK_CODEX_AGENTS_ENABLED=0",
		"GROK_CODEX_MCPS_ENABLED=0",
		"GROK_CODEX_HOOKS_ENABLED=0",
		"GROK_CODEX_SESSIONS_ENABLED=0",
	)
	return out
}

type grokCLIInspectReport struct {
	GrokVersion         string          `json:"grokVersion"`
	Channel             string          `json:"channel"`
	CWD                 string          `json:"cwd"`
	ProjectRoot         json.RawMessage `json:"projectRoot"`
	ProjectTrusted      *bool           `json:"projectTrusted"`
	ProjectInstructions json.RawMessage `json:"projectInstructions"`
	Permissions         json.RawMessage `json:"permissions"`
	LoginPolicy         json.RawMessage `json:"loginPolicy"`
	Hooks               json.RawMessage `json:"hooks"`
	Skills              json.RawMessage `json:"skills"`
	Agents              json.RawMessage `json:"agents"`
	Plugins             json.RawMessage `json:"plugins"`
	Marketplaces        json.RawMessage `json:"marketplaces"`
	MCPServers          json.RawMessage `json:"mcpServers"`
	LSPServers          json.RawMessage `json:"lspServers"`
	Config              json.RawMessage `json:"configSources"`
	ExternalCompat      json.RawMessage `json:"externalCompat"`
	ConfigWarnings      json.RawMessage `json:"configWarnings"`
	MCPConfigProblems   json.RawMessage `json:"mcpConfigProblems"`
}

func decodeGrokCLIInspectReport(raw []byte) (grokCLIInspectReport, bool) {
	object, ok := decodeGrokACPExactObject(raw)
	if !ok || !grokACPObjectKeyShape(object, []string{
		"grokVersion", "channel", "cwd", "projectRoot", "projectTrusted", "projectInstructions",
		"permissions", "loginPolicy", "hooks", "skills", "agents", "plugins", "marketplaces",
		"mcpServers", "lspServers", "configSources", "externalCompat",
	}, []string{"configWarnings", "mcpConfigProblems"}) {
		return grokCLIInspectReport{}, false
	}
	var report grokCLIInspectReport
	return report, json.Unmarshal(raw, &report) == nil
}

func (r *grokCLIReasoner) verifyPureInferenceSurface(ctx context.Context, env []string, workdir, expectedConfig string) error {
	cmd := exec.CommandContext(ctx, r.bin, "--cwd", workdir, "inspect", "--json")
	configureCLIReasonerCommand(cmd)
	cmd.Dir = workdir
	cmd.Env = env
	stdout := &boundedCLIWriter{limit: grokCLIEventStreamLimit}
	stderr := &boundedCLIWriter{limit: grokCLIStderrLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := startGrokCLIReasonerCommand(cmd); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return grokCLIError{message: "start isolated Grok Build inspection", kind: "protocol"}
	}
	defer releaseGrokCLIReasonerCommand(cmd)
	if err := cmd.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return grokCLIError{message: "inspect isolated Grok Build configuration", kind: "protocol"}
	}
	if len(stdout.data) >= grokCLIEventStreamLimit || len(stderr.data) >= grokCLIStderrLimit {
		return grokCLIError{message: "isolated Grok Build inspection exceeds size limit", kind: "protocol"}
	}
	report, ok := decodeGrokCLIInspectReport([]byte(stdout.String()))
	if !ok {
		return grokCLIError{message: "decode isolated Grok Build inspection", kind: "protocol"}
	}
	if strings.TrimSpace(report.GrokVersion) == "" || len(report.GrokVersion) > 100 ||
		strings.TrimSpace(report.Channel) == "" || len(report.Channel) > 100 ||
		r.version != "" && report.GrokVersion != r.version {
		return grokCLIError{message: "isolated Grok Build inspection reported an incompatible version", kind: "protocol"}
	}
	if cleanPath(report.CWD) != cleanPath(workdir) {
		return grokCLIError{message: "isolated Grok Build inspection used an unexpected directory", kind: "safety"}
	}
	if !bytes.Equal(bytes.TrimSpace(report.ProjectRoot), []byte("null")) || report.ProjectTrusted == nil {
		return grokCLIError{message: "isolated Grok Build inspection discovered a project", kind: "safety"}
	}
	for _, field := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"project instructions", report.ProjectInstructions}, {"hooks", report.Hooks},
		{"skills", report.Skills}, {"plugins", report.Plugins},
		{"marketplaces", report.Marketplaces}, {"MCP servers", report.MCPServers}, {"LSP servers", report.LSPServers},
	} {
		if !rawJSONArrayEmpty(field.raw) {
			return grokCLIError{message: "isolated Grok Build configuration contains " + field.name, kind: "safety"}
		}
	}
	if !inspectAgentsBuiltinOnly(report.Agents) {
		return grokCLIError{message: "isolated Grok Build configuration contains external agents", kind: "safety"}
	}
	if len(report.ConfigWarnings) > 0 && !rawJSONArrayEmpty(report.ConfigWarnings) {
		return grokCLIError{message: "isolated Grok Build configuration warning for " + inspectWarningTarget(report.ConfigWarnings), kind: "safety"}
	}
	if len(report.MCPConfigProblems) > 0 && !rawJSONArrayEmpty(report.MCPConfigProblems) {
		return grokCLIError{message: "isolated Grok Build configuration contains invalid MCP configuration", kind: "safety"}
	}
	if !inspectConfigLayersCarinaOnly(report.Config, expectedConfig) {
		return grokCLIError{message: "isolated Grok Build configuration contains external layers", kind: "safety"}
	}
	if !inspectPermissionsEmpty(report.Permissions) {
		return grokCLIError{message: "isolated Grok Build configuration contains permission policy", kind: "safety"}
	}
	if !inspectOAuthOnly(report.LoginPolicy) {
		return grokCLIError{message: "Grok Build API-key authentication is not disabled", kind: "safety"}
	}
	if !inspectExternalCompatDisabled(report.ExternalCompat) {
		return grokCLIError{message: "isolated Grok Build compatibility surfaces are enabled", kind: "safety"}
	}
	return nil
}

func rawJSONArrayEmpty(raw json.RawMessage) bool {
	var entries []json.RawMessage
	return json.Unmarshal(raw, &entries) == nil && entries != nil && len(entries) == 0
}

func inspectAgentsBuiltinOnly(raw json.RawMessage) bool {
	var agents []json.RawMessage
	if json.Unmarshal(raw, &agents) != nil || agents == nil {
		return false
	}
	seen := make(map[string]bool, len(agents))
	for _, rawAgent := range agents {
		agent, ok := decodeGrokACPExactObject(rawAgent)
		if !ok || !grokACPObjectKeysExact(agent, "name", "description", "source") {
			return false
		}
		var name, description string
		if json.Unmarshal(agent["name"], &name) != nil || strings.TrimSpace(name) == "" || len(name) > 200 ||
			json.Unmarshal(agent["description"], &description) != nil || len(description) > 4000 || seen[name] {
			return false
		}
		source, ok := decodeGrokACPExactObject(agent["source"])
		if !ok || !grokACPObjectKeysExact(source, "type") {
			return false
		}
		var sourceType string
		if json.Unmarshal(source["type"], &sourceType) != nil || sourceType != "builtin" {
			return false
		}
		seen[name] = true
	}
	return true
}

func inspectWarningTarget(raw json.RawMessage) string {
	var warnings []struct {
		Target string `json:"target"`
		Path   string `json:"path"`
		Field  string `json:"field"`
	}
	if json.Unmarshal(raw, &warnings) != nil || len(warnings) == 0 {
		return "unknown field"
	}
	target := strings.TrimSpace(warnings[0].Path)
	if target == "" {
		target = strings.TrimSpace(warnings[0].Field)
	}
	if target == "" {
		target = strings.TrimSpace(warnings[0].Target)
	}
	return nonempty(boundedMetadata(target, 100), "unknown field")
}

func inspectConfigLayersCarinaOnly(raw json.RawMessage, expectedConfig string) bool {
	config, ok := decodeGrokACPExactObject(raw)
	if !ok || !grokACPObjectKeysExact(config, "layers") {
		return false
	}
	var layers []json.RawMessage
	if json.Unmarshal(config["layers"], &layers) != nil || len(layers) != 1 {
		return false
	}
	layer, ok := decodeGrokACPExactObject(layers[0])
	if !ok || !grokACPObjectKeysExact(layer, "role", "path") {
		return false
	}
	var role, path string
	return json.Unmarshal(layer["role"], &role) == nil && role == "user" &&
		json.Unmarshal(layer["path"], &path) == nil && cleanPath(path) == cleanPath(expectedConfig)
}

func inspectPermissionsEmpty(raw json.RawMessage) bool {
	p, ok := decodeGrokACPExactObject(raw)
	if !ok || !grokACPObjectKeyShape(p, []string{
		"sources", "loaded", "skipped", "mcpServerAllowlist", "marketplaceAllowlist",
		"managedSettingsExists", "managedSettingsActive",
	}, []string{"managedSettingsPath", "enforced"}) {
		return false
	}
	var loaded int
	var exists, active bool
	if json.Unmarshal(p["loaded"], &loaded) != nil || loaded != 0 ||
		json.Unmarshal(p["managedSettingsExists"], &exists) != nil || exists ||
		json.Unmarshal(p["managedSettingsActive"], &active) != nil || active {
		return false
	}
	for _, field := range []string{"sources", "skipped", "mcpServerAllowlist", "marketplaceAllowlist"} {
		if !rawJSONArrayEmpty(p[field]) {
			return false
		}
	}
	if pathRaw, present := p["managedSettingsPath"]; present {
		var path string
		if json.Unmarshal(pathRaw, &path) != nil || len(path) > 4096 || !filepath.IsAbs(path) {
			return false
		}
	}
	if enforced, present := p["enforced"]; present && !rawJSONArrayEmpty(enforced) {
		return false
	}
	return true
}

func inspectOAuthOnly(raw json.RawMessage) bool {
	policy, ok := decodeGrokACPExactObject(raw)
	if !ok || !grokACPObjectKeysExact(policy, "disableApiKeyAuth", "forceLoginTeamUuid", "apiKeyAuthDisabled") ||
		!bytes.Equal(bytes.TrimSpace(policy["forceLoginTeamUuid"]), []byte("null")) {
		return false
	}
	var configured, resolved bool
	return json.Unmarshal(policy["disableApiKeyAuth"], &configured) == nil && configured &&
		json.Unmarshal(policy["apiKeyAuthDisabled"], &resolved) == nil && resolved
}

func inspectExternalCompatDisabled(raw json.RawMessage) bool {
	report, ok := decodeGrokACPExactObject(raw)
	if !ok || !grokACPObjectKeysExact(report, "remoteSettingsLoaded", "cells") {
		return false
	}
	var remoteLoaded bool
	var cells []json.RawMessage
	if json.Unmarshal(report["remoteSettingsLoaded"], &remoteLoaded) != nil || remoteLoaded ||
		json.Unmarshal(report["cells"], &cells) != nil || len(cells) == 0 {
		return false
	}
	seen := make(map[string]bool, len(cells))
	for _, rawCell := range cells {
		cell, ok := decodeGrokACPExactObject(rawCell)
		if !ok || !grokACPObjectKeysExact(cell, "vendor", "surface", "enabled", "source") {
			return false
		}
		var vendor, surface, source string
		var enabled bool
		if json.Unmarshal(cell["vendor"], &vendor) != nil || strings.TrimSpace(vendor) == "" || len(vendor) > 100 ||
			json.Unmarshal(cell["surface"], &surface) != nil || strings.TrimSpace(surface) == "" || len(surface) > 100 ||
			json.Unmarshal(cell["source"], &source) != nil || !grokACPCompatSourceValid(source) ||
			json.Unmarshal(cell["enabled"], &enabled) != nil || enabled {
			return false
		}
		key := vendor + "\x00" + surface
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func grokACPCompatSourceValid(source string) bool {
	switch source {
	case "env", "config", "configError", "default":
		return true
	default:
		return false
	}
}

type grokACPPhase int

const (
	grokACPPreflight grokACPPhase = iota
	grokACPPrompt
)

type grokACPResponseRail int

const (
	grokACPResponseRailUnknown grokACPResponseRail = iota
	grokACPResponseRailMessages
	grokACPResponseRailResponses
)

type grokACPCommand struct {
	Name string
}

func (c *grokACPCommand) UnmarshalJSON(raw []byte) error {
	var command map[string]json.RawMessage
	if json.Unmarshal(raw, &command) != nil ||
		!grokACPObjectShape(command, []string{"name"}, []string{"description", "input"}) ||
		json.Unmarshal(command["name"], &c.Name) != nil || strings.TrimSpace(c.Name) == "" || len(c.Name) > 100 {
		return errors.New("invalid ACP command")
	}
	if descriptionRaw, ok := command["description"]; ok {
		var description string
		if json.Unmarshal(descriptionRaw, &description) != nil || len(description) > 1000 {
			return errors.New("invalid ACP command description")
		}
	}
	if inputRaw, ok := command["input"]; ok {
		if !rawJSONPresent(inputRaw) {
			return nil
		}
		input, ok := decodeGrokACPExactObject(inputRaw)
		if !ok || !grokACPObjectKeysExact(input, "hint") {
			return errors.New("invalid ACP command input")
		}
		var hint string
		if json.Unmarshal(input["hint"], &hint) != nil || len(hint) > 500 {
			return errors.New("invalid ACP command input hint")
		}
	}
	return nil
}

type grokACPClient struct {
	scanner            *bufio.Scanner
	writer             io.Writer
	model              string
	workdir            string
	sessionID          string
	pendingSessionID   string
	totalBytes         int
	sessionRequested   bool
	commandsVerified   bool
	commandsReplayed   bool
	userEchoVerified   bool
	promptText         string
	sessionTitle       string
	responseStarted    bool
	responseCompleted  bool
	turnCompleted      bool
	promptCompleted    bool
	reasoningCompleted bool
	responseMessageID  string
	turnAgentResult    string
	turnResultPresent  bool
	responseRail       grokACPResponseRail
	responseUsage      *grokACPResponseUsage
	turnUsage          *grokACPUsage
	requestedEffort    string
	effectiveEffort    string
	thoughtBytes       int
	terminalRetryErr   *grokCLIError
	text               strings.Builder
	onText             func(string)
}

func newGrokACPClient(reader io.Reader, writer io.Writer, model, workdir string, onText func(string)) *grokACPClient {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), grokCLIEventLineLimit)
	return &grokACPClient{scanner: scanner, writer: writer, model: model, workdir: workdir, onText: onText}
}

func (c *grokACPClient) run(ctx context.Context, prompt string) (ReasonerResult, error) {
	if err := c.preflight(ctx); err != nil {
		return ReasonerResult{}, err
	}
	return c.prompt(ctx, prompt)
}

func (c *grokACPClient) preflight(ctx context.Context) error {
	if err := c.send(1, "initialize", map[string]any{
		"protocolVersion": "1",
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]string{"name": "carina", "version": Version},
		"_meta": map[string]any{
			"clientType": "carina", "clientVersion": Version,
			"startupHints": map[string]bool{"nonInteractive": true, "skipGitStatus": true, "skipProjectLayout": true},
		},
	}); err != nil {
		return err
	}
	initRaw, err := c.response(ctx, 1, grokACPPreflight)
	if err != nil {
		return grokACPStageError("initialize", err)
	}
	if err := validateGrokACPInitialize(initRaw, c.model, c.workdir); err != nil {
		return err
	}

	if err := c.send(2, "authenticate", map[string]any{"methodId": "cached_token"}); err != nil {
		return err
	}
	authRaw, err := c.response(ctx, 2, grokACPPreflight)
	if err != nil {
		return grokACPStageError("authenticate", err)
	}
	if err := validateGrokACPAuthenticate(authRaw); err != nil {
		return err
	}

	c.sessionRequested = true
	if err := c.send(3, "session/new", map[string]any{
		"cwd":        c.workdir,
		"mcpServers": []any{},
		"_meta": map[string]any{
			"agentProfile":         grokACPAgentProfile(),
			"systemPromptOverride": grokACPSystemPrompt,
			"modelId":              c.model,
			"yoloMode":             false,
			"autoMode":             false,
		},
	}); err != nil {
		return err
	}
	newRaw, err := c.response(ctx, 3, grokACPPreflight)
	if err != nil {
		return grokACPStageError("create session", err)
	}
	if err := c.acceptNewSession(newRaw); err != nil {
		return err
	}
	if !c.commandsVerified {
		return grokCLIError{message: "Grok Build did not prove an empty tool set before session start", kind: "safety"}
	}
	c.sessionRequested = false
	return nil
}

func grokACPStageError(stage string, err error) error {
	var cliErr grokCLIError
	if errors.As(err, &cliErr) {
		return grokCLIError{message: stage + ": " + cliErr.message, kind: cliErr.kind}
	}
	return err
}

func (c *grokACPClient) prompt(ctx context.Context, prompt string) (ReasonerResult, error) {
	if !c.commandsVerified || c.sessionID == "" {
		return ReasonerResult{}, grokCLIError{message: "Grok Build capabilities were not verified", kind: "safety"}
	}
	c.promptText = grokACPPromptPrefix + prompt
	if err := c.send(5, "session/prompt", map[string]any{
		"sessionId": c.sessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": c.promptText}},
		"_meta":     map[string]any{"verbatim": true, "promptId": "carina-one-shot"},
	}); err != nil {
		return ReasonerResult{}, err
	}
	promptRaw, err := c.response(ctx, 5, grokACPPrompt)
	if err != nil {
		return ReasonerResult{}, err
	}
	result, err := c.finish(promptRaw)
	if err != nil {
		return ReasonerResult{}, err
	}
	if closer, ok := c.writer.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			return ReasonerResult{}, grokCLIError{message: "close Grok Build ACP input", kind: "protocol"}
		}
	}
	if err := c.rejectPostResult(ctx); err != nil {
		return ReasonerResult{}, err
	}
	return result, nil
}

func (c *grokACPClient) rejectPostResult(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		done <- c.drainPostResult(ctx)
	}()
	timer := time.NewTimer(grokACPPostResultWindow)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		// ACP stdio remains open between requests. A short quiescence window
		// catches already-queued protocol violations; the caller then kills the
		// one-shot process instead of waiting for Grok's fixed shutdown grace.
		return nil
	}
}

func (c *grokACPClient) drainPostResult(ctx context.Context) error {
	for c.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := bytes.TrimSpace(c.scanner.Bytes())
		c.totalBytes += len(line) + 1
		if c.totalBytes > grokCLIEventStreamLimit {
			return grokCLIError{message: "ACP event stream exceeds size limit", kind: "protocol"}
		}
		if len(line) != 0 {
			return grokCLIError{message: "Grok Build emitted output after the prompt result", kind: "protocol"}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.scanner.Err(); err != nil {
		return grokCLIError{message: "read Grok Build ACP stream", kind: "protocol"}
	}
	return nil
}

func grokACPAgentProfile() map[string]any {
	return map[string]any{
		"name":               "carina-pure-inference",
		"description":        "Carina ReAct inference adapter (vendor tools disabled)",
		"promptMode":         "full",
		"promptBody":         grokACPSystemPrompt,
		"permissionMode":     "dontAsk",
		"discoverSkills":     false,
		"inheritSkills":      false,
		"agentsMd":           false,
		"injectDefaultTools": false,
		"tools":              []string{"read_file"},
		"disallowedTools":    []string{"read_file", "Agent"},
		"toolConfig": map[string]any{
			"tools": []any{map[string]any{
				"id": "GrokBuild:read_file", "params": nil,
				"name_override": nil, "params_name_overrides": nil,
			}},
		},
		"mcpServers":     []any{},
		"mcpInheritance": "none",
		"maxTurns":       1,
	}
}

func (c *grokACPClient) send(id int, method string, params any) error {
	request := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	if err := json.NewEncoder(c.writer).Encode(request); err != nil {
		return grokCLIError{message: "write Grok Build ACP request", kind: "protocol"}
	}
	return nil
}

type grokACPWireMessage struct {
	Kind   grokACPWireKind
	ID     json.RawMessage
	Method string
	Params json.RawMessage
	Result json.RawMessage
	Error  *grokACPWireError
}

type grokACPWireKind int

const (
	grokACPWireNotification grokACPWireKind = iota
	grokACPWireRequest
	grokACPWireSuccess
	grokACPWireFailure
)

type grokACPWireError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func decodeGrokACPWireMessage(raw []byte) (grokACPWireMessage, error) {
	invalid := func() (grokACPWireMessage, error) {
		return grokACPWireMessage{}, grokCLIError{message: "decode Grok Build ACP message", kind: "protocol"}
	}
	object, ok := decodeGrokACPExactObject(raw)
	if !ok {
		return invalid()
	}
	var version string
	if json.Unmarshal(object["jsonrpc"], &version) != nil || version != "2.0" {
		return invalid()
	}
	methodRaw, hasMethod := object["method"]
	_, hasID := object["id"]
	resultRaw, hasResult := object["result"]
	errorRaw, hasError := object["error"]
	if hasMethod {
		var method string
		if json.Unmarshal(methodRaw, &method) != nil || strings.TrimSpace(method) == "" || len(method) > 200 {
			return invalid()
		}
		if hasID {
			if !grokACPObjectShape(object, []string{"jsonrpc", "id", "method", "params"}, nil) {
				return invalid()
			}
			return grokACPWireMessage{Kind: grokACPWireRequest, ID: object["id"], Method: method, Params: object["params"]}, nil
		}
		if !grokACPObjectShape(object, []string{"jsonrpc", "method", "params"}, nil) {
			return invalid()
		}
		return grokACPWireMessage{Kind: grokACPWireNotification, Method: method, Params: object["params"]}, nil
	}
	if !hasID || hasResult == hasError {
		return invalid()
	}
	if hasResult {
		if !grokACPObjectKeyShape(object, []string{"jsonrpc", "id", "result"}, nil) {
			return invalid()
		}
		return grokACPWireMessage{Kind: grokACPWireSuccess, ID: object["id"], Result: resultRaw}, nil
	}
	if !grokACPObjectShape(object, []string{"jsonrpc", "id", "error"}, nil) {
		return invalid()
	}
	errorObject, ok := decodeGrokACPExactObject(errorRaw)
	if !ok ||
		!grokACPObjectShape(errorObject, []string{"code", "message"}, []string{"data"}) {
		return invalid()
	}
	var wireError grokACPWireError
	if json.Unmarshal(errorObject["code"], &wireError.Code) != nil ||
		json.Unmarshal(errorObject["message"], &wireError.Message) != nil ||
		strings.TrimSpace(wireError.Message) == "" || len(wireError.Message) > 2000 {
		return invalid()
	}
	wireError.Data = errorObject["data"]
	return grokACPWireMessage{Kind: grokACPWireFailure, ID: object["id"], Error: &wireError}, nil
}

func decodeGrokACPExactObject(raw []byte) (map[string]json.RawMessage, bool) {
	if checkNoDupKeys(json.NewDecoder(bytes.NewReader(raw))) != nil {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, false
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, false
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, false
		}
		if _, duplicate := object[key]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		object[key] = value
	}
	token, err = decoder.Token()
	if err != nil {
		return nil, false
	}
	delimiter, ok = token.(json.Delim)
	if !ok || delimiter != '}' {
		return nil, false
	}
	var trailing json.RawMessage
	return object, decoder.Decode(&trailing) == io.EOF
}

func (c *grokACPClient) response(ctx context.Context, expectedID int, phase grokACPPhase) (json.RawMessage, error) {
	for c.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := bytes.TrimSpace(c.scanner.Bytes())
		c.totalBytes += len(line) + 1
		if c.totalBytes > grokCLIEventStreamLimit {
			return nil, grokCLIError{message: "ACP event stream exceeds size limit", kind: "protocol"}
		}
		if len(line) == 0 {
			continue
		}
		message, err := decodeGrokACPWireMessage(line)
		if err != nil {
			return nil, err
		}
		switch message.Kind {
		case grokACPWireRequest:
			return nil, grokCLIError{message: "Grok Build requested a client capability", kind: "safety"}
		case grokACPWireNotification:
			if err := c.notification(message.Method, message.Params, phase); err != nil {
				return nil, err
			}
			continue
		case grokACPWireSuccess, grokACPWireFailure:
		default:
			return nil, grokCLIError{message: "decode Grok Build ACP message", kind: "protocol"}
		}
		id, ok := parseGrokACPID(message.ID)
		if !ok || id != expectedID {
			return nil, grokCLIError{message: "unexpected Grok Build ACP response", kind: "protocol"}
		}
		if message.Kind == grokACPWireFailure {
			return nil, safeGrokACPError(message.Error.Message, message.Error.Data)
		}
		// ACP stdio is persistent: the prompt response does not close stdout. Treat
		// id=5 as the hard boundary and never wait for EOF or a post-result event.
		if phase == grokACPPrompt && expectedID == 5 && !c.promptCompleted {
			return nil, grokCLIError{message: "Grok Build prompt result arrived before the terminal event sequence", kind: "protocol"}
		}
		return message.Result, nil
	}
	if err := c.scanner.Err(); err != nil {
		return nil, grokCLIError{message: "read Grok Build ACP stream", kind: "protocol"}
	}
	return nil, grokCLIError{message: "Grok Build ACP stream ended unexpectedly", kind: "protocol"}
}

func parseGrokACPID(raw json.RawMessage) (int, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, false
	}
	var id int
	return id, json.Unmarshal(raw, &id) == nil
}

func (c *grokACPClient) notification(method string, params json.RawMessage, phase grokACPPhase) error {
	switch method {
	case "session/update":
		return c.sessionUpdate(params, phase)
	case "_x.ai/sessions/changed", "x.ai/sessions/changed":
		if !c.acceptGrokACPSessionsChanged(method, params) {
			return grokCLIError{message: "Grok Build emitted an invalid session roster update", kind: "safety"}
		}
		return nil
	case "_x.ai/queue/changed", "x.ai/queue/changed":
		if phase != grokACPPrompt || !c.acceptGrokACPQueueChanged(method, params) {
			return grokCLIError{message: "Grok Build emitted an invalid prompt queue update", kind: "safety"}
		}
		return nil
	case "_x.ai/settings/update", "x.ai/settings/update":
		if phase != grokACPPreflight || !grokACPSettingsUpdateValid(method, params) {
			return grokCLIError{message: "Grok Build emitted an unsafe settings update", kind: "safety"}
		}
		return nil
	case "_x.ai/announcements/update", "x.ai/announcements/update":
		if phase != grokACPPreflight || !grokACPAnnouncementsUpdateValid(method, params) {
			return grokCLIError{message: "Grok Build emitted an invalid announcements update", kind: "safety"}
		}
		return nil
	case "_x.ai/mcp/servers_updated":
		if phase != grokACPPreflight || !grokACPEmptyMCPUpdate(params) {
			return grokCLIError{message: "Grok Build exposed an MCP server", kind: "safety"}
		}
		return nil
	case "_x.ai/mcp_initialized":
		if phase != grokACPPreflight || !c.grokACPEmptyMCPInitialized(params) {
			return grokCLIError{message: "Grok Build initialized MCP tools", kind: "safety"}
		}
		return nil
	case "_x.ai/session_notification", "x.ai/session_notification":
		valid := phase == grokACPPreflight && c.acceptGrokACPModelChanged(method, params)
		if phase == grokACPPrompt {
			valid = c.acceptGrokACPInferenceLifecycle(method, params)
		}
		if !valid {
			return grokCLIError{message: "Grok Build emitted an unsafe session notification " + grokACPNotificationDescriptor(method, params), kind: "safety"}
		}
		if c.terminalRetryErr != nil {
			return *c.terminalRetryErr
		}
		return nil
	case "_x.ai/session/prompt_complete", "x.ai/session/prompt_complete":
		if phase != grokACPPrompt {
			return grokCLIError{message: "unexpected prompt completion notification", kind: "protocol"}
		}
		if !c.acceptGrokACPPromptComplete(method, params) {
			return grokCLIError{message: "invalid prompt completion notification", kind: "protocol"}
		}
		return nil
	default:
		return grokCLIError{message: "Grok Build emitted unsupported notification " + grokACPNotificationDescriptor(method, params), kind: "safety"}
	}
}

func grokACPNotificationDescriptor(method string, params json.RawMessage) string {
	descriptor := boundedMetadata(method, 100)
	var wrapper struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if strings.HasPrefix(method, "_") && json.Unmarshal(params, &wrapper) == nil && wrapper.Method != "" {
		descriptor += "/" + boundedMetadata(wrapper.Method, 100)
		params = wrapper.Params
	}
	var event struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &event) == nil && event.Update.SessionUpdate != "" {
		descriptor += ":" + boundedMetadata(event.Update.SessionUpdate, 100)
	}
	return descriptor
}

func (c *grokACPClient) acceptGrokACPModelChanged(method string, raw json.RawMessage) bool {
	if !c.sessionRequested {
		return false
	}
	var ok bool
	raw, ok = unwrapGrokACPSessionNotification(method, raw)
	if !ok {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || !grokACPObjectShape(envelope, []string{"sessionId", "update"}, nil) {
		return false
	}
	var sessionID string
	if json.Unmarshal(envelope["sessionId"], &sessionID) != nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	var update map[string]json.RawMessage
	if json.Unmarshal(envelope["update"], &update) != nil ||
		!grokACPObjectShape(update, []string{"sessionUpdate", "model_id"}, []string{"reasoning_effort"}) {
		return false
	}
	var kind, modelID string
	if json.Unmarshal(update["sessionUpdate"], &kind) != nil || kind != "model_changed" ||
		json.Unmarshal(update["model_id"], &modelID) != nil || modelID != c.model {
		return false
	}
	if effortRaw, ok := update["reasoning_effort"]; ok {
		var effort string
		if json.Unmarshal(effortRaw, &effort) != nil || !grokACPEffortKnown(effort) {
			return false
		}
		if c.requestedEffort != "" && effort != c.requestedEffort {
			return false
		}
		c.effectiveEffort = effort
	} else if c.requestedEffort != "" {
		return false
	}
	if c.sessionID != "" && c.sessionID != sessionID || c.pendingSessionID != "" && c.pendingSessionID != sessionID {
		return false
	}
	if c.sessionID == "" {
		c.pendingSessionID = sessionID
	}
	return true
}

func unwrapGrokACPSessionNotification(method string, raw json.RawMessage) (json.RawMessage, bool) {
	return unwrapGrokACPExtNotification(method, "x.ai/session_notification", raw)
}

func unwrapGrokACPExtNotification(method, expected string, raw json.RawMessage) (json.RawMessage, bool) {
	if method != expected && method != "_"+expected {
		return nil, false
	}
	if !strings.HasPrefix(method, "_") {
		return raw, true
	}
	var wrapper map[string]json.RawMessage
	if json.Unmarshal(raw, &wrapper) != nil {
		return nil, false
	}
	innerRaw, wrapped := wrapper["method"]
	if !wrapped {
		return raw, true
	}
	if !grokACPObjectShape(wrapper, []string{"method", "params"}, nil) {
		return nil, false
	}
	var innerMethod string
	if json.Unmarshal(innerRaw, &innerMethod) != nil || innerMethod != expected {
		return nil, false
	}
	return wrapper["params"], true
}

func grokACPSettingsUpdateValid(method string, raw json.RawMessage) bool {
	var ok bool
	raw, ok = unwrapGrokACPExtNotification(method, "x.ai/settings/update", raw)
	if !ok {
		return false
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(raw, &settings) != nil || !grokACPObjectKeyShape(settings, nil, []string{
		"show_resolved_model", "sharing_enabled", "privacy_notice_rollout", "privacy_banner_reshow_days",
		"session_picker_grouped", "tips", "slash_command_tags", "announcements", "campaigns", "gate_message",
		"gate_url", "gate_label", "allow_access", "consent_gate", "subscription_tier_display",
		"auto_permission_mode_enabled", "permission_mode", "group_tool_verbs", "collapsed_edit_blocks",
		"subscription_watch_interval_secs",
	}) {
		return false
	}
	for _, field := range []string{
		"show_resolved_model", "sharing_enabled", "privacy_notice_rollout", "session_picker_grouped", "allow_access",
		"auto_permission_mode_enabled", "group_tool_verbs", "collapsed_edit_blocks",
	} {
		value, exists := settings[field]
		if !exists || !rawJSONPresent(value) {
			continue
		}
		var enabled bool
		if json.Unmarshal(value, &enabled) != nil {
			return false
		}
		if enabled && field == "auto_permission_mode_enabled" {
			return false
		}
	}
	for _, field := range []string{"privacy_banner_reshow_days", "subscription_watch_interval_secs"} {
		value, exists := settings[field]
		if !exists || !rawJSONPresent(value) {
			continue
		}
		var number uint64
		if json.Unmarshal(value, &number) != nil {
			return false
		}
	}
	for _, field := range []string{"gate_message", "gate_url", "gate_label", "subscription_tier_display"} {
		if value, exists := settings[field]; exists && !grokACPNullableBoundedString(value, 2000) {
			return false
		}
	}
	if modeRaw, exists := settings["permission_mode"]; exists && rawJSONPresent(modeRaw) {
		var mode string
		if json.Unmarshal(modeRaw, &mode) != nil || mode != "always-approve" && mode != "ask" && mode != "default" {
			return false
		}
	}
	if gateRaw, exists := settings["consent_gate"]; exists && rawJSONPresent(gateRaw) {
		// Grok 1.0.5 always includes the key. A boolean display flag is safe;
		// any object/array would be an unknown capability surface.
		var enabled bool
		if json.Unmarshal(gateRaw, &enabled) != nil {
			return false
		}
	}
	if tipsRaw, exists := settings["tips"]; exists && rawJSONPresent(tipsRaw) {
		var tips []string
		if json.Unmarshal(tipsRaw, &tips) != nil || tips == nil || len(tips) > 100 {
			return false
		}
		for _, tip := range tips {
			if len(tip) > 2000 {
				return false
			}
		}
	}
	if tagsRaw, exists := settings["slash_command_tags"]; exists && rawJSONPresent(tagsRaw) {
		var tags map[string]string
		if json.Unmarshal(tagsRaw, &tags) != nil || len(tags) > 100 {
			return false
		}
		for name, tag := range tags {
			if strings.TrimSpace(name) == "" || len(name) > 100 || len(tag) > 100 {
				return false
			}
		}
	}
	if announcementsRaw, exists := settings["announcements"]; exists && rawJSONPresent(announcementsRaw) &&
		!grokACPAnnouncementsValid(announcementsRaw) {
		return false
	}
	if campaignsRaw, exists := settings["campaigns"]; exists && rawJSONPresent(campaignsRaw) {
		var campaigns []map[string]json.RawMessage
		if json.Unmarshal(campaignsRaw, &campaigns) != nil || campaigns == nil || len(campaigns) > 100 {
			return false
		}
	}
	return true
}

func grokACPAnnouncementsUpdateValid(method string, raw json.RawMessage) bool {
	var ok bool
	raw, ok = unwrapGrokACPExtNotification(method, "x.ai/announcements/update", raw)
	if !ok {
		return false
	}
	var update map[string]json.RawMessage
	if json.Unmarshal(raw, &update) != nil ||
		!grokACPObjectShape(update, []string{"gen", "announcements"}, nil) {
		return false
	}
	var generation uint64
	return json.Unmarshal(update["gen"], &generation) == nil && generation > 0 &&
		grokACPAnnouncementsValid(update["announcements"])
}

func grokACPAnnouncementsValid(raw json.RawMessage) bool {
	var announcements []map[string]json.RawMessage
	if json.Unmarshal(raw, &announcements) != nil || announcements == nil || len(announcements) > 100 {
		return false
	}
	for _, announcement := range announcements {
		if !grokACPObjectKeyShape(announcement, nil, []string{
			"id", "message", "severity", "title", "cta", "updated_at", "expires_at", "dismissible", "persistent",
		}) {
			return false
		}
		for _, field := range []string{"id", "message", "severity", "title", "updated_at", "expires_at"} {
			if value, exists := announcement[field]; exists && !grokACPNullableBoundedString(value, 4000) {
				return false
			}
		}
		for _, field := range []string{"dismissible", "persistent"} {
			if value, exists := announcement[field]; exists && rawJSONPresent(value) {
				var boolean bool
				if json.Unmarshal(value, &boolean) != nil {
					return false
				}
			}
		}
		if ctaRaw, exists := announcement["cta"]; exists && rawJSONPresent(ctaRaw) {
			var cta map[string]json.RawMessage
			if json.Unmarshal(ctaRaw, &cta) != nil ||
				!grokACPObjectKeyShape(cta, nil, []string{"label", "url", "caption"}) {
				return false
			}
			for _, field := range []string{"label", "url", "caption"} {
				if value, present := cta[field]; present && !grokACPNullableBoundedString(value, 4000) {
					return false
				}
			}
		}
	}
	return true
}

func (c *grokACPClient) acceptGrokACPSessionsChanged(method string, raw json.RawMessage) bool {
	var ok bool
	raw, ok = unwrapGrokACPExtNotification(method, "x.ai/sessions/changed", raw)
	if !ok {
		return false
	}
	var roster map[string]json.RawMessage
	if json.Unmarshal(raw, &roster) != nil || !grokACPObjectShape(roster, []string{"upserted", "removed"}, nil) {
		return false
	}
	var removed []string
	var upserted []map[string]json.RawMessage
	if json.Unmarshal(roster["removed"], &removed) != nil || len(removed) != 0 ||
		json.Unmarshal(roster["upserted"], &upserted) != nil || len(upserted) != 1 {
		return false
	}
	entry := upserted[0]
	if !grokACPObjectKeyShape(entry, []string{
		"sessionId", "title", "cwd", "isWorktree", "modelId", "yolo", "activity", "resident", "lastChangeUnixMs", "origin",
	}, []string{"reasoningEffort"}) {
		return false
	}
	var sessionID, cwd, modelID, activity string
	var isWorktree, yolo, resident bool
	var lastChange int64
	if json.Unmarshal(entry["sessionId"], &sessionID) != nil ||
		json.Unmarshal(entry["cwd"], &cwd) != nil || cleanPath(cwd) != cleanPath(c.workdir) ||
		json.Unmarshal(entry["modelId"], &modelID) != nil || modelID != c.model ||
		json.Unmarshal(entry["isWorktree"], &isWorktree) != nil || isWorktree ||
		json.Unmarshal(entry["yolo"], &yolo) != nil || yolo ||
		json.Unmarshal(entry["resident"], &resident) != nil || !resident ||
		json.Unmarshal(entry["activity"], &activity) != nil || activity != "idle" && activity != "working" ||
		json.Unmarshal(entry["lastChangeUnixMs"], &lastChange) != nil || lastChange < 0 {
		return false
	}
	if c.sessionID != "" && sessionID != c.sessionID || c.pendingSessionID != "" && sessionID != c.pendingSessionID {
		return false
	}
	if c.sessionID == "" && c.pendingSessionID == "" {
		c.pendingSessionID = sessionID
	}
	var origin map[string]json.RawMessage
	var originKind string
	if json.Unmarshal(entry["origin"], &origin) != nil || !grokACPObjectShape(origin, []string{"kind"}, nil) ||
		json.Unmarshal(origin["kind"], &originKind) != nil || originKind != "local" {
		return false
	}
	if titleRaw := entry["title"]; rawJSONPresent(titleRaw) {
		var title string
		if json.Unmarshal(titleRaw, &title) != nil || len(title) > 500 {
			return false
		}
	}
	if effortRaw, exists := entry["reasoningEffort"]; exists {
		var effort string
		if json.Unmarshal(effortRaw, &effort) != nil || !grokACPEffortKnown(effort) {
			return false
		}
	}
	return true
}

func (c *grokACPClient) acceptGrokACPQueueChanged(method string, raw json.RawMessage) bool {
	var ok bool
	raw, ok = unwrapGrokACPExtNotification(method, "x.ai/queue/changed", raw)
	if !ok || c.sessionID == "" || c.promptText == "" {
		return false
	}
	var queue map[string]json.RawMessage
	if json.Unmarshal(raw, &queue) != nil || !grokACPObjectShape(queue, []string{"sessionId", "entries"},
		[]string{"runningPromptId", "runningText", "runningKind", "runningCombinedTexts"}) {
		return false
	}
	var sessionID string
	var entries []map[string]json.RawMessage
	if json.Unmarshal(queue["sessionId"], &sessionID) != nil || sessionID != c.sessionID ||
		json.Unmarshal(queue["entries"], &entries) != nil || len(entries) > 1 {
		return false
	}
	runningIDRaw, hasRunningID := queue["runningPromptId"]
	runningTextRaw, hasRunningText := queue["runningText"]
	runningKindRaw, hasRunningKind := queue["runningKind"]
	_, hasCombined := queue["runningCombinedTexts"]
	if hasCombined || hasRunningID != hasRunningText || hasRunningID != hasRunningKind {
		return false
	}
	if hasRunningID {
		var runningID, runningText, runningKind string
		if len(entries) != 0 || json.Unmarshal(runningIDRaw, &runningID) != nil || runningID != "carina-one-shot" ||
			json.Unmarshal(runningTextRaw, &runningText) != nil || runningText != c.promptText ||
			json.Unmarshal(runningKindRaw, &runningKind) != nil || runningKind != "prompt" {
			return false
		}
		return true
	}
	if len(entries) == 0 {
		return true
	}
	return c.grokACPQueueEntryValid(entries[0])
}

func (c *grokACPClient) acceptGrokACPPromptComplete(method string, raw json.RawMessage) bool {
	var ok bool
	raw, ok = unwrapGrokACPExtNotification(method, "x.ai/session/prompt_complete", raw)
	if !ok || !c.userEchoVerified || !c.turnCompleted || c.promptCompleted {
		return false
	}
	var event map[string]json.RawMessage
	if json.Unmarshal(raw, &event) != nil || !grokACPObjectKeyShape(event,
		[]string{"sessionId", "promptId", "stopReason", "agentResult"}, nil) {
		return false
	}
	var sessionID, promptID, stopReason string
	if json.Unmarshal(event["sessionId"], &sessionID) != nil || sessionID != c.sessionID ||
		json.Unmarshal(event["promptId"], &promptID) != nil || promptID != "carina-one-shot" ||
		json.Unmarshal(event["stopReason"], &stopReason) != nil || stopReason != "end_turn" {
		return false
	}
	result, present, valid := grokACPAgentResult(event["agentResult"])
	if !valid || present != c.turnResultPresent || present &&
		(len(result) > maxProviderResponseBytes || result != c.text.String() || c.turnAgentResult != result) {
		return false
	}
	c.promptCompleted = true
	return true
}

func (c *grokACPClient) grokACPQueueEntryValid(entry map[string]json.RawMessage) bool {
	if !grokACPObjectShape(entry, []string{"id", "version", "kind", "text", "position"},
		[]string{"owner", "lastEditor", "combinedTexts"}) {
		return false
	}
	var id, kind, text string
	var version uint64
	var position int
	if json.Unmarshal(entry["id"], &id) != nil || id != "carina-one-shot" ||
		json.Unmarshal(entry["version"], &version) != nil || version != 0 ||
		json.Unmarshal(entry["kind"], &kind) != nil || kind != "prompt" ||
		json.Unmarshal(entry["text"], &text) != nil || text != c.promptText ||
		json.Unmarshal(entry["position"], &position) != nil || position != 0 {
		return false
	}
	if _, exists := entry["combinedTexts"]; exists || rawJSONPresent(entry["lastEditor"]) {
		return false
	}
	if ownerRaw, exists := entry["owner"]; exists && rawJSONPresent(ownerRaw) {
		var owner string
		if json.Unmarshal(ownerRaw, &owner) != nil || owner != "carina" {
			return false
		}
	}
	return true
}

func (c *grokACPClient) acceptGrokACPInferenceLifecycle(method string, raw json.RawMessage) bool {
	var ok bool
	raw, ok = unwrapGrokACPSessionNotification(method, raw)
	if !ok || c.sessionID == "" || !c.commandsVerified || !c.userEchoVerified {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return false
	}
	var sessionID string
	if json.Unmarshal(envelope["sessionId"], &sessionID) != nil || sessionID != c.sessionID {
		return false
	}
	var update map[string]json.RawMessage
	if json.Unmarshal(envelope["update"], &update) != nil {
		return false
	}
	var kind string
	if json.Unmarshal(update["sessionUpdate"], &kind) != nil {
		return false
	}
	switch kind {
	case "session_summary_generated", "response_started", "reasoning_completed", "response_completed":
		if !grokACPObjectShape(envelope, []string{"sessionId", "update"}, nil) {
			return false
		}
	default:
		if !grokACPObjectShape(envelope, []string{"sessionId", "update", "_meta"}, nil) ||
			!grokACPTurnMetaValid(envelope["_meta"]) {
			return false
		}
	}

	switch kind {
	case "response_started":
		if c.responseRail != grokACPResponseRailUnknown || c.responseStarted || c.responseCompleted ||
			c.turnCompleted || c.promptCompleted || c.text.Len() != 0 || c.thoughtBytes != 0 ||
			!grokACPObjectShape(update,
				[]string{"sessionUpdate", "message_id", "model", "input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"}, nil) ||
			!grokACPUintFields(update, "input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens") {
			return false
		}
		var messageID, model string
		if json.Unmarshal(update["message_id"], &messageID) != nil || strings.TrimSpace(messageID) == "" || len(messageID) > 256 ||
			json.Unmarshal(update["model"], &model) != nil || model != c.model {
			return false
		}
		c.responseRail = grokACPResponseRailMessages
		c.responseStarted = true
		c.responseMessageID = messageID
		return true
	case "reasoning_completed":
		if c.responseRail != grokACPResponseRailMessages || !c.responseStarted || c.responseCompleted || c.reasoningCompleted ||
			!grokACPObjectShape(update, []string{"sessionUpdate"}, []string{"signature"}) ||
			!grokACPOptionalBoundedString(update, "signature", grokCLIEventLineLimit) {
			return false
		}
		c.reasoningCompleted = true
		return true
	case "response_completed":
		if c.responseRail == grokACPResponseRailUnknown || c.responseCompleted || c.turnCompleted || c.promptCompleted {
			return false
		}
		switch c.responseRail {
		case grokACPResponseRailMessages:
			if !c.responseStarted || !grokACPObjectShape(update,
				[]string{"sessionUpdate", "message_id", "stop_reason", "usage"},
				[]string{"signature", "stop_sequence"}) {
				return false
			}
			var messageID, stopReason string
			if json.Unmarshal(update["stop_reason"], &stopReason) != nil || stopReason != "end_turn" ||
				json.Unmarshal(update["message_id"], &messageID) != nil || messageID != c.responseMessageID ||
				!grokACPOptionalBoundedString(update, "signature", grokCLIEventLineLimit) ||
				!grokACPOptionalBoundedString(update, "stop_sequence", 256) {
				return false
			}
		case grokACPResponseRailResponses:
			if !grokACPObjectShape(update, []string{"sessionUpdate", "usage"}, []string{"signature"}) ||
				!grokACPOptionalBoundedString(update, "signature", grokCLIEventLineLimit) {
				return false
			}
		default:
			return false
		}
		usage, valid := decodeGrokACPResponseUsage(update["usage"])
		if !valid {
			return false
		}
		c.responseUsage = &usage
		c.responseCompleted = true
		return true
	case "turn_completed":
		if !c.responseCompleted || c.turnCompleted || c.promptCompleted ||
			!grokACPObjectKeyShape(update, []string{"sessionUpdate", "prompt_id", "stop_reason", "usage"}, []string{"agent_result"}) {
			return false
		}
		var promptID, stopReason string
		if json.Unmarshal(update["prompt_id"], &promptID) != nil || promptID != "carina-one-shot" ||
			json.Unmarshal(update["stop_reason"], &stopReason) != nil || stopReason != "end_turn" {
			return false
		}
		agentResult, resultPresent, valid := grokACPAgentResult(update["agent_result"])
		if !valid || resultPresent && (len(agentResult) > maxProviderResponseBytes || agentResult != c.text.String()) {
			return false
		}
		usage, valid := decodeGrokACPPromptUsage(update["usage"])
		if !valid {
			return false
		}
		c.turnCompleted = true
		c.turnAgentResult = agentResult
		c.turnResultPresent = resultPresent
		c.turnUsage = &usage
		return true
	case "session_summary_generated":
		if c.sessionTitle != "" || !grokACPObjectShape(update,
			[]string{"sessionUpdate", "session_summary"}, nil) {
			return false
		}
		var title string
		if json.Unmarshal(update["session_summary"], &title) != nil ||
			strings.TrimSpace(title) == "" || len(title) > 500 {
			return false
		}
		c.sessionTitle = title
		return true
	case "retry_state":
		valid, terminalErr := grokACPRetryState(update)
		if valid {
			c.terminalRetryErr = terminalErr
		}
		return valid
	default:
		return false
	}
}

func grokACPRetryState(update map[string]json.RawMessage) (bool, *grokCLIError) {
	var stateType string
	if json.Unmarshal(update["type"], &stateType) != nil {
		return false, nil
	}
	switch stateType {
	case "retrying":
		if !grokACPObjectShape(update,
			[]string{"sessionUpdate", "type", "attempt", "max_retries", "reason"}, nil) {
			return false, nil
		}
		var attempt, maxRetries uint32
		valid := json.Unmarshal(update["attempt"], &attempt) == nil && attempt > 0 &&
			json.Unmarshal(update["max_retries"], &maxRetries) == nil && maxRetries > 0 && maxRetries <= grokCLI1MaxRetries &&
			attempt <= maxRetries && grokACPRequiredBoundedString(update, "reason", 1000)
		return valid, nil
	case "exhausted":
		if !grokACPObjectShape(update,
			[]string{"sessionUpdate", "type", "attempts", "reason", "is_rate_limited"}, nil) {
			return false, nil
		}
		var attempts uint32
		var rateLimited bool
		var reason string
		valid := json.Unmarshal(update["attempts"], &attempts) == nil && attempts > 0 && attempts <= grokCLI1MaxRetries &&
			json.Unmarshal(update["is_rate_limited"], &rateLimited) == nil &&
			json.Unmarshal(update["reason"], &reason) == nil && strings.TrimSpace(reason) != "" && len(reason) <= 1000
		if !valid {
			return false, nil
		}
		return true, grokACPTerminalRetryError("", reason, rateLimited)
	case "failed":
		valid := grokACPObjectShape(update,
			[]string{"sessionUpdate", "type", "error_type", "message"}, nil) &&
			grokACPRequiredBoundedString(update, "error_type", 100) &&
			grokACPRequiredBoundedString(update, "message", 1000)
		if !valid {
			return false, nil
		}
		var errorType, message string
		if json.Unmarshal(update["error_type"], &errorType) != nil || json.Unmarshal(update["message"], &message) != nil {
			return false, nil
		}
		return true, grokACPTerminalRetryError(errorType, message, false)
	default:
		return false, nil
	}
}

func grokACPTerminalRetryError(errorType, message string, rateLimited bool) *grokCLIError {
	if rateLimited {
		return &grokCLIError{message: "rate limit reached after Grok Build retries", upstreamKind: "retry_exhausted"}
	}
	if safe := classifySafeGrokStderr(message); safe != "" {
		return &grokCLIError{message: safe, upstreamKind: errorType}
	}
	switch strings.ToLower(strings.TrimSpace(errorType)) {
	case "auth", "legacy_auth":
		return &grokCLIError{message: "authentication failed; run `grok login`", upstreamKind: errorType}
	case "rate_limit", "rate_limited":
		return &grokCLIError{message: "rate limit reached", upstreamKind: errorType}
	case "credit_limit", "quota", "usage_limit", "free_usage":
		return &grokCLIError{message: "Grok Build usage limit reached", upstreamKind: errorType}
	case "auth_transient", "server", "server_error", "network", "timeout", "unavailable", "http", "idle_timeout":
		return &grokCLIError{message: "Grok Build is temporarily unavailable", upstreamKind: errorType}
	default:
		return &grokCLIError{message: "Grok Build request failed", upstreamKind: errorType}
	}
}

func grokACPRequiredBoundedString(object map[string]json.RawMessage, field string, limit int) bool {
	var value string
	return json.Unmarshal(object[field], &value) == nil && strings.TrimSpace(value) != "" && len(value) <= limit
}

func grokACPObjectShape(object map[string]json.RawMessage, required, optional []string) bool {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		if !rawJSONPresent(object[key]) {
			return false
		}
		allowed[key] = true
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func grokACPObjectKeysExact(object map[string]json.RawMessage, keys ...string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, exists := object[key]; !exists {
			return false
		}
	}
	return true
}

func grokACPObjectKeyShape(object map[string]json.RawMessage, required, optional []string) bool {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		if _, exists := object[key]; !exists {
			return false
		}
		allowed[key] = true
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func grokACPUintFields(object map[string]json.RawMessage, fields ...string) bool {
	for _, field := range fields {
		var value uint64
		if json.Unmarshal(object[field], &value) != nil {
			return false
		}
	}
	return true
}

func grokACPOptionalBoundedString(object map[string]json.RawMessage, field string, limit int) bool {
	raw, exists := object[field]
	if !exists {
		return true
	}
	var value string
	return rawJSONPresent(raw) && json.Unmarshal(raw, &value) == nil && len(value) <= limit
}

func grokACPAgentResult(raw json.RawMessage) (string, bool, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", false, true
	}
	var result string
	if json.Unmarshal(trimmed, &result) != nil {
		return "", false, false
	}
	return result, true, true
}

func decodeGrokACPResponseUsage(raw json.RawMessage) (grokACPResponseUsage, bool) {
	var decoded grokACPResponseUsage
	var usage map[string]json.RawMessage
	if json.Unmarshal(raw, &usage) != nil || !grokACPObjectShape(usage, []string{
		"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "reasoning_tokens",
	}, nil) {
		return decoded, false
	}
	if !grokACPUintFields(usage, "input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "reasoning_tokens") ||
		json.Unmarshal(raw, &decoded) != nil {
		return decoded, false
	}
	return decoded, true
}

func grokACPResponseUsageValid(raw json.RawMessage) bool {
	_, ok := decodeGrokACPResponseUsage(raw)
	return ok
}

func grokACPUsageModelObjectValid(object map[string]json.RawMessage) bool {
	required := []string{
		"inputTokens", "outputTokens", "totalTokens", "cachedReadTokens", "cacheCreationTokens",
		"reasoningTokens", "modelCalls", "apiDurationMs",
	}
	if !grokACPObjectShape(object, required, []string{"costUsdTicks", "costIsPartial"}) ||
		!grokACPUintFields(object, required...) {
		return false
	}
	if raw, exists := object["costUsdTicks"]; exists {
		var value int64
		if json.Unmarshal(raw, &value) != nil || value < 0 {
			return false
		}
	}
	if raw, exists := object["costIsPartial"]; exists {
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return false
		}
	}
	var inputTokens, outputTokens, totalTokens, modelCalls uint64
	return json.Unmarshal(object["inputTokens"], &inputTokens) == nil &&
		json.Unmarshal(object["outputTokens"], &outputTokens) == nil &&
		json.Unmarshal(object["totalTokens"], &totalTokens) == nil &&
		json.Unmarshal(object["modelCalls"], &modelCalls) == nil &&
		inputTokens <= ^uint64(0)-outputTokens && totalTokens == inputTokens+outputTokens && modelCalls > 0
}

func decodeGrokACPPromptUsage(raw json.RawMessage) (grokACPUsage, bool) {
	var decoded grokACPUsage
	var object map[string]json.RawMessage
	modelFields := []string{
		"inputTokens", "outputTokens", "totalTokens", "cachedReadTokens", "cacheCreationTokens",
		"reasoningTokens", "modelCalls", "apiDurationMs",
	}
	required := append(append([]string{}, modelFields...), "numTurns")
	if json.Unmarshal(raw, &object) != nil || !grokACPObjectShape(object, required,
		[]string{"costUsdTicks", "costIsPartial", "modelUsage", "usageIsIncomplete"}) ||
		!grokACPUintFields(object, required...) {
		return decoded, false
	}
	modelObject := make(map[string]json.RawMessage, len(modelFields)+2)
	for _, field := range modelFields {
		modelObject[field] = object[field]
	}
	for _, field := range []string{"costUsdTicks", "costIsPartial"} {
		if value, exists := object[field]; exists {
			modelObject[field] = value
		}
	}
	if !grokACPUsageModelObjectValid(modelObject) {
		return decoded, false
	}
	if rawIncomplete, exists := object["usageIsIncomplete"]; exists {
		var incomplete bool
		if json.Unmarshal(rawIncomplete, &incomplete) != nil {
			return decoded, false
		}
	}
	if rawModels, exists := object["modelUsage"]; exists {
		var models map[string]json.RawMessage
		if json.Unmarshal(rawModels, &models) != nil || models == nil || len(models) == 0 || len(models) > 16 {
			return decoded, false
		}
		for modelID, modelRaw := range models {
			if strings.TrimSpace(modelID) == "" || len(modelID) > 128 {
				return decoded, false
			}
			var modelObject map[string]json.RawMessage
			if json.Unmarshal(modelRaw, &modelObject) != nil || !grokACPUsageModelObjectValid(modelObject) {
				return decoded, false
			}
		}
	}
	if json.Unmarshal(raw, &decoded) != nil || decoded.NumTurns != 1 {
		return decoded, false
	}
	return decoded, true
}

func grokACPTurnMetaValid(raw json.RawMessage) bool {
	var meta map[string]json.RawMessage
	if json.Unmarshal(raw, &meta) != nil || !grokACPObjectShape(meta, []string{"eventId", "agentTimestampMs"}, nil) {
		return false
	}
	var eventID string
	var timestamp int64
	return json.Unmarshal(meta["eventId"], &eventID) == nil && eventID != "" && len(eventID) <= 256 &&
		json.Unmarshal(meta["agentTimestampMs"], &timestamp) == nil && timestamp > 0
}

func grokACPStreamMetaValid(raw json.RawMessage, wantUpdateType string) bool {
	var meta map[string]json.RawMessage
	if json.Unmarshal(raw, &meta) != nil || !grokACPObjectShape(meta, []string{
		"eventId", "agentTimestampMs", "promptId", "streamStartMs", "turnStartMs", "updateType", "chunkId", "totalTokens",
	}, nil) {
		return false
	}
	var eventID, promptID, updateType string
	var agentTimestamp, streamStart, turnStart int64
	var chunkID, totalTokens uint64
	return json.Unmarshal(meta["eventId"], &eventID) == nil && eventID != "" && len(eventID) <= 256 &&
		json.Unmarshal(meta["agentTimestampMs"], &agentTimestamp) == nil && agentTimestamp > 0 &&
		json.Unmarshal(meta["promptId"], &promptID) == nil && promptID == "carina-one-shot" &&
		json.Unmarshal(meta["streamStartMs"], &streamStart) == nil && streamStart > 0 &&
		json.Unmarshal(meta["turnStartMs"], &turnStart) == nil && turnStart > 0 &&
		streamStart >= turnStart && agentTimestamp >= streamStart &&
		json.Unmarshal(meta["updateType"], &updateType) == nil && updateType == wantUpdateType &&
		json.Unmarshal(meta["chunkId"], &chunkID) == nil &&
		json.Unmarshal(meta["totalTokens"], &totalTokens) == nil
}

func grokACPCommandsMetaValid(raw json.RawMessage) bool {
	var meta map[string]json.RawMessage
	if json.Unmarshal(raw, &meta) != nil || !grokACPObjectShape(meta,
		[]string{"eventId", "agentTimestampMs", "updateType", "updateParams", "totalTokens"},
		[]string{"promptId", "streamStartMs", "turnStartMs"}) {
		return false
	}
	var eventID, updateType string
	var agentTimestamp int64
	var totalTokens uint64
	if json.Unmarshal(meta["eventId"], &eventID) != nil || eventID == "" || len(eventID) > 256 ||
		json.Unmarshal(meta["agentTimestampMs"], &agentTimestamp) != nil || agentTimestamp <= 0 ||
		json.Unmarshal(meta["updateType"], &updateType) != nil || updateType != "AvailableCommandsUpdate" ||
		json.Unmarshal(meta["totalTokens"], &totalTokens) != nil {
		return false
	}
	var updateParams map[string]json.RawMessage
	var commandCount uint64
	if json.Unmarshal(meta["updateParams"], &updateParams) != nil ||
		!grokACPObjectShape(updateParams, []string{"commandsCount"}, nil) ||
		json.Unmarshal(updateParams["commandsCount"], &commandCount) != nil || commandCount != uint64(len(grokACPBaselineCommands)) {
		return false
	}
	if promptRaw, exists := meta["promptId"]; exists {
		var promptID string
		if json.Unmarshal(promptRaw, &promptID) != nil || promptID != "carina-one-shot" {
			return false
		}
	}
	streamRaw, hasStream := meta["streamStartMs"]
	turnRaw, hasTurn := meta["turnStartMs"]
	if hasStream != hasTurn {
		return false
	}
	if hasStream {
		var streamStart, turnStart int64
		if json.Unmarshal(streamRaw, &streamStart) != nil || streamStart <= 0 ||
			json.Unmarshal(turnRaw, &turnStart) != nil || turnStart <= 0 ||
			streamStart < turnStart || agentTimestamp < streamStart {
			return false
		}
	}
	return true
}

func grokACPEffortKnown(effort string) bool {
	switch effort {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func grokACPEmptyMCPUpdate(raw json.RawMessage) bool {
	var direct struct {
		MCPServers *[]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &direct) == nil && direct.MCPServers != nil && len(*direct.MCPServers) == 0 {
		return true
	}
	var wrapper struct {
		Method string `json:"method"`
		Params *struct {
			MCPServers *[]json.RawMessage `json:"mcpServers"`
		} `json:"params"`
	}
	return json.Unmarshal(raw, &wrapper) == nil && wrapper.Method == "x.ai/mcp/servers_updated" &&
		wrapper.Params != nil && wrapper.Params.MCPServers != nil && len(*wrapper.Params.MCPServers) == 0
}

func (c *grokACPClient) grokACPEmptyMCPInitialized(raw json.RawMessage) bool {
	type event struct {
		SessionID    string `json:"sessionId"`
		MCPToolCount *int   `json:"mcpToolCount"`
	}
	valid := func(value event) bool {
		return value.MCPToolCount != nil && *value.MCPToolCount == 0 && value.SessionID != "" &&
			(c.sessionID == "" || value.SessionID == c.sessionID) &&
			(c.pendingSessionID == "" || value.SessionID == c.pendingSessionID)
	}
	var direct event
	if json.Unmarshal(raw, &direct) == nil && valid(direct) {
		return true
	}
	var wrapper struct {
		Method string `json:"method"`
		Params event  `json:"params"`
	}
	return json.Unmarshal(raw, &wrapper) == nil && wrapper.Method == "x.ai/mcp_initialized" && valid(wrapper.Params)
}

func (c *grokACPClient) sessionUpdate(params json.RawMessage, phase grokACPPhase) error {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(params, &envelope) != nil ||
		!grokACPObjectShape(envelope, []string{"sessionId", "update"}, []string{"_meta"}) {
		return grokCLIError{message: "invalid Grok Build session update", kind: "protocol"}
	}
	var sessionID string
	if json.Unmarshal(envelope["sessionId"], &sessionID) != nil || strings.TrimSpace(sessionID) == "" {
		return grokCLIError{message: "invalid Grok Build session update", kind: "protocol"}
	}
	updateRaw := envelope["update"]
	if c.sessionID != "" && sessionID != c.sessionID {
		return grokCLIError{message: "Grok Build emitted an update for another session", kind: "safety"}
	}
	if c.sessionID == "" {
		if c.pendingSessionID != "" && c.pendingSessionID != sessionID {
			return grokCLIError{message: "Grok Build emitted updates for multiple sessions", kind: "safety"}
		}
		c.pendingSessionID = sessionID
	}
	var kind struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if json.Unmarshal(updateRaw, &kind) != nil || kind.SessionUpdate == "" {
		return grokCLIError{message: "invalid Grok Build session update type", kind: "protocol"}
	}
	switch kind.SessionUpdate {
	case "available_commands_update":
		setupUpdate := phase == grokACPPreflight && c.sessionRequested
		inertReplay := phase == grokACPPrompt && c.commandsVerified && !c.commandsReplayed &&
			!c.responseCompleted && !c.turnCompleted
		if !setupUpdate && !inertReplay {
			return grokCLIError{message: "Grok Build changed capabilities outside session setup", kind: "safety"}
		}
		metaRaw, hasMeta := envelope["_meta"]
		if !hasMeta || !grokACPCommandsMetaValid(metaRaw) {
			return grokCLIError{message: "Grok Build command capabilities had invalid metadata", kind: "safety"}
		}
		if err := validateGrokACPCommandsUpdate(updateRaw); err != nil {
			return err
		}
		c.commandsVerified = true
		if phase == grokACPPrompt {
			c.commandsReplayed = true
		}
		return nil
	case "user_message_chunk":
		if phase != grokACPPrompt || !c.commandsVerified || c.userEchoVerified || !c.acceptGrokACPUserEcho(params) {
			return grokCLIError{message: "Grok Build emitted an invalid user prompt echo", kind: "safety"}
		}
		c.userEchoVerified = true
		return nil
	case "agent_message_chunk":
		if phase != grokACPPrompt || !c.commandsVerified || !c.userEchoVerified ||
			c.responseCompleted {
			return grokCLIError{message: "Grok Build emitted text before capability verification", kind: "safety"}
		}
		text, ok := c.grokACPTextChunk(params, "agent_message_chunk", "AgentMessageChunk")
		if !ok {
			return grokCLIError{message: "unsupported Grok Build content block", kind: "protocol"}
		}
		if c.text.Len()+len(text) > maxProviderResponseBytes {
			return grokCLIError{message: "response exceeds size limit", kind: "protocol"}
		}
		if c.responseRail == grokACPResponseRailUnknown {
			c.responseRail = grokACPResponseRailResponses
		}
		c.text.WriteString(text)
		if c.onText != nil {
			c.onText(text)
		}
		return nil
	case "agent_thought_chunk":
		if phase != grokACPPrompt || !c.commandsVerified || !c.userEchoVerified ||
			c.responseCompleted {
			return grokCLIError{message: "Grok Build emitted reasoning before capability verification", kind: "safety"}
		}
		text, ok := c.grokACPTextChunk(params, "agent_thought_chunk", "AgentThoughtChunk")
		if !ok || c.thoughtBytes+len(text) > maxProviderResponseBytes {
			return grokCLIError{message: "invalid Grok Build reasoning block", kind: "protocol"}
		}
		if c.responseRail == grokACPResponseRailUnknown {
			c.responseRail = grokACPResponseRailResponses
		}
		c.thoughtBytes += len(text)
		return nil
	case "session_info_update":
		if phase != grokACPPrompt || !c.acceptGrokACPSessionTitleUpdate(params) {
			return grokCLIError{message: "Grok Build emitted an invalid session title update", kind: "safety"}
		}
		return nil
	case "tool_call", "tool_call_update", "plan":
		// Isolation still refuses to execute Grok-native tools. If the model
		// already streamed JSON/prose, keep that text so Carina can parse or
		// salvage it. An empty body stays a json_fallback so the agent requeries
		// instead of killing a run that already did useful work.
		return grokCLIError{
			message: "Grok Build attempted a disabled capability",
			kind:    "json_fallback",
			salvage: strings.TrimSpace(c.text.String()),
		}
	default:
		return grokCLIError{message: "Grok Build emitted an unsupported session update", kind: "safety"}
	}
}

func (c *grokACPClient) grokACPTextChunk(raw json.RawMessage, wantKind, wantUpdateType string) (string, bool) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil ||
		!grokACPObjectShape(envelope, []string{"sessionId", "update", "_meta"}, nil) {
		return "", false
	}
	var sessionID string
	if json.Unmarshal(envelope["sessionId"], &sessionID) != nil || sessionID != c.sessionID ||
		!grokACPStreamMetaValid(envelope["_meta"], wantUpdateType) {
		return "", false
	}
	var update map[string]json.RawMessage
	if json.Unmarshal(envelope["update"], &update) != nil ||
		!grokACPObjectShape(update, []string{"sessionUpdate", "content"}, nil) {
		return "", false
	}
	var kind string
	if json.Unmarshal(update["sessionUpdate"], &kind) != nil || kind != wantKind {
		return "", false
	}
	var content map[string]json.RawMessage
	if json.Unmarshal(update["content"], &content) != nil ||
		!grokACPObjectShape(content, []string{"type", "text"}, nil) {
		return "", false
	}
	var contentType, text string
	if json.Unmarshal(content["type"], &contentType) != nil || contentType != "text" ||
		json.Unmarshal(content["text"], &text) != nil || len(text) > maxProviderResponseBytes {
		return "", false
	}
	return text, true
}

func (c *grokACPClient) acceptGrokACPSessionTitleUpdate(raw json.RawMessage) bool {
	if !c.userEchoVerified || c.sessionID == "" || c.sessionTitle == "" {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil ||
		!grokACPObjectShape(envelope, []string{"sessionId", "update"}, nil) {
		return false
	}
	var sessionID string
	if json.Unmarshal(envelope["sessionId"], &sessionID) != nil || sessionID != c.sessionID {
		return false
	}
	var update map[string]json.RawMessage
	if json.Unmarshal(envelope["update"], &update) != nil ||
		!grokACPObjectShape(update, []string{"sessionUpdate", "title"}, nil) {
		return false
	}
	var kind, title string
	return json.Unmarshal(update["sessionUpdate"], &kind) == nil && kind == "session_info_update" &&
		json.Unmarshal(update["title"], &title) == nil && title == c.sessionTitle
}

func (c *grokACPClient) acceptGrokACPUserEcho(raw json.RawMessage) bool {
	if c.sessionID == "" || c.promptText == "" {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil ||
		!grokACPObjectShape(envelope, []string{"sessionId", "update", "_meta"}, nil) ||
		!grokACPTurnMetaValid(envelope["_meta"]) {
		return false
	}
	var sessionID string
	if json.Unmarshal(envelope["sessionId"], &sessionID) != nil || sessionID != c.sessionID {
		return false
	}
	var update map[string]json.RawMessage
	if json.Unmarshal(envelope["update"], &update) != nil ||
		!grokACPObjectShape(update, []string{"sessionUpdate", "content", "_meta"}, nil) {
		return false
	}
	var kind string
	if json.Unmarshal(update["sessionUpdate"], &kind) != nil || kind != "user_message_chunk" {
		return false
	}
	var content map[string]json.RawMessage
	if json.Unmarshal(update["content"], &content) != nil ||
		!grokACPObjectShape(content, []string{"type", "text"}, nil) {
		return false
	}
	var contentType, text string
	if json.Unmarshal(content["type"], &contentType) != nil || contentType != "text" ||
		json.Unmarshal(content["text"], &text) != nil || text != c.promptText {
		return false
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(update["_meta"], &meta) != nil ||
		!grokACPObjectShape(meta, []string{"modelId", "promptIndex"}, nil) {
		return false
	}
	var modelID string
	var promptIndex uint64
	return json.Unmarshal(meta["modelId"], &modelID) == nil && modelID == c.model &&
		json.Unmarshal(meta["promptIndex"], &promptIndex) == nil && promptIndex == 0
}

func validateGrokACPInitialize(raw json.RawMessage, model, workdir string) error {
	response, ok := decodeGrokACPExactObject(raw)
	if !ok ||
		!grokACPObjectShape(response, []string{"protocolVersion", "agentCapabilities", "authMethods", "_meta"}, nil) ||
		!grokACPProtocolV1(response["protocolVersion"]) {
		return grokCLIError{message: "Grok Build ACP initialization is incompatible", kind: "protocol"}
	}
	if !grokACPAgentCapabilitiesValid(response["agentCapabilities"]) {
		return grokCLIError{message: "Grok Build ACP capabilities are incompatible", kind: "safety"}
	}
	var methods []map[string]json.RawMessage
	if json.Unmarshal(response["authMethods"], &methods) != nil || methods == nil || len(methods) == 0 || len(methods) > 4 {
		return grokCLIError{message: "Grok Build ACP authentication methods are incompatible", kind: "safety"}
	}
	hasCached := false
	for _, method := range methods {
		var id string
		if json.Unmarshal(method["id"], &id) == nil && id == "xai.api_key" {
			return grokCLIError{message: "Grok Build exposed API-key authentication", kind: "safety"}
		}
		if !grokACPAuthMethodValid(method) {
			return grokCLIError{message: "Grok Build ACP authentication methods are incompatible", kind: "safety"}
		}
		hasCached = hasCached || id == "cached_token"
	}
	if !hasCached {
		return grokCLIError{message: "Grok Build ACP cached OAuth authentication is unavailable", kind: "safety"}
	}
	if !grokACPInitializeMetaValid(response["_meta"], model, workdir) {
		return grokCLIError{message: "Grok Build ACP initialization metadata is incompatible", kind: "safety"}
	}
	return nil
}

func grokACPAgentCapabilitiesValid(raw json.RawMessage) bool {
	var capabilities map[string]json.RawMessage
	if json.Unmarshal(raw, &capabilities) != nil ||
		!grokACPObjectShape(capabilities,
			[]string{"loadSession", "promptCapabilities", "mcpCapabilities", "sessionCapabilities", "auth", "_meta"}, nil) {
		return false
	}
	var loadSession bool
	if json.Unmarshal(capabilities["loadSession"], &loadSession) != nil || !loadSession {
		return false
	}
	var prompt map[string]json.RawMessage
	var embeddedContext bool
	if json.Unmarshal(capabilities["promptCapabilities"], &prompt) != nil ||
		!grokACPObjectShape(prompt, []string{"embeddedContext"}, []string{"image", "audio"}) ||
		json.Unmarshal(prompt["embeddedContext"], &embeddedContext) != nil || !embeddedContext {
		return false
	}
	for _, field := range []string{"image", "audio"} {
		if value, ok := prompt[field]; ok {
			var supported bool
			if json.Unmarshal(value, &supported) != nil {
				return false
			}
		}
	}
	var mcp map[string]json.RawMessage
	var httpSupported, sseSupported bool
	if json.Unmarshal(capabilities["mcpCapabilities"], &mcp) != nil ||
		!grokACPObjectShape(mcp, []string{"http", "sse"}, nil) ||
		json.Unmarshal(mcp["http"], &httpSupported) != nil || !httpSupported ||
		json.Unmarshal(mcp["sse"], &sseSupported) != nil || !sseSupported {
		return false
	}
	var session map[string]json.RawMessage
	if json.Unmarshal(capabilities["sessionCapabilities"], &session) != nil ||
		!grokACPObjectKeysExact(session, "list", "resume", "close") {
		return false
	}
	for _, field := range []string{"list", "resume", "close"} {
		var capability map[string]json.RawMessage
		if json.Unmarshal(session[field], &capability) != nil || len(capability) != 0 {
			return false
		}
	}
	var auth map[string]json.RawMessage
	if json.Unmarshal(capabilities["auth"], &auth) != nil || len(auth) != 0 {
		return false
	}
	return grokACPAgentCapabilityMetaValid(capabilities["_meta"])
}

func grokACPAgentCapabilityMetaValid(raw json.RawMessage) bool {
	var meta map[string]json.RawMessage
	if json.Unmarshal(raw, &meta) != nil ||
		!grokACPObjectShape(meta, []string{"x.ai/fs_notify", "x.ai/hooks", "x.ai/capabilities"}, nil) {
		return false
	}
	var fsNotify bool
	if json.Unmarshal(meta["x.ai/fs_notify"], &fsNotify) != nil || !fsNotify {
		return false
	}
	var hooks map[string]json.RawMessage
	if json.Unmarshal(meta["x.ai/hooks"], &hooks) != nil ||
		!grokACPObjectShape(hooks, []string{"blockingEvents", "decisions", "stopSignals"}, nil) ||
		!grokACPStringSetEqual(hooks["blockingEvents"], []string{"pre_tool_use", "stop", "subagent_stop"}) ||
		!grokACPStringSetEqual(hooks["decisions"], []string{"deny", "block"}) ||
		!grokACPStringSetEqual(hooks["stopSignals"], []string{"continue", "stopReason", "additionalContext"}) {
		return false
	}
	var extensions map[string]json.RawMessage
	if json.Unmarshal(meta["x.ai/capabilities"], &extensions) != nil ||
		!grokACPObjectShape(extensions, []string{"toolOverrides"}, nil) {
		return false
	}
	var overrides map[string]json.RawMessage
	if json.Unmarshal(extensions["toolOverrides"], &overrides) != nil ||
		!grokACPObjectShape(overrides,
			[]string{"x_keyword_search", "x_semantic_search", "x_user_search", "x_thread_fetch"}, nil) {
		return false
	}
	want := map[string]bool{
		"x_keyword_search": true, "x_semantic_search": true,
		"x_user_search": false, "x_thread_fetch": false,
	}
	for field, expected := range want {
		var actual bool
		if json.Unmarshal(overrides[field], &actual) != nil || actual != expected {
			return false
		}
	}
	return true
}

func grokACPStringSetEqual(raw json.RawMessage, expected []string) bool {
	var values []string
	if json.Unmarshal(raw, &values) != nil || len(values) != len(expected) {
		return false
	}
	sort.Strings(values)
	want := append([]string(nil), expected...)
	sort.Strings(want)
	return strings.Join(values, "\x00") == strings.Join(want, "\x00")
}

func grokACPAuthMethodValid(method map[string]json.RawMessage) bool {
	if !grokACPObjectShape(method, []string{"id"}, []string{"name", "description", "_meta"}) {
		return false
	}
	var id string
	if json.Unmarshal(method["id"], &id) != nil || id != "cached_token" && id != "grok.com" && id != "oidc" {
		return false
	}
	for _, field := range []string{"name", "description"} {
		if raw, ok := method[field]; ok {
			var value string
			if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" || len(value) > 500 {
				return false
			}
		}
	}
	if raw, ok := method["_meta"]; ok {
		if !rawJSONPresent(raw) {
			return true
		}
		var meta map[string]json.RawMessage
		if json.Unmarshal(raw, &meta) != nil || !grokACPObjectKeyShape(meta, nil, []string{"external_provider"}) {
			return false
		}
		if externalRaw, exists := meta["external_provider"]; exists {
			var external bool
			if json.Unmarshal(externalRaw, &external) != nil || !external || id != "grok.com" {
				return false
			}
		}
	}
	return true
}

func grokACPInitializeMetaValid(raw json.RawMessage, model, workdir string) bool {
	var meta map[string]json.RawMessage
	if json.Unmarshal(raw, &meta) != nil || !grokACPObjectShape(meta,
		[]string{"grokShell", "defaultAuthMethodId", "availableCommands"},
		[]string{
			"x.ai/mcp/sdk", "x.ai/pluginDirs", "currentWorkingDirectory", "agentVersion", "agentId",
			"agentInstanceId", "hostname", "modelState", "mcpServers", "mcpApps", "metadata",
			"cancelRewind", "sessionRecap", "voiceMode",
		}) {
		return false
	}
	var grokShell bool
	var defaultAuth string
	var commands []grokACPCommand
	if json.Unmarshal(meta["grokShell"], &grokShell) != nil || !grokShell ||
		json.Unmarshal(meta["defaultAuthMethodId"], &defaultAuth) != nil || defaultAuth != "cached_token" ||
		json.Unmarshal(meta["availableCommands"], &commands) != nil || !grokACPCommandsMatch(commands) {
		return false
	}
	for _, field := range []string{"x.ai/mcp/sdk", "x.ai/pluginDirs"} {
		if value, ok := meta[field]; ok {
			var supported bool
			if json.Unmarshal(value, &supported) != nil || !supported {
				return false
			}
		}
	}
	if cwdRaw, ok := meta["currentWorkingDirectory"]; ok {
		var cwd string
		if json.Unmarshal(cwdRaw, &cwd) != nil || cleanPath(cwd) != cleanPath(workdir) {
			return false
		}
	}
	for _, field := range []string{"agentVersion", "agentId", "agentInstanceId", "hostname"} {
		if value, ok := meta[field]; ok && !grokACPNullableBoundedString(value, 500) {
			return false
		}
	}
	if state, ok := meta["modelState"]; ok && !grokACPModelStateValid(state, model) {
		return false
	}
	if servers, ok := meta["mcpServers"]; ok {
		var entries []json.RawMessage
		if json.Unmarshal(servers, &entries) != nil || entries == nil || len(entries) != 0 {
			return false
		}
	}
	for _, field := range []string{"mcpApps", "sessionRecap", "voiceMode"} {
		if value, ok := meta[field]; ok {
			var enabled bool
			if json.Unmarshal(value, &enabled) != nil || enabled {
				return false
			}
		}
	}
	if value, ok := meta["cancelRewind"]; ok {
		var enabled bool
		if json.Unmarshal(value, &enabled) != nil {
			return false
		}
	}
	if metadata, ok := meta["metadata"]; ok && rawJSONPresent(metadata) {
		var object map[string]json.RawMessage
		if json.Unmarshal(metadata, &object) != nil || len(object) != 0 {
			return false
		}
	}
	return true
}

func validateGrokACPAuthenticate(raw json.RawMessage) error {
	response, ok := decodeGrokACPExactObject(raw)
	if !ok || !grokACPObjectShape(response, []string{"_meta"}, nil) {
		return grokCLIError{message: "Grok Build ACP authentication response is incompatible", kind: "protocol"}
	}
	metaRaw := response["_meta"]
	var meta map[string]json.RawMessage
	if json.Unmarshal(metaRaw, &meta) != nil || !grokACPObjectShape(meta, []string{"auth_mode"}, []string{
		"email", "team_id", "team_name", "is_zdr", "team_role",
		"coding_data_retention_opt_out", "show_resolved_model", "gate", "subscription_tier",
	}) {
		return grokCLIError{message: "Grok Build ACP authentication metadata is incompatible", kind: "safety"}
	}
	var authMode string
	if json.Unmarshal(meta["auth_mode"], &authMode) != nil || authMode != "Oidc" && authMode != "GrokCom" {
		return grokCLIError{message: "Grok Build authentication is not a subscription OAuth session", kind: "safety"}
	}
	for _, field := range []string{"email", "team_id", "team_name", "team_role", "subscription_tier"} {
		if value, ok := meta[field]; ok && !grokACPNullableBoundedString(value, 1000) {
			return grokCLIError{message: "Grok Build ACP authentication metadata is incompatible", kind: "safety"}
		}
	}
	for _, field := range []string{"is_zdr", "coding_data_retention_opt_out", "show_resolved_model"} {
		if value, ok := meta[field]; ok && rawJSONPresent(value) {
			var boolean bool
			if json.Unmarshal(value, &boolean) != nil {
				return grokCLIError{message: "Grok Build ACP authentication metadata is incompatible", kind: "safety"}
			}
		}
	}
	if gate, ok := meta["gate"]; ok && rawJSONPresent(gate) {
		var gateObject map[string]json.RawMessage
		if json.Unmarshal(gate, &gateObject) != nil ||
			!grokACPObjectShape(gateObject, []string{"message"}, []string{"url", "label"}) ||
			!grokACPNullableBoundedString(gateObject["message"], 2000) {
			return grokCLIError{message: "Grok Build ACP authentication metadata is incompatible", kind: "safety"}
		}
		for _, field := range []string{"url", "label"} {
			if value, exists := gateObject[field]; exists && !grokACPNullableBoundedString(value, 2000) {
				return grokCLIError{message: "Grok Build ACP authentication metadata is incompatible", kind: "safety"}
			}
		}
	}
	return nil
}

func grokACPNullableBoundedString(raw json.RawMessage, limit int) bool {
	if !rawJSONPresent(raw) {
		return true
	}
	var value string
	return json.Unmarshal(raw, &value) == nil && len(value) <= limit
}

func grokACPModelStateValid(raw json.RawMessage, expectedModel string) bool {
	var state map[string]json.RawMessage
	if json.Unmarshal(raw, &state) != nil ||
		!grokACPObjectShape(state, []string{"currentModelId"}, []string{"availableModels"}) {
		return false
	}
	var current string
	if json.Unmarshal(state["currentModelId"], &current) != nil || current != expectedModel {
		return false
	}
	if availableRaw, ok := state["availableModels"]; ok {
		var available []map[string]json.RawMessage
		if json.Unmarshal(availableRaw, &available) != nil || available == nil || len(available) > 100 {
			return false
		}
		for _, entry := range available {
			if !grokACPObjectShape(entry, []string{"modelId"}, []string{"name", "description", "_meta"}) {
				return false
			}
			var id string
			if json.Unmarshal(entry["modelId"], &id) != nil || strings.TrimSpace(id) == "" || len(id) > 200 {
				return false
			}
			for _, field := range []string{"name", "description"} {
				if value, exists := entry[field]; exists && !grokACPNullableBoundedString(value, 1000) {
					return false
				}
			}
			if metaRaw, exists := entry["_meta"]; exists && rawJSONPresent(metaRaw) {
				var modelMeta map[string]json.RawMessage
				if json.Unmarshal(metaRaw, &modelMeta) != nil {
					return false
				}
			}
		}
	}
	return true
}

func grokACPProtocolV1(raw json.RawMessage) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text == "1"
	}
	var number int
	return json.Unmarshal(raw, &number) == nil && number == 1
}

func (c *grokACPClient) acceptNewSession(raw json.RawMessage) error {
	response, ok := decodeGrokACPExactObject(raw)
	if !ok ||
		!grokACPObjectShape(response, []string{"sessionId", "models", "_meta"}, nil) {
		return grokCLIError{message: "Grok Build created an invalid session", kind: "protocol"}
	}
	var sessionID string
	if json.Unmarshal(response["sessionId"], &sessionID) != nil || strings.TrimSpace(sessionID) == "" || len(sessionID) > 500 {
		return grokCLIError{message: "Grok Build created an invalid session", kind: "protocol"}
	}
	if !grokACPModelStateValid(response["models"], c.model) {
		return grokCLIError{message: "Grok Build selected an unexpected model", kind: "protocol"}
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(response["_meta"], &meta) != nil || !grokACPObjectShape(meta,
		[]string{"currentWorkingDirectory", "feedbackEnabled"},
		[]string{
			"codebaseIndexed", "isGitRepo", "gitRoot", "showNonGitWarning", "x.ai/sessionConfig",
			"x.ai/sessionDetail", "x.ai/schedulerBackgroundLoops", "toolOverrides",
		}) {
		return grokCLIError{message: "Grok Build created an invalid session", kind: "protocol"}
	}
	var cwd string
	if json.Unmarshal(meta["currentWorkingDirectory"], &cwd) != nil || cleanPath(cwd) != cleanPath(c.workdir) {
		return grokCLIError{message: "Grok Build selected an unexpected working directory", kind: "safety"}
	}
	var feedbackEnabled bool
	if json.Unmarshal(meta["feedbackEnabled"], &feedbackEnabled) != nil || feedbackEnabled {
		return grokCLIError{message: "Grok Build feedback remained enabled", kind: "safety"}
	}
	if _, exists := meta["toolOverrides"]; exists {
		return grokCLIError{message: "Grok Build applied a tool override", kind: "safety"}
	}
	if indexedRaw, exists := meta["codebaseIndexed"]; exists {
		var indexed []string
		if json.Unmarshal(indexedRaw, &indexed) != nil || indexed == nil || len(indexed) != 0 {
			return grokCLIError{message: "Grok Build initialized a codebase index", kind: "safety"}
		}
	}
	if gitRaw, exists := meta["isGitRepo"]; exists {
		var isGitRepo bool
		if json.Unmarshal(gitRaw, &isGitRepo) != nil || isGitRepo {
			return grokCLIError{message: "Grok Build escaped the isolated working directory", kind: "safety"}
		}
	}
	if gitRoot, exists := meta["gitRoot"]; exists && rawJSONPresent(gitRoot) {
		return grokCLIError{message: "Grok Build escaped the isolated working directory", kind: "safety"}
	}
	for _, field := range []string{"showNonGitWarning"} {
		if value, exists := meta[field]; exists {
			var boolean bool
			if json.Unmarshal(value, &boolean) != nil {
				return grokCLIError{message: "Grok Build created an invalid session", kind: "protocol"}
			}
		}
	}
	if backgroundRaw, exists := meta["x.ai/schedulerBackgroundLoops"]; exists {
		var enabled bool
		if json.Unmarshal(backgroundRaw, &enabled) != nil || enabled {
			return grokCLIError{message: "Grok Build enabled background loops", kind: "safety"}
		}
	}
	if configRaw, exists := meta["x.ai/sessionConfig"]; exists && !grokACPSessionConfigValid(configRaw) {
		return grokCLIError{message: "Grok Build exposed an invalid session configuration", kind: "safety"}
	}
	if detailRaw, exists := meta["x.ai/sessionDetail"]; exists && !grokACPSessionDetailValid(detailRaw, sessionID, c.model, c.workdir) {
		return grokCLIError{message: "Grok Build exposed an invalid session identity", kind: "safety"}
	}
	if c.pendingSessionID != "" && c.pendingSessionID != sessionID {
		return grokCLIError{message: "Grok Build session identity changed during setup", kind: "safety"}
	}
	c.sessionID = sessionID
	return nil
}

func grokACPSessionConfigValid(raw json.RawMessage) bool {
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil || !grokACPObjectShape(config, []string{"options"}, nil) {
		return false
	}
	var options []map[string]json.RawMessage
	if json.Unmarshal(config["options"], &options) != nil || options == nil || len(options) > 100 {
		return false
	}
	for _, option := range options {
		if !grokACPObjectShape(option, []string{"id", "category", "label", "selected"}, []string{"description"}) {
			return false
		}
		var id, category, label string
		var selected bool
		if json.Unmarshal(option["id"], &id) != nil || strings.TrimSpace(id) == "" || len(id) > 200 ||
			json.Unmarshal(option["category"], &category) != nil || category != "model" && category != "mode" ||
			json.Unmarshal(option["label"], &label) != nil || len(label) > 500 ||
			json.Unmarshal(option["selected"], &selected) != nil {
			return false
		}
		if description, exists := option["description"]; exists && !grokACPNullableBoundedString(description, 1000) {
			return false
		}
	}
	return true
}

func grokACPSessionDetailValid(raw json.RawMessage, sessionID, model, workdir string) bool {
	var detail map[string]json.RawMessage
	if json.Unmarshal(raw, &detail) != nil ||
		!grokACPObjectShape(detail, []string{"sessionId", "kind", "cwd", "currentModelId"}, []string{"title"}) {
		return false
	}
	var actualSession, kind, cwd, actualModel string
	if json.Unmarshal(detail["sessionId"], &actualSession) != nil || actualSession != sessionID ||
		json.Unmarshal(detail["kind"], &kind) != nil || kind != "build" ||
		json.Unmarshal(detail["cwd"], &cwd) != nil || cleanPath(cwd) != cleanPath(workdir) ||
		json.Unmarshal(detail["currentModelId"], &actualModel) != nil || actualModel != model {
		return false
	}
	return !rawJSONPresent(detail["title"]) || grokACPNullableBoundedString(detail["title"], 500)
}

func validateGrokACPCommandsUpdate(raw json.RawMessage) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != 3 ||
		!rawJSONPresent(object["sessionUpdate"]) || !rawJSONPresent(object["availableCommands"]) || !rawJSONPresent(object["_meta"]) {
		return grokCLIError{message: "invalid Grok Build capability update", kind: "safety"}
	}
	var update struct {
		AvailableCommands []grokACPCommand           `json:"availableCommands"`
		Meta              map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(raw, &update) != nil || len(update.Meta) != 1 || !grokACPCommandsMatch(update.AvailableCommands) {
		return grokCLIError{message: "invalid Grok Build capability update", kind: "safety"}
	}
	toolsRaw, ok := update.Meta["tools"]
	if !ok {
		return grokCLIError{message: "Grok Build capability update omitted tools", kind: "safety"}
	}
	var tools []string
	if json.Unmarshal(toolsRaw, &tools) != nil || tools == nil || len(tools) != 0 {
		return grokCLIError{message: "Grok Build exposed a tool in isolated mode", kind: "safety"}
	}
	return nil
}

func grokACPCommandsMatch(commands []grokACPCommand) bool {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Name)
	}
	sort.Strings(names)
	return len(names) == len(grokACPBaselineCommands) && strings.Join(names, "\x00") == strings.Join(grokACPBaselineCommands, "\x00")
}

type grokACPUsageModel struct {
	InputTokens         uint64 `json:"inputTokens"`
	OutputTokens        uint64 `json:"outputTokens"`
	TotalTokens         uint64 `json:"totalTokens"`
	CachedReadTokens    uint64 `json:"cachedReadTokens"`
	CacheCreationTokens uint64 `json:"cacheCreationTokens"`
	ReasoningTokens     uint64 `json:"reasoningTokens"`
	ModelCalls          uint64 `json:"modelCalls"`
	APIDurationMS       uint64 `json:"apiDurationMs"`
	CostUSDTicks        *int64 `json:"costUsdTicks,omitempty"`
	CostIsPartial       bool   `json:"costIsPartial,omitempty"`
}

type grokACPUsage struct {
	grokACPUsageModel
	ModelUsage        map[string]grokACPUsageModel `json:"modelUsage,omitempty"`
	NumTurns          uint64                       `json:"numTurns"`
	UsageIsIncomplete bool                         `json:"usageIsIncomplete"`
}

type grokACPResponseUsage struct {
	InputTokens              uint64 `json:"input_tokens"`
	OutputTokens             uint64 `json:"output_tokens"`
	CacheReadInputTokens     uint64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens uint64 `json:"cache_creation_input_tokens"`
	ReasoningTokens          uint64 `json:"reasoning_tokens"`
}

func (c *grokACPClient) finish(raw json.RawMessage) (ReasonerResult, error) {
	validRail := c.responseRail == grokACPResponseRailResponses ||
		c.responseRail == grokACPResponseRailMessages && c.responseStarted
	if !validRail || !c.commandsReplayed || !c.responseCompleted || !c.turnCompleted || !c.promptCompleted {
		return ReasonerResult{}, grokCLIError{message: "Grok Build prompt result arrived before the terminal event sequence", kind: "protocol"}
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(raw, &response) != nil || !grokACPObjectShape(response, []string{"stopReason", "_meta"}, nil) {
		return ReasonerResult{}, grokCLIError{message: "Grok Build prompt did not complete normally", kind: "protocol"}
	}
	var stopReason string
	var meta map[string]json.RawMessage
	metaFields := []string{
		"sessionId", "requestId", "promptId", "totalTokens", "modelId",
		"inputTokens", "outputTokens", "cachedReadTokens", "reasoningTokens", "usage",
	}
	if json.Unmarshal(response["stopReason"], &stopReason) != nil || stopReason != "end_turn" ||
		json.Unmarshal(response["_meta"], &meta) != nil || !grokACPObjectShape(meta, metaFields, nil) ||
		!grokACPUintFields(meta, "totalTokens", "inputTokens", "outputTokens", "cachedReadTokens", "reasoningTokens") {
		return ReasonerResult{}, grokCLIError{message: "Grok Build prompt did not complete normally", kind: "protocol"}
	}
	var sessionID, requestID, promptID, modelID string
	var totalTokens, inputTokens, outputTokens, cachedReadTokens, reasoningTokens uint64
	if json.Unmarshal(meta["sessionId"], &sessionID) != nil || sessionID != c.sessionID ||
		json.Unmarshal(meta["requestId"], &requestID) != nil || requestID != "carina-one-shot" ||
		json.Unmarshal(meta["promptId"], &promptID) != nil || promptID != "carina-one-shot" ||
		json.Unmarshal(meta["modelId"], &modelID) != nil || modelID != c.model ||
		json.Unmarshal(meta["totalTokens"], &totalTokens) != nil ||
		json.Unmarshal(meta["inputTokens"], &inputTokens) != nil ||
		json.Unmarshal(meta["outputTokens"], &outputTokens) != nil ||
		json.Unmarshal(meta["cachedReadTokens"], &cachedReadTokens) != nil ||
		json.Unmarshal(meta["reasoningTokens"], &reasoningTokens) != nil {
		return ReasonerResult{}, grokCLIError{message: "Grok Build prompt response was not correlated", kind: "protocol"}
	}
	usage, validUsage := decodeGrokACPPromptUsage(meta["usage"])
	if !validUsage || totalTokens != usage.TotalTokens || inputTokens != usage.InputTokens ||
		outputTokens != usage.OutputTokens || cachedReadTokens != usage.CachedReadTokens ||
		reasoningTokens != usage.ReasoningTokens || c.turnUsage == nil || !reflect.DeepEqual(*c.turnUsage, usage) ||
		c.responseUsage == nil || c.responseUsage.InputTokens > ^uint64(0)-c.responseUsage.CacheReadInputTokens ||
		c.responseUsage.InputTokens+c.responseUsage.CacheReadInputTokens > ^uint64(0)-c.responseUsage.CacheCreationInputTokens ||
		usage.InputTokens != c.responseUsage.InputTokens+c.responseUsage.CacheReadInputTokens+c.responseUsage.CacheCreationInputTokens ||
		usage.OutputTokens != c.responseUsage.OutputTokens || usage.CachedReadTokens != c.responseUsage.CacheReadInputTokens ||
		usage.CacheCreationTokens != c.responseUsage.CacheCreationInputTokens || usage.ReasoningTokens != c.responseUsage.ReasoningTokens {
		return ReasonerResult{}, grokCLIError{message: "Grok Build reported inconsistent usage", kind: "protocol"}
	}
	text := strings.TrimSpace(c.text.String())
	if text == "" {
		return ReasonerResult{}, grokCLIError{message: "Grok Build completed without response text", kind: "protocol"}
	}
	uncached := usage.InputTokens
	if usage.CachedReadTokens >= uncached {
		uncached = 0
	} else {
		uncached -= usage.CachedReadTokens
	}
	if usage.CacheCreationTokens >= uncached {
		uncached = 0
	} else {
		uncached -= usage.CacheCreationTokens
	}
	estimated := usage.UsageIsIncomplete || (usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CachedReadTokens == 0 && usage.CacheCreationTokens == 0)
	return ReasonerResult{Text: text, Usage: ModelUsage{
		Provider:         provider.GrokBuildProviderID,
		Model:            c.model,
		InputTokens:      boundedUint64Int(uncached),
		OutputTokens:     boundedUint64Int(usage.OutputTokens),
		CacheReadTokens:  boundedUint64Int(usage.CachedReadTokens),
		CacheWriteTokens: boundedUint64Int(usage.CacheCreationTokens),
		Estimated:        estimated,
	}}, nil
}

func boundedUint64Int(value uint64) int {
	maxInt := uint64(^uint(0) >> 1)
	if value > maxInt {
		return int(maxInt)
	}
	return int(value)
}

func safeGrokACPError(message string, data json.RawMessage) error {
	combined := strings.ToLower(message + " " + string(data))
	if safe := classifySafeGrokStderr(combined); safe != "" {
		return grokCLIError{message: safe}
	}
	if strings.Contains(combined, "auth") || strings.Contains(combined, "login") {
		return grokCLIError{message: "authentication failed; run `grok login`"}
	}
	return grokCLIError{message: "Grok Build request failed"}
}

func classifySafeGrokStderr(stderr string) string {
	lower := strings.ToLower(stderr)
	for _, marker := range []struct{ needle, message string }{
		{"not logged in", "not logged in; run `grok login`"}, {"not signed in", "not logged in; run `grok login`"},
		{"grok login", "not logged in; run `grok login`"}, {"unauthorized", "authentication failed; run `grok login`"},
		{"rate limit", "rate limit reached"}, {"too many requests", "rate limit reached"},
		{"quota", "Grok Build quota exhausted"}, {"usage limit", "Grok Build usage limit reached"},
		{"subscription", "Grok Build subscription is unavailable"}, {"billing", "Grok Build billing is unavailable"},
		{"temporarily unavailable", "Grok Build is temporarily unavailable"}, {"service unavailable", "Grok Build is temporarily unavailable"},
	} {
		if strings.Contains(lower, marker.needle) {
			return marker.message
		}
	}
	return ""
}

func cleanPath(path string) string {
	if path == "" {
		return ""
	}
	clean, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if evaluated, evalErr := filepath.EvalSymlinks(clean); evalErr == nil {
		clean = evaluated
	}
	return filepath.Clean(clean)
}

func (r *grokCLIReasoner) Close() {
	if r.isolationRoot != "" {
		_ = os.RemoveAll(r.isolationRoot)
	}
}

var _ routedGrokBuildReasoner = (*grokCLIReasoner)(nil)
var _ providerErrorClassifier = grokCLIError{}
var _ error = grokCLIError{}
