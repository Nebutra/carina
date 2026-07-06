// Package egress is a loopback forward proxy that turns network access into a
// gated capability: every outbound connection (plain HTTP or HTTPS via CONNECT)
// is checked against a Gate before it is allowed, deny-by-default. The daemon
// injects HTTP(S)_PROXY into command children so agent-run network egress flows
// through here and is subject to the capability kernel's NetworkAccess policy.
package egress

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// Gate decides whether a host may be reached; the reason is surfaced to the
// caller on denial.
type Gate func(host string) (allow bool, reason string)

// Proxy is a running loopback egress proxy.
type Proxy struct {
	mu       sync.RWMutex // guards gate (live-swappable via SetGate for hot-reload)
	gate     Gate
	inj      *Injector // optional per-host credential injection (nil = none)
	ca       *CA       // ephemeral MITM CA, generated only when a rule opts in
	upstream *http.Client
	ln       net.Listener
	srv      *http.Server
}

// SetGate swaps the allow/deny gate live (e.g. on a config reload of the egress
// allowlist) without restarting the listener.
func (p *Proxy) SetGate(g Gate) {
	p.mu.Lock()
	p.gate = g
	p.mu.Unlock()
}

func New(gate Gate) *Proxy { return &Proxy{gate: gate} }

// NewWithInjector builds a proxy that also injects per-host credentials at the
// boundary. HTTPS injection only happens for rules that explicitly opt into
// MITM; all other HTTPS CONNECT traffic remains an opaque tunnel.
func NewWithInjector(gate Gate, inj *Injector) *Proxy { return &Proxy{gate: gate, inj: inj} }

// Start binds a loopback port and serves; returns the proxy URL (http://host:port).
func (p *Proxy) Start() (string, error) {
	if p.inj.hasMITM() && p.ca == nil {
		ca, err := NewCA()
		if err != nil {
			return "", fmt.Errorf("egress mitm ca: %w", err)
		}
		p.ca = ca
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	p.ln = ln
	p.srv = &http.Server{Handler: http.HandlerFunc(p.handle)}
	go func() { _ = p.srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), nil
}

func (p *Proxy) Addr() string {
	if p.ln == nil {
		return ""
	}
	return p.ln.Addr().String()
}

func (p *Proxy) Close() error {
	if p.srv != nil {
		return p.srv.Close()
	}
	return nil
}

// MITMEnabled reports whether this proxy has any host opted into HTTPS
// interception.
func (p *Proxy) MITMEnabled() bool { return p.inj.hasMITM() }

// WriteMITMCABundleFile writes the process-local trust bundle children should
// use when MITM is enabled. The bundle keeps normal system roots where they can
// be discovered and appends Carina's ephemeral CA; it never modifies system
// trust.
func (p *Proxy) WriteMITMCABundleFile(path string) error {
	if p.ca == nil {
		return fmt.Errorf("egress mitm ca not initialized")
	}
	return p.ca.WriteBundleFile(path)
}

func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	gate := p.gate
	p.mu.RUnlock()
	if r.Method == http.MethodConnect {
		host := hostOnly(r.Host)
		if ok, reason := gate(host); !ok {
			http.Error(w, "egress denied: "+reason, http.StatusForbidden)
			return
		}
		if p.inj.mitm(host) {
			p.mitmConnect(w, r)
			return
		}
		p.tunnel(w, r)
		return
	}
	host := hostOnly(r.URL.Host)
	if host == "" {
		host = hostOnly(r.Host)
	}
	if ok, reason := gate(host); !ok {
		http.Error(w, "egress denied: "+reason, http.StatusForbidden)
		return
	}
	p.forward(w, r, host)
}

// tunnel establishes a CONNECT tunnel (for HTTPS) after the gate allows it.
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	dest, err := net.DialTimeout("tcp", r.Host, 15*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		dest.Close()
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		dest.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	go func() { _, _ = io.Copy(dest, client); dest.Close() }()
	go func() { _, _ = io.Copy(client, dest); client.Close() }()
}

// forward proxies a plain-HTTP request after the gate allows it, injecting a
// per-host credential at the boundary if configured. HTTPS requests normally
// flow through the opaque CONNECT tunnel; only per-host MITM opt-in rules are
// terminated for credential injection.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, host string) {
	if p.inj != nil {
		p.inj.apply(host, r.Header)
	}
	r.RequestURI = ""
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func hostOnly(hostport string) string {
	if hostport == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// Allowlist builds a Gate from an exact host allowlist (deny-by-default).
func Allowlist(hosts []string) Gate {
	set := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		set[h] = true
	}
	return func(host string) (bool, string) {
		if set[host] {
			return true, ""
		}
		return false, fmt.Sprintf("host %q not on egress allowlist", host)
	}
}
