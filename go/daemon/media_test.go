package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/artifact"
	"github.com/Nebutra/carina/go/provider"
)

// fakePNG builds a syntactically-sniffs-as-PNG payload carrying a distinctive
// sentinel so tests can grep model-facing output for leaked raw bytes.
func fakePNG(sentinel string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(sentinel)...)
}

func fakeWEBP() []byte {
	return []byte("RIFF\x2a\x00\x00\x00WEBPVP8 payload")
}

func newTestArtifactStore(t *testing.T, configs ...artifact.Config) *artifact.Store {
	t.Helper()
	store, err := artifact.New(t.TempDir(), configs...)
	if err != nil {
		t.Fatalf("artifact.New: %v", err)
	}
	return store
}

func TestSniffImageMediaTypeAllowlist(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{"png", fakePNG("body"), "image/png"},
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}, "image/jpeg"},
		{"gif87a", []byte("GIF87a....."), "image/gif"},
		{"gif89a", []byte("GIF89a....."), "image/gif"},
		{"webp", fakeWEBP(), "image/webp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := sniffImageMediaType(c.raw)
			if !ok || got != c.want {
				t.Fatalf("sniffImageMediaType(%s) = (%q, %v), want (%q, true)", c.name, got, ok, c.want)
			}
		})
	}
}

// TestSniffImageMediaTypeRejectsNonImages is the fail-closed half of the
// allowlist: zero-byte input, truncated magic, non-image formats, and media
// disguised as images (a WAV inside a RIFF container, a data URL, plain text)
// must all be rejected — the exact zero-byte/corrupt class of input this
// pipeline must never crash on or store.
func TestSniffImageMediaTypeRejectsNonImages(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
	}{
		{"zero-byte", nil},
		{"empty slice", []byte{}},
		{"truncated png magic", []byte("\x89PNG\r\n")},
		{"corrupt png magic", []byte("\x88PNG\r\n\x1a\nrest")},
		{"plain text", []byte("hello, this is not an image")},
		{"pdf disguised as image", []byte("%PDF-1.7 ...")},
		{"riff but wav not webp", []byte("RIFF\x2a\x00\x00\x00WAVEfmt payload")},
		{"riff too short for webp tag", []byte("RIFF\x2a\x00")},
		{"data url text", []byte("data:image/png;base64,iVBORw0KGgo=")},
		{"svg (scriptable, not allowlisted)", []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>")},
		{"bmp (not allowlisted)", []byte("BM\x36\x00\x00\x00")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, ok := sniffImageMediaType(c.raw); ok {
				t.Fatalf("sniffImageMediaType must reject %s, got %q", c.name, got)
			}
		})
	}
}

func TestIngestImageMediaHappyPath(t *testing.T) {
	store := newTestArtifactStore(t)
	scope := artifact.Scope{SessionID: "sess-1", TaskID: "task-1"}
	raw := fakePNG("happy-path-body")

	ref, err := ingestImageMedia(store, scope, "tool:screenshot", raw)
	if err != nil {
		t.Fatalf("ingestImageMedia: %v", err)
	}
	sum := sha256.Sum256(raw)
	if ref.ArtifactID != hex.EncodeToString(sum[:]) {
		t.Fatalf("ArtifactID must be the sha256 content address, got %q", ref.ArtifactID)
	}
	if ref.MediaType != "image/png" || ref.Bytes != int64(len(raw)) || ref.Origin != "tool:screenshot" {
		t.Fatalf("unexpected MediaRef: %+v", ref)
	}
	// The ref must point at durably-stored, integrity-checked content.
	got, meta, err := store.Read(scope, ref.ArtifactID)
	if err != nil {
		t.Fatalf("store.Read of minted ref: %v", err)
	}
	if string(got) != string(raw) || meta.MediaType != "image/png" {
		t.Fatalf("stored object mismatch: meta=%+v", meta)
	}
}

