package sdk

import "testing"

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
