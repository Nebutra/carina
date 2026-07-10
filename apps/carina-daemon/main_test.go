package main

import (
	"strings"
	"testing"
)

func TestValidateListenerSecurity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tcp     string
		ws      string
		keyFile string
		wantErr string
	}{
		{name: "local defaults"},
		{name: "loopback tcp", tcp: "127.0.0.1:7777"},
		{name: "authenticated websocket", ws: "127.0.0.1:8777", keyFile: "/private/key"},
		{name: "wildcard tcp", tcp: ":7777", wantErr: "restricted to explicit loopback"},
		{name: "all interfaces tcp", tcp: "0.0.0.0:7777", wantErr: "restricted to explicit loopback"},
		{name: "anonymous websocket", ws: "127.0.0.1:8777", wantErr: "gateway websocket requires"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateListenerSecurity(tc.tcp, tc.ws, tc.keyFile)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
