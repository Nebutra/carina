package daemon

import (
	"testing"

	"github.com/Nebutra/carina/go/toolnorm"
)

// TestMutatesPackagesFiresThroughTimeoutWrapperWithValueFlag proves the
// package-mutation audit flag is phrasing-independent: a `timeout -s KILL
// 30 npm install ...` invocation must be classified identically to the bare
// `npm install ...` form once run through toolnorm.Canonicalize, the same
// pipeline agentRun uses to derive classifyAs before calling
// mutatesPackages. Before the wrapperArgCount fix, the "-s" value-flag
// (KILL) and the duration (30) were mis-consumed, leaving a stray "30"
// token glued onto WrapperStripped's front so the HasPrefix("npm install")
// check silently failed to fire.
func TestMutatesPackagesFiresThroughTimeoutWrapperWithValueFlag(t *testing.T) {
	canon := toolnorm.Canonicalize([]string{"timeout", "-s", "KILL", "30", "npm", "install", "evil-package"}, "/ws")
	if !mutatesPackages(canon.WrapperStripped) {
		t.Fatalf("mutatesPackages(%q) = false, want true (timeout -s KILL 30 npm install ... must classify identically to bare npm install)", canon.WrapperStripped)
	}
}
