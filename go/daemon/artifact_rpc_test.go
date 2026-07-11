package daemon

import (
	"encoding/base64"
	"encoding/json"
	"github.com/Nebutra/carina/go/artifact"
	"testing"
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
