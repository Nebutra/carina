package tui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	ui "github.com/Nebutra/carina/go/tui/ui"
)

func testAttachmentPNG(t *testing.T, dir string) (string, []byte) {
	t.Helper()
	var encoded bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.RGBA{R: 0x8e, G: 0x40, B: 0x53, A: 0xff})
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "screen.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, encoded.Bytes()
}

func TestPastedImagePathCreatesAtomicAttachmentAndUndoRestoresIt(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.workspaceRoot = t.TempDir()
	path, _ := testAttachmentPNG(t, m.workspaceRoot)
	cmd := m.handlePaste(tea.PasteMsg{Content: path})
	if cmd == nil {
		t.Fatal("image path paste did not start asynchronous loading")
	}
	drain(m, cmd)
	if len(m.attachments) != 1 || m.attachments[0].PixelWidth != 3 || m.attachments[0].PixelHeight != 2 {
		t.Fatalf("attachments=%+v", m.attachments)
	}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"screen.png", "3x2", "Preview"} {
		if !strings.Contains(view, want) {
			t.Fatalf("attachment surface missing %q:\n%s", want, view)
		}
	}
	if _, handled := m.attachmentKey("delete"); !handled || len(m.attachments) != 0 {
		t.Fatal("focused attachment was not deleted atomically")
	}
	if _, handled := m.handleKey("ctrl+z"); !handled || len(m.attachments) != 1 || m.attachments[0].ID == "" {
		t.Fatalf("undo did not restore attachment identity: %+v", m.attachments)
	}
}

func TestSameImageCanBeInsertedAsIndependentElements(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.workspaceRoot = t.TempDir()
	m.input.SetValue("compare")
	m.input.SetCursorColumn(3)
	path, _ := testAttachmentPNG(t, m.workspaceRoot)
	for i := 0; i < 2; i++ {
		cmd := m.attachImage(path)
		if cmd == nil {
			t.Fatalf("insert %d did not start loading", i)
		}
		drain(m, cmd)
	}
	if len(m.attachments) != 2 {
		t.Fatalf("same image insertions collapsed to %d element(s)", len(m.attachments))
	}
	if m.attachments[0].ID == m.attachments[1].ID {
		t.Fatal("same image insertions reused the editor element identity")
	}
	if m.attachments[0].Digest != m.attachments[1].Digest {
		t.Fatal("same image insertions lost content-addressed identity")
	}
	if m.attachments[0].TextOffset != 3 || m.attachments[1].TextOffset != 3 {
		t.Fatalf("duplicate inline offsets=%d,%d want=3,3", m.attachments[0].TextOffset, m.attachments[1].TextOffset)
	}
	m.attachmentFocus = 0
	if _, handled := m.attachmentKey("delete"); !handled || len(m.attachments) != 1 || m.attachments[0].ID == "" {
		t.Fatalf("deleting one duplicate did not preserve the other: %+v", m.attachments)
	}
}

