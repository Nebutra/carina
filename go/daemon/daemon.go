// Package daemon hosts the long-running Carina control plane: it wires the
// session store, scheduler, worker pool, and model router behind the
// JSON-RPC server, and mediates every side effect through the Rust
// Capability Kernel (carina-kernel-service) and the Zig native toolchain.
package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nebutra/carina/go/auth"
	"github.com/Nebutra/carina/go/contextengine"
	"github.com/Nebutra/carina/go/egress"
	"github.com/Nebutra/carina/go/history"
	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/mcp"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/nebutra"
	"github.com/Nebutra/carina/go/product"
	"github.com/Nebutra/carina/go/provider"
	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	"github.com/Nebutra/carina/go/toolchain"
	"github.com/Nebutra/carina/go/worker"
)

const Version = product.Version

// Options configures external binaries and storage.
type Options struct {
	StateDir  string // session metadata, event logs, snapshots
	KernelBin string // carina-kernel-service path ("" = auto-discover)
	ToolsDir  string // zig tools directory ("" = auto-discover)
	PolicyDir string // enterprise org-policy directory ("" = none)
	Offline   bool   // disable network model providers (PRD §5: offline mode)

	MaxConcurrentTasks int // cap on concurrent background runs (0 => default 8)

	RequireWorkspaceTrust      bool               // when true, deny command exec in untrusted workspaces
	MaxTaskTokens              int                // per-task token budget (0 => unlimited); over-budget runs degrade
	EnableEgressProxy          bool               // route command network through a deny-by-default egress proxy
	EgressAllow                []string           // hosts allowed when the egress proxy is enabled
	SandboxCommands            bool               // run commands under an OS syscall sandbox (macOS sandbox-exec)
	InteractiveApproval        bool               // requires_approval pauses for an operator decision instead of auto-approving
	EnableDebugRPC             bool               // expose local-only debug.* diagnostic RPCs and collect their in-memory trace
	EgressCredentials          []EgressCredential // per-host credentials injected at the egress boundary
	VerifierModel              string             // model for the independent done-verifier ("" => verifier off)
	RiskReviewMode             string             // off|advisory|enforce for autonomous approval review ("" => advisory)
	RiskReviewModel            string             // optional model for Nebutra Risk Review ("" => deterministic local reviewer)
	NebutraCloudEndpoint       string             // Nebutra Cloud identity/sync boundary (default https://nebutra.com)
	NebutraSyncMode            string             // currently only "off"; future sync modes belong behind Nebutra
	GatewayTokenSigningKeyFile string             // optional local file containing Gateway token signing material
	GatewayTokenMaxTTLSeconds  int                // max scoped Gateway token TTL (0 => 15m)
	ContextEngine              string             // auto|off|headroom|noop
	HeadroomBin                string             // optional bundled/override headroom binary path
	HeadroomStateDir           string             // default: <state>/headroom
	HeadroomMode               string             // managed_mcp|sidecar|proxy
	HeadroomProxyPort          int                // 0 => choose later
	HeadroomTokenBudget        int                // budget for context blocks
}

// EgressCredential authenticates outbound requests to a host by injecting a
// header at the egress proxy, sourced from a daemon-side env var (deployment-
// scoped). The agent's command children never receive SecretEnv — carina-run's
// env allowlist excludes it — so the secret stays on the daemon side of the
// boundary.
type EgressCredential struct {
	Host        string // host to authenticate (also unioned into the egress allowlist)
	Header      string // header to set (default Authorization)
	ValuePrefix string // e.g. "Bearer "
	SecretEnv   string // daemon env var holding the secret value
	MITM        bool   // opt this host into HTTPS TLS interception for injection
}

type pendingCommand struct {
	sessionID string
	taskID    string
	argv      []string
}

type pendingMemoryWrite struct {
	sessionID string
	taskID    string
	req       memoryWriteRequest
	scope     memoryScope
	summary   memoryWriteSummary
}

type Daemon struct {
	store        *sessionstore.Store
	sched        *scheduler.Scheduler
	pool         *worker.Pool
	backpressure *backpressureManager
	router       *modelrouter.Router
	server       *rpc.Server
	kern         *kernel.Service
	tools        *toolchain.Toolchain
	events       *Bus
	debugTrace   *debugTrace
	started      time.Time

	org            *kernel.OrgPolicy // enterprise policy (nil when unconfigured)
	policyDir      string            // opts.PolicyDir, kept for doctor's policyBundleStale freshness probe
	stateDir       string
	socketPath     string
	cloudEndpoint  string
	syncMode       string
	reasoner       Reasoner     // agent "thinking" engine (nil => mock loop)
	summarizer     Reasoner     // optional cheaper model for compaction/summarization
	verifier       Reasoner     // optional independent "judge" for done-claims (nil => default-lenient)
	riskReviewer   Reasoner     // optional independent approval reviewer (nil => deterministic heuristic)
	riskReviewMode atomic.Value // string: off|advisory|enforce, hot-reloadable

	mu                  sync.Mutex
	pendingCmds         map[string]pendingCommand     // decision_id -> command awaiting approval
	pendingMemWrites    map[string]pendingMemoryWrite // decision_id -> memory write awaiting approval
	patchGates          map[string]*patchGate         // patch_id -> PatchApply decision state
	patchGateByDecision map[string]string             // decision_id -> patch_id

	runs   *runStore     // durable background-run registry (survives restart)
	runSem chan struct{} // concurrency cap for background runs

	readProv   map[string]map[string]string // session -> relpath -> sha256 of last read (dirty-write guard)
	readProvMu sync.Mutex

	indexBuilt sync.Map // session -> true once the code index was lazily built (code.* tools)

	indexSnapshot sync.Map // session -> *sweepSnapshot from the last index sync (V4 mtime staleness sweep)

	codeIntelStatus sync.Map // session -> codeIntelStatus (V3: semantic-layer health on daemon.status.code_intel)

	embedModelDefault string // "<provider>/<model>" of the default embeddings backend ("" = semantic layer off)

	trust         *trustStore  // trusted workspace roots
	requireTrust  atomic.Bool  // deny command exec in untrusted workspaces (hot-reloadable)
	maxTaskTokens atomic.Int64 // per-task token budget (0 => unlimited; hot-reloadable)

	mailbox   map[string][]string // task -> pending steering messages
	mailboxMu sync.Mutex

	planMode map[string]bool // session -> plan mode (read-only until approved)
	planMu   sync.Mutex

	mcp          *mcp.Manager // external MCP servers (proxied tools, kernel-gated)
	contextEng   contextengine.Engine
	egress       *egress.Proxy // deny-by-default network egress proxy (optional)
	egressURL    string
	egressCAPath string      // process-local CA bundle for MITM-enabled children
	sandbox      atomic.Bool // run commands under an OS syscall sandbox (hot-reloadable)

	stopCh   chan struct{} // closed on Close; stops background loops (lease reaper)
	stopOnce sync.Once
	loopWG   sync.WaitGroup
	taskWG   sync.WaitGroup

	interactiveApproval atomic.Bool                     // when true, requires_approval pauses for an operator decision (hot-reloadable)
	debugRPCEnabled     atomic.Bool                     // exposes debug.* and collects debug trace (hot-reloadable, default off)
	approvalTimeout     time.Duration                   // how long to wait for an interactive approval (0 => 5m)
	pendingApprovals    map[string]chan approvalSignal  // decision_id -> resolver channel
	pendingQuestions    map[string]*pendingUserQuestion // question_id -> blocked ask_user tool
	approvalGrants      *approvalGrantStore             // exact session/project grants, persisted under stateDir
	approvalMu          sync.Mutex
	questionMu          sync.Mutex
	patchGateRetention  time.Duration // how long a resolved patch gate survives before being swept (0 => 1h)

	subagentParentTask map[string]string // childSessionID -> parentTaskID (leader-bridge linkage)
	escalationCounts   map[string]int    // childTaskID -> escalations used (bridge cap)
	bridgeMu           sync.Mutex

	reload func() error // config reload closure (SIGHUP/RPC); nil until SetReloader

	authChain          *auth.Chain              // ordered provider-credential resolver (BYOK -> Nebutra OAuth)
	authStore          *auth.Store              // local BYOK credential store (doctor's per-provider probe)
	providerCatalog    provider.Catalog         // runtime provider catalog (doctor's per-provider probe)
	usage              *usageStore              // durable per-task/session model usage and cost accounting
	history            *history.History         // shared cross-process prompt history
	memory             *memoryStore             // governed local long-term memory
	schedules          *scheduler.ScheduleStore // persistent cron/at/every definitions
	gatewayTokens      *rpc.GatewayTokenIssuer  // optional scoped Gateway token signer/verifier
	gatewayTokenMaxTTL time.Duration            // max TTL for locally issued scoped Gateway tokens
	gatewayHTTPServers []*http.Server
	gatewayResponses   map[string]string // response id -> session id for /v1/responses continuity
}

