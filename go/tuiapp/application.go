package tuiapp

import (
	"os"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui"
	"github.com/Nebutra/carina/go/tui/theme"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

type applicationPhase int

const (
	applicationBootstrap applicationPhase = iota
	applicationConversation
)

type connectedTaskDoneMsg struct {
	applicationGeneration uint64
	sessionID             string
	sessionGeneration     uint64
	result                ConnectedTaskResult
}

// programSender lets runtime-owned goroutines target the one application
// program without making screen construction depend on *tea.Program.
type programSender struct {
	mu     sync.RWMutex
	target tui.Sender
	ready  chan struct{}
	once   sync.Once
}

func newProgramSender() *programSender {
	return &programSender{ready: make(chan struct{})}
}

func (s *programSender) Bind(target tui.Sender) {
	if target == nil {
		return
	}
	s.once.Do(func() {
		s.mu.Lock()
		s.target = target
		s.mu.Unlock()
		close(s.ready)
	})
}

func (s *programSender) Send(msg tea.Msg) {
	<-s.ready
	s.mu.RLock()
	target := s.target
	s.mu.RUnlock()
	if target != nil {
		target.Send(msg)
	}
}

type conversationFactory func(tui.Options) (*tui.Model, error)
type connectionStarter func(tui.Sender, bootstrapPrepared, *tui.ConnectionController)

// applicationModel retains Bootstrap and Conversation inside one Bubble Tea
// lifecycle. It is the only production root model and the only owner that may
// transition between top-level screens.
type applicationModel struct {
	opts         Options
	phase        applicationPhase
	runtime      *ui.Runtime
	bootstrap    *bootstrapModel
	conversation *tui.Model

	sender      *programSender
	controller  *tui.ConnectionController
	coordinator *runtimeCoordinator

	width, height           int
	generation              uint64
	connectedTaskStarted    bool
	activeSessionID         string
	activeSessionGeneration uint64
	outcome                 tui.Outcome
	closeOnce               sync.Once

	newConversation conversationFactory
	startConnection connectionStarter
}

func newApplicationModel(opts Options, locale string, altScreen bool, sender *programSender) *applicationModel {
	if sender == nil {
		sender = newProgramSender()
	}
	runtime := ui.NewRuntime()
	return &applicationModel{
		opts:            opts,
		phase:           applicationBootstrap,
		runtime:         runtime,
		bootstrap:       newBootstrapModelWithRuntime(opts, locale, altScreen, runtime, false),
		sender:          sender,
		generation:      1,
		newConversation: tui.NewChecked,
		startConnection: startPreparedConnection,
	}
}

func (m *applicationModel) Init() tea.Cmd {
	return m.bootstrap.Init()
}

func (m *applicationModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = max(size.Width, 1), max(size.Height, 1)
	}

	switch m.phase {
	case applicationBootstrap:
		_, cmd := m.bootstrap.Update(msg)
		if m.bootstrap.cancelled {
			m.outcome = m.bootstrap.outcome
			return m, cmd
		}
		if m.bootstrap.done {
			activateCmd := m.activateConversation()
			return m, tea.Batch(cmd, activateCmd)
		}
		return m, cmd

	case applicationConversation:
		if done, ok := msg.(connectedTaskDoneMsg); ok {
			return m, m.applyConnectedTask(done)
		}
		updated, cmd := m.conversation.Update(msg)
		if conversation, ok := updated.(*tui.Model); ok {
			m.conversation = conversation
		}
		ready, ok := msg.(tui.SessionReadyMsg)
		if !ok {
			return m, cmd
		}
		if ready.Generation == 0 || ready.Generation >= m.activeSessionGeneration {
			m.activeSessionID = ready.SessionID
			m.activeSessionGeneration = ready.Generation
		}
		if m.connectedTaskStarted || m.opts.ConnectedTask == nil || ready.Call == nil {
			return m, cmd
		}
		m.connectedTaskStarted = true
		return m, tea.Batch(cmd, m.runConnectedTask(ready))
	}
	return m, nil
}

func (m *applicationModel) View() tea.View {
	if m.phase == applicationConversation && m.conversation != nil {
		return m.conversation.View()
	}
	return m.bootstrap.View()
}

