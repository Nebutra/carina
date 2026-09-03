package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Nebutra/carina/go/artifact"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	"github.com/Nebutra/carina/go/toolchain"
)

const maxRangedReadLines = 2000

var errRangedImageUnsupported = errors.New("ranged reads are not supported for images")

func readRangeArguments(act *action) (startLine, lineCount int, requested bool, err error) {
	if act == nil || (act.StartLine == nil && act.LineCount == nil) {
		return 0, 0, false, nil
	}
	if act.StartLine == nil || act.LineCount == nil {
		return 0, 0, true, fmt.Errorf("start_line and line_count must be provided together")
	}
	if *act.StartLine < 1 || *act.LineCount < 1 {
		return 0, 0, true, fmt.Errorf("start_line and line_count must be positive")
	}
	if *act.LineCount > maxRangedReadLines {
		return 0, 0, true, fmt.Errorf("line_count must not exceed %d", maxRangedReadLines)
	}
	return *act.StartLine, *act.LineCount, true, nil
}

func (d *Daemon) readWorkspaceOutcome(sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	startLine, lineCount, ranged, rangeErr := readRangeArguments(act)
	if rangeErr != nil {
		return toolFailed("error: "+rangeErr.Error(), "invalid_read_range")
	}
	if _, skill := parseSkillURI(act.Path); skill {
		if ranged {
			return toolFailed("error: ranged reads are not supported for skill resources; omit start_line and line_count", "unsupported_read_range")
		}
		return d.readSkillURI(sess, task, act.Path)
	}
	abs := resolveIn(sess.WorkspaceRoot, act.Path)
	decision, err := d.fileReadDecision(sess, task, abs, act.authorizedRead)
	if err != nil {
		return toolFailed("error: "+err.Error(), "governance_error")
	}
	if decision.Decision != "allowed" {
		return toolDenied("DENIED: "+decision.Reason, "policy_denied")
	}
	if !ranged {
		return d.readWholeWorkspaceFile(sess, task, act.Path, abs, decision.DecisionID)
	}
	return d.readWorkspaceLineRange(sess, task, act.Path, abs, startLine, lineCount, decision.DecisionID)
}

func (d *Daemon) readWholeWorkspaceFile(sess *sessionstore.Session, task *scheduler.ExecutionRun, path, abs, decisionID string) toolExecutionOutcome {
	content, err := os.ReadFile(abs)
	if err != nil {
		return toolFailed("error: "+err.Error(), "io_error")
	}
	d.record(sess.SessionID, "FileRead", task.RunID, "go", map[string]any{"path": abs, "bytes": len(content)}, decisionID)
	d.recordRead(sess.SessionID, path, string(content))
	if _, isImage := sniffImageMediaType(content); isImage {
		ref, err := ingestImageMedia(d.artifacts, artifact.Scope{SessionID: sess.SessionID}, "read "+path, content)
		if err != nil {
			return toolFailed("error: "+err.Error(), "io_error")
		}
		return toolCompletedMedia(ref.placeholder(), ref)
	}
	return toolCompleted(string(content))
}

func (d *Daemon) readWorkspaceLineRange(sess *sessionstore.Session, task *scheduler.ExecutionRun, path, abs string, startLine, lineCount int, decisionID string) toolExecutionOutcome {
	result, version, err := toolchain.ReadLineRangeFileValidated(abs, startLine, lineCount, toolchain.LineRangeLimits{}, 12, func(prefix []byte) error {
		if _, image := sniffImageMediaType(prefix); image {
			return errRangedImageUnsupported
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errRangedImageUnsupported):
			return toolFailed("error: ranged reads are not supported for images; omit start_line and line_count", "unsupported_read_range")
		case errors.Is(err, toolchain.ErrFileChangedDuringRead):
			return toolFailed("error: file changed during ranged read; retry the read", "stale_read")
		case errors.Is(err, toolchain.ErrRangeReadBudgetExceeded), errors.Is(err, toolchain.ErrRangeOutputTooLarge):
			return toolFailed("error: "+err.Error()+"; request a smaller or nearer line window", "read_limit")
		case errors.Is(err, toolchain.ErrInvalidLineRange):
			return toolFailed("error: "+err.Error(), "invalid_read_range")
		default:
			return toolFailed("error: "+err.Error(), "io_error")
		}
	}
	d.record(sess.SessionID, "FileRead", task.RunID, "go", map[string]any{
		"path": abs, "bytes": len(result.Content), "partial": true,
		"start_line": result.StartLine, "requested_line_count": lineCount,
		"end_line": result.EndLine, "line_count": max(0, result.EndLine-result.StartLine+1),
		"start_byte": result.StartByte, "end_byte": result.EndByte,
		"stream_bytes": result.BytesRead, "eof": result.EOF, "truncated": result.Truncated,
	}, decisionID)
	d.recordRangeRead(sess.SessionID, path, result, version)
	return toolCompleted(formatRangedReadObservation(path, result))
}

func formatRangedReadObservation(path string, result toolchain.LineRangeResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[read %s lines %d-%d; eof=%t; more=%t; bytes=%d; stream_bytes=%d]\n",
		path, result.StartLine, result.EndLine, result.EOF, result.Truncated, len(result.Content), result.BytesRead)
	line := result.StartLine
	remaining := result.Content
	for len(remaining) > 0 {
		end := len(remaining)
		if newline := bytes.IndexByte(remaining, '\n'); newline >= 0 {
			end = newline + 1
		}
		fmt.Fprintf(&b, "L%d:%s", line, strings.ToValidUTF8(string(remaining[:end]), "\uFFFD"))
		remaining = remaining[end:]
		line++
	}
	return b.String()
}
