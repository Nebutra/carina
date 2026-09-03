package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/netguard"
	"github.com/chromedp/cdproto/accessibility"
	cdbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	cdruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/go-json-experiment/json/jsontext"
)

const (
	defaultRawSnapshotNodes = 2_000
	defaultRawSnapshotDepth = 32
	defaultDownloadBytes    = 8 << 20
	defaultScreenshotBytes  = 8 << 20
	maxCaptureWidth         = 4_096
	maxCaptureHeight        = 16_384
)

type ChromedpConfig struct {
	ChromePath          string
	LauncherPath        string
	MaxRawSnapshotNodes int
	MaxRawSnapshotDepth int
	MaxDownloadBytes    int
	MaxScreenshotBytes  int
	DownloadStartWait   time.Duration
}

type ChromedpDriver struct {
	config ChromedpConfig
}

func NewChromedpDriver(config ChromedpConfig) *ChromedpDriver {
	if config.MaxRawSnapshotNodes <= 0 {
		config.MaxRawSnapshotNodes = defaultRawSnapshotNodes
	}
	if config.MaxRawSnapshotDepth <= 0 {
		config.MaxRawSnapshotDepth = defaultRawSnapshotDepth
	}
	if config.MaxDownloadBytes <= 0 {
		config.MaxDownloadBytes = defaultDownloadBytes
	}
	if config.MaxScreenshotBytes <= 0 {
		config.MaxScreenshotBytes = defaultScreenshotBytes
	}
	if config.DownloadStartWait <= 0 {
		config.DownloadStartWait = 350 * time.Millisecond
	}
	return &ChromedpDriver{config: config}
}

type chromedpSession struct {
	mu sync.Mutex

	mode             Mode
	browserCtx       context.Context
	browserCancel    context.CancelFunc
	allocatorCancel  context.CancelFunc
	proxy            *browserProxy
	quarantineDir    string
	removeQuarantine bool
	closed           bool
	activeTab        string
	tabs             map[string]*chromedpTab
	allowedOrigins   map[string]struct{}
	blockedSequence  uint64
	downloads        map[string]*pendingDownload
	downloadStarted  chan string
	config           ChromedpConfig
}

type chromedpTab struct {
	ctx    context.Context
	cancel context.CancelFunc

	dialog       *Dialog
	dialogOpened chan struct{}
	pending      *pendingTabAction
	navSequence  uint64
}

type pendingTabAction struct {
	done   <-chan error
	cancel context.CancelFunc
}

type pendingDownload struct {
	suggested string
	path      string
	received  int64
	state     cdbrowser.DownloadProgressState
	done      chan struct{}
	doneOnce  sync.Once
}

// Chromium adds accessibility enum values independently of cdproto releases.
// Decode the small consumed projection with strings so unknown metadata stays
// forward compatible instead of failing the whole snapshot.
type permissiveAXNode struct {
	NodeID           string                  `json:"nodeId"`
	Ignored          bool                    `json:"ignored"`
	Role             *permissiveAXValue      `json:"role,omitempty"`
	Name             *permissiveAXValue      `json:"name,omitempty"`
	Description      *permissiveAXValue      `json:"description,omitempty"`
	Value            *permissiveAXValue      `json:"value,omitempty"`
	Properties       []*permissiveAXProperty `json:"properties,omitempty"`
	ParentID         string                  `json:"parentId,omitempty"`
	BackendDOMNodeID cdp.BackendNodeID       `json:"backendDOMNodeId,omitempty"`
}

type permissiveAXValue struct {
	Value jsontext.Value `json:"value,omitempty"`
}

type permissiveAXProperty struct {
	Name  string             `json:"name"`
	Value *permissiveAXValue `json:"value"`
}

