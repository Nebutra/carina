//go:build !linux

package daemon

// hardenProcess is a no-op off Linux (macOS offers no equivalent prctl here; the
// capability kernel and OS sandbox remain the protection on those platforms).
func hardenProcess() error { return nil }
