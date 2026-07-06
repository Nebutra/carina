//go:build linux

package daemon

import "syscall"

// prSetDumpable is prctl's PR_SET_DUMPABLE option.
const prSetDumpable = 4

// hardenProcess makes the daemon non-dumpable on Linux: it disables core dumps
// and blocks ptrace-attach and /proc/<pid>/mem reads by other (non-root) users,
// protecting in-memory secrets — the kernel secret broker and resolved
// credentials — from a co-located attacker. Best-effort; a failure is not fatal.
func hardenProcess() error {
	if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