func (d *ChromedpDriver) Launch(ctx context.Context, opts LaunchOptions) (DriverSession, error) {
	if !validBrowserID(opts.BrowserID) || !filepath.IsAbs(opts.ProfileDir) {
		return nil, browserError(ErrorInvalidRequest, "invalid managed browser launch options", "reopen the browser session", false, nil)
	}
	chromePath, err := discoverChrome(d.config.ChromePath)
	if err != nil {
		return nil, err
	}
	launcherPath := strings.TrimSpace(d.config.LauncherPath)
	if launcherPath == "" {
		launcherPath, err = os.Executable()
		if err != nil {
			return nil, browserError(ErrorUnavailable, "browser launcher is unavailable", "reinstall Carina and retry", false, err)
		}
	}
	launcherPath, err = validateChromeExecutable(launcherPath)
	if err != nil {
		return nil, browserError(ErrorUnavailable, "browser launcher is unavailable", "reinstall Carina and retry", false, err)
	}
	proxy := newBrowserProxy()
	proxyURL, err := proxy.Start()
	if err != nil {
		return nil, browserError(ErrorUnavailable, "browser network boundary could not start", "retry after checking local networking", true, err)
	}

	allocatorOptions := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	allocatorOptions = append(allocatorOptions,
		chromedp.ExecPath(launcherPath),
		chromedp.UserDataDir(opts.ProfileDir),
		chromedp.ProxyServer(proxyURL),
		chromedp.Flag("proxy-bypass-list", "<-loopback>"),
		chromedp.Flag("remote-debugging-address", "127.0.0.1"),
		chromedp.Flag("no-sandbox", false),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-component-extensions-with-background-pages", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-breakpad", true),
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("safebrowsing-disable-auto-update", true),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("use-mock-keychain", true),
		chromedp.Flag("disable-features", "AutofillServerCommunication,MediaRouter,OptimizationHints,PasswordManagerOnboarding,Translate"),
		chromedp.Flag(strings.TrimPrefix(chromeLauncherFlag, "--"), true),
		chromedp.Flag(strings.TrimSuffix(strings.TrimPrefix(chromePathFlag, "--"), "="), chromePath),
		chromedp.Flag(strings.TrimSuffix(strings.TrimPrefix(chromeBrowserIDFlag, "--"), "="), opts.BrowserID),
	)
	allocatorCtx, allocatorCancel := chromedp.NewExecAllocator(context.Background(), allocatorOptions...)
	browserCtx, browserCancel := chromedp.NewContext(allocatorCtx)
	session := newChromedpSession(ModeManaged, browserCtx, browserCancel, allocatorCancel, proxy, filepath.Join(opts.ProfileDir, "downloads"), false, d.config)
	if err := session.initialize(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

func (d *ChromedpDriver) Attach(ctx context.Context, opts AttachOptions) (DriverSession, error) {
	if !validBrowserID(opts.BrowserID) {
		return nil, browserError(ErrorInvalidRequest, "invalid attached browser identity", "reopen the browser session", false, nil)
	}
	if err := validateAttachEndpoint(opts.Endpoint); err != nil {
		return nil, err
	}
	quarantine, err := os.MkdirTemp("", "carina-browser-downloads-")
	if err != nil {
		return nil, browserError(ErrorUnavailable, "browser download quarantine is unavailable", "check temporary storage and retry", true, err)
	}
	if err := os.Chmod(quarantine, 0o700); err != nil {
		_ = os.RemoveAll(quarantine)
		return nil, browserError(ErrorUnavailable, "browser download quarantine is unavailable", "check temporary storage and retry", true, err)
	}
	allocatorCtx, allocatorCancel := chromedp.NewRemoteAllocator(context.Background(), opts.Endpoint)
	browserCtx, browserCancel := chromedp.NewContext(allocatorCtx)
	session := newChromedpSession(ModeAttach, browserCtx, browserCancel, allocatorCancel, nil, quarantine, true, d.config)
	if err := session.initialize(ctx); err != nil {
		_ = session.Close()
		return nil, browserError(ErrorUnavailable, "could not attach to the approved browser", "verify the operator-supplied debugging endpoint and retry", true, err)
	}
	return session, nil
}

func newChromedpSession(mode Mode, browserCtx context.Context, browserCancel, allocatorCancel context.CancelFunc, proxy *browserProxy, quarantine string, removeQuarantine bool, config ChromedpConfig) *chromedpSession {
	return &chromedpSession{
		mode: mode, browserCtx: browserCtx, browserCancel: browserCancel, allocatorCancel: allocatorCancel,
		proxy: proxy, quarantineDir: quarantine, removeQuarantine: removeQuarantine,
		tabs: make(map[string]*chromedpTab), allowedOrigins: make(map[string]struct{}),
		downloads: make(map[string]*pendingDownload), downloadStarted: make(chan string, 16), config: config,
	}
}

func (s *chromedpSession) initialize(requestCtx context.Context) error {
	if err := os.MkdirAll(s.quarantineDir, 0o700); err != nil {
		return browserError(ErrorUnavailable, "browser download quarantine is unavailable", "check local storage and retry", true, err)
	}
	chromedp.ListenBrowser(s.browserCtx, s.onBrowserEvent)
	if err := allocateChromedp(requestCtx, s.browserCtx, s.browserCancel); err != nil {
		return err
	}
	ctxState := chromedp.FromContext(s.browserCtx)
	if ctxState == nil || ctxState.Target == nil || ctxState.Target.TargetID == "" {
		return browserError(ErrorDriver, "browser did not create an isolated page", "reopen the browser", true, nil)
	}
	tabID := string(ctxState.Target.TargetID)
	s.mu.Lock()
	s.tabs[tabID] = newChromedpTab(s.browserCtx, nil)
	s.activeTab = tabID
	s.mu.Unlock()
	if err := s.configureTab(requestCtx, tabID); err != nil {
		return err
	}
	if err := runChromedp(requestCtx, s.browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdbrowser.SetDownloadBehavior(cdbrowser.SetDownloadBehaviorBehaviorAllowAndName).
			WithDownloadPath(s.quarantineDir).WithEventsEnabled(true).Do(ctx)
	})); err != nil {
		return err
	}
	return nil
}

func (s *chromedpSession) configureTab(requestCtx context.Context, tabID string) error {
	s.mu.Lock()
	tab := s.tabs[tabID]
	s.mu.Unlock()
	if tab == nil {
		return browserError(ErrorNotFound, "browser tab was not found", "refresh browser.tabs", false, nil)
	}
	chromedp.ListenTarget(tab.ctx, func(event any) {
		s.onTargetEvent(tabID, event)
	})
	patterns := []*fetch.RequestPattern{
		{URLPattern: "*", RequestStage: fetch.RequestStageRequest},
		{URLPattern: "*", RequestStage: fetch.RequestStageResponse},
	}
	return runChromedp(requestCtx, tab.ctx,
		chromedp.ActionFunc(func(ctx context.Context) error { return page.Enable().Do(ctx) }),
		chromedp.ActionFunc(func(ctx context.Context) error { return dom.Enable().Do(ctx) }),
		chromedp.ActionFunc(func(ctx context.Context) error { return accessibility.Enable().Do(ctx) }),
		chromedp.ActionFunc(func(ctx context.Context) error { return fetch.Enable().WithPatterns(patterns).Do(ctx) }),
	)
}

func (s *chromedpSession) AllowOrigins(_ context.Context, origins []string) error {
	validated := make([]string, 0, len(origins))
	for _, raw := range origins {
		origin, err := netguard.NormalizePublicHTTPSOrigin(raw)
		if err != nil {
			return browserError(ErrorInvalidRequest, "approved browser origin is invalid", "approve an exact public HTTPS origin", false, err)
		}
		validated = append(validated, origin)
	}
	if s.proxy != nil {
		if err := s.proxy.AllowOrigins(validated); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return browserError(ErrorNotFound, "browser is closed", "open a new browser", false, nil)
	}
	for _, origin := range validated {
		s.allowedOrigins[origin] = struct{}{}
	}
	return nil
}

func (s *chromedpSession) Navigate(ctx context.Context, tabID, rawURL string) error {
	if !s.isAllowedURL(rawURL) {
		return browserError(ErrorNetworkBlocked, "browser navigation origin is not approved", "approve the exact public HTTPS origin and retry", false, nil)
	}
	tab, sequence, err := s.tab(tabID)
	if err != nil {
		return err
	}
	err = runChromedp(ctx, tab.ctx, chromedp.Navigate(rawURL))
	return s.classifyOperationError(err, sequence)
}

