package browser

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

func TestChromedpManagedJourney(t *testing.T) {
	launcher := os.Getenv("CARINA_BROWSER_TEST_LAUNCHER")
	if launcher == "" {
		t.Skip("set CARINA_BROWSER_TEST_LAUNCHER to a carina-daemon binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager, err := NewManager(NewChromedpDriver(ChromedpConfig{LauncherPath: launcher}), Config{ProfileRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.CloseAll()
	key := SessionKey{TenantID: "integration", SessionID: "managed-journey"}
	opened, err := manager.Open(ctx, OpenRequest{Key: key, Mode: ModeManaged})
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Tabs) != 1 {
		t.Fatalf("initial tabs = %d", len(opened.Tabs))
	}
	if err := manager.AllowOrigins(ctx, key, opened.BrowserID, []string{"https://example.com"}); err != nil {
		t.Fatal(err)
	}
	tabID := opened.Tabs[0].ID
	if err := manager.Navigate(ctx, key, opened.BrowserID, tabID, "https://example.com/"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Snapshot(ctx, key, opened.BrowserID, tabID)
	if err != nil {
		if browserErr, ok := err.(*Error); ok {
			t.Fatalf("%v (driver cause: %v)", err, browserErr.Cause)
		}
		t.Fatal(err)
	}
	if len(snapshot.Nodes) == 0 || !snapshot.Untrusted || !strings.Contains(strings.ToLower(snapshot.Title), "example") {
		t.Fatalf("unexpected snapshot: title=%q nodes=%d untrusted=%v", snapshot.Title, len(snapshot.Nodes), snapshot.Untrusted)
	}
	capture, err := manager.Capture(ctx, key, opened.BrowserID, CaptureRequest{TabID: tabID})
	if err != nil {
		t.Fatal(err)
	}
	if capture.MediaType != "image/png" || len(capture.Bytes) < 100 || capture.Width <= 0 || capture.Height <= 0 {
		t.Fatalf("unexpected capture: type=%q bytes=%d size=%dx%d", capture.MediaType, len(capture.Bytes), capture.Width, capture.Height)
	}
}

func TestChromedpTypedActionsAndTabs(t *testing.T) {
	launcher := os.Getenv("CARINA_BROWSER_TEST_LAUNCHER")
	if launcher == "" {
		t.Skip("set CARINA_BROWSER_TEST_LAUNCHER to a carina-daemon binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager, err := NewManager(NewChromedpDriver(ChromedpConfig{LauncherPath: launcher}), Config{ProfileRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.CloseAll()
	key := SessionKey{TenantID: "integration", SessionID: "typed-actions"}
	opened, err := manager.Open(ctx, OpenRequest{Key: key, Mode: ModeManaged})
	if err != nil {
		t.Fatal(err)
	}
	tabID := opened.Tabs[0].ID
	state := manager.sessions[key]
	driverSession, ok := state.driver.(*chromedpSession)
	if !ok {
		t.Fatal("managed session is not a chromedp session")
	}
	tab, _, err := driverSession.tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	var frameTree *page.FrameTree
	const document = `<!doctype html><html><head><title>Typed actions</title></head><body>
		<label>Message <input aria-label="Message" value="old"></label>
		<select aria-label="Choice"><option value="a">Alpha</option><option value="b">Beta</option></select>
		<label><input type="checkbox" aria-label="Enabled"> Enabled</label>
		<button aria-label="Apply" onclick="document.getElementById('status').textContent='Applied'">Apply</button>
		<button aria-label="Confirm" onclick="confirm('Proceed?')">Confirm</button>
		<p id="status">Idle</p>
	</body></html>`
	if err := runChromedp(ctx, tab.ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			frameTree, err = page.GetFrameTree().Do(ctx)
			return err
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return page.SetDocumentContent(frameTree.Frame.ID, document).Do(ctx)
		}),
	); err != nil {
		t.Fatal(err)
	}

	snapshot, err := manager.Snapshot(ctx, key, opened.BrowserID, tabID)
	if err != nil {
		t.Fatal(err)
	}
	messageRef := snapshotRef(snapshot, "textbox", "Message")
	choiceRef := snapshotRef(snapshot, "combobox", "Choice")
	checkRef := snapshotRef(snapshot, "checkbox", "Enabled")
	applyRef := snapshotRef(snapshot, "button", "Apply")
	confirmRef := snapshotRef(snapshot, "button", "Confirm")
	for label, ref := range map[string]string{"message": messageRef, "choice": choiceRef, "check": checkRef, "apply": applyRef, "confirm": confirmRef} {
		if ref == "" {
			t.Fatalf("missing %s ref in snapshot: %+v", label, snapshot.Nodes)
		}
	}
	if _, err := manager.Action(ctx, key, opened.BrowserID, tabID, Action{Kind: ActionType, Ref: messageRef, Text: "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err = manager.Snapshot(ctx, key, opened.BrowserID, tabID)
	if err != nil {
		t.Fatal(err)
	}
	if node := snapshotNode(snapshot, "textbox", "Message"); node == nil || node.Value != "hello" {
		t.Fatalf("typed value not reflected: %+v", node)
	}
	choiceRef = snapshotRef(snapshot, "combobox", "Choice")
	if _, err := manager.Action(ctx, key, opened.BrowserID, tabID, Action{Kind: ActionSelect, Ref: choiceRef, Values: []string{"b"}}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err = manager.Snapshot(ctx, key, opened.BrowserID, tabID)
	if err != nil {
		t.Fatal(err)
	}
	checkRef = snapshotRef(snapshot, "checkbox", "Enabled")
	checked := true
	if _, err := manager.Action(ctx, key, opened.BrowserID, tabID, Action{Kind: ActionCheck, Ref: checkRef, Checked: &checked}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err = manager.Snapshot(ctx, key, opened.BrowserID, tabID)
	if err != nil {
		t.Fatal(err)
	}
	applyRef = snapshotRef(snapshot, "button", "Apply")
	if _, err := manager.Action(ctx, key, opened.BrowserID, tabID, Action{Kind: ActionClick, Ref: applyRef}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err = manager.Snapshot(ctx, key, opened.BrowserID, tabID)
	if err != nil {
		t.Fatal(err)
	}
	confirmRef = snapshotRef(snapshot, "button", "Confirm")
	dialogResult, err := manager.Action(ctx, key, opened.BrowserID, tabID, Action{Kind: ActionClick, Ref: confirmRef}, func(plan ActionPlan) error {
		if plan.EffectiveEffect != EffectExternalSubmit {
			t.Fatalf("confirm action effect = %s", plan.EffectiveEffect)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if dialogResult.Dialog == nil || dialogResult.Dialog.Message != "Proceed?" {
		t.Fatalf("dialog was not projected: %+v", dialogResult)
	}
	if _, err := manager.Action(ctx, key, opened.BrowserID, tabID, Action{Kind: ActionDialogDismiss}, nil); err != nil {
		t.Fatal(err)
	}
	newTab, err := manager.OpenTab(ctx, key, opened.BrowserID, "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ActivateTab(ctx, key, opened.BrowserID, newTab.ID); err != nil {
		t.Fatal(err)
	}
	tabs, err := manager.ListTabs(ctx, key, opened.BrowserID)
	if err != nil || len(tabs) != 2 {
		t.Fatalf("tabs=%+v err=%v", tabs, err)
	}
	if err := manager.CloseTab(ctx, key, opened.BrowserID, newTab.ID); err != nil {
		t.Fatal(err)
	}
}

func snapshotRef(snapshot Snapshot, role, name string) string {
	node := snapshotNode(snapshot, role, name)
	if node == nil {
		return ""
	}
	return node.Ref
}

func snapshotNode(snapshot Snapshot, role, name string) *Node {
	for index := range snapshot.Nodes {
		if snapshot.Nodes[index].Role == role && snapshot.Nodes[index].Name == name {
			return &snapshot.Nodes[index]
		}
	}
	return nil
}
