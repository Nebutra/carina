//go:build darwin || linux

package main

import "testing"

func TestRuntimeProcessTreeContainmentUnix(t *testing.T) {
	if got := runtimeProcessTreeContainment(); got != "unix_pgrp_v1" {
		t.Fatalf("runtimeProcessTreeContainment() = %q", got)
	}
}
