package contextengine

import (
	"context"
	"strings"
	"testing"
)

func TestNormalizeConfigDefaultsToAuto(t *testing.T) {
	cfg, err := NormalizeConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContextEngine != ModeAuto {
		t.Fatalf("context engine default = %q", cfg.ContextEngine)
	}
}

func TestNormalizeConfigAcceptsOwnedModes(t *testing.T) {
	for _, mode := range []string{ModeAuto, ModeOff, ModeNoop, " NOOP "} {
		cfg, err := NormalizeConfig(Config{ContextEngine: mode})
		if err != nil {
			t.Fatalf("NormalizeConfig(%q): %v", mode, err)
		}
		if cfg.ContextEngine == "" {
			t.Fatalf("NormalizeConfig(%q) returned an empty mode", mode)
		}
	}
}

func TestNormalizeConfigRejectsExternalMode(t *testing.T) {
	if _, err := NormalizeConfig(Config{ContextEngine: "external"}); err == nil {
		t.Fatal("external context engine should fail")
	}
}

func TestAutoIsDeterministicLocalNoop(t *testing.T) {
	m, err := New(Config{ContextEngine: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if st.ConfiguredEngine != ModeAuto || st.EffectiveEngine != ModeNoop || st.Phase != PhaseReady {
		t.Fatalf("unexpected status: %+v", st)
	}
	doc := m.Doctor()
	if doc["ok"] != true || doc["engine"] != ModeNoop || doc["transformed"] != false {
		t.Fatalf("doctor should pass as noop: %+v", doc)
	}
	reason, _ := doc["reason"].(string)
	if !strings.Contains(reason, "no bytes were transformed") {
		t.Fatalf("doctor reason missing identity sentence: %q", reason)
	}
}

func TestCompressIsLocalNoopAndCountsCalls(t *testing.T) {
	m, err := New(Config{ContextEngine: ModeNoop})
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Compress(context.Background(), CompressRequest{Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hello" || res.OriginalBytes != 5 || res.CompressedBytes != 5 || res.Ratio != 1 || res.Engine != ModeNoop || res.Transformed || res.Reason != NoopIdentityReason {
		t.Fatalf("unexpected compression result: %+v", res)
	}
	stats, err := m.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.CompressionCalls != 1 || stats.Engine != ModeNoop || stats.Phase != PhaseReady {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestAutoCompressIsIdentity(t *testing.T) {
	m, err := New(Config{ContextEngine: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	const payload = "verbatim payload"
	res, err := m.Compress(context.Background(), CompressRequest{Content: payload})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != payload || res.Transformed || res.Ratio != 1 || res.Engine != ModeNoop {
		t.Fatalf("auto compress must be identity noop: %+v", res)
	}
	if !strings.Contains(res.Reason, "no bytes were transformed") {
		t.Fatalf("auto compress missing identity reason: %q", res.Reason)
	}
}