func (s *chromedpSession) Snapshot(ctx context.Context, tabID string) (RawSnapshot, error) {
	tab, sequence, err := s.tab(tabID)
	if err != nil {
		return RawSnapshot{}, err
	}
	var frameTree *page.FrameTree
	var title string
	var axNodes []*permissiveAXNode
	err = runChromedp(ctx, tab.ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			frameTree, err = page.GetFrameTree().Do(ctx)
			return err
		}),
		chromedp.Title(&title),
		chromedp.ActionFunc(func(ctx context.Context) error {
			params := struct {
				Depth int64 `json:"depth,omitempty"`
			}{Depth: int64(s.config.MaxRawSnapshotDepth)}
			result := struct {
				Nodes []*permissiveAXNode `json:"nodes,omitempty"`
			}{}
			if err := cdp.Execute(ctx, accessibility.CommandGetFullAXTree, &params, &result); err != nil {
				return err
			}
			axNodes = result.Nodes
			return nil
		}),
	)
	if err := s.classifyOperationError(err, sequence); err != nil {
		return RawSnapshot{}, err
	}
	if frameTree == nil || frameTree.Frame == nil {
		return RawSnapshot{}, browserError(ErrorDriver, "browser returned no document frame", "retry the snapshot", true, nil)
	}
	raw := RawSnapshot{
		TabID: tabID, DocumentID: string(frameTree.Frame.ID) + ":" + string(frameTree.Frame.LoaderID),
		URL: redactPageURL(frameTree.Frame.URL), Title: title,
		Nodes: make([]RawNode, 0, min(len(axNodes), s.config.MaxRawSnapshotNodes)),
	}
	backendByAX := make(map[string]string, len(axNodes))
	depthByAX := make(map[string]int, len(axNodes))
	for index, node := range axNodes {
		if index >= s.config.MaxRawSnapshotNodes {
			raw.Truncated = true
			raw.Omitted += len(axNodes) - index
			break
		}
		if node == nil || node.Ignored || node.BackendDOMNodeID == 0 {
			continue
		}
		backendID := strconv.FormatInt(int64(node.BackendDOMNodeID), 10)
		backendByAX[node.NodeID] = backendID
		depth := 0
		if node.ParentID != "" {
			depth = depthByAX[node.ParentID] + 1
		}
		depthByAX[node.NodeID] = depth
		role := axString(node.Role)
		name := axString(node.Name)
		value := axString(node.Value)
		description := axString(node.Description)
		if role == "" && name == "" && value == "" && description == "" {
			continue
		}
		rawNode := RawNode{
			BackendNodeID: backendID, ParentID: backendByAX[node.ParentID], Depth: depth,
			Role: role, Name: name, Value: value, Description: description,
		}
		applyAXProperties(&rawNode, node.Properties)
		if isActionableRole(role) {
			if err := s.enrichNode(ctx, tab.ctx, node.BackendDOMNodeID, &rawNode); err != nil && !errors.Is(err, context.Canceled) {
				// DOM metadata is advisory for effect raising; inaccessible nodes
				// remain actionable only under their AX-derived effect floor.
			}
		}
		if rawNode.Sensitive {
			rawNode.Value = ""
		}
		raw.Nodes = append(raw.Nodes, rawNode)
	}
	return raw, nil
}

func (s *chromedpSession) InspectNode(ctx context.Context, tabID, rawBackendID string) (NodeInspection, error) {
	tab, sequence, err := s.tab(tabID)
	if err != nil {
		return NodeInspection{}, err
	}
	backendID, err := parseBackendID(rawBackendID)
	if err != nil {
		return NodeInspection{}, err
	}
	var tree *page.FrameTree
	var nodes []*permissiveAXNode
	err = runChromedp(ctx, tab.ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			tree, err = page.GetFrameTree().Do(ctx)
			return err
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			params := struct {
				BackendNodeID  cdp.BackendNodeID `json:"backendNodeId"`
				FetchRelatives bool              `json:"fetchRelatives"`
			}{BackendNodeID: backendID, FetchRelatives: false}
			result := struct {
				Nodes []*permissiveAXNode `json:"nodes,omitempty"`
			}{}
			if err := cdp.Execute(ctx, accessibility.CommandGetPartialAXTree, &params, &result); err != nil {
				return err
			}
			nodes = result.Nodes
			return nil
		}),
	)
	if err := s.classifyOperationError(err, sequence); err != nil {
		return NodeInspection{}, err
	}
	if tree == nil || tree.Frame == nil {
		return NodeInspection{}, browserError(ErrorStaleRef, "browser document is no longer available", "take a fresh browser.snapshot", false, nil)
	}
	var node *permissiveAXNode
	for _, candidate := range nodes {
		if candidate != nil && candidate.BackendDOMNodeID == backendID && !candidate.Ignored {
			node = candidate
			break
		}
	}
	if node == nil {
		return NodeInspection{}, browserError(ErrorStaleRef, "browser node is no longer available", "take a fresh browser.snapshot", false, nil)
	}
	raw := RawNode{
		BackendNodeID: rawBackendID,
		Role:          axString(node.Role),
		Name:          axString(node.Name),
		Value:         axString(node.Value),
		Description:   axString(node.Description),
	}
	applyAXProperties(&raw, node.Properties)
	if err := s.enrichNode(ctx, tab.ctx, backendID, &raw); err != nil {
		return NodeInspection{}, err
	}
	if raw.Sensitive {
		raw.Value = ""
	}
	return NodeInspection{DocumentID: string(tree.Frame.ID) + ":" + string(tree.Frame.LoaderID), Node: raw}, nil
}

