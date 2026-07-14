package tui

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func newClientSubmissionID(generation int, fallbackNanos int64) string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err == nil {
		return "tui_" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("tui_%x_%x", fallbackNanos, generation)
}
