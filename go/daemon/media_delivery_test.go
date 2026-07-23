package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/artifact"
	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
	"github.com/Nebutra/carina/go/scheduler"
)

// tinyPNG is a minimal valid-magic PNG payload (magic bytes + filler); the
// sniff allowlist only inspects the prefix, which is exactly the fail-closed
// contract under test.
func tinyPNG() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte("fake-png-body")...)
}

func TestTaskInputMediaIsDurableAndPartOfSubmissionIdentity(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	session, _ := d.store.CreateSession(workspace, "safe-edit")
	ref, err := ingestImageMedia(d.artifacts, artifact.Scope{SessionID: session.SessionID}, "composer image 1", tinyPNG())
	if err != nil {
		t.Fatal(err)
	}
	clientID := "submit-media"
	base := taskSubmitParams{SessionID: session.SessionID, ClientSubmissionID: &clientID, Prompt: "inspect", Mode: "background"}
	withMedia := base
	withMedia.InputMediaRefs = []MediaRef{ref}
	if taskSubmissionFingerprint(base) == taskSubmissionFingerprint(withMedia) {
		t.Fatal("input media refs were omitted from idempotency identity")
	}

	task := d.sched.Submit(session.SessionID, session.WorkspaceID, "inspect")
	d.sched.SetInputMediaRefs(task.TaskID, []scheduler.InputMediaRef{{
		ArtifactID: ref.ArtifactID, MediaType: ref.MediaType, Bytes: ref.Bytes, Origin: ref.Origin,
	}})
	task, _ = d.sched.Get(task.TaskID)
	transcript := newTranscript(task.UserPrompt)
	attachTaskInputMedia(transcript, task)
	if len(transcript.Turns) != 1 || len(transcript.Turns[0].Obs.MediaRefs) != 1 || transcript.Turns[0].Obs.MediaRefs[0].ArtifactID != ref.ArtifactID {
		t.Fatalf("task input media was not projected into the model transcript: %+v", transcript.Turns)
	}
	if err := d.runs.saveChecked(task); err != nil {
		t.Fatal(err)
	}
	loaded := d.runs.load()
	found := false
	for _, candidate := range loaded {
		if candidate.TaskID == task.TaskID {
			found = len(candidate.InputMediaRefs) == 1 && candidate.InputMediaRefs[0].ArtifactID == ref.ArtifactID
		}
	}
	if !found {
		t.Fatal("run store did not preserve input media refs")
	}
}

// TestReadImageFileProducesMediaRefNotBinary: the producer half. Reading an
// image through the outcome dispatch must put bytes in the artifact store and
// return a placeholder + MediaRef — never raw binary in the display string.
func TestReadImageFileProducesMediaRefNotBinary(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "look at the diagram")

	png := tinyPNG()
	if err := os.WriteFile(filepath.Join(ws, "diagram.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	var completed map[string]any
	d.events.Tap(func(_ string, event map[string]any) {
		if event["type"] == "ToolCallCompleted" {
			completed, _ = event["payload"].(map[string]any)
		}
	})
	_, outcome := d.executeActionOutcome(sess, task, &action{Tool: "read", Path: "diagram.png"})
	if outcome.status != "completed" {
		t.Fatalf("read of an image must complete, got %q (%s)", outcome.status, outcome.display)
	}
	if len(outcome.mediaRefs) != 1 {
		t.Fatalf("expected exactly one MediaRef, got %d", len(outcome.mediaRefs))
	}
	ref := outcome.mediaRefs[0]
	if ref.MediaType != "image/png" || ref.Bytes != int64(len(png)) {
		t.Fatalf("unexpected ref: %+v", ref)
	}
	if !strings.HasPrefix(outcome.display, "[image: image/png") {
		t.Fatalf("display must be the placeholder, got %q", outcome.display)
	}
	if strings.Contains(outcome.display, "PNG") || len(outcome.display) > 200 {
		t.Fatalf("display must not carry binary content: %q", outcome.display)
	}
	raw, _, err := d.artifacts.Read(artifact.Scope{SessionID: sess.SessionID}, ref.ArtifactID)
	if err != nil {
		t.Fatalf("artifact store must hold the bytes: %v", err)
	}
	if string(raw) != string(png) {
		t.Fatal("stored bytes differ from the file content")
	}
	refs, ok := completed["media_refs"].([]MediaRef)
	if !ok || len(refs) != 1 || refs[0].ArtifactID != ref.ArtifactID {
		t.Fatalf("authoritative event did not publish media refs: %#v", completed)
	}
	ids, ok := completed["artifact_ids"].([]string)
	if !ok || !slices.Contains(ids, ref.ArtifactID) {
		t.Fatalf("media artifact missing from artifact_ids: %#v", completed)
	}
}

// TestDispatchActionLegacyImageReadReturnsPlaceholder: the legacy string path
// (MCP server adapter) must also never emit raw binary.
func TestDispatchActionLegacyImageReadReturnsPlaceholder(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "look")

	if err := os.WriteFile(filepath.Join(ws, "shot.png"), tinyPNG(), 0o600); err != nil {
		t.Fatal(err)
	}
	out := d.dispatchAction(sess, task, &action{Tool: "read", Path: "shot.png"})
	if !strings.HasPrefix(out, "[image: image/png") {
		t.Fatalf("legacy path must return the placeholder, got %q", out)
	}
}

