package rpc

import "testing"

func TestNormalizeGatewayOrigin(t *testing.T) {
	tests := map[string]string{
		"tauri":      "tauri://localhost",
		"tauri http": "http://tauri.localhost",
		"dev":        "http://127.0.0.1:4173",
		"ipv6":       "https://[::1]:4173",
		"localhost":  "http://localhost:3000",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if got, err := NormalizeGatewayOrigin(input); err != nil || got != input {
				t.Fatalf("NormalizeGatewayOrigin(%q) = %q, %v", input, got, err)
			}
		})
	}
	for _, input := range []string{
		"",
		"https://evil.example",
		"http://127.0.0.1:4173/path",
		"http://127.0.0.1:4173/?x=1",
		"http://user@127.0.0.1:4173",
		"http://*.localhost",
		"file://localhost",
	} {
		t.Run("reject "+input, func(t *testing.T) {
			if _, err := NormalizeGatewayOrigin(input); err == nil {
				t.Fatalf("NormalizeGatewayOrigin(%q) unexpectedly succeeded", input)
			}
		})
	}
}