func New(opts Options) (*Daemon, error) {
	if opts.StateDir == "" {
		opts.StateDir = ".carina-state"
	}
	contextEng, err := contextengine.New(contextengine.Config{
		ContextEngine:       opts.ContextEngine,
		HeadroomBin:         opts.HeadroomBin,
		HeadroomStateDir:    opts.HeadroomStateDir,
		HeadroomMode:        opts.HeadroomMode,
		HeadroomProxyPort:   opts.HeadroomProxyPort,
		HeadroomTokenBudget: opts.HeadroomTokenBudget,
		CarinaStateDir:      opts.StateDir,
	})
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	riskReviewMode := opts.RiskReviewMode
	if riskReviewMode == "" {
		riskReviewMode = os.Getenv("CARINA_RISK_REVIEW_MODE")
	}
	riskReviewMode, err = normalizeRiskReviewMode(riskReviewMode)
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	cloudEndpoint, err := nebutra.NormalizeCloudEndpoint(opts.NebutraCloudEndpoint)
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	syncMode, err := nebutra.NormalizeSyncMode(opts.NebutraSyncMode)
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	gatewayTokenMaxTTL := time.Duration(opts.GatewayTokenMaxTTLSeconds) * time.Second
	if gatewayTokenMaxTTL <= 0 {
		gatewayTokenMaxTTL = 15 * time.Minute
	}
	var gatewayTokens *rpc.GatewayTokenIssuer
	if strings.TrimSpace(opts.GatewayTokenSigningKeyFile) != "" {
		key, err := readGatewayTokenSigningKey(opts.GatewayTokenSigningKeyFile)
		if err != nil {
			return nil, fmt.Errorf("daemon: %w", err)
		}
		gatewayTokens, err = rpc.NewGatewayTokenIssuer(key)
		if err != nil {
			return nil, fmt.Errorf("daemon: gateway token signing key: %w", err)
		}
	}
	store, err := sessionstore.Open(opts.StateDir)
	if err != nil {
		return nil, err
	}
	tools := toolchain.New(opts.ToolsDir)
	// The kernel delegates patch writes to carina-patch-native, so it needs the
	// same tools directory (PRD §4.4).
	kern, err := kernel.Start(opts.KernelBin, opts.StateDir, tools.Dir())
	if err != nil {
		return nil, fmt.Errorf("daemon: cannot start capability kernel: %w", err)
	}
	d := &Daemon{
		store:               store,
		sched:               scheduler.New(),
		pool:                worker.NewPool(),
		backpressure:        newBackpressureManager(),
		router:              modelrouter.New(),
		server:              rpc.NewServer(),
		kern:                kern,
		tools:               tools,
		events:              NewBus(),
		debugTrace:          newDebugTrace(defaultDebugTraceCapacity),
		org:                 loadOrgPolicy(opts.PolicyDir),
		policyDir:           opts.PolicyDir,
		stateDir:            opts.StateDir,
		cloudEndpoint:       cloudEndpoint,
		syncMode:            syncMode,
		started:             time.Now().UTC(),
		pendingCmds:         make(map[string]pendingCommand),
		pendingMemWrites:    make(map[string]pendingMemoryWrite),
		patchGates:          make(map[string]*patchGate),
		patchGateByDecision: make(map[string]string),
		memory:              newMemoryStore(opts.StateDir),
		schedules:           scheduler.OpenScheduleStore(opts.StateDir),
		contextEng:          contextEng,
		gatewayTokens:       gatewayTokens,
		gatewayTokenMaxTTL:  gatewayTokenMaxTTL,
		gatewayResponses:    map[string]string{},
	}
	d.riskReviewMode.Store(riskReviewMode)
	_ = hardenProcess() // Linux: non-dumpable, anti-ptrace (best-effort)
	d.registerMethods()
	authStore, _ := auth.NewStore("")
	// Doctor keeps a single safe provenance string for the primary Anthropic
	// chain. Runtime providers each get their own BYOK/env chain below.
	d.authChain = auth.ProviderChain(
		"anthropic",
		[]string{"ANTHROPIC_API_KEY"},
		authStore,
		func() (string, error) { return os.Getenv("CARINA_NEBUTRA_TOKEN"), nil },
	)
	providerCatalog := loadRuntimeProviderCatalog(opts.Offline)
	d.authStore = authStore
	d.providerCatalog = providerCatalog
	d.usage = newUsageStore(opts.StateDir)
	registerProviders(d.router, opts.Offline, authStore, providerCatalog)
	// Embeddings (V2 semantic layer): BYOK only, credential-gated at
	// registration so no provider means the layer is silently off.
	d.embedModelDefault = registerEmbeddingsProviders(d.router, opts.Offline, authStore)
	// Rerank (V4 §C): same BYOK credential gate; no registered provider means
	// the rerank stage stays off and code.search keeps the kernel order.
	registerRerankProviders(d.router, opts.Offline, authStore)
	// Durable run registry + concurrency cap for background runs. Reloading the
	// registry lets `task.list`/`task.status` answer for runs from before a
	// restart (the run record survives even though the live loop does not yet).
	d.runs = newRunStore(opts.StateDir)
	for _, t := range d.runs.load() {
		d.sched.Load(t)
	}
	maxConcurrent := opts.MaxConcurrentTasks
	if maxConcurrent <= 0 {
		maxConcurrent = 8
	}
	d.runSem = make(chan struct{}, maxConcurrent)
	d.readProv = map[string]map[string]string{}
	d.trust = newTrustStore(opts.StateDir)
	d.requireTrust.Store(opts.RequireWorkspaceTrust)
	d.maxTaskTokens.Store(int64(opts.MaxTaskTokens))
	d.sandbox.Store(opts.SandboxCommands)
	d.mailbox = map[string][]string{}
	d.planMode = map[string]bool{}
	d.stopCh = make(chan struct{})
	d.pendingApprovals = map[string]chan approvalSignal{}
	d.pendingQuestions = map[string]*pendingUserQuestion{}
	d.approvalGrants = newApprovalGrantStore(opts.StateDir)
	d.interactiveApproval.Store(opts.InteractiveApproval)
	d.debugRPCEnabled.Store(opts.EnableDebugRPC)
	d.subagentParentTask = map[string]string{}
	d.escalationCounts = map[string]int{}
	// Shared cross-process prompt history (survives restarts; multiple daemons
	// can append concurrently).
	d.history = history.New(filepath.Join(opts.StateDir, "history"))
	d.startBackgroundLoop(d.reapLeases) // re-queue dispatch tasks abandoned by crashed workers
	d.mcp = mcp.NewManager()
	if _, err := d.connectContextEngineMCP(d.contextEng); err != nil {
		_ = d.kern.Close()
		return nil, fmt.Errorf("daemon: managed Headroom MCP: %w", err)
	}
	if home, err := os.UserHomeDir(); err == nil {
		d.mcp.LoadAndConnect(filepath.Join(home, ".carina", "mcp.json"))
	}
	if opts.EnableEgressProxy {
		allow := append([]string{}, opts.EgressAllow...)
		var inj *egress.Injector
		if len(opts.EgressCredentials) > 0 {
			rules := map[string]egress.InjectionRule{}
			for _, c := range opts.EgressCredentials {
				rules[c.Host] = egress.InjectionRule{Header: c.Header, ValuePrefix: c.ValuePrefix, SecretName: c.SecretEnv, MITM: c.MITM}
				allow = append(allow, c.Host) // an injected host must also be reachable
			}
			// Deployment-scoped resolver: reads the secret from the daemon's env,
			// which carina-run's env allowlist withholds from command children.
			inj = egress.NewInjector(rules, func(name string) (string, bool) {
				v := os.Getenv(name)
				return v, v != ""
			})
			d.egress = egress.NewWithInjector(egress.Allowlist(allow), inj)
		} else {
			d.egress = egress.New(egress.Allowlist(allow))
		}
		url, err := d.egress.Start()
		if err != nil {
			return nil, fmt.Errorf("daemon: start egress proxy: %w", err)
		}
		d.egressURL = url
		if d.egress.MITMEnabled() {
			stateDir, err := filepath.Abs(opts.StateDir)
			if err != nil {
				stateDir = opts.StateDir
			}
			caPath := filepath.Join(stateDir, "egress-ca-bundle.pem")
			if err := d.egress.WriteMITMCABundleFile(caPath); err != nil {
				_ = d.egress.Close()
				return nil, fmt.Errorf("daemon: write egress MITM CA bundle: %w", err)
			}
			d.egressCAPath = caPath
		}
	}
	// Best-effort: wire a reasoner if available and not offline. An explicit
	// CARINA_REASONER_MODEL (for example "openai/gpt-5") selects the
	// model-router reasoner; otherwise Claude CLI remains the preferred local
	// reasoner, with BYOK provider adapters as the fallback.
	if !opts.Offline {
		if model := strings.TrimSpace(os.Getenv("CARINA_REASONER_MODEL")); model != "" {
			d.reasoner = newRouterReasoner(d.router, model)
		} else if r, err := newClaudeCLIReasoner(); err == nil {
			d.reasoner = r
		} else if hasRunnableRuntimeProvider(providerCatalog, authStore) {
			d.reasoner = newRouterReasoner(d.router, "default")
		}
		// Model tiering: an optional cheaper model for compaction/summarization.
		if m := os.Getenv("CARINA_SUMMARIZER_MODEL"); m != "" {
			if r, err := newClaudeCLIReasonerModel(m); err == nil {
				d.summarizer = r
			}
		}
		// Independent done-verifier: a separate model that judges completion.
		vm := opts.VerifierModel
		if vm == "" {
			vm = os.Getenv("CARINA_VERIFIER_MODEL")
		}
		if vm != "" {
			if r, err := newClaudeCLIReasonerModel(vm); err == nil {
				d.verifier = r
			}
		}
		// Nebutra Risk Review: optional model-backed reviewer for autonomous
		// approval requests. Without it, a deterministic local reviewer still
		// records and can enforce obvious high-risk cases.
		rm := opts.RiskReviewModel
		if rm == "" {
			rm = os.Getenv("CARINA_RISK_REVIEW_MODEL")
		}
		if rm != "" {
			if r, err := newClaudeCLIReasonerModel(rm); err == nil {
				d.riskReviewer = r
			}
		}
	}
	d.recover()
	d.resumeRuns()
	d.startBackgroundLoop(d.runScheduleLoop)
	return d, nil
}

