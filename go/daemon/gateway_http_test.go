package daemon_test

import (
	"bytes"
	"encoding/json"
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
	Code int
	Body string
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
	return httpTestResponse{Code: resp.StatusCode, Body: buf.String()}
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
