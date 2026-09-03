package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	"github.com/Nebutra/carina/go/statefmt"
	"github.com/Nebutra/carina/go/toolchain"
)

const (
	proposalStoreVersion    = 1
	proactivePendingCap     = 1
	proactiveCooldown       = 10 * time.Minute
	proactivePrepareRetry   = time.Minute
	proactivePrepareTimeout = 2 * time.Second
	proactivePrepareFileCap = 200
	proactivePrepareDepth   = 6
	proactiveEvidenceCap    = 6
	proactiveStoredCap      = 256
	proactiveModuleTouches  = 3
	proactiveLongTurn       = 32
	proactiveObsBudget      = 240
	proactiveTriggerVersion = "proactive-v2"
	proactiveClassModule    = "module_repeat"
	proactiveClassTestFail  = "test_fail"
	proactiveClassImpact    = "impact_edit"
	proactiveClassLongRun   = "long_run"
	proactiveStatusPending  = "pending"
	proactiveStatusAccepted = "accepted"
	proactiveStatusIgnored  = "ignored"
)

// proposal is a read-only 奏折 card. It is never a transcript turn.
type proposal struct {
	ID             string              `json:"id"`
	SessionID      string              `json:"session_id"`
	RunID          string              `json:"run_id"`
	Class          string              `json:"class"`
	Title          string              `json:"title"`
	Why            string              `json:"why"`
	Done           string              `json:"done"`
	Propose        string              `json:"propose"`
	Risk           string              `json:"risk"`
	Status         string              `json:"status"`
	Created        time.Time           `json:"created"`
	Prepared       bool                `json:"prepared,omitempty"`
	Evidence       []proposalEvidence  `json:"evidence,omitempty"`
	Preparation    proposalPreparation `json:"preparation,omitempty"`
	TriggerVersion string              `json:"trigger_version,omitempty"`
	prepareModule  string
}

type proposalEvidence struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type proposalPreparation struct {
	ToolCalls    int   `json:"tool_calls,omitempty"`
	FilesScanned int   `json:"files_scanned,omitempty"`
	Truncated    bool  `json:"truncated,omitempty"`
	ElapsedMS    int64 `json:"elapsed_ms,omitempty"`
}

type proactiveTouch struct {
	Tool    string
	Path    string
	Command []string
	Failed  bool
	Obs     string
	Turn    int
}

type proposalStore struct {
	mu        sync.Mutex
	path      string
	items     []*proposal
	touches   map[string][]proactiveTouch // runID
	lastFired map[string]time.Time        // session|class
	muted     map[string]bool             // session|class
	preparing map[string]bool             // process-local session|class reservation
	lastTry   map[string]time.Time        // process-local failed-preparation cooldown
	prepareCh chan struct{}               // one low-priority preparer at a time
	now       func() time.Time
}

type proposalStoreEnvelope struct {
	Version   int                  `json:"version"`
	Items     []*proposal          `json:"items"`
	LastFired map[string]time.Time `json:"last_fired,omitempty"`
	Muted     []string             `json:"muted,omitempty"`
}

func newProposalStore(stateDir string) *proposalStore {
	path := ""
	if strings.TrimSpace(stateDir) != "" {
		path = filepath.Join(stateDir, "proposals.json")
	}
	s := &proposalStore{
		path:      path,
		touches:   map[string][]proactiveTouch{},
		lastFired: map[string]time.Time{},
		muted:     map[string]bool{},
		preparing: map[string]bool{},
		lastTry:   map[string]time.Time{},
		prepareCh: make(chan struct{}, 1),
		now:       func() time.Time { return time.Now().UTC() },
	}
	s.load()
	return s
}

func validProposalStatus(status string) bool {
	switch status {
	case proactiveStatusPending, proactiveStatusAccepted, proactiveStatusIgnored:
		return true
	default:
		return false
	}
}

func validStoredProposal(item *proposal) bool {
	return item != nil && strings.TrimSpace(item.ID) != "" &&
		strings.TrimSpace(item.SessionID) != "" && strings.TrimSpace(item.RunID) != "" &&
		strings.TrimSpace(item.Class) != "" && strings.TrimSpace(item.Title) != "" &&
		validProposalStatus(item.Status) && !item.Created.IsZero()
}