// TestIngestImageMediaRejectsWithoutStoreWrite proves sniff-before-store:
// rejected input (zero-byte and corrupt alike) returns an error and leaves
// zero puts and zero objects behind.
func TestIngestImageMediaRejectsWithoutStoreWrite(t *testing.T) {
	store := newTestArtifactStore(t)
	scope := artifact.Scope{SessionID: "sess-1"}
	for _, raw := range [][]byte{nil, {}, []byte("not an image at all")} {
		if _, err := ingestImageMedia(store, scope, "paste", raw); err == nil {
			t.Fatalf("ingestImageMedia must reject %q", raw)
		}
	}
	if puts := store.Metrics().Puts; puts != 0 {
		t.Fatalf("rejected ingestion must not write to the store, got %d puts", puts)
	}
	usage, err := store.Usage()
	if err != nil {
		t.Fatalf("store.Usage: %v", err)
	}
	if usage.ObjectCount != 0 || usage.ReferenceCount != 0 {
		t.Fatalf("rejected ingestion left residue in the store: %+v", usage)
	}
}

// TestIngestImageMediaPropagatesStoreLimits proves the store's existing
// object-size limit surfaces as a typed error (never a panic, never a ref to
// content the store does not hold).
func TestIngestImageMediaPropagatesStoreLimits(t *testing.T) {
	store := newTestArtifactStore(t, artifact.Config{MaxObjectBytes: 16})
	scope := artifact.Scope{SessionID: "sess-1"}
	_, err := ingestImageMedia(store, scope, "paste", fakePNG(strings.Repeat("x", 64)))
	if !errors.Is(err, artifact.ErrObjectTooLarge) {
		t.Fatalf("oversized image must propagate ErrObjectTooLarge, got %v", err)
	}
}

func TestIngestImageMediaRequiresStore(t *testing.T) {
	if _, err := ingestImageMedia(nil, artifact.Scope{SessionID: "s"}, "paste", fakePNG("x")); err == nil {
		t.Fatal("nil store must be an error, not a panic")
	}
}

// TestMediaRefPlaceholderCarriesNoRawBytes locks the placeholder shape: media
// type, byte count, truncated artifact id — and no payload bytes, no data URL.
func TestMediaRefPlaceholderCarriesNoRawBytes(t *testing.T) {
	store := newTestArtifactStore(t)
	sentinel := "RAW_BYTES_SENTINEL_9f8e7d"
	ref, err := ingestImageMedia(store, artifact.Scope{SessionID: "sess-1"}, "paste", fakePNG(sentinel))
	if err != nil {
		t.Fatalf("ingestImageMedia: %v", err)
	}
	got := ref.placeholder()
	if !strings.Contains(got, "image/png") || !strings.Contains(got, "bytes") {
		t.Fatalf("placeholder must name the media type and size, got %q", got)
	}
	if !strings.Contains(got, ref.ArtifactID[:12]+"…") {
		t.Fatalf("placeholder must carry the truncated artifact id, got %q", got)
	}
	if strings.Contains(got, ref.ArtifactID) {
		t.Fatalf("placeholder must truncate, not embed, the full artifact id: %q", got)
	}
	if strings.Contains(got, sentinel) || strings.Contains(got, "data:") || strings.Contains(got, "base64") {
		t.Fatalf("placeholder leaked raw content or a data URL: %q", got)
	}
}

func TestModelSupportsImageInput(t *testing.T) {
	cases := []struct {
		name  string
		model provider.Model
		want  bool
	}{
		{"nil modalities fails closed", provider.Model{}, false},
		{"text-only input", provider.Model{Modalities: &provider.Modalities{Input: []string{"text"}}}, false},
		{"image input", provider.Model{Modalities: &provider.Modalities{Input: []string{"text", "image"}}}, true},
		{"case-folded image input", provider.Model{Modalities: &provider.Modalities{Input: []string{"Text", "IMAGE"}}}, true},
		{"image only in output", provider.Model{Modalities: &provider.Modalities{Input: []string{"text"}, Output: []string{"image"}}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := modelSupportsImageInput(c.model); got != c.want {
				t.Fatalf("modelSupportsImageInput = %v, want %v", got, c.want)
			}
		})
	}
}
