package daemon

import (
	"strings"
	"testing"
)

func TestEgressEnvIncludesMITMCABundleOnlyWhenConfigured(t *testing.T) {
	base := (&Daemon{egressURL: "http://127.0.0.1:12345"}).egressEnv()
	if !containsEnv(base, "HTTPS_PROXY=http://127.0.0.1:12345") {
		t.Fatalf("egress env missing HTTPS proxy: %v", base)
	}
	if containsEnvPrefix(base, "SSL_CERT_FILE=") {
		t.Fatalf("non-MITM egress env must not override TLS roots: %v", base)
	}

	const caPath = "/tmp/carina-egress-ca.pem"
	mitm := (&Daemon{egressURL: "http://127.0.0.1:12345", egressCAPath: caPath}).egressEnv()
	for _, want := range []string{
		"SSL_CERT_FILE=" + caPath,
		"REQUESTS_CA_BUNDLE=" + caPath,
		"CURL_CA_BUNDLE=" + caPath,
		"GIT_SSL_CAINFO=" + caPath,
		"NODE_EXTRA_CA_CERTS=" + caPath,
		"CARINA_EGRESS_CA_BUNDLE=" + caPath,
	} {
		if !containsEnv(mitm, want) {
			t.Fatalf("MITM egress env missing %s: %v", want, mitm)
		}
	}
}

func containsEnv(env []string, want string) bool {
	for _, got := range env {
		if got == want {
			return true
		}
	}
	return false
}

func containsEnvPrefix(env []string, prefix string) bool {
	for _, got := range env {
		if strings.HasPrefix(got, prefix) {
			return true
		}
	}
	return false
}
