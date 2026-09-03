package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	"github.com/Nebutra/carina/go/toolchain"
)

func intPointer(value int) *int { return &value }

func TestSpanProvenanceCoreDoesNotUpgradeWholeAuthority(t *testing.T) {
	d := &Daemon{readProv: map[string]sessionReadProvenance{}}
	result := toolchain.LineRangeResult{Content: []byte("target\n"), StartLine: 2, EndLine: 2, StartByte: 7, EndByte: 14, Truncated: true}
	version := toolchain.FileVersion{Size: 21, Mode: uint32(0o100600), ModTimeUnixNano: 10}
	d.recordRangeRead("session", "sample.txt", result, version)
	current := []byte("changed outside\ntarget\nfooter\n")
	next, span, err := d.materializeAuthorizedEdit("session", "sample.txt", current, "target", "updated")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(next); got != "changed outside\nupdated\nfooter\n" {
		t.Fatalf("next = %q", got)
	}
	if span == nil || span.Kind != readProvenanceSpan || string(span.Content) != "updated\n" {
		t.Fatalf("updated span = %+v", span)
	}
	d.replaceWithReadSpan("session", "sample.txt", *span)
	if _, whole := d.lastReadHash("session", "sample.txt"); whole {
		t.Fatal("span edit upgraded to whole-file authority")
	}

	target := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(target, next, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.checkWriteProvenance("session", "sample.txt", target); err == nil || !strings.Contains(err.Error(), "partial read") {
		t.Fatalf("complete-file guard = %v", err)
	}
	if _, _, err := d.materializeAuthorizedEdit("session", "sample.txt", next, "footer", "tail"); err == nil || !strings.Contains(err.Error(), "outside the ranges") {
		t.Fatalf("unread-span edit = %v", err)
	}
	if _, _, err := d.materializeAuthorizedEdit("session", "sample.txt", []byte("updated\nupdated\n"), "updated", "x"); err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("ambiguous edit = %v", err)
	}
	if _, _, err := d.materializeAuthorizedEdit("session", "sample.txt", []byte("updated changed\n"), "updated", "x"); err == nil || !strings.Contains(err.Error(), "stale observed range") {
		t.Fatalf("stale-span edit = %v", err)
	}
}

func TestRangeProvenanceRetainsOnlyEightStableVersionSpans(t *testing.T) {
	d := &Daemon{readProv: map[string]sessionReadProvenance{}}
	version := toolchain.FileVersion{Size: 100, Mode: uint32(0o600), ModTimeUnixNano: 10}
	for i := 1; i <= maxReadSpansPerFile+2; i++ {
		content := []byte{byte('a' + i), '\n'}
		d.recordRangeRead("session", "sample.txt", toolchain.LineRangeResult{
			Content: content, StartLine: i, EndLine: i,
			StartByte: int64(i * 2), EndByte: int64(i*2 + len(content)), Truncated: true,
		}, version)
	}
	records := d.readProvenanceForPath("session", "sample.txt")
	if len(records) != maxReadSpansPerFile || records[0].StartLine != 3 || records[len(records)-1].StartLine != 10 {
		t.Fatalf("bounded span history = %+v", records)
	}
}

func TestRangedReadNativeAndJSONFallbackDecodeEquivalently(t *testing.T) {
	const args = `{"path":"sample.txt","start_line":7,"line_count":3,"intent":"inspect"}`
	native, err := decodeNativeToolCalls([]modelrouter.ToolCall{{Name: "read", Arguments: json.RawMessage(args)}})
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := parseAction(`{"tool":"read","path":"sample.txt","start_line":7,"line_count":3,"intent":"inspect"}`)
	if err != nil {
		t.Fatal(err)
	}
	if native.signature() != fallback.signature() || native.StartLine == nil || native.LineCount == nil || *native.StartLine != 7 || *native.LineCount != 3 {
		t.Fatalf("native=%+v fallback=%+v", native, fallback)
	}
	descriptor, ok := defaultBuiltinTools.lookup("read")
	if !ok {
		t.Fatal("read descriptor missing")
	}
	properties, _ := descriptor.Schema["properties"].(map[string]any)
	startSchema, _ := properties["start_line"].(map[string]any)
	countSchema, _ := properties["line_count"].(map[string]any)
	if startSchema["minimum"] != float64(1) || countSchema["minimum"] != float64(1) || countSchema["maximum"] != float64(maxRangedReadLines) {
		t.Fatalf("range schema = start:%v count:%v", startSchema, countSchema)
	}
}