func readGatewayTokenSigningKey(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read gateway token signing key %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("gateway token signing key %s is a directory", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("gateway token signing key %s must not be group/world readable", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read gateway token signing key %s: %w", path, err)
	}
	key := bytes.TrimSpace(data)
	if len(key) < 32 {
		return nil, fmt.Errorf("gateway token signing key must be at least 32 bytes")
	}
	return append([]byte(nil), key...), nil
}

// SetReasoner overrides the agent reasoning engine (used by tests).
func (d *Daemon) SetReasoner(r Reasoner) { d.reasoner = r }

// SetSummarizer overrides the (cheaper) summarization engine used for compaction.
func (d *Daemon) SetSummarizer(r Reasoner) { d.summarizer = r }

// SetVerifier overrides the independent done-verifier engine (nil => lenient).
func (d *Daemon) SetVerifier(r Reasoner) { d.verifier = r }

// summarizeReasoner returns the tiered summarizer if configured, else the main
// reasoner — so compaction/summarization can run on a cheaper model.
func (d *Daemon) summarizeReasoner() Reasoner {
	if d.summarizer != nil {
		return d.summarizer
	}
	return d.reasoner
}

// recover re-initializes any sessions that were active when a previous
// daemon exited (PRD §17.3: daemon crash recovery). The event logs already
// persist; here we restore the in-kernel session context so the session can
// continue to be queried and used.
func (d *Daemon) recover() {
	recovered := 0
	for _, sess := range d.store.Recoverable() {
		if err := d.kern.InitSessionFull(sess.SessionID, sess.WorkspaceRoot, sess.PermissionProfile, sess.ApprovalMode, d.org); err != nil {
			continue
		}
		recovered++
	}
	if recovered > 0 {
		fmt.Printf("carina-daemon: recovered %d session(s)\n", recovered)
	}
}

// Run blocks serving JSON-RPC on the unix socket.
func (d *Daemon) Run(socketPath string) error {
	d.socketPath = socketPath
	// A local execution worker and a sandbox worker are always available
	// (PRD §5.4).
	d.pool.Register("local", worker.Local)
	d.pool.Register("sandbox", worker.Sandbox)
	return d.server.ListenUnix(socketPath)
}

// RunTCP additionally serves on a TCP address (remote workers/clients).
func (d *Daemon) RunTCP(addr string) error {
	return d.server.ListenTCP(addr)
}

// RunGatewayWebSocket serves the descriptor-backed Gateway skeleton over
// WebSocket. It is default-off and uses the remote transport allowlist.
func (d *Daemon) RunGatewayWebSocket(addr string, allowedOrigins []string) error {
	if d.gatewayTokens == nil {
		return fmt.Errorf("gateway websocket requires gateway_token_signing_key_file")
	}
	return d.server.ListenWebSocketWithOptions(addr, rpc.WebSocketOptions{
		Path:           "/gateway",
		AllowedOrigins: allowedOrigins,
		TokenVerifier:  d.gatewayTokens,
	})
}

// RunGatewayHTTP serves the OpenAI-compatible and tool-invoke Gateway facade.
// It is default-off and requires scoped Gateway token signing to be configured.
func (d *Daemon) RunGatewayHTTP(addr string, allowedOrigins []string) error {
	return d.runGatewayHTTP(addr, allowedOrigins)
}

func (d *Daemon) Close() error {
	d.stopOnce.Do(func() {
		if d.stopCh != nil {
			close(d.stopCh)
		}
	})
	_ = d.server.Close()
	waitGroupWithTimeout(&d.loopWG, 2*time.Second)
	waitGroupWithTimeout(&d.taskWG, 5*time.Second)
	if d.mcp != nil {
		d.mcp.Close()
	}
	if d.contextEng != nil {
		_ = d.contextEng.Close()
	}
	if d.egress != nil {
		_ = d.egress.Close()
	}
	for _, srv := range d.gatewayHTTPServers {
		_ = srv.Close()
	}
	return d.kern.Close()
}

func (d *Daemon) startBackgroundLoop(fn func()) {
	d.loopWG.Add(1)
	go func() {
		defer d.loopWG.Done()
		fn()
	}()
}

func (d *Daemon) startTask(fn func()) {
	d.taskWG.Add(1)
	go func() {
		defer d.taskWG.Done()
		fn()
	}()
}

func waitGroupWithTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// egressEnv returns the HTTP(S)_PROXY environment for command children when the
// egress proxy is active, so their network is gated deny-by-default; nil when
// the proxy is disabled (children keep direct network).
func (d *Daemon) egressEnv() []string {
	if d.egressURL == "" {
		return nil
	}
	env := []string{
		"HTTP_PROXY=" + d.egressURL, "HTTPS_PROXY=" + d.egressURL,
		"http_proxy=" + d.egressURL, "https_proxy=" + d.egressURL,
		"NO_PROXY=localhost,127.0.0.1", "no_proxy=localhost,127.0.0.1",
	}
	if d.egressCAPath != "" {
		env = append(env,
			"SSL_CERT_FILE="+d.egressCAPath,
			"REQUESTS_CA_BUNDLE="+d.egressCAPath,
			"CURL_CA_BUNDLE="+d.egressCAPath,
			"GIT_SSL_CAINFO="+d.egressCAPath,
			"NODE_EXTRA_CA_CERTS="+d.egressCAPath,
			"CARINA_EGRESS_CA_BUNDLE="+d.egressCAPath,
		)
	}
	return env
}

// Kernel exposes the capability kernel to the agent loop.
func (d *Daemon) Kernel() *kernel.Service { return d.kern }

// Tools exposes the native toolchain to the agent loop.
func (d *Daemon) Tools() *toolchain.Toolchain { return d.tools }

// Router exposes the model router.
func (d *Daemon) Router() *modelrouter.Router { return d.router }

func (d *Daemon) connectContextEngineMCP(eng contextengine.Engine) (bool, error) {
	if d.mcp == nil || eng == nil {
		return false, nil
	}
	connector, ok := eng.(interface {
		ManagedMCPServer() (string, contextengine.MCPServer, bool)
		AttachManagedMCP(contextengine.ManagedMCPAdapter) error
		MarkManagedMCPConnected(error)
	})
	if !ok {
		return false, nil
	}
	name, spec, enabled := connector.ManagedMCPServer()
	if !enabled {
		return false, nil
	}
	err := d.mcp.ConnectPrivate(name, mcp.Server{Command: spec.Command, Args: spec.Args, Env: spec.Env})
	if err == nil {
		err = connector.AttachManagedMCP(d.mcp)
	}
	connector.MarkManagedMCPConnected(err)
	if err != nil {
		d.mcp.Disconnect(name)
	}
	if err != nil && eng.Status().ConfiguredEngine == contextengine.ModeHeadroom {
		return true, err
	}
	return true, nil
}

func (d *Daemon) registerMethods() {
	d.registerRPC("daemon.status", rpc.ScopeRead, true, d.handleStatus)
	d.registerRPC("daemon.metrics", rpc.ScopeRead, true, d.handleMetrics)
	d.registerRPC("daemon.doctor", rpc.ScopeRead, true, d.handleDoctor)
	d.registerRPC("usage.cost", rpc.ScopeRead, true, d.handleUsageCost)
	d.registerRPC("backpressure.status", rpc.ScopeRead, true, d.handleBackpressureStatus)
	d.registerRPC("debug.snapshot", rpc.ScopeAdmin, false, d.handleDebugSnapshot)
	d.registerRPC("debug.correlation.search", rpc.ScopeAdmin, false, d.handleDebugCorrelation)
	d.registerRPC("context.status", rpc.ScopeRead, false, d.handleContextStatus)
	d.registerRPC("context.doctor", rpc.ScopeRead, false, d.handleContextDoctor)
	d.registerRPC("context.stats", rpc.ScopeRead, false, d.handleContextStats)
	d.registerRPC("context.retrieve", rpc.ScopeRead, false, d.handleContextRetrieve)
	d.registerRPC("context.compress", rpc.ScopeWrite, false, d.handleContextCompress)
	d.registerRPC("gateway.hello", rpc.ScopeRead, true, d.handleGatewayHello)
	d.registerRPC("gateway.methods", rpc.ScopeRead, true, d.handleGatewayMethods)
	d.registerRPC("gateway.resolve_scope", rpc.ScopeRead, false, d.handleGatewayResolveScope)
	if d.gatewayTokens != nil {
		d.registerRPC("gateway.token.issue", rpc.ScopeAdmin, false, d.handleGatewayTokenIssue, true)
	}
	d.registerRPC("agent.list", rpc.ScopeRead, true, d.handleAgentList)
	d.registerRPC("command.list", rpc.ScopeRead, true, d.handleCommandList)

	d.registerRPC("session.create", rpc.ScopeWrite, false, d.handleSessionCreate)
	d.registerRPC("session.get", rpc.ScopeRead, true, d.handleSessionGet)
	d.registerRPC("session.list", rpc.ScopeRead, true, d.handleSessionList)
	d.registerRPC("session.pause", rpc.ScopeWrite, false, d.handleSessionPause)
	d.registerRPC("session.resume", rpc.ScopeWrite, false, d.handleSessionResume)
	d.registerRPC("session.close", rpc.ScopeWrite, false, d.handleSessionClose)
	d.registerRPC("session.replay", rpc.ScopeRead, true, d.handleSessionReplay)
	d.registerRPC("session.items", rpc.ScopeRead, true, d.handleSessionItems)
	d.registerRPC("session.attach", rpc.ScopeRead, true, d.handleSessionAttach)
	d.registerRPC("session.fork", rpc.ScopeWrite, false, d.handleSessionFork)
	d.registerRPC("session.plan_mode", rpc.ScopeWrite, false, d.handlePlanMode)
	d.registerRPC("session.approve_plan", rpc.ScopeWrite, false, d.handleApprovePlan)
	d.registerRPCDynamic("session.add_dir", rpc.ScopeAdmin, false, d.handleAddDir, d.addDirScope, true)
	d.registerRPC("task.approval.resolve", rpc.ScopeAdmin, false, d.handleApprovalResolve, true)
	d.registerRPC("task.user.answer", rpc.ScopeWrite, false, d.handleUserAnswer)
	d.registerRPC("task.user.pending", rpc.ScopeRead, false, d.handlePendingUserQuestions)
	d.registerRPC("task.btw", rpc.ScopeWrite, false, d.handleTaskBtw)
	d.registerRPC("history.recent", rpc.ScopeRead, false, d.handleHistoryRecent)
	d.registerRPC("memory.list", rpc.ScopeRead, false, d.handleMemoryList)
	d.registerRPC("memory.context", rpc.ScopeRead, false, d.handleMemoryContext)
	d.registerRPC("memory.search", rpc.ScopeRead, false, d.handleMemorySearch)
	d.registerRPC("memory.status", rpc.ScopeRead, false, d.handleMemoryStatus)
	d.registerRPC("memory.write", rpc.ScopeWrite, false, d.handleMemoryWrite, true)
	d.registerRPC("schedule.create", rpc.ScopeWrite, false, d.handleScheduleCreate, true)
	d.registerRPC("schedule.list", rpc.ScopeRead, false, d.handleScheduleList)
	d.registerRPC("schedule.pause", rpc.ScopeWrite, false, d.handleSchedulePause, true)
	d.registerRPC("schedule.resume", rpc.ScopeWrite, false, d.handleScheduleResume, true)
	d.registerRPC("schedule.delete", rpc.ScopeWrite, false, d.handleScheduleDelete, true)

	d.registerRPC("task.submit", rpc.ScopeWrite, false, d.handleTaskSubmit)
	d.registerRPC("task.status", rpc.ScopeRead, true, d.handleTaskStatus)
	d.registerRPC("task.list", rpc.ScopeRead, true, d.handleTaskList)
	d.registerRPC("task.result", rpc.ScopeRead, true, d.handleTaskResult)
	d.registerRPC("task.cancel", rpc.ScopeWrite, false, d.handleTaskCancel)
	d.registerRPC("task.steer", rpc.ScopeWrite, false, d.handleTaskSteer)
	d.registerRPC("task.action.approve", rpc.ScopeAdmin, false, d.handleApprove, true)
	d.registerRPCDynamic("task.action.deny", rpc.ScopeAdmin, false, d.handleDeny, d.taskActionDenyScope, true)

	d.registerRPC("workspace.tree", rpc.ScopeRead, false, d.handleWorkspaceTree)
	d.registerRPC("workspace.search", rpc.ScopeRead, false, d.handleWorkspaceSearch)
	d.registerRPC("workspace.file.get", rpc.ScopeRead, false, d.handleFileGet)
	d.registerRPCDynamic("workspace.trust", rpc.ScopeAdmin, false, d.handleWorkspaceTrust, workspaceTrustScope, true)
	d.registerRPCDynamic("workspace.patch.propose", rpc.ScopeWrite, false, d.handlePatchPropose, patchProposeScope)
	d.registerRPC("workspace.patch.apply", rpc.ScopeWrite, false, d.handlePatchApply)
	d.registerRPC("workspace.patch.rollback", rpc.ScopeWrite, false, d.handlePatchRollback)
	d.registerRPC("workspace.patch.list", rpc.ScopeRead, false, d.handlePatchList)
	d.registerRPC("workspace.patch.show", rpc.ScopeRead, false, d.handlePatchShow)

	d.registerRPC("command.exec", rpc.ScopeWrite, false, d.handleCommandExec)
	d.registerRPC("audit.report", rpc.ScopeRead, true, d.handleAuditReport)
	d.registerRPC("audit.export", rpc.ScopeRead, true, d.handleAuditExport)
	d.registerRPC("audit.verify", rpc.ScopeRead, true, d.handleAuditVerify)
	d.registerRPC("profile.describe", rpc.ScopeRead, true, d.handleProfileDescribe)
	d.registerRPC("secret.grant", rpc.ScopeAdmin, false, d.handleSecretGrant, true)
	d.registerRPC("secret.request", rpc.ScopeAdmin, false, d.handleSecretRequest, true)
	d.registerRPC("plugin.inspect", rpc.ScopeRead, false, d.handlePluginInspect)
	d.registerRPC("plugin.run", rpc.ScopeAdmin, false, d.handlePluginRun, true)

	d.registerStreamRPC("session.events.stream", rpc.ScopeStream, true, d.handleEventStream)

	d.registerRPC("worker.register", rpc.ScopeWorker, true, d.handleWorkerRegister)
	d.registerRPC("worker.heartbeat", rpc.ScopeWorker, true, d.handleWorkerHeartbeat)
	d.registerRPC("worker.list", rpc.ScopeRead, true, d.handleWorkerList)
	d.registerRPC("worker.revoke", rpc.ScopeWorker, true, d.handleWorkerRevoke, true)
	d.registerRPC("backpressure.report", rpc.ScopeWorker, true, d.handleBackpressureReport)

	// Work-dispatch bridge: enqueue is control-plane (local); poll/renew/report
	// are the remote worker's lease protocol.
	d.registerRPC("work.submit", rpc.ScopeAdmin, false, d.handleWorkSubmit, true)
	d.registerRPC("work.poll", rpc.ScopeWorker, true, d.handleWorkPoll)
	d.registerRPC("work.renew", rpc.ScopeWorker, true, d.handleWorkRenew)
	d.registerRPC("work.report", rpc.ScopeWorker, true, d.handleWorkReport)

	d.registerRPC("daemon.remote.disable", rpc.ScopeAdmin, false, d.handleRemoteDisable, true)
	d.registerRPC("daemon.reload", rpc.ScopeAdmin, false, d.handleReload, true)
	d.server.RequireDescriptors(true)
}

func (d *Daemon) registerRPC(method string, scope rpc.Scope, remote bool, h rpc.Handler, controlPlaneWrite ...bool) {
	d.registerRPCDynamic(method, scope, remote, h, nil, controlPlaneWrite...)
}

func (d *Daemon) registerRPCDynamic(method string, scope rpc.Scope, remote bool, h rpc.Handler, resolver rpc.ScopeResolver, controlPlaneWrite ...bool) {
	desc := rpc.MethodDescriptor{
		Method:            method,
		Scope:             scope,
		Remote:            remote,
		Advertise:         true,
		ControlPlaneWrite: len(controlPlaneWrite) > 0 && controlPlaneWrite[0],
	}
	if err := d.server.RegisterMethodDynamic(desc, h, resolver); err != nil {
		panic(err)
	}
}

func (d *Daemon) registerStreamRPC(method string, scope rpc.Scope, remote bool, h rpc.StreamHandler) {
	desc := rpc.MethodDescriptor{Method: method, Scope: scope, Remote: remote, Advertise: true}
	if err := d.server.RegisterStreamMethod(desc, h); err != nil {
		panic(err)
	}
}

// handleRemoteDisable toggles the remote kill-switch (local-only: it is not on
// the remote allowlist, so a remote caller can never re-enable itself).
func (d *Daemon) handleRemoteDisable(params json.RawMessage) (any, error) {
	var p struct {
		On bool `json:"on"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	d.server.SetRemoteDisabled(p.On)
	return map[string]any{"remote_disabled": p.On}, nil
}

func (d *Daemon) handleAgentList(params json.RawMessage) (any, error) {
	var p struct {
		SessionID     string `json:"session_id"`
		WorkspaceRoot string `json:"workspace_root"`
		IncludeHidden bool   `json:"include_hidden"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	root := p.WorkspaceRoot
	if p.SessionID != "" {
		sess, ok := d.store.Get(p.SessionID)
		if !ok {
			return nil, fmt.Errorf("unknown session %s", p.SessionID)
		}
		root = sess.WorkspaceRoot
	}
	return map[string]any{"agents": sortedAgentInfos(loadAgentSpecs(root), p.IncludeHidden)}, nil
}

func (d *Daemon) handleCommandList(params json.RawMessage) (any, error) {
	var p struct {
		SessionID     string `json:"session_id"`
		WorkspaceRoot string `json:"workspace_root"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	root := p.WorkspaceRoot
	if p.SessionID != "" {
		sess, ok := d.store.Get(p.SessionID)
		if !ok {
			return nil, fmt.Errorf("unknown session %s", p.SessionID)
		}
		root = sess.WorkspaceRoot
	}
	return map[string]any{"commands": sortedCommandInfos(d.commandSpecs(root))}, nil
}

// handleWorkspaceTrust marks a workspace root trusted/untrusted for command
// execution under strict trust mode (local-only).
func (d *Daemon) handleWorkspaceTrust(params json.RawMessage) (any, error) {
	var p struct {
		Root    string `json:"root"`
		Trusted bool   `json:"trusted"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Root == "" {
		return nil, fmt.Errorf("root is required")
	}
	d.trust.setTrust(p.Root, p.Trusted)
	return map[string]any{"root": p.Root, "trusted": p.Trusted}, nil
}

// handleDoctor runs independent health probes and returns a self-diagnosis
// (kernel reachable, native tools present, state dir writable, reasoner
// wired, LSP servers present, BYOK provider keys resolvable, context/index
// health). Honors the CARINA_DOCTOR_DISABLE kill-switch (P1.6): when set,
// returns a minimal disabled report without touching the kernel, tools, or
// any provider credential — the intended behavior for locked-down
// deployments that do not want doctor's probes running at all.
func (d *Daemon) handleDoctor(_ json.RawMessage) (any, error) {
	if doctorDisabled(os.Getenv) {
		return map[string]any{
			"version":  Version,
			"disabled": true,
			"reason":   "CARINA_DOCTOR_DISABLE is set; probes did not run",
		}, nil
	}

	probe := func(fn func() error) map[string]any {
		if err := fn(); err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		return map[string]any{"ok": true}
	}

	byokStatuses := byokProbe(byokProviderList(d.providerCatalog), func(providerID string) bool {
		if d.authStore == nil {
			return false
		}
		_, ok, err := d.authStore.Get(providerID)
		return err == nil && ok
	}, os.Getenv)

	lspStatuses := lspProbe(realLookPath)

	policyStale, policyReason := policyBundleStale(d.policyDir, d.org)

	return map[string]any{
		"version":  Version,
		"disabled": false,
		"kernel":   probe(func() error { _, err := d.kern.ClassifyCommand("echo ok"); return err }),
		"state_dir_writable": probe(func() error {
			f := filepath.Join(d.stateDir, ".doctor")
			if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
				return err
			}
			return os.Remove(f)
		}),
		"tools":    map[string]any{"available": d.tools.Available(), "dir": d.tools.Dir()},
		"reasoner": d.reasoner != nil,
		// Resolved credential SOURCE only — never the value. "" = unauthenticated.
		"auth":           map[string]any{"source": d.authChain.ResolvedSource()},
		"context_engine": d.contextDoctor(),
		"lsp":            map[string]any{"servers": lspStatuses},
		"byok": map[string]any{
			"any_resolved": anyProviderResolved(byokStatuses),
			"providers":    byokStatuses,
		},
		// policy reports whether the enterprise policy bundle loaded at this
		// daemon's startup still matches what is on disk (reload.go
		// intentionally never re-inits kernel/policy wiring on SIGHUP/config
		// reload — only a restart applies a bundle.toml/trusted-keys/
		// approval.json edit). configured is false when no PolicyDir is
		// set at all (nothing to go stale).
		"policy": map[string]any{
			"configured": d.policyDir != "",
			"stale":      policyStale,
			"reason":     policyReason,
		},
	}, nil
}

// ---- daemon ---------------------------------------------------------------

func (d *Daemon) handleStatus(_ json.RawMessage) (any, error) {
	return map[string]any{
		"version":         Version,
		"pid":             os.Getpid(),
		"uptime_seconds":  int(time.Since(d.started).Seconds()),
		"active_sessions": len(d.store.List()),
		"sessions":        len(d.store.List()),
		"queued_tasks":    d.sched.CountByStatus()["queued"],
		"tasks":           d.sched.Count(),
		"active_workers":  len(d.pool.List()),
		"workers":         len(d.pool.List()),
		"backpressure":    d.backpressure.summary(time.Now().UTC()),
		"debug_trace":     map[string]any{"enabled": d.debugRPCEnabled.Load()},
		"tools":           d.tools.Available(),
		"rpc_endpoint":    d.socketPath,
		"event_log_path":  filepath.Join(d.stateDir, "events"),
		"context_engine":  d.contextStatus(),
		"code_intel":      d.codeIntelStatusSnapshot(),
		"nebutra_cloud": map[string]any{
			"endpoint":     d.cloudEndpoint,
			"sync_mode":    d.syncMode,
			"authority":    "identity/sync only; local runtime remains the action authority",
			"sync_enabled": d.syncMode != nebutra.SyncModeOff,
		},
	}, nil
}

func (d *Daemon) handleContextStatus(_ json.RawMessage) (any, error) {
	return d.contextStatus(), nil
}

func (d *Daemon) handleContextDoctor(_ json.RawMessage) (any, error) {
	return d.contextDoctor(), nil
}

func (d *Daemon) handleContextStats(_ json.RawMessage) (any, error) {
	if d.contextEng == nil {
		return map[string]any{
			"local": contextengine.Stats{Engine: contextengine.ModeNoop, Phase: "unconfigured"},
		}, nil
	}
	st, err := d.contextEng.Stats(context.Background())
	if err != nil {
		return nil, err
	}
	out := map[string]any{"local": st}
	if st.Headroom != nil {
		out["headroom"] = st.Headroom
	}
	if st.HeadroomError != "" {
		out["headroom_error"] = st.HeadroomError
	}
	return out, nil
}

func (d *Daemon) handleContextCompress(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		TaskID    string `json:"task_id"`
		Turn      int    `json:"turn"`
		Kind      string `json:"kind"`
		Tool      string `json:"tool"`
		Content   string `json:"content"`
		Pinned    bool   `json:"pinned"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Content == "" {
		return nil, fmt.Errorf("content is required")
	}
	req := contextengine.CompressRequest{
		SessionID: p.SessionID,
		TaskID:    p.TaskID,
		Turn:      p.Turn,
		Kind:      p.Kind,
		Tool:      p.Tool,
		Content:   p.Content,
		Pinned:    p.Pinned,
	}
	if d.contextEng == nil {
		return nil, fmt.Errorf("context engine is not configured")
	}
	res, err := d.contextEng.Compress(context.Background(), req)
	if err != nil {
		return nil, err
	}
	if p.SessionID != "" && d.kern != nil {
		d.record(p.SessionID, "TaskCreated", p.TaskID, "go", map[string]any{
			"status": "context_compressed", "engine": res.Engine, "turn": p.Turn, "kind": p.Kind, "tool": p.Tool,
			"original_bytes": res.OriginalBytes, "compressed_bytes": res.CompressedBytes,
			"original_tokens": res.OriginalTokens, "compressed_tokens": res.CompressedTokens,
			"savings_percent": res.SavingsPercent, "transforms": res.Transforms,
			"original_sha256": res.OriginalSHA256, "original_ref": res.OriginalRef,
		}, "")
	}
	return res, nil
}

func (d *Daemon) handleContextRetrieve(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		TaskID    string `json:"task_id"`
		Hash      string `json:"hash"`
		Ref       string `json:"ref"`
		Query     string `json:"query"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	ref := strings.TrimSpace(p.Hash)
	if ref == "" {
		ref = strings.TrimSpace(p.Ref)
	}
	if ref == "" {
		return nil, fmt.Errorf("hash or ref is required")
	}
	if strings.TrimSpace(p.Query) != "" {
		return nil, fmt.Errorf("context retrieve query is unavailable: pinned Headroom managed MCP supports hash retrieval only")
	}
	if d.contextEng == nil {
		return nil, fmt.Errorf("context engine is not configured")
	}
	res, err := d.contextEng.Retrieve(context.Background(), ref)
	if err != nil {
		return nil, err
	}
	if p.SessionID != "" && d.kern != nil {
		d.record(p.SessionID, "TaskCreated", p.TaskID, "go", map[string]any{
			"status": "context_retrieved", "engine": res.Engine, "ref": res.Ref, "source": res.Source,
			"original_bytes": res.OriginalBytes, "sha256": res.SHA256,
		}, "")
	}
	return res, nil
}

func (d *Daemon) contextStatus() any {
	if d.contextEng == nil {
		return map[string]any{"configured_engine": "noop", "effective_engine": "noop", "phase": "unconfigured"}
	}
	return d.contextEng.Status()
}

func (d *Daemon) contextDoctor() any {
	if d.contextEng == nil {
		return map[string]any{"ok": true, "status": d.contextStatus()}
	}
	return d.contextEng.Doctor()
}

func (d *Daemon) handleMetrics(_ json.RawMessage) (any, error) {
	return map[string]any{
		"version":         Version,
		"uptime_seconds":  int(time.Since(d.started).Seconds()),
		"tasks_by_status": d.sched.CountByStatus(),
		"model_usage":     d.router.UsageByProvider(),
		"subscribers":     d.events.SubscriberCount(),
		"backpressure":    d.backpressure.snapshot(time.Now().UTC()),
		"debug_trace":     d.debugTraceStats(),
	}, nil
}

func (d *Daemon) handleGatewayHello(params json.RawMessage) (any, error) {
	var req rpc.HelloRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	return rpc.BuildHelloResponse(req, Version, d.server.MethodDescriptors())
}

func (d *Daemon) handleGatewayMethods(_ json.RawMessage) (any, error) {
	return map[string]any{
		"version": "1",
		"methods": d.server.MethodDescriptors(),
	}, nil
}

func (d *Daemon) handleGatewayResolveScope(params json.RawMessage) (any, error) {
	var p struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	p.Method = strings.TrimSpace(p.Method)
	if p.Method == "" {
		return nil, fmt.Errorf("method is required")
	}
	scope, dynamic, err := d.server.ResolveScope(p.Method, p.Params)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"method":        p.Method,
		"scope":         scope,
		"dynamic_scope": dynamic,
	}, nil
}

func (d *Daemon) handleGatewayTokenIssue(params json.RawMessage) (any, error) {
	if d.gatewayTokens == nil {
		return nil, fmt.Errorf("gateway token issuing is disabled")
	}
	var p struct {
		Subject    string      `json:"subject"`
		Role       rpc.Role    `json:"role"`
		Scopes     []rpc.Scope `json:"scopes"`
		Routes     []string    `json:"routes"`
		TTLSeconds int64       `json:"ttl_seconds"`
		Transport  string      `json:"transport"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if len(p.Scopes) == 0 {
		return nil, fmt.Errorf("scopes are required")
	}
	ttl := time.Duration(p.TTLSeconds) * time.Second
	if p.TTLSeconds <= 0 {
		ttl = d.gatewayTokenMaxTTL
	}
	if ttl > d.gatewayTokenMaxTTL {
		return nil, fmt.Errorf("ttl_seconds exceeds gateway token max ttl")
	}
	token, claims, err := d.gatewayTokens.IssueWithRoutes(p.Subject, p.Role, p.Scopes, p.Routes, ttl, p.Transport)
	if err != nil {
		return nil, err
	}
	return map[string]any{"token": token, "claims": claims}, nil
}

// ---- sessions -------------------------------------------------------------

func (d *Daemon) handleSessionCreate(params json.RawMessage) (any, error) {
	var p struct {
		WorkspaceRoot string `json:"workspace_root"`
		Profile       string `json:"profile"`
		ApprovalMode  string `json:"approval_mode"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace_root is required")
	}
	if _, err := os.Stat(p.WorkspaceRoot); err != nil {
		return nil, fmt.Errorf("workspace_root: %w", err)
	}
	sess, err := d.store.CreateSessionMode(p.WorkspaceRoot, p.Profile, p.ApprovalMode)
	if err != nil {
		return nil, err
	}
	if err := d.kern.InitSessionFull(sess.SessionID, sess.WorkspaceRoot, sess.PermissionProfile, sess.ApprovalMode, d.org); err != nil {
		return nil, fmt.Errorf("kernel session init: %w", err)
	}
	return sess, nil
}

func (d *Daemon) handleSessionGet(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	sess, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", id)
	}
	return sess, nil
}

