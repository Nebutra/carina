package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultUpdateAPIBase = "https://api.github.com/repos/Nebutra/carina"
	maxUpdateMetadata    = 8 << 20
	maxUpdateArchive     = 512 << 20
	maxUpdateExtracted   = 1 << 30
)

type updateOptions struct {
	check   bool
	force   bool
	version string
}

type updateAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type updateRelease struct {
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []updateAsset `json:"assets"`
}

type updateManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Target  struct {
		GOOS   string `json:"goos"`
		GOARCH string `json:"goarch"`
	} `json:"target"`
	Warnings []string `json:"warnings"`
	Files    []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

type updateHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var (
	updateHTTPClient   updateHTTPDoer = &http.Client{Timeout: 5 * time.Minute}
	updateExecutable                  = os.Executable
	updateEvalSymlinks                = filepath.EvalSymlinks
	updateRename                      = os.Rename
	updateRemove                      = os.Remove
	updateGOOS                        = runtime.GOOS
	updateGOARCH                      = runtime.GOARCH
	updateRunCommand                  = func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	updateCommandOutput = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
)

var obsoleteUpdateBinaries = []string{
	"carina-tui", // removed: use `carina` / `carina tui`
}

var requiredUpdateBinaries = []string{
	"carina",
	"carina-daemon",
	"carina-worker",
	"carina-kernel-service",
	"carina-scan",
	"carina-grep",
	"carina-diff",
	"carina-run",
	"carina-pty",
	"carina-patch-native",
	"headroom",
}

func cmdUpdate(args []string) error {
	opts, err := parseUpdateOptions(args)
	if err != nil {
		return err
	}
	executable, err := updateExecutable()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	resolved, err := updateEvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve current executable symlinks: %w", err)
	}

	switch detectUpdateChannel(resolved) {
	case "homebrew":
		return updateWithHomebrew(opts)
	case "npm":
		return updateWithNodeManager("npm", opts)
	case "pnpm":
		return updateWithNodeManager("pnpm", opts)
	default:
		return updateStandalone(opts, resolved)
	}
}

func parseUpdateOptions(args []string) (updateOptions, error) {
	var opts updateOptions
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.check, "check", false, "check without installing")
	fs.BoolVar(&opts.force, "force", false, "allow reinstall or downgrade")
	fs.StringVar(&opts.version, "version", "", "install an exact release version")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return updateOptions{}, fmt.Errorf("usage: carina update [--check] [--version x.y.z] [--force]")
	}
	opts.version = strings.TrimPrefix(strings.TrimSpace(opts.version), "v")
	if opts.version != "" {
		if _, err := parseReleaseVersion(opts.version); err != nil {
			return updateOptions{}, fmt.Errorf("usage: carina update: invalid --version %q", opts.version)
		}
	}
	return opts, nil
}

func detectUpdateChannel(executable string) string {
	path := filepath.ToSlash(executable)
	switch {
	case strings.Contains(path, "/Cellar/carina/") || strings.Contains(path, "/homebrew/Cellar/carina/"):
		return "homebrew"
	case strings.Contains(path, "/.pnpm/") || strings.Contains(path, "/pnpm/global/"):
		return "pnpm"
	case strings.Contains(path, "/node_modules/@nebutra/"):
		return "npm"
	default:
		return "standalone"
	}
}

func updateWithHomebrew(opts updateOptions) error {
	if opts.version != "" {
		return fmt.Errorf("Homebrew owns this installation; exact-version updates must use Homebrew's versioned-formula workflow")
	}
	fmt.Printf("carina update: channel=homebrew current=%s\n", cliVersion)
	if opts.check {
		return updateRunCommand("brew", "outdated", "--formula", "carina")
	}
	if err := updateRunCommand("brew", "update"); err != nil {
		return fmt.Errorf("brew update: %w", err)
	}
	args := []string{"upgrade", "carina"}
	if opts.force {
		args = []string{"reinstall", "carina"}
	}
	if err := updateRunCommand("brew", args...); err != nil {
		return fmt.Errorf("brew %s: %w", args[0], err)
	}
	fmt.Println("carina update: restart the daemon after active tasks finish: carina daemon stop; carina")
	return nil
}

