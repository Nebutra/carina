package rpc

import "testing"

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
