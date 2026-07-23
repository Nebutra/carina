package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

const submissionJournalVersion = 1

type submissionJournal struct {
	dir           string
	workspaceRoot string
	lease         *os.File
	leaseSession  string
}

type submissionJournalRecord struct {
	Version         int         `json:"version"`
	SessionID       string      `json:"session_id"`
	ClientID        string      `json:"client_submission_id"`
	Prompt          string      `json:"prompt"`
	Draft           promptDraft `json:"draft"`
	Model           string      `json:"model,omitempty"`
	Agent           string      `json:"agent,omitempty"`
	Mode            string      `json:"mode,omitempty"`
	ReasoningEffort string      `json:"reasoning_effort,omitempty"`
	Workspace       string      `json:"workspace_root,omitempty"`
}

func newSubmissionJournal(stateDir, workspaceRoot string) submissionJournal {
	if stateDir == "" {
		return submissionJournal{}
	}
	return submissionJournal{
		dir:           filepath.Join(stateDir, "tui-submissions"),
		workspaceRoot: cleanWorkspaceRoot(workspaceRoot),
	}
}

func (j submissionJournal) path(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return filepath.Join(j.dir, hex.EncodeToString(sum[:])+".json")
}

func (j submissionJournal) lockPath(sessionID string) string {
	return j.path(sessionID) + ".lock"
}

