package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newProjectionTestProvider(t *testing.T, handler http.HandlerFunc, timeout time.Duration) *hmsRecallProvider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p, err := newHMSRecallProvider(memoryProviderHMSHybrid, srv.URL, "projection-secret", []byte(strings.Repeat("p", 32)), timeout, 4)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func projectionDesired() hmsProjectionDesired {
	return hmsProjectionDesired{
		BankID: "carina_bank", DocumentID: "entry:abc.2", Content: "User prefers concise output.",
		Timestamp: "2026-07-15T01:02:03Z", Context: "governed user memory",
		Metadata: map[string]string{"revision": "2"}, Tags: []string{"carina", "user"},
	}
}

func TestHMSProjectionRetainUsesSynchronousReplace(t *testing.T) {
	p := newProjectionTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer projection-secret" {
			t.Fatal("missing bearer credential")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/default/banks/carina_bank/memories":
			var body struct {
				Items []struct {
					Content    string            `json:"content"`
					DocumentID string            `json:"document_id"`
					UpdateMode string            `json:"update_mode"`
					Timestamp  string            `json:"timestamp"`
					Context    string            `json:"context"`
					Metadata   map[string]string `json:"metadata"`
					Tags       []string          `json:"tags"`
				}
				Async *bool `json:"async"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Async == nil || *body.Async || len(body.Items) != 1 || body.Items[0].UpdateMode != "replace" || body.Items[0].DocumentID != "entry:abc.2" || body.Items[0].Content == "" || body.Items[0].Metadata["revision"] != "2" {
				t.Fatalf("retain contract mismatch: %+v", body)
			}
			_, _ = w.Write([]byte(`{"success":true,"bank_id":"carina_bank","items_count":1,"async":false}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}, time.Second)
	if err := p.Project(context.Background(), projectionDesired()); err != nil {
		t.Fatal(err)
	}
}

func TestHMSProjectionDeleteIsIdempotentOnNotFound(t *testing.T) {
	p := newProjectionTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/default/banks/carina_bank/memories" {
			_, _ = w.Write([]byte(`{"success":true,"bank_id":"carina_bank","items_count":1,"async":false}`))
			return
		}
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/default/banks/carina_bank/documents/entry:abc.2" {
			t.Fatalf("unexpected delete: %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, `{"detail":"Document not found"}`, http.StatusNotFound)
	}, time.Second)
	d := projectionDesired()
	d.Delete, d.Content = true, ""
	if err := p.Project(context.Background(), d); err != nil {
		t.Fatalf("404 tombstone must be successful: %v", err)
	}
}

func TestHMSProjectionTypedErrors(t *testing.T) {
	t.Run("http", func(t *testing.T) {
		p := newProjectionTestProvider(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }, time.Second)
		err := p.Project(context.Background(), projectionDesired())
		var typed hmsProjectionHTTPError
		if !errors.As(err, &typed) || typed.Status != http.StatusTooManyRequests {
			t.Fatalf("expected typed HTTP error, got %T %v", err, err)
		}
	})
	t.Run("unexpected async response", func(t *testing.T) {
		const operationID = "550e8400-e29b-41d4-a716-446655440002"
		p := newProjectionTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"bank_id":"carina_bank","items_count":1,"async":true,"operation_id":"` + operationID + `"}`))
		}, time.Second)
		err := p.Project(context.Background(), projectionDesired())
		var typed hmsProjectionContractError
		if !errors.As(err, &typed) {
			t.Fatalf("expected contract rejection, got %T %v", err, err)
		}
	})
	t.Run("contract", func(t *testing.T) {
		p := newProjectionTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		}, time.Second)
		err := p.Project(context.Background(), projectionDesired())
		var typed hmsProjectionContractError
		if !errors.As(err, &typed) {
			t.Fatalf("expected typed contract error, got %T %v", err, err)
		}
	})
}

func TestHMSProjectionDeadlineAndResponseLimit(t *testing.T) {
	t.Run("projection is not capped by recall client timeout", func(t *testing.T) {
		p := newProjectionTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"bank_id":"carina_bank","items_count":1,"async":false}`))
		}, 120*time.Millisecond)
		p.projectionTimeout = time.Second
		if err := p.Project(context.Background(), projectionDesired()); err != nil {
			t.Fatalf("projection inherited recall timeout: %v", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		p := newProjectionTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"bank_id":"carina_bank","items_count":1,"async":false}`))
		}, 120*time.Millisecond)
		p.projectionTimeout = 120 * time.Millisecond
		err := p.Project(context.Background(), projectionDesired())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected overall deadline, got %v", err)
		}
	})
	t.Run("body limit", func(t *testing.T) {
		p := newProjectionTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(strings.Repeat("x", hmsMaxResponseBytes+1)))
		}, time.Second)
		err := p.Project(context.Background(), projectionDesired())
		var typed hmsProjectionContractError
		if !errors.As(err, &typed) || !strings.Contains(err.Error(), "exceeds limit") {
			t.Fatalf("expected body-limit contract error, got %v", err)
		}
	})
}

func TestHMSProjectionRejectsUnsafeInputBeforeNetwork(t *testing.T) {
	called := false
	p := newProjectionTestProvider(t, func(http.ResponseWriter, *http.Request) { called = true }, time.Second)
	d := projectionDesired()
	d.DocumentID = "../../another-bank"
	err := p.Project(context.Background(), d)
	var typed hmsProjectionContractError
	if !errors.As(err, &typed) || called {
		t.Fatalf("unsafe ID must fail locally: called=%v err=%v", called, err)
	}
}