func testImageCatalog(providerID, modelID string, imageInput bool) provider.Catalog {
	input := []string{"text"}
	if imageInput {
		input = append(input, "image")
	}
	return provider.Catalog{
		providerID: provider.Info{
			ID: providerID,
			Models: map[string]provider.Model{
				modelID: {Modalities: &provider.Modalities{Input: input, Output: []string{"text"}}},
			},
		},
	}
}

// TestCollectRequestMediaGatesOnModelModalities: media only attaches when the
// catalog affirmatively declares image input for the exact model. Every other
// shape — text-only model, unknown model, empty model — yields nil.
func TestCollectRequestMediaGatesOnModelModalities(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")

	ref, err := ingestImageMedia(d.artifacts, artifact.Scope{SessionID: sess.SessionID}, "read x.png", tinyPNG())
	if err != nil {
		t.Fatal(err)
	}
	tr := newTranscript("t")
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read x.png", Obs: Observation{Content: ref.placeholder(), MediaRefs: []MediaRef{ref}}})

	d.providerCatalog = testImageCatalog("visionprov", "eyes-1", true)
	if parts := d.collectRequestMedia(sess.SessionID, "visionprov/eyes-1", tr); len(parts) != 1 {
		t.Fatalf("vision model must receive 1 part, got %d", len(parts))
	} else if parts[0].MediaType != "image/png" || string(parts[0].Data) != string(tinyPNG()) {
		t.Fatalf("part content mismatch: %+v", parts[0])
	}

	d.providerCatalog = testImageCatalog("textprov", "words-1", false)
	if parts := d.collectRequestMedia(sess.SessionID, "textprov/words-1", tr); parts != nil {
		t.Fatalf("text-only model must get nil media, got %d parts", len(parts))
	}
	if parts := d.collectRequestMedia(sess.SessionID, "unknown/model", tr); parts != nil {
		t.Fatal("unknown model must get nil media (fail-closed)")
	}
	if parts := d.collectRequestMedia(sess.SessionID, "", tr); parts != nil {
		t.Fatal("empty model must get nil media (fail-closed)")
	}
}

// TestCollectRequestMediaSkipsElidedAndEnforcesCaps: elided observations are
// out of the model view — their media must not ride along; the count cap
// bounds per-request growth.
func TestCollectRequestMediaSkipsElidedAndEnforcesCaps(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.providerCatalog = testImageCatalog("visionprov", "eyes-1", true)

	tr := newTranscript("t")
	// One elided ref, then maxRequestMediaParts+1 live refs with distinct
	// content (distinct sha IDs).
	mk := func(tag string, elided bool) {
		content := append(tinyPNG(), []byte(tag)...)
		ref, err := ingestImageMedia(d.artifacts, artifact.Scope{SessionID: sess.SessionID}, "read "+tag, content)
		if err != nil {
			t.Fatal(err)
		}
		tr.addTurn(Turn{Tool: "read", ActionBrief: "read " + tag,
			Obs: Observation{Content: ref.placeholder(), Elided: elided, MediaRefs: []MediaRef{ref}}})
	}
	mk("elided", true)
	for i := 0; i <= maxRequestMediaParts; i++ {
		mk(strings.Repeat("x", i+1), false)
	}
	parts := d.collectRequestMedia(sess.SessionID, "visionprov/eyes-1", tr)
	if len(parts) != maxRequestMediaParts {
		t.Fatalf("cap must bound parts at %d, got %d", maxRequestMediaParts, len(parts))
	}
	for _, p := range parts {
		if strings.HasSuffix(string(p.Data), "elided") {
			t.Fatal("elided observation's media must not be attached")
		}
	}
}

// capturingProvider records the last request the router delivered to it.
type capturingProvider struct{ last modelrouter.Request }

