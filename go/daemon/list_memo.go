package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// listSearchMemo caches the last successful bounded list/search observation
// for a run. The kernel FileRead decision still runs on every call; a hit
// only skips the Zig spawn. CARINA_LIST_MEMO=0|off|false disables it.
type listSearchMemo struct {
	mu       sync.Mutex
	hits     map[string]string
	zigCalls atomic.Int64
}

func newListSearchMemo() *listSearchMemo {
	return &listSearchMemo{hits: make(map[string]string)}
}

func listSearchMemoEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CARINA_LIST_MEMO"))) {
	case "0", "off", "false":
		return false
	}
	return true
}

func listSearchMemoKey(sessionID, runID, tool, root, pattern string) string {
	return sessionID + "\x00" + runID + "\x00" + tool + "\x00" + filepath.Clean(root) + "\x00" + pattern
}

func (d *Daemon) lookupListSearchMemo(sessionID, runID, tool, root, pattern string) (string, bool) {
	if d == nil || d.listSearchMemo == nil || !listSearchMemoEnabled() {
		return "", false
	}
	if sessionID == "" || runID == "" || root == "" {
		return "", false
	}
	key := listSearchMemoKey(sessionID, runID, tool, root, pattern)
	d.listSearchMemo.mu.Lock()
	defer d.listSearchMemo.mu.Unlock()
	display, ok := d.listSearchMemo.hits[key]
	return display, ok
}

func (d *Daemon) storeListSearchMemo(sessionID, runID, tool, root, pattern, display string) {
	if d == nil || d.listSearchMemo == nil || !listSearchMemoEnabled() {
		return
	}
	if sessionID == "" || runID == "" || root == "" {
		return
	}
	key := listSearchMemoKey(sessionID, runID, tool, root, pattern)
	d.listSearchMemo.mu.Lock()
	defer d.listSearchMemo.mu.Unlock()
	d.listSearchMemo.hits[key] = display
}

func (d *Daemon) invalidateListSearchMemo(sessionID string) {
	if d == nil || d.listSearchMemo == nil || sessionID == "" {
		return
	}
	prefix := sessionID + "\x00"
	d.listSearchMemo.mu.Lock()
	defer d.listSearchMemo.mu.Unlock()
	for key := range d.listSearchMemo.hits {
		if strings.HasPrefix(key, prefix) {
			delete(d.listSearchMemo.hits, key)
		}
	}
}

func (d *Daemon) noteListSearchZig() {
	if d == nil || d.listSearchMemo == nil {
		return
	}
	d.listSearchMemo.zigCalls.Add(1)
}

func (d *Daemon) listSearchZigCalls() int64 {
	if d == nil || d.listSearchMemo == nil {
		return 0
	}
	return d.listSearchMemo.zigCalls.Load()
}
