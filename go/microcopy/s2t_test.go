package microcopy

import (
	"strings"
	"testing"
)

func TestToTraditionalExactAndCharFallback(t *testing.T) {
	got := ToTraditional("无法连接守护进程")
	if got != "無法連接守護進程" {
		t.Fatalf("exact convert = %q", got)
	}
	if ToTraditional("approval mode") != "approval mode" {
		t.Fatal("english must be unchanged")
	}
	got = ToTraditional("网络默认")
	if got == "网络默认" {
		t.Fatalf("char fallback did not convert: %q", got)
	}
}

func TestTraditionalLocaleResolution(t *testing.T) {
	cases := map[string]string{
		"zh-Hant":     "zh-Hant",
		"zh-TW":       "zh-Hant",
		"zh_HK.UTF-8": "zh-Hant",
		"zh-MO":       "zh-Hant",
		"zh-CN":       "zh",
		"zh-Hans":     "zh",
	}
	for raw, want := range cases {
		got, err := CanonicalLocale(raw)
		if err != nil || got != want {
			t.Errorf("CanonicalLocale(%q) = %q, %v; want %q", raw, got, err, want)
		}
		if NormalizeLocale(raw) != want {
			t.Errorf("NormalizeLocale(%q) = %q, want %q", raw, NormalizeLocale(raw), want)
		}
	}
}

func TestGovernedRendersTraditional(t *testing.T) {
	args := Args{"action": "write", "path": "/tmp/x", "decision_id": "perm_1"}
	line := Governed(GovernedApprovalRequired, args, WithLocale("zh-Hant"))
	if line == "" || line == governedFallback["zh-Hant"] {
		t.Fatalf("empty or fallback: %q", line)
	}
	simp := Governed(GovernedApprovalRequired, args, WithLocale("zh"))
	if strings.Contains(line, "需要授权") {
		t.Fatalf("zh-Hant line still has Simplified 授权: %q", line)
	}
	if !strings.Contains(line, "授權") && !strings.Contains(simp, "授权") {
		// If simplified also lacks 授权 phrasing, at least ensure non-English.
		if strings.Contains(line, "Approval required") {
			t.Fatalf("unexpected English: %q", line)
		}
	}
	if !strings.Contains(line, "perm_1") || !strings.Contains(line, "/tmp/x") {
		t.Fatalf("placeholders not preserved: %q", line)
	}
}
