// Package toolchain invokes the Zig native tools (PRD §8.5) and parses
// their JSON-line output. Tools are only ever called after a kernel
// decision has allowed the underlying capability.
package toolchain

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type FileEntry struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Binary   bool   `json:"binary"`
	Large    bool   `json:"large"`
	Language string `json:"language"`
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
	dir string // directory containing the pi-* binaries; "" = $PATH
}

// New resolves the tools directory: explicit arg, $PI_TOOLS_DIR, the
// in-repo zig-out/bin, or $PATH.
func New(dir string) *Toolchain {
	if dir == "" {
		dir = os.Getenv("PI_TOOLS_DIR")
	}
	if dir == "" {
		if _, err := os.Stat(filepath.Join("zig", "zig-out", "bin", "carina-scan")); err == nil {
			dir = filepath.Join("zig", "zig-out", "bin")
		}
	}
	return &Toolchain{dir: dir}
}

func (t *Toolchain) tool(name string) string {
	if t.dir != "" {
		return filepath.Join(t.dir, name)
	}
	return name
}

// Dir returns the resolved tools directory ("" if tools are on $PATH).
func (t *Toolchain) Dir() string { return t.dir }

// Available reports whether the native tools can be found.
func (t *Toolchain) Available() bool {
	_, err := exec.LookPath(t.tool("carina-scan"))
	return err == nil
}

// Scan walks the workspace tree via carina-scan.
func (t *Toolchain) Scan(root string) ([]FileEntry, error) {
	out, err := t.runJSONLines(30*time.Second, nil, t.tool("carina-scan"), root)
	if err != nil {
		return nil, err
	}
	var files []FileEntry
	for _, raw := range out {
		var f FileEntry
		if err := json.Unmarshal(raw, &f); err == nil && f.Path != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// Grep searches via carina-grep (which walks directories natively).
func (t *Toolchain) Grep(pattern, root string) ([]Match, error) {
	out, err := t.runJSONLines(30*time.Second, nil, t.tool("carina-grep"), pattern, root)
	if err != nil {
		return nil, err
	}
	var matches []Match
	for _, raw := range out {
		var m Match
		if err := json.Unmarshal(raw, &m); err == nil && m.File != "" {
			matches = append(matches, m)
		}
	}
	return matches, nil
}

// Run executes a command through carina-run with captured output. extraEnv is
// appended to the child's environment (used to inject HTTP(S)_PROXY when the
// egress proxy is active); nil leaves the inherited environment untouched.
func (t *Toolchain) Run(argv []string, cwd string, timeout time.Duration, extraEnv []string, sandbox bool) (*CommandResult, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("toolchain: empty command")
	}
	args := []string{"--cwd", cwd, "--timeout-ms", fmt.Sprintf("%d", timeout.Milliseconds())}
	if sandbox {
		args = append(args, "--sandbox")
	}
	args = append(args, "--")
	args = append(args, argv...)
	out, err := t.runJSONLines(timeout+10*time.Second, extraEnv, t.tool("carina-run"), args...)
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
	cmd := exec.Command(bin, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
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
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
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
