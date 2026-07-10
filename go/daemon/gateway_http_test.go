package daemon_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/daemon"
	"github.com/Nebutra/carina/go/rpc"
)

func TestGatewayHTTPModelsChatAndToolsInvoke(t *testing.T) {
	d, sock, httpAddr := startGatewayHTTPDaemon(t)
	defer d.Close()

	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	readWriteToken := issueGatewayHTTPToken(t, c, []string{"read", "write"}, []string{"/v1/*", "/tools/invoke"})

	resp := httpJSON(t, http.MethodGet, "http://"+httpAddr+"/v1/models", readWriteToken, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("models status %d: %s", resp.Code, resp.Body)
	}
	if !strings.Contains(resp.Body, `"id":"carina/default"`) {
		t.Fatalf("models response missing carina/default: %s", resp.Body)
	}

	ws := t.TempDir()
	chatBody := map[string]any{
		"model": "carina/default",
		"messages": []map[string]any{{
			"role": "user", "content": "hello from http gateway",
		}},
		"metadata": map[string]any{"workspace_root": ws},
	}
	resp = httpJSON(t, http.MethodPost, "http://"+httpAddr+"/v1/chat/completions", readWriteToken, chatBody)
	if resp.Code != http.StatusOK {
		t.Fatalf("chat status %d: %s", resp.Code, resp.Body)
	}
	if !strings.Contains(resp.Body, `"object":"chat.completion"`) || !strings.Contains(resp.Body, `"task_id"`) {
		t.Fatalf("chat response missing task metadata: %s", resp.Body)
	}

	resp = httpJSON(t, http.MethodPost, "http://"+httpAddr+"/tools/invoke", readWriteToken, map[string]any{
		"tool": "daemon.status",
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body, `"ok":true`) {
		t.Fatalf("tools status response %d: %s", resp.Code, resp.Body)
	}
	resp = httpJSON(t, http.MethodPost, "http://"+httpAddr+"/tools/invoke", readWriteToken, map[string]any{
		"tool": "command.exec",
	})
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body, "read-only invoke allowlist") {
		t.Fatalf("mutating tool should be denied, got %d: %s", resp.Code, resp.Body)
	}
}

func TestGatewayHTTPAuthFailures(t *testing.T) {
	d, sock, httpAddr := startGatewayHTTPDaemon(t)
	defer d.Close()
	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	toolsOnlyToken := issueGatewayHTTPToken(t, c, []string{"read"}, []string{"/tools/invoke"})
	wsToken := issueGatewayTokenTransport(t, c, []string{"read"}, []string{"/v1/models"}, "ws")

	resp := httpJSON(t, http.MethodGet, "http://"+httpAddr+"/v1/models", "", nil)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d: %s", resp.Code, resp.Body)
	}
	resp = httpJSON(t, http.MethodGet, "http://"+httpAddr+"/v1/models", toolsOnlyToken, nil)
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body, "route not granted") {
		t.Fatalf("route mismatch status = %d: %s", resp.Code, resp.Body)
	}
	resp = httpJSON(t, http.MethodGet, "http://"+httpAddr+"/v1/models", wsToken, nil)
	if resp.Code != http.StatusUnauthorized || !strings.Contains(resp.Body, "transport mismatch") {
		t.Fatalf("transport mismatch status = %d: %s", resp.Code, resp.Body)
	}
}