func updateWithNodeManager(manager string, opts updateOptions) error {
	version := "latest"
	if opts.version != "" {
		version = opts.version
	}
	fmt.Printf("carina update: channel=%s current=%s target=%s\n", manager, cliVersion, version)
	if opts.check {
		out, err := updateCommandOutput(manager, "view", "@nebutra/carina", "version")
		if err != nil {
			return fmt.Errorf("%s version check: %s: %w", manager, strings.TrimSpace(string(out)), err)
		}
		latest := strings.TrimSpace(string(out))
		comparison, err := compareReleaseVersions(latest, cliVersion)
		if err != nil {
			return fmt.Errorf("%s returned invalid package version %q", manager, latest)
		}
		if comparison > 0 {
			fmt.Printf("carina update: update available: %s\n", latest)
		} else {
			fmt.Println("carina update: already up to date")
		}
		return nil
	}
	args := []string{"install", "-g", "@nebutra/carina@" + version}
	if manager == "pnpm" {
		args[0] = "add"
	}
	if err := updateRunCommand(manager, args...); err != nil {
		return fmt.Errorf("%s update: %w", manager, err)
	}
	fmt.Println("carina update: restart the daemon after active tasks finish: carina daemon stop; carina")
	return nil
}

func updateStandalone(opts updateOptions, executable string) error {
	release, targetVersion, err := resolveUpdateRelease(opts.version)
	if err != nil {
		return err
	}
	comparison, err := compareReleaseVersions(targetVersion, cliVersion)
	if err != nil {
		return err
	}
	installDir := filepath.Dir(executable)
	fmt.Printf("carina update: channel=standalone current=%s target=%s install=%s\n", cliVersion, targetVersion, installDir)
	if opts.check {
		switch {
		case comparison > 0:
			fmt.Println("carina update: update available")
		case comparison == 0:
			fmt.Println("carina update: already up to date")
		default:
			fmt.Println("carina update: current build is newer than the selected public release")
		}
		return nil
	}
	if comparison <= 0 && !opts.force {
		if comparison == 0 {
			fmt.Println("carina update: already up to date")
			return nil
		}
		if opts.version == "" {
			fmt.Println("carina update: current build is newer than the latest public release; nothing changed")
			return nil
		}
		return fmt.Errorf("refusing to downgrade %s to %s without --force", cliVersion, targetVersion)
	}

	archiveName, err := updateArchiveName(targetVersion, updateGOOS, updateGOARCH)
	if err != nil {
		return err
	}
	archiveURL, checksumURL, err := releaseAssetURLs(release, archiveName)
	if err != nil {
		return err
	}
	apiBase := updateAPIBase()
	if err := validateUpdateDownloadURL(archiveURL, apiBase); err != nil {
		return err
	}
	if err := validateUpdateDownloadURL(checksumURL, apiBase); err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "carina-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	archivePath := filepath.Join(work, archiveName)
	checksumPath := archivePath + ".sha256"
	if err := downloadUpdateFile(archiveURL, archivePath, maxUpdateArchive); err != nil {
		return fmt.Errorf("download release archive: %w", err)
	}
	if err := downloadUpdateFile(checksumURL, checksumPath, maxUpdateMetadata); err != nil {
		return fmt.Errorf("download release checksum: %w", err)
	}
	if err := verifyUpdateChecksum(archivePath, checksumPath, archiveName); err != nil {
		return err
	}

	stage := filepath.Join(work, "stage")
	binaries, err := extractAndVerifyUpdateArchive(archivePath, stage, targetVersion, updateGOOS, updateGOARCH)
	if err != nil {
		return err
	}
	if err := installUpdateBundle(installDir, binaries); err != nil {
		return err
	}
	removeObsoleteUpdateBinaries(installDir)
	fmt.Printf("carina update: updated %s -> %s\n", cliVersion, targetVersion)
	fmt.Println("carina update: restart the daemon after active tasks finish: carina daemon stop; carina")
	return nil
}

