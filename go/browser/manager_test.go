package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakeDriver struct {
	mu          sync.Mutex
	template    RawSnapshot
	launchErr   error
	attachErr   error
	launches    []LaunchOptions
	attachments []AttachOptions
	sessions    []*fakeSession
}

type fakeSession struct {
	mu           sync.Mutex
	snapshot     RawSnapshot
	tabs         []RawTab
	origins      []string
	actions      []DriverAction
	actionResult DriverActionResult
	actionErr    error
	capture      CaptureResult
	closed       bool
	sequence     *[]string
}

func newFakeDriver(snapshot RawSnapshot) *fakeDriver {
	if snapshot.TabID == "" {
		snapshot.TabID = "tab-1"
	}
	if snapshot.DocumentID == "" {
		snapshot.DocumentID = "doc-1"
	}
	return &fakeDriver{template: snapshot}
}

func (d *fakeDriver) session() *fakeSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sessions[len(d.sessions)-1]
}

func (d *fakeDriver) Launch(_ context.Context, opts LaunchOptions) (DriverSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.launches = append(d.launches, opts)
	if d.launchErr != nil {
		return nil, d.launchErr
	}
	s := &fakeSession{
		snapshot: d.template,
		tabs:     []RawTab{{ID: d.template.TabID, URL: d.template.URL, Title: d.template.Title, Active: true}},
		capture:  CaptureResult{MediaType: "image/png", Bytes: []byte("png"), Width: 10, Height: 10},
	}
	d.sessions = append(d.sessions, s)
	return s, nil
}

func (d *fakeDriver) Attach(_ context.Context, opts AttachOptions) (DriverSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attachments = append(d.attachments, opts)
	if d.attachErr != nil {
		return nil, d.attachErr
	}
	s := &fakeSession{
		snapshot: d.template,
		tabs:     []RawTab{{ID: d.template.TabID, URL: d.template.URL, Title: d.template.Title, Active: true}},
		capture:  CaptureResult{MediaType: "image/png", Bytes: []byte("png")},
	}
	d.sessions = append(d.sessions, s)
	return s, nil
}

func (s *fakeSession) AllowOrigins(_ context.Context, origins []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.origins = append(s.origins, origins...)
	return nil
}

func (s *fakeSession) Navigate(_ context.Context, tabID, target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tabID != s.snapshot.TabID {
		return errors.New("unknown tab")
	}
	s.snapshot.URL = target
	s.snapshot.DocumentID += "-nav"
	return nil
}

func (s *fakeSession) Snapshot(_ context.Context, tabID string) (RawSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tabID != s.snapshot.TabID {
		return RawSnapshot{}, errors.New("unknown tab")
	}
	return s.snapshot, nil
}

func (s *fakeSession) InspectNode(_ context.Context, tabID, backendID string) (NodeInspection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tabID != s.snapshot.TabID {
		return NodeInspection{}, errors.New("unknown tab")
	}
	for _, node := range s.snapshot.Nodes {
		if node.BackendNodeID == backendID {
			return NodeInspection{DocumentID: s.snapshot.DocumentID, Node: node}, nil
		}
	}
	return NodeInspection{}, errors.New("unknown node")
}

func (s *fakeSession) ListTabs(context.Context) ([]RawTab, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RawTab(nil), s.tabs...), nil
}

func (s *fakeSession) OpenTab(_ context.Context, target string) (RawTab, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tab := RawTab{ID: fmt.Sprintf("tab-%d", len(s.tabs)+1), URL: target, Active: true}
	s.tabs = append(s.tabs, tab)
	return tab, nil
}

func (s *fakeSession) ActivateTab(_ context.Context, tabID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tabs {
		s.tabs[i].Active = s.tabs[i].ID == tabID
	}
	return nil
}

