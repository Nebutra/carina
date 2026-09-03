package browser

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/netguard"
)

type proxyDial func(context.Context, string, string) (net.Conn, error)

type blockedOrigin struct {
	Sequence uint64
	Host     string
	Reason   string
}

type browserProxy struct {
	mu      sync.RWMutex
	allowed map[string]struct{}
	blocked blockedOrigin
	dial    proxyDial

	ln  net.Listener
	srv *http.Server
}

func newBrowserProxy() *browserProxy {
	return newBrowserProxyWithDial(netguard.DialPublicContext)
}

func newBrowserProxyWithDial(dial proxyDial) *browserProxy {
	return &browserProxy{allowed: make(map[string]struct{}), dial: dial}
}

func (p *browserProxy) Start() (string, error) {
	if p == nil || p.dial == nil {
		return "", fmt.Errorf("browser proxy dialer is required")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("browser proxy listen: %w", err)
	}
	p.ln = ln
	p.srv = &http.Server{
		Handler:           http.HandlerFunc(p.handle),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	go func() { _ = p.srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), nil
}

func (p *browserProxy) Close() error {
	if p == nil || p.srv == nil {
		return nil
	}
	return p.srv.Close()
}

func (p *browserProxy) AllowOrigins(origins []string) error {
	validated := make([]string, 0, len(origins))
	for _, raw := range origins {
		origin, err := netguard.NormalizePublicHTTPSOrigin(raw)
		if err != nil {
			return browserError(ErrorInvalidRequest, "approved browser origin is invalid", "approve an exact public HTTPS origin", false, err)
		}
		target, _ := url.Parse(origin)
		validated = append(validated, strings.ToLower(target.Hostname())+":443")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, hostport := range validated {
		p.allowed[hostport] = struct{}{}
	}
	return nil
}

func (p *browserProxy) BlockedAfter(sequence uint64) (blockedOrigin, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.blocked, p.blocked.Sequence > sequence
}

func (p *browserProxy) Sequence() uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.blocked.Sequence
}

func (p *browserProxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		p.deny(w, hostOnly(r.Host), "plain HTTP is not allowed")
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" {
		p.deny(w, hostOnly(r.Host), "only HTTPS on port 443 is allowed")
		return
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	hostport := net.JoinHostPort(host, port)
	p.mu.RLock()
	_, allowed := p.allowed[hostport]
	p.mu.RUnlock()
	if !allowed {
		p.deny(w, host, "origin is not approved")
		return
	}
	destination, err := p.dial(r.Context(), "tcp", hostport)
	if err != nil {
		p.deny(w, host, "origin did not resolve to a reachable public address")
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = destination.Close()
		http.Error(w, "proxy tunnel unavailable", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		_ = destination.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = client.Close()
		_ = destination.Close()
		return
	}
	go proxyCopy(destination, client)
	go proxyCopy(client, destination)
}

func (p *browserProxy) deny(w http.ResponseWriter, host, reason string) {
	p.mu.Lock()
	p.blocked.Sequence++
	p.blocked.Host = truncateUTF8(strings.ToLower(strings.TrimSpace(host)), 253)
	p.blocked.Reason = reason
	p.mu.Unlock()
	http.Error(w, "browser egress denied", http.StatusForbidden)
}

func proxyCopy(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

func hostOnly(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return hostport
}