func (j *submissionJournal) acquire(sessionID string) error {
	if j.dir == "" || sessionID == "" {
		return nil
	}
	if j.lease != nil && j.leaseSession == sessionID {
		return nil
	}
	j.close()
	if err := os.MkdirAll(j.dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(j.dir, 0o700); err != nil {
		return err
	}
	lease, err := os.OpenFile(j.lockPath(sessionID), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lease.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lease.Close()
		return fmt.Errorf("another TUI owns submissions for session %s", sessionID)
	}
	j.lease = lease
	j.leaseSession = sessionID
	return nil
}

// transfer acquires the destination lock before releasing the current lock,
// so a failed switch cannot leave the TUI owning neither session.
func (j *submissionJournal) transfer(sessionID string) error {
	if j.dir == "" || sessionID == "" || j.leaseSession == sessionID {
		return nil
	}
	if err := os.MkdirAll(j.dir, 0o700); err != nil {
		return err
	}
	lease, err := os.OpenFile(j.lockPath(sessionID), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lease.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lease.Close()
		return fmt.Errorf("another TUI owns submissions for session %s", sessionID)
	}
	old := j.lease
	j.lease, j.leaseSession = lease, sessionID
	if old != nil {
		_ = syscall.Flock(int(old.Fd()), syscall.LOCK_UN)
		_ = old.Close()
	}
	return nil
}

func (j *submissionJournal) close() {
	if j.lease == nil {
		return
	}
	_ = syscall.Flock(int(j.lease.Fd()), syscall.LOCK_UN)
	_ = j.lease.Close()
	j.lease = nil
	j.leaseSession = ""
}

func (j submissionJournal) save(sessionID string, retry submissionRetry) error {
	if j.dir == "" {
		return nil
	}
	if sessionID == "" || retry.clientID == "" || retry.prompt == "" {
		return fmt.Errorf("incomplete submission recovery identity")
	}
	if err := os.MkdirAll(j.dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(j.dir, 0o700); err != nil {
		return err
	}
	record := submissionJournalRecord{
		Version: submissionJournalVersion, SessionID: sessionID,
		ClientID: retry.clientID, Prompt: retry.prompt, Draft: cloneDraft(retry.draft),
		Model: retry.model, Agent: retry.agent, Mode: retry.mode, ReasoningEffort: retry.reasoningEffort,
		Workspace: j.workspaceRoot,
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	path := j.path(sessionID)
	tmp, err := os.CreateTemp(j.dir, ".submission-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncDirectory(j.dir)
}

func (j submissionJournal) load(sessionID string) (submissionRetry, bool, error) {
	if j.dir == "" || sessionID == "" {
		return submissionRetry{}, false, nil
	}
	path := j.path(sessionID)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return submissionRetry{}, false, nil
	}
	if err != nil {
		return submissionRetry{}, false, err
	}
	var record submissionJournalRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.Version != submissionJournalVersion ||
		record.SessionID != sessionID || record.ClientID == "" || record.Prompt == "" ||
		draftPrompt(record.Draft) != record.Prompt {
		_ = os.Rename(path, path+fmt.Sprintf(".corrupt.%d", time.Now().UnixNano()))
		if err != nil {
			return submissionRetry{}, false, fmt.Errorf("invalid submission recovery record: %w", err)
		}
		return submissionRetry{}, false, fmt.Errorf("invalid submission recovery record")
	}
	return submissionRetry{
		clientID: record.ClientID, prompt: record.Prompt, draft: cloneDraft(record.Draft),
		model: record.Model, agent: record.Agent, mode: record.Mode, reasoningEffort: record.ReasoningEffort,
	}, true, nil
}

func (j submissionJournal) clear(sessionID, clientID string) error {
	if j.dir == "" || sessionID == "" {
		return nil
	}
	retry, ok, err := j.load(sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if retry.clientID != clientID {
		return fmt.Errorf("submission recovery record changed before acknowledgement")
	}
	err = os.Remove(j.path(sessionID))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDirectory(j.dir)
}

// LatestPendingSubmissionSession returns the newest pending session for the
// same workspace. Launchers use it before creating a fresh session so crash
// recovery remains the default behavior rather than an opt-in flag.
func LatestPendingSubmissionSession(stateDir, workspaceRoot string) (string, error) {
	journal := newSubmissionJournal(stateDir, workspaceRoot)
	if journal.dir == "" {
		return "", nil
	}
	entries, err := os.ReadDir(journal.dir)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	type candidate struct {
		session string
		modTime time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(journal.dir, entry.Name()))
		if err != nil {
			continue
		}
		var record submissionJournalRecord
		if json.Unmarshal(raw, &record) != nil || record.Version != submissionJournalVersion ||
			record.SessionID == "" || cleanWorkspaceRoot(record.Workspace) != journal.workspaceRoot {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{session: record.SessionID, modTime: info.ModTime()})
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.Slice(candidates, func(i, k int) bool { return candidates[i].modTime.After(candidates[k].modTime) })
	return candidates[0].session, nil
}

func cleanWorkspaceRoot(root string) string {
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return filepath.Clean(root)
	}
	return filepath.Clean(abs)
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	// Some Unix filesystems do not support directory fsync. The record itself
	// has already been fsynced and atomically renamed, so treat that capability
	// gap as a durability downgrade rather than blocking every submission.
	_ = dir.Sync()
	return nil
}

func (m *Model) restoreSubmissionJournal() tea.Cmd {
	if m.submissionLeaseErr != nil {
		return nil
	}
	retry, ok, err := m.submissions.load(m.sessionID)
	if err != nil {
		m.setOperationalNotice(m.text(MsgSubmissionRecoveryFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": err.Error()}), theme.RoleError)
		return nil
	}
	if !ok || m.submitting != nil {
		return nil
	}
	m.retrySubmission = &retry
	background := !draftEmpty(m.currentDraft())
	if !background {
		m.restoreDraft(retry.draft)
		m.resetComposerUndo()
		m.setOperationalNotice(m.text(MsgSubmissionRestored, nil), theme.RoleInfo)
	} else {
		m.setOperationalNotice(m.text(MsgSubmissionReconciling, nil), theme.RoleInfo)
	}
	cmd := m.beginSubmissionSourceWithIntent(submissionTask, "", retry.draft, false, false)
	if background && m.submitting != nil {
		m.submitting.background = true
	}
	return cmd
}
