package daemon

import (
	"testing"

	"github.com/Nebutra/carina/go/provider"
)

func TestClaudeCodeOwnedCCSwitchCredentialNeverUsesBearerSnapshots(t *testing.T) {
	const providerID = "ccswitch-claude-test"
	store := testAuthStore(t)
	if err := store.SetBearerToken(providerID, "stale-import", map[string]string{
		"source":          provider.CCSwitchSourceKind,
		"source_revision": "revision-1",
		"validation":      providerValidationContract,
	}); err != nil {
		t.Fatal(err)
	}

	info := provider.Info{
		ID: providerID,
		Source: &provider.Source{
			Kind:            provider.CCSwitchSourceKind,
			Action:          provider.CCSwitchActionImportSaved,
			Revision:        "revision-1",
			Importable:      true,
			CredentialOwner: provider.CCSwitchCredentialOwnerClaudeCode,
		},
	}
	chain := runtimeProviderAuthChain(info, store)
	if got := chain.Sources(); len(got) != 0 {
		t.Fatalf("sources = %v", got)
	}
	if credential, ok := chain.Resolve(); ok || credential.Value != "" {
		t.Fatalf("missing live credential fell back to stale store: %#v, ok=%v", credential, ok)
	}
}

func TestClaudeCodeOwnedCCSwitchReadinessRequiresCLI(t *testing.T) {
	previous := claudeCLIAvailable
	t.Cleanup(func() { claudeCLIAvailable = previous })

	info := provider.Info{
		ID:          "ccswitch-claude-test",
		API:         "https://api.anthropic.com/v1",
		APIProtocol: "anthropic",
		Source: &provider.Source{
			Kind:            provider.CCSwitchSourceKind,
			Action:          provider.CCSwitchActionImportSaved,
			Importable:      true,
			CredentialOwner: provider.CCSwitchCredentialOwnerClaudeCode,
		},
	}
	catalog := provider.Catalog{info.ID: info}

	claudeCLIAvailable = func() bool { return true }
	if !hasRunnableRuntimeProvider(catalog, nil, nil) {
		t.Fatal("Claude Code-owned route was not ready with the CLI available")
	}
	claudeCLIAvailable = func() bool { return false }
	if hasRunnableRuntimeProvider(catalog, nil, nil) {
		t.Fatal("Claude Code-owned route was ready without the CLI")
	}

	row := modelInventoryProvider{SourceCredentialOwner: provider.CCSwitchCredentialOwnerClaudeCode}
	if got := inventoryRouteKind(row, modelInventoryReasoner{Backend: reasonerBackendRouter}); got != "cli_oauth" {
		t.Fatalf("route kind = %q", got)
	}
}

func TestCCSwitchProfileCredentialKeepsValidatedStorePriority(t *testing.T) {
	const providerID = "ccswitch-claude-relay"
	store := testAuthStore(t)
	if err := store.SetBearerToken(providerID, "validated-store", map[string]string{
		"source":          provider.CCSwitchSourceKind,
		"source_revision": "revision-1",
		"validation":      providerValidationContract,
	}); err != nil {
		t.Fatal(err)
	}

	previous := lookupCCSwitchCredential
	lookupCCSwitchCredential = func(string) (provider.CCSwitchProfile, string, bool) {
		return provider.CCSwitchProfile{Revision: "revision-1"}, "profile-token", true
	}
	t.Cleanup(func() { lookupCCSwitchCredential = previous })

	info := provider.Info{
		ID: providerID,
		Source: &provider.Source{
			Kind:            provider.CCSwitchSourceKind,
			Action:          provider.CCSwitchActionImportSaved,
			Revision:        "revision-1",
			Importable:      true,
			CredentialOwner: provider.CCSwitchCredentialOwnerProfile,
		},
	}
	credential, ok := runtimeProviderAuthChain(info, store).Resolve()
	if !ok || credential.Value != "validated-store" || credential.Source != "auth:"+providerID {
		t.Fatalf("credential = %#v, ok=%v", credential, ok)
	}
}
