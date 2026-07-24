package tui

import (
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
	"github.com/Nebutra/carina/go/tui/ui"
)

func TestNewCheckedUsesInjectedRuntime(t *testing.T) {
	runtime := ui.NewRuntime()
	m, err := NewChecked(Options{Theme: theme.New(theme.Mono), Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.componentRuntime != runtime {
		t.Fatal("NewChecked replaced the application-owned runtime")
	}
}

func TestNewCheckedDefaultsToIsolatedRuntime(t *testing.T) {
	first, err := NewChecked(Options{Theme: theme.New(theme.Mono)})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewChecked(Options{Theme: theme.New(theme.Mono)})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.componentRuntime == nil || second.componentRuntime == nil {
		t.Fatal("standalone model has no component runtime")
	}
	if first.componentRuntime == second.componentRuntime {
		t.Fatal("standalone models unexpectedly share a component runtime")
	}
}

func TestOperationalNoticeMsgStaysOutOfTranscript(t *testing.T) {
	m, err := NewChecked(Options{Theme: theme.New(theme.Mono)})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	before := len(m.tr.entries)
	_, _ = m.Update(OperationalNoticeMsg{
		Kind: "startup", Text: "first-launch checks found warnings", Outcome: OutcomeDegradedPartial,
	})
	if len(m.tr.entries) != before {
		t.Fatal("operational notice was appended to the durable transcript")
	}
	if m.operationalNotice.Kind != "startup" || m.operationalNotice.Text == "" || m.operationalNotice.Role != theme.RoleWarning {
		t.Fatalf("notice not projected as transient state: %+v", m.operationalNotice)
	}
}
