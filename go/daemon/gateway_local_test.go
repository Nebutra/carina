package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func TestGatewayLocalEnsureIsIdempotentAndConcurrent(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()

	const callers = 12
	urls := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := d.handleGatewayLocalEnsure(testMustRaw(map[string]any{"origin": "http://127.0.0.1:4173"}))
			if err != nil {
				errs <- err
				return
			}
			body := result.(map[string]any)
			urls <- body["gateway_url"].(string)
		}()
	}
	wg.Wait()
	close(urls)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var want string
	for got := range urls {
		if want == "" {
			want = got
		}
		if got != want {
			t.Fatalf("concurrent ensure returned different URLs: %q vs %q", want, got)
		}
	}
	if !strings.HasPrefix(want, "ws://127.0.0.1:") || !strings.HasSuffix(want, "/gateway") {
		t.Fatalf("unexpected gateway URL %q", want)
	}
	if _, err := d.handleGatewayLocalEnsure(testMustRaw(map[string]any{"origin": "http://127.0.0.1:4174"})); err == nil {
		t.Fatal("changing origin after ensure must fail closed")
	}

	// The owner-only descriptor must remain unreachable from a remote origin,
	// even when a caller presents an otherwise valid admin token.
	for _, descriptor := range d.server.MethodDescriptors() {
		if descriptor.Method == "gateway.local.ensure" && descriptor.Remote {
			t.Fatal("gateway.local.ensure must be owner-only")
		}
	}
}

func TestGatewayLocalEnsureCloseReleasesListenerAndIssuesBinding(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	result, err := d.handleGatewayLocalEnsure(testMustRaw(map[string]any{"origin": "tauri://localhost"}))
	if err != nil {
		t.Fatal(err)
	}
	body := result.(map[string]any)
	urlValue := body["gateway_url"].(string)
	addr := strings.TrimSuffix(strings.TrimPrefix(urlValue, "ws://"), "/gateway")

	created, err := d.handleSessionCreate(testMustRaw(map[string]any{"workspace_root": workspace, "tenant_id": "tenant_a", "profile": "safe-edit"}))
	if err != nil {
		t.Fatal(err)
	}
	sess := created.(*sessionstore.Session)
	issued, err := d.handleGatewayTokenIssue(testMustRaw(map[string]any{
		"role": "observer", "scopes": []string{"read", "stream"}, "transport": "ws", "tenant_id": "tenant_a", "session_id": sess.SessionID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	claims := issued.(map[string]any)["claims"].(rpc.GatewayTokenClaims)
	if claims.TenantID != "tenant_a" || claims.SessionID != sess.SessionID {
		t.Fatalf("issued claims lost binding: %+v", claims)
	}
	if _, err := d.handleGatewayTokenIssue(testMustRaw(map[string]any{"role": "observer", "scopes": []string{"read"}, "transport": "ws", "tenant_id": "tenant_b", "session_id": sess.SessionID})); err == nil {
		t.Fatal("cross-tenant binding must fail")
	}

	_ = d.Close()
	if conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatalf("local gateway listener %s remained reachable after daemon close", addr)
	}
}

func testMustRaw(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal test params: %v", err))
	}
	return raw
}
