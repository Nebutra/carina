package browser

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	chromeLauncherFlag  = "--carina-internal-browser-launch"
	chromePathFlag      = "--carina-internal-chrome-path="
	chromeBrowserIDFlag = "--carina-internal-browser-id="
)

type chromeLaunchRequest struct {
	ChromePath string
	ProfileDir string
	BrowserID  string
	Args       []string
	Env        []string
}

// MaybeExecChromeLauncher re-executes an approved Chromium binary with a
// minimal environment. It must run before normal daemon argument parsing.
func MaybeExecChromeLauncher() (bool, error) {
	req, handled, err := parseChromeLaunchRequest(os.Args[1:])
	if !handled || err != nil {
		return handled, err
	}
	return true, execChromeProcess(req.ChromePath, append([]string{req.ChromePath}, req.Args...), req.Env)
}

func parseChromeLaunchRequest(args []string) (chromeLaunchRequest, bool, error) {
	var req chromeLaunchRequest
	handled := false
	forwarded := make([]string, 0, len(args))
	for _, arg := range args {
		switch {
		case arg == chromeLauncherFlag:
			if handled {
				return req, true, fmt.Errorf("duplicate internal launch marker")
			}
			handled = true
		case strings.HasPrefix(arg, chromePathFlag):
			if req.ChromePath != "" {
				return req, true, fmt.Errorf("duplicate internal browser path")
			}
			req.ChromePath = strings.TrimPrefix(arg, chromePathFlag)
		case strings.HasPrefix(arg, chromeBrowserIDFlag):
			if req.BrowserID != "" {
				return req, true, fmt.Errorf("duplicate internal browser identity")
			}
			req.BrowserID = strings.TrimPrefix(arg, chromeBrowserIDFlag)
		default:
			forwarded = append(forwarded, arg)
		}
	}
	if !handled {
		return chromeLaunchRequest{}, false, nil
	}
	if !validBrowserID(req.BrowserID) {
		return req, true, fmt.Errorf("invalid internal browser identity")
	}
	resolvedChrome, err := validateChromeExecutable(req.ChromePath)
	if err != nil {
		return req, true, err
	}
	req.ChromePath = resolvedChrome
	for _, arg := range forwarded {
		lower := strings.ToLower(arg)
		if lower == "--no-sandbox" || strings.HasPrefix(lower, "--no-sandbox=") ||
			strings.HasPrefix(lower, "--load-extension") || strings.HasPrefix(lower, "--disable-web-security") ||
			strings.HasPrefix(lower, "--remote-debugging-address=") && !strings.EqualFold(arg, "--remote-debugging-address=127.0.0.1") {
			return req, true, fmt.Errorf("unsafe Chromium launch flag rejected")
		}
		if strings.HasPrefix(arg, "--user-data-dir=") {
			if req.ProfileDir != "" {
				return req, true, fmt.Errorf("duplicate Chromium profile directory")
			}
			req.ProfileDir = strings.TrimPrefix(arg, "--user-data-dir=")
		}
	}
	if !filepath.IsAbs(req.ProfileDir) || filepath.Clean(req.ProfileDir) != req.ProfileDir {
		return req, true, fmt.Errorf("invalid Chromium profile directory")
	}
	marker, err := os.ReadFile(filepath.Join(req.ProfileDir, profileMarker))
	if err != nil || strings.TrimSpace(string(marker)) != req.BrowserID {
		return req, true, fmt.Errorf("Chromium profile ownership check failed")
	}
	info, err := os.Stat(req.ProfileDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return req, true, fmt.Errorf("Chromium profile must be a private directory")
	}
	for _, dir := range []string{"tmp", "xdg-config", "xdg-cache", "xdg-runtime"} {
		if err := os.MkdirAll(filepath.Join(req.ProfileDir, dir), 0o700); err != nil {
			return req, true, fmt.Errorf("prepare Chromium runtime directory: %w", err)
		}
	}
	req.Args = forwarded
	req.Env = []string{
		"HOME=" + req.ProfileDir,
		"TMPDIR=" + filepath.Join(req.ProfileDir, "tmp"),
		"XDG_CONFIG_HOME=" + filepath.Join(req.ProfileDir, "xdg-config"),
		"XDG_CACHE_HOME=" + filepath.Join(req.ProfileDir, "xdg-cache"),
		"XDG_RUNTIME_DIR=" + filepath.Join(req.ProfileDir, "xdg-runtime"),
		"PATH=/usr/bin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
	return req, true, nil
}

func discoverChrome(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		path, err := validateChromeExecutable(explicit)
		if err != nil {
			return "", browserError(ErrorUnavailable, "configured Chromium executable is unavailable", "install a supported Chromium browser or correct the deployment browser path", false, err)
		}
		return path, nil
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	case "linux":
		candidates = []string{"google-chrome-stable", "google-chrome", "chromium", "chromium-browser", "chrome"}
	default:
		return "", browserError(ErrorUnavailable, "native browser automation is unavailable on this platform", "use macOS or Linux with a supported Chromium browser", false, nil)
	}
	for _, candidate := range candidates {
		path := candidate
		if !filepath.IsAbs(path) {
			resolved, err := exec.LookPath(path)
			if err != nil {
				continue
			}
			path = resolved
		}
		if resolved, err := validateChromeExecutable(path); err == nil {
			return resolved, nil
		}
	}
	return "", browserError(ErrorUnavailable, "no supported Chromium browser was found", "install Google Chrome, Chromium, or Microsoft Edge and retry", false, nil)
}

func validateChromeExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return "", fmt.Errorf("Chromium executable path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve Chromium executable: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("Chromium executable is not an executable regular file")
	}
	return resolved, nil
}

func validBrowserID(value string) bool {
	if !strings.HasPrefix(value, "browser_") || len(value) != len("browser_")+32 {
		return false
	}
	for _, ch := range strings.TrimPrefix(value, "browser_") {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
