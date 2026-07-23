package tuiapp

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Nebutra/carina/go/config"
	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/tui"
)

type runtimeCoordinator struct {
	mu         sync.Mutex
	home       string
	root       string
	controller rebindController
	prepared   map[uint64]coordinatedRebind
	sender     tui.Sender
	stopWatch  context.CancelFunc
}

type rebindController interface {
	PrepareTarget(tui.ConnectionTarget) (uint64, error)
	CommitPrepared(uint64) error
	AbortPrepared(uint64) uint64
	AcknowledgePrepared(uint64)
}

type coordinatedRebind struct {
	sourceRoot string
	target     tui.ConnectionTarget
	committed  bool
}

func newRuntimeCoordinator(home, root string, controller rebindController) *runtimeCoordinator {
	return &runtimeCoordinator{home: home, root: root, controller: controller, prepared: make(map[uint64]coordinatedRebind)}
}

func (c *runtimeCoordinator) currentRoot() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.root
}

func (c *runtimeCoordinator) prepare(target tui.ConnectionTarget) (uint64, error) {
	token, err := c.controller.PrepareTarget(target)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.prepared[token] = coordinatedRebind{sourceRoot: c.root, target: target}
	c.mu.Unlock()
	return token, nil
}

func (c *runtimeCoordinator) commit(token uint64) error {
	if err := c.controller.CommitPrepared(token); err != nil {
		return err
	}
	c.mu.Lock()
	rebind, ok := c.prepared[token]
	if !ok {
		c.mu.Unlock()
		return fmt.Errorf("prepared runtime target %d is unavailable", token)
	}
	rebind.committed = true
	c.prepared[token] = rebind
	var superseded []uint64
	for preparedToken, prepared := range c.prepared {
		if preparedToken != token && prepared.committed {
			delete(c.prepared, preparedToken)
			superseded = append(superseded, preparedToken)
		}
	}
	target := rebind.target
	c.root = target.WorkspaceRoot
	sender := c.sender
	c.restartWatcherLocked()
	c.mu.Unlock()
	if sender != nil {
		c.sendKeymapReload(sender, target.WorkspaceRoot)
	}
	for _, preparedToken := range superseded {
		c.controller.AbortPrepared(preparedToken)
	}
	return nil
}

func (c *runtimeCoordinator) abort(token uint64) uint64 {
	generation := c.controller.AbortPrepared(token)
	c.mu.Lock()
	rebind, ok := c.prepared[token]
	delete(c.prepared, token)
	if ok && rebind.committed {
		c.root = rebind.sourceRoot
		c.restartWatcherLocked()
	}
	sender, root := c.sender, c.root
	c.mu.Unlock()
	if ok && rebind.committed && sender != nil {
		c.sendKeymapReload(sender, root)
	}
	return generation
}

func (c *runtimeCoordinator) acknowledge(token uint64) {
	c.controller.AcknowledgePrepared(token)
	c.mu.Lock()
	delete(c.prepared, token)
	c.mu.Unlock()
}

func (c *runtimeCoordinator) startWatcher(sender tui.Sender) {
	c.mu.Lock()
	c.sender = sender
	c.restartWatcherLocked()
	c.mu.Unlock()
}

func (c *runtimeCoordinator) restartWatcherLocked() {
	if c.stopWatch != nil {
		c.stopWatch()
		c.stopWatch = nil
	}
	if c.sender == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.stopWatch = cancel
	home, root, sender := c.home, c.root, c.sender
	go tui.WatchKeybindings(ctx, home, root, sender)
}

func (c *runtimeCoordinator) close() {
	c.mu.Lock()
	if c.stopWatch != nil {
		c.stopWatch()
	}
	c.stopWatch = nil
	c.mu.Unlock()
}

func (c *runtimeCoordinator) keymapUpdater() tui.KeymapUpdateFunc {
	return func(action string, keys []string, remove bool) ([]tui.KeyBindingOverride, error) {
		root := c.currentRoot()
		path := filepath.Join(root, ".carina", "config.json")
		_, locks, err := config.LoadWithManaged(c.home, root, config.DefaultManagedPath())
		if err != nil {
			return nil, err
		}
		if locks.Locked("tui_keybindings") {
			return nil, fmt.Errorf("tui_keybindings is locked by %s", locks.Source)
		}
		if err := config.UpdateTUIKeybinding(path, action, keys, remove); err != nil {
			return nil, err
		}
		cfg, err := config.Load(c.home, root)
		if err != nil {
			return nil, err
		}
		return tui.ParseKeyBindingOverrides(cfg.TUIKeybindings)
	}
}

func (c *runtimeCoordinator) sendKeymapReload(sender tui.Sender, root string) {
	cfg, err := config.Load(c.home, root)
	var overrides []tui.KeyBindingOverride
	if err == nil {
		overrides, err = tui.ParseKeyBindingOverrides(cfg.TUIKeybindings)
	}
	sender.Send(tui.KeymapReloadMsg{WorkspaceRoot: root, Overrides: overrides, Err: err})
}

func (c *runtimeCoordinator) listWorkspaces() ([]tui.WorkspaceListItem, error) {
	entries, err := localruntime.ScanRegistry(c.home)
	if err != nil {
		return nil, err
	}
	currentRoot := c.currentRoot()
	items := make([]tui.WorkspaceListItem, 0, len(entries))
	for _, entry := range entries {
		item := tui.WorkspaceListItem{Error: entry.Error}
		if entry.Spec != nil {
			item.Root = entry.Spec.Workspace.CanonicalRoot
			item.Name = filepath.Base(item.Root)
			item.RuntimeID = entry.Spec.RuntimeID
		} else if entry.Descriptor != nil {
			item.Root = entry.Descriptor.Workspace.CanonicalRoot
			item.Name = filepath.Base(item.Root)
			item.RuntimeID = entry.Descriptor.RuntimeID
		}
		if item.Root == "" {
			item.Root = entry.RuntimeDir
		}
		item.Current = filepath.Clean(item.Root) == filepath.Clean(currentRoot)
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Current != items[j].Current {
			return items[i].Current
		}
		return items[i].Root < items[j].Root
	})
	return items, nil
}
