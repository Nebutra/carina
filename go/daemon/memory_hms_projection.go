package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const hmsProjectionPollInterval = 200 * time.Millisecond

// hmsProjectionDesired is one complete desired document state. A tombstone is
// represented by Delete; retained documents always replace the prior HMS state.
type hmsProjectionDesired struct {
	BankID     string
	DocumentID string
	Content    string
	Timestamp  string
	Context    string
	Metadata   map[string]string
	Tags       []string
	Delete     bool
}

type hmsProjectionExecutor interface {
	Project(context.Context, hmsProjectionDesired) error
}

type hmsProjectionHTTPError struct{ Status int }

func (e hmsProjectionHTTPError) Error() string {
	return fmt.Sprintf("memory hms projection status %d", e.Status)
}

type hmsProjectionContractError struct{ Reason string }

func (e hmsProjectionContractError) Error() string {
	return "memory hms projection contract: " + e.Reason
}

type hmsProjectionOperationError struct {
	OperationID string
	Status      string
}

// memoryProjectionAmbiguousError means HMS may have committed the side effect,
// but Carina could not validate the response. Blind retries could reorder a
// later generation or resurrect a tombstoned document.
type memoryProjectionAmbiguousError struct{ err error }

func (e memoryProjectionAmbiguousError) Error() string {
	return "memory hms projection outcome is ambiguous"
}
func (e memoryProjectionAmbiguousError) Unwrap() error { return e.err }

func (e hmsProjectionOperationError) Error() string {
	return fmt.Sprintf("memory hms projection operation %s ended with status %s", e.OperationID, e.Status)
}

func (p *hmsRecallProvider) Project(ctx context.Context, desired hmsProjectionDesired) error {
	if err := validateHMSProjectionDesired(desired); err != nil {
		return err
	}
	projectionTimeout := p.projectionTimeout
	if projectionTimeout <= 0 {
		projectionTimeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, projectionTimeout)
	defer cancel()
	if desired.Delete {
		// HMS serializes retain for a document and skips stale requests. Complete
		// a synchronous marker replace before DELETE so every earlier ambiguous
		// retain is terminal and cannot resurrect the document after deletion.
		fence := desired
		fence.Delete = false
		fence.Content = `{"version":1,"entries":[],"tombstone":true}`
		if err := p.retainProjectedDocument(ctx, fence); err != nil {
			return err
		}
		return p.deleteProjectedDocument(ctx, desired.BankID, desired.DocumentID)
	}
	return p.retainProjectedDocument(ctx, desired)
}

func validateHMSProjectionDesired(d hmsProjectionDesired) error {
	if !safeHMSProjectionID(d.BankID) || !safeHMSProjectionID(d.DocumentID) {
		return hmsProjectionContractError{Reason: "invalid bank or document ID"}
	}
	if d.Delete {
		return nil
	}
	if strings.TrimSpace(d.Content) == "" || len(d.Content) > 1<<20 {
		return hmsProjectionContractError{Reason: "content is empty or exceeds limit"}
	}
	if d.Timestamp != "" {
		if _, err := time.Parse(time.RFC3339, d.Timestamp); err != nil {
			return hmsProjectionContractError{Reason: "timestamp is not RFC3339"}
		}
	}
	return nil
}

