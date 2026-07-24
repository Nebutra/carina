package tuiapp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/config"
	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

type bootstrapStage int

const (
	bootstrapWorkspace bootstrapStage = iota
	bootstrapMode
	bootstrapRuntime
	bootstrapIdentity
	bootstrapSession
	bootstrapReady
)

const bootstrapComponentID ui.ComponentID = "bootstrap-screen"

const bootstrapFirstPaintDelay = 35 * time.Millisecond

type bootstrapPrepared struct {
	home        string
	mode        localruntime.Mode
	config      config.Config
	socket      string
	runtimeSpec *localruntime.Spec
	projectRoot string
	locale      string
	keybindings []tui.KeyBindingOverride
	sessionID   string
	logPath     string
}

type bootstrapFailure struct {
	stage   bootstrapStage
	code    microcopy.BootstrapCode
	err     error
	outcome tui.Outcome
}

type bootstrapStepMsg struct {
	generation   uint64
	stage        bootstrapStage
	prepared     bootstrapPrepared
	modeDecision bool
	failure      *bootstrapFailure
}

type bootstrapCopy struct {
	title, running, workspace, mode, runtime, identity, session, ready string
	retry, details, hideDetails, exit, workspaceMode, legacyMode       string
	modePrompt                                                         string
}

type bootstrapModel struct {
	opts            Options
	bootstrapLocale string
	copy            bootstrapCopy
	stage           bootstrapStage
	prepared        bootstrapPrepared
	failure         *bootstrapFailure
	modeDecision    bool
	pendingMode     localruntime.Mode
	detailsOpen     bool
	selected        int
	hovered         string
	width           int
	height          int
	altScreen       bool
	allowAltScreen  bool
	modeOnly        bool
	mode            localruntime.Mode
	done            bool
	cancelled       bool
	outcome         tui.Outcome
	operation       uint64
	quitOnReady     bool
	runtime         *ui.Runtime
	component       *bootstrapComponent
}

func newBootstrapModel(opts Options, locale string, altScreen bool) *bootstrapModel {
	return newBootstrapModelWithRuntime(opts, locale, altScreen, nil, true)
}

func newBootstrapModelWithRuntime(opts Options, locale string, altScreen bool, runtime *ui.Runtime, quitOnReady bool) *bootstrapModel {
	if runtime == nil {
		runtime = ui.NewRuntime()
	}
	m := &bootstrapModel{
		opts: opts, bootstrapLocale: locale, copy: bootstrapText(locale),
		stage: bootstrapWorkspace, width: 80, height: 24, allowAltScreen: altScreen,
		operation: 1, quitOnReady: quitOnReady, runtime: runtime,
	}
	m.component = &bootstrapComponent{model: m, Base: ui.Base{ComponentID: bootstrapComponentID}}
	m.runtime.Mount(m.component)
	m.runtime.Screens.Transition(ui.ScreenBootstrap, bootstrapComponentID, ui.FocusSnapshot{}, nil)
	m.runtime.SetFocus(bootstrapComponentID, ui.FocusProgrammatic)
	return m
}

func newRuntimeModeChoiceModel(locale string) *bootstrapModel {
	m := newBootstrapModel(Options{}, locale, true)
	m.stage = bootstrapMode
	m.modeDecision = true
	m.modeOnly = true
	m.altScreen = true
	return m
}

func (m *bootstrapModel) Init() tea.Cmd {
	if m.modeOnly {
		return nil
	}
	stage := m.stageCmd()
	return func() tea.Msg {
		timer := time.NewTimer(bootstrapFirstPaintDelay)
		defer timer.Stop()
		<-timer.C
		return stage()
	}
}

func (m *bootstrapModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(typed.Width, 1), max(typed.Height, 1)
		m.runtime.InvalidateGeometry()
		return m, nil
	case bootstrapStepMsg:
		return m.applyStep(typed)
	case tea.MouseMotionMsg:
		return m.dispatchPointer(typed.Mouse(), ui.PointerMove)
	case tea.MouseClickMsg:
		if typed.Button != tea.MouseLeft {
			return m, nil
		}
		return m.dispatchPointer(typed.Mouse(), ui.PointerClick)
	case tea.MouseReleaseMsg:
		return m.dispatchPointer(typed.Mouse(), ui.PointerRelease)
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	result := m.runtime.Dispatch(ui.Event{Kind: ui.EventKey, Key: strings.ToLower(key.String())})
	return m.applyActions(result)
}

