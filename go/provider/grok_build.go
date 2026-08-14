package provider

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	GrokBuildProviderID       = "grok-build"
	GrokBuildSourceKind       = "grok-build"
	GrokBuildSourceLabel      = "Grok Build"
	GrokBuildRouteCLIOAuth    = "cli_oauth"
	GrokBuildAuthModeCLIOAuth = "cli_oauth"
	GrokBuildCredentialOwner  = "grok-build"

	GrokBuildActionUseSession = "use_cli_session"
	GrokBuildActionLogin      = "login_cli"
	GrokBuildActionUpdate     = "update_cli"
	GrokBuildActionRetry      = "retry_probe"

	GrokBuildStateAbsent              = "absent"
	GrokBuildStateSignedOut           = "signed_out"
	GrokBuildStateReady               = "ready"
	GrokBuildStateIncompatibleVersion = "incompatible_version"
	GrokBuildStateProbeFailed         = "probe_failed"

	grokBuildMinimumVersion = "1.0.3"
	grokBuildProbeLimit     = 64 << 10
	grokBuildCacheTTL       = 15 * time.Second
)

var (
	grokBuildVersionPattern = regexp.MustCompile(`(?m)^grok\s+([0-9]+\.[0-9]+\.[0-9]+)(?:\s|$)`)
	grokBuildModelPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type GrokBuildDiscovery struct {
	State        string
	Version      string
	DefaultModel string
	Models       []string
	Reason       string
	BinaryPath   string `json:"-"`
}

type GrokBuildDiscoverer struct {
	Timeout  time.Duration
	LookPath func(string) (string, error)
	HomeDir  func() (string, error)
	Getenv   func(string) string
	Environ  func() []string
}

func (d GrokBuildDiscoverer) Discover(ctx context.Context) GrokBuildDiscovery {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return grokBuildCanceledDiscovery()
	}
	bin, err := d.findBinary()
	if err != nil {
		return GrokBuildDiscovery{State: GrokBuildStateAbsent, Reason: "Install Grok Build to use your Grok subscription."}
	}
	result := GrokBuildDiscovery{State: GrokBuildStateProbeFailed, BinaryPath: bin}
	versionOutput, err := d.run(ctx, bin, "--version")
	if err != nil {
		result.Reason = "Grok Build could not be checked. Retry or run `grok doctor`."
		return result
	}
	match := grokBuildVersionPattern.FindStringSubmatch(versionOutput)
	if len(match) != 2 {
		result.State = GrokBuildStateIncompatibleVersion
		result.Reason = "Update Grok Build before using it with Carina."
		return result
	}
	result.Version = match[1]
	if !grokBuildVersionCompatible(result.Version) {
		result.State = GrokBuildStateIncompatibleVersion
		result.Reason = "Update Grok Build before using it with Carina."
		return result
	}
	modelsOutput, err := d.run(ctx, bin, "models")
	if err != nil {
		if isGrokBuildSignedOut(modelsOutput) {
			result.State = GrokBuildStateSignedOut
			result.Reason = "Run `grok login`, then refresh Providers."
			return result
		}
		result.Reason = "Grok Build could not verify this session. Retry or run `grok doctor`."
		return result
	}
	if isGrokBuildSignedOut(modelsOutput) {
		result.State = GrokBuildStateSignedOut
		result.Reason = "Run `grok login`, then refresh Providers."
		return result
	}
	defaultModel, models, ok := parseGrokBuildModels(modelsOutput)
	if !ok {
		result.State = GrokBuildStateIncompatibleVersion
		result.Reason = "Update Grok Build before using it with Carina."
		return result
	}
	result.State = GrokBuildStateReady
	result.DefaultModel = defaultModel
	result.Models = models
	result.Reason = "Uses your signed-in Grok Build session; usage applies to that account."
	return result
}

func grokBuildVersionCompatible(version string) bool {
	parsed, ok := parseGrokBuildSemver(version)
	if !ok {
		return false
	}
	minimum, ok := parseGrokBuildSemver(grokBuildMinimumVersion)
	if !ok {
		return false
	}
	for i := range parsed {
		if parsed[i] != minimum[i] {
			return parsed[i] > minimum[i]
		}
	}
	return true
}

func parseGrokBuildSemver(version string) ([3]uint64, bool) {
	var parsed [3]uint64
	parts := strings.Split(version, ".")
	if len(parts) != len(parsed) {
		return parsed, false
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return parsed, false
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return parsed, false
		}
		parsed[i] = value
	}
	return parsed, true
}

func (d GrokBuildDiscoverer) findBinary() (string, error) {
	return d.findBinaryForOS(runtime.GOOS)
}

