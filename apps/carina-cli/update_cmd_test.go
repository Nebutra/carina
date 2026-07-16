package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestUpdateVersionAndChannelParsing(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"0.6.3", "0.6.2", 1},
		{"0.6.2", "0.6.2", 0},
		{"1.0.0", "2.0.0", -1},
		{"0.10.0", "0.9.9", 1},
	} {
		got, err := compareReleaseVersions(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Errorf("compareReleaseVersions(%q, %q) = %d, %v; want %d", tc.a, tc.b, got, err, tc.want)
		}
	}
	for _, invalid := range []string{"", "1", "1.2", "1.2.3.4", "1.02.3", "1.2.x", "1.2.3-beta"} {
		if _, err := parseReleaseVersion(invalid); err == nil {
			t.Errorf("accepted invalid version %q", invalid)
		}
	}
	for path, want := range map[string]string{
		"/opt/homebrew/Cellar/carina/0.6.2/bin/carina":                         "homebrew",
		"/home/me/.local/share/pnpm/global/5/.pnpm/@nebutra/carina/bin/carina": "pnpm",
		"/usr/lib/node_modules/@nebutra/carina-linux-arm64/bin/carina":         "npm",
		"/home/me/.local/bin/carina":                                           "standalone",
	} {
		if got := detectUpdateChannel(path); got != want {
			t.Errorf("detectUpdateChannel(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestStandaloneUpdateDownloadsVerifiesAndReplacesWholeBundle(t *testing.T) {
	const version = "0.6.4"
	archive, checksum := buildUpdateFixture(t, version, "linux", "amd64", "")
	base, assetHits := installUpdateHTTPFixture(t, version, "linux", "amd64", archive, checksum)
	installDir := prepareOldUpdateInstall(t)
	withUpdateTestHooks(t, filepath.Join(installDir, "carina"), "linux", "amd64")
	t.Setenv("CARINA_UPDATE_API_BASE", base)

	out, err := captureStdout(t, func() error { return cmdUpdate(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "updated 0.6.3 -> 0.6.4") || !strings.Contains(out, "restart the daemon") {
		t.Fatalf("update output missing lifecycle guidance:\n%s", out)
	}
	if *assetHits != 2 {
		t.Fatalf("asset requests = %d, want archive + checksum", *assetHits)
	}
	for _, name := range requiredUpdateBinaries {
		raw, err := os.ReadFile(filepath.Join(installDir, name))
		if err != nil || string(raw) != "new-"+name {
			t.Errorf("installed %s = %q, %v", name, raw, err)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(installDir, ".*.carina-update-*")); len(matches) != 0 {
		t.Fatalf("transaction debris remained: %v", matches)
	}
}

func TestUpdateCheckDoesNotDownloadAssets(t *testing.T) {
	const version = "0.6.4"
	archive, checksum := buildUpdateFixture(t, version, "linux", "amd64", "")
	base, assetHits := installUpdateHTTPFixture(t, version, "linux", "amd64", archive, checksum)
	installDir := prepareOldUpdateInstall(t)
	withUpdateTestHooks(t, filepath.Join(installDir, "carina"), "linux", "amd64")
	t.Setenv("CARINA_UPDATE_API_BASE", base)

	out, err := captureStdout(t, func() error { return cmdUpdate([]string{"--check"}) })
	if err != nil || !strings.Contains(out, "update available") {
		t.Fatalf("check output=%q err=%v", out, err)
	}
	if *assetHits != 0 {
		t.Fatalf("--check downloaded %d assets", *assetHits)
	}
}

func TestUpdateDoesNotDowngradeDevelopmentBuildByDefault(t *testing.T) {
	const version = "0.6.0"
	archive, checksum := buildUpdateFixture(t, version, "linux", "amd64", "")
	base, assetHits := installUpdateHTTPFixture(t, version, "linux", "amd64", archive, checksum)
	installDir := prepareOldUpdateInstall(t)
	withUpdateTestHooks(t, filepath.Join(installDir, "carina"), "linux", "amd64")
	t.Setenv("CARINA_UPDATE_API_BASE", base)
	out, err := captureStdout(t, func() error { return cmdUpdate(nil) })
	if err != nil || !strings.Contains(out, "newer than the latest public release") {
		t.Fatalf("development update output=%q err=%v", out, err)
	}
	if *assetHits != 0 {
		t.Fatalf("development no-op downloaded %d assets", *assetHits)
	}
}

func TestStandaloneUpdateRejectsBadOuterChecksumWithoutMutation(t *testing.T) {
	const version = "0.6.4"
	archive, _ := buildUpdateFixture(t, version, "linux", "amd64", "")
	base, _ := installUpdateHTTPFixture(t, version, "linux", "amd64", archive, strings.Repeat("0", 64)+"  carina_0.6.4_linux_amd64.tar.gz\n")
	installDir := prepareOldUpdateInstall(t)
	withUpdateTestHooks(t, filepath.Join(installDir, "carina"), "linux", "amd64")
	t.Setenv("CARINA_UPDATE_API_BASE", base)

	if _, err := captureStdout(t, func() error { return cmdUpdate(nil) }); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("bad checksum error = %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(installDir, "carina"))
	if string(raw) != "old-carina" {
		t.Fatalf("bad checksum mutated installation: %q", raw)
	}
}

func TestStandaloneUpdateRejectsDeveloperOnlyArchive(t *testing.T) {
	const version = "0.6.4"
	archive, checksum := buildUpdateFixture(t, version, "linux", "amd64", "SKIP_BUILD=1: reused local artifacts")
	base, _ := installUpdateHTTPFixture(t, version, "linux", "amd64", archive, checksum)
	installDir := prepareOldUpdateInstall(t)
	withUpdateTestHooks(t, filepath.Join(installDir, "carina"), "linux", "amd64")
	t.Setenv("CARINA_UPDATE_API_BASE", base)
	if _, err := captureStdout(t, func() error { return cmdUpdate(nil) }); err == nil || !strings.Contains(err.Error(), "developer-only") {
		t.Fatalf("developer archive error = %v", err)
	}
}

func TestUpdateArchiveRejectsTraversalAndSymlink(t *testing.T) {
	for _, tc := range []struct {
		name     string
		entry    string
		typeflag byte
	}{
		{"traversal", "root/../../escaped", tar.TypeReg},
		{"symlink", "root/bin/carina", tar.TypeSymlink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "bad.tar.gz")
			writeTarFixture(t, archive, []tarFixtureEntry{{name: tc.entry, body: []byte("bad"), typeflag: tc.typeflag}})
			if _, err := extractAndVerifyUpdateArchive(archive, t.TempDir(), "0.6.3", "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("unsafe archive error = %v", err)
			}
		})
	}
}

func TestUpdateInstallRollsBackEveryBinaryOnActivationFailure(t *testing.T) {
	installDir := prepareOldUpdateInstall(t)
	stage := t.TempDir()
	binaries := map[string]string{}
	for _, name := range requiredUpdateBinaries {
		path := filepath.Join(stage, name)
		if err := os.WriteFile(path, []byte("new-"+name), 0o755); err != nil {
			t.Fatal(err)
		}
		binaries[name] = path
	}
	originalRename := updateRename
	t.Cleanup(func() { updateRename = originalRename })
	failed := false
	updateRename = func(old, new string) error {
		if !failed && strings.Contains(old, ".carina-daemon.carina-update-") && strings.HasSuffix(old, ".new") {
			failed = true
			return fmt.Errorf("injected activation failure")
		}
		return os.Rename(old, new)
	}
	if err := installUpdateBundle(installDir, binaries); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("rollback error = %v", err)
	}
	for _, name := range requiredUpdateBinaries {
		raw, err := os.ReadFile(filepath.Join(installDir, name))
		if err != nil || string(raw) != "old-"+name {
			t.Errorf("rollback %s = %q, %v", name, raw, err)
		}
	}
}

func TestUpdateInstallRefusesDestinationSymlink(t *testing.T) {
	installDir := prepareOldUpdateInstall(t)
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("do-not-touch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(installDir, "carina-daemon")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(installDir, "carina-daemon")); err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	binaries := map[string]string{}
	for _, name := range requiredUpdateBinaries {
		path := filepath.Join(stage, name)
		if err := os.WriteFile(path, []byte("new-"+name), 0o755); err != nil {
			t.Fatal(err)
		}
		binaries[name] = path
	}
	if err := installUpdateBundle(installDir, binaries); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("symlink destination error = %v", err)
	}
	raw, _ := os.ReadFile(external)
	if string(raw) != "do-not-touch" {
		t.Fatalf("symlink target was modified: %q", raw)
	}
}

func TestUpdateDelegatesHomebrewOwnedInstallation(t *testing.T) {
	withUpdateTestHooks(t, "/opt/homebrew/Cellar/carina/0.6.2/bin/carina", "darwin", "arm64")
	originalRun := updateRunCommand
	t.Cleanup(func() { updateRunCommand = originalRun })
	var calls [][]string
	updateRunCommand = func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	if _, err := captureStdout(t, func() error { return cmdUpdate(nil) }); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"brew", "update"}, {"brew", "upgrade", "carina"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("Homebrew calls = %#v, want %#v", calls, want)
	}
}

func TestUpdateChecksNodeManagedPackageWithOwningManager(t *testing.T) {
	withUpdateTestHooks(t, "/usr/lib/node_modules/@nebutra/carina-linux-arm64/bin/carina", "linux", "arm64")
	originalOutput := updateCommandOutput
	t.Cleanup(func() { updateCommandOutput = originalOutput })
	var got []string
	updateCommandOutput = func(name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return []byte("0.6.4\n"), nil
	}
	out, err := captureStdout(t, func() error { return cmdUpdate([]string{"--check"}) })
	if err != nil || !strings.Contains(out, "update available: 0.6.4") {
		t.Fatalf("npm check output=%q err=%v", out, err)
	}
	want := []string{"npm", "view", "@nebutra/carina", "version"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("npm check = %#v, want %#v", got, want)
	}
}

func TestUpdateCommandIsDocumentedAndDaemonFree(t *testing.T) {
	if !strings.Contains(usage, "carina update [--check] [--version x.y.z] [--force]") {
		t.Fatalf("usage missing update command:\n%s", usage)
	}
	if !ungatedCommands["update"] {
		t.Fatal("update command must never dial the daemon init gate")
	}
}

func TestLegacyPackagedArchiveFailsClosedWhenRuntimeIncomplete(t *testing.T) {
	// The last retained public-style archive predates the complete runtime
	// bundle contract (it has no Headroom). Even an explicit downgrade must not
	// silently remove a component from an existing installation.
	const packagedVersion = "0.6.0"
	archiveName, err := updateArchiveName(packagedVersion, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	archive := filepath.Join("..", "..", "dist", archiveName)
	if _, err := os.Stat(archive); errors.Is(err, os.ErrNotExist) {
		t.Skip("current-platform release archive is not present")
	} else if err != nil {
		t.Fatal(err)
	}
	if _, err := extractAndVerifyUpdateArchive(archive, t.TempDir(), packagedVersion, runtime.GOOS, runtime.GOARCH); err == nil || !strings.Contains(err.Error(), "missing headroom") {
		t.Fatalf("legacy incomplete archive error = %v", err)
	}
}

func withUpdateTestHooks(t *testing.T, executable, goos, goarch string) {
	t.Helper()
	originalExecutable := updateExecutable
	originalEval := updateEvalSymlinks
	originalGOOS, originalGOARCH := updateGOOS, updateGOARCH
	updateExecutable = func() (string, error) { return executable, nil }
	updateEvalSymlinks = func(path string) (string, error) { return path, nil }
	updateGOOS, updateGOARCH = goos, goarch
	t.Cleanup(func() {
		updateExecutable = originalExecutable
		updateEvalSymlinks = originalEval
		updateGOOS, updateGOARCH = originalGOOS, originalGOARCH
	})
}

func prepareOldUpdateInstall(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range requiredUpdateBinaries {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old-"+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type updateDoerFunc func(*http.Request) (*http.Response, error)

func (fn updateDoerFunc) Do(req *http.Request) (*http.Response, error) { return fn(req) }

func installUpdateHTTPFixture(t *testing.T, version, goos, goarch string, archive []byte, checksum string) (string, *int) {
	t.Helper()
	const base = "https://updates.test"
	archiveName := fmt.Sprintf("carina_%s_%s_%s.tar.gz", version, goos, goarch)
	assetHits := 0
	originalClient := updateHTTPClient
	updateHTTPClient = updateDoerFunc(func(r *http.Request) (*http.Response, error) {
		status := http.StatusOK
		var body []byte
		switch r.URL.Path {
		case "/releases/latest", "/releases/tags/v" + version:
			body, _ = json.Marshal(updateRelease{
				TagName: "v" + version,
				Assets: []updateAsset{
					{Name: archiveName, BrowserDownloadURL: base + "/assets/" + archiveName},
					{Name: archiveName + ".sha256", BrowserDownloadURL: base + "/assets/" + archiveName + ".sha256"},
				},
			})
		case "/assets/" + archiveName:
			assetHits++
			body = archive
		case "/assets/" + archiveName + ".sha256":
			assetHits++
			body = []byte(checksum)
		default:
			status = http.StatusNotFound
		}
		return &http.Response{
			StatusCode:    status,
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
			Header:        make(http.Header),
			Request:       r,
		}, nil
	})
	t.Cleanup(func() { updateHTTPClient = originalClient })
	return base, &assetHits
}

func buildUpdateFixture(t *testing.T, version, goos, goarch, warning string) ([]byte, string) {
	t.Helper()
	root := fmt.Sprintf("carina_%s_%s_%s", version, goos, goarch)
	checksums := map[string]string{}
	manifest := updateManifest{Name: "carina", Version: version}
	manifest.Target.GOOS, manifest.Target.GOARCH = goos, goarch
	if warning != "" {
		manifest.Warnings = []string{warning}
	}
	entries := make([]tarFixtureEntry, 0, len(requiredUpdateBinaries)+2)
	for _, name := range requiredUpdateBinaries {
		body := []byte("new-" + name)
		digest := sha256.Sum256(body)
		hash := hex.EncodeToString(digest[:])
		rel := "bin/" + name
		checksums[rel] = hash
		manifest.Files = append(manifest.Files, struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}{Path: rel, SHA256: hash})
		entries = append(entries, tarFixtureEntry{name: root + "/" + rel, body: body, typeflag: tar.TypeReg})
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var checksumText strings.Builder
	names := make([]string, 0, len(checksums))
	for name := range checksums {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		fmt.Fprintf(&checksumText, "%s  %s\n", checksums[name], name)
	}
	entries = append(entries,
		tarFixtureEntry{name: root + "/MANIFEST.json", body: manifestRaw, typeflag: tar.TypeReg},
		tarFixtureEntry{name: root + "/checksums.txt", body: []byte(checksumText.String()), typeflag: tar.TypeReg},
	)
	archivePath := filepath.Join(t.TempDir(), "release.tar.gz")
	writeTarFixture(t, archivePath, entries)
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	archiveName := fmt.Sprintf("carina_%s_%s_%s.tar.gz", version, goos, goarch)
	return archive, fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), archiveName)
}

type tarFixtureEntry struct {
	name     string
	body     []byte
	typeflag byte
}

func writeTarFixture(t *testing.T, destination string, entries []tarFixtureEntry) {
	t.Helper()
	f, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o755, Size: int64(len(entry.body)), Typeflag: entry.typeflag}
		if entry.typeflag == tar.TypeSymlink {
			header.Linkname = "../../outside"
			header.Size = 0
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := io.Copy(tw, bytes.NewReader(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