func (m *bootstrapModel) dispatchPointer(mouse tea.Mouse, kind ui.PointerKind) (tea.Model, tea.Cmd) {
	result := m.runtime.Dispatch(ui.Event{Kind: ui.EventPointer, Pointer: ui.PointerEvent{
		Kind: kind, X: mouse.X, Y: mouse.Y, Button: int(mouse.Button),
	}})
	return m.applyActions(result)
}

func (m *bootstrapModel) applyActions(result ui.Result) (tea.Model, tea.Cmd) {
	if !result.Handled || len(result.Actions) == 0 {
		return m, nil
	}
	action, _ := result.Actions[0].Data.(string)
	switch action {
	case "workspace":
		if m.modeOnly {
			m.mode, m.done = localruntime.ModeWorkspace, true
			return m, tea.Quit
		}
		m.pendingMode, m.modeDecision = localruntime.ModeWorkspace, false
		return m, m.stageCmd()
	case "legacy":
		if m.modeOnly {
			m.mode, m.done = localruntime.ModeLegacy, true
			return m, tea.Quit
		}
		m.pendingMode, m.modeDecision = localruntime.ModeLegacy, false
		return m, m.stageCmd()
	case "retry":
		m.operation++
		if m.failure != nil && m.failure.stage == bootstrapIdentity {
			// Identity consumes locale and keybindings from the prepared config.
			// Re-resolve runtime/config so an operator's external repair is visible
			// instead of retrying the same stale snapshot forever.
			m.stage = bootstrapRuntime
		}
		m.failure, m.detailsOpen = nil, false
		return m, m.stageCmd()
	case "details":
		m.detailsOpen = !m.detailsOpen
		return m, nil
	case "exit":
		m.cancelled = true
		if m.failure != nil {
			m.outcome = m.failure.outcome
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m *bootstrapModel) applyStep(step bootstrapStepMsg) (tea.Model, tea.Cmd) {
	if step.generation != 0 && step.generation != m.operation {
		return m, nil
	}
	if step.failure != nil {
		m.failure = step.failure
		m.stage = step.failure.stage
		m.selected = 0
		return m, nil
	}
	m.prepared = step.prepared
	m.stage = step.stage
	m.modeDecision = step.modeDecision
	if m.allowAltScreen && step.stage >= bootstrapIdentity &&
		!strings.EqualFold(step.prepared.config.TUIAlternateScreen, "never") {
		m.altScreen = true
	}
	m.pendingMode = ""
	m.selected = 0
	if m.modeDecision {
		return m, nil
	}
	if m.stage == bootstrapReady {
		m.done = true
		if m.quitOnReady {
			return m, tea.Quit
		}
		return m, nil
	}
	return m, m.stageCmd()
}

func (m *bootstrapModel) stageCmd() tea.Cmd {
	stage := m.stage
	generation := m.operation
	prepared := m.prepared
	pendingMode := m.pendingMode
	opts := m.opts
	return func() tea.Msg {
		switch stage {
		case bootstrapWorkspace:
			home, err := os.UserHomeDir()
			if err != nil {
				return failedBootstrapStep(generation, stage, microcopy.BootstrapResolveHomeFailed, tui.OutcomeRuntimeError, err)
			}
			prepared.home = home
			prepared.projectRoot = strings.TrimSpace(opts.WorkspaceRoot)
			return bootstrapStepMsg{generation: generation, stage: bootstrapMode, prepared: prepared}
		case bootstrapMode:
			if pendingMode != "" {
				if err := localruntime.WriteMode(prepared.home, pendingMode); err != nil {
					return failedBootstrapStep(generation, stage, microcopy.BootstrapConfigFailed, tui.OutcomeRuntimeError, err)
				}
				prepared.mode = pendingMode
				return bootstrapStepMsg{generation: generation, stage: bootstrapRuntime, prepared: prepared}
			}
			mode, err := localruntime.ResolveMode(prepared.home)
			if errors.Is(err, localruntime.ErrModeDecisionRequired) {
				return bootstrapStepMsg{generation: generation, stage: bootstrapMode, prepared: prepared, modeDecision: true}
			}
			if err != nil {
				return failedBootstrapStep(generation, stage, microcopy.BootstrapConfigFailed, tui.OutcomeRuntimeError, err)
			}
			prepared.mode = mode
			return bootstrapStepMsg{generation: generation, stage: bootstrapRuntime, prepared: prepared}
		case bootstrapRuntime:
			if prepared.mode == localruntime.ModeWorkspace {
				resolution, err := localruntime.Resolve(prepared.home, prepared.projectRoot, prepared.mode)
				if err != nil {
					if errors.Is(err, config.ErrInvalidTUILocale) {
						return failedBootstrapStep(generation, stage, microcopy.BootstrapLocaleInvalid, tui.OutcomeUsage, err)
					}
					return failedBootstrapStep(generation, stage, microcopy.BootstrapConfigFailed, tui.OutcomeRuntimeError, err)
				}
				if explicitSocket := strings.TrimSpace(opts.Socket); explicitSocket != "" {
					resolution, err = localruntime.ApplyOverrides(prepared.home, resolution, localruntime.Overrides{Socket: explicitSocket})
					if err != nil {
						return failedBootstrapStep(generation, stage, microcopy.BootstrapConfigFailed, tui.OutcomeRuntimeError, err)
					}
				}
				prepared.config = resolution.Config
				prepared.projectRoot = resolution.Workspace.CanonicalRoot
				prepared.socket = resolution.Spec.Paths.SocketPath
				prepared.logPath = resolution.Spec.Paths.LogPath
				prepared.runtimeSpec = &resolution.Spec
			} else {
				if prepared.projectRoot == "" {
					root, err := os.Getwd()
					if err != nil {
						return failedBootstrapStep(generation, stage, microcopy.BootstrapStartupFailed, tui.OutcomeRuntimeError, err)
					}
					prepared.projectRoot = root
				}
				cfg, err := config.Load(prepared.home, prepared.projectRoot)
				if err != nil {
					code, outcome := microcopy.BootstrapConfigFailed, tui.OutcomeRuntimeError
					if errors.Is(err, config.ErrInvalidTUILocale) {
						code, outcome = microcopy.BootstrapLocaleInvalid, tui.OutcomeUsage
					}
					return failedBootstrapStep(generation, stage, code, outcome, err)
				}
				prepared.config = cfg
				prepared.socket = strings.TrimSpace(opts.Socket)
				if prepared.socket == "" {
					prepared.socket = cfg.Socket
				}
				prepared.logPath = filepath.Join(filepath.Dir(prepared.socket), "daemon.log")
			}
			return bootstrapStepMsg{generation: generation, stage: bootstrapIdentity, prepared: prepared}
		case bootstrapIdentity:
			loc, err := microcopy.ResolveLocale(opts.Locale, prepared.config.TUILocale)
			if err != nil {
				return failedBootstrapStep(generation, stage, microcopy.BootstrapLocaleInvalid, tui.OutcomeUsage, err)
			}
			keybindings, err := tui.ParseKeyBindingOverrides(prepared.config.TUIKeybindings)
			if err != nil {
				return failedBootstrapStep(generation, stage, microcopy.BootstrapConfigFailed, tui.OutcomeUsage, err)
			}
			prepared.locale, prepared.keybindings = loc, keybindings
			return bootstrapStepMsg{generation: generation, stage: bootstrapSession, prepared: prepared}
		case bootstrapSession:
			prepared.sessionID = strings.TrimSpace(opts.SessionID)
			if prepared.sessionID == "" {
				sessionID, err := resolveSession(nil, prepared.config.StateDir, prepared.projectRoot)
				if err != nil {
					return failedBootstrapStep(generation, stage, microcopy.BootstrapRecoveryFailed, tui.OutcomeRuntimeError, err)
				}
				prepared.sessionID = sessionID
			}
			return bootstrapStepMsg{generation: generation, stage: bootstrapReady, prepared: prepared}
		default:
			return bootstrapStepMsg{generation: generation, stage: bootstrapReady, prepared: prepared}
		}
	}
}

func failedBootstrapStep(generation uint64, stage bootstrapStage, code microcopy.BootstrapCode, outcome tui.Outcome, err error) bootstrapStepMsg {
	return bootstrapStepMsg{
		generation: generation,
		failure:    &bootstrapFailure{stage: stage, code: code, err: err, outcome: outcome},
	}
}

func (m *bootstrapModel) View() tea.View {
	frame := m.runtime.BeginFrame(m.component, ui.Rect{Width: max(m.width, 1), Height: max(m.height, 1)})
	view := tea.NewView(frame.Root.Content)
	view.AltScreen = m.altScreen
	if frame.AllMotion {
		view.MouseMode = tea.MouseModeAllMotion
	}
	return view
}

type bootstrapComponent struct {
	ui.Base
	model *bootstrapModel
}

func (c *bootstrapComponent) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight})
}