func (s *chromedpSession) ListTabs(ctx context.Context) ([]RawTab, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, browserError(ErrorNotFound, "browser is closed", "open a new browser", false, nil)
	}
	owned := make(map[string]struct{}, len(s.tabs))
	for tabID := range s.tabs {
		owned[tabID] = struct{}{}
	}
	active := s.activeTab
	s.mu.Unlock()
	var infos []*target.Info
	err := runChromedp(ctx, s.browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		infos, err = chromedp.Targets(ctx)
		return err
	}))
	if err != nil {
		return nil, err
	}
	tabs := make([]RawTab, 0, len(owned))
	for _, info := range infos {
		if info == nil || info.Type != "page" {
			continue
		}
		if _, ok := owned[string(info.TargetID)]; !ok {
			continue
		}
		tabs = append(tabs, RawTab{ID: string(info.TargetID), URL: redactPageURL(info.URL), Title: info.Title, Active: string(info.TargetID) == active})
	}
	return tabs, nil
}

func (s *chromedpSession) OpenTab(ctx context.Context, rawURL string) (RawTab, error) {
	if !s.isAllowedURL(rawURL) {
		return RawTab{}, browserError(ErrorNetworkBlocked, "browser tab origin is not approved", "approve the exact public HTTPS origin and retry", false, nil)
	}
	var targetID target.ID
	err := runBrowserCommand(ctx, s.browserCtx, func(ctx context.Context) error {
		var err error
		targetID, err = target.CreateTarget("about:blank").Do(ctx)
		return err
	})
	if err != nil {
		return RawTab{}, err
	}
	tabCtx, cancel := chromedp.NewContext(s.browserCtx, chromedp.WithTargetID(targetID))
	tabID := string(targetID)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return RawTab{}, browserError(ErrorNotFound, "browser is closed", "open a new browser", false, nil)
	}
	s.tabs[tabID] = newChromedpTab(tabCtx, cancel)
	s.activeTab = tabID
	s.mu.Unlock()
	if err := allocateChromedp(ctx, tabCtx, cancel); err != nil {
		_ = s.removeTab(tabID, true)
		return RawTab{}, err
	}
	if err := s.configureTab(ctx, tabID); err != nil {
		_ = s.removeTab(tabID, true)
		return RawTab{}, err
	}
	if err := s.Navigate(ctx, tabID, rawURL); err != nil {
		_ = s.removeTab(tabID, true)
		return RawTab{}, err
	}
	return RawTab{ID: tabID, URL: redactPageURL(rawURL), Active: true}, nil
}

func (s *chromedpSession) ActivateTab(ctx context.Context, tabID string) error {
	if _, _, err := s.tab(tabID); err != nil {
		return err
	}
	err := runBrowserCommand(ctx, s.browserCtx, func(ctx context.Context) error {
		return target.ActivateTarget(target.ID(tabID)).Do(ctx)
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.activeTab = tabID
	s.mu.Unlock()
	return nil
}

func (s *chromedpSession) CloseTab(ctx context.Context, tabID string) error {
	tab, _, err := s.tab(tabID)
	if err != nil {
		return err
	}
	if tab.cancel != nil {
		return s.removeTab(tabID, true)
	}
	err = runBrowserCommand(ctx, s.browserCtx, func(ctx context.Context) error {
		return target.CloseTarget(target.ID(tabID)).Do(ctx)
	})
	if err != nil {
		return err
	}
	return s.removeTab(tabID, false)
}

func (s *chromedpSession) Action(ctx context.Context, action DriverAction) (DriverActionResult, error) {
	tab, sequence, err := s.tab(action.TabID)
	if err != nil {
		return DriverActionResult{}, err
	}
	s.mu.Lock()
	navigationBefore := tab.navSequence
	pendingDialog := tab.dialog != nil
	s.mu.Unlock()
	if action.Kind == ActionDialogAccept || action.Kind == ActionDialogDismiss {
		return s.handleDialogAction(ctx, tab, action.Kind, sequence, navigationBefore)
	}
	if pendingDialog {
		return DriverActionResult{}, browserError(ErrorInvalidRequest, "a browser dialog is awaiting a decision", "accept or dismiss the pending dialog before another action", false, nil)
	}
	var operation chromedp.Action
	switch action.Kind {
	case ActionClick:
		operation, err = clickAction(action.BackendNodeID)
	case ActionType:
		operation, err = typeAction(action.BackendNodeID, action.Text)
	case ActionSelect:
		operation, err = selectAction(action.BackendNodeID, action.Values)
	case ActionCheck:
		operation, err = checkAction(action.BackendNodeID, action.Checked)
	case ActionKey:
		operation, err = keyAction(action.Key)
	case ActionScroll:
		operation = chromedp.ActionFunc(func(ctx context.Context) error {
			return input.DispatchMouseEvent(input.MouseWheel, 0, 0).
				WithDeltaX(float64(action.DeltaX)).WithDeltaY(float64(action.DeltaY)).Do(ctx)
		})
	case ActionHover:
		operation, err = hoverAction(action.BackendNodeID)
	case ActionUpload:
		operation, err = uploadAction(action.BackendNodeID, action.Files)
	default:
		err = actionValidationError("browser driver received unknown action %q", action.Kind)
	}
	if err != nil {
		return DriverActionResult{}, err
	}
	dialog, err := s.runActionUntilDialog(ctx, tab, operation)
	if err := s.classifyOperationError(err, sequence); err != nil {
		return DriverActionResult{}, err
	}
	result := DriverActionResult{Dialog: dialog}
	if result.Dialog != nil {
		return result, nil
	}
	s.mu.Lock()
	result.Navigated = tab.navSequence > navigationBefore
	s.mu.Unlock()
	if download, err := s.collectActionDownload(ctx); err != nil {
		return DriverActionResult{}, err
	} else {
		result.Download = download
	}
	if result.Dialog == nil {
		s.mu.Lock()
		if tab.dialog != nil {
			copy := *tab.dialog
			result.Dialog = &copy
		}
		s.mu.Unlock()
	}
	return result, nil
}

func newChromedpTab(ctx context.Context, cancel context.CancelFunc) *chromedpTab {
	return &chromedpTab{ctx: ctx, cancel: cancel, dialogOpened: make(chan struct{}, 1)}
}

func (s *chromedpSession) runActionUntilDialog(requestCtx context.Context, tab *chromedpTab, operation chromedp.Action) (*Dialog, error) {
	actionCtx, cancel := context.WithCancel(tab.ctx)
	done := make(chan error, 1)
	go func() { done <- chromedp.Run(actionCtx, operation) }()
	select {
	case err := <-done:
		cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if tab.dialog == nil {
			return nil, err
		}
		copy := *tab.dialog
		return &copy, err
	case <-tab.dialogOpened:
		s.mu.Lock()
		defer s.mu.Unlock()
		if tab.dialog == nil {
			cancel()
			return nil, browserError(ErrorDriver, "browser dialog state was lost", "retry the triggering action", true, nil)
		}
		tab.pending = &pendingTabAction{done: done, cancel: cancel}
		copy := *tab.dialog
		return &copy, nil
	case <-requestCtx.Done():
		cancel()
		<-done
		return nil, requestCtx.Err()
	}
}

func (s *chromedpSession) handleDialogAction(ctx context.Context, tab *chromedpTab, kind ActionKind, blockedBefore, navigationBefore uint64) (DriverActionResult, error) {
	s.mu.Lock()
	if tab.dialog == nil {
		s.mu.Unlock()
		return DriverActionResult{}, browserError(ErrorInvalidRequest, "there is no pending browser dialog", "take a fresh snapshot or retry the triggering action", false, nil)
	}
	pending := tab.pending
	s.mu.Unlock()
	accept := kind == ActionDialogAccept
	err := runChromedp(ctx, tab.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return page.HandleJavaScriptDialog(accept).Do(ctx)
	}))
	if err := s.classifyOperationError(err, blockedBefore); err != nil {
		return DriverActionResult{}, err
	}
	if pending != nil {
		select {
		case err := <-pending.done:
			pending.cancel()
			if err != nil {
				return DriverActionResult{}, err
			}
		case <-ctx.Done():
			pending.cancel()
			return DriverActionResult{}, ctx.Err()
		}
	}
	s.mu.Lock()
	if tab.pending == pending {
		tab.pending = nil
	}
	tab.dialog = nil
	navigated := tab.navSequence > navigationBefore
	s.mu.Unlock()
	result := DriverActionResult{Navigated: navigated}
	if download, err := s.collectActionDownload(ctx); err != nil {
		return DriverActionResult{}, err
	} else {
		result.Download = download
	}
	return result, nil
}