func updateAPIBase() string {
	if override := strings.TrimRight(strings.TrimSpace(os.Getenv("CARINA_UPDATE_API_BASE")), "/"); override != "" {
		return override
	}
	return defaultUpdateAPIBase
}

func resolveUpdateRelease(version string) (updateRelease, string, error) {
	endpoint := updateAPIBase() + "/releases/latest"
	if version != "" {
		endpoint = updateAPIBase() + "/releases/tags/v" + version
	}
	var release updateRelease
	if err := fetchUpdateJSON(endpoint, &release); err != nil {
		return release, "", fmt.Errorf("resolve release: %w", err)
	}
	if release.Draft || release.Prerelease {
		return release, "", fmt.Errorf("refusing draft or prerelease update %q", release.TagName)
	}
	target := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	if _, err := parseReleaseVersion(target); err != nil {
		return release, "", fmt.Errorf("release returned invalid version %q", release.TagName)
	}
	if version != "" && target != version {
		return release, "", fmt.Errorf("release tag %q does not match requested version %q", target, version)
	}
	return release, target, nil
}

func fetchUpdateJSON(rawURL string, dst any) error {
	resp, err := doUpdateRequest(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP %d", rawURL, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxUpdateMetadata+1))
	if err != nil {
		return err
	}
	if len(raw) > maxUpdateMetadata {
		return fmt.Errorf("release metadata exceeds %d bytes", maxUpdateMetadata)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return err
	}
	return nil
}

func doUpdateRequest(rawURL string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "carina/"+cliVersion+" updater")
	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.Request != nil && !trustedFinalUpdateURL(resp.Request.URL.Scheme, resp.Request.URL.Hostname(), updateAPIBase()) {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("update request redirected to untrusted origin %s", resp.Request.URL.Host)
	}
	return resp, nil
}

func trustedFinalUpdateURL(scheme, host, apiBase string) bool {
	apiReq, err := http.NewRequest(http.MethodGet, apiBase, nil)
	if err != nil || scheme != apiReq.URL.Scheme {
		return false
	}
	if apiBase != defaultUpdateAPIBase {
		return host == apiReq.URL.Hostname()
	}
	switch host {
	case "api.github.com", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com":
		return scheme == "https"
	default:
		return false
	}
}

func updateArchiveName(version, goos, goarch string) (string, error) {
	if goos != "darwin" && goos != "linux" {
		return "", fmt.Errorf("carina update is unsupported on %s", goos)
	}
	if goarch != "arm64" && goarch != "amd64" {
		return "", fmt.Errorf("carina update is unsupported on %s/%s", goos, goarch)
	}
	return fmt.Sprintf("carina_%s_%s_%s.tar.gz", version, goos, goarch), nil
}

func releaseAssetURLs(release updateRelease, archiveName string) (string, string, error) {
	assets := make(map[string]string, len(release.Assets))
	for _, asset := range release.Assets {
		assets[asset.Name] = asset.BrowserDownloadURL
	}
	archiveURL, ok := assets[archiveName]
	if !ok || archiveURL == "" {
		return "", "", fmt.Errorf("release %s has no asset %s", release.TagName, archiveName)
	}
	checksumURL, ok := assets[archiveName+".sha256"]
	if !ok || checksumURL == "" {
		return "", "", fmt.Errorf("release %s has no checksum for %s", release.TagName, archiveName)
	}
	return archiveURL, checksumURL, nil
}

func validateUpdateDownloadURL(rawURL, apiBase string) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("invalid release asset URL: %w", err)
	}
	if apiBase == defaultUpdateAPIBase {
		if req.URL.Scheme != "https" || req.URL.Hostname() != "github.com" ||
			!strings.HasPrefix(req.URL.Path, "/Nebutra/carina/releases/download/") {
			return fmt.Errorf("untrusted release asset URL %q", rawURL)
		}
		return nil
	}
	apiReq, err := http.NewRequest(http.MethodGet, apiBase, nil)
	if err != nil || req.URL.Scheme != apiReq.URL.Scheme || req.URL.Host != apiReq.URL.Host {
		return fmt.Errorf("update mirror returned cross-origin asset URL %q", rawURL)
	}
	return nil
}

