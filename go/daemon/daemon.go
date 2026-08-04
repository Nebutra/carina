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
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nebutra/carina/go/agentview"
	"github.com/Nebutra/carina/go/artifact"
	"github.com/Nebutra/carina/go/auth"
	"github.com/Nebutra/carina/go/channels"
	"github.com/Nebutra/carina/go/contextengine"
	"github.com/Nebutra/carina/go/continuity"
	"github.com/Nebutra/carina/go/egress"
	"github.com/Nebutra/carina/go/extensions"
	"github.com/Nebutra/carina/go/history"
	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/mcp"
	"github.com/Nebutra/carina/go/microcopy"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/nebutra"
	"github.com/Nebutra/carina/go/product"
	"github.com/Nebutra/carina/go/provider"
	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	carinatelemetry "github.com/Nebutra/carina/go/telemetry"
	"github.com/Nebutra/carina/go/toolchain"
	"github.com/Nebutra/carina/go/worker"
	"github.com/Nebutra/carina/go/workflowui"
	"github.com/Nebutra/carina/go/worktree"
)

const Version = product.Version

const maxWorkspaceFilePreviewBytes = 1 << 20

// Options configures external binaries and storage.
type Options struct {
	StateDir          string             // session metadata, event logs, snapshots
	RuntimeSpec       *localruntime.Spec // authoritative workspace runtime identity; nil is legacy/manual mode
	KernelBin         string             // carina-kernel-service path ("" = auto-discover)
	ToolsDir          string             // zig tools directory ("" = auto-discover)
	PolicyDir         string             // enterprise org-policy directory ("" = none)
	Offline           bool               // disable network model providers (PRD §5: offline mode)
	DisabledProviders []string           // provider IDs excluded from completion, embeddings, rerank, and auto-selection
	SafeMode          bool               // disable user/project extensions while retaining built-ins and policy

	MaxConcurrentTasks int // cap on concurrent background runs (0 => default 8)

	RequireWorkspaceTrust      bool               // when true, deny command exec in untrusted workspaces
	MaxTaskTokens              int                // per-task token budget (0 => unlimited); over-budget runs degrade
	EnableEgressProxy          bool               // route command network through a deny-by-default egress proxy
	EgressAllow                []string           // hosts allowed when the egress proxy is enabled
	SandboxCommands            bool               // run commands under an OS syscall sandbox (macOS sandbox-exec)
	InteractiveApproval        bool               // legacy: true=ask, false=always-approve (overridden by ApprovalMode when set)
	ApprovalMode               string             // ask|always-approve|dont-ask (product HITL mode; empty uses InteractiveApproval)
	DisableAlwaysApprove       bool               // org lock: refuse always-approve mode
	EnableDebugRPC             bool               // expose local-only debug.* diagnostic RPCs and collect their in-memory trace
	EgressCredentials          []EgressCredential // per-host credentials injected at the egress boundary
	VerifierModel              string             // model for the independent done-verifier ("" => verifier off)
	RiskReviewMode             string             // off|advisory|enforce for autonomous approval review ("" => advisory)
	RiskReviewModel            string             // optional model for Nebutra Risk Review ("" => deterministic local reviewer)
	NebutraCloudEndpoint       string             // Nebutra Cloud identity/sync boundary (default https://nebutra.com)
	NebutraSyncMode            string             // currently only "off"; future sync modes belong behind Nebutra
	GatewayTokenSigningKeyFile string             // optional local file containing Gateway token signing material
	GatewayTokenMaxTTLSeconds  int                // max scoped Gateway token TTL (0 => 15m)
	ContextEngine              string             // auto|off|noop
	MemoryProvider             string             // off|hms-shadow|hms-hybrid
	MemoryHMSEndpoint          string             // deployment-owned HMS endpoint
	MemoryHMSAPIKeyEnv         string             // env var containing HMS bearer token
	MemoryHMSTimeout           time.Duration      // total recall deadline
	MemoryHMSMaxEvidence       int                // maximum recalled evidence rows
	MemoryHMSBankKeyEnv        string             // env var containing bank-ID HMAC key
	MemoryHMSProjectionEnabled bool               // opt-in external projection of approved local memory
	MemoryHMSProjectionPoll    time.Duration      // durable projection worker cadence
	ExtensionTrustedRoots      []string           // local roots allowed as extension install sources
	TelemetryWriter            io.Writer          // nil keeps OpenTelemetry export disabled
	BestOfNEnabled             bool               // opt-in: expose the best_of_n tool (default false — off)
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

type pendingMemoryControl struct {
	kind             string
	sessionID        string
	targetSessionID  string
	target           string
	expectedRevision string
	idempotencyKey   string
	source           string
	entries          []string
}

type pendingMemoryProjection struct {
	sessionID  string
	documentID string
	generation uint64
	stage      string
}

type Daemon struct {
	store               *sessionstore.Store
	sched               *scheduler.Scheduler
	pool                *worker.Pool
	backpressure        *backpressureManager
	router              *modelrouter.Router
	server              *rpc.Server
	kern                *kernel.Service
	tools               *toolchain.Toolchain
	events              *Bus
	debugTrace          *debugTrace
	started             time.Time
	journey             *journeyMetrics
	readinessGeneration atomic.Uint64

	org              *kernel.OrgPolicy // enterprise policy (nil when unconfigured)
	policyDir        string            // opts.PolicyDir, kept for doctor's policyBundleStale freshness probe
	stateDir         string
	socketPath       string
	cloudEndpoint    string
	syncMode         string
	reasoner         Reasoner     // agent "thinking" engine (nil => mock loop)
	reasonerBackend  string       // selected runtime backend; never inferred from binary presence
	reasonerModel    string       // configured default model, when the backend exposes one
	reasonerExplicit bool         // true only when CARINA_REASONER_BACKEND explicitly selected this backend
	summarizer       Reasoner     // optional cheaper model for compaction/summarization
	verifier         Reasoner     // optional independent "judge" for done-claims (nil => default-lenient)
	riskReviewer     Reasoner     // optional independent approval reviewer (nil => deterministic heuristic)
	judgeReasoner    Reasoner     // optional independent best-of-n judge (nil => falls back to d.reasoner, then a deterministic heuristic)
	riskReviewMode   atomic.Value // string: off|advisory|enforce, hot-reloadable

	mu                    sync.Mutex
	pendingCmds           map[string]pendingCommand          // decision_id -> command awaiting approval
	pendingMemWrites      map[string]pendingMemoryWrite      // decision_id -> memory write awaiting approval
	pendingMemControls    map[string]pendingMemoryControl    // decision_id -> rollback/handoff awaiting approval
	pendingMemProjections map[string]pendingMemoryProjection // decision_id -> HMS projection awaiting externalization approval
	patchGates            map[string]*patchGate              // patch_id -> PatchApply decision state
	patchGateByDecision   map[string]string                  // decision_id -> patch_id
	submissionMu          sync.Mutex
	taskSubmissions       map[string]string // session_id + client_submission_id -> task_id
	forkMu                sync.Mutex
	hookOutcomeMu         sync.Mutex
	hookOutcomes          map[string]hookOutcome
	hookStops             sync.Map // task_id -> true after Stop hooks run

	runs   *runStore     // durable background-run registry (survives restart)
	runSem chan struct{} // concurrency cap for background runs
	// checkpointMu serializes restore/resume commit boundaries. Both operations
	// update the kernel patch lineage, latest checkpoint pointer, and durable
	// task row, so they must never interleave for the same daemon.
	checkpointMu  sync.Mutex
	sessionFences sync.Map // session_id -> *sync.RWMutex; restore is writer, execution/mutations are readers

	readProv   map[string]map[string]string // session -> relpath -> sha256 of last read (dirty-write guard)
	readProvMu sync.Mutex

	restrictedTools sync.Map // session -> map[string]bool of tool verbs this session's loop must never dispatch (set for best-of-n candidate drafters)

	indexBuilt sync.Map // session -> true once the code index was lazily built (code.* tools)

	indexSnapshot sync.Map // session -> *sweepSnapshot from the last index sync (V4 mtime staleness sweep)

	codeIntelStatus sync.Map // session -> codeIntelStatus (V3: semantic-layer health on daemon.status.code_intel)

	// allowedTools/allowedSpawnAgents hold a spawned session's declarative
	// AgentSpec.ToolNames/SpawnableAgents allow-lists (session -> map[string]bool)
	// for the duration that session is actively running. Absent/nil means
	// unrestricted (the default for every spec that doesn't set these
	// fields) — additive constraints layered on top of the Rust-enforced
	// Profile ceiling, never a grant beyond it. Set in spawnSubagentContext,
	// cleared when that session's run finishes.
	allowedTools       sync.Map
	allowedSpawnAgents sync.Map

	// swarmChannels binds a spawned child session (by session ID) to the
	// swarmChannelBroker of the streaming workflow run it's executing a step
	// for, plus that step's own id and consumes_channel subscriptions — set
	// by spawnSubagentContextIDBound for the duration of the child's
	// synchronous run, so swarm_publish/swarm_receive tool calls made
	// mid-run can find the right broker (go/daemon/swarm_channel.go).
	swarmChannels sync.Map

	// dispatchSwarmBindings is swarmChannels' remote-execution counterpart:
	// binds a DISPATCH TASK ID (not a session ID — a remote step never gets
	// a local session at all) to the same *swarmChannelBinding shape, set by
	// runStreamingStepRemote for the lifetime of that dispatch. A leased
	// worker's work.report can include "channel_messages" to publish through
	// it (see handleWorkReport in dispatch.go) — batched at report time
	// since the executor result contract is one JSON value at the end, not a
	// live stream, so this is coarser than a local step's participation but
	// real (see workflow_remote.go's binding-registration comment for why).
	dispatchSwarmBindings sync.Map

	embedModelDefault string // "<provider>/<model>" of the default embeddings backend ("" = semantic layer off)

	trust          *trustStore  // trusted workspace roots
	requireTrust   atomic.Bool  // deny command exec in untrusted workspaces (hot-reloadable)
	maxTaskTokens  atomic.Int64 // per-task token budget (0 => unlimited; hot-reloadable)
	bestOfNEnabled atomic.Bool  // opt-in: expose/allow the best_of_n tool (default false — off; hot-reloadable)

	mailbox               map[string]*taskMailbox // task -> pending steering messages, urgent-first
	mailboxMu             sync.Mutex
	taskContexts          map[string]context.Context
	taskCancels           map[string]context.CancelCauseFunc
	taskContextMu         sync.Mutex
	activeToolCalls       map[string]*activeToolCall // call_id -> call
	activeToolCallsByTask map[string]map[string]struct{}
	activeToolCallMu      sync.Mutex

	planMode map[string]bool // session -> plan mode (read-only until approved)
	planMu   sync.Mutex

	mcp          *mcp.Manager // external MCP servers (proxied tools, kernel-gated)
	contextEng   contextengine.Engine
	egress       *egress.Proxy // deny-by-default network egress proxy (optional)
	egressURL    string
	egressCAPath string      // process-local CA bundle for MITM-enabled children
	sandbox      atomic.Bool // run commands under an OS syscall sandbox (hot-reloadable)
	safeMode     bool

	stopCh    chan struct{} // closed on Close; stops background loops (lease reaper)
	stopOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
	loopWG    sync.WaitGroup
	taskWG    sync.WaitGroup

	interactiveApproval  atomic.Bool                     // true iff approval mode is ask (legacy mirror, hot-reloadable)
	approvalMode         atomic.Value                    // string: ask|always-approve|dont-ask
	disableAlwaysApprove atomic.Bool                     // org lock: refuse always-approve (hot-reloadable)
	debugRPCEnabled      atomic.Bool                     // exposes debug.* and collects debug trace (hot-reloadable, default off)
	approvalTimeout      time.Duration                   // how long to wait for an interactive approval (0 => 5m)
	pendingApprovals     map[string]chan approvalSignal  // decision_id -> resolver channel
	pendingQuestions     map[string]*pendingUserQuestion // question_id -> blocked ask_user tool
	approvalGrants       *approvalGrantStore             // exact session/project grants, persisted under stateDir
	approvalMu           sync.Mutex
	questionMu           sync.Mutex
	patchGateRetention   time.Duration // how long a resolved patch gate survives before being swept (0 => 1h)

	subagentParentTask map[string]string // childSessionID -> parentTaskID (leader-bridge linkage)
	escalationCounts   map[string]int    // childTaskID -> escalations used (bridge cap)
	bridgeMu           sync.Mutex

	reload func() error // config reload closure (SIGHUP/RPC); nil until SetReloader

	authChain       *auth.Chain      // ordered provider-credential resolver (BYOK -> Nebutra OAuth)
	authStore       *auth.Store      // local BYOK credential store (doctor's per-provider probe)
	providerCatalog provider.Catalog // runtime provider catalog (doctor's per-provider probe)
	// liveModelsCache holds recent GET /models results per provider+endpoint so
	// model.list can expand thin catalogs (CC Switch profile.Model, open gateways)
	// without hammering the upstream on every picker open.
	liveModelsMu             sync.Mutex
	liveModelsCache          map[string]liveModelsCacheEntry
	liveModelsHTTP           *http.Client     // optional; tests inject httptest; nil => default client
	disabledProviders        map[string]bool  // normalized provider IDs blocked before registration and task routing
	usage                    *usageStore      // durable per-task/session model usage and cost accounting
	goals                    *goalStore       // one durable operator-controlled goal per session
	history                  *history.History // shared cross-process prompt history
	memory                   *memoryStore     // governed local long-term memory
	memoryVersions           *memoryControllerStore
	memoryHMS                *hmsRecallProvider // optional derived recall provider; local store stays authoritative
	memoryHMSAPIKeyEnv       string
	memoryProjection         *memoryProjectionStore // durable desired-state outbox (optional)
	memoryProjectionExecutor memoryProjectionExecutor
	memoryProjectionPoll     time.Duration
	memoryProjectionWriteMu  sync.Mutex
	schedules                *scheduler.ScheduleStore // persistent cron/at/every definitions
	gatewayTokens            *rpc.GatewayTokenIssuer  // optional scoped Gateway token signer/verifier
	gatewayTokenMaxTTL       time.Duration            // max TTL for locally issued scoped Gateway tokens
	gatewayHTTPServers       []*http.Server
	gatewayResponses         map[string]string // response id -> session id for /v1/responses continuity
	agentView                *agentview.Store
	worktrees                *worktree.Manager
	workflowRuns             *workflowui.Store
	workflowControls         map[string]*workflowRunControl
	workflowControlMu        sync.Mutex
	channels                 *channels.Registry
	extensions               *extensions.Marketplace
	telemetry                *carinatelemetry.Exporter
	compactionBreaker        *compactionCircuitBreaker
	retryGovernance          *retryGovernance
	artifacts                *artifact.Store
	artifactUploadMu         sync.Mutex
	artifactUploads          map[string]*artifactUploadState
	runtimeLease             *runtimeLease
	runtimeSpec              *localruntime.Spec
	runtimeMu                sync.Mutex
	runtimeLifecycle         string
	runtimeIdleMu            sync.Mutex
	runtimeConnections       int
	runtimeIdleGrace         time.Duration
	runtimeIdleTimer         *time.Timer
	runtimeIdleDeadline      *time.Time
	runtimeIdleStopping      bool
	runtimeIdleStop          func()
}

const artifactGCInterval = 30 * time.Minute

func New(opts Options) (*Daemon, error) {
	if opts.StateDir == "" {
		opts.StateDir = ".carina-state"
	}
	runtimeSpec, err := validateRuntimeSpec(opts.RuntimeSpec, opts.StateDir)
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	configuredReasonerBackend, err := normalizeReasonerBackend(os.Getenv("CARINA_REASONER_BACKEND"))
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	runtimeLease, err := acquireRuntimeLease(opts.StateDir)
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	leaseTransferred := false
	defer func() {
		if !leaseTransferred {
			_ = runtimeLease.close(false)
		}
	}()
	contextEng, err := contextengine.New(contextengine.Config{ContextEngine: opts.ContextEngine})
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
	if err := validateRuntimeSessions(runtimeSpec, store.List()); err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	tools := toolchain.New(opts.ToolsDir)
	// The kernel delegates patch writes to carina-patch-native, so it needs the
	// same tools directory (PRD §4.4).
	kern, err := kernel.Start(opts.KernelBin, opts.StateDir, tools.Dir())
	if err != nil {
		return nil, fmt.Errorf("daemon: cannot start capability kernel: %w", err)
	}
	d := &Daemon{
		store:                 store,
		sched:                 scheduler.New(),
		pool:                  worker.NewPool(),
		backpressure:          newBackpressureManager(),
		router:                modelrouter.New(),
		server:                rpc.NewServer(),
		kern:                  kern,
		tools:                 tools,
		events:                NewBus(),
		debugTrace:            newDebugTrace(defaultDebugTraceCapacity),
		org:                   loadOrgPolicy(opts.PolicyDir),
		policyDir:             opts.PolicyDir,
		stateDir:              opts.StateDir,
		cloudEndpoint:         cloudEndpoint,
		syncMode:              syncMode,
		started:               time.Now().UTC(),
		pendingCmds:           make(map[string]pendingCommand),
		pendingMemWrites:      make(map[string]pendingMemoryWrite),
		pendingMemControls:    make(map[string]pendingMemoryControl),
		pendingMemProjections: make(map[string]pendingMemoryProjection),
		patchGates:            make(map[string]*patchGate),
		patchGateByDecision:   make(map[string]string),
		taskSubmissions:       make(map[string]string),
		hookOutcomes:          make(map[string]hookOutcome),
		memory:                newMemoryStore(opts.StateDir),
		memoryVersions:        newMemoryControllerStore(opts.StateDir),
		schedules:             scheduler.OpenScheduleStore(opts.StateDir),
		contextEng:            contextEng,
		gatewayTokens:         gatewayTokens,
		gatewayTokenMaxTTL:    gatewayTokenMaxTTL,
		gatewayResponses:      map[string]string{},
		runtimeLease:          runtimeLease,
		runtimeSpec:           runtimeSpec,
	}
	d.journey = newJourneyMetrics(time.Now)
	d.server.SetConnectionObserver(d)
	if err := d.publishRuntimeDescriptor(localruntime.LifecycleStarting, ""); err != nil {
		_ = kern.Close()
		return nil, fmt.Errorf("daemon: publish starting runtime descriptor: %w", err)
	}
	if mode := strings.ToLower(strings.TrimSpace(opts.MemoryProvider)); mode != "" && mode != memoryProviderOff {
		if opts.Offline {
			_ = kern.Close()
			return nil, fmt.Errorf("daemon: memory provider %s is incompatible with offline mode", mode)
		}
		apiKey := ""
		if name := strings.TrimSpace(opts.MemoryHMSAPIKeyEnv); name != "" {
			apiKey = os.Getenv(name)
		}
		if apiKey == "" {
			_ = kern.Close()
			return nil, fmt.Errorf("daemon: HMS API key env %q is empty", opts.MemoryHMSAPIKeyEnv)
		}
		bankKey := os.Getenv(strings.TrimSpace(opts.MemoryHMSBankKeyEnv))
		timeout := opts.MemoryHMSTimeout
		if timeout == 0 {
			timeout = 3 * time.Second
		}
		maxEvidence := opts.MemoryHMSMaxEvidence
		if maxEvidence == 0 {
			maxEvidence = 8
		}
		d.memoryHMS, err = newHMSRecallProvider(mode, opts.MemoryHMSEndpoint, apiKey, []byte(bankKey), timeout, maxEvidence)
		if err != nil {
			_ = kern.Close()
			return nil, fmt.Errorf("daemon: configure HMS memory provider: %w", err)
		}
		d.memoryHMSAPIKeyEnv = opts.MemoryHMSAPIKeyEnv
	}
	if opts.MemoryHMSProjectionEnabled {
		if d.memoryHMS == nil {
			_ = kern.Close()
			return nil, fmt.Errorf("daemon: HMS projection requires an HMS memory provider")
		}
		if opts.MemoryHMSProjectionPoll != 0 && (opts.MemoryHMSProjectionPoll < 100*time.Millisecond || opts.MemoryHMSProjectionPoll > time.Minute) {
			_ = kern.Close()
			return nil, fmt.Errorf("daemon: HMS projection poll interval must be between 100ms and 60s")
		}
		d.memoryProjection, err = newMemoryProjectionStore(opts.StateDir)
		if err != nil {
			_ = kern.Close()
			return nil, fmt.Errorf("daemon: open memory projection outbox: %w", err)
		}
		if err := d.memoryProjection.BindEndpoint(d.memoryHMS.endpoint.String()); err != nil {
			_ = kern.Close()
			return nil, fmt.Errorf("daemon: bind HMS projection endpoint: %w", err)
		}
		if err := d.memoryProjection.ReauthorizePending(); err != nil {
			_ = kern.Close()
			return nil, fmt.Errorf("daemon: reauthorize memory projection outbox: %w", err)
		}
		d.memoryProjectionExecutor = auditedProjectionExecutor{d: d, next: hmsOutboxExecutor{provider: d.memoryHMS}}
		d.memoryProjectionPoll = opts.MemoryHMSProjectionPoll
		if d.memoryProjectionPoll <= 0 {
			d.memoryProjectionPoll = time.Second
		}
		d.reconcileDirtyMemoryProjections()
	}
	d.agentView = agentview.Open(opts.StateDir)
	d.worktrees, err = worktree.New(opts.StateDir)
	if err != nil {
		_ = kern.Close()
		return nil, fmt.Errorf("daemon: worktree manager: %w", err)
	}
	d.workflowRuns, err = workflowui.New(opts.StateDir)
	if err != nil {
		_ = kern.Close()
		return nil, fmt.Errorf("daemon: workflow run store: %w", err)
	}
	d.workflowControls = map[string]*workflowRunControl{}
	if _, err = d.workflowRuns.ReconcileStartup("daemon restarted before the run reached a terminal state"); err != nil {
		_ = kern.Close()
		return nil, fmt.Errorf("daemon: reconcile workflow runs: %w", err)
	}
	d.channels, err = channels.Open(opts.StateDir, 5*time.Minute, 24*time.Hour, func(ref string) ([]byte, error) {
		if !strings.HasPrefix(ref, "env:CARINA_CHANNEL_") {
			return nil, fmt.Errorf("unsupported channel secret handle")
		}
		value := os.Getenv(strings.TrimPrefix(ref, "env:"))
		if value == "" {
			return nil, fmt.Errorf("channel secret is not configured")
		}
		return []byte(value), nil
	})
	if err != nil {
		_ = kern.Close()
		return nil, fmt.Errorf("daemon: channels: %w", err)
	}
	trustedExtensionRoots := append([]string{}, opts.ExtensionTrustedRoots...)
	trustedExtensionRoots = append(trustedExtensionRoots, filepath.Join(opts.StateDir, "extension-sources"))
	d.extensions, err = extensions.New(opts.StateDir, Version, trustedExtensionRoots)
	if err != nil {
		_ = kern.Close()
		return nil, fmt.Errorf("daemon: extension marketplace: %w", err)
	}
	if err = d.extensions.SetOrgPolicy(extensions.LoadOrgPolicy(opts.PolicyDir)); err != nil {
		_ = kern.Close()
		return nil, fmt.Errorf("daemon: extension org policy: %w", err)
	}
	d.telemetry = carinatelemetry.New(opts.TelemetryWriter)
	d.compactionBreaker = newCompactionCircuitBreaker()
	d.retryGovernance = newRetryGovernance(time.Now)
	d.retryGovernance.pressure = func() string {
		if d.sched.DispatchDepth() >= 16 {
			return "pause"
		}
		if d.sched.DispatchDepth() >= 8 {
			return "throttle"
		}
		return "none"
	}
	d.artifacts, err = artifact.New(filepath.Join(opts.StateDir, "artifacts"))
	if err != nil {
		_ = kern.Close()
		return nil, fmt.Errorf("daemon: artifact store: %w", err)
	}
	if _, err = d.artifacts.GC(time.Now()); err != nil {
		_ = kern.Close()
		return nil, fmt.Errorf("daemon: artifact gc: %w", err)
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
	d.disabledProviders = disabledProviderSet(opts.DisabledProviders)
	d.usage = newUsageStore(opts.StateDir)
	d.goals = newGoalStore(opts.StateDir)
	registerProviders(d.router, opts.Offline, opts.DisabledProviders, authStore, providerCatalog)
	// Embeddings (V2 semantic layer): BYOK only, credential-gated at
	// registration so no provider means the layer is silently off.
	d.embedModelDefault = registerEmbeddingsProviders(d.router, opts.Offline, opts.DisabledProviders, authStore)
	// Rerank (V4 §C): same BYOK credential gate; no registered provider means
	// the rerank stage stays off and code.search keeps the kernel order.
	registerRerankProviders(d.router, opts.Offline, opts.DisabledProviders, authStore)
	// Durable run registry + concurrency cap for background runs. Reloading the
	// registry lets `execution.list`/`execution.status` answer for runs from before a
	// restart (the run record survives even though the live loop does not yet).
	d.runs = newRunStore(opts.StateDir)
	for _, t := range d.runs.load() {
		d.sched.Load(t)
		if t.ClientSubmissionID != "" {
			d.taskSubmissions[taskSubmissionKey(t.SessionID, t.ClientSubmissionID)] = t.RunID
		}
	}
	for _, task := range d.runs.loadTasks() {
		d.sched.LoadTask(task)
	}
	blockedRestores, err := d.runs.reconcileRestoreJournals()
	if err != nil {
		_ = d.kern.Close()
		return nil, fmt.Errorf("daemon: reconcile checkpoint restore journals: %w", err)
	}
	for _, taskID := range blockedRestores {
		if _, ok := d.sched.Get(taskID); ok {
			blocked, _ := d.sched.MarkReconciliationRequired(taskID, "checkpoint restore interrupted by daemon restart; retry the same checkpoint restore to reconcile")
			if err := d.runs.saveChecked(blocked); err != nil {
				_ = d.kern.Close()
				return nil, fmt.Errorf("daemon: persist blocked checkpoint restore %s: %w", taskID, err)
			}
		}
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
	d.bestOfNEnabled.Store(opts.BestOfNEnabled)
	d.sandbox.Store(opts.SandboxCommands)
	d.safeMode = opts.SafeMode
	d.mailbox = map[string]*taskMailbox{}
	controlRecords := d.runs.loadExecutionControls()
	for _, taskID := range controlRunIDs(controlRecords) {
		record := controlRecords[taskID]
		d.mailbox[taskID] = &taskMailbox{
			urgent: append([]queuedSteer(nil), record.Urgent...), normal: append([]queuedSteer(nil), record.Normal...),
			softInterruptRequested: record.SoftInterruptRequested,
		}
	}
	d.taskContexts = map[string]context.Context{}
	d.taskCancels = map[string]context.CancelCauseFunc{}
	d.activeToolCalls = map[string]*activeToolCall{}
	d.activeToolCallsByTask = map[string]map[string]struct{}{}
	d.planMode = map[string]bool{}
	for _, sess := range d.store.List() {
		if sess.PlanMode {
			d.planMode[sess.SessionID] = true
		}
	}
	d.stopCh = make(chan struct{})
	d.loopWG.Add(1)
	go d.runArtifactGC()
	d.pendingApprovals = map[string]chan approvalSignal{}
	d.pendingQuestions = map[string]*pendingUserQuestion{}
	d.approvalGrants = newApprovalGrantStore(opts.StateDir)
	d.disableAlwaysApprove.Store(opts.DisableAlwaysApprove)
	mode := strings.TrimSpace(opts.ApprovalMode)
	if mode == "" {
		mode = approvalModeFromInteractive(opts.InteractiveApproval)
	}
	if err := d.setApprovalMode(mode); err != nil {
		// Org lock may block always-approve from config; fall back to ask.
		_ = d.setApprovalMode(approvalModeAsk)
	}
	d.debugRPCEnabled.Store(opts.EnableDebugRPC)
	d.subagentParentTask = map[string]string{}
	d.escalationCounts = map[string]int{}
	// Shared cross-process prompt history (survives restarts; multiple daemons
	// can append concurrently).
	d.history = history.New(filepath.Join(opts.StateDir, "history"))
	d.startBackgroundLoop(d.reapLeases) // re-queue dispatch tasks abandoned by crashed workers
	if d.memoryProjection != nil {
		d.startBackgroundLoop(d.runMemoryProjectionLoop)
	}
	d.mcp = mcp.NewManager()
	if !opts.SafeMode {
		if home, err := os.UserHomeDir(); err == nil {
			d.mcp.LoadAndConnect(filepath.Join(home, ".carina", "mcp.json"))
		}
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
	// Auto owns a stable router reasoner so an in-product credential import can
	// become executable without restarting the workspace daemon. Readiness is
	// still fail-closed and evaluated from live provider credentials before use.
	// CLI reasoners remain explicit compatibility adapters.
	if !opts.Offline {
		model := strings.TrimSpace(os.Getenv("CARINA_REASONER_MODEL"))
		selectedBackend := selectReasonerBackend(false, configuredReasonerBackend)
		d.reasonerBackend = selectedBackend
		d.reasonerModel = model
		d.reasonerExplicit = configuredReasonerBackend != reasonerBackendAuto && selectedBackend != reasonerBackendNone
		d.reasoner, err = newConfiguredReasoner(selectedBackend, d.router, model)
		if err != nil {
			closeReasoners(d.reasoner, d.summarizer, d.verifier, d.riskReviewer)
			_ = kern.Close()
			return nil, fmt.Errorf("daemon: configure %s reasoner: %w", selectedBackend, err)
		}
		// Model tiering: an optional cheaper model for compaction/summarization.
		if m := os.Getenv("CARINA_SUMMARIZER_MODEL"); m != "" && selectedBackend != reasonerBackendNone {
			d.summarizer, err = newConfiguredReasoner(selectedBackend, d.router, m)
			if err != nil {
				closeReasoners(d.reasoner, d.summarizer, d.verifier, d.riskReviewer)
				_ = kern.Close()
				return nil, fmt.Errorf("daemon: configure summarizer reasoner: %w", err)
			}
		}
		// Independent done-verifier: a separate model that judges completion.
		vm := opts.VerifierModel
		if vm == "" {
			vm = os.Getenv("CARINA_VERIFIER_MODEL")
		}
		if vm != "" && selectedBackend != reasonerBackendNone {
			d.verifier, err = newConfiguredReasoner(selectedBackend, d.router, vm)
			if err != nil {
				closeReasoners(d.reasoner, d.summarizer, d.verifier, d.riskReviewer)
				_ = kern.Close()
				return nil, fmt.Errorf("daemon: configure verifier reasoner: %w", err)
			}
		}
		// Nebutra Risk Review: optional model-backed reviewer for autonomous
		// approval requests. Without it, a deterministic local reviewer still
		// records and can enforce obvious high-risk cases.
		rm := opts.RiskReviewModel
		if rm == "" {
			rm = os.Getenv("CARINA_RISK_REVIEW_MODEL")
		}
		if rm != "" && selectedBackend != reasonerBackendNone {
			d.riskReviewer, err = newConfiguredReasoner(selectedBackend, d.router, rm)
			if err != nil {
				closeReasoners(d.reasoner, d.summarizer, d.verifier, d.riskReviewer)
				_ = kern.Close()
				return nil, fmt.Errorf("daemon: configure risk-review reasoner: %w", err)
			}
		}
	}
	d.recover()
	d.reconcileMemoryVersions()
	d.resumeRuns()
	d.recoverAutoGoals()
	d.startBackgroundLoop(d.runScheduleLoop)
	d.initializeRuntimeIdle()
	leaseTransferred = true
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
func (d *Daemon) SetReasoner(r Reasoner) {
	d.reasoner = r
	d.reasonerBackend = ""
	d.reasonerModel = ""
	d.reasonerExplicit = false
	if r != nil {
		d.reasonerBackend = r.Name()
	}
}

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
	d.setRuntimeSocket(socketPath)
	// A local execution worker and a sandbox worker are always available
	// (PRD §5.4).
	d.pool.Register("local", worker.Local)
	d.pool.Register("sandbox", worker.Sandbox)
	if err := d.publishRuntimeDescriptor(localruntime.LifecycleRunning, socketPath); err != nil {
		return fmt.Errorf("daemon: publish running runtime descriptor: %w", err)
	}
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
	d.closeOnce.Do(func() {
		d.closeErr = d.close()
	})
	return d.closeErr
}

func (d *Daemon) close() error {
	d.stopRuntimeIdleTimer()
	_, socketPath := d.runtimePublishState()
	descriptorStoppingErr := d.publishRuntimeDescriptor(localruntime.LifecycleStopping, socketPath)
	d.stopOnce.Do(func() {
		if d.stopCh != nil {
			close(d.stopCh)
		}
		d.taskContextMu.Lock()
		cancels := make([]context.CancelCauseFunc, 0, len(d.taskCancels))
		for _, cancel := range d.taskCancels {
			cancels = append(cancels, cancel)
		}
		d.taskContextMu.Unlock()
		for _, cancel := range cancels {
			cancel(context.Canceled)
		}
		d.cancelWorkflowControls()
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
	if d.memoryHMS != nil {
		d.memoryHMS.Close()
	}
	if d.egress != nil {
		_ = d.egress.Close()
	}
	for _, srv := range d.gatewayHTTPServers {
		_ = srv.Close()
	}
	closeReasoners(d.reasoner, d.summarizer, d.verifier, d.riskReviewer)
	kernelErr := d.kern.Close()
	leaseErr := d.runtimeLease.close(true)
	descriptorStoppedErr := d.publishRuntimeDescriptor(localruntime.LifecycleStopped, socketPath)
	if kernelErr != nil {
		return kernelErr
	}
	if leaseErr != nil {
		return leaseErr
	}
	if descriptorStoppingErr != nil {
		return descriptorStoppingErr
	}
	return descriptorStoppedErr
}

type closeableReasoner interface {
	Close()
}

func closeReasoners(reasoners ...Reasoner) {
	closed := make([]Reasoner, 0, len(reasoners))
	for _, reasoner := range reasoners {
		closer, ok := reasoner.(closeableReasoner)
		if !ok {
			continue
		}
		duplicate := false
		kind := reflect.TypeOf(reasoner)
		if kind != nil && kind.Comparable() {
			for _, prior := range closed {
				if reflect.TypeOf(prior) == kind && prior == reasoner {
					duplicate = true
					break
				}
			}
		}
		if duplicate {
			continue
		}
		closer.Close()
		closed = append(closed, reasoner)
	}
}

func (d *Daemon) runArtifactGC() {
	defer d.loopWG.Done()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-d.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	_ = d.artifacts.RunPeriodicGC(ctx, artifactGCInterval, time.Now)
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

func (d *Daemon) registerMethods() {
	d.registerRPC("runtime.initialize", rpc.ScopeRead, true, d.handleRuntimeInitialize)
	d.registerRPC("runtime.describe", rpc.ScopeRead, false, d.handleRuntimeDescribe)
	d.registerRPC("runtime.capabilities", rpc.ScopeRead, true, d.handleRuntimeCapabilities)
	d.registerRPC("runtime.registry_schema", rpc.ScopeRead, true, d.handleRuntimeSchema)
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
	d.registerRPC("context.summary", rpc.ScopeRead, false, d.handleContextSummary)
	d.registerRPC("context.compress", rpc.ScopeWrite, false, d.handleContextCompress)
	d.registerRPC("gateway.hello", rpc.ScopeRead, true, d.handleGatewayHello)
	d.registerRPC("gateway.methods", rpc.ScopeRead, true, d.handleGatewayMethods)
	d.registerRPC("gateway.resolve_scope", rpc.ScopeRead, false, d.handleGatewayResolveScope)
	if d.gatewayTokens != nil {
		d.registerRPC("gateway.token.issue", rpc.ScopeAdmin, false, d.handleGatewayTokenIssue, true)
	}
	d.registerRPC("agent.list", rpc.ScopeRead, true, d.handleAgentList)
	d.registerRPC("model.list", rpc.ScopeRead, true, d.handleModelList)
	d.registerRPC("agent.view", rpc.ScopeRead, true, d.handleAgentView)
	d.registerRPC("agent.peek", rpc.ScopeRead, true, d.handleAgentPeek)
	d.registerRPC("agent.recap", rpc.ScopeRead, true, d.handleAgentRecap)
	d.registerRPC("agent.dispatch", rpc.ScopeWrite, false, d.handleAgentDispatch, true)
	d.registerRPC("agent.stop", rpc.ScopeWrite, false, d.handleAgentStop)
	d.registerRPC("agent.remove", rpc.ScopeWrite, false, d.handleAgentRemove)
	d.registerRPC("agent.metadata.set", rpc.ScopeWrite, false, d.handleAgentMetadataSet)
	d.registerRPC("worktree.create", rpc.ScopeWrite, false, d.handleWorktreeCreate, true)
	d.registerRPC("worktree.list", rpc.ScopeRead, false, d.handleWorktreeList)
	d.registerRPC("worktree.enter", rpc.ScopeWrite, false, d.handleWorktreeEnter, true)
	d.registerRPC("worktree.lock", rpc.ScopeWrite, false, d.handleWorktreeLock, true)
	d.registerRPC("worktree.unlock", rpc.ScopeWrite, false, d.handleWorktreeUnlock, true)
	d.registerRPC("worktree.cleanup", rpc.ScopeWrite, false, d.handleWorktreeCleanup, true)
	d.registerRPC("command.list", rpc.ScopeRead, true, d.handleCommandList)

	d.registerRPC("session.create", rpc.ScopeWrite, false, d.handleSessionCreate)
	d.registerRPC("session.get", rpc.ScopeRead, true, d.handleSessionGet)
	d.registerRPC("session.list", rpc.ScopeRead, true, d.handleSessionList)
	d.registerRPC("session.rename", rpc.ScopeWrite, false, d.handleSessionRename)
	d.registerRPC("session.archive", rpc.ScopeWrite, false, d.handleSessionArchive)
	d.registerRPC("session.unarchive", rpc.ScopeWrite, false, d.handleSessionUnarchive)
	d.registerRPC("session.pause", rpc.ScopeWrite, false, d.handleSessionPause)
	d.registerRPC("session.resume", rpc.ScopeWrite, false, d.handleSessionResume)
	d.registerRPC("session.close", rpc.ScopeWrite, false, d.handleSessionClose)
	d.registerRPC("session.replay", rpc.ScopeRead, true, d.handleSessionReplay)
	d.registerRPC("session.items", rpc.ScopeRead, true, d.handleSessionItems)
	d.registerRPC("session.review", rpc.ScopeRead, true, d.handleSessionReview)
	d.registerRPC("session.attach", rpc.ScopeRead, true, d.handleSessionAttach)
	d.registerRPC("session.events.unsubscribe", rpc.ScopeStream, true, d.handleEventUnsubscribe)
	d.registerRPC("session.fork", rpc.ScopeWrite, false, d.handleSessionFork)
	d.registerRPC("session.checkpoint.list", rpc.ScopeRead, false, d.handleCheckpointList)
	d.registerRPC("session.checkpoint.preview", rpc.ScopeRead, false, d.handleCheckpointPreview)
	d.registerRPC("session.checkpoint.summarize", rpc.ScopeRead, false, d.handleCheckpointSummarize)
	d.registerRPC("session.checkpoint.restore", rpc.ScopeWrite, false, d.handleCheckpointRestore, true)
	d.registerRPC("session.checkpoint.compact", rpc.ScopeWrite, false, d.handleCheckpointCompact, true)
	d.registerRPC("session.plan_mode", rpc.ScopeWrite, false, d.handlePlanMode)
	d.registerRPC("session.model.get", rpc.ScopeRead, false, d.handleSessionModelGet)
	d.registerRPC("session.model.set", rpc.ScopeWrite, false, d.handleSessionModelSet, true)
	d.registerRPC("session.approve_plan", rpc.ScopeWrite, false, d.handleApprovePlan)
	d.registerRPCDynamic("session.add_dir", rpc.ScopeAdmin, false, d.handleAddDir, d.addDirScope, true)
	d.registerRPC("governance.approval.resolve", rpc.ScopeAdmin, false, d.handleApprovalResolve, true)
	d.registerRPC("question.answer", rpc.ScopeWrite, false, d.handleUserAnswer)
	d.registerRPC("question.pending", rpc.ScopeRead, false, d.handlePendingUserQuestions)
	d.registerRPC("execution.btw", rpc.ScopeWrite, false, d.handleTaskBtw)
	d.registerRPC("history.recent", rpc.ScopeRead, false, d.handleHistoryRecent)
	d.registerRPC("memory.list", rpc.ScopeRead, false, d.handleMemoryList)
	d.registerRPC("memory.context", rpc.ScopeRead, false, d.handleMemoryContext)
	d.registerRPC("memory.search", rpc.ScopeRead, false, d.handleMemorySearch)
	d.registerRPC("memory.status", rpc.ScopeRead, false, d.handleMemoryStatus)
	d.registerRPC("memory.write", rpc.ScopeWrite, false, d.handleMemoryWrite, true)
	d.registerRPC("memory.read", rpc.ScopeRead, false, d.handleMemoryRead)
	d.registerRPC("memory.handoff", rpc.ScopeWrite, false, d.handleMemoryHandoff, true)
	d.registerRPC("memory.rollback", rpc.ScopeWrite, false, d.handleMemoryRollback, true)
	d.registerRPC("memory.verify", rpc.ScopeRead, false, d.handleMemoryVerify)
	d.registerRPC("memory.projection.authorize", rpc.ScopeAdmin, false, d.handleMemoryProjectionAuthorize, true)
	d.registerRPC("memory.projection.reseed", rpc.ScopeAdmin, false, d.handleMemoryProjectionReseed, true)
	d.registerRPC("memory.projection.retry", rpc.ScopeAdmin, false, d.handleMemoryProjectionRetry, true)
	d.registerRPC("schedule.create", rpc.ScopeWrite, false, d.handleScheduleCreate, true)
	d.registerRPC("schedule.list", rpc.ScopeRead, false, d.handleScheduleList)
	d.registerRPC("schedule.pause", rpc.ScopeWrite, false, d.handleSchedulePause, true)
	d.registerRPC("schedule.resume", rpc.ScopeWrite, false, d.handleScheduleResume, true)
	d.registerRPC("schedule.delete", rpc.ScopeWrite, false, d.handleScheduleDelete, true)
	d.registerRPC("goal.get", rpc.ScopeRead, false, d.handleGoalGet)
	d.registerRPC("goal.set", rpc.ScopeWrite, false, d.handleGoalSet, true)
	d.registerRPC("goal.clear", rpc.ScopeWrite, false, d.handleGoalClear, true)
	d.registerRPC("goal.pause", rpc.ScopeWrite, false, d.handleGoalPause, true)
	d.registerRPC("goal.resume", rpc.ScopeWrite, false, d.handleGoalResume, true)
	d.registerRPC("goal.complete", rpc.ScopeWrite, false, d.handleGoalComplete, true)
	d.registerRPC("goal.continue", rpc.ScopeWrite, false, d.handleGoalContinue, true)
	d.registerRPC("workflow.run", rpc.ScopeWrite, false, d.handleWorkflowRun, true)
	d.registerRPC("workflow.list", rpc.ScopeRead, true, d.handleWorkflowList)
	d.registerRPC("workflow.detail", rpc.ScopeRead, true, d.handleWorkflowDetail)
	d.registerRPC("workflow.pause", rpc.ScopeWrite, false, d.handleWorkflowPause, true)
	d.registerRPC("workflow.resume", rpc.ScopeWrite, false, d.handleWorkflowResume, true)
	d.registerRPC("workflow.stop", rpc.ScopeWrite, false, d.handleWorkflowStop, true)
	d.registerRPC("workflow.restart", rpc.ScopeWrite, false, d.handleWorkflowRestart, true)
	d.registerRPC("workflow.save", rpc.ScopeWrite, false, d.handleWorkflowSave, true)
	d.registerRPC("channel.sender.register", rpc.ScopeAdmin, false, d.handleChannelSenderRegister, true)
	d.registerRPC("channel.sender.list", rpc.ScopeAdmin, false, d.handleChannelSenderList)
	d.registerRPC("channel.event.inject", rpc.ScopeAdmin, true, d.handleChannelEventInject, true)
	d.registerRPC("channel.event.pending", rpc.ScopeAdmin, false, d.handleChannelEventPending)
	d.registerRPC("channel.event.reconcile", rpc.ScopeAdmin, false, d.handleChannelEventReconcile, true)
	d.registerRPC("extension.install", rpc.ScopeAdmin, false, d.handleExtensionInstall, true)
	d.registerRPC("extension.list", rpc.ScopeRead, false, d.handleExtensionList)
	d.registerRPC("extension.enable", rpc.ScopeAdmin, false, d.handleExtensionEnable, true)
	d.registerRPC("extension.disable", rpc.ScopeAdmin, false, d.handleExtensionDisable, true)
	d.registerRPC("extension.update", rpc.ScopeAdmin, false, d.handleExtensionUpdate, true)
	d.registerRPC("extension.safe_mode", rpc.ScopeAdmin, false, d.handleExtensionSafeMode, true)
	d.registerRPC("telemetry.status", rpc.ScopeRead, true, d.handleTelemetryStatus)
	d.registerRPC("artifact.stat", rpc.ScopeRead, false, d.handleArtifactStat)
	d.registerRPC("artifact.read", rpc.ScopeRead, false, d.handleArtifactRead)
	d.registerRPC("artifact.upload", rpc.ScopeWrite, false, d.handleArtifactUpload)

	d.registerRPC("execution.start", rpc.ScopeWrite, false, d.handleTaskSubmit)
	d.registerRPC("execution.retry", rpc.ScopeWrite, false, d.handleTaskRetry)
	d.registerRPC("execution.resume", rpc.ScopeWrite, false, d.handleTaskResume, true)
	d.registerRPC("execution.status", rpc.ScopeRead, true, d.handleTaskStatus)
	d.registerRPC("execution.list", rpc.ScopeRead, true, d.handleTaskList)
	d.registerRPC("execution.result", rpc.ScopeRead, true, d.handleTaskResult)
	d.registerRPC("execution.cancel", rpc.ScopeWrite, false, d.handleTaskCancel)
	d.registerRPC("execution.interrupt", rpc.ScopeWrite, false, d.handleTaskInterrupt)
	d.registerRPC("execution.steer", rpc.ScopeWrite, false, d.handleTaskSteer)
	d.registerRPC("execution.queue.list", rpc.ScopeRead, false, d.handleExecutionQueueList)
	d.registerRPC("execution.queue.drop", rpc.ScopeWrite, false, d.handleExecutionQueueDrop)
	d.registerRPC("execution.budget.extend", rpc.ScopeAdmin, false, d.handleTaskBudgetExtend, true)
	d.registerRPC("governance.action.approve", rpc.ScopeAdmin, false, d.handleApprove, true)
	d.registerRPCDynamic("governance.action.deny", rpc.ScopeAdmin, false, d.handleDeny, d.taskActionDenyScope, true)

	d.registerRPC("workspace.tree", rpc.ScopeRead, false, d.handleWorkspaceTree)
	d.registerRPC("workspace.diff", rpc.ScopeRead, false, d.handleWorkspaceDiff)
	d.registerRPC("workspace.search", rpc.ScopeRead, false, d.handleWorkspaceSearch)
	d.registerRPC("workspace.file.get", rpc.ScopeRead, false, d.handleFileGet)
	d.registerRPC("mcp.inventory", rpc.ScopeRead, false, d.handleMCPInventory)
	d.registerRPCDynamic("workspace.trust", rpc.ScopeAdmin, false, d.handleWorkspaceTrust, workspaceTrustScope, true)
	d.registerRPCDynamic("workspace.patch.propose", rpc.ScopeWrite, false, d.handlePatchPropose, patchProposeScope)
	d.registerRPC("workspace.patch.apply", rpc.ScopeWrite, false, d.handlePatchApply)
	d.registerRPC("workspace.patch.verify", rpc.ScopeWrite, false, d.handlePatchVerify)
	d.registerRPC("workspace.patch.rollback.preview", rpc.ScopeRead, false, d.handlePatchRollbackPreview)
	d.registerRPC("workspace.patch.rollback", rpc.ScopeWrite, false, d.handlePatchRollback)
	d.registerRPC("workspace.patch.list", rpc.ScopeRead, false, d.handlePatchList)
	d.registerRPC("workspace.patch.show", rpc.ScopeRead, false, d.handlePatchShow)

	d.registerRPC("command.exec", rpc.ScopeWrite, false, d.handleCommandExec)
	d.registerRPC("audit.report", rpc.ScopeRead, true, d.handleAuditReport)
	d.registerRPC("audit.export", rpc.ScopeRead, true, d.handleAuditExport)
	d.registerRPC("audit.verify", rpc.ScopeRead, true, d.handleAuditVerify)
	d.registerRPC("profile.describe", rpc.ScopeRead, true, d.handleProfileDescribe)
	d.registerRPC("profile.inventory", rpc.ScopeRead, false, d.handleProfileInventory)
	d.registerRPC("config.inventory", rpc.ScopeRead, false, d.handleConfigInventory)
	d.registerRPC("skill.inventory", rpc.ScopeRead, false, d.handleSkillInventory)
	d.registerRPC("hook.inventory", rpc.ScopeRead, false, d.handleHookInventory)
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
	d.registerRPC("work.cancel", rpc.ScopeAdmin, false, d.handleWorkCancel, true)
	d.registerRPC("work.poll", rpc.ScopeWorker, true, d.handleWorkPoll)
	d.registerRPC("work.renew", rpc.ScopeWorker, true, d.handleWorkRenew)
	d.registerRPC("work.report", rpc.ScopeWorker, true, d.handleWorkReport)

	d.registerRPC("daemon.remote.disable", rpc.ScopeAdmin, false, d.handleRemoteDisable, true)
	d.registerRPC("daemon.reload", rpc.ScopeAdmin, false, d.handleReload, true)
	// Local operator control of always-approve (interactive_approval inverted).
	// Write scope so the TUI can toggle without an admin token; not remote.
	d.registerRPC("daemon.set_interactive_approval", rpc.ScopeWrite, false, d.handleSetInteractiveApproval, true)
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
	specs := loadAgentSpecs(root)
	if d.safeMode {
		specs = builtinAgentSpecs()
	}
	return map[string]any{"agents": sortedAgentInfos(specs, p.IncludeHidden)}, nil
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
	specs := d.commandSpecs(root)
	if d.safeMode {
		specs = builtinCommandSpecs()
	}
	infos := sortedCommandInfos(specs)
	return map[string]any{
		"revision": commandRegistryRevision(infos),
		"commands": infos,
	}, nil
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

	byokStatuses := byokProbe(byokProviderList(d.providerCatalog, d.disabledProviders), func(providerID string) bool {
		if d.authStore == nil {
			return false
		}
		_, ok, err := d.authStore.Get(providerID)
		return err == nil && ok
	}, os.Getenv)

	lspStatuses := lspProbe(realLookPath)

	policyStale, policyReason := policyBundleStale(d.policyDir, d.org)

	report := map[string]any{
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
		"reasoner": d.reasonerReady(),
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
	}
	if d.memoryHMS == nil {
		report["hms_memory"] = map[string]any{"configured": false, "ok": true}
	} else {
		h := d.memoryHMS.Health()
		projection := d.memoryProjectionStatus()
		projectionOK := true
		if d.memoryProjection != nil {
			ps := d.memoryProjection.Status()
			projectionOK = ps.Dirty == 0 && ps.Failed == 0 && ps.Blocked == 0 && ps.Reconcile == 0
			projection["affected"] = nonHealthyProjectionItems(d.memoryProjection.Items(nil))
		}
		report["hms_memory"] = map[string]any{
			"configured": true, "credential_resolved": d.memoryHMS.apiKey != "",
			"credential_source": "env:" + d.memoryHMSAPIKeyEnv,
			"endpoint_host":     h.EndpointHost, "last_state": h.LastState,
			"last_success": h.LastSuccess, "projection": projection,
			"ok":     (h.LastState == "ok" || h.LastState == "not_checked") && projectionOK,
			"reason": "reachability is cached from governed session calls; doctor does not bypass NetworkAccess",
		}
	}
	artifactHealth := d.artifacts.Health()
	report["artifact_store"] = map[string]any{"ok": artifactHealth.OK, "health": artifactHealth, "metrics": d.artifacts.Metrics()}
	report["resources"] = d.resourceSummary(time.Now().UTC())
	if info, err := os.Stat(d.stateDir); err == nil {
		report["state_dir_permissions"] = map[string]any{"ok": info.Mode().Perm() == 0o700, "mode": fmt.Sprintf("%04o", info.Mode().Perm())}
	}
	fixPlan := []map[string]any{}
	if d.memoryProjection != nil {
		for _, item := range nonHealthyProjectionItems(d.memoryProjection.Items(nil)) {
			action, severity := fmt.Sprintf("carina memory projection-authorize %s", item.SessionID), "warn"
			switch item.Status {
			case projectionReconcile:
				severity = "error"
				action = fmt.Sprintf("carina memory projection-reseed %s %s --remote-quiesced; carina memory projection-authorize %s", item.SessionID, item.DocumentID, item.SessionID)
			case projectionFailed:
				severity = "error"
				action = fmt.Sprintf("carina memory projection-retry %s %s; carina memory projection-authorize %s", item.SessionID, item.DocumentID, item.SessionID)
			}
			fixPlan = append(fixPlan, map[string]any{"check": "hms_memory_projection", "severity": severity, "issue": fmt.Sprintf("projection %s is %s (%s)", item.DocumentID, item.Status, item.ErrorCode), "action": action, "automatic": false})
		}
	}
	interrupted := 0
	for _, run := range d.workflowRuns.List() {
		if run.Status == workflowui.Interrupted {
			interrupted++
		}
	}
	if interrupted > 0 {
		fixPlan = append(fixPlan, map[string]any{"check": "workflow_runs", "severity": "warn", "issue": fmt.Sprintf("%d workflow run(s) were interrupted", interrupted), "action": "inspect workflow.detail, then call workflow.resume or workflow.stop", "automatic": false})
	}
	if len(d.channels.Senders()) == 0 {
		fixPlan = append(fixPlan, map[string]any{"check": "channels", "severity": "info", "issue": "no trusted channel senders configured", "action": "set CARINA_CHANNEL_* and register a sender with channel.sender.register", "automatic": false})
	}
	channelIncidents := d.channels.Incidents()
	report["channels"] = map[string]any{"pending_reconciliation": channelIncidents, "ok": len(channelIncidents) == 0}
	if len(channelIncidents) > 0 {
		fixPlan = append(fixPlan, map[string]any{"check": "channels", "severity": "error", "issue": fmt.Sprintf("%d channel event(s) require crash reconciliation", len(channelIncidents)), "action": "inspect channel.event.pending, verify the external side effect, then call channel.event.reconcile with confirmed=true", "automatic": false})
	}
	inv := d.extensions.Inventory()
	if inv.SafeMode {
		fixPlan = append(fixPlan, map[string]any{"check": "extensions", "severity": "info", "issue": "extension safe mode is enabled", "action": "keep enabled for diagnosis or explicitly disable with extension.safe_mode", "automatic": false})
	}
	restoreJournals, _ := filepath.Glob(filepath.Join(d.stateDir, "runs", "*.restore.json"))
	report["restore"] = map[string]any{"pending_journals": len(restoreJournals), "ok": len(restoreJournals) == 0}
	if len(restoreJournals) > 0 {
		fixPlan = append(fixPlan, map[string]any{"check": "restore", "severity": "warn", "issue": fmt.Sprintf("%d restore journal(s) require verification", len(restoreJournals)), "action": "inspect session.checkpoint.preview before retrying or clearing a restore journal", "automatic": false})
	}
	launcher := map[string]any{"ok": false}
	if exe, err := os.Executable(); err == nil {
		if info, err := os.Stat(exe); err == nil {
			launcher = map[string]any{"ok": !info.IsDir() && info.Mode()&0o111 != 0, "path": exe}
		}
	}
	report["launcher"] = launcher
	if launcher["ok"] != true {
		fixPlan = append(fixPlan, map[string]any{"check": "launcher", "severity": "warn", "issue": "current launcher is not executable", "action": "reinstall Carina from a signed package", "automatic": false})
	}
	channel := strings.TrimSpace(os.Getenv("CARINA_UPDATE_CHANNEL"))
	if channel == "" {
		channel = "stable"
	}
	validChannel := channel == "stable" || channel == "beta" || channel == "nightly"
	report["update_channel"] = map[string]any{"ok": validChannel, "channel": channel}
	if !validChannel {
		fixPlan = append(fixPlan, map[string]any{"check": "update_channel", "severity": "warn", "issue": "unknown update channel " + channel, "action": "set CARINA_UPDATE_CHANNEL to stable, beta, or nightly", "automatic": false})
	}
	report["channels"] = map[string]any{"configured_senders": len(d.channels.Senders()), "secret_policy": "env:CARINA_CHANNEL_*"}
	report["runtime_protocol"] = map[string]any{"version": runtimeProtocolVersion, "negotiation": "runtime.initialize"}
	report["telemetry"] = map[string]any{"enabled": d.telemetry.Enabled(), "format": "carina-telemetry-json-v1", "otlp": false}
	report["compaction_circuit"] = d.compactionBreaker.snapshot()
	report["fix_plan"] = fixPlan
	return report, nil
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
	return map[string]any{"local": st}, nil
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
	if p.SessionID != "" && d.kern != nil {
		allowed, dec, err := d.gateContextCompressRPC(p.SessionID, p.TaskID, "context_compress")
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, fmt.Errorf("context compression denied by policy: %s", dec.Reason)
		}
	}
	res, err := d.contextEng.Compress(context.Background(), req)
	if err != nil {
		return nil, err
	}
	if p.SessionID != "" && d.kern != nil {
		d.record(p.SessionID, "ContextCompacted", p.TaskID, "go", map[string]any{
			"status": "context_compressed", "engine": res.Engine, "turn": p.Turn, "kind": p.Kind, "tool": p.Tool,
			"original_bytes": res.OriginalBytes, "compressed_bytes": res.CompressedBytes,
			"original_tokens": res.OriginalTokens, "compressed_tokens": res.CompressedTokens,
			"savings_percent": res.SavingsPercent, "transforms": res.Transforms,
			"original_sha256": res.OriginalSHA256, "original_ref": res.OriginalRef,
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
	artifactUsage, artifactErr := d.artifacts.Usage()
	artifactMetrics := map[string]any{"usage": artifactUsage, "operations": d.artifacts.Metrics()}
	if artifactErr != nil {
		artifactMetrics["error"] = artifactErr.Error()
	}
	retryMetrics := map[string]any{"scope": "daemon", "enabled": false}
	if d.retryGovernance != nil {
		retryMetrics = d.retryGovernance.metricsSnapshot()
		retryMetrics["enabled"] = true
	}
	report := map[string]any{
		"version":         Version,
		"uptime_seconds":  int(time.Since(d.started).Seconds()),
		"tasks_by_status": d.sched.CountByStatus(),
		"model_usage":     d.router.UsageByProvider(),
		"subscribers":     d.events.SubscriberCount(),
		"backpressure":    d.backpressure.snapshot(time.Now().UTC()),
		"debug_trace":     d.debugTraceStats(),
		"artifacts":       artifactMetrics,
		"provider_retry":  retryMetrics,
		"resources":       d.resourceSummary(time.Now().UTC()),
	}
	if d.journey != nil {
		report["first_five_minute_journey"] = d.journey.snapshot()
	}
	return report, nil
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
	workspaceRoot, err := d.validateSessionWorkspace(p.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	sess, err := d.createSession(workspaceRoot, p.Profile, p.ApprovalMode)
	if err != nil {
		return nil, err
	}
	if err := d.kern.InitSessionFull(sess.SessionID, sess.WorkspaceRoot, sess.PermissionProfile, sess.ApprovalMode, d.org); err != nil {
		return nil, fmt.Errorf("kernel session init: %w", err)
	}
	d.runLifecycleHooks(sess.WorkspaceRoot, "SessionStart", map[string]any{"session_id": sess.SessionID, "workspace_root": sess.WorkspaceRoot})
	return sess, nil
}

type sessionContinuityEntry struct {
	*sessionstore.Session
	LatestTaskID     string            `json:"latest_task_id,omitempty"`
	LatestTaskAgent  string            `json:"latest_task_agent,omitempty"`
	LatestResultKind string            `json:"latest_run_result_kind,omitempty"`
	TaskRevision     int64             `json:"task_revision,omitempty"`
	TaskStatus       string            `json:"task_status,omitempty"`
	Summary          string            `json:"summary,omitempty"`
	Continuity       *continuity.State `json:"continuity,omitempty"`
	UpdatedAt        time.Time         `json:"updated_at,omitempty"`
}

func (d *Daemon) projectSession(sess *sessionstore.Session, task *scheduler.ExecutionRun) sessionContinuityEntry {
	entry := sessionContinuityEntry{Session: sess}
	if task != nil {
		state := task.Continuity
		entry.LatestTaskID, entry.LatestTaskAgent = task.RunID, task.Agent
		entry.LatestResultKind = task.ResultKind
		entry.TaskRevision, entry.TaskStatus, entry.Summary = task.Revision, task.Status, task.Summary
		entry.Continuity, entry.UpdatedAt = &state, task.UpdatedAt
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = sess.CreatedAt
	}
	return entry
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
	return d.projectSession(sess, d.latestSessionTask(id)), nil
}

func (d *Daemon) handleSessionList(params json.RawMessage) (any, error) {
	var p struct {
		Archived *bool `json:"archived"`
	}
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	tasks := d.sched.List()
	latest := map[string]*scheduler.ExecutionRun{}
	for _, task := range tasks {
		current := latest[task.SessionID]
		if current == nil || task.UpdatedAt.After(current.UpdatedAt) {
			latest[task.SessionID] = task
		}
	}
	out := make([]sessionContinuityEntry, 0, len(d.store.List()))
	for _, sess := range d.store.List() {
		if p.Archived != nil && (sess.Status == "closed") != *p.Archived {
			continue
		}
		entry := d.projectSession(sess, latest[sess.SessionID])
		// Always expose a recency timestamp so clients can resume the most recent
		// conversation instead of map-iteration order.
		if entry.UpdatedAt.IsZero() {
			entry.UpdatedAt = sess.CreatedAt
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out, nil
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
	return d.handleSessionArchive(params)
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
		EventMode string `json:"event_mode"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("session_id required")
	}
	mode, err := parseEventMode(p.EventMode)
	if err != nil {
		return nil, err
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
	var events any = all[since:]
	if mode == eventModeCanonical {
		projectedEvents := make([]any, 0, len(all)-since)
		for index, event := range all[since:] {
			if projected, ok := projectEvent(mode, event, since+index+1); ok {
				projectedEvents = append(projectedEvents, projected)
			}
		}
		events = projectedEvents
	}
	return map[string]any{
		"events":     events,
		"from":       since,
		"cursor":     len(all),
		"event_mode": mode,
	}, nil
}

type sessionForkParams struct {
	SessionID    string `json:"session_id"`
	LastTaskID   string `json:"last_task_id"`
	ThroughTurn  int    `json:"through_turn"`
	BeforeFirst  bool   `json:"before_first"`
	ClientForkID string `json:"client_fork_id"`
}

// handleSessionFork branches a session at a source-owned conversation
// boundary. A client fork identity makes retries idempotent even when child
// creation succeeds but the response or destination hydration is interrupted.
func (d *Daemon) handleSessionFork(params json.RawMessage) (any, error) {
	var p sessionForkParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	id := p.SessionID
	src, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", id)
	}
	if p.BeforeFirst && (p.LastTaskID != "" || p.ThroughTurn > 0) {
		return nil, fmt.Errorf("before_first cannot be combined with last_task_id or through_turn")
	}
	if p.ClientForkID != "" && !validClientSubmissionID(p.ClientForkID) {
		return nil, fmt.Errorf("client_fork_id must be a 1-128 byte ASCII token using letters, digits, '.', '_', ':', or '-'")
	}
	fingerprint := sessionForkFingerprint(p)

	d.forkMu.Lock()
	defer d.forkMu.Unlock()
	if existing, err := d.store.FindForkRequest(src.SessionID, p.ClientForkID, fingerprint); err != nil {
		return nil, err
	} else if existing != nil {
		if err := d.ensureKernelSession(existing); err != nil {
			return nil, err
		}
		d.setPlanMode(existing.SessionID, existing.PlanMode)
		return existing, nil
	}

	var sourceTask *scheduler.ExecutionRun
	for _, task := range d.sched.List() {
		if task.SessionID != id {
			continue
		}
		switch task.Status {
		case "running", "queued", "waiting_approval", "paused":
			return nil, fmt.Errorf("cannot fork session %s while task %s is %s", id, task.RunID, task.Status)
		}
		if p.LastTaskID != "" && task.RunID == p.LastTaskID {
			sourceTask = task
		}
		if p.LastTaskID == "" && (sourceTask == nil || task.UpdatedAt.After(sourceTask.UpdatedAt)) {
			sourceTask = task
		}
	}
	if !p.BeforeFirst && sourceTask == nil {
		return nil, fmt.Errorf("cannot fork session %s without a completed task checkpoint", id)
	}
	var sourceTaskID string
	var sourceTurn int
	if sourceTask != nil {
		cp := d.runs.loadCheckpoint(sourceTask.RunID)
		if p.ThroughTurn > 0 {
			cp = d.runs.loadCheckpointTurn(sourceTask.RunID, p.ThroughTurn)
		}
		if cp == nil {
			return nil, fmt.Errorf("fork boundary not found for task %s", sourceTask.RunID)
		}
		sourceTaskID = sourceTask.RunID
		sourceTurn = cp.Turn
	}
	child, created, err := d.store.CreateForkSession(src, sourceTaskID, sourceTurn, p.ClientForkID, fingerprint)
	if err != nil {
		return nil, err
	}
	if err := d.kern.InitSessionFull(child.SessionID, child.WorkspaceRoot, child.PermissionProfile, child.ApprovalMode, d.org); err != nil {
		if created {
			_, _ = d.store.SetStatus(child.SessionID, "closed")
			_ = d.store.Delete(child.SessionID)
		}
		return nil, fmt.Errorf("fork init: %w", err)
	}
	d.setPlanMode(child.SessionID, child.PlanMode)
	if created {
		d.record(child.SessionID, "SessionForked", "", "go",
			map[string]any{
				"status":         "forked",
				"parent":         src.SessionID,
				"source_task_id": sourceTaskID,
				"through_turn":   sourceTurn,
				"before_first":   p.BeforeFirst,
			}, "")
	}
	return child, nil
}

func sessionForkFingerprint(p sessionForkParams) string {
	p.ClientForkID = ""
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
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
	if !p.On {
		return nil, fmt.Errorf("plan mode can only be exited through session.approve_plan")
	}
	if _, err := d.store.SetPlanMode(p.SessionID, p.On); err != nil {
		return nil, err
	}
	d.setPlanMode(p.SessionID, p.On)
	if err := d.noticePlanModeSwitch(p.SessionID, p.On); err != nil {
		_, _ = d.store.SetPlanMode(p.SessionID, false)
		d.setPlanMode(p.SessionID, false)
		return nil, fmt.Errorf("persist plan-mode notification: %w", err)
	}
	return map[string]any{"session_id": p.SessionID, "plan_mode": p.On}, nil
}

func (d *Daemon) handleSessionModelGet(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	return map[string]any{"session_id": sess.SessionID, "next_model": sess.NextModel, "next_reasoning_effort": sess.NextReasoningEffort}, nil
}

func (d *Daemon) handleSessionModelSet(params json.RawMessage) (any, error) {
	var p struct {
		SessionID       string `json:"session_id"`
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	p.Model = strings.TrimSpace(p.Model)
	if err := d.validateTaskModel(p.Model); err != nil {
		return nil, err
	}
	// Remap/clear effort to what the destination model can actually send.
	// Stale session effort (e.g. low after switching onto a no-effort route)
	// must not hard-fail the preference write.
	p.ReasoningEffort = resolveEffortForModel(d.reasoningEffortSpec(p.Model), p.ReasoningEffort)
	sess, err := d.store.SetNextModelPreference(p.SessionID, p.Model, p.ReasoningEffort)
	if err != nil {
		return nil, err
	}
	return map[string]any{"session_id": sess.SessionID, "next_model": sess.NextModel, "next_reasoning_effort": sess.NextReasoningEffort}, nil
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
	d.record(sess.SessionID, "DirectoryGranted", "", "go",
		map[string]any{"status": "dir_granted", "path": abs}, "")
	return map[string]any{"session_id": sess.SessionID, "path": abs, "granted": true}, nil
}

// handleApprovePlan approves the latest completed plan exactly once. Approval
// changes execution state: it exits plan mode and submits a governed build task
// carrying the approved summary, rather than emitting a display-only receipt.
func (d *Daemon) handleApprovePlan(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	sess, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", id)
	}
	if !sess.PlanMode {
		result := map[string]any{"session_id": id, "plan_mode": false, "approved": true}
		if task := d.approvedPlanBuildTask(id); task != nil {
			result["task"] = task
		}
		return result, nil
	}
	latest := d.latestSessionTask(id)
	if _, err := d.store.SetPlanMode(id, false); err != nil {
		return nil, err
	}
	d.setPlanMode(id, false)
	if err := d.noticePlanModeSwitch(id, false); err != nil {
		_, _ = d.store.SetPlanMode(id, true)
		d.setPlanMode(id, true)
		return nil, fmt.Errorf("persist plan approval notification: %w", err)
	}
	result := map[string]any{"session_id": id, "plan_mode": false, "approved": true}
	if latest == nil || latest.Status != "completed" || latest.Agent != "plan" || latest.ResultKind != "plan" || strings.TrimSpace(latest.Summary) == "" {
		return result, nil
	}
	model := strings.TrimSpace(latest.RequestedModel)
	if model == "" {
		model = strings.TrimSpace(latest.Model)
	}
	effort := strings.TrimSpace(latest.RequestedReasoningEffort)
	if effort == "" {
		effort = strings.TrimSpace(latest.EffectiveReasoningEffort)
	}
	submissionID := "plan-approval:" + latest.RunID
	buildParams, err := json.Marshal(map[string]any{
		"session_id":           id,
		"client_submission_id": submissionID,
		"prompt":               "Implement this approved plan:\n\n" + strings.TrimSpace(latest.Summary),
		"model":                model,
		"agent":                "build",
		"reasoning_effort":     effort,
		"locale":               latest.Locale,
	})
	if err != nil {
		return nil, err
	}
	build, err := d.handleTaskSubmit(buildParams)
	if err != nil {
		_, _ = d.store.SetPlanMode(id, true)
		d.setPlanMode(id, true)
		_ = d.noticePlanModeSwitch(id, true)
		return nil, fmt.Errorf("approve plan: submit implementation: %w", err)
	}
	result["task"] = build
	return result, nil
}

func (d *Daemon) latestSessionTask(sessionID string) *scheduler.ExecutionRun {
	var latest *scheduler.ExecutionRun
	for _, task := range d.sched.List() {
		if task.SessionID != sessionID {
			continue
		}
		if latest == nil || task.UpdatedAt.After(latest.UpdatedAt) ||
			(task.UpdatedAt.Equal(latest.UpdatedAt) && task.RunID > latest.RunID) {
			latest = task
		}
	}
	return latest
}

func (d *Daemon) approvedPlanBuildTask(sessionID string) *scheduler.ExecutionRun {
	var latest *scheduler.ExecutionRun
	for _, task := range d.sched.List() {
		if task.SessionID != sessionID || task.Agent != "build" ||
			!strings.HasPrefix(task.ClientSubmissionID, "plan-approval:") {
			continue
		}
		if latest == nil || task.UpdatedAt.After(latest.UpdatedAt) {
			latest = task
		}
	}
	return latest
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
	recallProvider := map[string]any{
		"enabled":  false,
		"provider": "off",
		"reason":   "external recall provider is not configured",
	}
	if d.memoryHMS != nil {
		h := d.memoryHMS.Health()
		recallProvider = map[string]any{
			"enabled": true, "provider": "hms", "mode": h.Mode,
			"adapter_version": h.Adapter, "endpoint_host": h.EndpointHost,
			"last_state": h.LastState, "last_reason": h.LastReason,
			"configured": h.Configured, "authorized": h.Authorized,
			"last_attempt": h.LastAttempt, "last_success": h.LastSuccess,
			"last_latency_ms": h.LastLatency, "last_evidence_count": h.LastEvidence,
			"authority": "local Carina memory remains authoritative; HMS evidence is derived and untrusted",
		}
	}
	semanticProvider := map[string]any{
		"enabled": false, "provider": "local-only",
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
		"recall_provider":   recallProvider,
		"projection":        d.memoryProjectionStatus(scope),
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
		SessionID        string            `json:"session_id"`
		Action           string            `json:"action"`
		Target           string            `json:"target"`
		Content          string            `json:"content"`
		OldText          string            `json:"old_text"`
		Ops              []memoryOperation `json:"operations"`
		Version          int               `json:"version"`
		ExpectedRevision string            `json:"expected_revision"`
		IdempotencyKey   string            `json:"idempotency_key"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	req := memoryWriteRequest{
		Action:           p.Action,
		Target:           p.Target,
		Content:          p.Content,
		OldText:          p.OldText,
		Operations:       p.Ops,
		ExpectedRevision: p.ExpectedRevision,
		IdempotencyKey:   p.IdempotencyKey,
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
	// The WAL marker, canonical mutation, and desired-state materialization are
	// one per-document transaction. Without this lock two approved writes to the
	// same target can publish a stale desired generation.
	d.memoryProjectionWriteMu.Lock()
	defer d.memoryProjectionWriteMu.Unlock()
	requestPayload := map[string]any{"target": summary.Target, "action": summary.Action, "operation_count": summary.OperationCount, "content_sha256": summary.ContentSHA256, "scope_id": summary.ScopeID, "status": "prepared"}
	if err := d.recordChecked(sess.SessionID, "MemoryWriteRequested", taskID, "go", requestPayload, decision.DecisionID); err != nil {
		return memoryWriteResult{}, fmt.Errorf("memory write audit WAL: %w", err)
	}
	before, err := d.memory.list(scope, summary.Target)
	if err != nil {
		return memoryWriteResult{}, err
	}
	documentID := memoryDocumentID(scope, before.Target)
	actualRevision := memoryRevisionID(documentID, before.Entries)
	if req.ExpectedRevision != "" && req.ExpectedRevision != actualRevision {
		return memoryWriteResult{}, fmt.Errorf("memory revision conflict: expected %s, actual %s", req.ExpectedRevision, actualRevision)
	}
	dirty, err := d.prepareMemoryProjection(sess, scope, summary.Target)
	if err != nil {
		return memoryWriteResult{}, fmt.Errorf("memory projection write-ahead: %w", err)
	}
	result, err := d.memory.apply(scope, req)
	if err != nil {
		if dirty != nil {
			_ = d.memoryProjection.DiscardDirty(dirty.DocumentID, dirty.Generation)
		}
		return memoryWriteResult{}, err
	}
	result.DecisionID = decision.DecisionID
	result.ContentSHA256 = summary.ContentSHA256
	result.OperationCount = summary.OperationCount
	if !result.Success && dirty != nil {
		_ = d.memoryProjection.DiscardDirty(dirty.DocumentID, dirty.Generation)
	}
	afterState, err := d.memory.list(scope, summary.Target)
	if err != nil {
		_ = d.memory.restore(scope, summary.Target, before.Entries)
		return memoryWriteResult{}, err
	}
	predictedRevision := memoryRevisionID(documentID, afterState.Entries)
	payload := map[string]any{
		"status":          "memory_write",
		"target":          summary.Target,
		"action":          summary.Action,
		"success":         result.Success,
		"usage":           result.Usage,
		"entry_count":     result.EntryCount,
		"operation_count": summary.OperationCount,
		"content_sha256":  summary.ContentSHA256,
		"revision":        predictedRevision,
		"parent_revision": actualRevision,
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
	var prepared memoryRevision
	if result.Success {
		prepared, err = d.memoryVersions.prepare(sess.SessionID, scope, summary.Target, afterState.Entries, before.Entries, "memory.write", req.IdempotencyKey)
		if err != nil {
			_ = d.memory.restore(scope, summary.Target, before.Entries)
			if dirty != nil {
				_ = d.memoryProjection.DiscardDirty(dirty.DocumentID, dirty.Generation)
			}
			return memoryWriteResult{}, fmt.Errorf("memory revision prepare: %w", err)
		}
	}
	if err := d.recordChecked(sess.SessionID, "MemoryWritten", taskID, "go", payload, decision.DecisionID); err != nil {
		if result.Success {
			_ = d.memory.restore(scope, summary.Target, before.Entries)
		}
		if dirty != nil {
			_ = d.memoryProjection.DiscardDirty(dirty.DocumentID, dirty.Generation)
		}
		if prepared.Revision != "" {
			_ = d.memoryVersions.abort(prepared.DocumentID, prepared.Revision)
		}
		return memoryWriteResult{}, fmt.Errorf("memory write commit audit: %w", err)
	}
	if result.Success {
		revision, versionErr := d.memoryVersions.publish(prepared.DocumentID, prepared.Revision)
		if versionErr != nil {
			result.Version, result.Revision, result.ParentRevision = memoryControllerVersion, prepared.Revision, prepared.Parent
			result.Message += "; revision publication pending recovery"
		} else {
			result.Version, result.Revision, result.ParentRevision = memoryControllerVersion, revision.Revision, revision.Parent
		}
		result.Projection = d.finishMemoryProjection(sess, dirty)
	}
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

func (d *Daemon) planModeEnabled(sessionID string) bool { return d.isPlanMode(sessionID) }

// noticePlanModeSwitch queues an urgent mailbox notice for a session's active
// task when plan/act mode is toggled mid-run, so a task already executing
// sees the switch at the next turn boundary instead of only inferring it
// from a subsequent tool denial. Same shape as the channel-event notice in
// ecosystem.go's handleChannelEventInject: urgent-tier steerWithPriority,
// drained by the existing loop in agent.go's runLoopContext. This never
// touches enforcement (isPlanMode / the plan-mode tool gate is unchanged) —
// it only makes the switch legible to the model. A no-op if the session has
// no active task (e.g. the mode is set before a task is submitted).
func (d *Daemon) noticePlanModeSwitch(sessionID string, on bool) error {
	task := d.activeChannelTask(sessionID)
	if task == nil {
		return nil
	}
	var msg string
	if on {
		msg = "MODE SWITCH: plan mode is now ON — explore read-only and present a plan; edits, commands, and memory writes are blocked until the operator approves it (session.approve_plan)"
	} else {
		msg = "MODE SWITCH: plan mode is now OFF — the plan was approved (or plan mode was cleared); edits, commands, and memory writes are permitted again"
	}
	return d.steerWithPriority(task.RunID, msg, steerUrgent)
}

// ---- tasks ----------------------------------------------------------------

type taskSubmitParams struct {
	SessionID          string                   `json:"session_id"`
	ClientSubmissionID *string                  `json:"client_submission_id"`
	Prompt             string                   `json:"prompt"`
	Model              string                   `json:"model"`
	Agent              string                   `json:"agent"`
	Mode               string                   `json:"mode"`
	ReasoningEffort    string                   `json:"reasoning_effort"`
	Locale             string                   `json:"locale"`
	TokenBudget        int                      `json:"token_budget"`
	SuccessCriteria    []scheduler.SuccessCheck `json:"success_criteria"`
	OutputSchema       json.RawMessage          `json:"output_schema"`
	InputMediaRefs     []MediaRef               `json:"input_media_refs"`
}

func (d *Daemon) handleTaskSubmit(params json.RawMessage) (any, error) {
	return d.handleTaskSubmitInternal(params, "")
}

func (d *Daemon) handleTaskSubmitInternal(params json.RawMessage, retryOfRunID string) (any, error) {
	var p taskSubmitParams
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
	if strings.TrimSpace(p.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if strings.TrimSpace(p.Locale) != "" {
		locale, err := microcopy.CanonicalLocale(p.Locale)
		if err != nil {
			return nil, fmt.Errorf("locale: %w", err)
		}
		p.Locale = locale
	}
	if p.TokenBudget < 0 {
		return nil, fmt.Errorf("token_budget must be >= 0")
	}
	inputMediaRefs, err := d.validateTaskInputMedia(p.SessionID, p.InputMediaRefs)
	if err != nil {
		return nil, err
	}
	p.Model = strings.TrimSpace(p.Model)
	if err := d.validateTaskModel(p.Model); err != nil {
		return nil, err
	}
	p.Agent = strings.TrimSpace(p.Agent)
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	if p.Mode == "" {
		p.Mode = "background"
	}
	if p.Mode != "background" {
		return nil, fmt.Errorf("task submit mode must be background")
	}
	fence := d.sessionExecutionFence(sess.SessionID)
	fence.RLock()
	defer fence.RUnlock()
	submissionFingerprint := ""
	clientSubmissionID := ""
	if p.ClientSubmissionID != nil {
		clientSubmissionID = *p.ClientSubmissionID
		if !validClientSubmissionID(clientSubmissionID) {
			return nil, fmt.Errorf("client_submission_id must be a 1-128 byte ASCII token using letters, digits, '.', '_', ':', or '-'")
		}
		submissionFingerprint = taskSubmissionFingerprint(p)
		key := taskSubmissionKey(p.SessionID, clientSubmissionID)
		d.submissionMu.Lock()
		defer d.submissionMu.Unlock()
		if taskID := d.taskSubmissions[key]; taskID != "" {
			if task, exists := d.sched.Get(taskID); exists {
				if task.ClientSubmissionFingerprint != submissionFingerprint {
					return nil, fmt.Errorf("client_submission_id %q was already used for a different request", clientSubmissionID)
				}
				return task, nil
			}
			delete(d.taskSubmissions, key)
		}
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
		d.record(sess.SessionID, "CommandExpanded", "", "go",
			map[string]any{"status": "command_expanded", "command": expanded.Name}, "")
	}
	if agent == "" {
		agent = "build"
	}
	if model == "" {
		model = strings.TrimSpace(sess.NextModel)
	}
	agents := loadAgentSpecs(sess.WorkspaceRoot)
	if d.safeMode {
		agents = builtinAgentSpecs()
	}
	spec := agents[agent]
	if spec == nil {
		return nil, fmt.Errorf("unknown agent %q", agent)
	}
	if model == "" {
		model = spec.Model
	}
	requestedModel := model
	requestedEffort := normalizeReasoningEffort(p.ReasoningEffort)
	if requestedEffort == "" {
		requestedEffort = normalizeReasoningEffort(sess.NextReasoningEffort)
	}
	// End-to-end: only freeze efforts the route can encode. Invalid/stale
	// values remap (minimal→low) or clear instead of failing the whole turn.
	effortSpec := d.reasoningEffortSpec(model)
	effectiveEffort := resolveEffortForModel(effortSpec, requestedEffort)
	if requestedEffort != "" && effectiveEffort == "" {
		requestedEffort = ""
	} else if effectiveEffort != "" {
		requestedEffort = effectiveEffort
	}
	task := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, prompt, model, agent, p.SuccessCriteria)
	d.sched.SetLocale(task.RunID, p.Locale)
	d.sched.SetInputMediaRefs(task.RunID, inputMediaRefs)
	d.sched.SetModelState(task.RunID, requestedModel, taskModel(task))
	d.sched.SetReasoningEffortState(task.RunID, requestedEffort, effectiveEffort)
	if retryOfRunID != "" {
		d.sched.SetRetryOf(task.RunID, retryOfRunID)
	}
	if clientSubmissionID != "" {
		d.sched.SetClientSubmission(task.RunID, clientSubmissionID, submissionFingerprint)
	}
	if p.TokenBudget > 0 {
		d.sched.SetTokenBudget(task.RunID, p.TokenBudget)
	} else if budget := d.maxTaskTokens.Load(); budget > 0 {
		d.sched.SetTokenBudget(task.RunID, int(budget))
	}
	d.sched.SetMode(task.RunID, p.Mode)
	if len(p.OutputSchema) > 0 {
		d.sched.SetOutputSchema(task.RunID, p.OutputSchema)
	}
	// Scheduler setters publish immutable task copies. Capture the final
	// submission envelope once and use that same row for WAL, persistence, and
	// the asynchronous execution closure.
	if frozen, ok := d.sched.Get(task.RunID); ok {
		copy := *frozen
		copy.SuccessCriteria = append([]scheduler.SuccessCheck(nil), frozen.SuccessCriteria...)
		copy.InputMediaRefs = append([]scheduler.InputMediaRef(nil), frozen.InputMediaRefs...)
		copy.OutputSchema = append(json.RawMessage(nil), frozen.OutputSchema...)
		task = &copy
	}
	// Write-ahead (P1.8): the defining instruction must be durably
	// audit-chain-appended BEFORE any goroutine is dispatched to act on it,
	// and — unlike every other d.record() call site, which is fire-and-
	// forget — a FAILED append here must refuse the submission rather than
	// let an ungoverned task run whose instruction the audit trail can
	// never attest to. Call the kernel directly (bypassing d.record, whose
	// signature intentionally swallows the error for its many best-effort
	// callers) so this one write-ahead call can be checked.
	writeAheadPayload := map[string]any{
		"run_id": task.RunID, "user_prompt": task.UserPrompt,
		"model": task.Model, "requested_model": task.RequestedModel, "effective_model": task.EffectiveModel,
		"requested_reasoning_effort": task.RequestedReasoningEffort, "effective_reasoning_effort": task.EffectiveReasoningEffort,
		"agent": task.Agent, "mode": task.Mode,
		"locale":           task.Locale,
		"input_media_refs": task.InputMediaRefs,
	}
	if task.RetryOfRunID != "" {
		writeAheadPayload["retry_of_run_id"] = task.RetryOfRunID
	}
	receipt, err := d.kern.RecordEventWithCursor(sess.SessionID, "ExecutionQueued", task.RunID, "go", writeAheadPayload, "")
	if err != nil {
		_, _ = d.sched.Cancel(task.RunID)
		return nil, fmt.Errorf("execution_start_failed: write-ahead audit-chain append failed; execution was not dispatched: %w", err)
	}
	d.events.Publish(sess.SessionID, map[string]any{
		"event_id": receipt.EventID, "session_id": sess.SessionID, "task_id": task.RunID,
		"type": "ExecutionQueued", "actor": "go", "timestamp": time.Now().UTC().Format(time.RFC3339),
		"payload": writeAheadPayload, internalRawAuditCursor: receipt.Cursor,
	})
	_ = d.history.AppendScoped(history.Entry{ // shared cross-process prompt history (best-effort)
		Text: prompt, SessionID: sess.SessionID, WorkspaceRoot: sess.WorkspaceRoot,
	})
	if err := d.runs.saveChecked(task); err != nil {
		_, _ = d.sched.Cancel(task.RunID)
		return nil, fmt.Errorf("execution_start_failed: durable submission record failed; execution was not dispatched: %w", err)
	}
	if clientSubmissionID != "" {
		d.taskSubmissions[taskSubmissionKey(p.SessionID, clientSubmissionID)] = task.RunID
	}
	if d.journey != nil {
		d.journey.accepted(task.RunID, sess.SessionID)
	}

	d.startTask(func() { d.runTaskGuarded(sess, task) })
	if t, ok := d.sched.Get(task.RunID); ok {
		return t, nil
	}
	return task, nil
}

type taskRetryParams struct {
	RunID              string `json:"run_id"`
	ClientSubmissionID string `json:"client_submission_id"`
}

func (d *Daemon) handleTaskRetry(params json.RawMessage) (any, error) {
	var p taskRetryParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	p.RunID = strings.TrimSpace(p.RunID)
	if p.RunID == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	if !validClientSubmissionID(p.ClientSubmissionID) {
		return nil, fmt.Errorf("client_submission_id must be a 1-128 byte ASCII token using letters, digits, '.', '_', ':', or '-'")
	}
	original, ok := d.sched.Get(p.RunID)
	if !ok {
		return nil, fmt.Errorf("unknown execution %s", p.RunID)
	}
	if !matchesRetryableExecutionStatus(original.Status) {
		return nil, fmt.Errorf("execution %s is %s, not retryable", p.RunID, original.Status)
	}
	if original.Status == "interrupted" && original.Continuity.Recovery.Disposition == continuity.RecoveryResumeCheckpoint {
		return nil, fmt.Errorf("execution %s has automatic checkpoint recovery in progress", p.RunID)
	}
	media := make([]MediaRef, 0, len(original.InputMediaRefs))
	for _, ref := range original.InputMediaRefs {
		media = append(media, MediaRef{
			ArtifactID: ref.ArtifactID,
			MediaType:  ref.MediaType,
			Bytes:      ref.Bytes,
			Origin:     ref.Origin,
		})
	}
	mode := original.Mode
	if mode == "" || mode == "foreground" {
		mode = "background"
	}
	retryParams, err := json.Marshal(taskSubmitParams{
		SessionID:          original.SessionID,
		ClientSubmissionID: &p.ClientSubmissionID,
		Prompt:             original.UserPrompt,
		Model:              firstNonEmpty(original.RequestedModel, original.Model),
		Agent:              original.Agent,
		Mode:               mode,
		ReasoningEffort:    firstNonEmpty(original.RequestedReasoningEffort, original.EffectiveReasoningEffort),
		Locale:             original.Locale,
		TokenBudget:        original.TokenBudget,
		SuccessCriteria:    append([]scheduler.SuccessCheck(nil), original.SuccessCriteria...),
		OutputSchema:       append(json.RawMessage(nil), original.OutputSchema...),
		InputMediaRefs:     media,
	})
	if err != nil {
		return nil, fmt.Errorf("encode retry submission: %w", err)
	}
	return d.handleTaskSubmitInternal(retryParams, original.RunID)
}

func matchesRetryableExecutionStatus(status string) bool {
	switch status {
	case "failed", "degraded", "cancelled", "interrupted":
		return true
	default:
		return false
	}
}

func (d *Daemon) validateTaskModel(model string) error {
	if model == "" || model == "default" {
		return nil
	}
	if len(model) > 256 || strings.ContainsAny(model, " \t\r\n\x00") {
		return fmt.Errorf("model must be a non-empty identifier without whitespace (maximum 256 bytes)")
	}
	providerID, _, hasProvider := strings.Cut(model, "/")
	if hasProvider {
		providerID = normalizeProviderID(providerID)
		if providerID == "" || d.providerCatalog[providerID].ID == "" {
			return fmt.Errorf("unknown model provider %q", providerID)
		}
		if d.disabledProviders[providerID] {
			return fmt.Errorf("model provider %q is disabled", providerID)
		}
	}
	return nil
}

func taskSubmissionKey(sessionID, clientSubmissionID string) string {
	return sessionID + "\x00" + clientSubmissionID
}

func taskSubmissionFingerprint(p taskSubmitParams) string {
	p.SessionID = ""
	p.ClientSubmissionID = nil
	if len(p.OutputSchema) > 0 {
		var schema any
		if json.Unmarshal(p.OutputSchema, &schema) == nil {
			p.OutputSchema, _ = json.Marshal(schema)
		}
	}
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validClientSubmissionID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '.' || c == '_' || c == ':' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func (d *Daemon) handleTaskStatus(params json.RawMessage) (any, error) {
	var p struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	task, ok := d.sched.Get(p.RunID)
	if !ok {
		return nil, fmt.Errorf("unknown execution %s", p.RunID)
	}
	return d.taskWithControl(task, task.RunID), nil
}

func (d *Daemon) handleTaskInterrupt(params json.RawMessage) (any, error) {
	var p struct {
		RunID string `json:"run_id"`
		Mode  string `json:"mode"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	p.RunID = strings.TrimSpace(p.RunID)
	p.Mode = strings.TrimSpace(p.Mode)
	if p.RunID == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	if p.Mode != "" && p.Mode != "soft" {
		return nil, fmt.Errorf("invalid interrupt mode %q (want soft)", p.Mode)
	}
	task, ok := d.sched.Get(p.RunID)
	if !ok {
		return nil, fmt.Errorf("unknown execution %s", p.RunID)
	}
	if !acceptsExecutionControl(task.Status) {
		return nil, fmt.Errorf("execution %s is %s and cannot be interrupted", p.RunID, task.Status)
	}
	already, depth, err := d.requestSoftInterrupt(p.RunID)
	if err != nil {
		return nil, fmt.Errorf("persist soft interrupt: %w", err)
	}
	activeTool := d.hasActiveToolCall(p.RunID)
	d.record(task.SessionID, "ExecutionProgressed", task.RunID, "operator", map[string]any{
		"status": "soft_interrupt_requested", "mode": "soft", "safe_point": "next_turn_boundary",
		"active_tool": activeTool, "queue_depth": depth,
	}, "")
	return map[string]any{
		"requested": true, "already_requested": already, "run_id": p.RunID, "mode": "soft",
		"safe_point": "next_turn_boundary", "active_tool": activeTool, "queue_depth": depth,
	}, nil
}

func (d *Daemon) handleTaskCancel(params json.RawMessage) (any, error) {
	d.checkpointMu.Lock()
	defer d.checkpointMu.Unlock()
	var p struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	task, err := d.sched.Cancel(p.RunID)
	if err != nil {
		return nil, err
	}
	d.record(task.SessionID, "ExecutionCancelled", task.RunID, "operator", map[string]any{
		"reason": "operator_cancelled", "reason_code": "operator_cancelled",
		"owner": "operator", "retryable": true,
	}, "")
	persistErr := d.runs.saveChecked(task)
	controlErr := d.discardExecutionControl(p.RunID)
	d.taskContextMu.Lock()
	cancel := d.taskCancels[p.RunID]
	d.taskContextMu.Unlock()
	if cancel != nil {
		cancel(context.Canceled)
	} else {
		d.emitCompletion(task.SessionID, task)
	}
	if persistErr != nil {
		return nil, fmt.Errorf("task_cancel_pending: task is cancelled in memory but durable persistence failed; retry execution.cancel: %w", persistErr)
	}
	if controlErr != nil {
		return nil, fmt.Errorf("task_cancel_pending: task is cancelled but queued follow-ups could not be discarded durably: %w", controlErr)
	}
	return d.taskWithControl(task, task.RunID), nil
}

// steerPriority selects which mailbox tier a steering message is queued
// into. Urgent messages are drained (and thus folded into the transcript)
// ahead of any normal-tier backlog at the next turn boundary, so a
// time-sensitive redirect (e.g. an external channel event) does not sit
// behind a pile of routine steering notes.
type steerPriority string

const (
	steerNormal steerPriority = "normal"
	steerUrgent steerPriority = "urgent"
)

func parseSteerPriority(raw string) (steerPriority, error) {
	switch steerPriority(strings.TrimSpace(raw)) {
	case "", steerNormal:
		return steerNormal, nil
	case steerUrgent:
		return steerUrgent, nil
	default:
		return "", fmt.Errorf("invalid priority %q (want normal|urgent)", raw)
	}
}

// taskMailbox is a two-tier FIFO queue: urgent messages are always drained
// before normal ones, preserving arrival order within each tier.
type taskMailbox struct {
	urgent                 []queuedSteer
	normal                 []queuedSteer
	softInterruptRequested bool
}

func (m *taskMailbox) pushEntry(message queuedSteer) {
	if message.Priority == steerUrgent {
		m.urgent = append(m.urgent, message)
		return
	}
	m.normal = append(m.normal, message)
}

func (m *taskMailbox) push(priority steerPriority, message string) {
	m.pushEntry(queuedSteer{SteerID: sessionstore.NewID("steer"), Message: message, Priority: priority})
}

func (m *taskMailbox) remove(steerID string) {
	remove := func(values []queuedSteer) []queuedSteer {
		for index := range values {
			if values[index].SteerID == steerID {
				return append(values[:index], values[index+1:]...)
			}
		}
		return values
	}
	m.urgent = remove(m.urgent)
	m.normal = remove(m.normal)
}

func (m *taskMailbox) depth() int {
	if m == nil {
		return 0
	}
	return len(m.urgent) + len(m.normal)
}

func (m *taskMailbox) empty() bool {
	return m == nil || (len(m.urgent) == 0 && len(m.normal) == 0)
}

// drain returns queued messages urgent-first, normal-second, each in FIFO
// arrival order within its tier.
func (m *taskMailbox) peek() []queuedSteer {
	if m == nil {
		return nil
	}
	if len(m.urgent) == 0 {
		return append([]queuedSteer(nil), m.normal...)
	}
	if len(m.normal) == 0 {
		return append([]queuedSteer(nil), m.urgent...)
	}
	out := make([]queuedSteer, 0, len(m.urgent)+len(m.normal))
	out = append(out, m.urgent...)
	out = append(out, m.normal...)
	return out
}

func (m *taskMailbox) drain() []string {
	pending := m.peek()
	out := make([]string, len(pending))
	for index := range pending {
		out[index] = pending[index].Message
	}
	return out
}

// handleTaskSteer queues a steering message for a running task; the agent loop
// drains it at the next turn boundary and folds it into the transcript, so you
// can redirect a running (background) agent without restarting it. An
// "urgent" priority jumps ahead of any queued normal-priority messages for
// the same task without discarding or reordering either tier internally.
func (d *Daemon) handleTaskSteer(params json.RawMessage) (any, error) {
	var p struct {
		RunID    string `json:"run_id"`
		Message  string `json:"message"`
		Priority string `json:"priority"`
		SteerID  string `json:"steer_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	p.RunID = strings.TrimSpace(p.RunID)
	p.Message = strings.TrimSpace(p.Message)
	if p.RunID == "" || p.Message == "" {
		return nil, fmt.Errorf("run_id and message are required")
	}
	priority, err := parseSteerPriority(p.Priority)
	if err != nil {
		return nil, err
	}
	task, ok := d.sched.Get(p.RunID)
	if !ok {
		return nil, fmt.Errorf("unknown execution %s", p.RunID)
	}
	if !acceptsExecutionControl(task.Status) {
		return nil, fmt.Errorf("execution %s is %s and cannot be steered", p.RunID, task.Status)
	}
	p.SteerID = strings.TrimSpace(p.SteerID)
	if p.SteerID == "" {
		p.SteerID = sessionstore.NewID("steer")
	} else if !validClientSubmissionID(p.SteerID) {
		return nil, fmt.Errorf("steer_id must be a 1-128 byte ASCII token using letters, digits, '.', '_', ':', or '-'")
	}
	depth, err := d.enqueueSteer(p.RunID, p.SteerID, p.Message, priority)
	if err != nil {
		return nil, err
	}
	d.record(task.SessionID, "ExecutionProgressed", task.RunID, "user", map[string]any{
		"status": "steered", "message": p.Message, "steer_id": p.SteerID, "queue_depth": depth,
	}, "")
	return map[string]any{"queued": true, "run_id": p.RunID, "status": task.Status, "priority": string(priority), "steer_id": p.SteerID, "queue_depth": depth, "safe_point": "next_turn_boundary"}, nil
}

// handleExecutionQueueList returns truncated operator previews of pending steers.
// Full bodies remain off list/status projections (see docs/SOFT_INTERRUPT.md).
func (d *Daemon) handleExecutionQueueList(params json.RawMessage) (any, error) {
	var p struct {
		RunID        string `json:"run_id"`
		PreviewCells int    `json:"preview_cells"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	p.RunID = strings.TrimSpace(p.RunID)
	if p.RunID == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	if _, ok := d.sched.Get(p.RunID); !ok {
		return nil, fmt.Errorf("unknown execution %s", p.RunID)
	}
	items := d.listQueuedSteers(p.RunID, p.PreviewCells)
	return map[string]any{
		"run_id":                 p.RunID,
		"queue_depth":            len(items),
		"items":                  items,
		"soft_interrupt_pending": d.softInterruptRequested(p.RunID),
	}, nil
}

// handleExecutionQueueDrop removes one pending steer by stable steer_id.
func (d *Daemon) handleExecutionQueueDrop(params json.RawMessage) (any, error) {
	var p struct {
		RunID   string `json:"run_id"`
		SteerID string `json:"steer_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	p.RunID = strings.TrimSpace(p.RunID)
	p.SteerID = strings.TrimSpace(p.SteerID)
	if p.RunID == "" || p.SteerID == "" {
		return nil, fmt.Errorf("run_id and steer_id are required")
	}
	dropped, depth, err := d.dropQueuedSteer(p.RunID, p.SteerID)
	if err != nil {
		return nil, err
	}
	if dropped {
		if task, ok := d.sched.Get(p.RunID); ok {
			d.record(task.SessionID, "ExecutionProgressed", task.RunID, "user", map[string]any{
				"status": "steer_dropped", "steer_id": p.SteerID, "queue_depth": depth,
			}, "")
		}
	}
	return map[string]any{
		"run_id": p.RunID, "steer_id": p.SteerID, "dropped": dropped, "queue_depth": depth,
	}, nil
}

// steer queues a normal-priority steering message. Kept for existing call
// sites that do not need to express priority.
func (d *Daemon) steer(taskID, message string) error {
	return d.steerWithPriority(taskID, message, steerNormal)
}

// steerWithPriority queues a steering message into the given tier.
func (d *Daemon) steerWithPriority(taskID, message string, priority steerPriority) error {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	_, err := d.enqueueSteer(taskID, sessionstore.NewID("steer"), strings.TrimSpace(message), priority)
	return err
}

// drainMailbox returns and clears a task's pending steering messages,
// urgent-tier messages first.
func (d *Daemon) drainMailbox(taskID string) []string {
	pending := d.peekMailbox(taskID)
	messages := make([]string, len(pending))
	for index := range pending {
		messages[index] = pending[index].Message
	}
	_ = d.acknowledgeMailbox(taskID, pending)
	return messages
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
	out := make([]*scheduler.ExecutionRun, 0, len(all))
	for _, t := range all {
		if p.SessionID != "" && t.SessionID != p.SessionID {
			continue
		}
		if p.Status != "" && t.Status != p.Status {
			continue
		}
		out = append(out, t)
	}
	projected := make([]map[string]any, 0, len(out))
	for _, task := range out {
		projected = append(projected, d.taskWithControl(task, task.RunID))
	}
	return projected, nil
}

// handleTaskResult returns one run record: status, summary, and applied patches.
func (d *Daemon) handleTaskResult(params json.RawMessage) (any, error) {
	var p struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	t, ok := d.sched.Get(p.RunID)
	if !ok {
		return nil, fmt.Errorf("unknown execution %s", p.RunID)
	}
	return d.taskWithControl(t, t.RunID), nil
}

// persistRun snapshots a task's current record to the durable run store.
func (d *Daemon) persistRun(taskID string) {
	if t, ok := d.sched.Get(taskID); ok {
		d.runs.save(t)
		if terminalHookTaskStatus(t.Status) {
			go d.reconcileGoalTask(t)
		}
		if terminalHookTaskStatus(t.Status) {
			if _, loaded := d.hookStops.LoadOrStore(taskID, true); !loaded {
				if sess, exists := d.store.Get(t.SessionID); exists {
					d.runLifecycleHooks(sess.WorkspaceRoot, "Stop", map[string]any{"session_id": t.SessionID, "task_id": taskID, "status": t.Status})
				}
			}
		}
	}
}

func (d *Daemon) persistTask(taskID string) {
	if task, ok := d.sched.GetTask(taskID); ok {
		_ = d.runs.saveTask(task)
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

// lastReadHash returns the sha256 (hex) this session last recorded for path
// via recordRead, and whether any read was ever recorded at all. Used to
// transfer real read-provenance between sessions (see bestofn.go) instead of
// re-stamping current disk content, which would make drift undetectable.
func (d *Daemon) lastReadHash(sessionID, path string) (string, bool) {
	d.readProvMu.Lock()
	defer d.readProvMu.Unlock()
	m := d.readProv[sessionID]
	if m == nil {
		return "", false
	}
	h, ok := m[path]
	return h, ok
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
func (d *Daemon) guardRun(ctx context.Context, sess *sessionstore.Session, task *scheduler.ExecutionRun, run func()) {
	select {
	case d.runSem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-d.runSem }()
	defer func() {
		if r := recover(); r != nil {
			_, _ = d.sched.SetTerminalResultFenced(task.RunID, task.Continuity.Execution.LeaseGeneration, "failed", fmt.Sprintf("panic: %v", r), nil)
			d.record(sess.SessionID, "ExecutionProgressed", task.RunID, "go",
				map[string]any{"status": "failed", "reason": "panic recovered"}, "")
			d.persistRun(task.RunID)
		}
	}()
	run()
	d.persistRun(task.RunID)
}

func (d *Daemon) runTaskGuarded(sess *sessionstore.Session, task *scheduler.ExecutionRun) {
	if task.Continuity.Execution.LeaseGeneration == 0 {
		current, ok := d.sched.Get(task.RunID)
		if !ok {
			return
		}
		claimed, err := d.sched.AcquireExecution(task.RunID, current.Revision, "local", d.runtimeLease.state.InstanceID, d.runtimeLease.state.Epoch, time.Time{})
		if err != nil {
			return
		}
		task = claimed
		d.persistRun(task.RunID)
	}
	fence := d.sessionExecutionFence(sess.SessionID)
	fence.RLock()
	defer fence.RUnlock()
	d.withTaskContext(task.RunID, func(ctx context.Context) {
		d.guardRun(ctx, sess, task, func() { d.runTaskContext(ctx, sess, task) })
	})
	_ = d.cleanupTerminalExecutionControl(task.RunID)
	if current, ok := d.sched.Get(task.RunID); ok && current.Status == "cancelled" {
		d.emitCompletion(sess.SessionID, current)
	}
}

func (d *Daemon) withTaskContext(taskID string, run func(context.Context)) {
	d.withTaskParentContext(context.Background(), taskID, run)
}

func (d *Daemon) withTaskParentContext(parent context.Context, taskID string, run func(context.Context)) {
	ctx, cancel := context.WithCancelCause(parent)
	d.taskContextMu.Lock()
	d.taskContexts[taskID], d.taskCancels[taskID] = ctx, cancel
	d.taskContextMu.Unlock()
	defer func() {
		d.taskContextMu.Lock()
		delete(d.taskContexts, taskID)
		delete(d.taskCancels, taskID)
		d.taskContextMu.Unlock()
	}()
	run(ctx)
}

func (d *Daemon) resumeTaskGuarded(sess *sessionstore.Session, task *scheduler.ExecutionRun, cp *runCheckpoint) {
	fence := d.sessionExecutionFence(sess.SessionID)
	fence.RLock()
	defer fence.RUnlock()
	d.withTaskContext(task.RunID, func(ctx context.Context) {
		d.guardRun(ctx, sess, task, func() { d.resumeTaskContext(ctx, sess, task, cp) })
	})
	_ = d.cleanupTerminalExecutionControl(task.RunID)
	if current, ok := d.sched.Get(task.RunID); ok && current.Status == "cancelled" {
		d.emitCompletion(sess.SessionID, current)
	}
}

// resumeRuns reconciles abandoned execution. It resumes only when every
// independently persisted proof passes; all legacy or ambiguous rows remain
// quiescent and explain why operator review is required.
func (d *Daemon) resumeRuns() {
	for _, task := range d.sched.List() {
		switch task.Status {
		case "running":
			d.reconcileInterruptedTask(task)
		case "interrupted":
			d.startPlannedRecovery(task)
		}
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
	// the RPC surface the TUI's approval overlay calls (governance.action.approve)
	// — it must resolve the same wait governance.approval.resolve does, or the
	// operator's verdict is recorded as allowed while the gated action still
	// times out to denied.
	d.signalPendingApproval(p.DecisionID, decision, decision.Decision == "allowed", actualScope)
	// A role-rejected approval does not execute the pending command.
	if decision.Decision != "allowed" {
		d.mu.Lock()
		pendingProjection, projectionOK := d.pendingMemProjections[p.DecisionID]
		delete(d.pendingMemProjections, p.DecisionID)
		delete(d.pendingMemControls, p.DecisionID)
		d.mu.Unlock()
		if projectionOK && d.memoryProjection != nil {
			if intent, exists := d.memoryProjection.Get(pendingProjection.documentID, pendingProjection.generation); exists {
				_ = d.memoryProjection.SetBlockedReason(intent.DocumentID, intent.Generation, "authorization_denied")
				d.recordMemoryProjection(intent, projectionBlocked, "authorization_denied", p.DecisionID)
			}
		}
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
	pendingProjection, projectionOK := d.pendingMemProjections[p.DecisionID]
	delete(d.pendingMemProjections, p.DecisionID)
	d.mu.Unlock()
	if projectionOK && d.memoryProjection != nil {
		intent, exists := d.memoryProjection.Get(pendingProjection.documentID, pendingProjection.generation)
		if !exists {
			return nil, fmt.Errorf("memory projection generation is stale")
		}
		sess, exists := d.store.Get(pendingProjection.sessionID)
		if !exists {
			return nil, fmt.Errorf("unknown session %s", pendingProjection.sessionID)
		}
		var projection *memoryProjectionWriteResult
		if pendingProjection.stage == projectionApprovalNetwork {
			if err := d.memoryProjection.SetNetworkDecision(intent.DocumentID, intent.Generation, p.DecisionID); err != nil {
				return nil, err
			}
			intent.NetworkDecisionID = p.DecisionID
			projection = d.authorizeMemoryProjectionAfterNetwork(sess, intent, "")
		} else {
			if err := d.memoryProjection.Authorize(intent.DocumentID, intent.Generation, p.DecisionID); err != nil {
				return nil, err
			}
			d.recordMemoryProjection(intent, projectionPending, "", p.DecisionID)
			projection = &memoryProjectionWriteResult{Enabled: true, Status: projectionPending, DocumentID: intent.DocumentID, Revision: intent.Revision, DecisionID: p.DecisionID, Decision: "allowed"}
		}
		return response(projection), nil
	}
	d.mu.Lock()
	memPending, ok := d.pendingMemWrites[p.DecisionID]
	delete(d.pendingMemWrites, p.DecisionID)
	if pending, ok := d.pendingMemProjections[p.DecisionID]; ok {
		if intent, exists := d.memoryProjection.Get(pending.documentID, pending.generation); exists {
			_ = d.memoryProjection.SetBlockedReason(intent.DocumentID, intent.Generation, "authorization_denied")
			d.recordMemoryProjection(intent, projectionBlocked, "authorization_denied", p.DecisionID)
		}
		delete(d.pendingMemProjections, p.DecisionID)
	}
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
	d.mu.Lock()
	controlPending, controlOK := d.pendingMemControls[p.DecisionID]
	delete(d.pendingMemControls, p.DecisionID)
	d.mu.Unlock()
	if controlOK {
		result, err := d.resumePendingMemoryControl(controlPending, decision)
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
	delete(d.pendingMemControls, p.DecisionID)
	pendingProjection, projectionOK := d.pendingMemProjections[p.DecisionID]
	delete(d.pendingMemProjections, p.DecisionID)
	// A denied patch gate refuses every later apply of that patch.
	if patchID, ok := d.patchGateByDecision[p.DecisionID]; ok {
		if gate := d.patchGates[patchID]; gate != nil && gate.status == "requires_approval" {
			gate.status = "denied"
		}
	}
	d.mu.Unlock()
	if projectionOK && d.memoryProjection != nil {
		if intent, exists := d.memoryProjection.Get(pendingProjection.documentID, pendingProjection.generation); exists {
			_ = d.memoryProjection.SetBlockedReason(intent.DocumentID, intent.Generation, "authorization_denied")
			d.recordMemoryProjection(intent, projectionBlocked, "authorization_denied", p.DecisionID)
		}
	}
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
	abs, err := resolveWorkspacePreviewPath(sess.WorkspaceRoot, p.Path)
	if err != nil {
		return nil, err
	}
	decision, err := d.kern.Request(sess.SessionID, "FileRead", abs, "")
	if err != nil {
		return nil, err
	}
	if decision.Decision != "allowed" {
		return nil, fmt.Errorf("denied: %s", decision.Reason)
	}
	content, err := readWorkspacePreview(abs)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(content)
	d.record(sess.SessionID, "FileRead", "", "go",
		map[string]any{"path": abs, "bytes": len(content)}, decision.DecisionID)
	return map[string]any{"content": string(content), "hash": hex.EncodeToString(sum[:])}, nil
}

func resolveWorkspacePreviewPath(root, relative string) (string, error) {
	if strings.TrimSpace(relative) != relative || relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return "", fmt.Errorf("workspace file path must be clean and relative")
	}
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		if component == "" || component == "." || component == ".." {
			return "", fmt.Errorf("workspace file path must be clean and relative")
		}
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	targetReal, err := filepath.EvalSymlinks(filepath.Join(rootReal, relative))
	if err != nil {
		return "", fmt.Errorf("resolve workspace file: %w", err)
	}
	rel, err := filepath.Rel(rootReal, targetReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace file escapes the active workspace")
	}
	return targetReal, nil
}

func readWorkspacePreview(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxWorkspaceFilePreviewBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxWorkspaceFilePreviewBytes {
		return nil, fmt.Errorf("workspace file exceeds %d byte preview limit", maxWorkspaceFilePreviewBytes)
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return nil, fmt.Errorf("binary workspace files cannot be previewed")
	}
	return content, nil
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
	d.publishKernelPatchEvents(patch)
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
	fence := d.sessionExecutionFence(p.SessionID)
	fence.RLock()
	defer fence.RUnlock()
	if err := d.checkPatchGate(p.SessionID, p.PatchID); err != nil {
		return nil, err
	}
	approvalID, err := d.patchGateApprovalID(p.SessionID, p.PatchID)
	if err != nil {
		return nil, err
	}
	patch, err := d.kern.PatchApplyAttributed(p.SessionID, p.PatchID, p.Approver, approvalID)
	if err != nil {
		return nil, err
	}
	d.publishKernelPatchEvents(patch)
	return patch, nil
}

func (d *Daemon) patchGateApprovalID(sessionID, patchID string) (string, error) {
	d.mu.Lock()
	gate := d.patchGates[patchID]
	if gate != nil && gate.sessionID == sessionID && gate.decisionID != "" {
		decisionID := gate.decisionID
		d.mu.Unlock()
		return decisionID, nil
	}
	d.mu.Unlock()
	d.recordPatchRefusal(sessionID, patchID, "", "approval_attribution_missing")
	return "", fmt.Errorf("approval_attribution_missing: patch %s was not applied because its approved gate is no longer available", patchID)
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
		return fmt.Errorf("approval_required: patch %s was not applied. decision %s is awaiting approval; resolve it with governance.action.approve or governance.action.deny.", patchID, decisionID)
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
	fence := d.sessionExecutionFence(p.SessionID)
	fence.RLock()
	defer fence.RUnlock()
	patch, err := d.kern.PatchRollback(p.SessionID, p.PatchID)
	if err != nil {
		return nil, err
	}
	d.publishKernelPatchEvents(patch)
	// Keep the code index in step with the restore (best-effort; an index
	// error never fails the rollback).
	d.invalidateIndex(p.SessionID, patch.AffectedFiles)
	return patch, nil
}

func (d *Daemon) handlePatchRollbackPreview(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		PatchID   string `json:"patch_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	return d.kern.PatchRollbackPreview(p.SessionID, p.PatchID)
}

func (d *Daemon) handlePatchVerify(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		PatchID   string `json:"patch_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	patch, err := d.kern.PatchVerify(p.SessionID, p.PatchID)
	if err != nil {
		return nil, err
	}
	d.publishKernelPatchEvents(patch)
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
	fence := d.sessionExecutionFence(sessionID)
	fence.RLock()
	defer fence.RUnlock()
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
	if d.safeMode {
		return nil, fmt.Errorf("safe_mode: plugins are disabled; restart without --safe-mode after reviewing configuration")
	}
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
	var p struct {
		SessionID string `json:"session_id"`
		Since     int    `json:"since"`
		EventMode string `json:"event_mode"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	if p.SessionID == "" {
		return fmt.Errorf("session_id required")
	}
	mode, err := parseEventMode(p.EventMode)
	if err != nil {
		return err
	}
	if p.Since < 0 {
		p.Since = 0
	}
	baselineRaw, err := d.kern.ReadEvents(p.SessionID)
	if err != nil {
		return err
	}
	var baseline []json.RawMessage
	if err := json.Unmarshal(baselineRaw, &baseline); err != nil {
		return fmt.Errorf("event stream baseline: %w", err)
	}
	projectedSub := projectingSubscriber{eventSubscriber: sub, mode: mode}
	id, cursor, replayed, err := d.events.SubscribeCatchUp(p.SessionID, projectedSub, func() ([]any, int, map[string]int, error) {
		raw, readErr := d.kern.ReadEvents(p.SessionID)
		if readErr != nil {
			return nil, 0, nil, readErr
		}
		var all []json.RawMessage
		if decodeErr := json.Unmarshal(raw, &all); decodeErr != nil {
			return nil, 0, nil, decodeErr
		}
		since := p.Since
		if since > len(all) {
			since = len(all)
		}
		deliver := make([]any, 0, len(all)-since)
		for index, event := range all[since:] {
			if projected, ok := projectEvent(mode, event, since+index+1); ok {
				deliver = append(deliver, projected)
			}
		}
		overlap := make(map[string]int)
		start := len(baseline)
		if start > len(all) {
			start = len(all)
		}
		for _, event := range all[start:] {
			overlap[eventKey(event)]++
		}
		return deliver, len(all), overlap, nil
	})
	if err != nil {
		return err
	}
	sub.SetResult(map[string]any{"subscription_id": id, "cursor": cursor, "replayed": replayed, "event_mode": mode})
	return nil
}

func (d *Daemon) handleEventUnsubscribe(params json.RawMessage) (any, error) {
	var p struct {
		SubscriptionID string `json:"subscription_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.SubscriptionID == "" {
		return nil, fmt.Errorf("subscription_id required")
	}
	return map[string]any{"unsubscribed": d.events.Unsubscribe(p.SubscriptionID)}, nil
}

// record appends an event through the kernel (single audit writer) and
// fans it out to live subscribers. actor tags the language layer that
// produced the effect (go/rust/zig/model/user) so the audit trail shows
// the Go → Rust → Zig control flow (PRD §4.1).
func (d *Daemon) record(sessionID, eventType, taskID, actor string, payload map[string]any, decisionID string) {
	_ = d.recordChecked(sessionID, eventType, taskID, actor, payload, decisionID)
}
func (d *Daemon) recordChecked(sessionID, eventType, taskID, actor string, payload map[string]any, decisionID string) error {
	receipt, err := d.kern.RecordEventWithCursor(sessionID, eventType, taskID, actor, payload, decisionID)
	if err != nil {
		return err
	}
	d.events.Publish(sessionID, map[string]any{
		"event_id":             receipt.EventID,
		"session_id":           sessionID,
		"task_id":              taskID,
		"type":                 eventType,
		"actor":                actor,
		"timestamp":            time.Now().UTC().Format(time.RFC3339),
		"payload":              payload,
		internalRawAuditCursor: receipt.Cursor,
	})
	if d.journey != nil {
		d.journey.observeEvent(eventType, taskID)
	}
	return nil
}

// publishKernelPatchEvents fans out events that the kernel already appended
// while executing patch RPCs. It must never call recordChecked: doing so would
// duplicate the durable audit event. Private transport metadata is cleared
// before the Patch can cross a public daemon RPC boundary.
func (d *Daemon) publishKernelPatchEvents(patch *kernel.Patch) {
	if patch == nil {
		return
	}
	events := patch.AuditEvents
	patch.AuditEvents = nil
	for _, persisted := range events {
		if persisted.Cursor <= 0 || persisted.Event == nil {
			continue
		}
		sessionID, _ := persisted.Event["session_id"].(string)
		if sessionID == "" || sessionID != patch.SessionID {
			continue
		}
		persisted.Event[internalRawAuditCursor] = persisted.Cursor
		d.events.Publish(sessionID, persisted.Event)
		if d.journey != nil {
			eventType, _ := persisted.Event["type"].(string)
			taskID, _ := persisted.Event["task_id"].(string)
			if taskID == "" {
				taskID = patch.TaskID
			}
			d.journey.observeEvent(eventType, taskID)
		}
	}
}

// ---- workers ----------------------------------------------------------------

// maxWorkerRegisterPools/maxWorkerPoolTagLength/validWorkerPoolTag bound and
// sanitize the "worker_pool:<tag>" capability tags a registering worker may
// self-declare — this RPC boundary is the authoritative validation point
// (go/worker.Pool.RegisterAuthenticatedWithPools trusts its caller); a
// malformed or oversized tag here would otherwise flow straight into the
// scheduler's capability-matching namespace.
const (
	maxWorkerRegisterPools = 8
	maxWorkerPoolTagLength = 64
)

func validWorkerPoolTag(tag string) bool {
	if tag == "" || len(tag) > maxWorkerPoolTagLength {
		return false
	}
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func (d *Daemon) handleWorkerRegister(params json.RawMessage) (any, error) {
	var p struct {
		Name                   string                        `json:"name"`
		Kind                   string                        `json:"kind"`
		ProcessTreeContainment worker.ProcessTreeContainment `json:"process_tree_containment"`
		Pools                  []string                      `json:"pools,omitempty"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Kind == "" {
		p.Kind = "remote"
	}
	if p.ProcessTreeContainment == "" {
		p.ProcessTreeContainment = worker.ContainmentNone
	}
	kind := worker.Kind(p.Kind)
	switch kind {
	case worker.Remote, worker.CI, worker.Sandbox:
	default:
		return nil, fmt.Errorf("unsupported worker kind %q", p.Kind)
	}
	if len(p.Pools) > maxWorkerRegisterPools {
		return nil, fmt.Errorf("at most %d pool tags may be declared", maxWorkerRegisterPools)
	}
	for _, tag := range p.Pools {
		if !validWorkerPoolTag(tag) {
			return nil, fmt.Errorf("invalid pool tag %q: must be 1-%d lowercase letters, digits, dashes, or underscores", tag, maxWorkerPoolTagLength)
		}
	}
	w, credential, err := d.pool.RegisterAuthenticatedWithPools(strings.TrimSpace(p.Name), kind, p.ProcessTreeContainment, p.Pools)
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