func (s *chromedpSession) Capture(ctx context.Context, request CaptureRequest) (CaptureResult, error) {
	tab, sequence, err := s.tab(request.TabID)
	if err != nil {
		return CaptureResult{}, err
	}
	var bytes []byte
	var width, height float64
	actions := []chromedp.Action{chromedp.ActionFunc(func(ctx context.Context) error {
		_, _, _, layout, visual, content, err := page.GetLayoutMetrics().Do(ctx)
		if err != nil {
			return err
		}
		if request.FullPage && content != nil {
			width, height = math.Min(content.Width, maxCaptureWidth), math.Min(content.Height, maxCaptureHeight)
		} else if visual != nil {
			width, height = visual.ClientWidth, visual.ClientHeight
		} else if layout != nil {
			width, height = float64(layout.ClientWidth), float64(layout.ClientHeight)
		}
		if width <= 0 || height <= 0 {
			return fmt.Errorf("invalid browser viewport")
		}
		capture := page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).WithFromSurface(true)
		if request.FullPage {
			capture = capture.WithCaptureBeyondViewport(true).WithClip(&page.Viewport{X: 0, Y: 0, Width: width, Height: height, Scale: 1})
		}
		bytes, err = capture.Do(ctx)
		return err
	})}
	if err := s.classifyOperationError(runChromedp(ctx, tab.ctx, actions...), sequence); err != nil {
		return CaptureResult{}, err
	}
	if len(bytes) == 0 || len(bytes) > s.config.MaxScreenshotBytes {
		return CaptureResult{}, browserError(ErrorResourceLimit, "browser capture exceeds the driver limit", "capture a smaller page or viewport", false, nil)
	}
	return CaptureResult{MediaType: "image/png", Bytes: bytes, Width: int(math.Ceil(width)), Height: int(math.Ceil(height))}, nil
}

func (s *chromedpSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancels := make([]context.CancelFunc, 0, len(s.tabs))
	for _, tab := range s.tabs {
		if tab.cancel != nil {
			cancels = append(cancels, tab.cancel)
		}
	}
	s.tabs = make(map[string]*chromedpTab)
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	if s.mode == ModeAttach {
		resetCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = runChromedp(resetCtx, s.browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			return cdbrowser.SetDownloadBehavior(cdbrowser.SetDownloadBehaviorBehaviorDefault).Do(ctx)
		}))
		cancel()
	}
	if s.browserCancel != nil {
		s.browserCancel()
	}
	if s.allocatorCancel != nil {
		s.allocatorCancel()
	}
	if s.proxy != nil {
		_ = s.proxy.Close()
	}
	if s.removeQuarantine {
		return os.RemoveAll(s.quarantineDir)
	}
	return nil
}

func (s *chromedpSession) onTargetEvent(tabID string, event any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tab := s.tabs[tabID]
	if tab == nil || s.closed {
		return
	}
	switch event := event.(type) {
	case *fetch.EventRequestPaused:
		allowed := event.Request != nil && s.isAllowedURLLocked(event.Request.URL)
		if !allowed {
			s.blockedSequence++
		}
		go respondToPausedRequest(tab.ctx, event.RequestID, allowed)
	case *page.EventJavascriptDialogOpening:
		tab.dialog = &Dialog{Type: string(event.Type), Message: truncateUTF8(event.Message, 4<<10)}
		select {
		case tab.dialogOpened <- struct{}{}:
		default:
		}
	case *page.EventJavascriptDialogClosed:
		tab.dialog = nil
	case *page.EventFrameNavigated:
		if event.Frame != nil && event.Frame.ParentID == "" {
			tab.navSequence++
		}
	}
}