func (d *Daemon) handleSessionList(_ json.RawMessage) (any, error) {
	return d.store.List(), nil
}

func (d *Daemon) handleSessionPause(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	current, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", id)
	}
	if current.Status == "closed" {
		return nil, fmt.Errorf("session %s is closed", id)
	}
	if current.Status == "paused" {
		return current, nil
	}
	sess, err := d.store.SetStatus(id, "paused")
	if err != nil {
		return nil, err
	}
	d.record(id, "SessionPaused", "", "go", map[string]any{"reason": "client request"}, "")
	return sess, nil
}

func (d *Daemon) handleSessionResume(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	current, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", id)
	}
	if current.Status == "closed" {
		return nil, fmt.Errorf("session %s is closed", id)
	}
	if err := d.ensureKernelSession(current); err != nil {
		return nil, err
	}
	if current.Status == "active" {
		return current, nil
	}
	sess, err := d.store.SetStatus(id, "active")
	if err != nil {
		return nil, err
	}
	d.record(id, "SessionResumed", "", "go", map[string]any{"reason": "client request"}, "")
	return sess, nil
}

func (d *Daemon) ensureKernelSession(sess *sessionstore.Session) error {
	if _, err := d.kern.ProfileDescribe(sess.SessionID); err == nil {
		return nil
	}
	if err := d.kern.InitSessionFull(sess.SessionID, sess.WorkspaceRoot, sess.PermissionProfile, sess.ApprovalMode, d.org); err != nil {
		return fmt.Errorf("kernel session init: %w", err)
	}
	return nil
}