func (d GrokBuildDiscoverer) findBinaryForOS(goos string) (string, error) {
	lookPath := d.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if path, err := lookPath("grok"); err == nil && strings.TrimSpace(path) != "" {
		return filepath.Clean(path), nil
	}
	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	binaryName := "grok"
	if goos == "windows" {
		binaryName = "grok.exe"
	}
	var candidates []string
	if home := strings.TrimSpace(getenv("GROK_HOME")); home != "" {
		candidates = append(candidates, filepath.Join(home, "bin", binaryName))
	}
	if goos == "windows" {
		if profile := strings.TrimSpace(getenv("USERPROFILE")); profile != "" {
			candidates = append(candidates, filepath.Join(profile, ".grok", "bin", binaryName))
		}
	}
	homeDir := d.HomeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	if home, err := homeDir(); err == nil && strings.TrimSpace(home) != "" {
		candidates = append(candidates, filepath.Join(home, ".grok", "bin", binaryName))
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && (goos == "windows" || info.Mode()&0o111 != 0) {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func (d GrokBuildDiscoverer) run(parent context.Context, bin string, args ...string) (string, error) {
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	environ := d.Environ
	if environ == nil {
		environ = os.Environ
	}
	cmd.Env = grokBuildProbeEnvironment(environ())
	var stdout, stderr limitedProbeBuffer
	stdout.limit = grokBuildProbeLimit
	stderr.limit = grokBuildProbeLimit
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := runGrokBuildProbeCommand(cmd)
	combined := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
	if ctxErr := ctx.Err(); ctxErr != nil {
		return combined, ctxErr
	}
	if stdout.overflow || stderr.overflow {
		return combined, fmt.Errorf("grok probe output exceeds limit")
	}
	return combined, err
}

func runGrokBuildProbeCommand(cmd *exec.Cmd) error {
	if err := startGrokBuildProbeCommand(cmd); err != nil {
		return err
	}
	defer releaseGrokBuildProbeCommand(cmd)
	return cmd.Wait()
}

func grokBuildProbeEnvironment(env []string) []string {
	return grokBuildProbeEnvironmentForOS(env, runtime.GOOS)
}

func grokBuildProbeEnvironmentForOS(env []string, goos string) []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true,
		"LANG": true, "LC_ALL": true, "LC_CTYPE": true,
		"GROK_HOME": true, "GROK_AUTH_PATH": true,
	}
	if goos == "windows" {
		allowed["USERPROFILE"] = true
		allowed["HOMEDRIVE"] = true
		allowed["HOMEPATH"] = true
		allowed["SYSTEMROOT"] = true
		allowed["WINDIR"] = true
		allowed["PATHEXT"] = true
	}
	out := make([]string, 0, len(allowed)+4)
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if IsSafeGrokBuildProxyKey(key) {
			if IsSafeGrokBuildLoopbackProxy(value) {
				out = append(out, entry)
			}
			continue
		}
		lookupKey := key
		if goos == "windows" {
			lookupKey = strings.ToUpper(key)
		}
		if allowed[lookupKey] {
			out = append(out, entry)
		}
	}
	return append(out,
		"GROK_DISABLE_AUTOUPDATER=1",
		"GROK_DISABLE_API_KEY_AUTH=1",
		"GROK_TELEMETRY_ENABLED=0",
		"OTEL_SDK_DISABLED=true",
	)
}

// IsSafeGrokBuildProxyKey reports whether key is a supported proxy transport variable.
func IsSafeGrokBuildProxyKey(key string) bool {
	switch key {
	case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy":
		return true
	default:
		return false
	}
}

// IsSafeGrokBuildLoopbackProxy permits only credential-free local proxy URLs.
func IsSafeGrokBuildLoopbackProxy(value string) bool {
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type limitedProbeBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedProbeBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.Buffer.Write(p[:remaining])
		} else {
			_, _ = b.Buffer.Write(p)
		}
	}
	if n > remaining {
		b.overflow = true
	}
	return n, nil
}

func isGrokBuildSignedOut(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "not logged in") ||
		strings.Contains(lower, "not signed in") ||
		strings.Contains(lower, "run `grok login`") ||
		strings.Contains(lower, "run grok login") ||
		strings.Contains(lower, "please log in")
}

func parseGrokBuildModels(output string) (string, []string, bool) {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	defaultModel := ""
	models := make([]string, 0, 4)
	stage := 0
	defaultMarkerSeen := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch stage {
		case 0:
			if line != "You are logged in with grok.com." {
				return "", nil, false
			}
			stage = 1
		case 1:
			const prefix = "Default model:"
			if !strings.HasPrefix(line, prefix) {
				return "", nil, false
			}
			defaultModel = strings.TrimSpace(strings.TrimPrefix(line, prefix))
			if !grokBuildModelPattern.MatchString(defaultModel) {
				return "", nil, false
			}
			stage = 2
		case 2:
			if line != "Available models:" {
				return "", nil, false
			}
			stage = 3
		case 3:
			if !strings.HasPrefix(line, "*") && !strings.HasPrefix(line, "-") {
				return "", nil, false
			}
			markedDefault := strings.HasSuffix(line, " (default)")
			modelText := strings.TrimSpace(line[1:])
			if markedDefault {
				modelText = strings.TrimSpace(strings.TrimSuffix(modelText, "(default)"))
			}
			if !grokBuildModelPattern.MatchString(modelText) || markedDefault != (modelText == defaultModel) {
				return "", nil, false
			}
			if markedDefault {
				if defaultMarkerSeen || line[0] != '*' {
					return "", nil, false
				}
				defaultMarkerSeen = true
			} else if line[0] != '-' {
				return "", nil, false
			}
			models = append(models, modelText)
		default:
			return "", nil, false
		}
	}
	if stage != 3 || !defaultMarkerSeen || len(models) == 0 {
		return "", nil, false
	}
	seen := map[string]bool{}
	unique := models[:0]
	defaultPresent := false
	for _, model := range models {
		if seen[model] {
			return "", nil, false
		}
		seen[model] = true
		unique = append(unique, model)
		defaultPresent = defaultPresent || model == defaultModel
	}
	if !defaultPresent {
		return "", nil, false
	}
	sort.SliceStable(unique, func(i, j int) bool {
		if unique[i] == defaultModel {
			return true
		}
		if unique[j] == defaultModel {
			return false
		}
		return unique[i] < unique[j]
	})
	return defaultModel, append([]string(nil), unique...), true
}

