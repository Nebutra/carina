package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type webFetchRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn webFetchRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func webFetchResponse(req *http.Request, status int, contentType, body string) *http.Response {
	header := make(http.Header)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestWebFetchWaitsForHostApprovalAndRedactsURL(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	if err := d.SetApprovalMode("ask"); err != nil {
		t.Fatal(err)
	}
	requests := permissionRequests(d)
	var hits atomic.Int32
	d.webFetchHTTP = &http.Client{Transport: webFetchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		hits.Add(1)
		if req.Method != http.MethodGet || req.URL.Hostname() != "weather.example" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		return webFetchResponse(req, http.StatusOK, "application/json", `{"temperature_c":26}`), nil
	})}

	sess, err := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "on_request", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "weather")
	result := make(chan toolExecutionOutcome, 1)
	go func() {
		_, outcome := d.executeActionOutcome(sess, task, &action{
			Tool:   "web.fetch",
			URL:    "https://weather.example/current?location=secret-location",
			Intent: "Fetch current weather",
		})
		result <- outcome
	}()

	var decisionID string
	select {
	case decisionID = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("web.fetch did not request host approval")
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
		if outcome.status != "completed" || !strings.Contains(outcome.display, `"temperature_c":26`) {
			t.Fatalf("unexpected web.fetch outcome: %+v", outcome)
		}
		if !strings.Contains(outcome.display, "untrusted external content") {
			t.Fatalf("fetched data must carry an injection boundary: %q", outcome.display)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approved web.fetch did not complete")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("approved web.fetch should execute exactly once: hits=%d", got)
	}

	raw, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-location") || strings.Contains(string(raw), "/current") {
		t.Fatalf("tool lifecycle must not persist URL path or query: %s", raw)
	}
	var events []itemAuditEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	foundNetworkRequest := false
	for _, event := range events {
		if event.Type == "NetworkRequested" {
			foundNetworkRequest = event.Payload["host"] == "weather.example" && event.Payload["method"] == http.MethodGet
		}
	}
	if !foundNetworkRequest {
		t.Fatal("missing redacted NetworkRequested audit event")
	}
}

func TestNormalizeWebFetchURLRejectsUnsafeTargets(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/weather",
		"https://localhost/weather",
		"https://api.localhost/weather",
		"https://127.0.0.1/weather",
		"https://169.254.169.254/latest/meta-data",
		"https://[::1]/weather",
		"https://user:secret@example.com/weather",
		"https://example.com:8443/weather",
		"https://example.com/weather#fragment",
		"https://-bad.example/weather",
		"https://metadata/weather",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := normalizeWebFetchURL(raw); err == nil {
				t.Fatalf("unsafe target accepted: %q", raw)
			}
		})
	}
	target, err := normalizeWebFetchURL("HTTPS://Weather.Example.:443/current?q=Beijing")
	if err != nil {
		t.Fatal(err)
	}
	if got := target.String(); got != "https://weather.example/current?q=Beijing" {
		t.Fatalf("unexpected canonical URL: %q", got)
	}
}

func TestWebFetchActionAuditRetainsOnlyHost(t *testing.T) {
	raw := `{"tool":"web.fetch","url":"https://weather.example/current?location=secret-location","intent":"Fetch current weather"}`
	got := sanitizeModelResponseForAudit(raw)
	if !strings.Contains(got, `"host":"weather.example"`) || !strings.Contains(got, `"url":"[redacted]"`) {
		t.Fatalf("missing host-only web.fetch audit projection: %s", got)
	}
	if strings.Contains(got, "secret-location") || strings.Contains(got, "/current") {
		t.Fatalf("web.fetch audit leaked URL path or query: %s", got)
	}
}

func TestWebFetchRejectsRedirectBinaryAndOversizedResponses(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	d.webFetchHTTP = &http.Client{Transport: webFetchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/redirect":
			response := webFetchResponse(req, http.StatusFound, "text/plain", "redirect")
			response.Header.Set("Location", "https://other.example/")
			return response, nil
		case "/binary":
			return webFetchResponse(req, http.StatusOK, "application/octet-stream", "binary"), nil
		case "/large":
			return webFetchResponse(req, http.StatusOK, "text/plain", strings.Repeat("x", webFetchMaxBody+1)), nil
		case "/invalid":
			return webFetchResponse(req, http.StatusOK, "text/plain", string([]byte{0xff, 0xfe})), nil
		default:
			return webFetchResponse(req, http.StatusOK, "application/problem+json; charset=utf-8", `{"ok":true}`), nil
		}
	})}
	sess, err := d.store.CreateSessionMode(workspace, "safe-edit", "never")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "never", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "fetch contracts")

	for path, category := range map[string]string{
		"/redirect": "redirect_refused",
		"/binary":   "unsupported_media_type",
		"/large":    "response_too_large",
		"/invalid":  "invalid_text",
	} {
		outcome := d.agentWebFetchOutcome(sess, task, "https://weather.example"+path)
		if outcome.status != "failed" || outcome.errorCategory != category {
			t.Fatalf("%s: outcome=%+v", path, outcome)
		}
	}
	if outcome := d.agentWebFetchOutcome(sess, task, "https://weather.example/ok"); outcome.status != "completed" {
		t.Fatalf("textual +json should pass: %+v", outcome)
	}
}

func TestWebFetchDialRejectsPrivateDNSResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if _, err := publicWebFetchDialContext(ctx, "tcp", "localhost:443"); err == nil {
		t.Fatal("localhost DNS resolution must not reach the network")
	}
}

func TestPublicWebFetchIPRejectsNonPublicSpecialRanges(t *testing.T) {
	for _, raw := range []string{
		"0.1.2.3",
		"10.0.0.1",
		"100.64.0.1",
		"127.0.0.1",
		"169.254.169.254",
		"192.0.2.1",
		"198.18.0.1",
		"198.51.100.1",
		"203.0.113.1",
		"240.0.0.1",
		"::1",
		"64:ff9b::a00:1",
		"100::1",
		"2001:db8::1",
		"2002:a00:1::",
		"3fff::1",
		"fc00::1",
		"fe80::1",
	} {
		if publicWebFetchIP(net.ParseIP(raw)) {
			t.Fatalf("non-public address accepted: %s", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicWebFetchIP(net.ParseIP(raw)) {
			t.Fatalf("public address rejected: %s", raw)
		}
	}
}
