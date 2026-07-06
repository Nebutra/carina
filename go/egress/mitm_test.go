package egress

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func echoAuthTLS() *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "auth="+r.Header.Get("Authorization"))
	}))
}

func trustedUpstreamClient(up *httptest.Server) *http.Client {
	c := up.Client()
	if tr, ok := c.Transport.(*http.Transport); ok {
		clone := tr.Clone()
		clone.Proxy = nil
		c.Transport = clone
	}
	return c
}

func mitmProxyClient(t *testing.T, proxyURL string, caPEM []byte) *http.Client {
	t.Helper()
	u, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to trust egress MITM CA")
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(u),
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}
}

func upstreamTrustProxyClient(t *testing.T, proxyURL string, up *httptest.Server) *http.Client {
	t.Helper()
	u, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	c := trustedUpstreamClient(up)
	tr := c.Transport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(u)
	c.Transport = tr
	return c
}

func TestEgressMITMInjectsCredentialForHTTPSOptIn(t *testing.T) {
	up := echoAuthTLS()
	defer up.Close()
	host := hostOnly(strings.TrimPrefix(up.URL, "https://"))

	rules := map[string]InjectionRule{
		host: {Header: "Authorization", ValuePrefix: "Bearer ", SecretName: "TOK", MITM: true},
	}
	p := NewWithInjector(Allowlist([]string{host}), NewInjector(rules, func(name string) (string, bool) {
		if name == "TOK" {
			return "s3cr3t", true
		}
		return "", false
	}))
	p.upstream = trustedUpstreamClient(up)
	proxyURL, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	code, body := getVia(t, mitmProxyClient(t, proxyURL, p.ca.CertPEM()), up.URL)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if body != "auth=Bearer s3cr3t" {
		t.Fatalf("HTTPS credential not injected at the boundary, upstream saw: %q", body)
	}
}

func TestEgressHTTPSWithoutMITMStaysOpaque(t *testing.T) {
	up := echoAuthTLS()
	defer up.Close()
	host := hostOnly(strings.TrimPrefix(up.URL, "https://"))

	rules := map[string]InjectionRule{
		host: {Header: "Authorization", ValuePrefix: "Bearer ", SecretName: "TOK"},
	}
	p := NewWithInjector(Allowlist([]string{host}), NewInjector(rules, func(string) (string, bool) {
		return "s3cr3t", true
	}))
	proxyURL, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	code, body := getVia(t, upstreamTrustProxyClient(t, proxyURL, up), up.URL)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if body != "auth=" {
		t.Fatalf("HTTPS without MITM must remain opaque and unauthenticated, upstream saw: %q", body)
	}
}

func TestEgressHTTPSMITMDeniedBeforeTLSAndSecret(t *testing.T) {
	const host = "api.example.test"
	p := NewWithInjector(Allowlist(nil), NewInjector(map[string]InjectionRule{
		host: {Header: "Authorization", ValuePrefix: "Bearer ", SecretName: "TOK", MITM: true},
	}, func(string) (string, bool) {
		return "s3cr3t", true
	}))
	proxyURL, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	u, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\n\r\n", host, host); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied CONNECT should be 403 before TLS, got %d", resp.StatusCode)
	}
	if strings.Contains(string(body), "s3cr3t") {
		t.Fatal("denial response must never contain the secret value")
	}
}

func TestMITMCABundleWrittenPrivate(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "egress-ca-bundle.pem")
	if err := ca.WriteBundleFile(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("CA bundle permissions = %o, want 0600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, ca.CertPEM()) {
		t.Fatal("CA bundle must include the egress CA certificate")
	}
}