func MergeGrokBuildProvider(base Catalog, discovery GrokBuildDiscovery) Catalog {
	merged := make(Catalog, len(base)+1)
	for id, info := range base {
		if id == GrokBuildProviderID {
			continue
		}
		merged[id] = info
	}
	if discovery.State == GrokBuildStateAbsent {
		return merged
	}
	models := map[string]Model{}
	if discovery.State == GrokBuildStateReady {
		for _, id := range discovery.Models {
			models[id] = Model{
				ID: id, Name: id, Reasoning: true, ToolCall: false,
				Modalities: &Modalities{Input: []string{"text"}, Output: []string{"text"}},
			}
		}
	}
	action := GrokBuildActionRetry
	switch discovery.State {
	case GrokBuildStateReady:
		action = GrokBuildActionUseSession
	case GrokBuildStateSignedOut:
		action = GrokBuildActionLogin
	case GrokBuildStateIncompatibleVersion:
		action = GrokBuildActionUpdate
	}
	merged[GrokBuildProviderID] = Info{
		ID: GrokBuildProviderID, Name: GrokBuildSourceLabel,
		Source: &Source{
			Kind: GrokBuildSourceKind, Label: GrokBuildSourceLabel, App: "grok",
			Route: GrokBuildRouteCLIOAuth, AuthMode: GrokBuildAuthModeCLIOAuth,
			DefaultModel:    discovery.DefaultModel,
			CredentialOwner: GrokBuildCredentialOwner, Action: action,
			Revision: discovery.Version, Rank: -100, Current: discovery.State == GrokBuildStateReady,
			Importable: false, Reason: discovery.Reason,
		},
		Models: models,
	}
	return merged
}

type grokBuildDiscoveryCacheState struct {
	sync.Mutex
	at       time.Time
	result   GrokBuildDiscovery
	inFlight chan struct{}
	epoch    uint64
}

var grokBuildDiscoveryCache grokBuildDiscoveryCacheState

func DetectGrokBuild(ctx context.Context) GrokBuildDiscovery {
	return grokBuildDiscoveryCache.detect(ctx, func(ctx context.Context) GrokBuildDiscovery {
		return (GrokBuildDiscoverer{}).Discover(ctx)
	})
}

func (c *grokBuildDiscoveryCacheState) detect(ctx context.Context, discover func(context.Context) GrokBuildDiscovery) GrokBuildDiscovery {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if ctx.Err() != nil {
			return grokBuildCanceledDiscovery()
		}
		c.Lock()
		now := time.Now()
		age := now.Sub(c.at)
		if !c.at.IsZero() && age >= 0 && age < grokBuildCacheTTL {
			result := cloneGrokBuildDiscovery(c.result)
			c.Unlock()
			return result
		}
		if c.inFlight != nil {
			done := c.inFlight
			c.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return grokBuildCanceledDiscovery()
			}
		}

		done := make(chan struct{})
		c.inFlight = done
		epoch := c.epoch
		c.Unlock()

		result := cloneGrokBuildDiscovery(discover(ctx))
		canceled := ctx.Err() != nil
		c.Lock()
		c.inFlight = nil
		if !canceled && c.epoch == epoch {
			c.at = time.Now()
			c.result = cloneGrokBuildDiscovery(result)
		}
		close(done)
		c.Unlock()
		return cloneGrokBuildDiscovery(result)
	}
}

func grokBuildCanceledDiscovery() GrokBuildDiscovery {
	return GrokBuildDiscovery{
		State:  GrokBuildStateProbeFailed,
		Reason: "Grok Build check was canceled. Retry Providers.",
	}
}

func cloneGrokBuildDiscovery(discovery GrokBuildDiscovery) GrokBuildDiscovery {
	discovery.Models = append([]string(nil), discovery.Models...)
	return discovery
}

func InvalidateGrokBuildDiscovery() {
	grokBuildDiscoveryCache.Lock()
	grokBuildDiscoveryCache.epoch++
	grokBuildDiscoveryCache.at = time.Time{}
	grokBuildDiscoveryCache.result = GrokBuildDiscovery{}
	grokBuildDiscoveryCache.Unlock()
}