func (c *capturingProvider) Name() string { return "capture" }
func (c *capturingProvider) Complete(_ context.Context, req modelrouter.Request) (*modelrouter.Response, error) {
	c.last = req
	return &modelrouter.Response{Provider: "capture", Model: req.Model, Text: `{"tool":"done","summary":"ok"}`}, nil
}

// TestRouterReasonerPassesMediaThrough: the media-capable reasoner upgrade
// path must deliver parts into the router request; the plain segments path
// must not.
func TestRouterReasonerPassesMediaThrough(t *testing.T) {
	router := modelrouter.New()
	cap := &capturingProvider{}
	router.RegisterProvider(cap)
	r := newRouterReasoner(router, "capture/m")

	media := []modelrouter.MediaPart{{MediaType: "image/png", Data: tinyPNG()}}
	if _, err := thinkWithRetrySegments(context.Background(), r, "capture/m", "sv", "s", "v", media...); err != nil {
		t.Fatal(err)
	}
	if len(cap.last.Media) != 1 || cap.last.Media[0].MediaType != "image/png" {
		t.Fatalf("media must reach the provider request, got %+v", cap.last.Media)
	}
	if _, err := thinkWithRetrySegments(context.Background(), r, "capture/m", "sv", "s", "v"); err != nil {
		t.Fatal(err)
	}
	if len(cap.last.Media) != 0 {
		t.Fatal("no-media call must not carry media")
	}
}

// TestAnthropicProviderEncodesImageBlocks: image parts become base64 source
// blocks AFTER the text blocks, preserving the cache_control breakpoint.
func TestAnthropicProviderEncodesImageBlocks(t *testing.T) {
	png := tinyPNG()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content []struct {
					Type         string            `json:"type"`
					Text         string            `json:"text"`
					CacheControl map[string]string `json:"cache_control"`
					Source       struct {
						Type      string `json:"type"`
						MediaType string `json:"media_type"`
						Data      string `json:"data"`
					} `json:"source"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		blocks := body.Messages[0].Content
		if len(blocks) != 3 {
			t.Fatalf("want text+text+image blocks, got %d", len(blocks))
		}
		if blocks[0].CacheControl["type"] != "ephemeral" {
			t.Fatalf("stable prefix must keep cache control: %+v", blocks[0])
		}
		img := blocks[2]
		if img.Type != "image" || img.Source.Type != "base64" || img.Source.MediaType != "image/png" {
			t.Fatalf("bad image block: %+v", img)
		}
		if decoded, err := base64.StdEncoding.DecodeString(img.Source.Data); err != nil || string(decoded) != string(png) {
			t.Fatalf("image data round-trip failed: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	store := testAuthStore(t)
	if err := store.SetAPIKey("anthropic", "sk-ant", nil); err != nil {
		t.Fatal(err)
	}
	p := &anthropicProvider{
		id: "anthropic", baseURL: srv.URL, model: "claude-test",
		auth: auth.ProviderChain("anthropic", nil, store, nil), client: srv.Client(),
	}
	_, err := p.Complete(context.Background(), modelrouter.Request{
		Model: "default", Prompt: "stablevolatile", StablePrefix: "stable", VolatileSuffix: "volatile",
		Media: []modelrouter.MediaPart{{MediaType: "image/png", Data: png}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestOpenAIChatEncodesImageParts: media becomes a content-part array with a
// data-URI image_url; without media the content stays a plain string
// (regression guard for the pre-media request shape).
func TestOpenAIChatEncodesImageParts(t *testing.T) {
	png := tinyPNG()
	wantURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	var sawParts, sawPlain bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		raw := body.Messages[0].Content
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(raw, &parts); err == nil {
			sawParts = true
			if len(parts) != 2 || parts[1].Type != "image_url" || parts[1].ImageURL.URL != wantURI {
				t.Fatalf("bad image part: %+v", parts)
			}
		} else {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				t.Fatalf("content neither parts nor string: %s", raw)
			}
			sawPlain = true
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	store := testAuthStore(t)
	if err := store.SetAPIKey("openai", "sk-oai", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "openai", baseURL: srv.URL, defaultModel: "gpt-test",
		auth: auth.ProviderChain("openai", nil, store, nil), client: srv.Client(),
	}}
	if _, err := p.Complete(context.Background(), modelrouter.Request{Model: "default", Prompt: "look",
		Media: []modelrouter.MediaPart{{MediaType: "image/png", Data: png}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), modelrouter.Request{Model: "default", Prompt: "plain"}); err != nil {
		t.Fatal(err)
	}
	if !sawParts || !sawPlain {
		t.Fatalf("expected both shapes exercised: parts=%v plain=%v", sawParts, sawPlain)
	}
}
