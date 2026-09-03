// Package toolchain invokes the Zig native tools (PRD §8.5) and parses
// their JSON-line output. Tools are only ever called after a kernel
// decision has allowed the underlying capability.
package toolchain

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type FileEntry struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Binary   bool   `json:"binary"`
	Large    bool   `json:"large"`
	Language string `json:"language"`
	Mtime    int64  `json:"mtime"` // Unix seconds; 0 when the scanner predates the field
}

type Match struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type CommandResult struct {
	ExitCode   int      `json:"exit_code"`
	DurationMs int64    `json:"duration_ms"`
	Stdout     []string `json:"stdout"`
	Stderr     []string `json:"stderr"`
	TimedOut   bool     `json:"timed_out"`
}

// Toolchain locates and runs the native tools.
type Toolchain struct {
	dir string // directory containing the carina-* binaries; "" = $PATH
}

// New resolves the tools directory: explicit arg, $CARINA_TOOLS_DIR, an
// installed sibling bundle, the in-repo zig-out/bin, or $PATH. Even when the
// tools are discovered through $PATH, keep their concrete directory so the
// daemon can pass it to the kernel as CARINA_TOOLS_DIR.
func New(dir string) *Toolchain {
	if dir == "" {
		dir = os.Getenv("CARINA_TOOLS_DIR")
	}
	if dir == "" {
		if exe, err := os.Executable(); err == nil {
			dir = nativeToolsBeside(exe)
		}
	}
	if dir == "" {
		if _, err := os.Stat(filepath.Join("zig", "zig-out", "bin", "carina-scan")); err == nil {
			dir = filepath.Join("zig", "zig-out", "bin")
		}
	}
	if dir == "" {
		if patchBin, err := exec.LookPath("carina-patch-native"); err == nil {
			if absolute, absErr := filepath.Abs(patchBin); absErr == nil {
				dir = filepath.Dir(absolute)
			}
		}
	}
	return &Toolchain{dir: dir}
}

func nativeToolsBeside(executable string) string {
	dir := filepath.Dir(executable)
	if _, err := os.Stat(filepath.Join(dir, "carina-patch-native")); err == nil {
		return dir
	}
	return ""
}

func (t *Toolchain) tool(name string) string {
	if t.dir != "" {
		return filepath.Join(t.dir, name)
	}
	return name
}

// Dir returns the resolved tools directory ("" only when no bundle was found).
func (t *Toolchain) Dir() string { return t.dir }

// Available reports whether the native tools can be found.
func (t *Toolchain) Available() bool {
	_, err := exec.LookPath(t.tool("carina-scan"))
	return err == nil
}

// Scan walks the workspace tree via carina-scan.
func (t *Toolchain) Scan(root string) ([]FileEntry, error) {
	files, _, err := t.ScanBounded(root, 0, 0)
	return files, err
}

// ScanBounded walks the tree but stops after maxFiles entries or maxDepth
// path components (0 means unlimited). truncated reports a cap was hit.
func (t *Toolchain) ScanBounded(root string, maxFiles, maxDepth int) ([]FileEntry, bool, error) {
	return t.ScanBoundedContext(context.Background(), root, maxFiles, maxDepth)
}

// ScanBoundedContext is ScanBounded with caller-owned cancellation. Background
// readers use it so daemon shutdown and their smaller wall-time budgets stop
// the scanner instead of waiting for the toolchain's 30-second hard ceiling.
func (t *Toolchain) ScanBoundedContext(ctx context.Context, root string, maxFiles, maxDepth int) ([]FileEntry, bool, error) {
	args := []string{root}
	if maxFiles > 0 {
		args = append(args, "--max-files", strconv.Itoa(maxFiles))
	}
	if maxDepth > 0 {
		args = append(args, "--max-depth", strconv.Itoa(maxDepth))
	}
	out, err := t.runJSONLinesContext(ctx, 30*time.Second, nil, t.tool("carina-scan"), args...)
	if err != nil {
		return nil, false, err
	}
	var files []FileEntry
	truncated := false
	for _, raw := range out {
		var f FileEntry
		if err := json.Unmarshal(raw, &f); err == nil && f.Path != "" {
			files = append(files, f)
			continue
		}
		var summary struct {
			Summary struct {
				Truncated bool `json:"truncated"`
			} `json:"summary"`
		}
		if json.Unmarshal(raw, &summary) == nil && summary.Summary.Truncated {
			truncated = true
		}
	}
	return files, truncated, nil
}

// Grep searches via carina-grep (which walks directories natively).
func (t *Toolchain) Grep(pattern, root string) ([]Match, error) {
	matches, _, err := t.GrepBounded(pattern, root, 0)
	return matches, err
}

