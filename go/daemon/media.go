package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/artifact"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
	"github.com/Nebutra/carina/go/scheduler"
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
// among its input modalities (fail-closed: nil/absent modalities means no
// image support). This is the per-turn gate collectRequestMedia applies
// BEFORE any provider I/O, so a text-only model never has image bytes
// resolved, encoded, or transmitted on its behalf. Reuses
// provider_registry.go's containsStringFold.
func modelSupportsImageInput(m provider.Model) bool {
	return m.Modalities != nil && containsStringFold(m.Modalities.Input, "image")
}

// Request media budget: at most 4 images totaling at most 4 MiB per model
// call. Newest-first selection mirrors the transcript's own bias (recent
// context wins); anything over budget stays placeholder-only. The caps are
// deliberately conservative — provider limits are higher (Anthropic ~5 MB
// per image), but every attached image is re-sent on EVERY subsequent turn
// until the referencing turn is elided, so the real cost is per-turn, not
// per-request.
const (
	maxRequestMediaParts = 4
	maxRequestMediaBytes = 4 << 20
)

func (d *Daemon) validateTaskInputMedia(sessionID string, refs []MediaRef) ([]scheduler.InputMediaRef, error) {
	if len(refs) > maxRequestMediaParts {
		return nil, fmt.Errorf("input_media_refs must contain at most %d images", maxRequestMediaParts)
	}
	out := make([]scheduler.InputMediaRef, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	var total int64
	for _, ref := range refs {
		if len(ref.ArtifactID) != 64 || !strings.HasPrefix(ref.MediaType, "image/") || ref.Bytes < 1 {
			return nil, fmt.Errorf("invalid input media reference")
		}
		if _, duplicate := seen[ref.ArtifactID]; duplicate {
			return nil, fmt.Errorf("duplicate input media reference %s", ref.ArtifactID)
		}
		seen[ref.ArtifactID] = struct{}{}
		raw, meta, err := d.artifacts.Read(artifact.Scope{SessionID: sessionID}, ref.ArtifactID)
		if err != nil {
			return nil, fmt.Errorf("input media %s is unavailable in this session: %w", ref.ArtifactID, err)
		}
		mediaType, ok := sniffImageMediaType(raw)
		if !ok || mediaType != ref.MediaType || meta.Bytes != ref.Bytes {
			return nil, fmt.Errorf("input media metadata does not match stored content")
		}
		total += meta.Bytes
		if total > maxRequestMediaBytes {
			return nil, fmt.Errorf("input media exceeds %d byte request budget", maxRequestMediaBytes)
		}
		out = append(out, scheduler.InputMediaRef{
			ArtifactID: ref.ArtifactID, MediaType: ref.MediaType, Bytes: ref.Bytes, Origin: ref.Origin,
		})
	}
	return out, nil
}

func attachTaskInputMedia(transcript *Transcript, task *scheduler.Task) {
	if transcript == nil || task == nil || len(task.InputMediaRefs) == 0 {
		return
	}
	refs := make([]MediaRef, 0, len(task.InputMediaRefs))
	for _, ref := range task.InputMediaRefs {
		refs = append(refs, MediaRef{
			ArtifactID: ref.ArtifactID, MediaType: ref.MediaType, Bytes: ref.Bytes, Origin: ref.Origin,
		})
	}
	transcript.addTurn(Turn{
		Tool: "user", ActionBrief: "input-media",
		Obs: Observation{Content: fmt.Sprintf("User attached %d image(s).", len(refs)), MediaRefs: refs, Pinned: true},
	})
}

// catalogModelImageCapable resolves whether the task's model string refers to
// a catalog entry that declares image input. The model string is the routing
// form "provider/model..." (targetedModel splits on the first slash). Empty
// or unresolvable model strings — the default-model path, bare model ids not
// in the catalog, offline mocks — return false: media is only ever attached
// when the model is explicitly known to accept it (fail-closed).
func catalogModelImageCapable(cat provider.Catalog, model string) bool {
	providerID, modelID, ok := strings.Cut(strings.TrimSpace(model), "/")
	if !ok || providerID == "" || modelID == "" {
		return false
	}
	info, ok := cat[providerID]
	if !ok {
		return false
	}
	m, ok := info.Models[modelID]
	if !ok {
		return false
	}
	return modelSupportsImageInput(m)
}

// collectRequestMedia walks the transcript newest-first and resolves the
// MediaRefs a vision-capable model should receive this turn, reading bytes
// back from the artifact store. Returns nil (attach nothing) when the model
// is not affirmatively image-capable per the catalog, when the transcript
// carries no live refs, or when the store no longer holds the content
// (TTL/GC expiry) — in every such case the model still sees the textual
// placeholder that render() already emits for each MediaRef. Elided
// observations contribute nothing: elision means "removed from the model
// view", and media follows the same rule.
func (d *Daemon) collectRequestMedia(sessionID, model string, tr *Transcript) []modelrouter.MediaPart {
	if d.artifacts == nil || tr == nil || !catalogModelImageCapable(d.providerCatalog, model) {
		return nil
	}
	var parts []modelrouter.MediaPart
	var total int64
	for i := len(tr.Turns) - 1; i >= 0 && len(parts) < maxRequestMediaParts; i-- {
		turn := tr.Turns[i]
		if turn.Obs.Elided {
			continue
		}
		for _, ref := range turn.Obs.MediaRefs {
			if len(parts) >= maxRequestMediaParts || total+ref.Bytes > maxRequestMediaBytes {
				break
			}
			raw, _, err := d.artifacts.Read(artifact.Scope{SessionID: sessionID}, ref.ArtifactID)
			if err != nil {
				continue // expired/GC'd: placeholder-only, never an error
			}
			parts = append(parts, modelrouter.MediaPart{MediaType: ref.MediaType, Data: raw})
			total += int64(len(raw))
		}
	}
	return parts
}