func (s *chromedpSession) onBrowserEvent(event any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	switch event := event.(type) {
	case *cdbrowser.EventDownloadWillBegin:
		pending := &pendingDownload{suggested: event.SuggestedFilename, done: make(chan struct{})}
		s.downloads[event.GUID] = pending
		select {
		case s.downloadStarted <- event.GUID:
		default:
			pending.state = cdbrowser.DownloadProgressStateCanceled
			pending.doneOnce.Do(func() { close(pending.done) })
		}
	case *cdbrowser.EventDownloadProgress:
		pending := s.downloads[event.GUID]
		if pending == nil {
			return
		}
		pending.received = int64(event.ReceivedBytes)
		pending.path = event.FilePath
		pending.state = event.State
		if event.State == cdbrowser.DownloadProgressStateCompleted || event.State == cdbrowser.DownloadProgressStateCanceled {
			pending.doneOnce.Do(func() { close(pending.done) })
		}
	}
}

func respondToPausedRequest(tabCtx context.Context, requestID fetch.RequestID, allowed bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if allowed {
		_ = runChromedp(ctx, tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			return fetch.ContinueRequest(requestID).Do(ctx)
		}))
		return
	}
	_ = runChromedp(ctx, tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		return fetch.FailRequest(requestID, network.ErrorReasonBlockedByClient).Do(ctx)
	}))
}

func (s *chromedpSession) tab(tabID string) (*chromedpTab, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, 0, browserError(ErrorNotFound, "browser is closed", "open a new browser", false, nil)
	}
	tab := s.tabs[tabID]
	if tab == nil {
		return nil, 0, browserError(ErrorNotFound, "browser tab was not found", "refresh browser.tabs", false, nil)
	}
	return tab, s.blockedSequence, nil
}

func (s *chromedpSession) removeTab(tabID string, cancelContext bool) error {
	s.mu.Lock()
	tab := s.tabs[tabID]
	delete(s.tabs, tabID)
	if s.activeTab == tabID {
		s.activeTab = ""
		for id := range s.tabs {
			s.activeTab = id
			break
		}
	}
	s.mu.Unlock()
	if cancelContext && tab != nil && tab.cancel != nil {
		tab.cancel()
	}
	return nil
}

func (s *chromedpSession) classifyOperationError(err error, blockedBefore uint64) error {
	if err == nil {
		return nil
	}
	s.mu.Lock()
	blocked := s.blockedSequence > blockedBefore
	s.mu.Unlock()
	if blocked {
		return browserError(ErrorNetworkBlocked, "browser request origin was blocked", "approve the exact public HTTPS origin and retry", false, nil)
	}
	return err
}

func (s *chromedpSession) isAllowedURL(raw string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isAllowedURLLocked(raw)
}

func (s *chromedpSession) isAllowedURLLocked(raw string) bool {
	if raw == "about:blank" {
		return true
	}
	targetURL, err := netguard.NormalizePublicHTTPSURL(raw)
	if err != nil {
		return false
	}
	_, ok := s.allowedOrigins["https://"+targetURL.Hostname()]
	return ok
}

func (s *chromedpSession) documentID(ctx context.Context, tabCtx context.Context) string {
	var tree *page.FrameTree
	if err := runChromedp(ctx, tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		tree, err = page.GetFrameTree().Do(ctx)
		return err
	})); err != nil || tree == nil || tree.Frame == nil {
		return ""
	}
	return string(tree.Frame.ID) + ":" + string(tree.Frame.LoaderID)
}

func (s *chromedpSession) enrichNode(requestCtx, tabCtx context.Context, backendID cdp.BackendNodeID, raw *RawNode) error {
	return runChromedp(requestCtx, tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		node, err := dom.DescribeNode().WithBackendNodeID(backendID).WithDepth(0).Do(ctx)
		if err != nil {
			return err
		}
		attrs := attributes(node.Attributes)
		raw.InputType = strings.ToLower(attrs["type"])
		raw.FormMethod = strings.ToLower(attrs["formmethod"])
		raw.FormAction = attrs["formaction"]
		raw.TargetURL = firstNonempty(attrs["href"], raw.FormAction)
		raw.Disabled = raw.Disabled || hasAttribute(node.Attributes, "disabled")
		current := node
		for depth := 0; depth < 16 && current != nil; depth++ {
			if strings.EqualFold(current.LocalName, "form") {
				formAttrs := attributes(current.Attributes)
				raw.FormMethod = strings.ToLower(firstNonempty(raw.FormMethod, formAttrs["method"], "get"))
				raw.FormAction = firstNonempty(raw.FormAction, formAttrs["action"])
				break
			}
			if current.ParentID == 0 {
				break
			}
			current, err = dom.DescribeNode().WithNodeID(current.ParentID).WithDepth(0).Do(ctx)
			if err != nil {
				return err
			}
		}
		raw.Sensitive = raw.InputType == "password" || raw.InputType == "email" || raw.InputType == "tel" ||
			strings.Contains(strings.ToLower(raw.Name), "password")
		return nil
	}))
}