func downloadUpdateFile(rawURL, destination string, maxBytes int64) error {
	resp, err := doUpdateRequest(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP %d", rawURL, resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("download exceeds %d bytes", maxBytes)
	}
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if n > maxBytes {
		return fmt.Errorf("download exceeds %d bytes", maxBytes)
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func verifyUpdateChecksum(archivePath, checksumPath, archiveName string) error {
	raw, err := os.ReadFile(checksumPath)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != archiveName {
		return fmt.Errorf("release checksum filename mismatch for %s", archiveName)
	}
	expected := strings.ToLower(fields[0])
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("release checksum is not SHA256")
	}
	actual, err := sha256UpdateFile(archivePath)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("release checksum mismatch for %s", archiveName)
	}
	return nil
}

func extractAndVerifyUpdateArchive(archivePath, stage, version, goos, goarch string) (map[string]string, error) {
	archive, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return nil, fmt.Errorf("open release gzip: %w", err)
	}
	defer gz.Close()
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return nil, err
	}

	tr := tar.NewReader(gz)
	var root string
	var manifestRaw, checksumsRaw []byte
	binaries := map[string]string{}
	var extractedBytes int64
	for entries := 0; ; entries++ {
		if entries > 512 {
			return nil, fmt.Errorf("release archive has too many entries")
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		clean, entryRoot, rel, err := safeUpdateArchivePath(header.Name)
		if err != nil {
			return nil, err
		}
		if isAppleDoubleUpdatePath(clean) {
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
				return nil, fmt.Errorf("release archive contains unsafe AppleDouble entry %q", header.Name)
			}
			if header.Size < 0 || header.Size > maxUpdateMetadata || extractedBytes > maxUpdateExtracted-header.Size {
				return nil, fmt.Errorf("release AppleDouble metadata exceeds limits")
			}
			extractedBytes += header.Size
			continue
		}
		if root == "" {
			root = entryRoot
		} else if entryRoot != root {
			return nil, fmt.Errorf("release archive has multiple roots: %q and %q (entry %q)", root, entryRoot, header.Name)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("release archive contains unsafe entry %q", header.Name)
		}
		if header.Size < 0 || header.Size > maxUpdateExtracted || extractedBytes > maxUpdateExtracted-header.Size {
			return nil, fmt.Errorf("release archive exceeds extraction limit")
		}
		extractedBytes += header.Size
		switch rel {
		case "MANIFEST.json":
			manifestRaw, err = readUpdateEntry(tr, header.Size, maxUpdateMetadata)
		case "checksums.txt":
			checksumsRaw, err = readUpdateEntry(tr, header.Size, maxUpdateMetadata)
		default:
			if !strings.HasPrefix(rel, "bin/") || strings.Contains(strings.TrimPrefix(rel, "bin/"), "/") {
				continue
			}
			name := strings.TrimPrefix(rel, "bin/")
			if name == "" || (!strings.HasPrefix(name, "carina") && name != "headroom") {
				return nil, fmt.Errorf("release archive contains unexpected executable %q", name)
			}
			if _, duplicate := binaries[name]; duplicate {
				return nil, fmt.Errorf("release archive repeats executable %q", name)
			}
			destination := filepath.Join(stage, name)
			err = writeUpdateEntry(tr, header.Size, destination)
			if err == nil {
				binaries[name] = destination
			}
		}
		if err != nil {
			return nil, err
		}
	}
	if root == "" || len(manifestRaw) == 0 || len(checksumsRaw) == 0 {
		return nil, fmt.Errorf("release archive is missing manifest or checksums")
	}
	for _, required := range requiredUpdateBinaries {
		if _, ok := binaries[required]; !ok {
			return nil, fmt.Errorf("release archive is missing %s", required)
		}
	}
	if err := verifyUpdateManifest(manifestRaw, checksumsRaw, binaries, version, goos, goarch); err != nil {
		return nil, err
	}
	return binaries, nil
}

