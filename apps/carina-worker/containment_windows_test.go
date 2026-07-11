//go:build windows

package main

import "testing"

func TestRuntimeProcessTreeContainmentFailsClosedUntilJobGuardConformance(t *testing.T) {
	if got := runtimeProcessTreeContainment(); got != "none" {
		t.Fatalf("Windows containment must remain unavailable until native Job Object conformance passes, got %q", got)
	}
}