func (s *chromedpSession) collectActionDownload(ctx context.Context) (*Download, error) {
	timer := time.NewTimer(s.config.DownloadStartWait)
	defer timer.Stop()
	var guid string
	select {
	case guid = <-s.downloadStarted:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, nil
	}
	s.mu.Lock()
	pending := s.downloads[guid]
	s.mu.Unlock()
	if pending == nil {
		return nil, browserError(ErrorDriver, "browser lost download state", "retry the download", true, nil)
	}
	select {
	case <-pending.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.mu.Lock()
	delete(s.downloads, guid)
	state, received, path, suggested := pending.state, pending.received, pending.path, pending.suggested
	s.mu.Unlock()
	if path == "" {
		path = filepath.Join(s.quarantineDir, guid)
	}
	defer os.Remove(path)
	if state != cdbrowser.DownloadProgressStateCompleted {
		return nil, browserError(ErrorDriver, "browser download did not complete", "retry through an approved origin", true, nil)
	}
	if received <= 0 || received > int64(s.config.MaxDownloadBytes) {
		return nil, browserError(ErrorResourceLimit, "browser download exceeds the quarantine limit", "use a smaller approved download", false, nil)
	}
	cleanPath := filepath.Clean(path)
	if filepath.Dir(cleanPath) != filepath.Clean(s.quarantineDir) {
		return nil, browserError(ErrorDriver, "browser download escaped quarantine", "close and reopen the browser", false, nil)
	}
	info, err := os.Lstat(cleanPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > int64(s.config.MaxDownloadBytes) {
		return nil, browserError(ErrorResourceLimit, "browser download is not a bounded regular file", "use a smaller approved download", false, err)
	}
	bytes, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, browserError(ErrorDriver, "browser download could not be quarantined", "retry the download", true, err)
	}
	name := filepath.Base(suggested)
	return &Download{SuggestedName: name, MediaType: mime.TypeByExtension(strings.ToLower(filepath.Ext(name))), Bytes: bytes, Executable: executableDownload(name, bytes)}, nil
}

func runChromedp(requestCtx, browserCtx context.Context, actions ...chromedp.Action) error {
	if err := requestCtx.Err(); err != nil {
		return err
	}
	opCtx, cancel := context.WithCancel(browserCtx)
	done := make(chan struct{})
	go func() {
		select {
		case <-requestCtx.Done():
			cancel()
		case <-done:
		}
	}()
	err := chromedp.Run(opCtx, actions...)
	close(done)
	cancel()
	if err != nil && requestCtx.Err() != nil {
		return requestCtx.Err()
	}
	return err
}

func runBrowserCommand(requestCtx, browserCtx context.Context, command func(context.Context) error) error {
	state := chromedp.FromContext(browserCtx)
	if state == nil || state.Browser == nil {
		return browserError(ErrorDriver, "browser connection is unavailable", "reopen the browser", true, nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = cdp.WithExecutor(ctx, state.Browser)
	done := make(chan error, 1)
	go func() { done <- command(ctx) }()
	select {
	case err := <-done:
		cancel()
		return err
	case <-requestCtx.Done():
		cancel()
		<-done
		return requestCtx.Err()
	}
}

func allocateChromedp(requestCtx, browserCtx context.Context, cancelBrowser context.CancelFunc) error {
	done := make(chan error, 1)
	go func() { done <- chromedp.Run(browserCtx) }()
	select {
	case err := <-done:
		return err
	case <-requestCtx.Done():
		cancelBrowser()
		<-done
		return requestCtx.Err()
	}
}

func validateAttachEndpoint(raw string) error {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\r\n\x00") {
		return browserError(ErrorInvalidRequest, "attached browser endpoint is invalid", "configure a bounded operator endpoint", false, nil)
	}
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return browserError(ErrorInvalidRequest, "attached browser endpoint is invalid", "configure an explicit CDP HTTP or WebSocket endpoint", false, nil)
	}
	switch strings.ToLower(endpoint.Scheme) {
	case "http", "ws":
		host := net.ParseIP(endpoint.Hostname())
		if endpoint.Hostname() != "localhost" && (host == nil || !host.IsLoopback()) {
			return browserError(ErrorInvalidRequest, "insecure attached browser endpoints must be loopback", "use loopback or a TLS-protected operator endpoint", false, nil)
		}
	case "https", "wss":
	default:
		return browserError(ErrorInvalidRequest, "attached browser endpoint scheme is unsupported", "use HTTP, HTTPS, WS, or WSS", false, nil)
	}
	return nil
}

func clickAction(rawBackendID string) (chromedp.Action, error) {
	backendID, err := parseBackendID(rawBackendID)
	if err != nil {
		return nil, err
	}
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if err := dom.ScrollIntoViewIfNeeded().WithBackendNodeID(backendID).Do(ctx); err != nil {
			return err
		}
		x, y, err := backendCenter(ctx, backendID)
		if err != nil {
			return err
		}
		if err := input.DispatchMouseEvent(input.MousePressed, x, y).WithButton(input.Left).WithButtons(1).WithClickCount(1).Do(ctx); err != nil {
			return err
		}
		return input.DispatchMouseEvent(input.MouseReleased, x, y).WithButton(input.Left).WithClickCount(1).Do(ctx)
	}), nil
}

func hoverAction(rawBackendID string) (chromedp.Action, error) {
	backendID, err := parseBackendID(rawBackendID)
	if err != nil {
		return nil, err
	}
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if err := dom.ScrollIntoViewIfNeeded().WithBackendNodeID(backendID).Do(ctx); err != nil {
			return err
		}
		x, y, err := backendCenter(ctx, backendID)
		if err != nil {
			return err
		}
		return input.DispatchMouseEvent(input.MouseMoved, x, y).Do(ctx)
	}), nil
}

func typeAction(rawBackendID, text string) (chromedp.Action, error) {
	backendID, err := parseBackendID(rawBackendID)
	if err != nil {
		return nil, err
	}
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if err := dom.Focus().WithBackendNodeID(backendID).Do(ctx); err != nil {
			return err
		}
		if err := input.DispatchKeyEvent(input.KeyRawDown).WithKey("a").WithCommands([]string{"selectAll"}).Do(ctx); err != nil {
			return err
		}
		if err := input.DispatchKeyEvent(input.KeyUp).WithKey("a").Do(ctx); err != nil {
			return err
		}
		return input.InsertText(text).Do(ctx)
	}), nil
}

func selectAction(rawBackendID string, values []string) (chromedp.Action, error) {
	return fixedNodeFunctionAction(rawBackendID, `function(values) {
		if (this.tagName !== "SELECT") return false;
		const selected = new Set(values);
		for (const option of this.options) option.selected = selected.has(option.value);
		this.dispatchEvent(new Event("input", {bubbles: true}));
		this.dispatchEvent(new Event("change", {bubbles: true}));
		return true;
	}`, values)
}

func checkAction(rawBackendID string, checked *bool) (chromedp.Action, error) {
	if checked == nil {
		return nil, actionValidationError("check action requires a boolean state")
	}
	return fixedNodeFunctionAction(rawBackendID, `function(checked) {
		if (this.tagName !== "INPUT" || (this.type !== "checkbox" && this.type !== "radio")) return false;
		this.checked = checked;
		this.dispatchEvent(new Event("input", {bubbles: true}));
		this.dispatchEvent(new Event("change", {bubbles: true}));
		return true;
	}`, *checked)
}

