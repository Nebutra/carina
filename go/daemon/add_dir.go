package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func (d *Daemon) agentAddDir(sess *sessionstore.Session, task *scheduler.ExecutionRun, path string) string {
	return d.agentAddDirOutcome(sess, task, path).display
}

func (d *Daemon) agentAddDirOutcome(sess *sessionstore.Session, task *scheduler.ExecutionRun, path string) toolExecutionOutcome {
	abs, err := resolveAddDirPath(path)
	if err != nil {
		return toolFailed("error: "+err.Error(), "invalid_arguments")
	}
	if addDirTooBroad(abs) {
		return toolDenied("DENIED: refusing to grant "+abs+"; pick a specific directory, not the home or system root", "policy_denied")
	}
	if sess != nil && d.store != nil && d.store.PathOwnedByOtherTenant(sess.TenantID, abs) {
		return toolDenied("DENIED: path belongs to another tenant", "policy_denied")
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return toolFailed("error: add_dir requires an existing directory: "+abs, "invalid_arguments")
	}
	if sess != nil {
		if _, ok := workspaceRelPath(sess.WorkspaceRoot, abs); ok {
			return toolCompleted("already inside the workspace: " + abs)
		}
	}
	prompt, options := addDirGrantPrompt(task.Locale, abs)
	obs := d.askUser(sess, task, prompt, options)
	if !addDirGrantAccepted(obs) {
		return toolDenied("DENIED: operator declined to grant "+abs, "approval_denied")
	}
	if err := d.kern.AddDir(sess.SessionID, abs); err != nil {
		return toolFailed("error: "+err.Error(), "governance_error")
	}
	d.record(sess.SessionID, "DirectoryGranted", task.RunID, "go",
		map[string]any{"status": "dir_granted", "path": abs}, "")
	return toolCompleted("granted extra root " + abs)
}

func resolveAddDirPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("add_dir needs a path")
	}
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("cannot expand ~")
		}
		if path == "~" {
			path = home
		} else if strings.HasPrefix(path, "~/") {
			path = filepath.Join(home, path[2:])
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid path")
	}
	if followed, err := filepath.EvalSymlinks(abs); err == nil {
		abs = followed
	}
	return filepath.Clean(abs), nil
}

func addDirTooBroad(abs string) bool {
	abs = filepath.Clean(abs)
	if abs == string(filepath.Separator) {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil {
		home = filepath.Clean(home)
		if abs == home {
			return true
		}
	}
	switch abs {
	case "/Users", "/home", "/private", "/System", "/usr", "/bin", "/sbin", "/etc", "/var", "/Volumes", "/opt":
		return true
	}
	return false
}

func addDirGrantPrompt(locale, abs string) (string, []userQuestionOption) {
	switch locale {
	case "zh", "zh-Hans":
		return fmt.Sprintf("当前工作区不能写 %s。是否把该目录授权给本会话（仍受策略约束）？", abs), []userQuestionOption{
			{Label: "授权该目录", Value: "grant", Description: "之后可以在该目录读写，仍走审批"},
			{Label: "取消", Value: "deny"},
		}
	default:
		return fmt.Sprintf("This session cannot write %s. Grant that directory for this session (still policy-gated)?", abs), []userQuestionOption{
			{Label: "Grant this directory", Value: "grant", Description: "Reads and writes there stay policy-gated"},
			{Label: "Cancel", Value: "deny"},
		}
	}
}

func addDirGrantAccepted(observation string) bool {
	obs := strings.ToLower(strings.TrimSpace(observation))
	return strings.Contains(obs, "grant") || strings.Contains(obs, "授权")
}
