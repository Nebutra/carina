package tuiapp

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/config"
	"github.com/Nebutra/carina/go/tui"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

func TestApplicationTransitionsToConversationInOneRuntime(t *testing.T) {
	app := newTestApplication(t, Options{})
	defer app.Close()
	started := 0
	app.startConnection = func(tui.Sender, bootstrapPrepared, *tui.ConnectionController) {
		started++
	}
	var injected *ui.Runtime
	app.newConversation = func(opts tui.Options) (*tui.Model, error) {
		injected = opts.Runtime
		return tui.NewChecked(opts)
	}

	_, _ = app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	_, _ = app.Update(bootstrapStepMsg{
		generation: app.bootstrap.operation,
		stage:      bootstrapReady,
		prepared:   app.bootstrap.prepared,
	})

	if app.phase != applicationConversation || app.conversation == nil {
		t.Fatal("application did not retain Conversation after Bootstrap readiness")
	}
	if injected != app.runtime {
		t.Fatal("Conversation did not reuse the application Runtime")
	}
	if current := app.runtime.Screens.Current(); current.ID != ui.ScreenConversation {
		t.Fatalf("screen=%q, want conversation", current.ID)
	}
	if started != 1 {
		t.Fatalf("connection started %d times, want once", started)
	}
	if view := app.View(); view.Content == "" {
		t.Fatal("transition produced a blank Conversation frame")
	} else if view.Cursor == nil {
		t.Fatal("Conversation did not receive deterministic composer cursor ownership")
	}
}

func TestApplicationPreservesNormalBufferPolicy(t *testing.T) {
	for _, test := range []struct {
		name       string
		noAlt      bool
		configMode string
	}{
		{name: "cli override", noAlt: true, configMode: "always"},
		{name: "config never", configMode: "never"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newTestApplication(t, Options{NoAltScreen: test.noAlt})
			defer app.Close()
			app.bootstrap.prepared.config.TUIAlternateScreen = test.configMode
			app.startConnection = func(tui.Sender, bootstrapPrepared, *tui.ConnectionController) {}
			_, _ = app.Update(bootstrapStepMsg{
				generation: app.bootstrap.operation,
				stage:      bootstrapReady,
				prepared:   app.bootstrap.prepared,
			})
			if view := app.View(); view.AltScreen {
				t.Fatalf("normal-buffer policy entered alternate screen: noAlt=%v config=%q", test.noAlt, test.configMode)
			}
		})
	}
}

func TestApplicationConstructionFailureStaysRetryableInBootstrap(t *testing.T) {
	app := newTestApplication(t, Options{})
	defer app.Close()
	app.startConnection = func(tui.Sender, bootstrapPrepared, *tui.ConnectionController) {}
	attempts := 0
	app.newConversation = func(opts tui.Options) (*tui.Model, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("invalid conversation configuration")
		}
		return tui.NewChecked(opts)
	}

	_, _ = app.Update(bootstrapStepMsg{
		generation: app.bootstrap.operation,
		stage:      bootstrapReady,
		prepared:   app.bootstrap.prepared,
	})
	if app.phase != applicationBootstrap || app.bootstrap.failure == nil {
		t.Fatal("construction failure escaped the Bootstrap recovery surface")
	}
	if app.bootstrap.failure.outcome != tui.OutcomeUsage {
		t.Fatalf("construction outcome=%v, want usage", app.bootstrap.failure.outcome)
	}
	if current := app.runtime.Screens.Current(); current.ID != ui.ScreenBootstrap {
		t.Fatalf("screen=%q, want bootstrap failure", current.ID)
	}
	if !strings.Contains(app.View().Content, "invalid conversation configuration") {
		t.Fatal("Bootstrap details omitted the construction failure")
	}

	_, retry := app.bootstrap.applyActions(bootstrapActionResult("retry"))
	if retry == nil {
		t.Fatal("Retry did not schedule a new bootstrap operation")
	}
	_, _ = app.Update(retry())
	if app.phase != applicationConversation || attempts != 2 {
		t.Fatalf("retry phase=%v attempts=%d, want conversation after two attempts", app.phase, attempts)
	}
}