func rangedReadTestContext(t *testing.T, content []byte) (*Daemon, *sessionstore.Session, *scheduler.ExecutionRun, string) {
	t.Helper()
	d, workspace := newLoopDaemon(t)
	target := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(target, content, 0o600); err != nil {
		d.Close()
		t.Fatal(err)
	}
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		d.Close()
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil); err != nil {
		d.Close()
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "range test")
	return d, sess, task, target
}

func TestRangedReadReturnsMetadataAndSpanProvenance(t *testing.T) {
	d, sess, task, _ := rangedReadTestContext(t, []byte("one\r\ntwo\r\nthree\r\n"))
	defer d.Close()
	outcome := d.readWorkspaceOutcome(sess, task, &action{Tool: "read", Path: "sample.txt", StartLine: intPointer(2), LineCount: intPointer(1)})
	if outcome.status != "completed" || !strings.Contains(outcome.display, "lines 2-2") || !strings.Contains(outcome.display, "L2:two\r\n") {
		t.Fatalf("outcome = %+v", outcome)
	}
	records := d.readProvenanceForPath(sess.SessionID, "sample.txt")
	if len(records) != 1 || records[0].Kind != readProvenanceSpan || string(records[0].Content) != "two\r\n" {
		t.Fatalf("provenance = %+v", records)
	}
	events := readAuditEvents(t, d, sess.SessionID)
	found := false
	for _, event := range events {
		if event["type"] != "FileRead" {
			continue
		}
		payload, _ := event["payload"].(map[string]any)
		if payload["partial"] == true && payload["start_line"] == float64(2) && payload["line_count"] == float64(1) && payload["bytes"] == float64(5) {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing partial FileRead payload: %v", events)
	}
}

func TestRangedReadRejectsSkillImageAndInvalidPairs(t *testing.T) {
	d, sess, task, target := rangedReadTestContext(t, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...))
	defer d.Close()
	start, count := intPointer(1), intPointer(1)
	for name, act := range map[string]*action{
		"skill":   {Tool: "read", Path: "skill://review", StartLine: start, LineCount: count},
		"image":   {Tool: "read", Path: filepath.Base(target), StartLine: start, LineCount: count},
		"missing": {Tool: "read", Path: filepath.Base(target), StartLine: start},
	} {
		t.Run(name, func(t *testing.T) {
			outcome := d.readWorkspaceOutcome(sess, task, act)
			if outcome.status != "failed" || (outcome.errorCategory != "unsupported_read_range" && outcome.errorCategory != "invalid_read_range") {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

func TestRangedReadSymlinkContainment(t *testing.T) {
	d, sess, task, target := rangedReadTestContext(t, []byte("inside\n"))
	defer d.Close()
	insideLink := filepath.Join(sess.WorkspaceRoot, "inside-link.txt")
	if err := os.Symlink(filepath.Base(target), insideLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	start, count := intPointer(1), intPointer(1)
	inside := d.readWorkspaceOutcome(sess, task, &action{Tool: "read", Path: filepath.Base(insideLink), StartLine: start, LineCount: count})
	if inside.status != "completed" || !strings.Contains(inside.display, "L1:inside") {
		t.Fatalf("inside symlink outcome = %+v", inside)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	escapeLink := filepath.Join(sess.WorkspaceRoot, "escape-link.txt")
	if err := os.Symlink(outside, escapeLink); err != nil {
		t.Fatal(err)
	}
	escape := d.readWorkspaceOutcome(sess, task, &action{Tool: "read", Path: filepath.Base(escapeLink), StartLine: start, LineCount: count})
	if escape.status != "denied" {
		t.Fatalf("escaping symlink outcome = %+v", escape)
	}
}

func TestPartialReadCannotAuthorizeWholePatch(t *testing.T) {
	d, sess, task, _ := rangedReadTestContext(t, []byte("one\ntwo\nthree\n"))
	defer d.Close()
	d.readWorkspaceOutcome(sess, task, &action{Tool: "read", Path: "sample.txt", StartLine: intPointer(2), LineCount: intPointer(1)})
	outcome := d.agentPatchOutcome(sess, task, "sample.txt", "replacement\n")
	if outcome.status != "denied" || !strings.Contains(outcome.display, "partial read") {
		t.Fatalf("whole patch outcome = %+v", outcome)
	}
}

type editAfterOutsideDriftReasoner struct {
	path string
	turn int
}

func (r *editAfterOutsideDriftReasoner) Name() string { return "span-edit" }

func (r *editAfterOutsideDriftReasoner) Think(context.Context, string) (string, error) {
	r.turn++
	switch r.turn {
	case 1:
		return `{"tool":"read","path":"sample.txt","start_line":2,"line_count":1,"intent":"inspect target"}`, nil
	case 2:
		if err := os.WriteFile(r.path, []byte("changed outside\ntarget\nfooter\n"), 0o600); err != nil {
			return "", err
		}
		return `{"tool":"edit","path":"sample.txt","old":"target","new":"updated","intent":"edit observed target"}`, nil
	default:
		return `{"tool":"done","summary":"updated target"}`, nil
	}
}

func TestSpanEditPreservesConcurrentChangesOutsideObservedRange(t *testing.T) {
	d, sess, task, target := rangedReadTestContext(t, []byte("header\ntarget\nfooter\n"))
	defer d.Close()
	d.SetReasoner(&editAfterOutsideDriftReasoner{path: target})
	d.runTask(sess, task)
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "changed outside\nupdated\nfooter\n" {
		t.Fatalf("content = %q", got)
	}
	records := d.readProvenanceForPath(sess.SessionID, "sample.txt")
	if len(records) != 1 || records[0].Kind != readProvenanceSpan || string(records[0].Content) != "updated\n" {
		t.Fatalf("post-edit provenance = %+v", records)
	}
}

func TestSpanEditDeletingObservedWindowClearsAuthority(t *testing.T) {
	d, sess, task, target := rangedReadTestContext(t, []byte("target\n"))
	defer d.Close()
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"read","path":"sample.txt","start_line":1,"line_count":1,"intent":"inspect"}`,
		`{"tool":"edit","path":"sample.txt","old":"target\n","new":"","intent":"remove target"}`,
		`{"tool":"done","summary":"removed target"}`,
	}})
	d.runTask(sess, task)
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Fatalf("content = %q", content)
	}
	if records := d.readProvenanceForPath(sess.SessionID, "sample.txt"); len(records) != 0 {
		t.Fatalf("empty span retained authority: %+v", records)
	}
}

func TestSpanEditRejectsUnreadStaleAndAmbiguousTargets(t *testing.T) {
	tests := []struct {
		name       string
		initial    string
		start      int
		afterRead  string
		old        string
		wantReason string
	}{
		{name: "unread", initial: "header\ntarget\n", start: 1, old: "target", wantReason: "outside the ranges"},
		{name: "stale", initial: "header\ntarget\n", start: 2, afterRead: "header\ndrifted\n", old: "target", wantReason: "not found"},
		{name: "ambiguous", initial: "header\ntarget\n", start: 2, afterRead: "target\ntarget\n", old: "target", wantReason: "not unique"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, sess, task, target := rangedReadTestContext(t, []byte(test.initial))
			defer d.Close()
			d.readWorkspaceOutcome(sess, task, &action{Tool: "read", Path: "sample.txt", StartLine: intPointer(test.start), LineCount: intPointer(1)})
			if test.afterRead != "" {
				if err := os.WriteFile(target, []byte(test.afterRead), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			outcome := d.agentEditOutcome(sess, task, "sample.txt", test.old, "updated")
			if outcome.status != "denied" || !strings.Contains(outcome.display, test.wantReason) {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

func TestSpanProvenanceRoundTripsCheckpointAndAnchor(t *testing.T) {
	d, sess, task, target := rangedReadTestContext(t, []byte("target\nunread tail\n"))
	defer d.Close()
	d.readWorkspaceOutcome(sess, task, &action{Tool: "read", Path: "sample.txt", StartLine: intPointer(1), LineCount: intPointer(1)})
	if err := os.WriteFile(target, []byte("target\nchanged unread tail\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, err := d.captureWorkspaceAnchor(sess)
	if err != nil {
		t.Fatal(err)
	}
	if len(anchor.DependencyFiles) != 0 || len(anchor.DependencySpans) != 1 {
		t.Fatalf("anchor = %+v", anchor)
	}
	cp := &runCheckpoint{Turn: 1, Transcript: newTranscript("range"), WorkspaceAnchor: anchor, ReadProvenance: d.snapshotReadProvenance(sess.SessionID)}
	raw, err := encodeRunCheckpoint(cp)
	if err != nil {
		t.Fatal(err)
	}
	restored := decodeRunCheckpoint(raw)
	if restored == nil || len(restored.ReadProvenance["sample.txt"]) != 1 || restored.ReadProvenance["sample.txt"][0].Kind != readProvenanceSpan {
		t.Fatalf("restored checkpoint = %+v", restored)
	}
	if ok, reason := verifyWorkspaceAnchor(restored.WorkspaceAnchor); !ok {
		t.Fatalf("span anchor failed: %s", reason)
	}

	legacyRaw, err := json.Marshal(map[string]any{
		"version": 2, "turn": 1, "transcript": newTranscript("legacy"),
		"read_provenance": map[string]string{"legacy.txt": strings.Repeat("a", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy := decodeRunCheckpoint(legacyRaw)
	if legacy == nil || legacy.ReadProvenance["legacy.txt"][0].Kind != readProvenanceWhole {
		t.Fatalf("legacy provenance did not normalize: %+v", legacy)
	}
	corruptRaw, err := json.Marshal(map[string]any{
		"version": 2, "turn": 1, "transcript": newTranscript("corrupt"),
		"read_provenance": map[string]any{"sample.txt": []readProvenance{{Version: readProvenanceVersion, Kind: readProvenanceSpan, SHA256: strings.Repeat("0", 64), Content: []byte("target\n"), StartLine: 1, LineCount: 1, EndByte: 7}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decoded := decodeRunCheckpoint(corruptRaw); decoded != nil {
		t.Fatalf("corrupt provenance checkpoint loaded: %+v", decoded.ReadProvenance)
	}
	_ = task
}

func TestCheckpointRejectsInconsistentSpanBounds(t *testing.T) {
	content := []byte("target\n")
	sum := sha256.Sum256(content)
	valid := readProvenance{
		Version: readProvenanceVersion, Kind: readProvenanceSpan,
		SHA256: hex.EncodeToString(sum[:]), Content: content,
		StartLine: 2, LineCount: 1, StartByte: 7, EndByte: 14,
		Truncated: true, ObservedSize: 21, ObservedMode: uint32(0o600),
	}
	tests := map[string]readProvenance{
		"end byte":      func() readProvenance { p := valid; p.EndByte++; return p }(),
		"observed size": func() readProvenance { p := valid; p.ObservedSize = 13; return p }(),
		"ambiguous eof": func() readProvenance { p := valid; p.EOF = true; return p }(),
		"truncated at eof": func() readProvenance {
			p := valid
			p.ObservedSize = p.EndByte
			return p
		}(),
		"whole with span data": {
			Version: readProvenanceVersion, Kind: readProvenanceWhole,
			SHA256: strings.Repeat("a", 64), ObservedSize: 21,
		},
	}
	for name, provenance := range tests {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"version": 2, "turn": 1, "transcript": newTranscript("invalid bounds"),
				"read_provenance": map[string]any{"sample.txt": []readProvenance{provenance}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if decoded := decodeRunCheckpoint(raw); decoded != nil {
				t.Fatalf("inconsistent provenance loaded: %+v", decoded.ReadProvenance)
			}
		})
	}
}
