package daemon

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Nebutra/carina/go/artifact"
)

func TestArtifactRPCRequiresExactScope(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	scope := artifact.Scope{SessionID: "sess_scope", TaskID: "task_scope", CallID: "call_scope"}
	meta, err := d.artifacts.Put([]byte("kept outside audit"), artifact.PutOptions{Scope: scope, MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(map[string]any{"session_id": scope.SessionID, "task_id": scope.TaskID, "call_id": scope.CallID, "artifact_id": meta.ID})
	stat, err := d.handleArtifactStat(params)
	if err != nil || stat.(artifact.Metadata).ID != meta.ID {
		t.Fatalf("stat=%+v err=%v", stat, err)
	}
	read, err := d.handleArtifactRead(params)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(read.(map[string]any)["content_base64"].(string))
	if err != nil || string(raw) != "kept outside audit" {
		t.Fatalf("read=%q err=%v", raw, err)
	}
	wrong, _ := json.Marshal(map[string]any{"session_id": "sess_other", "task_id": scope.TaskID, "call_id": scope.CallID, "artifact_id": meta.ID})
	if _, err = d.handleArtifactRead(wrong); err == nil {
		t.Fatal("cross-scope artifact read succeeded")
	}
}

func TestArtifactUploadEnforcesChunkOrderDigestAndSessionScope(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	session, _ := d.store.CreateSession(workspace, "safe-edit")
	raw := append(tinyPNG(), []byte("chunked-upload")...)
	digest := sha256.Sum256(raw)
	digestHex := fmt.Sprintf("%x", digest[:])

	call := func(index int, chunk []byte, final bool) (any, error) {
		params, _ := json.Marshal(map[string]any{
			"session_id": session.SessionID, "upload_id": "upload-1", "chunk_index": index,
			"content_base64": base64.StdEncoding.EncodeToString(chunk), "final": final,
			"sha256": digestHex, "total_bytes": len(raw), "media_type": "image/png", "origin": "composer image 1",
		})
		return d.handleArtifactUpload(params)
	}
	mid := len(raw) / 2
	if _, err := call(1, raw[mid:], true); err == nil {
		t.Fatal("out-of-order first chunk was accepted")
	}
	if _, err := call(0, raw[:mid], false); err != nil {
		t.Fatal(err)
	}
	result, err := call(1, raw[mid:], true)
	if err != nil {
		t.Fatal(err)
	}
	ref := result.(MediaRef)
	if ref.ArtifactID != digestHex || ref.Bytes != int64(len(raw)) || ref.MediaType != "image/png" {
		t.Fatalf("ref=%+v", ref)
	}
	if _, _, err := d.artifacts.Read(artifact.Scope{SessionID: "sess_other"}, ref.ArtifactID); err == nil {
		t.Fatal("uploaded image escaped its session scope")
	}
	validated, err := d.validateTaskInputMedia(session.SessionID, []MediaRef{ref})
	if err != nil || len(validated) != 1 || validated[0].ArtifactID != digestHex {
		t.Fatalf("validated=%+v err=%v", validated, err)
	}
}