func TestApplicationConnectedTaskBecomesOperationalNotice(t *testing.T) {
	app := newTestApplication(t, Options{
		ConnectedTask: func(RPC) ConnectedTaskResult {
			return ConnectedTaskResult{Notice: "First-launch checks passed.", Outcome: tui.OutcomeOK}
		},
	})
	defer app.Close()
	app.startConnection = func(tui.Sender, bootstrapPrepared, *tui.ConnectionController) {}
	_, _ = app.Update(bootstrapStepMsg{
		generation: app.bootstrap.operation,
		stage:      bootstrapReady,
		prepared:   app.bootstrap.prepared,
	})

	ready := tui.SessionReadyMsg{SessionID: "sess_test", Generation: 1, Call: &fakeRPC{}}
	_, cmd := app.Update(ready)
	if !app.connectedTaskStarted || cmd == nil {
		t.Fatal("first SessionReady did not schedule the connected task")
	}
	var done connectedTaskDoneMsg
	for _, batched := range cmd().(tea.BatchMsg) {
		if msg := batched(); msg != nil {
			if result, ok := msg.(connectedTaskDoneMsg); ok {
				done = result
			}
		}
	}
	if done.result.Notice == "" {
		t.Fatal("connected task did not return a structured result")
	}
	_, _ = app.Update(done)
	if !strings.Contains(app.View().Content, done.result.Notice) {
		t.Fatalf("Conversation omitted connected-task notice:\n%s", app.View().Content)
	}
	if transcript := app.conversation.View().Content; strings.Count(transcript, done.result.Notice) != 1 {
		t.Fatalf("notice count=%d, want one transient notice", strings.Count(transcript, done.result.Notice))
	}
}

func TestApplicationDropsConnectedTaskResultAfterSessionGenerationChanges(t *testing.T) {
	app := newTestApplication(t, Options{})
	defer app.Close()
	app.startConnection = func(tui.Sender, bootstrapPrepared, *tui.ConnectionController) {}
	_, _ = app.Update(bootstrapStepMsg{
		generation: app.bootstrap.operation,
		stage:      bootstrapReady,
		prepared:   app.bootstrap.prepared,
	})
	app.activeSessionID = "sess_new"
	app.activeSessionGeneration = 2
	done := connectedTaskDoneMsg{
		applicationGeneration: app.generation,
		sessionID:             "sess_old",
		sessionGeneration:     1,
		result:                ConnectedTaskResult{Notice: "stale connected task", Outcome: tui.OutcomeOK},
	}
	_, _ = app.Update(done)
	if strings.Contains(app.View().Content, done.result.Notice) {
		t.Fatal("late connected-task result leaked into the replacement session")
	}
}

func TestProgramSenderWaitsForSingleBindingWithoutDroppingMessage(t *testing.T) {
	sender := newProgramSender()
	target := &recordingSender{messages: make(chan tea.Msg, 1)}
	sent := make(chan struct{})
	go func() {
		sender.Send("ready")
		close(sent)
	}()

	select {
	case <-sent:
		t.Fatal("Send returned before the program was bound")
	case <-time.After(10 * time.Millisecond):
	}
	sender.Bind(target)
	select {
	case msg := <-target.messages:
		if msg != "ready" {
			t.Fatalf("message=%v, want ready", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("bound sender dropped the queued message")
	}
}

type recordingSender struct {
	messages chan tea.Msg
}

func (s *recordingSender) Send(msg tea.Msg) {
	s.messages <- msg
}

func newTestApplication(t *testing.T, opts Options) *applicationModel {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "workspace")
	cfg := config.Defaults(home)
	cfg.StateDir = filepath.Join(home, "state")
	app := newApplicationModel(opts, "en", false, newProgramSender())
	app.bootstrap.prepared = bootstrapPrepared{
		home: home, projectRoot: root, socket: filepath.Join(home, "daemon.sock"),
		config: cfg, locale: "en", sessionID: "sess_test",
	}
	return app
}
