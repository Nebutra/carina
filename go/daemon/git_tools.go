package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	defaultGitStatusLimit = 100
	maxGitStatusLimit     = 200
	maxGitStatusScanRows  = 512
	maxGitStatusBytes     = 2 << 20
	maxGitPathFilters     = 32
	maxGitPathBytes       = 1024

	maxGitDiffFiles      = 64
	maxGitDiffFileBytes  = 256 << 10
	maxGitDiffTotalBytes = 1 << 20

	defaultGitLogCommits = 20
	maxGitLogCommits     = 100
	maxGitLogBytes       = 512 << 10
	maxGitLogFieldBytes  = 512

	maxGitProbeBytes      = 4096
	maxGitFilterConfig    = 64 << 10
	maxGitFilterDrivers   = 128
	maxGitStderrBytes     = 8192
	legacyGitOutputLimit  = 8 << 20
	defaultGitToolTimeout = 30 * time.Second
	gitLogRecordMarker    = "CARINA_GIT_RECORD_v1"
)

var (
	errGitNotRepository       = errors.New("workspace is not a Git repository")
	errGitBareRepository      = errors.New("bare Git repositories are not supported")
	errGitWorkspaceEscape     = errors.New("Git worktree escapes the session workspace")
	errGitRevisionUnavailable = errors.New("requested Git revision is unavailable")
	errGitUnsafeConfiguration = errors.New("Git local config includes are unsupported for workspace-contained inspection")
)

type boundedGitWriter struct {
	buffer    bytes.Buffer
	limit     int
	seen      int64
	truncated bool
}

func (w *boundedGitWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.seen += int64(n)
	remaining := w.limit - w.buffer.Len()
	if remaining > 0 {
		if remaining < len(p) {
			_, _ = w.buffer.Write(p[:remaining])
		} else {
			_, _ = w.buffer.Write(p)
		}
	}
	if w.seen > int64(w.limit) {
		w.truncated = true
	}
	return n, nil
}

type gitCommandResult struct {
	Stdout          []byte
	StdoutBytes     int64
	StdoutTruncated bool
	Stderr          string
}

type gitCommandError struct {
	Subcommand string
	Stderr     string
	Err        error
}

func (e *gitCommandError) Error() string {
	detail := strings.TrimSpace(e.Stderr)
	if detail == "" && e.Err != nil {
		detail = e.Err.Error()
	}
	if detail == "" {
		detail = "command failed"
	}
	return fmt.Sprintf("git %s: %s", e.Subcommand, detail)
}

func (e *gitCommandError) Unwrap() error { return e.Err }

func readOnlyGit(ctx context.Context, root string, args ...string) ([]byte, error) {
	result, err := runReadOnlyGit(ctx, root, legacyGitOutputLimit, args...)
	return result.Stdout, err
}

func runReadOnlyGit(ctx context.Context, root string, outputLimit int, args ...string) (gitCommandResult, error) {
	return runReadOnlyGitBinary(ctx, "git", root, outputLimit, args...)
}