func (s *proposalStore) load() {
	if s == nil || s.path == "" {
		return
	}
	raw, version, ok := statefmt.ReadVersioned(s.path, proposalStoreVersion)
	if !ok {
		return
	}
	var env proposalStoreEnvelope
	if json.Unmarshal(raw, &env) != nil || (env.Version != 0 && env.Version != proposalStoreVersion) {
		_ = statefmt.Quarantine(s.path, version)
		return
	}
	for _, item := range env.Items {
		if !validStoredProposal(item) {
			_ = statefmt.Quarantine(s.path, version)
			s.items = nil
			s.lastFired = map[string]time.Time{}
			s.muted = map[string]bool{}
			return
		}
		cp := *item
		cp.Evidence = append([]proposalEvidence(nil), item.Evidence...)
		s.items = append(s.items, &cp)
	}
	for key, fired := range env.LastFired {
		if strings.TrimSpace(key) != "" && !fired.IsZero() {
			s.lastFired[key] = fired
		}
	}
	for _, key := range env.Muted {
		if strings.TrimSpace(key) != "" {
			s.muted[key] = true
		}
	}
	s.items = trimStoredProposals(s.items)
}

func trimStoredProposals(items []*proposal) []*proposal {
	if len(items) <= proactiveStoredCap {
		return append([]*proposal(nil), items...)
	}
	pending := make([]*proposal, 0)
	resolved := make([]*proposal, 0, len(items))
	for _, item := range items {
		if item != nil && item.Status == proactiveStatusPending {
			pending = append(pending, item)
		} else if item != nil {
			resolved = append(resolved, item)
		}
	}
	sort.SliceStable(resolved, func(i, j int) bool { return resolved[i].Created.Before(resolved[j].Created) })
	keepResolved := max(0, proactiveStoredCap-len(pending))
	if len(resolved) > keepResolved {
		resolved = resolved[len(resolved)-keepResolved:]
	}
	out := append(resolved, pending...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func (s *proposalStore) persistLocked() error {
	if s == nil || s.path == "" {
		return nil
	}
	items := trimStoredProposals(s.items)
	muted := make([]string, 0, len(s.muted))
	for key, on := range s.muted {
		if on {
			muted = append(muted, key)
		}
	}
	sort.Strings(muted)
	lastFired := make(map[string]time.Time, len(s.lastFired))
	for key, fired := range s.lastFired {
		lastFired[key] = fired
	}
	raw, err := json.MarshalIndent(proposalStoreEnvelope{
		Version: proposalStoreVersion, Items: items, LastFired: lastFired, Muted: muted,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("proposal store: marshal: %w", err)
	}
	if err := durableAtomicWrite(s.path, raw, 0o600); err != nil {
		return fmt.Errorf("proposal store: persist: %w", err)
	}
	s.items = items
	return nil
}

func (s *proposalStore) beginPreparation(sessionID, class string) bool {
	if s == nil {
		return false
	}
	now := s.now()
	key := proactiveKey(sessionID, class)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.muted[key] || s.preparing[key] {
		return false
	}
	if last, ok := s.lastFired[key]; ok && now.Sub(last) < proactiveCooldown {
		return false
	}
	if last, ok := s.lastTry[key]; ok && now.Sub(last) < proactivePrepareRetry {
		return false
	}
	pending := 0
	for _, item := range s.items {
		if item.SessionID == sessionID && item.Status == proactiveStatusPending {
			pending++
		}
	}
	if pending >= proactivePendingCap {
		return false
	}
	select {
	case s.prepareCh <- struct{}{}:
		s.preparing[key] = true
		s.lastTry[key] = now
		return true
	default:
		return false
	}
}

func (s *proposalStore) finishPreparation(sessionID, class string) {
	if s == nil {
		return
	}
	key := proactiveKey(sessionID, class)
	s.mu.Lock()
	delete(s.preparing, key)
	s.mu.Unlock()
	<-s.prepareCh
}

func proactiveEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CARINA_PROACTIVE"))) {
	case "1", "on", "true", "yes":
		return true
	default:
		return false
	}
}

func proactiveKey(sessionID, class string) string {
	return sessionID + "\x00" + class
}

func proactiveModule(path string) string {
	path = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(path)), "./")
	if path == "" {
		return ""
	}
	if i := strings.IndexByte(path, '/'); i > 0 {
		return path[:i]
	}
	return path
}