type bootstrapAction struct {
	id, label string
}

func (c *bootstrapComponent) actions() []bootstrapAction {
	m := c.model
	if m.failure != nil {
		details := m.copy.details
		if m.detailsOpen {
			details = m.copy.hideDetails
		}
		return []bootstrapAction{{"retry", m.copy.retry}, {"details", details}, {"exit", m.copy.exit}}
	}
	if m.modeDecision {
		return []bootstrapAction{{"workspace", m.copy.workspaceMode}, {"legacy", m.copy.legacyMode}, {"exit", m.copy.exit}}
	}
	return nil
}

func (c *bootstrapComponent) Render(ui.RenderContext) ui.Node {
	m := c.model
	actions := c.actions()
	if len(actions) > 0 {
		m.selected = min(max(m.selected, 0), len(actions)-1)
	}
	width := max(c.Bounds.Width, 1)
	innerWidth := max(min(width-4, 76), 1)
	lines := []string{m.copy.title, c.statusLine()}
	if m.prepared.projectRoot != "" && c.Bounds.Height >= 8 {
		lines = append(lines, "", compactBootstrapPath(m.prepared.projectRoot, innerWidth))
	}
	if len(actions) > 0 {
		lines = append(lines, "")
	}
	actionLine := make(map[int]int, len(actions))
	for index, action := range actions {
		prefix := "  "
		if index == m.selected {
			prefix = "> "
		} else if action.id == m.hovered {
			prefix = ". "
		}
		actionLine[index] = len(lines)
		lines = append(lines, prefix+action.label)
	}
	if m.failure != nil && m.detailsOpen {
		lines = append(lines, "", "stage: "+c.stageLabel(m.stage), "reason: "+m.failure.err.Error())
		if m.prepared.logPath != "" {
			lines = append(lines, "log: "+m.prepared.logPath)
		}
	}
	for index := range lines {
		lines[index] = ansi.Truncate(lines[index], innerWidth, "")
	}
	if len(lines) > c.Bounds.Height {
		lines = lines[:c.Bounds.Height]
	}
	x := c.Bounds.X + max((width-innerWidth)/2, 0)
	y := c.Bounds.Y + max((c.Bounds.Height-len(lines))/3, 0)
	screen := make([]string, 0, y-c.Bounds.Y+len(lines))
	for row := c.Bounds.Y; row < y; row++ {
		screen = append(screen, "")
	}
	for _, line := range lines {
		screen = append(screen, strings.Repeat(" ", max(x-c.Bounds.X, 0))+line)
	}
	hits := make([]ui.HitRegion, 0, len(actions))
	for index, action := range actions {
		line, visible := actionLine[index]
		if !visible || line >= len(lines) {
			continue
		}
		hits = append(hits, ui.HitRegion{
			ID: ui.HitID("bootstrap:" + action.id), Owner: bootstrapComponentID,
			Bounds: ui.Rect{X: x, Y: y + line, Width: min(ansi.StringWidth(lines[line]), innerWidth), Height: 1},
			Z:      1, Kind: ui.HitActivate, Action: "bootstrap-action", Data: action.id, Focusable: true,
		})
	}
	return ui.Node{
		ID: bootstrapComponentID, Bounds: c.Bounds, Content: strings.Join(screen, "\n"),
		Focusable: true, Focused: c.Focused(), Hit: hits,
	}
}

