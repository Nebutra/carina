package main

import (
	"strings"
	"testing"
)

func TestOperatorCommandsRequireExplicitCoordinates(t *testing.T) {
	tests := []struct {
		name string
		fn   func() error
		want string
	}{
		{"session", func() error { return cmdSession(nil, []string{"review"}) }, "session review"},
		{"channel", func() error { return cmdChannel(nil, []string{"reconcile", "ci", "e", "executed"}) }, "--yes"},
		{"outcome", func() error { return cmdChannel(nil, []string{"reconcile", "ci", "e", "maybe", "--yes"}) }, "outcome"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