func safeHMSProjectionID(s string) bool {
	if s == "" || len(s) > 512 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func (p *hmsRecallProvider) retainProjectedDocument(ctx context.Context, d hmsProjectionDesired) error {
	item := map[string]any{"content": d.Content, "document_id": d.DocumentID, "update_mode": "replace"}
	if d.Timestamp != "" {
		item["timestamp"] = d.Timestamp
	}
	if d.Context != "" {
		item["context"] = d.Context
	}
	if len(d.Metadata) != 0 {
		item["metadata"] = d.Metadata
	}
	if len(d.Tags) != 0 {
		item["tags"] = d.Tags
	}
	// HMS has no conditional revision fence for async operations. A synchronous
	// replace prevents an older accepted operation from completing after a newer
	// generation or tombstone.
	raw, err := json.Marshal(map[string]any{"items": []any{item}, "async": false})
	if err != nil {
		return hmsProjectionContractError{Reason: "cannot encode retain request"}
	}
	response, err := p.projectionJSON(ctx, http.MethodPost, projectionPath(d.BankID, "memories"), raw, http.StatusOK)
	if err != nil {
		var httpErr hmsProjectionHTTPError
		if errors.As(err, &httpErr) && projectionHTTPKnownNotApplied(httpErr.Status) {
			return err
		}
		return memoryProjectionAmbiguousError{err: err}
	}
	var out struct {
		Success      *bool    `json:"success"`
		BankID       string   `json:"bank_id"`
		ItemsCount   *int     `json:"items_count"`
		Async        *bool    `json:"async"`
		OperationID  string   `json:"operation_id"`
		OperationIDs []string `json:"operation_ids"`
	}
	if json.Unmarshal(response, &out) != nil || out.Success == nil || !*out.Success || out.Async == nil || *out.Async || out.ItemsCount == nil || *out.ItemsCount != 1 || out.BankID != d.BankID {
		return memoryProjectionAmbiguousError{err: hmsProjectionContractError{Reason: "invalid synchronous retain response"}}
	}
	if out.OperationID != "" || len(out.OperationIDs) != 0 {
		return memoryProjectionAmbiguousError{err: hmsProjectionContractError{Reason: "synchronous retain returned operation IDs"}}
	}
	return nil
}

func validHMSOperationID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func (p *hmsRecallProvider) waitForProjectionOperation(ctx context.Context, bankID, operationID string) error {
	for {
		response, err := p.projectionJSON(ctx, http.MethodGet, projectionPath(bankID, "operations", operationID), nil, http.StatusOK)
		if err != nil {
			return err
		}
		var out struct {
			OperationID string `json:"operation_id"`
			Status      string `json:"status"`
		}
		if json.Unmarshal(response, &out) != nil || out.OperationID != operationID {
			return hmsProjectionContractError{Reason: "invalid operation response"}
		}
		switch out.Status {
		case "completed":
			return nil
		case "failed", "cancelled", "not_found":
			return hmsProjectionOperationError{OperationID: operationID, Status: out.Status}
		case "pending", "processing":
		case "":
			return hmsProjectionContractError{Reason: "operation response has no status"}
		default:
			return hmsProjectionContractError{Reason: "operation response has unknown status"}
		}
		timer := time.NewTimer(hmsProjectionPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (p *hmsRecallProvider) deleteProjectedDocument(ctx context.Context, bankID, documentID string) error {
	raw, err := p.projectionJSON(ctx, http.MethodDelete, projectionPath(bankID, "documents", documentID), nil, http.StatusOK, http.StatusNotFound)
	if err != nil {
		var httpErr hmsProjectionHTTPError
		if errors.As(err, &httpErr) && projectionHTTPKnownNotApplied(httpErr.Status) {
			return err
		}
		return memoryProjectionAmbiguousError{err: err}
	}
	if raw == nil {
		return nil
	}
	var out struct {
		Success    *bool  `json:"success"`
		DocumentID string `json:"document_id"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Success == nil || !*out.Success || out.DocumentID != documentID {
		return memoryProjectionAmbiguousError{err: hmsProjectionContractError{Reason: "invalid delete response"}}
	}
	return nil
}

func projectionHTTPKnownNotApplied(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

func projectionPath(bankID string, parts ...string) string {
	path := "/v1/default/banks/" + url.PathEscape(bankID)
	for _, part := range parts {
		path += "/" + url.PathEscape(part)
	}
	return path
}

func (p *hmsRecallProvider) projectionJSON(ctx context.Context, method, path string, body []byte, accepted ...int) ([]byte, error) {
	u := *p.endpoint
	u.Path = strings.TrimRight(u.Path, "/") + path
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, hmsProjectionContractError{Reason: "cannot build request"}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	client := *p.client
	// Recall uses a short client timeout. Projection has its own longer context
	// deadline because synchronous extraction may legitimately take minutes.
	client.Timeout = 0
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("memory hms projection request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, hmsMaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("memory hms projection response: %w", err)
	}
	if len(raw) > hmsMaxResponseBytes {
		return nil, hmsProjectionContractError{Reason: "response exceeds limit"}
	}
	statusAccepted := false
	for _, status := range accepted {
		statusAccepted = statusAccepted || resp.StatusCode == status
	}
	if !statusAccepted {
		return nil, hmsProjectionHTTPError{Status: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		return nil, hmsProjectionContractError{Reason: "response is not JSON"}
	}
	return raw, nil
}

var _ hmsProjectionExecutor = (*hmsRecallProvider)(nil)
var _ error = hmsProjectionHTTPError{}
var _ error = hmsProjectionContractError{}
var _ error = hmsProjectionOperationError{}

type hmsOutboxExecutor struct{ provider *hmsRecallProvider }

func (e hmsOutboxExecutor) Put(ctx context.Context, intent memoryProjectionIntent) error {
	return e.execute(ctx, intent, false)
}

func (e hmsOutboxExecutor) Delete(ctx context.Context, intent memoryProjectionIntent) error {
	return e.execute(ctx, intent, true)
}

func (e hmsOutboxExecutor) execute(ctx context.Context, intent memoryProjectionIntent, deleteDocument bool) error {
	if e.provider == nil {
		return permanentMemoryProjectionError(errors.New("HMS projection provider is unavailable"))
	}
	err := e.provider.Project(ctx, hmsProjectionDesired{
		BankID: intent.BankID, DocumentID: intent.DocumentID, Content: intent.Content,
		Context: "Carina governed memory desired state", Delete: deleteDocument,
		Metadata: map[string]string{"revision": intent.Revision, "generation": fmt.Sprint(intent.Generation)},
		Tags:     []string{"carina", intent.Target},
	})
	if err == nil {
		return nil
	}
	var contract hmsProjectionContractError
	if errors.As(err, &contract) {
		return permanentMemoryProjectionError(err)
	}
	var httpErr hmsProjectionHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Status {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity:
			return permanentMemoryProjectionError(err)
		}
	}
	return err
}

var _ memoryProjectionExecutor = hmsOutboxExecutor{}