func runReadOnlyGitBinary(ctx context.Context, binary, root string, outputLimit int, args ...string) (gitCommandResult, error) {
	if len(args) == 0 {
		return gitCommandResult{}, fmt.Errorf("Git subcommand is required")
	}
	if outputLimit < 1 {
		return gitCommandResult{}, fmt.Errorf("Git output limit must be positive")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("workspace is not a directory")
		}
		return gitCommandResult{}, err
	}

	baseArgs := gitReadOnlyBaseArgs()
	if args[0] == "status" || args[0] == "diff" {
		filterOverrides, filterErr := gitFilterOverrides(ctx, binary, root)
		if filterErr != nil {
			return gitCommandResult{}, filterErr
		}
		baseArgs = append(baseArgs, filterOverrides...)
	}
	argv := append(baseArgs, args...)
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Dir = root
	cmd.Env = gitReadOnlyEnvironment()
	stdout := &boundedGitWriter{limit: outputLimit}
	stderr := &boundedGitWriter{limit: maxGitStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	result := gitCommandResult{
		Stdout:          append([]byte(nil), stdout.buffer.Bytes()...),
		StdoutBytes:     stdout.seen,
		StdoutTruncated: stdout.truncated,
		Stderr:          strings.ToValidUTF8(stderr.buffer.String(), "?"),
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if err != nil {
		return result, &gitCommandError{Subcommand: args[0], Stderr: result.Stderr, Err: err}
	}
	return result, nil
}

func gitReadOnlyBaseArgs() []string {
	return []string{
		"--no-pager",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.attributesFile=" + os.DevNull,
		"-c", "core.excludesFile=" + os.DevNull,
		"-c", "core.alternateRefsCommand=",
		"-c", "core.askPass=",
		"-c", "credential.helper=",
		"-c", "credential.interactive=false",
		"-c", "diff.external=",
		"-c", "diff.trustExitCode=false",
		"-c", "diff.autoRefreshIndex=false",
		"-c", "diff.orderFile=" + os.DevNull,
		"-c", "maintenance.auto=false",
		"-c", "gc.auto=0",
		"-c", "protocol.allow=never",
		"-c", "submodule.recurse=false",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "status.submoduleSummary=false",
		"-c", "color.ui=false",
		"-c", "core.quotePath=true",
	}
}

func gitFilterOverrides(ctx context.Context, binary, root string) ([]string, error) {
	result, err := runReadOnlyGitBinary(ctx, binary, root, maxGitFilterConfig,
		"config", "--local", "--null", "--name-only", "--get-regexp", `^filter\..*\.(clean|smudge|process|required)$`)
	if err != nil {
		var commandErr *gitCommandError
		var exitErr *exec.ExitError
		if errors.As(err, &commandErr) && errors.As(commandErr.Err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	if result.StdoutTruncated {
		return nil, fmt.Errorf("Git filter configuration exceeds %d bytes", maxGitFilterConfig)
	}
	drivers := make(map[string]struct{})
	for _, rawName := range bytes.Split(result.Stdout, []byte{0}) {
		name := string(rawName)
		lower := strings.ToLower(name)
		for _, suffix := range []string{".clean", ".smudge", ".process", ".required"} {
			if !strings.HasPrefix(lower, "filter.") || !strings.HasSuffix(lower, suffix) {
				continue
			}
			driver := name[:len(name)-len(suffix)]
			if len(driver) <= len("filter.") {
				continue
			}
			drivers[driver] = struct{}{}
			break
		}
	}
	if len(drivers) > maxGitFilterDrivers {
		return nil, fmt.Errorf("Git config defines more than %d filter drivers", maxGitFilterDrivers)
	}
	names := make([]string, 0, len(drivers))
	for name := range drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	overrides := make([]string, 0, len(names)*8)
	for _, name := range names {
		for _, setting := range []string{"clean=", "smudge=", "process=", "required=false"} {
			overrides = append(overrides, "-c", name+"."+setting)
		}
	}
	return overrides, nil
}

func gitReadOnlyEnvironment() []string {
	env := make([]string, 0, 24)
	for _, key := range []string{"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "TMPDIR", "TMP", "TEMP"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env,
		"LC_ALL=C",
		"LANG=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_EXTERNAL_DIFF=",
		"GIT_LITERAL_PATHSPECS=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_PROTOCOL_FROM_USER=0",
	)
}

type gitBranchProjection struct {
	Name     string `json:"name,omitempty"`
	OID      string `json:"oid,omitempty"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	Detached bool   `json:"detached"`
	Unborn   bool   `json:"unborn"`
}

type gitStatusEntry struct {
	Path              string `json:"path"`
	OriginalPath      string `json:"original_path,omitempty"`
	IndexStatus       string `json:"index_status"`
	WorktreeStatus    string `json:"worktree_status"`
	Kind              string `json:"kind"`
	Submodule         string `json:"submodule,omitempty"`
	PathTruncated     bool   `json:"path_truncated,omitempty"`
	OriginalTruncated bool   `json:"original_path_truncated,omitempty"`

	rawPath         string `json:"-"`
	rawOriginalPath string `json:"-"`
}

type gitStatusResponse struct {
	Branch    gitBranchProjection `json:"branch"`
	Changes   []gitStatusEntry    `json:"changes"`
	Truncated bool                `json:"truncated"`
	Limit     int                 `json:"limit"`
}

func collectGitStatus(ctx context.Context, root string, limit int, paths []string) (gitStatusResponse, error) {
	if err := validateGitWorkspace(ctx, root); err != nil {
		return gitStatusResponse{}, err
	}
	if limit == 0 {
		limit = defaultGitStatusLimit
	}
	if limit < 1 || limit > maxGitStatusScanRows {
		return gitStatusResponse{}, fmt.Errorf("status limit must be between 1 and %d", maxGitStatusScanRows)
	}
	normalized, err := normalizeGitPathFilters(paths)
	if err != nil {
		return gitStatusResponse{}, err
	}
	args := []string{"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all", "--ignore-submodules=all", "--renames"}
	if len(normalized) > 0 {
		args = append(args, "--")
		args = append(args, normalized...)
	}
	result, err := runReadOnlyGit(ctx, root, maxGitStatusBytes, args...)
	if err != nil {
		return gitStatusResponse{}, err
	}
	response, err := parseGitStatusV2(result.Stdout, limit, result.StdoutTruncated)
	if err != nil {
		return gitStatusResponse{}, err
	}
	response.Truncated = response.Truncated || result.StdoutTruncated
	response.Limit = limit
	return response, nil
}

func parseGitStatusV2(raw []byte, limit int, allowTrailingPartial bool) (gitStatusResponse, error) {
	response := gitStatusResponse{Changes: []gitStatusEntry{}, Limit: limit}
	records := bytes.Split(raw, []byte{0})
	for index := 0; index < len(records); index++ {
		record := string(records[index])
		if record == "" {
			continue
		}
		recordComplete := index < len(records)-1 || bytes.HasSuffix(raw, []byte{0})
		if allowTrailingPartial && !recordComplete {
			response.Truncated = true
			break
		}
		if !recordComplete {
			return gitStatusResponse{}, fmt.Errorf("unterminated Git status record")
		}
		switch {
		case strings.HasPrefix(record, "# branch.oid "):
			oid := strings.TrimPrefix(record, "# branch.oid ")
			if oid == "(initial)" {
				response.Branch.Unborn = true
			} else {
				response.Branch.OID = oid
			}
		case strings.HasPrefix(record, "# branch.head "):
			head := strings.TrimPrefix(record, "# branch.head ")
			if head == "(detached)" {
				response.Branch.Detached = true
			} else {
				response.Branch.Name = head
			}
		case strings.HasPrefix(record, "# branch.upstream "):
			response.Branch.Upstream = strings.TrimPrefix(record, "# branch.upstream ")
		case strings.HasPrefix(record, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(record, "# branch.ab "))
			if len(fields) == 2 {
				response.Branch.Ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
				response.Branch.Behind, _ = strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
			}
		case strings.HasPrefix(record, "1 "):
			fields := strings.SplitN(record, " ", 9)
			if len(fields) != 9 || len(fields[1]) != 2 {
				return gitStatusResponse{}, fmt.Errorf("invalid Git ordinary status record")
			}
			appendGitStatusEntry(&response, limit, fields[8], "", fields[1], "ordinary", fields[2])
		case strings.HasPrefix(record, "2 "):
			fields := strings.SplitN(record, " ", 10)
			originalComplete := index+1 < len(records)-1 || bytes.HasSuffix(raw, []byte{0})
			if allowTrailingPartial && (!originalComplete || index+1 >= len(records) || len(records[index+1]) == 0) {
				response.Truncated = true
				return response, nil
			}
			if !originalComplete {
				return gitStatusResponse{}, fmt.Errorf("unterminated Git rename status record")
			}
			if len(fields) != 10 || len(fields[1]) != 2 || index+1 >= len(records) || len(records[index+1]) == 0 {
				return gitStatusResponse{}, fmt.Errorf("invalid Git rename status record")
			}
			index++
			kind := "rename"
			if strings.HasPrefix(fields[8], "C") {
				kind = "copy"
			}
			appendGitStatusEntry(&response, limit, fields[9], string(records[index]), fields[1], kind, fields[2])
		case strings.HasPrefix(record, "u "):
			fields := strings.SplitN(record, " ", 11)
			if len(fields) != 11 || len(fields[1]) != 2 {
				return gitStatusResponse{}, fmt.Errorf("invalid Git unmerged status record")
			}
			appendGitStatusEntry(&response, limit, fields[10], "", fields[1], "unmerged", fields[2])
		case strings.HasPrefix(record, "? "):
			appendGitStatusEntry(&response, limit, strings.TrimPrefix(record, "? "), "", "??", "untracked", "")
		}
	}
	return response, nil
}

func appendGitStatusEntry(response *gitStatusResponse, limit int, path, original, status, kind, submodule string) {
	if len(response.Changes) >= limit {
		response.Truncated = true
		return
	}
	boundedPath, pathTruncated := boundedGitPath(path)
	boundedOriginal, originalTruncated := boundedGitPath(original)
	response.Changes = append(response.Changes, gitStatusEntry{
		Path: boundedPath, OriginalPath: boundedOriginal,
		IndexStatus: status[:1], WorktreeStatus: status[1:2], Kind: kind, Submodule: submodule,
		PathTruncated: pathTruncated, OriginalTruncated: originalTruncated,
		rawPath: path, rawOriginalPath: original,
	})
}

func boundedGitPath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	valid := strings.ToValidUTF8(path, "?")
	truncated := valid != path || len([]byte(valid)) > maxGitPathBytes
	return truncateUTF8Bytes(valid, maxGitPathBytes), truncated
}

type gitDiffFile struct {
	Path           string `json:"path"`
	OriginalPath   string `json:"original_path,omitempty"`
	Status         string `json:"status"`
	Untracked      bool   `json:"untracked,omitempty"`
	Binary         bool   `json:"binary"`
	ContentOmitted bool   `json:"content_omitted,omitempty"`
	Truncated      bool   `json:"truncated"`
	Bytes          int64  `json:"bytes"`
	Patch          string `json:"patch,omitempty"`
}

type gitDiffResponse struct {
	View       string         `json:"view"`
	Files      []gitDiffFile  `json:"files"`
	Truncated  bool           `json:"truncated"`
	TotalBytes int            `json:"total_bytes"`
	Limits     map[string]int `json:"limits"`
}

func collectGitDiff(ctx context.Context, root, view string, paths []string) (gitDiffResponse, error) {
	view = strings.ToLower(strings.TrimSpace(view))
	if view != "worktree" && view != "staged" && view != "head" {
		return gitDiffResponse{}, fmt.Errorf("view must be worktree, staged, or head")
	}
	normalized, err := normalizeGitPathFilters(paths)
	if err != nil {
		return gitDiffResponse{}, err
	}
	status, err := collectGitStatus(ctx, root, maxGitStatusScanRows, normalized)
	if err != nil {
		return gitDiffResponse{}, err
	}
	if view == "head" && status.Branch.Unborn {
		return gitDiffResponse{}, errGitRevisionUnavailable
	}
	response := gitDiffResponse{
		View: view, Files: []gitDiffFile{},
		Limits: map[string]int{"files": maxGitDiffFiles, "per_file_bytes": maxGitDiffFileBytes, "total_bytes": maxGitDiffTotalBytes},
	}
	for _, change := range status.Changes {
		if !gitChangeInView(change, view) {
			continue
		}
		if len(response.Files) >= maxGitDiffFiles || response.TotalBytes >= maxGitDiffTotalBytes {
			response.Truncated = true
			break
		}
		file := gitDiffFile{Path: change.Path, OriginalPath: change.OriginalPath, Status: change.IndexStatus + change.WorktreeStatus}
		if change.Kind == "untracked" {
			file.Untracked = true
			file.ContentOmitted = true
			response.Files = append(response.Files, file)
			continue
		}
		args := gitDiffArgs(view)
		args = append(args, "--")
		if change.rawOriginalPath != "" {
			args = append(args, change.rawOriginalPath)
		}
		args = append(args, change.rawPath)
		remaining := maxGitDiffTotalBytes - response.TotalBytes
		limit := min(maxGitDiffFileBytes, remaining)
		result, runErr := runReadOnlyGit(ctx, root, limit+1, args...)
		if runErr != nil {
			return gitDiffResponse{}, runErr
		}
		file.Bytes = result.StdoutBytes
		file.Binary = gitDiffOutputIsBinary(result.Stdout, result.StdoutTruncated)
		if file.Binary {
			file.ContentOmitted = true
		} else {
			patch := result.Stdout
			if len(patch) > limit {
				patch = patch[:limit]
			}
			file.Patch = truncateUTF8Bytes(string(patch), limit)
			response.TotalBytes += len([]byte(file.Patch))
		}
		file.Truncated = result.StdoutTruncated || result.StdoutBytes > int64(limit)
		response.Truncated = response.Truncated || file.Truncated
		response.Files = append(response.Files, file)
	}
	response.Truncated = response.Truncated || status.Truncated
	return response, nil
}

func gitDiffOutputIsBinary(raw []byte, truncated bool) bool {
	if bytes.Contains(raw, []byte("Binary files ")) || bytes.Contains(raw, []byte("GIT binary patch")) || bytes.IndexByte(raw, 0) >= 0 {
		return true
	}
	if utf8.Valid(raw) {
		return false
	}
	if truncated {
		for trim := 1; trim < utf8.UTFMax && trim < len(raw); trim++ {
			if utf8.Valid(raw[:len(raw)-trim]) {
				return false
			}
		}
	}
	return true
}

func gitChangeInView(change gitStatusEntry, view string) bool {
	if change.Kind == "untracked" {
		return view != "staged"
	}
	switch view {
	case "worktree":
		return change.WorktreeStatus != "."
	case "staged":
		return change.IndexStatus != "."
	default:
		return change.IndexStatus != "." || change.WorktreeStatus != "."
	}
}

func gitDiffArgs(view string) []string {
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--find-renames=50%", "--ignore-submodules=all"}
	switch view {
	case "staged":
		args = append(args, "--cached")
	case "head":
		args = append(args, "HEAD")
	}
	return args
}

type gitLogAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type gitLogCommit struct {
	OID              string       `json:"oid"`
	Parents          []string     `json:"parents"`
	Author           gitLogAuthor `json:"author"`
	AuthoredAt       string       `json:"authored_at"`
	Subject          string       `json:"subject"`
	SubjectTruncated bool         `json:"subject_truncated,omitempty"`
}

type gitLogResponse struct {
	Revision  string         `json:"revision"`
	Commits   []gitLogCommit `json:"commits"`
	Truncated bool           `json:"truncated"`
	Limit     int            `json:"limit"`
}

func collectGitLog(ctx context.Context, root, revision string, maxCommits int, paths []string) (gitLogResponse, error) {
	revision = strings.ToLower(strings.TrimSpace(revision))
	if revision == "" {
		revision = "head"
	}
	if revision != "head" {
		return gitLogResponse{}, fmt.Errorf("revision must be head")
	}
	if maxCommits == 0 {
		maxCommits = defaultGitLogCommits
	}
	if maxCommits < 1 || maxCommits > maxGitLogCommits {
		return gitLogResponse{}, fmt.Errorf("max_commits must be between 1 and %d", maxGitLogCommits)
	}
	normalized, err := normalizeGitPathFilters(paths)
	if err != nil {
		return gitLogResponse{}, err
	}
	if err := validateGitWorkspace(ctx, root); err != nil {
		return gitLogResponse{}, err
	}
	format := "%H%x00%P%x00%an%x00%ae%x00%aI%x00%s%x00" + gitLogRecordMarker + "%x00"
	args := []string{"log", "--no-show-signature", "--no-decorate", "--no-notes", "--format=format:" + format, "--max-count=" + strconv.Itoa(maxCommits+1), "HEAD"}
	if len(normalized) > 0 {
		args = append(args, "--")
		args = append(args, normalized...)
	}
	result, err := runReadOnlyGit(ctx, root, maxGitLogBytes, args...)
	if err != nil {
		if gitErrorIsRevisionUnavailable(err) {
			return gitLogResponse{}, errGitRevisionUnavailable
		}
		return gitLogResponse{}, err
	}
	response := gitLogResponse{Revision: revision, Commits: []gitLogCommit{}, Limit: maxCommits, Truncated: result.StdoutTruncated}
	recordDelimiter := append(append([]byte{0}, gitLogRecordMarker...), 0)
	records := bytes.Split(result.Stdout, recordDelimiter)
	if result.StdoutTruncated && len(records) > 0 {
		records = records[:len(records)-1]
	}
	for _, record := range records {
		record = bytes.Trim(record, "\r\n")
		if len(record) == 0 {
			continue
		}
		fields := bytes.Split(record, []byte{0})
		if len(fields) != 6 {
			if result.StdoutTruncated {
				response.Truncated = true
				break
			}
			return gitLogResponse{}, fmt.Errorf("invalid Git log record with %d fields in %d bytes (boundary %02x/%02x)", len(fields), len(record), record[0], record[len(record)-1])
		}
		if len(response.Commits) >= maxCommits {
			response.Truncated = true
			break
		}
		subject, subjectTruncated := boundedGitText(string(fields[5]), maxGitLogFieldBytes)
		name, _ := boundedGitText(string(fields[2]), maxGitLogFieldBytes)
		email, _ := boundedGitText(string(fields[3]), maxGitLogFieldBytes)
		response.Commits = append(response.Commits, gitLogCommit{
			OID: string(fields[0]), Parents: strings.Fields(string(fields[1])),
			Author: gitLogAuthor{Name: name, Email: email}, AuthoredAt: string(fields[4]),
			Subject: subject, SubjectTruncated: subjectTruncated,
		})
	}
	return response, nil
}

func boundedGitText(value string, limit int) (string, bool) {
	value = strings.ToValidUTF8(value, "?")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	truncated := len([]byte(value)) > limit
	return truncateUTF8Bytes(value, limit), truncated
}

func normalizeGitPathFilters(paths []string) ([]string, error) {
	if len(paths) > maxGitPathFilters {
		return nil, fmt.Errorf("paths supports at most %d entries", maxGitPathFilters)
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || len([]byte(path)) > maxGitPathBytes || strings.IndexByte(path, 0) >= 0 || filepath.IsAbs(path) {
			return nil, fmt.Errorf("Git paths must be non-empty relative paths of at most %d bytes", maxGitPathBytes)
		}
		clean := filepath.Clean(path)
		if clean != path || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("Git paths must be clean and workspace-relative")
		}
		normalized := filepath.ToSlash(clean)
		for _, component := range strings.Split(normalized, "/") {
			if component == "" || component == "." || component == ".." || component == ".git" {
				return nil, fmt.Errorf("Git path is outside the readable worktree")
			}
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

func validateGitWorkspace(ctx context.Context, root string) error {
	if _, _, err := validateGitWorkspaceLayout(ctx, root); err != nil {
		return err
	}
	bare, err := runReadOnlyGit(ctx, root, maxGitProbeBytes, "rev-parse", "--is-bare-repository")
	if err != nil {
		if gitErrorIsNotRepository(err) {
			return errGitNotRepository
		}
		return err
	}
	if strings.TrimSpace(string(bare.Stdout)) == "true" {
		return errGitBareRepository
	}
	top, err := runReadOnlyGit(ctx, root, maxGitProbeBytes, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	realTop, err := filepath.EvalSymlinks(strings.TrimSpace(string(top.Stdout)))
	if err != nil {
		return err
	}
	realRoot, _ = filepath.Abs(realRoot)
	realTop, _ = filepath.Abs(realTop)
	if filepath.Clean(realRoot) != filepath.Clean(realTop) {
		return errGitWorkspaceEscape
	}
	return nil
}

func validateGitWorkspaceLayout(ctx context.Context, root string) (string, string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	realRoot, err = filepath.Abs(realRoot)
	if err != nil {
		return "", "", err
	}
	marker := filepath.Join(root, ".git")
	info, err := os.Lstat(marker)
	if err != nil {
		if os.IsNotExist(err) {
			if looksLikeBareGitRepository(root) {
				return "", "", errGitBareRepository
			}
			return "", "", errGitNotRepository
		}
		return "", "", err
	}
	gitDir := marker
	switch {
	case info.IsDir(), info.Mode()&os.ModeSymlink != 0:
		gitDir, err = filepath.EvalSymlinks(marker)
		if err != nil {
			return "", "", err
		}
	case info.Mode().IsRegular():
		if info.Size() > maxGitProbeBytes {
			return "", "", errGitUnsafeConfiguration
		}
		content, readErr := os.ReadFile(marker)
		if readErr != nil {
			return "", "", readErr
		}
		line := strings.TrimSpace(string(content))
		if !strings.HasPrefix(strings.ToLower(line), "gitdir:") {
			return "", "", errGitNotRepository
		}
		gitDir = strings.TrimSpace(line[len("gitdir:"):])
		if gitDir == "" {
			return "", "", errGitNotRepository
		}
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(filepath.Dir(marker), gitDir)
		}
		gitDir, err = filepath.EvalSymlinks(gitDir)
		if err != nil {
			return "", "", err
		}
	default:
		return "", "", errGitNotRepository
	}
	gitDir, err = filepath.Abs(gitDir)
	if err != nil {
		return "", "", err
	}
	if !pathWithin(realRoot, gitDir) {
		return "", "", errGitWorkspaceEscape
	}
	gitInfo, err := os.Stat(gitDir)
	if err != nil || !gitInfo.IsDir() {
		if err == nil {
			err = errGitNotRepository
		}
		return "", "", err
	}
	if err := validateGitMetadataPaths(realRoot, gitDir); err != nil {
		return "", "", err
	}
	for _, name := range []string{"config", "config.worktree"} {
		configPath := filepath.Join(gitDir, name)
		if err := rejectGitConfigIncludes(ctx, root, configPath); err != nil {
			return "", "", err
		}
	}
	return realRoot, gitDir, nil
}

func looksLikeBareGitRepository(root string) bool {
	head, headErr := os.Stat(filepath.Join(root, "HEAD"))
	objects, objectsErr := os.Stat(filepath.Join(root, "objects"))
	refs, refsErr := os.Stat(filepath.Join(root, "refs"))
	return headErr == nil && head.Mode().IsRegular() && objectsErr == nil && objects.IsDir() && refsErr == nil && refs.IsDir()
}

func validateGitMetadataPaths(realRoot, gitDir string) error {
	for _, relative := range []string{
		"HEAD", "index", "config", "config.worktree", "commondir", "objects", "refs", "logs", "packed-refs",
		filepath.Join("info", "attributes"), filepath.Join("info", "exclude"), filepath.Join("objects", "info", "alternates"),
	} {
		path := filepath.Join(gitDir, relative)
		if _, err := os.Lstat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		realPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		realPath, err = filepath.Abs(realPath)
		if err != nil {
			return err
		}
		if !pathWithin(realRoot, realPath) {
			return errGitWorkspaceEscape
		}
	}
	commondir := filepath.Join(gitDir, "commondir")
	if info, err := os.Stat(commondir); err == nil && info.Mode().IsRegular() {
		if info.Size() > maxGitProbeBytes {
			return errGitUnsafeConfiguration
		}
		content, err := os.ReadFile(commondir)
		if err != nil {
			return err
		}
		commonPath := strings.TrimSpace(string(content))
		if !filepath.IsAbs(commonPath) {
			commonPath = filepath.Join(gitDir, commonPath)
		}
		commonPath, err = filepath.EvalSymlinks(commonPath)
		if err != nil {
			return err
		}
		if !pathWithin(realRoot, commonPath) {
			return errGitWorkspaceEscape
		}
	}
	alternates := filepath.Join(gitDir, "objects", "info", "alternates")
	if info, err := os.Stat(alternates); err == nil && info.Mode().IsRegular() {
		if info.Size() > maxGitFilterConfig {
			return errGitUnsafeConfiguration
		}
		content, err := os.ReadFile(alternates)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(content), "\n") {
			alternate := strings.TrimSpace(line)
			if alternate == "" {
				continue
			}
			if !filepath.IsAbs(alternate) {
				alternate = filepath.Join(gitDir, "objects", alternate)
			}
			alternate, err = filepath.EvalSymlinks(alternate)
			if err != nil {
				return err
			}
			if !pathWithin(realRoot, alternate) {
				return errGitWorkspaceEscape
			}
		}
	}
	return nil
}

func rejectGitConfigIncludes(ctx context.Context, root, configPath string) error {
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	result, err := runReadOnlyGit(ctx, root, maxGitFilterConfig,
		"config", "--file", configPath, "--no-includes", "--null", "--name-only", "--list")
	if err != nil {
		return err
	}
	if result.StdoutTruncated {
		return errGitUnsafeConfiguration
	}
	for _, rawName := range bytes.Split(result.Stdout, []byte{0}) {
		name := strings.ToLower(string(rawName))
		if name == "include.path" || (strings.HasPrefix(name, "includeif.") && strings.HasSuffix(name, ".path")) {
			return errGitUnsafeConfiguration
		}
	}
	return nil
}

func gitErrorIsNotRepository(err error) bool {
	var commandErr *gitCommandError
	return errors.As(err, &commandErr) && strings.Contains(strings.ToLower(commandErr.Stderr), "not a git repository")
}

func gitErrorIsRevisionUnavailable(err error) bool {
	var commandErr *gitCommandError
	if !errors.As(err, &commandErr) {
		return false
	}
	lower := strings.ToLower(commandErr.Stderr)
	return strings.Contains(lower, "unknown revision") || strings.Contains(lower, "bad revision") ||
		strings.Contains(lower, "ambiguous argument 'head'") || strings.Contains(lower, "does not have any commits yet")
}

func (d *Daemon) gitStatusOutcome(ctx context.Context, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	if act.Limit < 0 || act.Limit > maxGitStatusLimit {
		return toolFailed(fmt.Sprintf("limit must be between 1 and %d", maxGitStatusLimit), "invalid_arguments")
	}
	decision, err := d.fileReadDecision(sess, task, sess.WorkspaceRoot, nil)
	if err != nil {
		return toolFailed("Git status governance failed", "governance_error")
	}
	if decision.Decision != "allowed" {
		return toolDenied("DENIED: cannot inspect Git status", "policy_denied")
	}
	result, err := collectGitStatus(ctx, sess.WorkspaceRoot, act.Limit, nil)
	if err != nil {
		return gitToolErrorOutcome(ctx, err)
	}
	d.record(sess.SessionID, "FileRead", task.RunID, "git", map[string]any{"operation": "status", "changes": len(result.Changes), "truncated": result.Truncated}, decision.DecisionID)
	return marshalGitToolResult(result)
}

func (d *Daemon) gitDiffOutcome(ctx context.Context, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	if _, err := normalizeGitPathFilters(act.Paths); err != nil {
		return toolFailed(err.Error(), "invalid_arguments")
	}
	decision, err := d.fileReadDecision(sess, task, sess.WorkspaceRoot, nil)
	if err != nil {
		return toolFailed("Git diff governance failed", "governance_error")
	}
	if decision.Decision != "allowed" {
		return toolDenied("DENIED: cannot inspect Git diff", "policy_denied")
	}
	result, err := collectGitDiff(ctx, sess.WorkspaceRoot, act.GitView, act.Paths)
	if err != nil {
		if strings.Contains(err.Error(), "view must be") {
			return toolFailed(err.Error(), "invalid_arguments")
		}
		return gitToolErrorOutcome(ctx, err)
	}
	d.record(sess.SessionID, "FileRead", task.RunID, "git", map[string]any{"operation": "diff", "view": result.View, "files": len(result.Files), "bytes": result.TotalBytes, "truncated": result.Truncated}, decision.DecisionID)
	return marshalGitToolResult(result)
}

func (d *Daemon) gitLogOutcome(ctx context.Context, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	if _, err := normalizeGitPathFilters(act.Paths); err != nil {
		return toolFailed(err.Error(), "invalid_arguments")
	}
	if act.MaxCommits < 0 || act.MaxCommits > maxGitLogCommits || (act.GitRevision != "" && strings.ToLower(strings.TrimSpace(act.GitRevision)) != "head") {
		return toolFailed("revision must be head and max_commits must be between 1 and 100", "invalid_arguments")
	}
	decision, err := d.fileReadDecision(sess, task, sess.WorkspaceRoot, nil)
	if err != nil {
		return toolFailed("Git log governance failed", "governance_error")
	}
	if decision.Decision != "allowed" {
		return toolDenied("DENIED: cannot inspect Git history", "policy_denied")
	}
	result, err := collectGitLog(ctx, sess.WorkspaceRoot, act.GitRevision, act.MaxCommits, act.Paths)
	if err != nil {
		return gitToolErrorOutcome(ctx, err)
	}
	d.record(sess.SessionID, "FileRead", task.RunID, "git", map[string]any{"operation": "log", "commits": len(result.Commits), "truncated": result.Truncated}, decision.DecisionID)
	return marshalGitToolResult(result)
}

func gitToolErrorOutcome(ctx context.Context, err error) toolExecutionOutcome {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return toolCancelled("Git inspection cancelled", "operator_cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return toolTimedOut("Git inspection timed out")
	}
	switch {
	case errors.Is(err, errGitNotRepository):
		return toolFailed(err.Error(), "git_not_repository")
	case errors.Is(err, errGitBareRepository):
		return toolFailed(err.Error(), "git_bare_repository")
	case errors.Is(err, errGitWorkspaceEscape):
		return toolFailed(err.Error(), "git_workspace_escape")
	case errors.Is(err, errGitUnsafeConfiguration):
		return toolFailed(err.Error(), "git_unsafe_configuration")
	case errors.Is(err, errGitRevisionUnavailable), gitErrorIsRevisionUnavailable(err):
		return toolFailed(errGitRevisionUnavailable.Error(), "git_revision_unavailable")
	case errors.Is(err, os.ErrNotExist), errors.Is(err, exec.ErrNotFound):
		return toolFailed("Git executable is unavailable", "git_unavailable")
	default:
		return toolFailed("Git inspection failed: "+err.Error(), "git_error")
	}
}

func marshalGitToolResult(value any) toolExecutionOutcome {
	raw, err := json.Marshal(value)
	if err != nil {
		return toolFailed("Git result could not be encoded", "internal_error")
	}
	return toolCompleted(string(raw))
}