func looksLikeTestCommand(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	base := strings.ToLower(filepath.Base(argv[0]))
	if base == "pytest" || base == "gotestsum" {
		return true
	}
	for i, raw := range argv {
		a := strings.ToLower(raw)
		if a != "test" {
			continue
		}
		if i == 0 {
			return false
		}
		prev := strings.ToLower(filepath.Base(argv[i-1]))
		switch prev {
		case "go", "cargo", "npm", "pnpm", "yarn", "make", "mvn", "gradle", "python", "python3":
			return true
		}
	}
	return false
}

func (d *Daemon) considerProactive(sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action, outcome toolExecutionOutcome, turn int, tr *Transcript) {
	if d == nil || d.proposals == nil || sess == nil || task == nil || act == nil {
		return
	}
	if d.safeMode || !proactiveEnabled() {
		return
	}
	if taskAgent(task) != "build" {
		return
	}
	if d.isPlanMode(sess.SessionID) {
		return
	}
	if act.Tool == "done" || act.Tool == "ask_user" || act.Tool == "spawn" || strings.HasPrefix(act.Tool, "job.") || act.Tool == "workflow" || act.Tool == "todo" || act.Tool == "update_plan" {
		return
	}
	touch := proactiveTouch{
		Tool:    act.Tool,
		Path:    strings.TrimSpace(act.Path),
		Command: append([]string(nil), act.Command...),
		Failed:  outcome.status == "failed" || outcome.status == "timed_out",
		Obs:     truncate(strings.TrimSpace(outcome.display), proactiveObsBudget),
		Turn:    turn,
	}
	d.proposals.mu.Lock()
	d.proposals.touches[task.RunID] = append(d.proposals.touches[task.RunID], touch)
	history := append([]proactiveTouch(nil), d.proposals.touches[task.RunID]...)
	d.proposals.mu.Unlock()

	class, card := d.proactiveCard(history, turn)
	if class == "" || card == nil {
		return
	}
	select {
	case <-d.stopCh:
		return
	default:
	}
	if !d.proposals.beginPreparation(sess.SessionID, class) {
		return
	}
	d.startTask(func() {
		defer d.proposals.finishPreparation(sess.SessionID, class)
		prepared := d.prepareProactive(sess, task, class, card)
		if prepared == nil {
			return
		}
		d.emitProposal(sess, task, class, prepared, tr)
	})
}

