package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const webSearchFixtureHTML = `<!doctype html><html><body>
<div class="result">
<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fdocs.example.com%2Fapi">Example API docs</a>
<a class="result__snippet">Official HTTP API reference for Example.</a>
</div>
<div class="result">
<a class="result__a" href="https://html.duckduckgo.com/l/?uddg=https%3A%2F%2Fgithub.com%2Fexample%2Fsdk">Example SDK</a>
<div class="result__snippet">Source and install notes.</div>
</div>
<div class="result">
<a class="result__a" href="javascript:alert(1)">Ignore me</a>
</div>
</body></html>`

func TestParseWebSearchHitsUnwrapsPublicHTTPSResults(t *testing.T) {
	hits := parseWebSearchHits(webSearchFixtureHTML)
	if len(hits) != 2 {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].URL != "https://docs.example.com/api" || hits[0].Title != "Example API docs" {
		t.Fatalf("first hit = %+v", hits[0])
	}
	if hits[0].Snippet != "Official HTTP API reference for Example." {
		t.Fatalf("snippet = %q", hits[0].Snippet)
	}
	if hits[1].URL != "https://github.com/example/sdk" {
		t.Fatalf("second hit = %+v", hits[1])
	}
}

func TestWebSearchWaitsForHostApprovalAndTreatsResultsAsUntrusted(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	if err := d.SetApprovalMode("ask"); err != nil {
		t.Fatal(err)
	}
	requests := permissionRequests(d)
	var hits atomic.Int32
	d.webFetchHTTP = &http.Client{Transport: webFetchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		hits.Add(1)
		if req.Method != http.MethodGet || req.URL.Hostname() != webSearchHost {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		if req.URL.Query().Get("q") != "example api" {
			t.Fatalf("query = %q", req.URL.Query().Get("q"))
		}
		return webFetchResponse(req, http.StatusOK, "text/html; charset=utf-8", webSearchFixtureHTML), nil
	})}

	sess, err := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "on_request", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "docs")
	result := make(chan toolExecutionOutcome, 1)
	go func() {
		_, outcome := d.executeActionOutcome(sess, task, &action{
			Tool:   "web.search",
			Query:  "example api",
			Intent: "Find public API docs",
		})
		result <- outcome
	}()

	var decisionID string
	select {
	case decisionID = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("web.search did not request host approval")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("network request started before approval: hits=%d", got)
	}
	if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{
		"decision_id": decisionID,
		"approve":     true,
	})); err != nil {
		t.Fatal(err)
	}

	select {
	case outcome := <-result:
		if outcome.status != "completed" || !strings.Contains(outcome.display, "https://docs.example.com/api") {
			t.Fatalf("unexpected web.search outcome: %+v", outcome)
		}
		if !strings.Contains(outcome.display, "untrusted external content") {
			t.Fatalf("search data must carry an injection boundary: %q", outcome.display)
		}
		if strings.Contains(outcome.display, "javascript:") {
			t.Fatal("non-HTTPS result leaked into the observation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approved web.search did not complete")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("approved web.search should execute exactly once: hits=%d", got)
	}

	raw, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "example api") && strings.Contains(string(raw), "NetworkRequested") {
		// query must not live on the network audit event
		var events []itemAuditEvent
		if err := json.Unmarshal(raw, &events); err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Type != "NetworkRequested" {
				continue
			}
			if event.Payload["host"] != webSearchHost || event.Payload["method"] != http.MethodGet {
				t.Fatalf("network audit = %+v", event.Payload)
			}
			encoded, _ := json.Marshal(event.Payload)
			if strings.Contains(string(encoded), "example api") {
				t.Fatalf("NetworkRequested leaked query: %s", encoded)
			}
		}
	}
}

func TestWebSearchRejectsEmptyQueryAndAnomalyPages(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	d.webFetchHTTP = &http.Client{Transport: webFetchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return webFetchResponse(req, http.StatusOK, "text/html", `<div id="anomaly-modal">challenge</div>`), nil
	})}
	sess, err := d.store.CreateSessionMode(workspace, "safe-edit", "never")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "never", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "docs")
	if _, outcome := d.executeActionOutcome(sess, task, &action{Tool: "web.search", Query: "   ", Intent: "noop"}); outcome.status != "failed" {
		t.Fatalf("empty query = %+v", outcome)
	}
	if _, outcome := d.executeActionOutcome(sess, task, &action{Tool: "web.search", Query: "example", Intent: "Find docs"}); outcome.status != "failed" || !strings.Contains(outcome.display, "challenged") {
		t.Fatalf("anomaly = %+v", outcome)
	}
}

func TestWebSearchBlockedInPlanMode(t *testing.T) {
	if !planModeBlocksTool("web.search") {
		t.Fatal("web.search is outbound network and must stay blocked in plan mode")
	}
}