func safeUpdateArchivePath(name string) (clean, root, rel string, err error) {
	if name == "" || strings.ContainsRune(name, '\\') || strings.HasPrefix(name, "/") {
		return "", "", "", fmt.Errorf("release archive contains unsafe path %q", name)
	}
	clean = pathpkg.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != strings.TrimSuffix(name, "/") {
		return "", "", "", fmt.Errorf("release archive contains unsafe path %q", name)
	}
	parts := strings.SplitN(clean, "/", 2)
	root = parts[0]
	if len(parts) == 2 {
		rel = parts[1]
	}
	return clean, root, rel, nil
}

func isAppleDoubleUpdatePath(clean string) bool {
	for _, component := range strings.Split(clean, "/") {
		if component == "__MACOSX" || strings.HasPrefix(component, "._") {
			return true
		}
	}
	return false
}

func readUpdateEntry(r io.Reader, size, limit int64) ([]byte, error) {
	if size > limit {
		return nil, fmt.Errorf("release metadata exceeds %d bytes", limit)
	}
	raw, err := io.ReadAll(io.LimitReader(r, size))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != size {
		return nil, io.ErrUnexpectedEOF
	}
	return raw, nil
}

func writeUpdateEntry(r io.Reader, size int64, destination string) error {
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyN(f, r, size)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func verifyUpdateManifest(manifestRaw, checksumsRaw []byte, binaries map[string]string, version, goos, goarch string) error {
	var manifest updateManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return fmt.Errorf("invalid release manifest: %w", err)
	}
	if manifest.Name != "carina" || manifest.Version != version || manifest.Target.GOOS != goos || manifest.Target.GOARCH != goarch {
		return fmt.Errorf("release manifest identity mismatch: name=%s version=%s target=%s/%s", manifest.Name, manifest.Version, manifest.Target.GOOS, manifest.Target.GOARCH)
	}
	for _, warning := range manifest.Warnings {
		if strings.Contains(strings.ToUpper(warning), "SKIP_") {
			return fmt.Errorf("refusing developer-only release archive: %s", warning)
		}
	}
	checksumMap, err := parseUpdateChecksums(checksumsRaw)
	if err != nil {
		return err
	}
	manifestMap := make(map[string]string, len(manifest.Files))
	for _, file := range manifest.Files {
		if _, duplicate := manifestMap[file.Path]; duplicate {
			return fmt.Errorf("release manifest repeats %s", file.Path)
		}
		manifestMap[file.Path] = strings.ToLower(file.SHA256)
	}
	for name, file := range binaries {
		rel := "bin/" + name
		actual, err := sha256UpdateFile(file)
		if err != nil {
			return err
		}
		if checksumMap[rel] != actual || manifestMap[rel] != actual {
			return fmt.Errorf("release internal checksum mismatch for %s", rel)
		}
	}
	return nil
}

func parseUpdateChecksums(raw []byte) (map[string]string, error) {
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			return nil, fmt.Errorf("invalid release checksums entry %q", line)
		}
		name := strings.TrimPrefix(fields[1], "*")
		if _, _, _, err := safeUpdateArchivePath("root/" + name); err != nil {
			return nil, fmt.Errorf("invalid release checksum path %q", name)
		}
		if _, duplicate := result[name]; duplicate {
			return nil, fmt.Errorf("release checksums repeat %s", name)
		}
		result[name] = strings.ToLower(fields[0])
	}
	return result, nil
}

// removeObsoleteUpdateBinaries deletes retired product binaries (e.g. the
// former carina-tui alias) so upgrades do not leave a second shell entry.
func removeObsoleteUpdateBinaries(installDir string) {
	for _, name := range obsoleteUpdateBinaries {
		path := filepath.Join(installDir, name)
		if err := updateRemove(path); err == nil {
			fmt.Printf("carina update: removed obsolete binary %s\n", name)
		}
	}
}

