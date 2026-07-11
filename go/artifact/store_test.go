package artifact

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRunPeriodicGCStopsWithContext(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.RunPeriodicGC(ctx, 5*time.Millisecond, time.Now) }()
	deadline := time.Now().Add(time.Second)
	for s.Metrics().GCRuns < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunPeriodicGC error = %v", err)
	}
	if s.Metrics().GCRuns < 2 {
		t.Fatalf("GC runs = %d, want at least 2", s.Metrics().GCRuns)
	}
}

func TestRunPeriodicGCRecoversAfterTransientError(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(s.root, "refs", "bad.json")
	if err = os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.RunPeriodicGC(ctx, 2*time.Millisecond, time.Now) }()
	deadline := time.Now().Add(time.Second)
	for s.Metrics().GCErrors < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.Health().OK {
		t.Fatal("health remained OK after GC error")
	}
	if err = os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	for (s.Metrics().LastGC == nil || s.Metrics().LastGC.Error != "") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.Health().OK {
		t.Fatalf("health did not recover: %+v", s.Health())
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestStorePutReadAndScopeEnforcement(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{SessionID: "session-1", TaskID: "task-1", CallID: "call-1"}
	want := []byte("first line\nsecond line\nthird")
	meta, err := store.Put(want, PutOptions{Scope: scope, MediaType: "text/plain", PreviewBytes: 100, PreviewLines: 2, Now: time.Unix(123, 0)})
	if err != nil {
		t.Fatal(err)
	}
	// PreviewLines: 2 with 3 lines of content forces a head+tail split: the
	// preview keeps the first line and the last line with an elision marker
	// between them, instead of a head-only cut that would drop "third".
	if len(meta.ID) != 64 || meta.Preview != "first line\n\n… [12 bytes omitted] …\nthird" || !meta.Truncated {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	got, stat, err := store.Read(scope, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) || stat.ID != meta.ID {
		t.Fatalf("read mismatch: %q %+v", got, stat)
	}
	_, _, err = store.Read(Scope{SessionID: "session-2", TaskID: "task-1", CallID: "call-1"}, meta.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-session read error = %v", err)
	}
	encoded := strings.Join([]string{meta.ID, meta.MediaType, meta.Scope.SessionID}, " ")
	if strings.Contains(encoded, store.root) {
		t.Fatal("public metadata exposed local path")
	}
}

func TestDeleteSessionRefsPreservesSharedObject(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("shared")
	a, err := s.Put(raw, PutOptions{Scope: Scope{SessionID: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Put(raw, PutOptions{Scope: Scope{SessionID: "s2"}}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.DeleteSessionRefs("s1")
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if _, _, err = s.Read(Scope{SessionID: "s1"}, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("s1 read err=%v", err)
	}
	if got, _, err := s.Read(Scope{SessionID: "s2"}, a.ID); err != nil || string(got) != "shared" {
		t.Fatalf("shared object lost: %q %v", got, err)
	}
	gc, err := s.GC(time.Now())
	if err != nil || gc.ObjectsRemoved != 0 {
		t.Fatalf("gc=%+v err=%v", gc, err)
	}
}

func TestUsageMetricsAndExpiredGC(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err = s.Put([]byte("abc"), PutOptions{Scope: Scope{SessionID: "s"}, Now: now, TTL: time.Second}); err != nil {
		t.Fatal(err)
	}
	u, err := s.Usage()
	if err != nil || u.PhysicalBytes != 3 || u.ReferenceCount != 1 || u.LogicalReferenceBytes != 3 {
		t.Fatalf("usage=%+v err=%v", u, err)
	}
	g, err := s.GC(now.Add(2 * time.Second))
	if err != nil || g.ReferencesRemoved != 1 || g.ObjectsRemoved != 1 || g.BytesReclaimed != 3 {
		t.Fatalf("gc=%+v err=%v", g, err)
	}
	m := s.Metrics()
	if m.Puts != 1 || m.GCRuns != 1 || m.LastGC == nil {
		t.Fatalf("metrics=%+v", m)
	}
}

func TestStoreDeduplicatesContentAcrossScopedReferences(t *testing.T) {
	store, _ := New(t.TempDir())
	one, err := store.Put([]byte("same"), PutOptions{Scope: Scope{SessionID: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := store.Put([]byte("same"), PutOptions{Scope: Scope{SessionID: "s2"}})
	if err != nil {
		t.Fatal(err)
	}
	if one.ID != two.ID {
		t.Fatalf("ids differ: %s %s", one.ID, two.ID)
	}
	entries := 0
	_ = filepathWalk(store.root+"/objects", func() { entries++ })
	if entries != 1 {
		t.Fatalf("object files = %d, want 1", entries)
	}
}

func TestPreviewIsUTF8SafeAndBinaryIsNotLeaked(t *testing.T) {
	store, _ := New(t.TempDir())
	meta, err := store.Put([]byte("ab世界cd"), PutOptions{Scope: Scope{SessionID: "s"}, PreviewBytes: 5})
	if err != nil {
		t.Fatal(err)
	}
	// Five bytes cannot hold both content ends plus the elision marker, so the
	// preview falls back to the longest UTF-8-safe head within the hard budget.
	if meta.Preview != "ab世" || len(meta.Preview) > 5 || !utf8.ValidString(meta.Preview) || !meta.Truncated || !meta.PreviewUTF8 {
		t.Fatalf("bad preview: %+v", meta)
	}
	binary, err := store.Put([]byte{0xff, 0x00, 0x01}, PutOptions{Scope: Scope{SessionID: "s"}, PreviewBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if binary.Preview != "" || binary.PreviewUTF8 {
		t.Fatalf("binary preview leaked: %+v", binary)
	}
}

func TestStoreRejectsInvalidScopeAndDetectsTampering(t *testing.T) {
	store, _ := New(t.TempDir())
	if _, err := store.Put([]byte("x"), PutOptions{Scope: Scope{SessionID: "../escape"}}); err == nil {
		t.Fatal("expected invalid scope error")
	}
	scope := Scope{SessionID: "s"}
	meta, err := store.Put([]byte("original"), PutOptions{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.objectPath(meta.ID), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Read(scope, meta.ID); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("error = %v", err)
	}
}

func filepathWalk(root string, found func()) error {
	return filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found()
		}
		return err
	})
}

func TestObjectSessionAndStoreQuotas(t *testing.T) {
	objectStore, err := New(t.TempDir(), Config{MaxObjectBytes: 4, MaxSessionBytes: 8, MaxStoreBytes: 12})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := objectStore.Put([]byte("12345"), PutOptions{Scope: Scope{SessionID: "s"}}); !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("object limit error = %v", err)
	}

	sessionStore, _ := New(t.TempDir(), Config{MaxObjectBytes: 6, MaxSessionBytes: 8, MaxStoreBytes: 20})
	if _, err := sessionStore.Put([]byte("123456"), PutOptions{Scope: Scope{SessionID: "s"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionStore.Put([]byte("abc"), PutOptions{Scope: Scope{SessionID: "s"}}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("session quota error = %v", err)
	}
	if _, err := sessionStore.Put([]byte("123456"), PutOptions{Scope: Scope{SessionID: "s", TaskID: "other"}}); err != nil {
		t.Fatalf("same content should not consume session quota twice: %v", err)
	}

	globalStore, _ := New(t.TempDir(), Config{MaxObjectBytes: 6, MaxSessionBytes: 8, MaxStoreBytes: 8})
	if _, err := globalStore.Put([]byte("123456"), PutOptions{Scope: Scope{SessionID: "s1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := globalStore.Put([]byte("abc"), PutOptions{Scope: Scope{SessionID: "s2"}}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("store quota error = %v", err)
	}
}

func TestPutReaderRejectsOverflowBeforeWrite(t *testing.T) {
	store, _ := New(t.TempDir(), Config{MaxObjectBytes: 4, MaxSessionBytes: 8, MaxStoreBytes: 8})
	if _, err := PutReader(store, bytes.NewBufferString("12345"), PutOptions{Scope: Scope{SessionID: "s"}}); !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("reader overflow error = %v", err)
	}
	entries := 0
	_ = filepathWalk(filepath.Join(store.root, "objects"), func() { entries++ })
	if entries != 0 {
		t.Fatalf("overflow wrote %d objects", entries)
	}
}

func TestTTLAndGC(t *testing.T) {
	store, _ := New(t.TempDir())
	base := time.Now().UTC()
	expiredScope := Scope{SessionID: "expired"}
	liveScope := Scope{SessionID: "live"}
	expired, err := store.Put([]byte("shared"), PutOptions{Scope: expiredScope, Now: base, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put([]byte("shared"), PutOptions{Scope: liveScope, Now: base, TTL: 2 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	result, err := store.GC(base.Add(90 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if result.ReferencesRemoved != 1 || result.ObjectsRemoved != 0 {
		t.Fatalf("first gc = %+v", result)
	}
	if _, _, err := store.Read(expiredScope, expired.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired read error = %v", err)
	}
	if _, _, err := store.Read(liveScope, expired.ID); err != nil {
		t.Fatalf("live shared read: %v", err)
	}
	result, err = store.GC(base.Add(3 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if result.ReferencesRemoved != 1 || result.ObjectsRemoved != 1 || result.BytesReclaimed != int64(len("shared")) {
		t.Fatalf("second gc = %+v", result)
	}
}

func TestConcurrentQuotaSafety(t *testing.T) {
	store, _ := New(t.TempDir(), Config{MaxObjectBytes: 8, MaxSessionBytes: 8, MaxStoreBytes: 8})
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i, raw := range [][]byte{[]byte("123456"), []byte("abcdef")} {
		go func(i int, raw []byte) {
			<-start
			_, err := store.Put(raw, PutOptions{Scope: Scope{SessionID: fmt.Sprintf("s%d", i)}})
			errs <- err
		}(i, raw)
	}
	close(start)
	succeeded, rejected := 0, 0
	for range 2 {
		if err := <-errs; err == nil {
			succeeded++
		} else if errors.Is(err, ErrQuotaExceeded) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func TestRetentionTiersAreBounded(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	store, err := New(t.TempDir(), Config{EphemeralTTL: time.Hour, NormalTTL: 24 * time.Hour, PinnedTTL: 7 * 24 * time.Hour, MaxPinnedTTL: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		tier RetentionTier
		want time.Duration
	}{
		{"ephemeral", RetentionEphemeral, time.Hour},
		{"normal", RetentionNormal, 24 * time.Hour},
		{"pinned", RetentionPinned, 7 * 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := store.Put([]byte(tc.name), PutOptions{Scope: Scope{SessionID: "s_" + tc.name}, Now: now, Retention: tc.tier})
			if err != nil {
				t.Fatal(err)
			}
			if meta.ExpiresAt == nil || !meta.ExpiresAt.Equal(now.Add(tc.want)) {
				t.Fatalf("expires_at = %v", meta.ExpiresAt)
			}
			if meta.Retention != tc.tier {
				t.Fatalf("retention = %q", meta.Retention)
			}
		})
	}
	if _, err := store.Put([]byte("x"), PutOptions{Scope: Scope{SessionID: "s_bad"}, Retention: RetentionPinned, TTL: 31 * 24 * time.Hour}); err == nil {
		t.Fatal("unbounded pinned retention succeeded")
	}
	if _, err := store.Put([]byte("x"), PutOptions{Scope: Scope{SessionID: "s_invalid"}, Retention: "forever"}); err == nil {
		t.Fatal("invalid retention tier succeeded")
	}
}

func TestRetentionConfigurationRequiresMonotonicTTLs(t *testing.T) {
	for _, cfg := range []Config{
		{EphemeralTTL: 48 * time.Hour, NormalTTL: 24 * time.Hour},
		{NormalTTL: 8 * 24 * time.Hour, PinnedTTL: 7 * 24 * time.Hour},
		{PinnedTTL: 31 * 24 * time.Hour, MaxPinnedTTL: 30 * 24 * time.Hour},
	} {
		if _, err := New(t.TempDir(), cfg); err == nil {
			t.Fatalf("New accepted non-monotonic retention config: %+v", cfg)
		}
	}
}

func TestMetricsUseLowCardinalityByteCounters(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.Put([]byte("hello"), PutOptions{Scope: Scope{SessionID: "s"}, Retention: RetentionNormal})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Read(meta.Scope, meta.ID); err != nil {
		t.Fatal(err)
	}
	m := store.Metrics()
	if m.Puts != 1 || m.Reads != 1 || m.PutBytes != 5 || m.ReadBytes != 5 {
		t.Fatalf("metrics = %+v", m)
	}
	u, err := store.Usage()
	if err != nil {
		t.Fatal(err)
	}
	if u.ReferencesByRetention[RetentionNormal] != 1 {
		t.Fatalf("retention usage = %+v", u.ReferencesByRetention)
	}
}

// TestPreviewPreservesTailSignal is the mid_truncation regression case: a
// command output whose actionable signal (the final "build failed" line) is
// far past a head-only cutoff must still surface in the preview. A head-only
// makePreview would show 500+ lines of noise and silently drop the outcome.
func TestPreviewPreservesTailSignal(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	for i := 0; i < 500; i++ {
		raw = append(raw, fmt.Appendf(nil, "line %d: compiling...\n", i)...)
	}
	raw = append(raw, []byte("FINAL: build failed with 3 errors\n")...)
	meta, err := store.Put(raw, PutOptions{Scope: Scope{SessionID: "s"}, PreviewBytes: 400, PreviewLines: 40})
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Truncated {
		t.Fatalf("expected truncation for %d-byte input", len(raw))
	}
	if !strings.HasPrefix(meta.Preview, "line 0: compiling...\n") {
		t.Fatalf("preview lost the head: %q", meta.Preview)
	}
	if !strings.HasSuffix(meta.Preview, "FINAL: build failed with 3 errors\n") {
		t.Fatalf("preview dropped the tail signal: %q", meta.Preview)
	}
	if !strings.Contains(meta.Preview, "omitted") {
		t.Fatalf("preview does not disclose the elided middle: %q", meta.Preview)
	}
}

// TestPreviewSkipsSplitWhenContentFits ensures small content that already
// fits the budget is returned verbatim, with no marker and Truncated=false —
// the head+tail split only activates when a cut is actually needed.
func TestPreviewSkipsSplitWhenContentFits(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.Put([]byte("ok\n"), PutOptions{Scope: Scope{SessionID: "s"}, PreviewBytes: 4096, PreviewLines: 200})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Truncated || meta.Preview != "ok\n" || strings.Contains(meta.Preview, "omitted") {
		t.Fatalf("unexpected split on content that fits budget: %+v", meta)
	}
}

// TestPreviewFallsBackToHeadOnlyUnderTinyBudget covers the degenerate case
// where PreviewLines: 1 leaves the tail half of the line-split with a zero
// line budget (head takes the only line slot) — the preview must still be
// valid (head-only) rather than emit a bare marker with nothing after it.
func TestPreviewFallsBackToHeadOnlyUnderTinyBudget(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.Put([]byte("abcdefghij\nmore\n"), PutOptions{Scope: Scope{SessionID: "s"}, PreviewLines: 1})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Preview != "abcdefghij\n" || !meta.Truncated || strings.Contains(meta.Preview, "omitted") {
		t.Fatalf("expected head-only fallback under PreviewLines: 1: %+v", meta)
	}
}

func TestPreviewByteBudgetIncludesElisionMarker(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.Put([]byte("abcdefghijklmnopqrstuvwxyz"), PutOptions{
		Scope: Scope{SessionID: "s"}, PreviewBytes: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Preview) > 24 {
		t.Fatalf("preview exceeds byte budget: got %d bytes: %q", len(meta.Preview), meta.Preview)
	}
	if !meta.Truncated {
		t.Fatal("expected truncated preview")
	}
}

// TestPreviewLineSplitBiasesHeadOnOddBudget locks in the odd-maxLines split
// rule (head gets the ceil half) so the allocation between head/tail stays
// deterministic across refactors.
func TestPreviewLineSplitBiasesHeadOnOddBudget(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("l1\nl2\nl3\nl4\nl5\n")
	meta, err := store.Put(raw, PutOptions{Scope: Scope{SessionID: "s"}, PreviewLines: 3, PreviewBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	// maxLines=3 -> head gets ceil(3/2)=2 lines ("l1\nl2\n"), tail gets 1
	// ("l5\n"); the 6 bytes of "l3\nl4\n" in between are omitted.
	want := "l1\nl2\n\n… [6 bytes omitted] …\nl5\n"
	if meta.Preview != want || !meta.Truncated {
		t.Fatalf("preview = %q, want %q (truncated=%v)", meta.Preview, want, meta.Truncated)
	}
}
