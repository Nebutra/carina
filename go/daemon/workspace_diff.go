package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	workspaceDiffFileLimit  = 256 << 10
	workspaceDiffTotalLimit = 1 << 20
)

type workspaceDiffFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Untracked bool   `json:"untracked,omitempty"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Bytes     int    `json:"bytes"`
	Diff      string `json:"diff,omitempty"`
}

type workspaceDiffResponse struct {
	Files      []workspaceDiffFile `json:"files"`
	Truncated  bool                `json:"truncated"`
	TotalBytes int                 `json:"total_bytes"`
	Limits     map[string]int      `json:"limits"`
}

func readOnlyGit(ctx context.Context, root string, args ...string) ([]byte, error) {
	base := []string{"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

func parseGitStatusZ(raw []byte) [][2]string {
	parts := bytes.Split(raw, []byte{0})
	out := make([][2]string, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		entry := string(parts[i])
		if len(entry) < 4 {
			continue
		}
		status, path := entry[:2], entry[3:]
		if status[0] == 'R' || status[0] == 'C' || status[1] == 'R' || status[1] == 'C' {
			i++ // porcelain -z follows a rename/copy destination with its source.
		}
		out = append(out, [2]string{status, path})
	}
	return out
}

func (d *Daemon) handleWorkspaceDiff(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	decision, err := d.kern.Request(sess.SessionID, "FileRead", sess.WorkspaceRoot, "")
	if err != nil {
		return nil, err
	}
	if decision.Decision != "allowed" {
		return nil, fmt.Errorf("denied: %s", decision.Reason)
	}
	return collectWorkspaceDiff(sess.WorkspaceRoot)
}

func collectWorkspaceDiff(root string) (workspaceDiffResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := readOnlyGit(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return workspaceDiffResponse{}, err
	}
	resp := workspaceDiffResponse{Files: []workspaceDiffFile{}, Limits: map[string]int{"per_file_bytes": workspaceDiffFileLimit, "total_bytes": workspaceDiffTotalLimit}}
	for _, item := range parseGitStatusZ(raw) {
		status, rel := item[0], filepath.ToSlash(item[1])
		if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(filepath.Clean(rel), ".."+string(filepath.Separator)) {
			continue
		}
		row := workspaceDiffFile{Path: rel, Status: status, Untracked: status == "??"}
		var diff []byte
		if row.Untracked {
			path := filepath.Join(root, filepath.FromSlash(rel))
			info, readErr := os.Lstat(path)
			if readErr != nil {
				row.Status = "!!"
			} else if info.Mode()&os.ModeSymlink != 0 {
				target, linkErr := os.Readlink(path)
				if linkErr != nil {
					row.Status = "!!"
				} else {
					row.Bytes = len(target)
					diff = []byte(fmt.Sprintf("diff --git a/%s b/%s\nnew file mode 120000\n--- /dev/null\n+++ b/%s\n+%s\n", rel, rel, rel, target))
				}
			} else if !info.Mode().IsRegular() {
				row.Binary = true
			} else {
				file, openErr := os.Open(path)
				if openErr != nil {
					row.Status = "!!"
					resp.Files = append(resp.Files, row)
					continue
				}
				row.Bytes = int(info.Size())
				content, limitErr := io.ReadAll(io.LimitReader(file, workspaceDiffFileLimit+1))
				_ = file.Close()
				if limitErr != nil {
					row.Status = "!!"
					content = nil
				}
				row.Binary = bytes.IndexByte(content, 0) >= 0
				if !row.Binary {
					diff = []byte(fmt.Sprintf("diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n", rel, rel, rel))
					for _, line := range strings.Split(string(content), "\n") {
						diff = append(diff, []byte("+"+line+"\n")...)
					}
				}
			}
		} else {
			diff, err = readOnlyGit(ctx, root, "diff", "--no-ext-diff", "--no-textconv", "HEAD", "--", rel)
			if err != nil { // Unborn repository: combine staged and worktree diffs without writing an index.
				staged, _ := readOnlyGit(ctx, root, "diff", "--cached", "--no-ext-diff", "--no-textconv", "--", rel)
				worktree, _ := readOnlyGit(ctx, root, "diff", "--no-ext-diff", "--no-textconv", "--", rel)
				diff = append(staged, worktree...)
			}
			row.Bytes = len(diff)
			row.Binary = bytes.Contains(diff, []byte("GIT binary patch")) || bytes.Contains(diff, []byte("Binary files "))
		}
		if row.Binary {
			diff = nil
		}
		allowed := min(workspaceDiffFileLimit, workspaceDiffTotalLimit-resp.TotalBytes)
		if allowed < 0 {
			allowed = 0
		}
		if len(diff) > allowed {
			diff = diff[:allowed]
			row.Truncated = true
			resp.Truncated = true
		}
		row.Diff = string(diff)
		resp.TotalBytes += len(diff)
		resp.Files = append(resp.Files, row)
		if resp.TotalBytes >= workspaceDiffTotalLimit {
			resp.Truncated = true
			break
		}
	}
	return resp, nil
}