func (s *fakeSession) CloseTab(_ context.Context, tabID string) error {
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

func (s *fakeSession) Action(_ context.Context, action DriverAction) (DriverActionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sequence != nil {
		*s.sequence = append(*s.sequence, "driver")
	}
	s.actions = append(s.actions, action)
	return s.actionResult, s.actionErr
}

func (s *fakeSession) Capture(context.Context, CaptureRequest) (CaptureResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capture, nil
}

func (s *fakeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func openManaged(t *testing.T, manager *Manager, key SessionKey) OpenResult {
	t.Helper()
	result, err := manager.Open(context.Background(), OpenRequest{Key: key, Mode: ModeManaged})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestManagerScopesOpaqueRefsAndInvalidatesAfterAction(t *testing.T) {
	raw := RawSnapshot{TabID: "tab-1", DocumentID: "doc-1", Nodes: []RawNode{
		{BackendNodeID: "backend-secret-id", Depth: 0, Role: "button", Name: "Continue"},
	}}
	driver := newFakeDriver(raw)
	manager, err := NewManager(driver, Config{ProfileRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	keyA := SessionKey{TenantID: "tenant-a", SessionID: "session-a"}
	openedA := openManaged(t, manager, keyA)
	snapshotA, err := manager.Snapshot(context.Background(), keyA, openedA.BrowserID, "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	ref := snapshotA.Nodes[0].Ref
	if !strings.HasPrefix(ref, "br_") || strings.Contains(ref, "backend") {
		t.Fatalf("reference is not opaque: %q", ref)
	}

	keyB := SessionKey{TenantID: "tenant-b", SessionID: "session-b"}
	openedB := openManaged(t, manager, keyB)
	if _, err := manager.Action(context.Background(), keyB, openedB.BrowserID, "tab-1", Action{Kind: ActionClick, Ref: ref}, nil); err == nil || !isCode(err, ErrorStaleRef) {
		t.Fatalf("cross-session ref should fail stale: %v", err)
	}

	if _, err := manager.Action(context.Background(), keyA, openedA.BrowserID, "tab-1", Action{Kind: ActionClick, Ref: ref}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Action(context.Background(), keyA, openedA.BrowserID, "tab-1", Action{Kind: ActionClick, Ref: ref}, nil); err == nil || !isCode(err, ErrorStaleRef) {
		t.Fatalf("used ref should become stale: %v", err)
	}
}

func TestManagerBoundsSnapshotsAndRedactsSensitiveValues(t *testing.T) {
	checked := true
	raw := RawSnapshot{TabID: "tab-1", DocumentID: "doc", URL: "https://example.com", Nodes: []RawNode{
		{BackendNodeID: "1", Depth: 0, Role: "document", Name: "Page"},
		{BackendNodeID: "2", ParentID: "1", Depth: 1, Role: "textbox", Name: "Password", Value: "secret", Sensitive: true, Checked: &checked},
		{BackendNodeID: "3", Depth: 30, Role: "button", Name: "too deep"},
		{BackendNodeID: "4", Depth: 1, Role: "button", Name: "over node limit"},
	}}
	manager, err := NewManager(newFakeDriver(raw), Config{ProfileRoot: t.TempDir(), Limits: Limits{MaxSnapshotNodes: 2, MaxSnapshotDepth: 4, MaxSnapshotChars: 1024}})
	if err != nil {
		t.Fatal(err)
	}
	key := SessionKey{TenantID: "local", SessionID: "session"}
	opened := openManaged(t, manager, key)
	snapshot, err := manager.Snapshot(context.Background(), key, opened.BrowserID, "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Nodes) != 2 || !snapshot.Truncated || snapshot.Omitted < 2 {
		t.Fatalf("unexpected bounds: %+v", snapshot)
	}
	if snapshot.Nodes[1].Value != "" || snapshot.Nodes[1].ParentRef != snapshot.Nodes[0].Ref {
		t.Fatalf("sensitive value or parent projection invalid: %+v", snapshot.Nodes[1])
	}
	if !snapshot.Untrusted || !strings.Contains(snapshot.ContentNote, "untrusted") {
		t.Fatalf("snapshot trust label missing: %+v", snapshot)
	}
}

func TestHighImpactApprovalRunsImmediatelyBeforeDriver(t *testing.T) {
	raw := RawSnapshot{TabID: "tab-1", DocumentID: "doc", Nodes: []RawNode{
		{BackendNodeID: "1", Depth: 0, Role: "button", Name: "Delete account"},
	}}
	driver := newFakeDriver(raw)
	manager, err := NewManager(driver, Config{ProfileRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	key := SessionKey{TenantID: "local", SessionID: "session"}
	opened := openManaged(t, manager, key)
	snapshot, err := manager.Snapshot(context.Background(), key, opened.BrowserID, "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	sequence := []string{}
	driver.session().sequence = &sequence
	result, err := manager.Action(context.Background(), key, opened.BrowserID, "tab-1", Action{
		Kind: ActionClick, Ref: snapshot.Nodes[0].Ref, DeclaredEffect: EffectObserve,
	}, func(plan ActionPlan) error {
		sequence = append(sequence, "approval")
		if plan.DerivedEffect != EffectDestructive || plan.EffectiveEffect != EffectDestructive {
			t.Fatalf("model declaration lowered derived effect: %+v", plan)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(sequence, ",") != "approval,driver" || result.Plan.EffectiveEffect != EffectDestructive {
		t.Fatalf("approval ordering drifted: %v %+v", sequence, result)
	}
}

func TestSafeActionCallbackRunsImmediatelyBeforeDriver(t *testing.T) {
	raw := RawSnapshot{TabID: "tab-1", DocumentID: "doc", Nodes: []RawNode{
		{BackendNodeID: "1", Depth: 0, Role: "button", Name: "Continue"},
	}}
	driver := newFakeDriver(raw)
	manager, err := NewManager(driver, Config{ProfileRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	key := SessionKey{TenantID: "local", SessionID: "safe-callback"}
	opened := openManaged(t, manager, key)
	snapshot, err := manager.Snapshot(context.Background(), key, opened.BrowserID, "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	sequence := []string{}
	driver.session().sequence = &sequence
	_, err = manager.Action(context.Background(), key, opened.BrowserID, "tab-1", Action{
		Kind: ActionClick, Ref: snapshot.Nodes[0].Ref,
	}, func(plan ActionPlan) error {
		sequence = append(sequence, "authorize")
		if plan.EffectiveEffect != EffectReversible {
			t.Fatalf("safe action effect = %+v", plan)
		}
		return nil
	})
	if err != nil || strings.Join(sequence, ",") != "authorize,driver" {
		t.Fatalf("safe callback ordering = %v err=%v", sequence, err)
	}
}

func TestActionRefreshesNodeSemanticsBeforeApproval(t *testing.T) {
	raw := RawSnapshot{TabID: "tab-1", DocumentID: "doc", Nodes: []RawNode{
		{BackendNodeID: "1", Depth: 0, Role: "button", Name: "Continue"},
	}}
	driver := newFakeDriver(raw)
	manager, err := NewManager(driver, Config{ProfileRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	key := SessionKey{TenantID: "local", SessionID: "dynamic-effect"}
	opened := openManaged(t, manager, key)
	snapshot, err := manager.Snapshot(context.Background(), key, opened.BrowserID, "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	session := driver.session()
	session.mu.Lock()
	session.snapshot.Nodes[0].Name = "Delete account"
	session.mu.Unlock()
	approved := false
	result, err := manager.Action(context.Background(), key, opened.BrowserID, "tab-1", Action{
		Kind: ActionClick, Ref: snapshot.Nodes[0].Ref,
	}, func(plan ActionPlan) error {
		approved = true
		if plan.EffectiveEffect != EffectDestructive || plan.Name != "Delete account" {
			t.Fatalf("fresh node semantics were not used: %+v", plan)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !approved || result.Plan.EffectiveEffect != EffectDestructive {
		t.Fatalf("dynamic high-impact action bypassed approval: %+v", result)
	}
}

func TestAttachRaisesInteractiveActionsAndEndpointErrorsAreRedacted(t *testing.T) {
	raw := RawSnapshot{TabID: "tab-1", DocumentID: "doc", Nodes: []RawNode{
		{BackendNodeID: "1", Depth: 0, Role: "button", Name: "Continue"},
	}}
	driver := newFakeDriver(raw)
	manager, err := NewManager(driver, Config{ProfileRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	key := SessionKey{TenantID: "local", SessionID: "attach"}
	opened, err := manager.Open(context.Background(), OpenRequest{Key: key, Mode: ModeAttach, AttachEndpoint: "ws://secret-endpoint/devtools"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Snapshot(context.Background(), key, opened.BrowserID, "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = manager.Action(context.Background(), key, opened.BrowserID, "tab-1", Action{Kind: ActionClick, Ref: snapshot.Nodes[0].Ref}, func(plan ActionPlan) error {
		called = true
		if plan.EffectiveEffect != EffectAuthenticatedRepresentation || !plan.Attached {
			t.Fatalf("attach effect floor missing: %+v", plan)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("attach action did not require approval: called=%v err=%v", called, err)
	}

	failing := newFakeDriver(raw)
	failing.attachErr = errors.New("dial ws://secret-endpoint/devtools: refused")
	failingManager, err := NewManager(failing, Config{ProfileRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = failingManager.Open(context.Background(), OpenRequest{Key: SessionKey{TenantID: "local", SessionID: "fail"}, Mode: ModeAttach, AttachEndpoint: "ws://secret-endpoint/devtools"})
	if err == nil || strings.Contains(err.Error(), "secret-endpoint") {
		t.Fatalf("attach endpoint leaked through error: %v", err)
	}
}

func TestManagedProfilesArePrivateRecoveredAndRemoved(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, profilePrefix+"stale")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	staleID := "browser_0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(stale, profileMarker), []byte(staleID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(root, profilePrefix+"foreign")
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	driver := newFakeDriver(RawSnapshot{})
	manager, err := NewManager(driver, Config{ProfileRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale owned profile survived: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign directory was removed: %v", err)
	}
	key := SessionKey{TenantID: "local", SessionID: "session"}
	opened := openManaged(t, manager, key)
	profile := driver.launches[0].ProfileDir
	info, err := os.Stat(profile)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("profile mode = %v err=%v", info, err)
	}
	if err := manager.Close(key, opened.BrowserID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("profile survived close: %v", err)
	}
}

func TestCaptureAndTabLimitsFailClosed(t *testing.T) {
	driver := newFakeDriver(RawSnapshot{})
	manager, err := NewManager(driver, Config{ProfileRoot: t.TempDir(), Limits: Limits{MaxTabs: 1, MaxScreenshotBytes: 2}})
	if err != nil {
		t.Fatal(err)
	}
	key := SessionKey{TenantID: "local", SessionID: "session"}
	opened := openManaged(t, manager, key)
	if _, err := manager.OpenTab(context.Background(), key, opened.BrowserID, "https://example.com"); err == nil || !isCode(err, ErrorResourceLimit) {
		t.Fatalf("tab limit did not fail closed: %v", err)
	}
	if _, err := manager.Capture(context.Background(), key, opened.BrowserID, CaptureRequest{TabID: "tab-1"}); err == nil || !isCode(err, ErrorResourceLimit) {
		t.Fatalf("capture limit did not fail closed: %v", err)
	}
}

func TestManagerConcurrentCloseAndObservationsStayFenced(t *testing.T) {
	driver := newFakeDriver(RawSnapshot{TabID: "tab-1", DocumentID: "doc", Nodes: []RawNode{
		{BackendNodeID: "1", Depth: 0, Role: "button", Name: "Continue"},
	}})
	manager, err := NewManager(driver, Config{ProfileRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	key := SessionKey{TenantID: "tenant", SessionID: "concurrent"}
	opened := openManaged(t, manager, key)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := manager.ListTabs(context.Background(), key, opened.BrowserID)
			if err != nil && !isCode(err, ErrorNotFound) {
				t.Errorf("list during close: %v", err)
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := manager.Snapshot(context.Background(), key, opened.BrowserID, "tab-1")
			if err != nil && !isCode(err, ErrorNotFound) {
				t.Errorf("snapshot during close: %v", err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := manager.Close(key, opened.BrowserID); err != nil && !isCode(err, ErrorNotFound) {
			t.Errorf("close: %v", err)
		}
	}()
	wg.Wait()
	if _, err := manager.Open(context.Background(), OpenRequest{Key: key, Mode: ModeManaged}); err != nil {
		t.Fatalf("reopen after concurrent close: %v", err)
	}
}

func isCode(err error, code ErrorCode) bool {
	var browserErr *Error
	return errors.As(err, &browserErr) && browserErr.Code == code
}