func installUpdateBundle(installDir string, binaries map[string]string) error {
	info, err := os.Stat(installDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("update install directory is unavailable: %s", installDir)
	}
	lockPath := filepath.Join(installDir, ".carina-update.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("another update is active or left %s behind", lockPath)
		}
		return fmt.Errorf("acquire update lock: %w", err)
	}
	_, _ = fmt.Fprintf(lock, "pid=%d time=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	_ = lock.Sync()
	_ = lock.Close()
	defer updateRemove(lockPath)

	names := make([]string, 0, len(binaries))
	for name := range binaries {
		names = append(names, name)
	}
	sort.Strings(names)
	// Replace the currently executing binary last; every earlier component is
	// rolled back if that final rename fails.
	for i, name := range names {
		if name == "carina" {
			names = append(append(names[:i], names[i+1:]...), name)
			break
		}
	}

	tx := fmt.Sprintf("%d", os.Getpid())
	prepared := make(map[string]string, len(names))
	defer func() {
		for _, tmp := range prepared {
			_ = updateRemove(tmp)
		}
	}()
	for _, name := range names {
		destination := filepath.Join(installDir, name)
		if info, err := os.Lstat(destination); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular file %s", destination)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		tmp := filepath.Join(installDir, "."+name+".carina-update-"+tx+".new")
		if err := copyUpdateBinary(binaries[name], tmp); err != nil {
			return fmt.Errorf("prepare %s: %w", name, err)
		}
		prepared[name] = tmp
	}

	type replacement struct {
		name, destination, backup string
		hadOld, placed            bool
	}
	replaced := make([]replacement, 0, len(names))
	rollback := func(cause error) error {
		var rollbackErr error
		for i := len(replaced) - 1; i >= 0; i-- {
			item := replaced[i]
			if item.placed {
				_ = updateRemove(item.destination)
			}
			if item.hadOld {
				if err := updateRename(item.backup, item.destination); err != nil && rollbackErr == nil {
					rollbackErr = err
				}
			}
		}
		if rollbackErr != nil {
			return fmt.Errorf("update failed: %v; rollback failed: %w", cause, rollbackErr)
		}
		return fmt.Errorf("update failed and was rolled back: %w", cause)
	}

	for _, name := range names {
		destination := filepath.Join(installDir, name)
		item := replacement{name: name, destination: destination, backup: filepath.Join(installDir, "."+name+".carina-update-"+tx+".old")}
		if _, err := os.Lstat(item.backup); err == nil {
			return rollback(fmt.Errorf("stale update backup exists: %s", item.backup))
		} else if !errors.Is(err, os.ErrNotExist) {
			return rollback(err)
		}
		if _, err := os.Stat(destination); err == nil {
			if err := updateRename(destination, item.backup); err != nil {
				return rollback(fmt.Errorf("backup %s: %w", name, err))
			}
			item.hadOld = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return rollback(err)
		}
		replaced = append(replaced, item)
		if err := updateRename(prepared[name], destination); err != nil {
			return rollback(fmt.Errorf("activate %s: %w", name, err))
		}
		replaced[len(replaced)-1].placed = true
		delete(prepared, name)
	}
	if err := syncUpdateDirectory(installDir); err != nil {
		return rollback(fmt.Errorf("sync install directory: %w", err))
	}
	for _, item := range replaced {
		if item.hadOld {
			_ = updateRemove(item.backup)
		}
	}
	return syncUpdateDirectory(installDir)
}

func copyUpdateBinary(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = updateRemove(destination)
		}
	}()
	_, copyErr := io.Copy(out, in)
	chmodErr := out.Chmod(0o755)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if chmodErr != nil {
		return chmodErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	ok = true
	return nil
}

func syncUpdateDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func sha256UpdateFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func compareReleaseVersions(a, b string) (int, error) {
	av, err := parseReleaseVersion(a)
	if err != nil {
		return 0, err
	}
	bv, err := parseReleaseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range av {
		if av[i] < bv[i] {
			return -1, nil
		}
		if av[i] > bv[i] {
			return 1, nil
		}
	}
	return 0, nil
}

func parseReleaseVersion(raw string) ([3]int, error) {
	var result [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(raw), "v"), ".")
	if len(parts) != 3 {
		return result, fmt.Errorf("invalid release version %q", raw)
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return result, fmt.Errorf("invalid release version %q", raw)
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return result, fmt.Errorf("invalid release version %q", raw)
		}
		result[i] = value
	}
	return result, nil
}
