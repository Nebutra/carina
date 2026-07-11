package main

import (
	"strings"
	"testing"
)

func TestCheckpointRestoreRequiresExplicitConfirmation(t *testing.T) {
	err := cmdCheckpoint(nil, []string{"restore", "s1", "cp1"})
	if err == nil || !strings.Contains(err.Error(), "destructive") {
		t.Fatalf("got %v", err)
	}
}
