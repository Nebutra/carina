package contextengine

import (
	"context"
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
	if doc := m.Doctor(); doc["ok"] != true {
		t.Fatalf("doctor should pass: %+v", doc)
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
	if res.Content != "hello" || res.OriginalBytes != 5 || res.CompressedBytes != 5 || res.Ratio != 1 || res.Engine != ModeNoop {
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
