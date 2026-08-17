package rpc

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestRemoteOriginRestriction: local origin may call anything; a remote (TCP)
// origin may only call allowlisted read/observe methods; the kill-switch cuts
// off all remote access without affecting local.
func TestRemoteOriginRestriction(t *testing.T) {
	s := NewServer()
	s.MarkRemoteSafe("daemon.status")

	check := func(method string, origin Origin, want bool) {
		t.Helper()
		if ok, _ := s.remoteAuthorized(method, origin); ok != want {
			t.Fatalf("remoteAuthorized(%q, %v) = %v, want %v", method, origin, ok, want)
		}
	}

	// Local: everything allowed.
	check("daemon.status", OriginLocal, true)
	check("command.exec", OriginLocal, true)

	// Remote: only allowlisted methods.
	check("daemon.status", OriginRemote, true)
	check("command.exec", OriginRemote, false)
	check("daemon.remote.disable", OriginRemote, false) // kill-switch itself is local-only

	// Kill-switch: all remote refused, local unaffected.
	s.SetRemoteDisabled(true)
	check("daemon.status", OriginRemote, false)
	check("daemon.status", OriginLocal, true)

	s.SetRemoteDisabled(false)
	check("daemon.status", OriginRemote, true)
}

func TestRemoteParamsGuardSkipsLocalOrigin(t *testing.T) {
	s := NewServer()
	var remoteHits int
	s.SetRemoteParamsGuard(func(method string, _ json.RawMessage) error {
		remoteHits++
		if method == "session.get" {
			return errors.New("gateway workspace is pinned")
		}
		return nil
	})
	if err := s.applyRemoteParamsGuard(OriginLocal, "session.get", json.RawMessage(`{"session_id":"s1"}`)); err != nil {
		t.Fatalf("local origin must skip the remote guard: %v", err)
	}
	if remoteHits != 0 {
		t.Fatalf("local origin invoked the remote guard %d times", remoteHits)
	}
	err := s.applyRemoteParamsGuard(OriginRemote, "session.get", json.RawMessage(`{"session_id":"s1"}`))
	if err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("remote origin must run the guard, got %v", err)
	}
	if remoteHits != 1 {
		t.Fatalf("remote hits = %d", remoteHits)
	}
}
