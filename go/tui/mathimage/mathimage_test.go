package mathimage

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderQueuesProtocolOutsidePlaceholderLines(t *testing.T) {
	resetForTest()
	t.Setenv("CARINA_MATH_GRAPHICS", "kitty")
	lines, ok := Render(`\frac{carina_7}{\sqrt{x}}`, 80, "  ")
	if !ok || len(lines) < 2 {
		t.Fatalf("pixel formula did not render: ok=%v rows=%d", ok, len(lines))
	}
	for _, line := range lines {
		if strings.Contains(line, "\x1b_G") {
			t.Fatal("graphics APC leaked into cell-buffer content")
		}
		if ansi.StringWidth(line) <= 2 || ansi.StringWidth(line) > 82 {
			t.Fatalf("placeholder width is outside its cell budget: %d", ansi.StringWidth(line))
		}
	}
	raw := Drain()
	for _, want := range []string{"\x1b_Ga=t,f=100", "\x1b_Ga=p,U=1", "\x1b\\"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("protocol output missing %q", want)
		}
	}
	if again := Drain(); again != "" {
		t.Fatalf("transmit was not one-shot: %d bytes", len(again))
	}
}

func TestUnsupportedTerminalFailsClosed(t *testing.T) {
	resetForTest()
	t.Setenv("CARINA_MATH_GRAPHICS", "off")
	if lines, ok := Render(`x^2`, 80, ""); ok || lines != nil {
		t.Fatalf("disabled graphics rendered: ok=%v lines=%q", ok, lines)
	}
}

func TestOversizedTexFailsClosedBeforeParsing(t *testing.T) {
	resetForTest()
	t.Setenv("CARINA_MATH_GRAPHICS", "kitty")
	if lines, ok := Render(strings.Repeat("x", maxTexBytes+1), 80, ""); ok || lines != nil {
		t.Fatalf("oversized formula rendered: ok=%v lines=%d", ok, len(lines))
	}
}

func TestImageCacheNeverEvictsOwnedImages(t *testing.T) {
	resetForTest()
	t.Setenv("CARINA_MATH_GRAPHICS", "kitty")
	encoded := testPNG(t)
	for i := 0; i < maxImages; i++ {
		if _, ok := RenderImageOwned(fmt.Sprintf("session:%d", i), fmt.Sprintf("image-%d", i), encoded, 12, ""); !ok {
			t.Fatalf("image %d failed before cache reached its budget", i)
		}
	}
	_ = Drain()
	if lines, ok := RenderImageOwned("session:over-budget", "over-budget", encoded, 12, ""); ok || lines != nil {
		t.Fatalf("over-budget image displaced an owned image: ok=%v lines=%d", ok, len(lines))
	}
	images.Lock()
	entries := len(images.m)
	controls := len(images.controls)
	images.Unlock()
	if entries != maxImages {
		t.Fatalf("cache entries=%d want=%d", entries, maxImages)
	}
	if controls != 0 {
		t.Fatalf("owned image eviction queued %d terminal deletes", controls)
	}
}

func TestResetTransportRetransmitsRenderedImage(t *testing.T) {
	resetForTest()
	t.Setenv("CARINA_MATH_GRAPHICS", "kitty")
	encoded := testPNG(t)
	if _, ok := RenderImageOwned("session:a", "same", encoded, 12, ""); !ok {
		t.Fatal("initial render failed")
	}
	first := Drain()
	if !strings.Contains(first, "a=t") {
		t.Fatalf("initial transport=%q", first)
	}
	ResetTransport()
	reset := Drain()
	deleteAt, transmitAt := strings.Index(reset, "a=d,d=I"), strings.Index(reset, "a=t")
	if deleteAt < 0 || transmitAt < 0 || deleteAt > transmitAt || !strings.Contains(reset, "a=p,U=1") {
		t.Fatalf("resize did not delete then retransmit/place image: %q", reset)
	}
}

func TestOwnerRebindEvictsOldWidthVariant(t *testing.T) {
	resetForTest()
	t.Setenv("CARINA_MATH_GRAPHICS", "kitty")
	encoded := testPNG(t)
	if _, ok := RenderImageOwned("transcript:image", "same", encoded, 12, ""); !ok {
		t.Fatal("initial render failed")
	}
	_ = Drain()
	if _, ok := RenderImageOwned("transcript:image", "same", encoded, 6, ""); !ok {
		t.Fatal("resized render failed")
	}
	images.Lock()
	entries := len(images.m)
	owners := len(images.ownerKey)
	images.Unlock()
	if entries != 1 || owners != 1 {
		t.Fatalf("owner rebind retained stale variants: entries=%d owners=%d", entries, owners)
	}
	if raw := Drain(); !strings.Contains(raw, "a=d,d=I") || !strings.Contains(raw, "a=t") {
		t.Fatalf("owner rebind did not delete old backing and transmit new one: %q", raw)
	}
}

func TestCellSizeChangeInvalidatesCachedGeometry(t *testing.T) {
	resetForTest()
	t.Setenv("CARINA_MATH_GRAPHICS", "kitty")
	encoded := testPNG(t)
	if _, ok := RenderImageOwned("transcript:image", "same", encoded, 12, ""); !ok {
		t.Fatal("initial render failed")
	}
	_ = Drain()
	if !SetCellSize(6, 12) {
		t.Fatal("cell size change was ignored")
	}
	images.Lock()
	entries := len(images.m)
	images.Unlock()
	if entries != 0 {
		t.Fatalf("cell size change retained %d stale cache entries", entries)
	}
	if _, ok := RenderImageOwned("transcript:image", "same", encoded, 12, ""); !ok {
		t.Fatal("render after cell size change failed")
	}
	if raw := Drain(); !strings.Contains(raw, "a=d,d=I") || !strings.Contains(raw, "a=t") {
		t.Fatalf("cell size change did not replace terminal backing: %q", raw)
	}
}

func TestReleaseOwnerDeletesOnlyAfterLastOwner(t *testing.T) {
	resetForTest()
	t.Setenv("CARINA_MATH_GRAPHICS", "kitty")
	encoded := testPNG(t)
	RenderImageOwned("session:a", "shared", encoded, 12, "")
	RenderImageOwned("composer:a", "shared", encoded, 12, "")
	_ = Drain()
	ReleaseOwner("session:a")
	if raw := Drain(); raw != "" {
		t.Fatalf("first owner release deleted shared image: %q", raw)
	}
	ReleaseOwner("composer:a")
	if raw := Drain(); !strings.Contains(raw, "a=d,d=I") {
		t.Fatalf("last owner release did not delete image: %q", raw)
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 18, 18))
	img.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
