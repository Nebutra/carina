package egress

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// CA is an ephemeral certificate authority the egress proxy uses to terminate
// TLS for MITM-opted-in hosts. The private key is generated per daemon run and
// held only in memory (never written to disk); only the public CA cert is
// exported for children to trust. It is a SEPARATE trust anchor; it is never
// added to the system root store, so it only affects processes explicitly given
// the CA-bundle env.
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte

	mu     sync.Mutex
	leaves map[string]*tls.Certificate // host -> minted leaf cert (cached)
}

// NewCA generates a fresh short-lived in-memory CA.
func NewCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Carina Egress CA", Organization: []string{"Carina"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		leaves:  map[string]*tls.Certificate{},
	}, nil
}

// CertPEM returns the public CA certificate (safe to distribute to children).
func (c *CA) CertPEM() []byte { return c.certPEM }

// WriteCertFile writes the public CA cert (0600) so children can trust it.
func (c *CA) WriteCertFile(path string) error {
	return writePrivatePEMFile(path, c.certPEM)
}

// WriteBundleFile writes a process-local CA bundle (0600) with Carina's
// ephemeral CA appended. If a platform CA bundle is discoverable, it is kept so
// non-MITM transparent CONNECTs still validate ordinary public TLS chains.
func (c *CA) WriteBundleFile(path string) error {
	var out []byte
	if sys := readSystemCABundle(); len(sys) > 0 {
		out = append(out, sys...)
		if !bytes.HasSuffix(out, []byte("\n")) {
			out = append(out, '\n')
		}
	}
	out = append(out, c.certPEM...)
	return writePrivatePEMFile(path, out)
}

func writePrivatePEMFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func readSystemCABundle() []byte {
	for _, path := range []string{
		"/etc/ssl/cert.pem",                                 // macOS, Alpine
		"/etc/ssl/certs/ca-certificates.crt",                // Debian/Ubuntu/Gentoo
		"/etc/pki/tls/certs/ca-bundle.crt",                  // Fedora/RHEL
		"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // CentOS/RHEL
		"/etc/ssl/ca-bundle.pem",                            // OpenSUSE
		"/usr/local/etc/openssl@3/cert.pem",                 // Homebrew OpenSSL
		"/usr/local/etc/openssl/cert.pem",                   // older Homebrew OpenSSL
	} {
		data, err := os.ReadFile(path)
		if err == nil && bytes.Contains(data, []byte("BEGIN CERTIFICATE")) {
			return data
		}
	}
	return nil
}

// leafFor mints (and caches) a leaf certificate for host, signed by the CA.
func (c *CA) leafFor(host string) (*tls.Certificate, error) {
	if host == "" {
		return nil, fmt.Errorf("egress mitm: empty host for cert")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if lc, ok := c.leaves[host]; ok {
		return lc, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	lc := &tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key}
	c.leaves[host] = lc
	return lc, nil
}

// mitmTunnel terminates the client's TLS with a per-host minted cert, injects
// the credential, and re-originates a VERIFIED TLS request to the real upstream.
// Only reached for hosts explicitly opted into MITM.
func (p *Proxy) mitmTunnel(clientConn net.Conn, connectHost string) {
	defer clientConn.Close()
	host := hostOnly(connectHost)

	tlsConn := tls.Server(clientConn, &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = host // clients omit SNI for IP literals
			}
			return p.ca.leafFor(name)
		},
	})
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	defer tlsConn.Close()

	reader := bufio.NewReader(tlsConn)
	client := p.upstreamClient()
	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			return // client closed or protocol error
		}
		req.URL.Scheme = "https"
		req.URL.Host = connectHost
		if req.Host == "" {
			req.Host = connectHost
		}
		req.RequestURI = ""
		req.Header.Del("Proxy-Connection")
		if p.inj != nil {
			p.inj.apply(host, req.Header) // credential injected on the decrypted request
		}
		resp, err := client.Do(req)
		if err != nil {
			io.WriteString(tlsConn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
			return
		}
		werr := resp.Write(tlsConn)
		resp.Body.Close()
		if werr != nil {
			return
		}
	}
}

func (p *Proxy) mitmConnect(w http.ResponseWriter, r *http.Request) {
	if p.ca == nil {
		http.Error(w, "egress mitm ca not initialized", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		client.Close()
		return
	}
	p.mitmTunnel(client, r.Host)
}

// upstreamClient is the proxy's real-upstream HTTP client: it does standard,
// VERIFYING TLS (we never weaken upstream authenticity) and does not chain
// through any proxy.
func (p *Proxy) upstreamClient() *http.Client {
	if p.upstream != nil {
		return p.upstream
	}
	return &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}}
}
