package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type gitSnapshotEntry struct {
	Mode fs.FileMode
	Data string
}

func newGitToolRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitToolRun(t, root, "init", "-q")
	gitToolRun(t, root, "config", "user.email", "carina@example.test")
	gitToolRun(t, root, "config", "user.name", "Carina Test")
	gitToolWrite(t, root, "base.txt", "base\n")
	gitToolRun(t, root, "add", "base.txt")
	gitToolRun(t, root, "commit", "-qm", "base")
	return root
}

func gitToolRun(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitToolRunError(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("git %v unexpectedly succeeded: %s", args, out)
	}
	return string(out)
}

func gitToolWrite(t *testing.T, root, path, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func snapshotGitRepository(t *testing.T, root string) map[string]gitSnapshotEntry {
	t.Helper()
	snapshot := map[string]gitSnapshotEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := gitSnapshotEntry{Mode: info.Mode()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			item.Data = "link:" + target
		case info.Mode().IsRegular():
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			item.Data = string(content)
		case info.IsDir():
			item.Data = "directory"
		default:
			return fmt.Errorf("unexpected repository entry %s with mode %s", rel, info.Mode())
		}
		snapshot[filepath.ToSlash(rel)] = item
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertGitRepositoryUnchanged(t *testing.T, root string, call func() error) {
	t.Helper()
	before := snapshotGitRepository(t, root)
	if err := call(); err != nil {
		t.Fatal(err)
	}
	after := snapshotGitRepository(t, root)
	if reflect.DeepEqual(before, after) {
		return
	}
	keys := make([]string, 0, len(before)+len(after))
	seen := map[string]struct{}{}
	for key := range before {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range after {
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !reflect.DeepEqual(before[key], after[key]) {
			t.Fatalf("read-only Git call changed %q: before=%+v after=%+v", key, before[key], after[key])
		}
	}
	t.Fatal("read-only Git call changed the repository")
}

func gitStatusByPath(response gitStatusResponse) map[string]gitStatusEntry {
	out := make(map[string]gitStatusEntry, len(response.Changes))
	for _, change := range response.Changes {
		out[change.Path] = change
	}
	return out
}

func gitDiffByPath(response gitDiffResponse) map[string]gitDiffFile {
	out := make(map[string]gitDiffFile, len(response.Files))
	for _, file := range response.Files {
		out[file.Path] = file
	}
	return out
}

func TestGitStatusBranchStatesAndRepositoryBoundary(t *testing.T) {
	root := newGitToolRepository(t)
	branch := gitToolRun(t, root, "symbolic-ref", "--short", "HEAD")
	gitToolRun(t, root, "branch", "peer")
	gitToolWrite(t, root, "local.txt", "local\n")
	gitToolRun(t, root, "add", "local.txt")
	gitToolRun(t, root, "commit", "-qm", "local")
	gitToolRun(t, root, "checkout", "-q", "peer")
	gitToolWrite(t, root, "peer.txt", "peer\n")
	gitToolRun(t, root, "add", "peer.txt")
	gitToolRun(t, root, "commit", "-qm", "peer")
	gitToolRun(t, root, "checkout", "-q", branch)
	gitToolRun(t, root, "branch", "--set-upstream-to=peer", branch)

	var status gitStatusResponse
	assertGitRepositoryUnchanged(t, root, func() error {
		var err error
		status, err = collectGitStatus(context.Background(), root, 0, nil)
		return err
	})
	if status.Branch.Name != branch || status.Branch.Upstream != "peer" || status.Branch.Ahead != 1 || status.Branch.Behind != 1 {
		t.Fatalf("branch projection = %+v", status.Branch)
	}

	head := gitToolRun(t, root, "rev-parse", "HEAD")
	gitToolRun(t, root, "checkout", "-q", "--detach", head)
	assertGitRepositoryUnchanged(t, root, func() error {
		var err error
		status, err = collectGitStatus(context.Background(), root, 0, nil)
		return err
	})
	if !status.Branch.Detached || status.Branch.OID != head || status.Branch.Name != "" {
		t.Fatalf("detached branch projection = %+v", status.Branch)
	}

	unborn := t.TempDir()
	gitToolRun(t, unborn, "init", "-q")
	assertGitRepositoryUnchanged(t, unborn, func() error {
		var err error
		status, err = collectGitStatus(context.Background(), unborn, 0, nil)
		return err
	})
	if !status.Branch.Unborn || status.Branch.Name == "" || status.Branch.OID != "" {
		t.Fatalf("unborn branch projection = %+v", status.Branch)
	}
	if _, err := collectGitLog(context.Background(), unborn, "head", 1, nil); !errors.Is(err, errGitRevisionUnavailable) {
		t.Fatalf("unborn log error = %v", err)
	}

	bare := t.TempDir()
	gitToolRun(t, bare, "init", "--bare", "-q")
	if _, err := collectGitStatus(context.Background(), bare, 0, nil); !errors.Is(err, errGitBareRepository) {
		t.Fatalf("bare status error = %v", err)
	}
	subdir := filepath.Join(root, "nested")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := collectGitStatus(context.Background(), subdir, 0, nil); !errors.Is(err, errGitNotRepository) {
		t.Fatalf("subdirectory status error = %v", err)
	}
	if _, err := collectGitStatus(context.Background(), t.TempDir(), 0, nil); !errors.Is(err, errGitNotRepository) {
		t.Fatalf("non-repository status error = %v", err)
	}
}

func TestGitStatusKindsBoundsAndPathFilter(t *testing.T) {
	root := newGitToolRepository(t)
	gitToolWrite(t, root, "rename-old.txt", "rename\n")
	gitToolWrite(t, root, "conflict.txt", "base\n")
	gitToolRun(t, root, "add", "rename-old.txt", "conflict.txt")
	gitToolRun(t, root, "commit", "-qm", "fixtures")
	defaultBranch := gitToolRun(t, root, "symbolic-ref", "--short", "HEAD")
	gitToolRun(t, root, "branch", "conflicting")
	gitToolWrite(t, root, "conflict.txt", "main\n")
	gitToolRun(t, root, "add", "conflict.txt")
	gitToolRun(t, root, "commit", "-qm", "main conflict")
	gitToolRun(t, root, "checkout", "-q", "conflicting")
	gitToolWrite(t, root, "conflict.txt", "branch\n")
	gitToolRun(t, root, "add", "conflict.txt")
	gitToolRun(t, root, "commit", "-qm", "branch conflict")
	gitToolRun(t, root, "checkout", "-q", defaultBranch)
	gitToolRunError(t, root, "merge", "--no-edit", "conflicting")
	gitToolRun(t, root, "mv", "rename-old.txt", "rename-new.txt")
	gitToolWrite(t, root, "ordinary.txt", "untracked\n")

	var status gitStatusResponse
	assertGitRepositoryUnchanged(t, root, func() error {
		var err error
		status, err = collectGitStatus(context.Background(), root, maxGitStatusLimit, nil)
		return err
	})
	changes := gitStatusByPath(status)
	if changes["rename-new.txt"].Kind != "rename" || changes["rename-new.txt"].OriginalPath != "rename-old.txt" {
		t.Fatalf("rename projection = %+v", changes["rename-new.txt"])
	}
	if changes["conflict.txt"].Kind != "unmerged" {
		t.Fatalf("conflict projection = %+v", changes["conflict.txt"])
	}
	if changes["ordinary.txt"].Kind != "untracked" {
		t.Fatalf("untracked projection = %+v", changes["ordinary.txt"])
	}
	var diff gitDiffResponse
	assertGitRepositoryUnchanged(t, root, func() error {
		var err error
		diff, err = collectGitDiff(context.Background(), root, "head", nil)
		return err
	})
	diffFiles := gitDiffByPath(diff)
	if diffFiles["rename-new.txt"].OriginalPath != "rename-old.txt" || diffFiles["rename-new.txt"].Patch == "" {
		t.Fatalf("rename diff = %+v", diffFiles["rename-new.txt"])
	}
	if diffFiles["conflict.txt"].Patch == "" {
		t.Fatalf("conflict diff = %+v", diffFiles["conflict.txt"])
	}

	limited, err := collectGitStatus(context.Background(), root, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Changes) != 1 || !limited.Truncated || limited.Limit != 1 {
		t.Fatalf("bounded status = %+v", limited)
	}
	filtered, err := collectGitStatus(context.Background(), root, 10, []string{"ordinary.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Changes) != 1 || filtered.Changes[0].Path != "ordinary.txt" {
		t.Fatalf("filtered status = %+v", filtered)
	}
	if got, truncated := boundedGitPath(strings.Repeat("x", maxGitPathBytes+7)); len([]byte(got)) != maxGitPathBytes || !truncated {
		t.Fatalf("bounded path bytes=%d truncated=%v", len([]byte(got)), truncated)
	}
}

func TestGitDiffViewsBinaryUntrackedSymlinkAndLimits(t *testing.T) {
	root := newGitToolRepository(t)
	large := strings.Repeat("a", maxGitDiffFileBytes+4096) + "\n"
	for path, content := range map[string]string{
		"worktree.txt": "before\n", "staged.txt": "before\n", "both.txt": "before\n", "large.txt": large,
	} {
		gitToolWrite(t, root, path, content)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 1, 2}, 0600); err != nil {
		t.Fatal(err)
	}
	gitToolRun(t, root, "add", ".")
	gitToolRun(t, root, "commit", "-qm", "diff fixtures")

	gitToolWrite(t, root, "worktree.txt", "after-worktree\n")
	gitToolWrite(t, root, "staged.txt", "after-staged\n")
	gitToolRun(t, root, "add", "staged.txt")
	gitToolWrite(t, root, "both.txt", "after-index\n")
	gitToolRun(t, root, "add", "both.txt")
	gitToolWrite(t, root, "both.txt", "after-worktree-too\n")
	gitToolWrite(t, root, "large.txt", strings.Repeat("b", maxGitDiffFileBytes+4096)+"\n")
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 3, 4}, 0600); err != nil {
		t.Fatal(err)
	}
	gitToolWrite(t, root, "untracked.txt", "untracked secret stays omitted\n")
	outside := filepath.Join(t.TempDir(), "outside-secret.txt")
	if err := os.WriteFile(outside, []byte("outside secret must not be read"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink fixture unavailable: %v", err)
		}
		t.Fatal(err)
	}

	responses := map[string]gitDiffResponse{}
	for _, view := range []string{"worktree", "staged", "head"} {
		view := view
		assertGitRepositoryUnchanged(t, root, func() error {
			var err error
			responses[view], err = collectGitDiff(context.Background(), root, view, nil)
			return err
		})
	}
	worktree := gitDiffByPath(responses["worktree"])
	if _, ok := worktree["staged.txt"]; ok {
		t.Fatal("worktree diff included a staged-only change")
	}
	if !strings.Contains(worktree["worktree.txt"].Patch, "+after-worktree") || !strings.Contains(worktree["both.txt"].Patch, "+after-worktree-too") {
		t.Fatalf("worktree diff = %+v", responses["worktree"])
	}
	if !worktree["binary.bin"].Binary || !worktree["binary.bin"].ContentOmitted || worktree["binary.bin"].Patch != "" {
		t.Fatalf("binary diff = %+v", worktree["binary.bin"])
	}
	for _, path := range []string{"untracked.txt", "outside-link"} {
		file := worktree[path]
		if !file.Untracked || !file.ContentOmitted || file.Patch != "" {
			t.Fatalf("untracked diff %s = %+v", path, file)
		}
	}
	if strings.Contains(fmt.Sprintf("%+v", responses), "outside secret must not be read") {
		t.Fatal("symlink target content leaked into Git diff")
	}
	if !worktree["large.txt"].Truncated || len([]byte(worktree["large.txt"].Patch)) > maxGitDiffFileBytes || !responses["worktree"].Truncated {
		t.Fatalf("large diff limits = file:%+v response:%+v", worktree["large.txt"], responses["worktree"])
	}

	staged := gitDiffByPath(responses["staged"])
	if _, ok := staged["worktree.txt"]; ok {
		t.Fatal("staged diff included a worktree-only change")
	}
	if _, ok := staged["untracked.txt"]; ok {
		t.Fatal("staged diff included an untracked file")
	}
	if !strings.Contains(staged["staged.txt"].Patch, "+after-staged") || !strings.Contains(staged["both.txt"].Patch, "+after-index") {
		t.Fatalf("staged diff = %+v", responses["staged"])
	}

	head := gitDiffByPath(responses["head"])
	for _, path := range []string{"worktree.txt", "staged.txt", "both.txt", "binary.bin", "untracked.txt", "outside-link"} {
		if _, ok := head[path]; !ok {
			t.Fatalf("head diff omitted %q: %+v", path, responses["head"])
		}
	}
	filtered, err := collectGitDiff(context.Background(), root, "worktree", []string{"worktree.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Files) != 1 || filtered.Files[0].Path != "worktree.txt" {
		t.Fatalf("filtered diff = %+v", filtered)
	}
}

func TestGitTruncationNeverReturnsPartialRecordsOrMislabelsUTF8(t *testing.T) {
	statusRecord := "2 R. N... 100644 100644 100644 aaaaaaa bbbbbbb R100 renamed.txt\x00partial-original"
	status, err := parseGitStatusV2([]byte(statusRecord), 10, true)
	if err != nil || len(status.Changes) != 0 || !status.Truncated {
		t.Fatalf("partial rename status = %+v, err=%v", status, err)
	}
	if _, err := parseGitStatusV2([]byte(statusRecord), 10, false); err == nil {
		t.Fatal("non-truncated parser accepted an unterminated rename")
	}

	text := append([]byte("valid UTF-8: "), []byte("界")...)
	if gitDiffOutputIsBinary(text[:len(text)-1], false) == false {
		t.Fatal("invalid complete UTF-8 output was accepted as text")
	}
	if gitDiffOutputIsBinary(text[:len(text)-1], true) {
		t.Fatal("a truncated final UTF-8 rune was mislabeled as binary")
	}

	root := newGitToolRepository(t)
	gitToolWrite(t, root, "utf8.txt", strings.Repeat("界", maxGitDiffFileBytes/3+4096)+"\n")
	gitToolRun(t, root, "add", "utf8.txt")
	gitToolRun(t, root, "commit", "-qm", "utf8 base")
	gitToolWrite(t, root, "utf8.txt", strings.Repeat("测", maxGitDiffFileBytes/3+4096)+"\n")
	var diff gitDiffResponse
	assertGitRepositoryUnchanged(t, root, func() error {
		var collectErr error
		diff, collectErr = collectGitDiff(context.Background(), root, "worktree", nil)
		return collectErr
	})
	file := gitDiffByPath(diff)["utf8.txt"]
	if file.Binary || !file.Truncated || !utf8.ValidString(file.Patch) || len([]byte(file.Patch)) > maxGitDiffFileBytes {
		t.Fatalf("truncated UTF-8 diff = %+v", file)
	}
}

func TestGitDiffFileAndAggregateLimits(t *testing.T) {
	t.Run("file count", func(t *testing.T) {
		root := newGitToolRepository(t)
		for index := 0; index < maxGitDiffFiles+2; index++ {
			gitToolWrite(t, root, fmt.Sprintf("many/%03d.txt", index), "before\n")
		}
		gitToolRun(t, root, "add", "many")
		gitToolRun(t, root, "commit", "-qm", "many files")
		for index := 0; index < maxGitDiffFiles+2; index++ {
			gitToolWrite(t, root, fmt.Sprintf("many/%03d.txt", index), "after\n")
		}
		var diff gitDiffResponse
		assertGitRepositoryUnchanged(t, root, func() error {
			var err error
			diff, err = collectGitDiff(context.Background(), root, "worktree", nil)
			return err
		})
		if len(diff.Files) != maxGitDiffFiles || !diff.Truncated {
			t.Fatalf("file-count limit = files:%d truncated:%v", len(diff.Files), diff.Truncated)
		}
	})

	t.Run("aggregate bytes", func(t *testing.T) {
		root := newGitToolRepository(t)
		before := strings.Repeat("a", maxGitDiffFileBytes+4096) + "\n"
		after := strings.Repeat("b", maxGitDiffFileBytes+4096) + "\n"
		for index := 0; index < 5; index++ {
			gitToolWrite(t, root, fmt.Sprintf("large/%d.txt", index), before)
		}
		gitToolRun(t, root, "add", "large")
		gitToolRun(t, root, "commit", "-qm", "large files")
		for index := 0; index < 5; index++ {
			gitToolWrite(t, root, fmt.Sprintf("large/%d.txt", index), after)
		}
		var diff gitDiffResponse
		assertGitRepositoryUnchanged(t, root, func() error {
			var err error
			diff, err = collectGitDiff(context.Background(), root, "worktree", nil)
			return err
		})
		if diff.TotalBytes > maxGitDiffTotalBytes || !diff.Truncated || len(diff.Files) >= 5 {
			t.Fatalf("aggregate limit = files:%d bytes:%d truncated:%v", len(diff.Files), diff.TotalBytes, diff.Truncated)
		}
	})
}

func TestGitLogStructuredBoundedAndFiltered(t *testing.T) {
	root := newGitToolRepository(t)
	gitToolWrite(t, root, "only.txt", "one\n")
	gitToolRun(t, root, "add", "only.txt")
	gitToolRun(t, root, "commit", "-qm", "only path")
	gitToolWrite(t, root, "other.txt", "two\n")
	gitToolRun(t, root, "add", "other.txt")
	longSubject := strings.Repeat("subject ", maxGitLogFieldBytes/4)
	gitToolRun(t, root, "commit", "-qm", longSubject)

	var log gitLogResponse
	assertGitRepositoryUnchanged(t, root, func() error {
		var err error
		log, err = collectGitLog(context.Background(), root, "head", 2, nil)
		return err
	})
	if len(log.Commits) != 2 || !log.Truncated || log.Limit != 2 || log.Revision != "head" {
		t.Fatalf("bounded log = %+v", log)
	}
	latest := log.Commits[0]
	if len(latest.OID) < 40 || latest.Author.Name != "Carina Test" || latest.Author.Email != "carina@example.test" || latest.AuthoredAt == "" || len(latest.Parents) != 1 {
		t.Fatalf("structured commit = %+v", latest)
	}
	if !latest.SubjectTruncated || len([]byte(latest.Subject)) > maxGitLogFieldBytes {
		t.Fatalf("subject bound = bytes:%d commit:%+v", len([]byte(latest.Subject)), latest)
	}
	filtered, err := collectGitLog(context.Background(), root, "", 10, []string{"only.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Commits) != 1 || filtered.Commits[0].Subject != "only path" || filtered.Truncated {
		t.Fatalf("filtered log = %+v", filtered)
	}
}

func TestGitArgumentsErrorsMissingBinaryTimeoutAndCancel(t *testing.T) {
	root := newGitToolRepository(t)
	invalidPaths := [][]string{
		{""}, {"../escape"}, {"a/../b"}, {".git/config"}, {filepath.Join(root, "base.txt")},
		make([]string, maxGitPathFilters+1),
	}
	for _, paths := range invalidPaths {
		if _, err := normalizeGitPathFilters(paths); err == nil {
			t.Fatalf("paths unexpectedly accepted: %#v", paths)
		}
	}
	if _, err := collectGitDiff(context.Background(), root, "raw", nil); err == nil || !strings.Contains(err.Error(), "view must be") {
		t.Fatalf("invalid view error = %v", err)
	}
	if _, err := collectGitLog(context.Background(), root, "HEAD~1", 1, nil); err == nil || !strings.Contains(err.Error(), "revision must be head") {
		t.Fatalf("invalid revision error = %v", err)
	}
	if _, err := collectGitLog(context.Background(), root, "head", maxGitLogCommits+1, nil); err == nil {
		t.Fatal("oversized log request was accepted")
	}
	if _, err := collectGitStatus(context.Background(), root, maxGitStatusScanRows+1, nil); err == nil {
		t.Fatal("oversized status request was accepted")
	}

	missing := filepath.Join(t.TempDir(), "missing-git")
	_, err := runReadOnlyGitBinary(context.Background(), missing, root, 32, "status")
	if err == nil || (!errors.Is(err, exec.ErrNotFound) && !errors.Is(err, os.ErrNotExist)) {
		t.Fatalf("missing Git error = %v", err)
	}
	outcome := gitToolErrorOutcome(context.Background(), err)
	if outcome.errorCategory != "git_unavailable" || outcome.observationError == nil || outcome.observationError.Category != toolCategoryUnavailable {
		t.Fatalf("missing Git outcome = %+v", outcome)
	}

	if runtime.GOOS != "windows" {
		slowGit := filepath.Join(t.TempDir(), "slow-git")
		if err := os.WriteFile(slowGit, []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err = runReadOnlyGitBinary(ctx, slowGit, root, 32, "status")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout error = %v", err)
		}
		outcome = gitToolErrorOutcome(ctx, err)
		if outcome.status != "timed_out" || outcome.observationError.Category != toolCategoryTimeout {
			t.Fatalf("timeout outcome = %+v", outcome)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runReadOnlyGit(cancelled, root, 32, "status")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	outcome = gitToolErrorOutcome(cancelled, err)
	if outcome.status != "cancelled" || outcome.observationError.Category != toolCategoryCancelled {
		t.Fatalf("cancel outcome = %+v", outcome)
	}

	for err, category := range map[error]string{
		errGitNotRepository:       "git_not_repository",
		errGitBareRepository:      "git_bare_repository",
		errGitWorkspaceEscape:     "git_workspace_escape",
		errGitRevisionUnavailable: "git_revision_unavailable",
		errGitUnsafeConfiguration: "git_unsafe_configuration",
	} {
		if got := gitToolErrorOutcome(context.Background(), err); got.errorCategory != category || got.observationError == nil {
			t.Fatalf("error %v outcome = %+v", err, got)
		}
	}
}

func TestGitRejectsExternalMetadataAndConfigIncludes(t *testing.T) {
	t.Run("separate git dir", func(t *testing.T) {
		root := t.TempDir()
		gitDir := filepath.Join(t.TempDir(), "metadata.git")
		gitToolRun(t, root, "init", "-q", "--separate-git-dir", gitDir)
		rootBefore := snapshotGitRepository(t, root)
		metadataBefore := snapshotGitRepository(t, gitDir)
		if _, err := collectGitStatus(context.Background(), root, 10, nil); !errors.Is(err, errGitWorkspaceEscape) {
			t.Fatalf("separate git-dir status error = %v", err)
		}
		if _, err := collectWorkspaceDiff(root); !errors.Is(err, errGitWorkspaceEscape) {
			t.Fatalf("separate git-dir workspace diff error = %v", err)
		}
		if !reflect.DeepEqual(rootBefore, snapshotGitRepository(t, root)) || !reflect.DeepEqual(metadataBefore, snapshotGitRepository(t, gitDir)) {
			t.Fatal("rejected separate git-dir inspection changed repository bytes")
		}
	})

	t.Run("external objects", func(t *testing.T) {
		root := newGitToolRepository(t)
		objects := filepath.Join(root, ".git", "objects")
		externalObjects := filepath.Join(t.TempDir(), "objects")
		if err := os.Rename(objects, externalObjects); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(externalObjects, objects); err != nil {
			t.Fatal(err)
		}
		if _, err := collectGitStatus(context.Background(), root, 10, nil); !errors.Is(err, errGitWorkspaceEscape) {
			t.Fatalf("external objects status error = %v", err)
		}
	})

	t.Run("external alternate objects", func(t *testing.T) {
		root := newGitToolRepository(t)
		external := newGitToolRepository(t)
		alternates := filepath.Join(root, ".git", "objects", "info", "alternates")
		if err := os.WriteFile(alternates, []byte(filepath.Join(external, ".git", "objects")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := collectGitLog(context.Background(), root, "head", 10, nil); !errors.Is(err, errGitWorkspaceEscape) {
			t.Fatalf("external alternates log error = %v", err)
		}
	})

	t.Run("local config include", func(t *testing.T) {
		root := newGitToolRepository(t)
		included := filepath.Join(t.TempDir(), "outside.gitconfig")
		if err := os.WriteFile(included, []byte("[filter \"outside\"]\n\tclean = false\n"), 0600); err != nil {
			t.Fatal(err)
		}
		gitToolRun(t, root, "config", "--local", "include.path", included)
		before := snapshotGitRepository(t, root)
		if _, err := collectGitStatus(context.Background(), root, 10, nil); !errors.Is(err, errGitUnsafeConfiguration) {
			t.Fatalf("local include status error = %v", err)
		}
		if !reflect.DeepEqual(before, snapshotGitRepository(t, root)) {
			t.Fatal("rejected local include changed repository bytes")
		}
		outcome := gitToolErrorOutcome(context.Background(), errGitUnsafeConfiguration)
		if outcome.errorCategory != "git_unsafe_configuration" || outcome.observationError == nil || outcome.observationError.Category != toolCategoryPermission {
			t.Fatalf("unsafe config outcome = %+v", outcome)
		}
	})
}

func TestGitToolSchemasRejectRawArguments(t *testing.T) {
	for _, name := range []string{"git.status", "git.diff", "git.log"} {
		descriptor, ok := defaultBuiltinTools.lookup(name)
		if !ok {
			t.Fatalf("descriptor %q missing", name)
		}
		properties := descriptor.Schema["properties"].(map[string]any)
		if _, ok := properties["args"]; ok || descriptor.Schema["additionalProperties"] != false {
			t.Fatalf("%s accepts raw or unknown arguments: %#v", name, descriptor.Schema)
		}
		if descriptor.Effect != builtinToolEffectRead || descriptor.PlanMode != builtinToolPlanAllowed || !reflect.DeepEqual(descriptor.Capabilities, []string{"FileRead"}) {
			t.Fatalf("%s governance = %+v", name, descriptor)
		}
	}
}

func TestGitToolsDispatchThroughGovernanceInEveryRegistryMode(t *testing.T) {
	d, root := newLoopDaemon(t)
	defer d.Close()
	gitToolRun(t, root, "init", "-q")
	gitToolRun(t, root, "config", "user.email", "carina@example.test")
	gitToolRun(t, root, "config", "user.name", "Carina Test")
	gitToolWrite(t, root, "base.txt", "before\n")
	gitToolRun(t, root, "add", "base.txt")
	gitToolRun(t, root, "commit", "-qm", "base")
	gitToolWrite(t, root, "base.txt", "after\n")

	sess, err := d.store.CreateSession(root, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, root, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect repository evidence")
	for _, mode := range []builtinToolRegistryMode{builtinToolRegistryDescriptor, builtinToolRegistryShadow, builtinToolRegistryLegacy} {
		t.Run(string(mode), func(t *testing.T) {
			d.builtinToolsMode = mode
			for _, act := range []*action{
				{Tool: "git.status", Limit: 10, Intent: "inspect status"},
				{Tool: "git.diff", GitView: "worktree", Intent: "inspect patch"},
				{Tool: "git.log", GitRevision: "head", MaxCommits: 2, Intent: "inspect history"},
			} {
				act := act
				assertGitRepositoryUnchanged(t, root, func() error {
					outcome := d.dispatchBuiltinActionOutcome(sess, task, act)
					if outcome.status != "completed" || outcome.observationError != nil {
						return fmt.Errorf("%s outcome = %+v", act.Tool, outcome)
					}
					var decoded map[string]any
					if err := json.Unmarshal([]byte(outcome.display), &decoded); err != nil {
						return fmt.Errorf("%s observation is not JSON: %w", act.Tool, err)
					}
					if len(decoded) == 0 {
						return fmt.Errorf("%s observation is empty", act.Tool)
					}
					return nil
				})
			}
		})
	}
}

func TestGitHostileConfigurationCannotExecuteHelpersOrMutate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hostile executable fixture uses POSIX scripts")
	}
	root := newGitToolRepository(t)
	gitToolWrite(t, root, ".gitattributes", "*.txt diff=evil filter=evil\n")
	gitToolRun(t, root, "add", ".gitattributes")
	gitToolRun(t, root, "commit", "-qm", "attributes")

	helperDir := t.TempDir()
	helper := filepath.Join(helperDir, "sentinel-helper")
	invoked := helper + ".invoked"
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n: > \"$0.invoked\"\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(helperDir, "hooks")
	if err := os.Mkdir(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"post-checkout", "post-index-change", "pre-commit"} {
		path := filepath.Join(hooks, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n: > \"$0.invoked\"\nexit 97\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}

	for key, value := range map[string]string{
		"core.fsmonitor":       helper,
		"core.hooksPath":       hooks,
		"core.pager":           helper,
		"pager.status":         helper,
		"pager.diff":           helper,
		"pager.log":            helper,
		"diff.external":        helper,
		"diff.evil.textconv":   helper,
		"filter.evil.clean":    helper,
		"filter.evil.smudge":   helper,
		"filter.evil.process":  helper,
		"filter.evil.required": "true",
		"credential.helper":    helper,
		"submodule.recurse":    "true",
		"alias.hostile":        "!" + helper,
	} {
		gitToolRun(t, root, "config", "--local", key, value)
	}
	globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
	systemConfig := filepath.Join(t.TempDir(), "system.gitconfig")
	configBody := fmt.Sprintf("[core]\n\tpager = %s\n\tfsmonitor = %s\n[diff]\n\texternal = %s\n[credential]\n\thelper = %s\n", helper, helper, helper, helper)
	if err := os.WriteFile(globalConfig, []byte(configBody), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(systemConfig, []byte(configBody), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_SYSTEM", systemConfig)
	gitToolWrite(t, root, "base.txt", "hostile config must remain inert\n")

	assertNoHelper := func() error {
		if _, err := os.Stat(invoked); err == nil {
			return errors.New("configured helper executed")
		} else if !os.IsNotExist(err) {
			return err
		}
		matches, err := filepath.Glob(filepath.Join(hooks, "*.invoked"))
		if err != nil {
			return err
		}
		if len(matches) != 0 {
			return fmt.Errorf("configured hook executed: %v", matches)
		}
		return nil
	}
	for name, call := range map[string]func() error{
		"status": func() error { _, err := collectGitStatus(context.Background(), root, 10, nil); return err },
		"diff":   func() error { _, err := collectGitDiff(context.Background(), root, "worktree", nil); return err },
		"log":    func() error { _, err := collectGitLog(context.Background(), root, "head", 10, nil); return err },
	} {
		name, call := name, call
		t.Run(name, func(t *testing.T) {
			assertGitRepositoryUnchanged(t, root, func() error {
				if err := call(); err != nil {
					return err
				}
				return assertNoHelper()
			})
		})
	}
}