func fixedNodeFunctionAction(rawBackendID, function string, value any) (chromedp.Action, error) {
	backendID, err := parseBackendID(rawBackendID)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, actionValidationError("browser action value is not serializable")
	}
	return chromedp.ActionFunc(func(ctx context.Context) error {
		object, err := dom.ResolveNode().WithBackendNodeID(backendID).Do(ctx)
		if err != nil || object == nil || object.ObjectID == "" {
			return fmt.Errorf("resolve browser node: %w", err)
		}
		defer func() { _ = cdruntime.ReleaseObject(object.ObjectID).Do(ctx) }()
		result, exception, err := cdruntime.CallFunctionOn(function).
			WithObjectID(object.ObjectID).
			WithArguments([]*cdruntime.CallArgument{{Value: jsontext.Value(encoded)}}).
			WithReturnByValue(true).WithUserGesture(true).Do(ctx)
		if err != nil || exception != nil || result == nil {
			return fmt.Errorf("browser node action failed")
		}
		var applied bool
		if err := json.Unmarshal([]byte(result.Value), &applied); err != nil || !applied {
			return fmt.Errorf("browser node no longer matches the requested action")
		}
		return nil
	}), nil
}

func uploadAction(rawBackendID string, files []string) (chromedp.Action, error) {
	backendID, err := parseBackendID(rawBackendID)
	if err != nil {
		return nil, err
	}
	for _, path := range files {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, actionValidationError("upload path is not a staged regular file")
		}
	}
	return chromedp.ActionFunc(func(ctx context.Context) error {
		return dom.SetFileInputFiles(files).WithBackendNodeID(backendID).Do(ctx)
	}), nil
}

func keyAction(key Key) (chromedp.Action, error) {
	var encoded string
	switch key {
	case KeyEnter:
		encoded = kb.Enter
	case KeyEscape:
		encoded = kb.Escape
	case KeyTab:
		encoded = kb.Tab
	case KeyArrowUp:
		encoded = kb.ArrowUp
	case KeyArrowDown:
		encoded = kb.ArrowDown
	case KeyArrowLeft:
		encoded = kb.ArrowLeft
	case KeyArrowRight:
		encoded = kb.ArrowRight
	case KeyPageUp:
		encoded = kb.PageUp
	case KeyPageDown:
		encoded = kb.PageDown
	case KeyHome:
		encoded = kb.Home
	case KeyEnd:
		encoded = kb.End
	case KeyBackspace:
		encoded = kb.Backspace
	case KeyDelete:
		encoded = kb.Delete
	default:
		return nil, actionValidationError("unsupported browser key")
	}
	return chromedp.KeyEvent(encoded), nil
}

func parseBackendID(raw string) (cdp.BackendNodeID, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, browserError(ErrorStaleRef, "browser node is no longer available", "take a fresh browser.snapshot", false, nil)
	}
	return cdp.BackendNodeID(value), nil
}

func backendCenter(ctx context.Context, backendID cdp.BackendNodeID) (float64, float64, error) {
	quads, err := dom.GetContentQuads().WithBackendNodeID(backendID).Do(ctx)
	if err != nil || len(quads) == 0 || len(quads[0]) < 8 {
		return 0, 0, fmt.Errorf("browser node has no visible bounds")
	}
	quad := quads[0]
	var x, y float64
	for i := 0; i+1 < len(quad); i += 2 {
		x += quad[i]
		y += quad[i+1]
	}
	points := float64(len(quad) / 2)
	return x / points, y / points, nil
}

func axString(value *permissiveAXValue) string {
	if value == nil || len(value.Value) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(value.Value), &decoded); err != nil {
		return ""
	}
	switch value := decoded.(type) {
	case string:
		return value
	case float64, bool:
		return fmt.Sprint(value)
	default:
		return ""
	}
}

func applyAXProperties(raw *RawNode, properties []*permissiveAXProperty) {
	for _, property := range properties {
		if property == nil {
			continue
		}
		value, ok := axBool(property.Value)
		if !ok {
			continue
		}
		switch property.Name {
		case "disabled":
			raw.Disabled = value
		case "checked":
			raw.Checked = boolPointer(value)
		case "selected":
			raw.Selected = boolPointer(value)
		case "expanded":
			raw.Expanded = boolPointer(value)
		}
	}
}

func axBool(value *permissiveAXValue) (bool, bool) {
	if value == nil || len(value.Value) == 0 {
		return false, false
	}
	var decoded any
	if err := json.Unmarshal([]byte(value.Value), &decoded); err != nil {
		return false, false
	}
	switch value := decoded.(type) {
	case bool:
		return value, true
	case string:
		if value == "true" {
			return true, true
		}
		if value == "false" {
			return false, true
		}
	}
	return false, false
}

func boolPointer(value bool) *bool { return &value }

func isActionableRole(role string) bool {
	switch strings.ToLower(role) {
	case "button", "checkbox", "combobox", "link", "menuitem", "option", "radio", "searchbox", "slider", "spinbutton", "switch", "tab", "textbox", "treeitem":
		return true
	default:
		return false
	}
}

func attributes(values []string) map[string]string {
	result := make(map[string]string, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		result[strings.ToLower(values[i])] = values[i+1]
	}
	return result
}

func hasAttribute(values []string, name string) bool {
	for i := 0; i+1 < len(values); i += 2 {
		if strings.EqualFold(values[i], name) {
			return true
		}
	}
	return false
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func redactPageURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return ""
	}
	parsed.User = nil
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if containsAny(lower, "token", "secret", "password", "passwd", "auth", "credential", "signature", "session", "code", "key") {
			query[key] = []string{"[redacted]"}
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String()
}

func executableDownload(name string, bytes []byte) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".app", ".appimage", ".bat", ".cmd", ".com", ".deb", ".dmg", ".exe", ".msi", ".pkg", ".ps1", ".rpm", ".sh":
		return true
	}
	return len(bytes) >= 4 && (string(bytes[:4]) == "\x7fELF" || string(bytes[:2]) == "MZ" ||
		string(bytes[:4]) == "\xfe\xed\xfa\xce" || string(bytes[:4]) == "\xfe\xed\xfa\xcf" ||
		string(bytes[:4]) == "\xcf\xfa\xed\xfe" || string(bytes[:4]) == "\xce\xfa\xed\xfe")
}
