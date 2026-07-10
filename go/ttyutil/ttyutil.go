// Package ttyutil provides the single shared isatty check both
// apps/carina-tui and apps/carina-cli use to decide whether to launch the
// interactive TUI or fall back to a scriptable/piped path (P1.5(a)).
package ttyutil

import "os"

// IsTTY reports whether f is an interactive character device (a real
// terminal), not a pipe/file/socket.
func IsTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
