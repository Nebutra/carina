package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChromeLauncherScrubsEnvironmentAndRejectsUnsafeFlags(t *testing.T) {
	profile := t.TempDir()
	if err := os.Chmod(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	browserID := "browser_0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(profile, profileMarker), []byte(browserID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(executable, []byte("stub"), 0o700); err != nil {
		t.Fatal(err)
	}
	base := []string{
		chromeLauncherFlag,
		chromePathFlag + executable,
		chromeBrowserIDFlag + browserID,
		"--user-data-dir=" + profile,
		"--remote-debugging-address=127.0.0.1",
		"about:blank",
	}
	req, handled, err := parseChromeLaunchRequest(base)
	if err != nil || !handled {
		t.Fatalf("parse launcher: handled=%v err=%v", handled, err)
	}
	joined := strings.Join(req.Env, "\n")
	if strings.Contains(joined, "SECRET") || !strings.Contains(joined, "HOME="+profile) {
		t.Fatalf("launcher environment is not isolated: %q", joined)
	}
	for _, unsafe := range []string{"--no-sandbox", "--disable-web-security", "--load-extension=/tmp/x", "--remote-debugging-address=0.0.0.0"} {
		_, _, err := parseChromeLaunchRequest(append(append([]string(nil), base...), unsafe))
		if err == nil {
			t.Fatalf("unsafe flag accepted: %s", unsafe)
		}
	}
}

func TestChromeLauncherIgnoresOrdinaryDaemonInvocation(t *testing.T) {
	_, handled, err := parseChromeLaunchRequest([]string{"--state=/tmp/carina"})
	if err != nil || handled {
		t.Fatalf("ordinary invocation treated as launcher: handled=%v err=%v", handled, err)
	}
}