func (d *Daemon) handleSessionClose(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	sess, err := d.store.SetStatus(id, "closed")
	if err != nil {
		return nil, err
	}
	d.record(id, "SessionClosed", "", "go", map[string]any{"reason": "client request"}, "")
	return sess, nil
}

func (d *Daemon) handleSessionReplay(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.kern.ReadEvents(id)
}

// handleSessionAttach is cursor-based replay for a reconnecting client (attach +
// tail). It returns the events at/after `since` plus a monotonic `cursor` (the
// append-only audit log's length). A client attaches with since=0 to catch up,
// then either re-attaches with since=cursor to poll for more, or subscribes to
// session.events.stream to tail live from that point.
func (d *Daemon) handleSessionAttach(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Since     int    `json:"since"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("session_id required")
	}
	raw, err := d.kern.ReadEvents(p.SessionID)
	if err != nil {
		return nil, err
	}
	var all []json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("attach: decode events: %w", err)
	}
	since := p.Since
	if since < 0 {
		since = 0
	}
	if since > len(all) {
		since = len(all) // cursor ahead of the log (e.g. after a compaction) => nothing new
	}
	return map[string]any{
		"events": all[since:],
		"from":   since,
		"cursor": len(all),
	}, nil
}

// handleSessionFork branches a session: a new session sharing the workspace,
// profile, and approval mode, linked to the source as its parent (lineage), so
// you can explore an alternate line of work without disturbing the original.
func (d *Daemon) handleSessionFork(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	src, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", id)
	}
	child, err := d.store.CreateSubSession(src.WorkspaceRoot, src.PermissionProfile, src.ApprovalMode, src.SessionID, src.Depth+1)
	if err != nil {
		return nil, err
	}
	if err := d.kern.InitSessionFull(child.SessionID, child.WorkspaceRoot, child.PermissionProfile, child.ApprovalMode, d.org); err != nil {
		return nil, fmt.Errorf("fork init: %w", err)
	}
	d.record(child.SessionID, "TaskCreated", "", "go",
		map[string]any{"status": "forked", "parent": src.SessionID}, "")
	return child, nil
}

// handlePlanMode toggles plan mode for a session: while on, the agent may
// explore read-only but edits/commands are blocked until the plan is approved.
func (d *Daemon) handlePlanMode(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		On        bool   `json:"on"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	d.setPlanMode(p.SessionID, p.On)
	return map[string]any{"session_id": p.SessionID, "plan_mode": p.On}, nil
}

// handleAddDir grants a session an additional allowed root (the /add-dir scoped
// grant). Local-only: it is never on the remote allowlist, so a remote caller
// can never widen the sandbox. The directory must already exist.
func (d *Daemon) handleAddDir(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	abs, err := filepath.Abs(p.Path)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("add_dir requires an existing directory: %s", abs)
	}
	if err := d.kern.AddDir(sess.SessionID, abs); err != nil {
		return nil, err
	}
	d.record(sess.SessionID, "TaskCreated", "", "go",
		map[string]any{"status": "dir_granted", "path": abs}, "")
	return map[string]any{"session_id": sess.SessionID, "path": abs, "granted": true}, nil
}

// handleApprovePlan approves the plan and exits plan mode so execution proceeds.
func (d *Daemon) handleApprovePlan(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	d.setPlanMode(id, false)
	return map[string]any{"session_id": id, "plan_mode": false, "approved": true}, nil
}

func (d *Daemon) handleMemoryList(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Target    string `json:"target"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	return d.memory.list(memoryScopeFromSession(sess), p.Target)
}

func (d *Daemon) handleMemoryContext(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	sess, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", id)
	}
	scope := memoryScopeFromSession(sess)
	return map[string]any{
		"scope":   scope,
		"context": d.memory.contextBlock(scope),
	}, nil
}

func (d *Daemon) handleMemorySearch(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Query     string `json:"query"`
		Target    string `json:"target"`
		Limit     int    `json:"limit"`
		Mode      string `json:"mode"`
		Model     string `json:"model"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	return d.searchMemory(memoryScopeFromSession(sess), p.Query, p.Target, p.Limit, p.Mode, p.Model)
}

func (d *Daemon) handleMemoryStatus(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	sess, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", id)
	}
	scope := memoryScopeFromSession(sess)
	semanticProvider := map[string]any{
		"enabled":  false,
		"provider": "local-only",
		"reason":   "external semantic/vector memory provider is not configured in the source-first runtime",
		"contract": "future providers must preserve MemoryWrite policy, local deletion semantics, and Nebutra identity scope",
	}
	if modelID := d.embeddingsModelID(); modelID != "" {
		semanticProvider = map[string]any{
			"enabled":  true,
			"provider": "byok-embeddings",
			"model":    modelID,
			"contract": "semantic memory search uses only curated MemoryWrite-approved entries and the BYOK embeddings router",
		}
	}
	return map[string]any{
		"scope": scope,
		"storage": map[string]any{
			"mode":        "local",
			"memory_path": d.memory.pathFor(scope, memoryTargetMemory),
			"user_path":   d.memory.pathFor(scope, memoryTargetUser),
		},
		"semantic_provider": semanticProvider,
		"nebutra_cloud_sync": map[string]any{
			"enabled":   d.syncMode != nebutra.SyncModeOff,
			"endpoint":  d.cloudEndpoint,
			"sync_mode": d.syncMode,
			"authority": "identity/sync only; local runtime remains the action authority",
			"reason":    "off is the only supported mode until the Nebutra connector exists",
		},
	}, nil
}

func (d *Daemon) handleMemoryWrite(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string            `json:"session_id"`
		Action    string            `json:"action"`
		Target    string            `json:"target"`
		Content   string            `json:"content"`
		OldText   string            `json:"old_text"`
		Ops       []memoryOperation `json:"operations"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	req := memoryWriteRequest{
		Action:     p.Action,
		Target:     p.Target,
		Content:    p.Content,
		OldText:    p.OldText,
		Operations: p.Ops,
	}
	scope := memoryScopeFromSession(sess)
	summary, err := summarizeMemoryWrite(scope, req)
	if err != nil {
		return nil, err
	}
	decision, err := d.kern.Request(sess.SessionID, "MemoryWrite", summary.Resource, "")
	if err != nil {
		return nil, err
	}
	if approved, ok := d.approveFromStoredGrant(sess, decision); ok {
		decision = approved
	}
	switch decision.Decision {
	case "denied":
		return map[string]any{"decision": decision}, nil
	case "requires_approval":
		d.mu.Lock()
		d.pendingMemWrites[decision.DecisionID] = pendingMemoryWrite{
			sessionID: sess.SessionID,
			req:       req,
			scope:     scope,
			summary:   summary,
		}
		d.mu.Unlock()
		return map[string]any{"decision": decision}, nil
	}
	result, err := d.applyMemoryWrite(sess, "", req, decision, scope, summary)
	if err != nil {
		return nil, err
	}
	return map[string]any{"decision": decision, "result": result}, nil
}

type memoryWriteSummary struct {
	Target         string
	Action         string
	ScopeID        string
	Resource       string
	ContentSHA256  string
	OperationCount int
}

func summarizeMemoryWrite(scope memoryScope, req memoryWriteRequest) (memoryWriteSummary, error) {
	target, err := normalizeMemoryTarget(req.Target)
	if err != nil {
		return memoryWriteSummary{}, err
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" && len(req.Operations) > 0 {
		action = "batch"
	}
	switch action {
	case "add", "replace", "remove", "batch":
	default:
		return memoryWriteSummary{}, fmt.Errorf("unsupported memory action %q", action)
	}
	opCount := 1
	if action == "batch" {
		opCount = len(req.Operations)
	}
	contentHash := memoryWriteHash(req)
	scopeID := scope.WorkspaceHash
	if target == memoryTargetUser {
		scopeID = scope.Profile
	}
	resource := fmt.Sprintf(
		"target=%s scope=%s action=%s ops=%d content_sha256=%s",
		target,
		scopeID,
		action,
		opCount,
		contentHash,
	)
	return memoryWriteSummary{
		Target:         target,
		Action:         action,
		ScopeID:        scopeID,
		Resource:       resource,
		ContentSHA256:  contentHash,
		OperationCount: opCount,
	}, nil
}

func memoryWriteHash(req memoryWriteRequest) string {
	payload := struct {
		Action     string            `json:"action"`
		Target     string            `json:"target"`
		Content    string            `json:"content,omitempty"`
		OldText    string            `json:"old_text,omitempty"`
		Operations []memoryOperation `json:"operations,omitempty"`
	}{
		Action:     strings.ToLower(strings.TrimSpace(req.Action)),
		Target:     strings.ToLower(strings.TrimSpace(req.Target)),
		Content:    req.Content,
		OldText:    req.OldText,
		Operations: req.Operations,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (d *Daemon) applyMemoryWrite(sess *sessionstore.Session, taskID string, req memoryWriteRequest, decision *kernel.Decision, scope memoryScope, summary memoryWriteSummary) (memoryWriteResult, error) {
	result, err := d.memory.apply(scope, req)
	if err != nil {
		return memoryWriteResult{}, err
	}
	result.DecisionID = decision.DecisionID
	result.ContentSHA256 = summary.ContentSHA256
	result.OperationCount = summary.OperationCount
	payload := map[string]any{
		"status":          "memory_write",
		"target":          summary.Target,
		"action":          summary.Action,
		"success":         result.Success,
		"usage":           result.Usage,
		"entry_count":     result.EntryCount,
		"operation_count": summary.OperationCount,
		"content_sha256":  summary.ContentSHA256,
		"scope": map[string]any{
			"profile":                result.Scope.Profile,
			"workspace_hash":         result.Scope.WorkspaceHash,
			"identity_source":        result.Scope.IdentitySource,
			"authenticated_identity": result.Scope.Authenticated,
		},
	}
	if !result.Success {
		payload["error"] = result.Error
	}
	d.record(sess.SessionID, "TaskCreated", taskID, "go", payload, decision.DecisionID)
	return result, nil
}

func (d *Daemon) setPlanMode(sessionID string, on bool) {
	d.planMu.Lock()
	defer d.planMu.Unlock()
	if on {
		d.planMode[sessionID] = true
	} else {
		delete(d.planMode, sessionID)
	}
}

func (d *Daemon) isPlanMode(sessionID string) bool {
	d.planMu.Lock()
	defer d.planMu.Unlock()
	return d.planMode[sessionID]
}

// ---- tasks ----------------------------------------------------------------

func (d *Daemon) handleTaskSubmit(params json.RawMessage) (any, error) {
	var p struct {
		SessionID       string                   `json:"session_id"`
		Prompt          string                   `json:"prompt"`
		Model           string                   `json:"model"`
		Agent           string                   `json:"agent"`
		SuccessCriteria []scheduler.SuccessCheck `json:"success_criteria"`
		OutputSchema    []string                 `json:"output_schema"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	if sess.Status != "active" {
		return nil, fmt.Errorf("session %s is %s, not active", p.SessionID, sess.Status)
	}
	prompt := p.Prompt
	model := p.Model
	agent := p.Agent
	if expanded, ok, err := d.expandTaskSlashCommand(prompt, sess.WorkspaceRoot); err != nil {
		return nil, err
	} else if ok {
		prompt = expanded.Prompt
		if agent == "" {
			agent = expanded.Agent
		}
		if model == "" {
			model = expanded.Model
		}
		d.record(sess.SessionID, "TaskCreated", "", "go",
			map[string]any{"status": "command_expanded", "command": expanded.Name}, "")
	}
	if agent == "" {
		agent = "build"
	}
	agents := loadAgentSpecs(sess.WorkspaceRoot)
	spec := agents[agent]
	if spec == nil {
		return nil, fmt.Errorf("unknown agent %q", agent)
	}
	if model == "" {
		model = spec.Model
	}
	task := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, prompt, model, agent, p.SuccessCriteria)
	d.sched.SetMode(task.TaskID, "background")
	if len(p.OutputSchema) > 0 {
		d.sched.SetOutputSchema(task.TaskID, p.OutputSchema)
	}
	// Write-ahead (P1.8): the defining instruction must be durably
	// audit-chain-appended BEFORE any goroutine is dispatched to act on it,
	// and — unlike every other d.record() call site, which is fire-and-
	// forget — a FAILED append here must refuse the submission rather than
	// let an ungoverned task run whose instruction the audit trail can
	// never attest to. Call the kernel directly (bypassing d.record, whose
	// signature intentionally swallows the error for its many best-effort
	// callers) so this one write-ahead call can be checked.
	writeAheadPayload := map[string]any{"task_id": task.TaskID, "user_prompt": task.UserPrompt, "model": task.Model, "agent": task.Agent}
	if err := d.kern.RecordEvent(sess.SessionID, "TaskCreated", task.TaskID, "go", writeAheadPayload, ""); err != nil {
		_, _ = d.sched.Cancel(task.TaskID)
		return nil, fmt.Errorf("task_submit_failed: write-ahead audit-chain append failed, task was not dispatched: %w", err)
	}
	d.events.Publish(sess.SessionID, map[string]any{
		"session_id": sess.SessionID, "task_id": task.TaskID, "type": "TaskCreated", "actor": "go",
		"timestamp": time.Now().UTC().Format(time.RFC3339), "payload": writeAheadPayload,
	})
	_ = d.history.Append(prompt) // shared cross-process prompt history (best-effort)
	d.persistRun(task.TaskID)

	d.startTask(func() { d.runTaskGuarded(sess, task) })
	if t, ok := d.sched.Get(task.TaskID); ok {
		return t, nil
	}
	return task, nil
}

