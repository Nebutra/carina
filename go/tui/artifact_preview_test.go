package tui

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/mathimage"
	"github.com/Nebutra/carina/go/tui/theme"
)

func previewPNG() []byte {
	raw, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	return raw
}

func TestMediaReferencesAcceptsOnlyBoundedImageArtifacts(t *testing.T) {
	id := strings.Repeat("a", 64)
	refs := mediaReferences(map[string]any{"type": "ToolCallCompleted", "payload": map[string]any{"media_refs": []any{
		map[string]any{"artifact_id": id, "media_type": "image/png", "bytes": float64(12)},
		map[string]any{"artifact_id": strings.Repeat("b", 64), "media_type": "text/html", "bytes": float64(12)},
		map[string]any{"artifact_id": strings.Repeat("c", 64), "media_type": "image/png", "bytes": float64(artifactPreviewMaxBytes + 1)},
	}}})
	if len(refs) != 1 || refs[0].ArtifactID != id {
		t.Fatalf("refs = %#v", refs)
	}
}

func TestArtifactPreviewFetchVerifiesAndRendersInline(t *testing.T) {
	t.Setenv("CARINA_MATH_GRAPHICS", "kitty")
	data := previewPNG()
	digest := sha256.Sum256(data)
	id := hex.EncodeToString(digest[:])
	caller := &fakeCaller{handler: map[string]any{"artifact.read": artifactReadPage{
		NextOffset: int64(len(data)), EOF: true, ContentBase64: base64.StdEncoding.EncodeToString(data),
	}}}
	ref := mediaReference{ArtifactID: id, MediaType: "image/png", Bytes: int64(len(data)), Origin: "read chart.png"}
	msg := fetchArtifactPreview(caller, "sess_1", "/work/one", 3, "call_1", ref)().(artifactPreviewMsg)
	if msg.Err != nil || len(msg.Data) == 0 {
		t.Fatalf("preview = %#v", msg)
	}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID = "sess_1"
	m.workspaceRoot = "/work/one"
	m.sessionGeneration = 3
	m.tr.pushPresentation(eventPresentation{Key: "tool:call_1", Kind: presentationTool, Title: "tool"}, m.th, 80)
	m.handleArtifactPreview(msg)
	if len(m.tr.entries) != 2 || m.tr.entries[1].key != "media:"+id {
		t.Fatalf("image was not inserted after tool: %#v", m.tr.entries)
	}
	if got := m.tr.entries[1].rendered; !strings.Contains(got, "\U0010eeee") || strings.Contains(got, "carina artifact read") {
		t.Fatalf("inline preview not rendered: %q", got)
	}
	if raw := mathimage.Drain(); !strings.Contains(raw, "\x1b_G") {
		t.Fatalf("Kitty image transport was not queued: %q", raw)
	}
}

func TestArtifactPreviewRejectsDigestMismatch(t *testing.T) {
	data := previewPNG()
	caller := &fakeCaller{handler: map[string]any{"artifact.read": artifactReadPage{
		NextOffset: int64(len(data)), EOF: true, ContentBase64: base64.StdEncoding.EncodeToString(data),
	}}}
	msg := fetchArtifactPreview(caller, "sess_1", "/work/one", 3, "call_1", mediaReference{
		ArtifactID: strings.Repeat("0", 64), MediaType: "image/png", Bytes: int64(len(data)),
	})().(artifactPreviewMsg)
	if msg.Err == nil || !strings.Contains(msg.Err.Error(), "digest mismatch") {
		t.Fatalf("expected digest rejection, got %#v", msg)
	}
}

func TestCollapsedTranscriptImageReleasesGraphicsOwnership(t *testing.T) {
	t.Setenv("CARINA_MATH_GRAPHICS", "kitty")
	owner := "test:collapsed-transcript-image"
	defer mathimage.ReleaseOwner(owner)
	p := eventPresentation{
		Key: "media:test", Kind: presentationFile, Title: "image",
		ImageOwner: owner, ImageKey: "collapsed-image", ImageData: previewPNG(),
	}
	_ = p.render(theme.New(theme.Mono), 80)
	if raw := mathimage.Drain(); !strings.Contains(raw, "a=t") {
		t.Fatalf("initial image upload missing: %q", raw)
	}
	p.Collapsed = true
	_ = p.render(theme.New(theme.Mono), 80)
	if raw := mathimage.Drain(); !strings.Contains(raw, "a=d,d=I") {
		t.Fatalf("collapsed image did not release terminal ownership: %q", raw)
	}
	p.Collapsed = false
	_ = p.render(theme.New(theme.Mono), 80)
	if raw := mathimage.Drain(); !strings.Contains(raw, "a=t") {
		t.Fatalf("expanded image did not re-upload after release: %q", raw)
	}
}

func TestArtifactPreviewRejectsStaleWorkspaceWithCollidingSessionID(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID = "sess_same"
	m.workspaceRoot = "/work/current"
	m.sessionGeneration = 8
	m.handleArtifactPreview(artifactPreviewMsg{
		SessionID: "sess_same", WorkspaceRoot: "/work/old", Generation: 7,
		CallID: "call_old", Ref: mediaReference{ArtifactID: strings.Repeat("a", 64)}, Data: previewPNG(),
	})
	if len(m.tr.entries) != 0 {
		t.Fatalf("stale workspace image entered transcript: %#v", m.tr.entries)
	}
}