func TestGatewayHTTPChatCompletionStream(t *testing.T) {
	d, sock, httpAddr := startGatewayHTTPDaemon(t)
	defer d.Close()
	d.SetReasoner(gatewayStreamReasoner{})

	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	token := issueGatewayHTTPToken(t, c, []string{"write"}, []string{"/v1/chat/completions"})

	body := map[string]any{
		"model":  "carina/default",
		"stream": true,
		"messages": []map[string]any{{
			"role": "user", "content": "verify the gateway stream",
		}},
		"metadata": map[string]any{"workspace_root": t.TempDir()},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+httpAddr+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream status %d: %s", resp.StatusCode, got)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("stream content type = %q", got)
	}
	if resp.Header.Get("X-Carina-Task-ID") == "" || resp.Header.Get("X-Carina-Session-ID") == "" {
		t.Fatalf("stream response missing Carina task/session headers: %+v", resp.Header)
	}

	chunks, done := readChatCompletionStream(t, resp.Body)
	if !done {
		t.Fatal("stream did not end with data: [DONE]")
	}
	if len(chunks) < 4 {
		t.Fatalf("expected role, progress, final content, and finish chunks; got %d: %+v", len(chunks), chunks)
	}
	first := chunks[0]
	if first.Object != "chat.completion.chunk" || first.ID == "" || first.Model != "carina/default" || first.Created == 0 {
		t.Fatalf("invalid first chunk envelope: %+v", first)
	}
	if len(first.Choices) != 1 || first.Choices[0].Index != 0 || first.Choices[0].Delta.Role != "assistant" || first.Choices[0].FinishReason != nil {
		t.Fatalf("first chunk must establish assistant role: %+v", first)
	}

	var content strings.Builder
	for i, chunk := range chunks {
		if chunk.Object != "chat.completion.chunk" || chunk.ID != first.ID || chunk.Model != first.Model || chunk.Created != first.Created {
			t.Fatalf("chunk %d envelope changed within one stream: %+v", i, chunk)
		}
		if len(chunk.Choices) != 1 {
			t.Fatalf("chunk %d choices = %+v", i, chunk.Choices)
		}
		content.WriteString(chunk.Choices[0].Delta.Content)
	}
	if !strings.Contains(content.String(), "Carina task status:") || !strings.Contains(content.String(), "gateway stream complete") {
		t.Fatalf("stream must expose real progress and final result, got %q", content.String())
	}
	last := chunks[len(chunks)-1]
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "stop" {
		t.Fatalf("last chunk finish_reason = %+v", last.Choices[0].FinishReason)
	}
	if last.Choices[0].Delta.Role != "" || last.Choices[0].Delta.Content != "" {
		t.Fatalf("terminal chunk delta must be empty: %+v", last.Choices[0].Delta)
	}
}

func TestGatewayHTTPChatCompletionStreamKeepsRouteAndScopeAuthorization(t *testing.T) {
	d, sock, httpAddr := startGatewayHTTPDaemon(t)
	defer d.Close()

	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	routeDenied := issueGatewayHTTPToken(t, c, []string{"write"}, []string{"/tools/invoke"})
	scopeDenied := issueGatewayHTTPToken(t, c, []string{"read"}, []string{"/v1/chat/completions"})
	body := map[string]any{
		"model":  "carina/default",
		"stream": true,
		"messages": []map[string]any{{
			"role": "user", "content": "must not submit",
		}},
	}

	for name, tc := range map[string]struct {
		token string
		want  string
	}{
		"route": {token: routeDenied, want: "route not granted"},
		"scope": {token: scopeDenied, want: "scope not granted"},
	} {
		t.Run(name, func(t *testing.T) {
			resp := httpJSON(t, http.MethodPost, "http://"+httpAddr+"/v1/chat/completions", tc.token, body)
			if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body, tc.want) {
				t.Fatalf("authorization response %d: %s", resp.Code, resp.Body)
			}
			if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatalf("authorization failure must be JSON, got headers %+v", resp.Header)
			}
			if resp.Header.Get("X-Carina-Task-ID") != "" {
				t.Fatalf("authorization failure submitted a task: %+v", resp.Header)
			}
		})
	}
}