func TestInlineAttachmentsTraverseBetweenTextBoundaries(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.input.SetValue("ab")
	m.setComposerCaretOffset(1)
	m.attachments = []draftAttachment{
		{ID: "element-a", Digest: "digest", TextOffset: 1},
		{ID: "element-b", Digest: "digest", TextOffset: 1},
	}
	m.attachmentCaretAffinity = attachmentCaretBefore
	m.syncAttachmentPreviewOwner()

	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.attachmentFocus != 0 || m.attachmentPreviewID != "element-a" {
		t.Fatalf("right did not enter first attachment: focus=%d preview=%q", m.attachmentFocus, m.attachmentPreviewID)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.attachmentFocus != 1 || m.attachmentPreviewID != "element-b" {
		t.Fatalf("right did not traverse duplicate-content element: focus=%d preview=%q", m.attachmentFocus, m.attachmentPreviewID)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.attachmentFocus != -1 || m.composerCaretOffset() != 1 || m.attachmentCaretAffinity != attachmentCaretAfter {
		t.Fatalf("right did not exit after attachment run: focus=%d offset=%d affinity=%d", m.attachmentFocus, m.composerCaretOffset(), m.attachmentCaretAffinity)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.composerCaretOffset() != 2 || m.attachmentPreviewID != "" {
		t.Fatalf("right did not continue into text: offset=%d preview=%q", m.composerCaretOffset(), m.attachmentPreviewID)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.composerCaretOffset() != 1 || m.attachmentFocus != -1 || m.attachmentPreviewID != "element-b" {
		t.Fatalf("left adjacency mapping=%d focus=%d preview=%q", m.composerCaretOffset(), m.attachmentFocus, m.attachmentPreviewID)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.attachmentFocus != 1 {
		t.Fatalf("left did not enter attachment from text: focus=%d", m.attachmentFocus)
	}
}

func TestInlineAttachmentDeleteAndUndoPreserveElementIdentity(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.input.SetValue("ab")
	m.setComposerCaretOffset(1)
	m.attachments = []draftAttachment{
		{ID: "element-a", Digest: "same-digest", TextOffset: 1},
		{ID: "element-b", Digest: "same-digest", TextOffset: 1},
	}
	m.attachmentCaretAffinity = attachmentCaretAfter
	m.syncAttachmentPreviewOwner()

	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(m.attachments) != 1 || m.attachments[0].ID != "element-a" {
		t.Fatalf("backspace removed wrong inline element: %+v", m.attachments)
	}
	if _, handled := m.handleKey("ctrl+z"); !handled {
		t.Fatal("composer undo key was not handled")
	}
	if len(m.attachments) != 2 || m.attachments[0].ID != "element-a" || m.attachments[1].ID != "element-b" {
		t.Fatalf("undo corrupted duplicate-content identities: %+v", m.attachments)
	}
	if m.attachmentPreviewID != "element-b" || m.attachmentCaretAffinity != attachmentCaretAfter {
		t.Fatalf("undo did not restore caret adjacency: preview=%q affinity=%d", m.attachmentPreviewID, m.attachmentCaretAffinity)
	}

	m.attachmentCaretAffinity = attachmentCaretBefore
	m.syncAttachmentPreviewOwner()
	m.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
	if len(m.attachments) != 1 || m.attachments[0].ID != "element-b" {
		t.Fatalf("delete removed wrong inline element: %+v", m.attachments)
	}
}

func TestInlineAttachmentAnchorTracksTextEditsByCaretSide(t *testing.T) {
	t.Run("insert before chip shifts anchor", func(t *testing.T) {
		m, _ := newTestModel(&fakeCaller{})
		m.input.SetValue("ab")
		m.setComposerCaretOffset(1)
		m.attachments = []draftAttachment{{ID: "element", Digest: "digest", TextOffset: 1}}
		m.attachmentCaretAffinity = attachmentCaretBefore
		m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
		if m.input.Value() != "axb" || m.attachments[0].TextOffset != 2 {
			t.Fatalf("insert-before text=%q offset=%d", m.input.Value(), m.attachments[0].TextOffset)
		}
		if _, handled := m.handleKey("ctrl+z"); !handled {
			t.Fatal("composer undo key was not handled")
		}
		if m.input.Value() != "ab" || m.attachments[0].TextOffset != 1 || m.attachments[0].ID != "element" {
			t.Fatalf("undo insert-before text=%q attachment=%+v", m.input.Value(), m.attachments[0])
		}
	})

	t.Run("insert after chip keeps anchor", func(t *testing.T) {
		m, _ := newTestModel(&fakeCaller{})
		m.input.SetValue("ab")
		m.setComposerCaretOffset(1)
		m.attachments = []draftAttachment{{ID: "element", Digest: "digest", TextOffset: 1}}
		m.attachmentCaretAffinity = attachmentCaretAfter
		m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
		if m.input.Value() != "axb" || m.attachments[0].TextOffset != 1 {
			t.Fatalf("insert-after text=%q offset=%d", m.input.Value(), m.attachments[0].TextOffset)
		}
	})

	t.Run("delete text after chip keeps caret after chip", func(t *testing.T) {
		m, _ := newTestModel(&fakeCaller{})
		m.input.SetValue("ab")
		m.setComposerCaretOffset(2)
		m.attachments = []draftAttachment{{ID: "element", Digest: "digest", TextOffset: 1}}
		m.attachmentCaretAffinity = attachmentCaretBefore
		m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		if m.input.Value() != "a" || m.attachments[0].TextOffset != 1 || m.attachmentCaretAffinity != attachmentCaretAfter {
			t.Fatalf("delete-after text=%q offset=%d affinity=%d", m.input.Value(), m.attachments[0].TextOffset, m.attachmentCaretAffinity)
		}
		if m.attachmentPreviewID != "element" {
			t.Fatalf("delete-after lost adjacent preview: %q", m.attachmentPreviewID)
		}
	})
}

func TestCaretPreviewOwnershipSurvivesHoverOverride(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.input.SetValue("ab")
	m.setComposerCaretOffset(1)
	m.attachments = []draftAttachment{
		{ID: "caret-element", Digest: "a", TextOffset: 1},
		{ID: "hover-element", Digest: "b", TextOffset: 2},
	}
	m.attachmentCaretAffinity = attachmentCaretBefore
	m.syncAttachmentPreviewOwner()
	if m.attachmentCaretPreviewID != "caret-element" || m.attachmentPreviewID != "caret-element" {
		t.Fatalf("caret preview owner=%q resolved=%q", m.attachmentCaretPreviewID, m.attachmentPreviewID)
	}
	m.attachmentHoverID = "hover-element"
	m.syncAttachmentPreviewOwner()
	if m.attachmentCaretPreviewID != "caret-element" || m.attachmentPreviewID != "hover-element" {
		t.Fatalf("hover override lost caret ownership: caret=%q resolved=%q", m.attachmentCaretPreviewID, m.attachmentPreviewID)
	}
	m.attachmentHoverID = ""
	m.syncAttachmentPreviewOwner()
	if m.attachmentPreviewID != "caret-element" {
		t.Fatalf("hover leave did not restore caret preview: %q", m.attachmentPreviewID)
	}
}

func TestAttachmentUploadsBeforeImmutableTaskSubmission(t *testing.T) {
	dir := t.TempDir()
	path, raw := testAttachmentPNG(t, dir)
	attachment, err := validateDraftAttachment(path, raw)
	if err != nil {
		t.Fatal(err)
	}
	ref := mediaReference{ArtifactID: attachment.Digest, MediaType: attachment.MediaType, Bytes: attachment.ByteSize, Origin: "composer image 1"}
	caller := &fakeCaller{handler: map[string]any{
		"artifact.upload": ref,
		"task.submit":     map[string]any{"task_id": "task_media", "status": "queued"},
	}}
	m, _ := newTestModel(caller)
	m.attachments = []draftAttachment{attachment}
	m.input.SetValue("inspect this screenshot")
	drain(m, m.submit())
	if len(caller.calls) != 2 || caller.calls[0].method != "artifact.upload" || caller.calls[1].method != "task.submit" {
		t.Fatalf("RPC order=%+v", caller.calls)
	}
	refs, ok := caller.calls[1].params["input_media_refs"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("task.submit media refs=%#v", caller.calls[1].params["input_media_refs"])
	}
	item, _ := refs[0].(map[string]any)
	if item["artifact_id"] != attachment.Digest || item["media_type"] != "image/png" {
		t.Fatalf("task.submit media ref=%#v", item)
	}
	if _, leaked := caller.calls[1].params["content_base64"]; leaked {
		t.Fatal("raw image bytes leaked into task.submit")
	}
}

func TestDuplicateAttachmentContentUploadsOnce(t *testing.T) {
	dir := t.TempDir()
	path, raw := testAttachmentPNG(t, dir)
	first, err := validateDraftAttachment(path, raw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := validateDraftAttachment(path, raw)
	if err != nil {
		t.Fatal(err)
	}
	ref := mediaReference{ArtifactID: first.Digest, MediaType: first.MediaType, Bytes: first.ByteSize}
	caller := &fakeCaller{handler: map[string]any{
		"artifact.upload": ref,
		"task.submit":     map[string]any{"task_id": "task_media", "status": "queued"},
	}}
	m, _ := newTestModel(caller)
	m.attachments = []draftAttachment{first, second}
	m.input.SetValue("compare both placements")
	drain(m, m.submit())
	uploads := 0
	for _, call := range caller.calls {
		if call.method == "artifact.upload" {
			uploads++
		}
	}
	if uploads != 1 {
		t.Fatalf("duplicate content uploads=%d want=1; calls=%+v", uploads, caller.calls)
	}
}

func TestAttachmentSnapshotsShareImmutableBlob(t *testing.T) {
	path, raw := testAttachmentPNG(t, t.TempDir())
	attachment, err := validateDraftAttachment(path, raw)
	if err != nil {
		t.Fatal(err)
	}
	cloned := cloneAttachments([]draftAttachment{attachment})
	if len(cloned[0].Data) == 0 || &cloned[0].Data[0] != &attachment.Data[0] {
		t.Fatal("attachment snapshot copied immutable image bytes")
	}
	if cloned[0].ID != attachment.ID || cloned[0].Digest != attachment.Digest {
		t.Fatal("attachment identity changed while cloning")
	}
}

func TestQueuedDraftRestorePreservesAttachments(t *testing.T) {
	path, raw := testAttachmentPNG(t, t.TempDir())
	attachment, err := validateDraftAttachment(path, raw)
	if err != nil {
		t.Fatal(err)
	}
	merged := mergeDraftsForRestore([]promptDraft{{Text: "inspect", Attachments: []draftAttachment{attachment}}}, promptDraft{Text: "next"})
	if len(merged.Attachments) != 1 || merged.Attachments[0].ID != attachment.ID {
		t.Fatalf("restored attachments=%+v", merged.Attachments)
	}
}

func TestAttachmentChipHoverUsesComponentGeometry(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.width, m.height = 80, 24
	first := draftAttachment{ID: strings.Repeat("a", 64), Digest: strings.Repeat("a", 64), MediaType: "image/png", ByteSize: 3, PixelWidth: 3, PixelHeight: 2, SourcePath: "first.png"}
	second := draftAttachment{ID: strings.Repeat("b", 64), Digest: strings.Repeat("b", 64), MediaType: "image/png", ByteSize: 4, PixelWidth: 4, PixelHeight: 2, SourcePath: "second.png"}
	m.attachments = []draftAttachment{first, second}
	m.attachmentFocus = -1
	m.layout()
	view := m.View()
	if view.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode=%v, want all motion for attachment hover", view.MouseMode)
	}
	frame := m.componentFrame
	if len(frame.Graphics) > 0 {
		placement := frame.Graphics[0]
		if placement.Generation != frame.Generation || placement.TargetGeneration != m.sessionGeneration {
			t.Fatalf("graphics placement is not frame/target fenced: frame=%+v placement=%+v", frame, placement)
		}
	}
	var firstHit, secondHit ui.HitRegion
	for _, hit := range collectNodeHits(frame.Root) {
		attachment, ok := hit.Data.(attachmentHit)
		if !ok {
			continue
		}
		switch attachment.ID {
		case first.ID:
			firstHit = hit
		case second.ID:
			secondHit = hit
		}
	}
	if firstHit.ID == "" || secondHit.ID == "" {
		t.Fatalf("attachment hit regions=%+v", collectNodeHits(frame.Root))
	}
	m.Update(tea.MouseMotionMsg{X: firstHit.Bounds.X, Y: firstHit.Bounds.Y})
	if m.attachmentPreviewID != first.ID || m.attachmentFocus != -1 {
		t.Fatalf("hover preview=%q focus=%d", m.attachmentPreviewID, m.attachmentFocus)
	}
	m.Update(tea.MouseClickMsg{X: secondHit.Bounds.X, Y: secondHit.Bounds.Y, Button: tea.MouseLeft})
	if m.attachmentPreviewID != second.ID || m.attachmentFocus != 1 {
		t.Fatalf("click preview=%q focus=%d", m.attachmentPreviewID, m.attachmentFocus)
	}
	m.Update(tea.MouseMotionMsg{X: 0, Y: 0})
	if m.attachmentPreviewID != second.ID {
		t.Fatalf("pointer leave did not restore keyboard preview: %q", m.attachmentPreviewID)
	}
}

func collectNodeHits(node ui.Node) []ui.HitRegion {
	hits := append([]ui.HitRegion(nil), node.Hit...)
	for _, child := range node.Children {
		hits = append(hits, collectNodeHits(child)...)
	}
	return hits
}
