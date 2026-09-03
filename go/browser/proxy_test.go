package browser

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBrowserProxyDenyByDefaultAndExactOriginAllow(t *testing.T) {
	dialed := make(chan string, 1)
	proxy := newBrowserProxyWithDial(func(_ context.Context, _, address string) (net.Conn, error) {
		dialed <- address
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			_, _ = bufio.NewReader(server).ReadByte()
		}()
		return client, nil
	})
	proxyURL, err := proxy.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	status := connectStatus(t, proxyURL, "example.com:443")
	if status != http.StatusForbidden {
		t.Fatalf("default CONNECT status = %d", status)
	}
	if err := proxy.AllowOrigins([]string{"https://example.com"}); err != nil {
		t.Fatal(err)
	}
	status = connectStatus(t, proxyURL, "example.com:443")
	if status != http.StatusOK {
		t.Fatalf("approved CONNECT status = %d", status)
	}
	select {
	case address := <-dialed:
		if address != "example.com:443" {
			t.Fatalf("dial address = %q", address)
		}
	case <-time.After(time.Second):
		t.Fatal("approved origin was not dialed")
	}
	if status := connectStatus(t, proxyURL, "other.example.com:443"); status != http.StatusForbidden {
		t.Fatalf("unapproved sibling status = %d", status)
	}
	if blocked, ok := proxy.BlockedAfter(0); !ok || blocked.Host == "" {
		t.Fatalf("blocked origin was not recorded: %+v ok=%v", blocked, ok)
	}
}

func TestBrowserProxyRejectsUnsafeOriginShapes(t *testing.T) {
	proxy := newBrowserProxyWithDial(func(context.Context, string, string) (net.Conn, error) {
		return nil, fmt.Errorf("should not dial")
	})
	for _, origin := range []string{
		"http://example.com", "https://127.0.0.1", "https://example.com/path",
		"https://example.com?query=1", "https://example.com:444",
	} {
		if err := proxy.AllowOrigins([]string{origin}); err == nil {
			t.Fatalf("unsafe origin accepted: %s", origin)
		}
	}
}

func connectStatus(t *testing.T, rawProxyURL, target string) int {
	t.Helper()
	parsed, err := url.Parse(rawProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if strings.TrimSpace(response.Status) == "" {
		t.Fatal("empty proxy response")
	}
	return response.StatusCode
}
