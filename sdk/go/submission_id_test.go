package sdk

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"testing"

	"github.com/Nebutra/carina/go/rpc"
)

func TestTaskSubmitParamsIncludeOptionalIdempotencyKey(t *testing.T) {
	without := taskSubmitParams("sess_1", "work", "")
	if _, ok := without["client_submission_id"]; ok {
		t.Fatal("empty idempotency key must be omitted")
	}
	with := taskSubmitParams("sess_1", "work", "sdk_request_1")
	if with["client_submission_id"] != "sdk_request_1" {
		t.Fatalf("idempotency key = %#v", with["client_submission_id"])
	}
}

func TestTaskSubmitParamsIncludeMediaRefs(t *testing.T) {
	ref := MediaRef{ArtifactID: "abc", MediaType: "image/png", Bytes: 3, Origin: "paste"}
	params := taskSubmitParamsWithMedia("sess_1", "work", "sdk_request_1", []MediaRef{ref})
	refs, ok := params["input_media_refs"].([]MediaRef)
	if !ok || len(refs) != 1 || refs[0] != ref {
		t.Fatalf("input media refs = %#v", params["input_media_refs"])
	}
	without := taskSubmitParamsWithMedia("sess_1", "work", "", nil)
	if _, ok := without["input_media_refs"]; ok {
		t.Fatal("empty input media refs must be omitted")
	}
}

func TestUploadArtifactChunksAndVerifiesResult(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := NewClient(rpc.NewClient(clientConn, clientConn, clientConn))
	defer client.Close()

	content := bytes.Repeat([]byte("x"), artifactUploadChunkSize+7)
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(serverConn)
		for index := 0; index < 2; index++ {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				done <- err
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					ChunkIndex    int    `json:"chunk_index"`
					ContentBase64 string `json:"content_base64"`
					Final         bool   `json:"final"`
					SHA256        string `json:"sha256"`
				} `json:"params"`
			}
			if err := json.Unmarshal(line, &request); err != nil {
				done <- err
				return
			}
			chunk, err := base64.StdEncoding.DecodeString(request.Params.ContentBase64)
			if err != nil || request.Method != "artifact.upload" || request.Params.ChunkIndex != index || request.Params.SHA256 != digest {
				done <- fmt.Errorf("unexpected upload request %d: method=%s params=%+v decode=%v", index, request.Method, request.Params, err)
				return
			}
			if index == 0 && (request.Params.Final || len(chunk) != artifactUploadChunkSize) {
				done <- fmt.Errorf("first chunk final=%v bytes=%d", request.Params.Final, len(chunk))
				return
			}
			if index == 1 && (!request.Params.Final || len(chunk) != 7) {
				done <- fmt.Errorf("final chunk final=%v bytes=%d", request.Params.Final, len(chunk))
				return
			}
			result := any(map[string]any{"upload_id": "upload_1", "next_chunk_index": 1})
			if index == 1 {
				result = map[string]any{"artifact_id": digest, "media_type": "image/png", "bytes": len(content), "origin": "paste"}
			}
			response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			if _, err := serverConn.Write(append(response, '\n')); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	ref, err := client.UploadArtifact("sess_1", "upload_1", "image/png", "paste", content)
	if err != nil || ref.ArtifactID != digest || ref.Bytes != int64(len(content)) || ref.Origin != "paste" {
		t.Fatalf("upload ref=%+v err=%v", ref, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSuccessCheckCommandIsAString(t *testing.T) {
	raw, err := json.Marshal(SuccessCheck{Kind: "command_zero_exit", Command: "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"kind":"command_zero_exit","command":"go test ./..."}` {
		t.Fatalf("success check JSON = %s", raw)
	}
}
