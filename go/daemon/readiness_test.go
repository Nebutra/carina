package daemon

import (
	"encoding/json"
	"testing"

	"github.com/Nebutra/carina/go/provider"
)

func TestModelListReadinessIsDaemonOwnedAndDynamic(t *testing.T) {
	isolateLocalGrokBuild(t)
	t.Setenv("OPENAI_API_KEY", "runtime-key")
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	d.router.RegisterProvider(inventoryProvider("openai"))
	d.reasoner = modelInventoryTestReasoner{name: reasonerBackendRouter}
	d.reasonerBackend = reasonerBackendRouter
	session, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}

	initial, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	if count := d.journey.snapshot()["time_to_ready"].(map[string]any)["count"]; count != 0 {
		t.Fatalf("incomplete inventory request recorded ready metric: %v", count)
	}
	modelID := initial.(map[string]any)["default_model"].(string)
	params, _ := json.Marshal(modelInventoryParams{
		SessionID: session.SessionID,
		ModelID:   modelID,
		Locale:    "zh",
	})
	result, err := d.handleModelList(params)
	if err != nil {
		t.Fatal(err)
	}
	ready := result.(map[string]any)["readiness"].(modelInventoryReadiness)
	if !ready.CanSubmit || ready.Step != "conversation" || ready.Locale != "zh" || ready.Generation == 0 {
		t.Fatalf("ready snapshot = %+v", ready)
	}
	if count := d.journey.snapshot()["time_to_ready"].(map[string]any)["count"]; count != 1 {
		t.Fatalf("authoritative ready snapshot metric count = %v", count)
	}

	t.Setenv("OPENAI_API_KEY", "")
	result, err = d.handleModelList(params)
	if err != nil {
		t.Fatal(err)
	}
	blocked := result.(map[string]any)["readiness"].(modelInventoryReadiness)
	if blocked.CanSubmit || blocked.Step != "provider" || !containsReadinessBlocker(blocked.Blockers, "provider_unavailable") {
		t.Fatalf("revoked snapshot = %+v", blocked)
	}

	t.Setenv("OPENAI_API_KEY", "restored-key")
	result, err = d.handleModelList(params)
	if err != nil {
		t.Fatal(err)
	}
	restored := result.(map[string]any)["readiness"].(modelInventoryReadiness)
	if !restored.CanSubmit || restored.Generation <= blocked.Generation {
		t.Fatalf("same-process import snapshot = %+v", restored)
	}
}

func TestModelListReadinessRejectsUnsupportedLocaleAndUnknownModel(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "runtime-key")
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	d.router.RegisterProvider(inventoryProvider("openai"))
	d.reasoner = modelInventoryTestReasoner{name: reasonerBackendRouter}
	d.reasonerBackend = reasonerBackendRouter
	session, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(modelInventoryParams{
		SessionID: session.SessionID,
		ModelID:   "openai/not-a-model",
		Locale:    "xx-invalid",
	})
	result, err := d.handleModelList(params)
	if err != nil {
		t.Fatal(err)
	}
	blocked := result.(map[string]any)["readiness"].(modelInventoryReadiness)
	if blocked.CanSubmit || blocked.Step != "locale" {
		t.Fatalf("blocked snapshot = %+v", blocked)
	}
	for _, blocker := range []string{"locale_unsupported", "model_unavailable"} {
		if !containsReadinessBlocker(blocked.Blockers, blocker) {
			t.Fatalf("snapshot missing %s: %+v", blocker, blocked)
		}
	}
}

func TestInventoryRouteKindUsesExecutionBackendsAndProviderFacts(t *testing.T) {
	tests := []struct {
		name     string
		provider modelInventoryProvider
		reasoner modelInventoryReasoner
		want     string
	}{
		{name: "codex cli", reasoner: modelInventoryReasoner{Backend: reasonerBackendCodexCLI}, want: "cli_oauth"},
		{name: "claude cli", reasoner: modelInventoryReasoner{Backend: reasonerBackendClaudeCLI}, want: "cli_oauth"},
		{name: "managed proxy", provider: modelInventoryProvider{SourceRoute: provider.CCSwitchRouteManagedProxy}, want: "live_proxy"},
		{name: "credential", provider: modelInventoryProvider{AuthSource: "env:OPENAI_API_KEY"}, want: "credential_source"},
		{name: "catalog", want: "upstream_record"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := inventoryRouteKind(test.provider, test.reasoner); got != test.want {
				t.Fatalf("route kind = %q, want %q", got, test.want)
			}
		})
	}
}

func containsReadinessBlocker(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
