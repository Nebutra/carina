package nebutra

import "testing"

func TestNormalizeCloudEndpointDefaultsToNebutraDotCom(t *testing.T) {
	got, err := NormalizeCloudEndpoint("")
	if err != nil {
		t.Fatal(err)
	}
	if got != DefaultCloudEndpoint {
		t.Fatalf("endpoint = %q, want %q", got, DefaultCloudEndpoint)
	}
}

func TestNormalizeCloudEndpointRequiresHTTPSOutsideLocalhost(t *testing.T) {
	if _, err := NormalizeCloudEndpoint("http://nebutra.com"); err == nil {
		t.Fatal("non-local http endpoint must be rejected")
	}
	if got, err := NormalizeCloudEndpoint("http://localhost:8787"); err != nil || got != "http://localhost:8787" {
		t.Fatalf("localhost http should be accepted for development, got %q err=%v", got, err)
	}
}

func TestNormalizeSyncModeCurrentlyOffOnly(t *testing.T) {
	got, err := NormalizeSyncMode("")
	if err != nil {
		t.Fatal(err)
	}
	if got != SyncModeOff {
		t.Fatalf("mode = %q, want %q", got, SyncModeOff)
	}
	if _, err := NormalizeSyncMode("metadata"); err == nil {
		t.Fatal("metadata sync must remain unavailable until the Nebutra connector exists")
	}
}