func (c *bootstrapComponent) statusLine() string {
	m := c.model
	if m.failure != nil {
		return microcopy.Bootstrap(m.failure.code, microcopy.Args{"reason": m.failure.err.Error()}, m.bootstrapLocale)
	}
	if m.modeDecision {
		return m.copy.modePrompt
	}
	return fmt.Sprintf("[%d/6] %s  %s", int(m.stage)+1, c.stageLabel(m.stage), m.copy.running)
}

func (c *bootstrapComponent) stageLabel(stage bootstrapStage) string {
	switch stage {
	case bootstrapWorkspace:
		return c.model.copy.workspace
	case bootstrapMode:
		return c.model.copy.mode
	case bootstrapRuntime:
		return c.model.copy.runtime
	case bootstrapIdentity:
		return c.model.copy.identity
	case bootstrapSession:
		return c.model.copy.session
	default:
		return c.model.copy.ready
	}
}

func (c *bootstrapComponent) Handle(event ui.Event) ui.Result {
	actions := c.actions()
	if len(actions) == 0 {
		return ui.Result{}
	}
	m := c.model
	if event.Kind == ui.EventPointer {
		if event.Pointer.Kind == ui.PointerLeave {
			m.hovered = ""
			return ui.Result{Handled: true}
		}
		if event.Pointer.Hit == nil || event.Pointer.Hit.Action != "bootstrap-action" {
			return ui.Result{}
		}
		id, ok := event.Pointer.Hit.Data.(string)
		if !ok {
			return ui.Result{}
		}
		for index, action := range actions {
			if action.id == id {
				m.hovered = id
				if event.Pointer.Kind == ui.PointerClick {
					m.selected = index
				}
				break
			}
		}
		if event.Pointer.Kind == ui.PointerClick {
			return bootstrapActionResult(id)
		}
		return ui.Result{Handled: true}
	}
	if event.Kind != ui.EventKey {
		return ui.Result{}
	}
	switch event.Key {
	case "up", "k", "shift+tab":
		m.selected = (m.selected + len(actions) - 1) % len(actions)
	case "down", "j", "tab":
		m.selected = (m.selected + 1) % len(actions)
	case "enter":
		return bootstrapActionResult(actions[m.selected].id)
	case "w", "1":
		if m.modeDecision {
			return bootstrapActionResult("workspace")
		}
	case "l", "2":
		if m.modeDecision {
			return bootstrapActionResult("legacy")
		}
	case "r":
		if m.failure != nil {
			return bootstrapActionResult("retry")
		}
	case "d":
		if m.failure != nil {
			return bootstrapActionResult("details")
		}
	case "q", "esc", "ctrl+c", "c", "3":
		return bootstrapActionResult("exit")
	default:
		return ui.Result{}
	}
	return ui.Result{Handled: true}
}