func TestGatewayHTTPChatCompletionStreamClientDisconnectReturnsPromptly(t *testing.T) {
	d, sock, httpAddr := startGatewayHTTPDaemon(t)
	defer d.Close()
	d.SetReasoner(gatewayStreamReasoner{delay: 500 * time.Millisecond})

	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	token := issueGatewayHTTPToken(t, c, []string{"write"}, []string{"/v1/chat/completions"})
	raw, err := json.Marshal(map[string]any{
		"model":  "carina/default",
		"stream": true,
		"messages": []map[string]any{{
			"role": "user", "content": "stay active briefly",
		}},
		"metadata": map[string]any{"workspace_root": t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+httpAddr+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		resp.Body.Close()
		t.Fatalf("read first streamed chunk: %v", err)
	}
	cancel()
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		_ = resp.Body.Close()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("stream body did not close promptly after client cancellation")
	}

	var status map[string]any
	if err := c.Call("daemon.status", map[string]any{}, &status); err != nil {
		t.Fatalf("daemon stopped serving after stream disconnect: %v", err)
	}
}

type gatewayStreamReasoner struct {
	delay time.Duration
}

func (r gatewayStreamReasoner) Name() string { return "gateway-stream-test" }

func (r gatewayStreamReasoner) Think(ctx context.Context, _ string) (string, error) {
	if r.delay > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(r.delay):
		}
	}
	return `{"tool":"done","summary":"gateway stream complete"}`, nil
}

type gatewayChatCompletionChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

func readChatCompletionStream(t *testing.T, body io.Reader) ([]gatewayChatCompletionChunk, bool) {
	t.Helper()
	var chunks []gatewayChatCompletionChunk
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return chunks, true
		}
		var chunk gatewayChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("decode SSE chunk %q: %v", data, err)
		}
		chunks = append(chunks, chunk)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read SSE stream: %v", err)
	}
	return chunks, false
}

func TestGatewayHTTPRequiresTokenSigner(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	d, err := daemon.New(daemon.Options{StateDir: t.TempDir(), KernelBin: kernelBin, ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.RunGatewayHTTP(freeTCPAddr(t), nil); err == nil || !strings.Contains(err.Error(), "gateway_token_signing_key_file") {
		t.Fatalf("gateway http without signer should fail closed, got %v", err)
	}
}

func startGatewayHTTPDaemon(t *testing.T) (*daemon.Daemon, string, string) {
	t.Helper()
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	keyFile := filepath.Join(t.TempDir(), "gateway-token.key")
	if err := os.WriteFile(keyFile, []byte("01234567890123456789012345678901\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := daemon.New(daemon.Options{
		StateDir:                   t.TempDir(),
		KernelBin:                  kernelBin,
		ToolsDir:                   filepath.Join(repoRoot, "zig/zig-out/bin"),
		GatewayTokenSigningKeyFile: keyFile,
		GatewayTokenMaxTTLSeconds:  120,
	})
	if err != nil {
		t.Fatal(err)
	}
	sock := shortSocket(t)
	go func() { _ = d.Run(sock) }()
	waitForSocket(t, sock)
	httpAddr := freeTCPAddr(t)
	go func() { _ = d.RunGatewayHTTP(httpAddr, nil) }()
	waitHTTP(t, "http://"+httpAddr+"/v1/models")
	return d, sock, httpAddr
}

func issueGatewayHTTPToken(t *testing.T, c *rpc.Client, scopes, routes []string) string {
	t.Helper()
	return issueGatewayTokenTransport(t, c, scopes, routes, "http")
}

func issueGatewayTokenTransport(t *testing.T, c *rpc.Client, scopes, routes []string, transport string) string {
	t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	if err := c.Call("gateway.token.issue", map[string]any{
		"role": "operator", "scopes": scopes, "routes": routes, "ttl_seconds": 60, "transport": transport,
	}, &out); err != nil {
		t.Fatal(err)
	}
	return out.Token
}

type httpTestResponse struct {
	Code   int
	Body   string
	Header http.Header
}

func httpJSON(t *testing.T, method, url, token string, body any) httpTestResponse {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return httpTestResponse{Code: resp.StatusCode, Body: buf.String(), Header: resp.Header.Clone()}
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("http listener did not appear: %s", url)
}