func (m *applicationModel) activateConversation() tea.Cmd {
	prepared := m.bootstrap.prepared
	m.runtime.Unmount(bootstrapComponentID)
	m.runtime.InvalidateGeometry()

	target := tui.ConnectionTarget{
		Socket: prepared.socket, SessionID: prepared.sessionID, WorkspaceRoot: prepared.projectRoot,
		StateDir: prepared.config.StateDir, RuntimeSpec: prepared.runtimeSpec, AutoStart: true,
	}
	controller := tui.NewConnectionController(target)
	coordinator := newRuntimeCoordinator(prepared.home, prepared.projectRoot, controller)
	conversation, err := m.newConversation(tui.Options{
		Theme:             theme.New(theme.Detect(os.Getenv, true)),
		Locale:            prepared.locale,
		Socket:            prepared.socket,
		SessionID:         prepared.sessionID,
		WorkspaceRoot:     prepared.projectRoot,
		StateDir:          prepared.config.StateDir,
		Keybindings:       prepared.keybindings,
		NoAlternateScreen: m.opts.NoAltScreen || strings.EqualFold(prepared.config.TUIAlternateScreen, "never"),
		KeymapUpdater:     coordinator.keymapUpdater(),
		SwitchSession:     controller.Switch,
		PrepareTarget:     coordinator.prepare,
		CommitTarget:      coordinator.commit,
		AbortTarget:       coordinator.abort,
		AcknowledgeTarget: coordinator.acknowledge,
		ListWorkspaces:    coordinator.listWorkspaces,
		LoadWorkspace:     workspaceLoader(prepared.home),
		ResumeWorkspace:   workspaceResumer,
		Runtime:           m.runtime,
	})
	if err != nil {
		if conversation != nil {
			conversation.Close()
		}
		coordinator.close()
		m.restoreBootstrapFailure(err)
		return nil
	}

	m.controller = controller
	m.coordinator = coordinator
	m.conversation = conversation
	m.phase = applicationConversation
	m.generation++
	var resizeCmd tea.Cmd
	if m.width > 0 && m.height > 0 {
		_, resizeCmd = m.conversation.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	}
	m.coordinator.startWatcher(m.sender)
	m.startConnection(m.sender, prepared, controller)
	return tea.Batch(resizeCmd, m.conversation.Init())
}

func (m *applicationModel) restoreBootstrapFailure(err error) {
	m.bootstrap.done = false
	m.bootstrap.failure = &bootstrapFailure{
		stage: bootstrapReady, code: microcopy.BootstrapStartupFailed,
		err: err, outcome: tui.OutcomeUsage,
	}
	m.bootstrap.stage = bootstrapReady
	m.bootstrap.selected = 0
	m.runtime.Mount(m.bootstrap.component)
	m.runtime.Screens.Transition(ui.ScreenBootstrap, bootstrapComponentID, ui.FocusSnapshot{}, nil)
	m.runtime.SetFocus(bootstrapComponentID, ui.FocusProgrammatic)
	m.runtime.InvalidateGeometry()
}

func (m *applicationModel) runConnectedTask(ready tui.SessionReadyMsg) tea.Cmd {
	generation := m.generation
	task := m.opts.ConnectedTask
	return func() tea.Msg {
		return connectedTaskDoneMsg{
			applicationGeneration: generation,
			sessionID:             ready.SessionID,
			sessionGeneration:     ready.Generation,
			result:                task(borrowedRPC{Caller: ready.Call}),
		}
	}
}

func (m *applicationModel) applyConnectedTask(done connectedTaskDoneMsg) tea.Cmd {
	if done.applicationGeneration != m.generation ||
		done.sessionID != m.activeSessionID ||
		done.sessionGeneration != m.activeSessionGeneration ||
		m.conversation == nil || strings.TrimSpace(done.result.Notice) == "" {
		return nil
	}
	updated, cmd := m.conversation.Update(tui.OperationalNoticeMsg{
		Kind: "connected-task", Text: done.result.Notice, Outcome: done.result.Outcome,
	})
	if conversation, ok := updated.(*tui.Model); ok {
		m.conversation = conversation
	}
	return cmd
}

func (m *applicationModel) Outcome() tui.Outcome {
	if m.conversation != nil {
		return m.conversation.Outcome()
	}
	if m.bootstrap.failure != nil && m.bootstrap.cancelled {
		return m.bootstrap.failure.outcome
	}
	return m.outcome
}

func (m *applicationModel) Close() {
	m.closeOnce.Do(func() {
		if m.coordinator != nil {
			m.coordinator.close()
		}
		if m.conversation != nil {
			m.conversation.Close()
		}
	})
}

func (m *applicationModel) CloseTerminalGraphics() string {
	if m.conversation == nil {
		return ""
	}
	return m.conversation.CloseTerminalGraphics()
}

func (m *applicationModel) locale() string {
	if m.bootstrap.prepared.locale != "" {
		return m.bootstrap.prepared.locale
	}
	return m.bootstrap.bootstrapLocale
}

func startPreparedConnection(sender tui.Sender, prepared bootstrapPrepared, controller *tui.ConnectionController) {
	if prepared.runtimeSpec != nil {
		tui.ConnectControlledRuntime(sender, prepared.socket, prepared.sessionID, prepared.projectRoot, controller, *prepared.runtimeSpec)
		return
	}
	tui.ConnectControlled(sender, prepared.socket, prepared.sessionID, prepared.projectRoot, controller)
}
