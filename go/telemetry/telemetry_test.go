package telemetry

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDefaultOffAndAttributedRecord(t *testing.T) {
	if err := New(nil).Metric("cost", Attribution{}, Cost{}); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	e := New(&b)
	if err := e.Metric("carina.cost", Attribution{WorkflowID: "wf", StepID: "s", Provider: "p", Model: "m"}, Cost{InputTokens: 2, USD: .1}); err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := json.Unmarshal(b.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Format != Format || r.Attributes.StepID != "s" || r.Cost.USD != .1 {
		t.Fatalf("%+v", r)
	}
}