func (d *Daemon) handleTaskStatus(params json.RawMessage) (any, error) {
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	task, ok := d.sched.Get(p.TaskID)
	if !ok {
		return nil, fmt.Errorf("unknown task %s", p.TaskID)
	}
	return task, nil
}

func (d *Daemon) handleTaskCancel(params json.RawMessage) (any, error) {
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	task, err := d.sched.Cancel(p.TaskID)
	if err != nil {
		return nil, err
	}
	d.emitCompletion(task.SessionID, task)
	return task, nil
}

// handleTaskSteer queues a steering message for a running task; the agent loop
// drains it at the next turn boundary and folds it into the transcript, so you
// can redirect a running (background) agent without restarting it.
func (d *Daemon) handleTaskSteer(params json.RawMessage) (any, error) {
	var p struct {
		TaskID  string `json:"task_id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	p.TaskID = strings.TrimSpace(p.TaskID)
	p.Message = strings.TrimSpace(p.Message)
	if p.TaskID == "" || p.Message == "" {
		return nil, fmt.Errorf("task_id and message are required")
	}
	task, ok := d.sched.Get(p.TaskID)
	if !ok {
		return nil, fmt.Errorf("unknown task %s", p.TaskID)
	}
	switch task.Status {
	case "queued", "running", "waiting_approval":
	default:
		return nil, fmt.Errorf("task %s is %s and cannot be steered", p.TaskID, task.Status)
	}
	d.steer(p.TaskID, p.Message)
	return map[string]any{"queued": true, "task_id": p.TaskID, "status": task.Status}, nil
}

func (d *Daemon) steer(taskID, message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	d.mailboxMu.Lock()
	d.mailbox[taskID] = append(d.mailbox[taskID], message)
	d.mailboxMu.Unlock()
}

// drainMailbox returns and clears a task's pending steering messages.
func (d *Daemon) drainMailbox(taskID string) []string {
	d.mailboxMu.Lock()
	defer d.mailboxMu.Unlock()
	msgs := d.mailbox[taskID]
	delete(d.mailbox, taskID)
	return msgs
}

// handleTaskList returns the background-run registry, optionally filtered by
// session or status — the "check back later" surface for background agents.
func (d *Daemon) handleTaskList(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Status    string `json:"status"`
	}
	_ = json.Unmarshal(params, &p) // all filters optional
	all := d.sched.List()
	out := make([]*scheduler.Task, 0, len(all))
	for _, t := range all {
		if p.SessionID != "" && t.SessionID != p.SessionID {
			continue
		}
		if p.Status != "" && t.Status != p.Status {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// handleTaskResult returns one run record: status, summary, and applied patches.
func (d *Daemon) handleTaskResult(params json.RawMessage) (any, error) {
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	t, ok := d.sched.Get(p.TaskID)
	if !ok {
		return nil, fmt.Errorf("unknown task %s", p.TaskID)
	}
	return t, nil
}

// persistRun snapshots a task's current record to the durable run store.
func (d *Daemon) persistRun(taskID string) {
	if t, ok := d.sched.Get(taskID); ok {
		d.runs.save(t)
	}
}

// recordRead notes the hash of content the agent read for a path, so a later
// blind or stale full-file overwrite (a dirty write) can be caught.
func (d *Daemon) recordRead(sessionID, path, content string) {
	h := sha256.Sum256([]byte(content))
	d.readProvMu.Lock()
	defer d.readProvMu.Unlock()
	if d.readProv[sessionID] == nil {
		d.readProv[sessionID] = map[string]string{}
	}
	d.readProv[sessionID][path] = hex.EncodeToString(h[:])
}

// checkWriteProvenance rejects a full-file overwrite that would clobber an
// existing file the agent never read, or one that drifted since it was last
// read (a concurrent agent/hook/formatter touched it). New files are allowed.
func (d *Daemon) checkWriteProvenance(sessionID, relpath, abspath string) error {
	cur, err := os.ReadFile(abspath)
	if err != nil {
		return nil // file does not exist yet — nothing to clobber
	}
	sum := sha256.Sum256(cur)
	curHash := hex.EncodeToString(sum[:])
	d.readProvMu.Lock()
	seen := ""
	if m := d.readProv[sessionID]; m != nil {
		seen = m[relpath]
	}
	d.readProvMu.Unlock()
	if seen == "" {
		return fmt.Errorf("refusing blind overwrite of existing file %q — read it first", relpath)
	}
	if seen != curHash {
		return fmt.Errorf("stale write: %q changed since you last read it — re-read before editing", relpath)
	}
	return nil
}

// guardRun runs a background agent function under a concurrency cap and a panic
// guard: a panic marks that one run failed (recorded + persisted) instead of
// crashing the daemon and taking every other run with it.
func (d *Daemon) guardRun(sess *sessionstore.Session, task *scheduler.Task, run func()) {
	d.runSem <- struct{}{}
	defer func() { <-d.runSem }()
	defer func() {
		if r := recover(); r != nil {
			d.sched.SetStatus(task.TaskID, "failed")
			d.sched.SetResult(task.TaskID, fmt.Sprintf("panic: %v", r), nil)
			d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
				map[string]any{"status": "failed", "reason": "panic recovered"}, "")
			d.persistRun(task.TaskID)
		}
	}()
	run()
	d.persistRun(task.TaskID)
}

func (d *Daemon) runTaskGuarded(sess *sessionstore.Session, task *scheduler.Task) {
	d.guardRun(sess, task, func() { d.runTask(sess, task) })
}

func (d *Daemon) resumeTaskGuarded(sess *sessionstore.Session, task *scheduler.Task, cp *runCheckpoint) {
	d.guardRun(sess, task, func() { d.resumeTask(sess, task, cp) })
}

// markInterrupted records that a mid-flight run could not be resumed after a
// restart (its session vanished, or it had no checkpoint to resume from).
func (d *Daemon) markInterrupted(task *scheduler.Task, reason string) {
	d.sched.SetStatus(task.TaskID, "degraded")
	d.sched.SetResult(task.TaskID, "interrupted by daemon restart: "+reason, nil)
	d.persistRun(task.TaskID)
}

// resumeRuns relaunches background runs that were mid-flight when the daemon
// stopped. A run with a transcript checkpoint continues from its next turn; one
// without a checkpoint is marked interrupted rather than blindly re-run (which
// could duplicate side effects). It requires a reasoner — otherwise a no-op, so
// the run stays "running" until a reasoner-backed daemon picks it up.
func (d *Daemon) resumeRuns() {
	if d.reasoner == nil {
		return
	}
	resumed := 0
	for _, task := range d.sched.List() {
		if task.Status != "running" {
			continue
		}
		sess, ok := d.store.Get(task.SessionID)
		if !ok {
			d.markInterrupted(task, "session gone")
			continue
		}
		cp := d.runs.loadCheckpoint(task.TaskID)
		if cp == nil {
			d.markInterrupted(task, "no checkpoint")
			continue
		}
		sessCopy, taskCopy, cpCopy := sess, task, cp
		d.startTask(func() { d.resumeTaskGuarded(sessCopy, taskCopy, cpCopy) })
		resumed++
	}
	if resumed > 0 {
		fmt.Printf("carina-daemon: resumed %d background run(s)\n", resumed)
	}
}

// ---- approvals ------------------------------------------------------------

func (d *Daemon) handleApprove(params json.RawMessage) (any, error) {
	var p struct {
		SessionID  string `json:"session_id"`
		DecisionID string `json:"decision_id"`
		Approver   string `json:"approver"`
		Role       string `json:"role"`
		Scope      string `json:"scope"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Approver == "" {
		p.Approver = "user"
	}
	scope, err := normalizeApprovalScope(p.Scope)
	if err != nil {
		return nil, err
	}
	// A patch gate's approval window is enforced regardless of call order:
	// checkPatchGate only discovers an elapsed window when
	// workspace.patch.apply is actually called, so a late approval arriving
	// here first — before any apply attempt — must not be allowed to flip a
	// stale "requires_approval" gate straight to "allowed". Refuse and
	// expire it here too, before ever asking the kernel to approve it.
	if patchID, expired := d.expirePatchGateIfStale(p.SessionID, p.DecisionID); expired {
		d.recordPatchRefusal(p.SessionID, patchID, p.DecisionID, "approval_expired")
		return nil, fmt.Errorf("approval_expired: patch %s was not applied. decision %s expired before approval; propose the patch again to request a new decision.", patchID, p.DecisionID)
	}

	decision, err := d.kern.ApproveWithRole(p.SessionID, p.DecisionID, p.Approver, p.Role)
	if err != nil {
		return nil, err
	}
	actualScope := scope
	grantError := ""
	if decision.Decision == "allowed" && scope != approvalScopeOnce {
		sess, ok := d.store.Get(p.SessionID)
		if !ok {
			actualScope = approvalScopeOnce
			grantError = "unknown session " + p.SessionID
		} else if err := d.rememberApprovalGrant(sess, decision, scope, p.Approver, p.Role); err != nil {
			actualScope = approvalScopeOnce
			grantError = err.Error()
			d.record(p.SessionID, "ToolApproved", "", "go", map[string]any{
				"status": "approval_grant_failed", "requested_scope": scope, "error": grantError,
			}, p.DecisionID)
		}
	}
	response := func(result any) map[string]any {
		out := map[string]any{"decision": decision, "scope": actualScope}
		if result != nil {
			out["result"] = result
		}
		if grantError != "" {
			out["grant_error"] = grantError
		}
		return out
	}
	// Unblock a live awaitInteractiveApproval wait on this decision (an
	// agent-originated requires_approval pause), if one is pending. This is
	// the RPC surface the TUI's approval overlay calls (task.action.approve)
	// — it must resolve the same wait task.approval.resolve does, or the
	// operator's verdict is recorded as allowed while the gated action still
	// times out to denied.
	d.signalPendingApproval(p.DecisionID, decision, decision.Decision == "allowed", actualScope)
	// A role-rejected approval does not execute the pending command.
	if decision.Decision != "allowed" {
		return response(nil), nil
	}

	// If the approval unblocks a queued command, execute it now.
	d.mu.Lock()
	pending, ok := d.pendingCmds[p.DecisionID]
	delete(d.pendingCmds, p.DecisionID)
	d.mu.Unlock()
	if ok {
		result, err := d.executeCommand(pending.sessionID, pending.taskID, pending.argv, decision)
		if err != nil {
			return nil, err
		}
		return response(result), nil
	}
	d.mu.Lock()
	memPending, ok := d.pendingMemWrites[p.DecisionID]
	delete(d.pendingMemWrites, p.DecisionID)
	d.mu.Unlock()
	if ok {
		sess, ok := d.store.Get(memPending.sessionID)
		if !ok {
			return nil, fmt.Errorf("unknown session %s", memPending.sessionID)
		}
		result, err := d.applyMemoryWrite(sess, memPending.taskID, memPending.req, decision, memPending.scope, memPending.summary)
		if err != nil {
			return nil, err
		}
		return response(result), nil
	}
	// If the approval resolves a patch gate, unlock the apply for that patch.
	d.mu.Lock()
	if patchID, ok := d.patchGateByDecision[p.DecisionID]; ok {
		if gate := d.patchGates[patchID]; gate != nil && gate.status == "requires_approval" {
			gate.status = "allowed"
		}
		d.mu.Unlock()
		out := response(nil)
		out["patch_id"] = patchID
		return out, nil
	}
	d.mu.Unlock()
	return response(nil), nil
}

func (d *Daemon) handleDeny(params json.RawMessage) (any, error) {
	var p struct {
		SessionID  string `json:"session_id"`
		DecisionID string `json:"decision_id"`
		Approver   string `json:"approver"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Approver == "" {
		p.Approver = "user"
	}
	denied, err := d.kern.Deny(p.SessionID, p.DecisionID, p.Approver, p.Reason)
	if err != nil {
		return nil, err
	}
	// Unblock a live awaitInteractiveApproval wait on this decision, same as
	// handleApprove — a TUI deny must resolve the agent's pause immediately,
	// not leave it to time out.
	d.signalPendingApproval(p.DecisionID, denied, false, approvalScopeOnce)
	d.mu.Lock()
	delete(d.pendingCmds, p.DecisionID)
	delete(d.pendingMemWrites, p.DecisionID)
	// A denied patch gate refuses every later apply of that patch.
	if patchID, ok := d.patchGateByDecision[p.DecisionID]; ok {
		if gate := d.patchGates[patchID]; gate != nil && gate.status == "requires_approval" {
			gate.status = "denied"
		}
	}
	d.mu.Unlock()
	return denied, nil
}

// ---- workspace ------------------------------------------------------------

func (d *Daemon) handleWorkspaceTree(params json.RawMessage) (any, error) {
	sess, _, err := d.session(params)
	if err != nil {
		return nil, err
	}
	decision, err := d.kern.Request(sess.SessionID, "FileRead", sess.WorkspaceRoot, "")
	if err != nil {
		return nil, err
	}
	if decision.Decision != "allowed" {
		return nil, fmt.Errorf("denied: %s", decision.Reason)
	}
	return d.tools.Scan(sess.WorkspaceRoot)
}

func (d *Daemon) handleWorkspaceSearch(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Pattern   string `json:"pattern"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	decision, err := d.kern.Request(sess.SessionID, "FileRead", sess.WorkspaceRoot, "")
	if err != nil {
		return nil, err
	}
	if decision.Decision != "allowed" {
		return nil, fmt.Errorf("denied: %s", decision.Reason)
	}
	return d.tools.Grep(p.Pattern, sess.WorkspaceRoot)
}

func (d *Daemon) handleFileGet(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	abs := p.Path
	if !strings.HasPrefix(abs, "/") {
		abs = sess.WorkspaceRoot + "/" + p.Path
	}
	decision, err := d.kern.Request(sess.SessionID, "FileRead", abs, "")
	if err != nil {
		return nil, err
	}
	if decision.Decision != "allowed" {
		return nil, fmt.Errorf("denied: %s", decision.Reason)
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(content)
	d.record(sess.SessionID, "FileRead", "", "go",
		map[string]any{"path": abs, "bytes": len(content)}, decision.DecisionID)
	return map[string]any{"content": string(content), "hash": hex.EncodeToString(sum[:])}, nil
}

func (d *Daemon) addDirScope(params json.RawMessage) (rpc.Scope, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	sessionID := strings.TrimSpace(p.SessionID)
	path := strings.TrimSpace(p.Path)
	if sessionID == "" || sessionID != p.SessionID || path == "" || path != p.Path || !filepath.IsAbs(path) {
		return rpc.ScopeAdmin, nil
	}
	sess, ok := d.store.Get(sessionID)
	if !ok {
		return rpc.ScopeAdmin, nil
	}
	root, ok := canonicalExistingDir(sess.WorkspaceRoot)
	if !ok {
		return rpc.ScopeAdmin, nil
	}
	target, ok := canonicalExistingDir(path)
	if !ok {
		return rpc.ScopeAdmin, nil
	}
	if pathWithin(root, target) {
		return rpc.ScopeWrite, nil
	}
	return rpc.ScopeAdmin, nil
}

func workspaceTrustScope(params json.RawMessage) (rpc.Scope, error) {
	var p struct {
		Root    string `json:"root"`
		Trusted bool   `json:"trusted"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	root := strings.TrimSpace(p.Root)
	if root == "" || root != p.Root || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return rpc.ScopeAdmin, nil
	}
	if p.Trusted {
		return rpc.ScopeAdmin, nil
	}
	return rpc.ScopeWrite, nil
}

func (d *Daemon) taskActionDenyScope(params json.RawMessage) (rpc.Scope, error) {
	var p struct {
		SessionID  string `json:"session_id"`
		DecisionID string `json:"decision_id"`
		Approver   string `json:"approver"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	sessionID := strings.TrimSpace(p.SessionID)
	decisionID := strings.TrimSpace(p.DecisionID)
	if sessionID == "" || sessionID != p.SessionID || decisionID == "" || decisionID != p.DecisionID {
		return rpc.ScopeAdmin, nil
	}
	if strings.TrimSpace(p.Approver) != "" {
		return rpc.ScopeAdmin, nil
	}
	if _, ok := d.store.Get(sessionID); !ok {
		return rpc.ScopeAdmin, nil
	}
	return rpc.ScopeWrite, nil
}

func canonicalExistingDir(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return filepath.Clean(real), true
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// ---- patches --------------------------------------------------------------

func patchProposeScope(params json.RawMessage) (rpc.Scope, error) {
	var p struct {
		Files []kernel.FileChange `json:"files"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if len(p.Files) == 0 {
		return rpc.ScopeAdmin, nil
	}
	for _, f := range p.Files {
		if patchPathNeedsAdmin(f.Path) {
			return rpc.ScopeAdmin, nil
		}
	}
	return rpc.ScopeWrite, nil
}

func patchPathNeedsAdmin(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) == "." {
		return true
	}
	for _, part := range strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == ".." {
			return true
		}
	}
	return false
}

// patchGate is the PatchApply capability decision minted when a patch is
// proposed. workspace.patch.apply verifies it instead of letting the kernel
// record a fabricated approval at apply time (the governance gap found by the
// TUI spikes — docs/plans/tui-stack-decision.md, spike verdict).
type patchGate struct {
	sessionID  string
	patchID    string
	decisionID string
	status     string // requires_approval | allowed | denied | expired
	requested  time.Time
}

func (d *Daemon) handlePatchPropose(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string              `json:"session_id"`
		TaskID    string              `json:"task_id"`
		Reason    string              `json:"reason"`
		Files     []kernel.FileChange `json:"files"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	patch, err := d.kern.PatchPropose(p.SessionID, p.TaskID, p.Reason, p.Files)
	if err != nil {
		return nil, err
	}
	// Gate the future apply now: the PatchApply decision travels with the
	// proposal so approval resolves a real decision_id, and apply can verify
	// that the approval actually happened.
	decision, err := d.registerPatchGate(p.SessionID, patch.PatchID, p.TaskID)
	if err != nil {
		return nil, err
	}
	return patchWithApplyDecision(patch, decision)
}

// registerPatchGate requests the PatchApply capability for a proposed patch
// and remembers the decision so workspace.patch.apply can check it.
// defaultPatchGateRetention bounds how long a resolved (terminal) patch gate
// is kept around purely for idempotent status queries/retries, so a
// long-running daemon's patchGates/patchGateByDecision maps do not grow
// without bound as an agent proposes many patches over its lifetime.
const defaultPatchGateRetention = time.Hour

func (d *Daemon) registerPatchGate(sessionID, patchID, taskID string) (*kernel.Decision, error) {
	decision, err := d.kern.Request(sessionID, "PatchApply", patchID, taskID)
	if err != nil {
		return nil, err
	}
	if sess, ok := d.store.Get(sessionID); ok {
		if approved, matched := d.approveFromStoredGrant(sess, decision); matched {
			decision = approved
		}
	}
	d.mu.Lock()
	d.sweepPatchGatesLocked()
	d.patchGates[patchID] = &patchGate{
		sessionID:  sessionID,
		patchID:    patchID,
		decisionID: decision.DecisionID,
		status:     decision.Decision,
		requested:  time.Now(),
	}
	d.patchGateByDecision[decision.DecisionID] = patchID
	d.mu.Unlock()
	return decision, nil
}

// sweepPatchGatesLocked deletes patch gates that have both reached a
// terminal state (allowed, denied, expired — never "requires_approval",
// which must stay reachable until it resolves) and aged past the retention
// window. Callers must hold d.mu. Piggybacked on registration rather than a
// background goroutine: the only operation that grows the maps is also a
// natural, low-frequency point to shrink them, with no extra goroutine
// lifecycle to manage or leak.
func (d *Daemon) sweepPatchGatesLocked() {
	retention := d.patchGateRetention
	if retention <= 0 {
		retention = defaultPatchGateRetention
	}
	now := time.Now()
	for patchID, gate := range d.patchGates {
		if gate.status == "requires_approval" {
			continue
		}
		if now.Sub(gate.requested) <= retention {
			continue
		}
		delete(d.patchGates, patchID)
		delete(d.patchGateByDecision, gate.decisionID)
	}
}

// patchWithApplyDecision returns the patch JSON with the gate decision merged
// in as apply_decision, so clients learn the decision_id they must resolve.
func patchWithApplyDecision(patch *kernel.Patch, decision *kernel.Decision) (any, error) {
	raw, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out["apply_decision"] = decision
	return out, nil
}

func (d *Daemon) handlePatchApply(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		PatchID   string `json:"patch_id"`
		Approver  string `json:"approver"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Approver == "" {
		p.Approver = "user"
	}
	if err := d.checkPatchGate(p.SessionID, p.PatchID); err != nil {
		return nil, err
	}
	return d.kern.PatchApply(p.SessionID, p.PatchID, p.Approver)
}

// checkPatchGate refuses a patch apply unless its PatchApply decision was
// resolved to allowed. Pending, denied, and expired decisions refuse with a
// Governed-register error and a PolicyViolation audit event — the refusal is
// always observable, never silently swallowed.
func (d *Daemon) checkPatchGate(sessionID, patchID string) error {
	d.mu.Lock()
	_, ok := d.patchGates[patchID]
	d.mu.Unlock()

	if !ok {
		// No gate on record (the patch was proposed outside
		// workspace.patch.propose): mint the decision now instead of trusting
		// the caller — an unapproved apply still refuses below.
		if _, err := d.registerPatchGate(sessionID, patchID, ""); err != nil {
			return err
		}
	}

	status, decisionID := d.expirePatchGateStatus(sessionID, patchID)

	switch status {
	case "allowed":
		return nil
	case "denied":
		d.recordPatchRefusal(sessionID, patchID, decisionID, "approval_denied")
		return fmt.Errorf("approval_denied: patch %s was not applied. decision %s was denied.", patchID, decisionID)
	case "expired":
		d.recordPatchRefusal(sessionID, patchID, decisionID, "approval_expired")
		return fmt.Errorf("approval_expired: patch %s was not applied. decision %s expired before approval; propose the patch again to request a new decision.", patchID, decisionID)
	default: // requires_approval
		d.recordPatchRefusal(sessionID, patchID, decisionID, "approval_required")
		return fmt.Errorf("approval_required: patch %s was not applied. decision %s is awaiting approval; resolve it with task.action.approve or task.action.deny.", patchID, decisionID)
	}
}

// expirePatchGateStatus reads a patch gate's current status, lazily flipping
// it (and denying the underlying kernel decision, so the expiry is attested
// in the audit chain rather than just a daemon-side state flip) from
// "requires_approval" to "expired" if the approval window has already
// elapsed. Both checkPatchGate (the apply path) and handleApprove (the
// approve path) must apply this same window regardless of which is called
// first — a late approval must not be able to race ahead of an apply that
// would have caught the expiry.
func (d *Daemon) expirePatchGateStatus(sessionID, patchID string) (status, decisionID string) {
	window := d.approvalTimeout
	if window <= 0 {
		window = defaultApprovalTimeout
	}

	d.mu.Lock()
	gate, ok := d.patchGates[patchID]
	expiredNow := false
	if ok && gate.status == "requires_approval" && time.Since(gate.requested) > window {
		gate.status = "expired"
		expiredNow = true
	}
	if ok {
		status, decisionID = gate.status, gate.decisionID
	}
	d.mu.Unlock()

	if expiredNow {
		// Two callers (an apply via checkPatchGate and an approve via
		// expirePatchGateIfStale) can both observe "requires_approval"
		// before either flips it above, so both land here and both call
		// Deny on the same decision_id; only the first actually resolves
		// it; the kernel refuses the second with no pending decision left
		// to deny. That is expected under the race, but it must never be
		// silently discarded — the failure is recorded as its own
		// PolicyViolation so the audit trail shows a kernel-side attestation
		// gap instead of nothing at all.
		if _, err := d.kern.Deny(sessionID, decisionID, "system", "approval window expired before the patch was applied"); err != nil {
			d.record(sessionID, "PolicyViolation", "", "go",
				map[string]any{
					"capability": "PatchApply", "patch_id": patchID, "decision_id": decisionID,
					"refusal": "expiry_deny_failed", "error": err.Error(),
				}, decisionID)
		}
	}
	return status, decisionID
}

// expirePatchGateIfStale reports whether decisionID gates a patch whose
// approval window has already elapsed, expiring it as a side effect if so.
// Used by handleApprove to refuse (and audit) a late approval before ever
// asking the kernel to approve a decision whose gate is already stale.
func (d *Daemon) expirePatchGateIfStale(sessionID, decisionID string) (patchID string, expired bool) {
	d.mu.Lock()
	patchID, ok := d.patchGateByDecision[decisionID]
	d.mu.Unlock()
	if !ok {
		return "", false
	}
	status, _ := d.expirePatchGateStatus(sessionID, patchID)
	return patchID, status == "expired"
}

// recordPatchRefusal writes the audit event for a refused patch apply.
func (d *Daemon) recordPatchRefusal(sessionID, patchID, decisionID, code string) {
	d.record(sessionID, "PolicyViolation", "", "go",
		map[string]any{"capability": "PatchApply", "patch_id": patchID, "refusal": code}, decisionID)
}

func (d *Daemon) handlePatchRollback(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		PatchID   string `json:"patch_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	patch, err := d.kern.PatchRollback(p.SessionID, p.PatchID)
	if err != nil {
		return nil, err
	}
	// Keep the code index in step with the restore (best-effort; an index
	// error never fails the rollback).
	d.invalidateIndex(p.SessionID, patch.AffectedFiles)
	return patch, nil
}

func (d *Daemon) handlePatchList(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.kern.PatchList(id)
}

func (d *Daemon) handlePatchShow(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		PatchID   string `json:"patch_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	return d.kern.PatchShow(p.SessionID, p.PatchID)
}

// ---- command execution ------------------------------------------------------

func (d *Daemon) handleCommandExec(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string   `json:"session_id"`
		TaskID    string   `json:"task_id"`
		Argv      []string `json:"argv"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if len(p.Argv) == 0 {
		return nil, fmt.Errorf("argv is required")
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	command := strings.Join(p.Argv, " ")
	decision, err := d.kern.Request(sess.SessionID, "CommandExec", command, p.TaskID)
	if err != nil {
		return nil, err
	}
	if approved, ok := d.approveFromStoredGrant(sess, decision); ok {
		decision = approved
	}
	switch decision.Decision {
	case "denied":
		return map[string]any{"decision": decision}, nil
	case "requires_approval":
		d.mu.Lock()
		d.pendingCmds[decision.DecisionID] = pendingCommand{sessionID: sess.SessionID, taskID: p.TaskID, argv: p.Argv}
		d.mu.Unlock()
		return map[string]any{"decision": decision}, nil
	}
	result, err := d.executeCommand(sess.SessionID, p.TaskID, p.Argv, decision)
	if err != nil {
		return nil, err
	}
	return map[string]any{"decision": decision, "result": result}, nil
}

func (d *Daemon) executeCommand(sessionID, taskID string, argv []string, decision *kernel.Decision) (*toolchain.CommandResult, error) {
	sess, ok := d.store.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", sessionID)
	}
	command := strings.Join(argv, " ")
	risk, _ := d.kern.ClassifyCommand(command)
	// The command is executed by the Zig carina-run tool, so its lifecycle
	// events are attributed to the Zig actor. Package-manager mutations are
	// flagged so lockfile changes are auditable (PRD §13.7).
	commandID := sessionstore.NewID("cmd")
	started := map[string]any{"command_id": commandID, "command": command, "cwd": sess.WorkspaceRoot, "risk_level": risk}
	if mutatesPackages(command) {
		started["package_mutation"] = true
	}
	d.record(sessionID, "CommandStarted", taskID, "zig", started, decision.DecisionID)

	result, err := d.tools.Run(argv, sess.WorkspaceRoot, 2*time.Minute, d.egressEnv(), d.sandbox.Load())
	if err != nil {
		d.record(sessionID, "CommandExited", taskID, "zig", map[string]any{"command_id": commandID, "exit_code": -1, "error": err.Error()}, "")
		return nil, err
	}
	output := result.Stdout
	if len(output) > 100 {
		output = output[:100]
	}
	// Redact any known secret values before the output enters the log.
	chunk := strings.Join(output, "\n")
	if redacted, err := d.kern.Redact(sessionID, chunk); err == nil {
		chunk = redacted
	}
	d.record(sessionID, "CommandOutput", taskID, "zig", map[string]any{"command_id": commandID, "stream": "stdout", "chunk": chunk}, "")
	d.record(sessionID, "CommandExited", taskID, "zig",
		map[string]any{"command_id": commandID, "exit_code": result.ExitCode, "duration_ms": result.DurationMs}, "")
	return result, nil
}

// ---- audit / events ---------------------------------------------------------

func (d *Daemon) handleAuditReport(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.kern.AuditReport(id)
}

func (d *Daemon) handleAuditExport(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.kern.AuditExport(id)
}

func (d *Daemon) handleAuditVerify(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.kern.AuditVerify(id)
}

func (d *Daemon) handleProfileDescribe(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.kern.ProfileDescribe(id)
}

func (d *Daemon) handleSecretGrant(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Name      string `json:"name"`
		Value     string `json:"value"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	handle, err := d.kern.GrantSecret(p.SessionID, p.Name, p.Value)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": p.Name, "handle": handle}, nil
}

func (d *Daemon) handleSecretRequest(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	decision, handle, err := d.kern.RequestSecret(p.SessionID, p.Name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"decision": decision, "handle": handle}, nil
}

func (d *Daemon) handlePluginInspect(params json.RawMessage) (any, error) {
	var p struct {
		ManifestTOML string `json:"manifest_toml"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	return d.kern.PluginInspect(p.ManifestTOML)
}

func (d *Daemon) handlePluginRun(params json.RawMessage) (any, error) {
	var p struct {
		SessionID       string `json:"session_id"`
		ManifestTOML    string `json:"manifest_toml"`
		WasmBase64      string `json:"wasm_base64"`
		SignatureBase64 string `json:"signature_base64"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if _, ok := d.store.Get(p.SessionID); !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	return d.kern.PluginRun(p.SessionID, p.ManifestTOML, p.WasmBase64, p.SignatureBase64)
}

func (d *Daemon) handleEventStream(params json.RawMessage, sub *rpc.Subscription) error {
	id, err := sessionID(params)
	if err != nil {
		return err
	}
	d.events.Subscribe(id, sub)
	return nil
}

// record appends an event through the kernel (single audit writer) and
// fans it out to live subscribers. actor tags the language layer that
// produced the effect (go/rust/zig/model/user) so the audit trail shows
// the Go → Rust → Zig control flow (PRD §4.1).
func (d *Daemon) record(sessionID, eventType, taskID, actor string, payload map[string]any, decisionID string) {
	_ = d.kern.RecordEvent(sessionID, eventType, taskID, actor, payload, decisionID)
	d.events.Publish(sessionID, map[string]any{
		"session_id": sessionID,
		"task_id":    taskID,
		"type":       eventType,
		"actor":      actor,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"payload":    payload,
	})
}

// ---- workers ----------------------------------------------------------------

func (d *Daemon) handleWorkerRegister(params json.RawMessage) (any, error) {
	var p struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Kind == "" {
		p.Kind = "remote"
	}
	kind := worker.Kind(p.Kind)
	switch kind {
	case worker.Remote, worker.CI, worker.Sandbox:
	default:
		return nil, fmt.Errorf("unsupported worker kind %q", p.Kind)
	}
	w, credential, err := d.pool.RegisterAuthenticated(strings.TrimSpace(p.Name), kind)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"worker_id":         w.WorkerID,
		"worker_credential": credential,
	}, nil
}

func (d *Daemon) handleWorkerHeartbeat(params json.RawMessage) (any, error) {
	var p struct {
		WorkerID         string `json:"worker_id"`
		WorkerCredential string `json:"worker_credential"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := d.authenticateWorker(p.WorkerID, p.WorkerCredential); err != nil {
		return nil, err
	}
	if err := d.pool.Heartbeat(p.WorkerID); err != nil {
		return nil, fmt.Errorf("%s", workerAuthenticationError)
	}
	return map[string]any{"ok": true}, nil
}

func (d *Daemon) handleWorkerList(_ json.RawMessage) (any, error) {
	return d.pool.List(), nil
}

func (d *Daemon) handleWorkerRevoke(params json.RawMessage) (any, error) {
	var p struct {
		WorkerID         string `json:"worker_id"`
		WorkerCredential string `json:"worker_credential"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := d.authenticateWorker(p.WorkerID, p.WorkerCredential); err != nil {
		return nil, err
	}
	if err := d.pool.Revoke(p.WorkerID); err != nil {
		return nil, fmt.Errorf("%s", workerAuthenticationError)
	}
	return map[string]any{"ok": true}, nil
}

func (d *Daemon) session(params json.RawMessage) (*sessionstore.Session, string, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, "", err
	}
	sess, ok := d.store.Get(id)
	if !ok {
		return nil, "", fmt.Errorf("unknown session %s", id)
	}
	return sess, id, nil
}

// mutatesPackages reports whether a command installs/updates dependencies
// and therefore likely changes a lockfile (PRD §13.7).
func mutatesPackages(command string) bool {
	prefixes := []string{
		"npm install", "npm i ", "npm ci", "npm uninstall", "npm update",
		"pnpm add", "pnpm install", "pnpm remove", "yarn add", "yarn install", "yarn remove",
		"pip install", "pip uninstall", "poetry add", "poetry remove",
		"cargo add", "cargo install", "cargo remove", "go get", "bundle add",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(command, p) {
			return true
		}
	}
	// Direct lockfile edits.
	for _, lock := range []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock", "Cargo.lock", "go.sum", "poetry.lock"} {
		if strings.Contains(command, lock) {
			return true
		}
	}
	return false
}

func sessionID(params json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if p.SessionID == "" {
		return "", fmt.Errorf("session_id is required")
	}
	return p.SessionID, nil
}
