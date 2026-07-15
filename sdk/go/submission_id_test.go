package sdk

import (
	"encoding/json"
	"testing"
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

func TestSuccessCheckCommandIsAString(t *testing.T) {
	raw, err := json.Marshal(SuccessCheck{Kind: "command_zero_exit", Command: "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"kind":"command_zero_exit","command":"go test ./..."}` {
		t.Fatalf("success check JSON = %s", raw)
	}
}