// GrepBounded stops after maxMatches hits (0 means unlimited). truncated
// reports the cap was hit; remaining files are not walked.
func (t *Toolchain) GrepBounded(pattern, root string, maxMatches int) ([]Match, bool, error) {
	args := []string{pattern, root}
	if maxMatches > 0 {
		args = append(args, "--max-matches", strconv.Itoa(maxMatches))
	}
	out, err := t.runJSONLines(30*time.Second, nil, t.tool("carina-grep"), args...)
	if err != nil {
		return nil, false, err
	}
	var matches []Match
	truncated := false
	for _, raw := range out {
		var m Match
		if err := json.Unmarshal(raw, &m); err == nil && m.File != "" {
			matches = append(matches, m)
			continue
		}
		var summary struct {
			Summary struct {
				Truncated bool `json:"truncated"`
			} `json:"summary"`
		}
		if json.Unmarshal(raw, &summary) == nil && summary.Summary.Truncated {
			truncated = true
		}
	}
	return matches, truncated, nil
}

// Run executes a command through carina-run with captured output. extraEnv is
// appended to the child's environment (used to inject HTTP(S)_PROXY when the
// egress proxy is active); nil leaves the inherited environment untouched.
func (t *Toolchain) Run(argv []string, cwd string, timeout time.Duration, extraEnv []string, sandbox bool) (*CommandResult, error) {
	return t.RunContext(context.Background(), argv, cwd, timeout, extraEnv, sandbox)
}

func (t *Toolchain) RunContext(ctx context.Context, argv []string, cwd string, timeout time.Duration, extraEnv []string, sandbox bool) (*CommandResult, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("toolchain: empty command")
	}
	if sandbox {
		if st := InspectSandbox(true); !st.Available {
			return nil, sandboxUnavailableError(st)
		}
	}
	args := []string{"--cwd", cwd, "--timeout-ms", fmt.Sprintf("%d", timeout.Milliseconds())}
	if sandbox {
		args = append(args, "--sandbox")
	}
	args = append(args, "--")
	args = append(args, argv...)
	env := extraEnv
	if sandbox {
		env = sandboxProcessEnv(cwd, extraEnv)
	} else if extraEnv != nil {
		env = append(os.Environ(), extraEnv...)
	}
	out, err := t.runJSONLinesContext(ctx, timeout+10*time.Second, env, t.tool("carina-run"), args...)
	if err != nil {
		return nil, err
	}
	result := &CommandResult{ExitCode: -1}
	for _, raw := range out {
		var chunk struct {
			Stream string `json:"stream"`
			Chunk  string `json:"chunk"`
		}
		if err := json.Unmarshal(raw, &chunk); err == nil && chunk.Stream != "" {
			if chunk.Stream == "stdout" {
				result.Stdout = append(result.Stdout, chunk.Chunk)
			} else {
				result.Stderr = append(result.Stderr, chunk.Chunk)
			}
			continue
		}
		var final struct {
			ExitCode   *int  `json:"exit_code"`
			DurationMs int64 `json:"duration_ms"`
			TimedOut   bool  `json:"timed_out"`
		}
		if err := json.Unmarshal(raw, &final); err == nil && final.ExitCode != nil {
			result.ExitCode = *final.ExitCode
			result.DurationMs = final.DurationMs
			result.TimedOut = final.TimedOut
		}
	}
	return result, nil
}

func (t *Toolchain) runJSONLines(timeout time.Duration, env []string, bin string, args ...string) ([]json.RawMessage, error) {
	return t.runJSONLinesContext(context.Background(), timeout, env, bin, args...)
}

func (t *Toolchain) runJSONLinesContext(ctx context.Context, timeout time.Duration, env []string, bin string, args ...string) ([]json.RawMessage, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	configureCommandProcess(cmd)
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("toolchain: start %s: %w", bin, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		killCommandProcess(cmd)
		<-done
		return nil, context.Cause(ctx)
	case <-time.After(timeout):
		killCommandProcess(cmd)
		<-done
		return nil, fmt.Errorf("toolchain: %s timed out after %s", bin, timeout)
	case err := <-done:
		// Non-zero exits still produce parseable JSON (e.g. error objects);
		// only surface hard failures with no output.
		if err != nil && stdout.Len() == 0 {
			return nil, fmt.Errorf("toolchain: %s: %w (%s)", bin, err, stderr.String())
		}
	}

	var lines []json.RawMessage
	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		raw := make([]byte, len(line))
		copy(raw, line)
		lines = append(lines, raw)
	}
	return lines, nil
}