func bootstrapActionResult(id string) ui.Result {
	return ui.Result{Handled: true, Actions: []ui.Action{{Source: bootstrapComponentID, Name: "activate", Data: id}}}
}

func compactBootstrapPath(path string, width int) string {
	path = strings.TrimSpace(path)
	if ansi.StringWidth(path) <= width {
		return path
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) >= 2 {
		path = ".../" + strings.Join(parts[len(parts)-2:], "/")
	}
	return ansi.Truncate(path, width, "")
}

func bootstrapText(locale string) bootstrapCopy {
	switch microcopy.NormalizeLocale(locale) {
	case "zh":
		return bootstrapCopy{"Carina", "正在准备", "解析工作区", "选择运行模式", "准备项目运行时", "验证身份与配置", "恢复会话", "就绪", "重试", "诊断 / 详情", "隐藏详情", "取消并退出", "独立 workspace runtime（推荐）", "旧版全局 runtime", "检测到旧版全局运行时数据；请选择项目运行方式。"}
	case "ja":
		return bootstrapCopy{"Carina", "準備中", "ワークスペースを確認", "実行モードを選択", "プロジェクト runtime を準備", "ID と設定を確認", "セッションを復元", "準備完了", "再試行", "診断 / 詳細", "詳細を隠す", "終了", "分離 workspace runtime（推奨）", "従来のグローバル runtime", "従来のグローバル runtime データが見つかりました。"}
	case "ko":
		return bootstrapCopy{"Carina", "준비 중", "작업 공간 확인", "실행 모드 선택", "프로젝트 runtime 준비", "ID 및 설정 확인", "세션 복원", "준비 완료", "다시 시도", "진단 / 세부 정보", "세부 정보 숨기기", "종료", "격리 workspace runtime(권장)", "기존 전역 runtime", "기존 전역 runtime 데이터가 발견되었습니다."}
	case "es":
		return bootstrapCopy{"Carina", "Preparando", "Resolver espacio de trabajo", "Elegir modo", "Preparar runtime del proyecto", "Verificar identidad y configuración", "Restaurar sesión", "Listo", "Reintentar", "Diagnóstico / detalles", "Ocultar detalles", "Salir", "Runtime aislado por workspace (recomendado)", "Runtime global anterior", "Se encontraron datos del runtime global anterior."}
	case "fr":
		return bootstrapCopy{"Carina", "Préparation", "Résoudre l’espace de travail", "Choisir le mode", "Préparer le runtime du projet", "Vérifier l’identité et la configuration", "Restaurer la session", "Prêt", "Réessayer", "Diagnostic / détails", "Masquer les détails", "Quitter", "Runtime isolé par workspace (recommandé)", "Ancien runtime global", "Des données de l’ancien runtime global ont été détectées."}
	default:
		return bootstrapCopy{"Carina", "Preparing", "Resolve workspace", "Choose runtime mode", "Prepare project runtime", "Verify identity and configuration", "Restore session", "Ready", "Retry", "Doctor / details", "Hide details", "Exit", "Isolated workspace runtime (recommended)", "Legacy global runtime", "Legacy global runtime data was found. Choose how projects should run."}
	}
}
