package daemon

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/Nebutra/carina/go/artifact"
	"github.com/Nebutra/carina/go/provider"
)

// MediaRef is a content-addressed reference to a non-text observation payload
// (an image) living in the artifact store. ArtifactID is the store's sha256
// content address (artifact.Store.Put derives id = hex(sha256(raw))), so the
// reference doubles as the content hash — audit continuity by construction,
// consistent with carina's hashes-not-content hygiene. Raw media bytes NEVER
// enter the transcript, checkpoints, or audit payloads: the only rendering of
// a MediaRef anywhere in the model view is placeholder() below, so data URLs
// and inline bytes are unrepresentable in render() output by construction.
type MediaRef struct {
	ArtifactID string `json:"artifact_id"`
	MediaType  string `json:"media_type"`
	Bytes      int64  `json:"bytes"`
	Origin     string `json:"origin,omitempty"` // producing tool/path, informational only
}

// image magic-byte signatures for the strict sniff allowlist below. Kept as
// package vars (not recomputed per call) and deliberately NOT extensible via
// config: adding a media type is a code change with review, matching carina's
// fail-closed posture at every ingestion boundary.
var (
	magicPNG   = []byte("\x89PNG\r\n\x1a\n")
	magicJPEG  = []byte{0xff, 0xd8, 0xff}
	magicGIF87 = []byte("GIF87a")
	magicGIF89 = []byte("GIF89a")
	magicRIFF  = []byte("RIFF")
	magicWEBP  = []byte("WEBP")
)

// sniffImageMediaType identifies raw bytes against a strict magic-byte
// allowlist (png, jpeg, gif, webp). Anything else — zero-byte input, corrupt
// prefixes, non-image formats disguised with an image extension, RIFF
// containers that are not WEBP — returns ok=false. Sniffing happens BEFORE
// any store write (see ingestImageMedia), so rejected input is fail-closed at
// ingestion and leaves no residue anywhere.
func sniffImageMediaType(raw []byte) (string, bool) {
	switch {
	case len(raw) >= len(magicPNG) && bytes.Equal(raw[:len(magicPNG)], magicPNG):
		return "image/png", true
	case len(raw) >= len(magicJPEG) && bytes.Equal(raw[:len(magicJPEG)], magicJPEG):
		return "image/jpeg", true
	case len(raw) >= len(magicGIF87) && (bytes.Equal(raw[:len(magicGIF87)], magicGIF87) || bytes.Equal(raw[:len(magicGIF89)], magicGIF89)):
		return "image/gif", true
	case len(raw) >= 12 && bytes.Equal(raw[:4], magicRIFF) && bytes.Equal(raw[8:12], magicWEBP):
		return "image/webp", true
	}
	return "", false
}

// ingestImageMedia sniffs raw against the image allowlist and, only on a
// match, writes it into the artifact store under the given session scope,
// returning a MediaRef for the transcript. Order matters: sniff-before-store
// means a rejected payload performs no store write at all. Size limits are
// the store's existing contract — MaxObjectBytes and the per-session quota —
// so ErrObjectTooLarge / ErrQuotaExceeded propagate as errors (never panics)
// and the ref is only minted for content the store durably holds.
func ingestImageMedia(store *artifact.Store, scope artifact.Scope, origin string, raw []byte) (MediaRef, error) {
	if store == nil {
		return MediaRef{}, errors.New("media: artifact store is required")
	}
	mediaType, ok := sniffImageMediaType(raw)
	if !ok {
		return MediaRef{}, fmt.Errorf("media: content (%d bytes) is not an allowed image type (png, jpeg, gif, webp)", len(raw))
	}
	meta, err := store.Put(raw, artifact.PutOptions{Scope: scope, MediaType: mediaType})
	if err != nil {
		return MediaRef{}, fmt.Errorf("media: store image: %w", err)
	}
	return MediaRef{ArtifactID: meta.ID, MediaType: mediaType, Bytes: meta.Bytes, Origin: origin}, nil
}

// placeholder is the ONLY model-facing rendering of a MediaRef — e.g.
// "[image: image/png, 48213 bytes, artifact ab12cd34ef56…]". Non-vision
// models degrade gracefully to this line, and because nothing else ever
// serializes media into the transcript, raw bytes / data URLs cannot appear
// in render() output by construction. The artifact ID is truncated to 12 hex
// chars: enough to correlate with the store and audit trail, short enough to
// stay a single cheap line under the compaction char budget.
func (m MediaRef) placeholder() string {
	id := m.ArtifactID
	if len(id) > 12 {
		id = id[:12] + "…"
	}
	return fmt.Sprintf("[image: %s, %d bytes, artifact %s]", m.MediaType, m.Bytes, id)
}

// modelSupportsImageInput reports whether a catalog model declares "image"
// among its input modalities. Deliberately NOT called from any live request
// path yet: it exists so a future prompt-assembly change gates image content
// per-turn BEFORE any provider I/O (fail-closed: nil/absent modalities means
// no image support), instead of discovering mid-request that the model is
// text-only. Reuses provider_registry.go's containsStringFold.
func modelSupportsImageInput(m provider.Model) bool {
	return m.Modalities != nil && containsStringFold(m.Modalities.Input, "image")
}