func (d *Daemon) prepareProactive(sess *sessionstore.Session, task *scheduler.ExecutionRun, class string, seed *proposal) *proposal {
	if d == nil || sess == nil || task == nil || seed == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(d.contextForTask(task.RunID), proactivePrepareTimeout)
	defer cancel()
	go func() {
		select {
		case <-d.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	started := time.Now()
	dec, err := d.fileReadDecision(sess, task, sess.WorkspaceRoot, nil)
	if err != nil || dec.Decision != "allowed" {
		return nil
	}
	files, truncated, err := d.tools.ScanBoundedContext(ctx, sess.WorkspaceRoot, proactivePrepareFileCap, proactivePrepareDepth)
	if err != nil {
		return nil
	}
	evidence := proactiveEvidence(files, sess.WorkspaceRoot, seed.prepareModule)
	if err := d.recordChecked(sess.SessionID, "FileRead", task.RunID, "go", map[string]any{
		"resource": sess.WorkspaceRoot, "purpose": "proactive_prepare", "class": class,
		"files_scanned": len(files), "truncated": truncated,
	}, dec.DecisionID); err != nil {
		return nil
	}
	card := *seed
	card.Prepared = true
	card.Evidence = evidence
	card.TriggerVersion = proactiveTriggerVersion
	card.Preparation = proposalPreparation{
		ToolCalls: 1, FilesScanned: len(files), Truncated: truncated,
		ElapsedMS: time.Since(started).Milliseconds(),
	}
	card.Done = strings.TrimSpace(card.Done + "\n" + proactivePreparationSummary(seed.prepareModule, evidence, len(files), truncated))
	return &card
}

func proactiveEvidence(files []toolchain.FileEntry, workspaceRoot, module string) []proposalEvidence {
	module = strings.Trim(filepath.ToSlash(strings.TrimSpace(module)), "/")
	paths := make([]string, 0)
	for _, file := range files {
		rel := displayRelPath(file.Path, workspaceRoot)
		if rel == "" || file.Binary || !proactiveLikelyTestPath(rel) {
			continue
		}
		if module != "" && rel != module && !strings.HasPrefix(rel, module+"/") {
			continue
		}
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	if len(paths) > proactiveEvidenceCap {
		paths = paths[:proactiveEvidenceCap]
	}
	evidence := make([]proposalEvidence, 0, len(paths))
	for _, path := range paths {
		evidence = append(evidence, proposalEvidence{Kind: "test_file", Path: path})
	}
	return evidence
}

func proactiveLikelyTestPath(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(path)
	if strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") ||
		strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	for _, part := range strings.Split(path, "/") {
		switch part {
		case "test", "tests", "__tests__":
			return true
		}
	}
	return false
}

func proactivePreparationSummary(module string, evidence []proposalEvidence, scanned int, truncated bool) string {
	scope := "workspace"
	if module = strings.Trim(filepath.ToSlash(strings.TrimSpace(module)), "/"); module != "" {
		scope = module
	}
	var b strings.Builder
	fmt.Fprintf(&b, "read-only prep scanned %d files for %s", scanned, scope)
	if truncated {
		b.WriteString(" (bounded scan truncated)")
	}
	if len(evidence) == 0 {
		b.WriteString("; no likely test files found")
		return b.String()
	}
	b.WriteString("; likely tests: ")
	for i, item := range evidence {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(item.Path)
	}
	return b.String()
}

func (d *Daemon) proactiveCard(history []proactiveTouch, turn int) (string, *proposal) {
	if class, card := proactiveModuleRepeat(history); class != "" {
		return class, card
	}
	if class, card := proactiveTestFail(history); class != "" {
		return class, card
	}
	if class, card := proactiveImpactEdit(history); class != "" {
		return class, card
	}
	if turn >= proactiveLongTurn {
		return proactiveClassLongRun, &proposal{
			Title:         "Long build still open",
			Why:           fmt.Sprintf("this run is at turn %d without a proposal", turn),
			Done:          "identified a long-running build from the turn budget",
			Propose:       "pause to recap remaining work, or keep going with a narrower next edit",
			Risk:          "read-only card; adopting it only steers the current run",
			prepareModule: proactiveLastModule(history),
		}
	}
	return "", nil
}

func proactiveModuleRepeat(history []proactiveTouch) (string, *proposal) {
	var modules []string
	var paths []string
	sawTest := false
	for _, touch := range history {
		if looksLikeTestCommand(touch.Command) {
			sawTest = true
		}
		if touch.Tool != "read" && touch.Tool != "edit" && touch.Tool != "patch" {
			continue
		}
		mod := proactiveModule(touch.Path)
		if mod == "" {
			continue
		}
		modules = append(modules, mod)
		paths = append(paths, touch.Path)
	}
	if sawTest || len(modules) < proactiveModuleTouches {
		return "", nil
	}
	last := modules[len(modules)-proactiveModuleTouches:]
	mod := last[0]
	for _, m := range last[1:] {
		if m != mod {
			return "", nil
		}
	}
	shown := paths
	if len(shown) > 6 {
		shown = shown[len(shown)-6:]
	}
	return proactiveClassModule, &proposal{
		Title:         "Prepare tests for " + mod,
		Why:           fmt.Sprintf("touched %s at least %d times and has not run tests in this run", mod, proactiveModuleTouches),
		Done:          "recent paths: " + strings.Join(shown, ", "),
		Propose:       "run the package tests for " + mod + " (still policy-gated)",
		Risk:          "read-only prep; adopting queues a steer, it does not write or run",
		prepareModule: mod,
	}
}

func proactiveTestFail(history []proactiveTouch) (string, *proposal) {
	if len(history) == 0 {
		return "", nil
	}
	last := history[len(history)-1]
	if last.Tool != "run" || !last.Failed || !looksLikeTestCommand(last.Command) {
		return "", nil
	}
	cmd := strings.Join(last.Command, " ")
	return proactiveClassTestFail, &proposal{
		Title:         "Tests failed",
		Why:           "the last command looked like a test run and did not succeed",
		Done:          "command: " + cmd + "\n" + last.Obs,
		Propose:       "re-run `" + cmd + "` after the next edit, or inspect the failure excerpt",
		Risk:          "read-only excerpt; adopting only steers, it does not re-run yet",
		prepareModule: proactiveLastModule(history),
	}
}

func proactiveImpactEdit(history []proactiveTouch) (string, *proposal) {
	sawImpact := false
	var lastEdit proactiveTouch
	for _, touch := range history {
		if touch.Tool == "code.impact" {
			sawImpact = true
		}
		if touch.Tool == "edit" || touch.Tool == "patch" {
			lastEdit = touch
		}
	}
	if !sawImpact || lastEdit.Path == "" {
		return "", nil
	}
	return proactiveClassImpact, &proposal{
		Title:         "Impact map is ready for " + lastEdit.Path,
		Why:           "this run already called code.impact and then edited",
		Done:          "edited " + lastEdit.Path,
		Propose:       "read remaining dependents from the last impact map before more writes",
		Risk:          "read-only reminder; adopting steers the current run",
		prepareModule: proactiveModule(lastEdit.Path),
	}
}

func proactiveLastModule(history []proactiveTouch) string {
	for i := len(history) - 1; i >= 0; i-- {
		if module := proactiveModule(history[i].Path); module != "" {
			return module
		}
	}
	return ""
}

func (d *Daemon) emitProposal(sess *sessionstore.Session, task *scheduler.ExecutionRun, class string, seed *proposal, tr *Transcript) {
	_ = tr // proposals must never become transcript turns
	if seed == nil || sess == nil || task == nil {
		return
	}
	now := d.proposals.now()
	key := proactiveKey(sess.SessionID, class)
	d.proposals.mu.Lock()
	if d.proposals.muted[key] {
		d.proposals.mu.Unlock()
		return
	}
	if last, ok := d.proposals.lastFired[key]; ok && now.Sub(last) < proactiveCooldown {
		d.proposals.mu.Unlock()
		return
	}
	pending := 0
	for _, item := range d.proposals.items {
		if item.SessionID == sess.SessionID && item.Status == proactiveStatusPending {
			pending++
		}
	}
	if pending >= proactivePendingCap {
		d.proposals.mu.Unlock()
		return
	}
	card := *seed
	card.ID = sessionstore.NewID("prop")
	card.SessionID = sess.SessionID
	card.RunID = task.RunID
	card.Class = class
	card.Status = proactiveStatusPending
	card.Created = now
	item := card
	previousLen := len(d.proposals.items)
	previousFired, hadPreviousFired := d.proposals.lastFired[key]
	d.proposals.items = append(d.proposals.items, &item)
	d.proposals.lastFired[key] = now
	if err := d.proposals.persistLocked(); err != nil {
		d.proposals.items = d.proposals.items[:previousLen]
		if hadPreviousFired {
			d.proposals.lastFired[key] = previousFired
		} else {
			delete(d.proposals.lastFired, key)
		}
		d.proposals.mu.Unlock()
		d.record(sess.SessionID, "ExecutionProgressed", task.RunID, "go", map[string]any{
			"status": "proposal_persistence_failed", "class": class,
		}, "")
		return
	}
	d.proposals.mu.Unlock()
	d.record(sess.SessionID, "ExecutionProgressed", task.RunID, "go", map[string]any{
		"status": "proposal_created", "proposal_id": card.ID, "class": class, "title": card.Title,
		"prepared": card.Prepared, "trigger_version": card.TriggerVersion, "preparation": card.Preparation,
	}, "")
	d.events.Publish(sess.SessionID, map[string]any{
		"type": "proposal.created", "session_id": sess.SessionID, "task_id": task.RunID,
		"proposal_id": card.ID, "class": class, "title": card.Title, "why": card.Why,
		"done": card.Done, "propose": card.Propose, "risk": card.Risk,
		"prepared": card.Prepared, "evidence": card.Evidence,
		"preparation": card.Preparation, "trigger_version": card.TriggerVersion,
		"payload": map[string]any{
			"proposal_id": card.ID, "class": class, "title": card.Title, "why": card.Why,
			"done": card.Done, "propose": card.Propose, "risk": card.Risk,
			"prepared": card.Prepared, "evidence": card.Evidence,
			"preparation": card.Preparation, "trigger_version": card.TriggerVersion,
		},
	})
}

func (d *Daemon) pendingProposalCount(sessionID string) int {
	if d == nil || d.proposals == nil {
		return 0
	}
	d.proposals.mu.Lock()
	defer d.proposals.mu.Unlock()
	n := 0
	for _, item := range d.proposals.items {
		if item.SessionID == sessionID && item.Status == proactiveStatusPending {
			n++
		}
	}
	return n
}

func (d *Daemon) handleProposalList(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, err := d.requireNamedSession(p.SessionID, params)
	if err != nil {
		return nil, err
	}
	d.proposals.mu.Lock()
	defer d.proposals.mu.Unlock()
	out := make([]*proposal, 0)
	for _, item := range d.proposals.items {
		if item.SessionID == sess.SessionID {
			cp := *item
			cp.Evidence = append([]proposalEvidence(nil), item.Evidence...)
			out = append(out, &cp)
		}
	}
	return map[string]any{"proposals": out, "pending": d.lockedPending(sess.SessionID), "enabled": proactiveEnabled()}, nil
}

func (d *Daemon) lockedPending(sessionID string) int {
	n := 0
	for _, item := range d.proposals.items {
		if item.SessionID == sessionID && item.Status == proactiveStatusPending {
			n++
		}
	}
	return n
}

func (d *Daemon) handleProposalIgnore(params json.RawMessage) (any, error) {
	var p struct {
		ProposalID string `json:"proposal_id"`
		Mute       bool   `json:"mute"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	card, err := d.setProposalStatus(strings.TrimSpace(p.ProposalID), proactiveStatusIgnored, p.Mute)
	if err != nil {
		return nil, err
	}
	d.record(card.SessionID, "ExecutionProgressed", card.RunID, "user", map[string]any{
		"status": "proposal_ignored", "proposal_id": card.ID, "class": card.Class, "muted": p.Mute,
	}, "")
	return map[string]any{"ignored": true, "proposal_id": card.ID, "muted": p.Mute}, nil
}

func (d *Daemon) handleProposalAccept(params json.RawMessage) (any, error) {
	var p struct {
		ProposalID string `json:"proposal_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	card, err := d.setProposalStatus(strings.TrimSpace(p.ProposalID), proactiveStatusAccepted, false)
	if err != nil {
		return nil, err
	}
	message := "OPERATOR ACCEPTED PROPOSAL (read-only prep; still follow policy): " + card.Propose
	steered := false
	if _, ok := d.sched.Get(card.RunID); ok {
		if _, err := d.handleTaskSteer(mustProposalSteer(card.RunID, message)); err == nil {
			steered = true
		}
	}
	d.record(card.SessionID, "ExecutionProgressed", card.RunID, "user", map[string]any{
		"status": "proposal_accepted", "proposal_id": card.ID, "class": card.Class, "steered": steered,
	}, "")
	return map[string]any{"accepted": true, "proposal_id": card.ID, "steered": steered, "run_id": card.RunID}, nil
}

func mustProposalSteer(runID, message string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"run_id": runID, "message": message})
	return raw
}

func (d *Daemon) setProposalStatus(id, status string, mute bool) (*proposal, error) {
	if id == "" {
		return nil, fmt.Errorf("proposal_id is required")
	}
	if d.proposals == nil {
		return nil, fmt.Errorf("unknown proposal %s", id)
	}
	d.proposals.mu.Lock()
	defer d.proposals.mu.Unlock()
	for _, item := range d.proposals.items {
		if item.ID != id {
			continue
		}
		if item.Status != proactiveStatusPending {
			return nil, fmt.Errorf("proposal %s is %s", id, item.Status)
		}
		previousStatus := item.Status
		item.Status = status
		key := proactiveKey(item.SessionID, item.Class)
		previousMuted := d.proposals.muted[key]
		if mute {
			d.proposals.muted[key] = true
		}
		if err := d.proposals.persistLocked(); err != nil {
			item.Status = previousStatus
			if previousMuted {
				d.proposals.muted[key] = true
			} else {
				delete(d.proposals.muted, key)
			}
			return nil, err
		}
		cp := *item
		cp.Evidence = append([]proposalEvidence(nil), item.Evidence...)
		return &cp, nil
	}
	return nil, fmt.Errorf("unknown proposal %s", id)
}
