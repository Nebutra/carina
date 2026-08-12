package daemon

import (
	"strings"
	"testing"
)

func TestPresentDoneSummaryConvertsReportEnvelopeToMarkdown(t *testing.T) {
	raw := `{"summary":"Pebble is a local-first IDE.","architecture":["Desktop: Tauri","Runtime: Go"],"commands":{"development":"pnpm dev","test":"pnpm test"},"verification":"No files changed."}`
	got := presentDoneSummary(raw)
	for _, want := range []string{
		"Pebble is a local-first IDE.",
		"**Architecture**\n- Desktop: Tauri\n- Runtime: Go",
		"**Commands**",
		"- **Development:** pnpm dev",
		"**Verification**\nNo files changed.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("presentation missing %q:\n%s", want, got)
		}
	}
	if strings.HasPrefix(got, "{") || strings.Contains(got, `"architecture"`) {
		t.Fatalf("report envelope leaked as JSON:\n%s", got)
	}
}

func TestPresentDoneSummaryPreservesProseAndGenericJSON(t *testing.T) {
	for _, input := range []string{
		"Done. No files changed.",
		`{"answer":42}`,
		`["one","two"]`,
	} {
		if got := presentDoneSummary(input); got != input {
			t.Fatalf("presentation changed %q to %q", input, got)
		}
	}
}
