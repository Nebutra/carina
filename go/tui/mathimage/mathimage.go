// Package mathimage renders display TeX as terminal-native pixel graphics.
package mathimage

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"strings"
	"sync"

	"github.com/luo-studio/go-tex/tex/layout"
	"github.com/luo-studio/go-tex/tex/parser"
	"github.com/luo-studio/go-tex/tex/render"
	_ "golang.org/x/image/webp"
)

const (
	placeholder  = '\U0010eeee'
	cellWidthPx  = 9
	cellHeightPx = 18
	maxImages    = 96
	maxTexBytes  = 16 << 10
	maxImageSide = 16384
	maxPixels    = 32 << 20
)

// These are the row/column marks assigned by the Kitty graphics protocol.
// Explicit coordinates make placeholders stable under viewport clipping.
var diacritics = []rune{
	0x305, 0x30d, 0x30e, 0x310, 0x312, 0x33d, 0x33e, 0x33f, 0x346, 0x34a, 0x34b, 0x34c, 0x350, 0x351, 0x352, 0x357,
	0x35b, 0x363, 0x364, 0x365, 0x366, 0x367, 0x368, 0x369, 0x36a, 0x36b, 0x36c, 0x36d, 0x36e, 0x36f, 0x483, 0x484,
	0x485, 0x486, 0x487, 0x592, 0x593, 0x594, 0x595, 0x597, 0x598, 0x599, 0x59c, 0x59d, 0x59e, 0x59f, 0x5a0, 0x5a1,
	0x5a8, 0x5a9, 0x5ab, 0x5ac, 0x5af, 0x5c4, 0x610, 0x611, 0x612, 0x613, 0x614, 0x615, 0x616, 0x617, 0x657, 0x658,
	0x659, 0x65a, 0x65b, 0x65d, 0x65e, 0x6d6, 0x6d7, 0x6d8, 0x6d9, 0x6da, 0x6db, 0x6dc, 0x6df, 0x6e0, 0x6e1, 0x6e2,
	0x6e4, 0x6e7, 0x6e8, 0x6eb, 0x6ec, 0x730, 0x732, 0x733, 0x735, 0x736, 0x73a, 0x73d, 0x73f, 0x740, 0x741, 0x743,
	0x745, 0x747, 0x749, 0x74a, 0x7eb, 0x7ec, 0x7ed, 0x7ee, 0x7ef, 0x7f0, 0x7f1, 0x7f3, 0x816, 0x817, 0x818, 0x819,
	0x81b, 0x81c, 0x81d, 0x81e, 0x81f, 0x820, 0x821, 0x822, 0x823, 0x825, 0x826, 0x827, 0x829, 0x82a, 0x82b, 0x82c,
	0x82d, 0x951, 0x953, 0x954, 0xf82, 0xf83, 0xf86, 0xf87, 0x135d, 0x135e, 0x135f, 0x17dd, 0x193a, 0x1a17, 0x1a75,
	0x1a76, 0x1a77, 0x1a78, 0x1a79, 0x1a7a, 0x1a7b, 0x1a7c, 0x1b6b, 0x1b6d, 0x1b6e, 0x1b6f, 0x1b70, 0x1b71, 0x1b72,
	0x1b73, 0x1cd0, 0x1cd1, 0x1cd2, 0x1cda, 0x1cdb, 0x1ce0, 0x1dc0, 0x1dc1, 0x1dc3, 0x1dc4, 0x1dc5, 0x1dc6, 0x1dc7,
	0x1dc8, 0x1dc9, 0x1dcb, 0x1dcc, 0x1dd1, 0x1dd2, 0x1dd3, 0x1dd4, 0x1dd5, 0x1dd6, 0x1dd7, 0x1dd8, 0x1dd9, 0x1dda,
	0x1ddb, 0x1ddc, 0x1ddd, 0x1dde, 0x1ddf, 0x1de0, 0x1de1, 0x1de2, 0x1de3, 0x1de4, 0x1de5, 0x1de6, 0x1dfe, 0x20d0,
	0x20d1, 0x20d4, 0x20d5, 0x20d6, 0x20d7, 0x20db, 0x20dc, 0x20e1, 0x20e7, 0x20e9, 0x20f0, 0x2cef, 0x2cf0, 0x2cf1,
	0x2de0, 0x2de1, 0x2de2, 0x2de3, 0x2de4, 0x2de5, 0x2de6, 0x2de7, 0x2de8, 0x2de9, 0x2dea, 0x2deb, 0x2dec, 0x2ded,
	0x2dee, 0x2def, 0x2df0, 0x2df1, 0x2df2, 0x2df3, 0x2df4, 0x2df5, 0x2df6, 0x2df7, 0x2df8, 0x2df9, 0x2dfa, 0x2dfb,
	0x2dfc, 0x2dfd, 0x2dfe, 0x2dff, 0xa66f, 0xa67c, 0xa67d, 0xa6f0, 0xa6f1, 0xa8e0, 0xa8e1, 0xa8e2, 0xa8e3, 0xa8e4,
	0xa8e5, 0xa8e6, 0xa8e7, 0xa8e8, 0xa8e9, 0xa8ea, 0xa8eb, 0xa8ec, 0xa8ed, 0xa8ee, 0xa8ef, 0xa8f0, 0xa8f1, 0xaab0,
	0xaab2, 0xaab3, 0xaab7, 0xaab8, 0xaabe, 0xaabf, 0xaac1, 0xfe20, 0xfe21, 0xfe22, 0xfe23, 0xfe24, 0xfe25, 0xfe26,
}

type cached struct {
	png         []byte
	widthCells  int
	heightCells int
	id          uint32
}

var images = struct {
	sync.Mutex
	m       map[string]cached
	loaded  map[uint32]bool
	pending map[uint32]string
}{m: make(map[string]cached), loaded: make(map[uint32]bool), pending: make(map[uint32]string)}

// Supported reports whether Kitty Unicode placeholders are safe in this terminal.
func Supported() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("CARINA_MATH_GRAPHICS")))
	if v == "0" || v == "false" || v == "off" {
		return false
	}
	if v == "1" || v == "true" || v == "kitty" {
		return true
	}
	info, err := os.Stdout.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if os.Getenv("TMUX") != "" {
		return false
	}
	p := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	return strings.Contains(p, "ghostty") || strings.Contains(p, "kitty")
}

// Render returns a Kitty virtual placement followed by ordinary placeholder rows.
func Render(tex string, maxWidthCells int, indent string) ([]string, bool) {
	if !Supported() || maxWidthCells < 1 || strings.TrimSpace(tex) == "" || len(tex) > maxTexBytes {
		return nil, false
	}
	c, err := raster(strings.TrimSpace(tex), maxWidthCells)
	if err != nil || c.widthCells > len(diacritics) || c.heightCells > len(diacritics) {
		return nil, false
	}
	queueGraphics(c)
	fg := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c.id>>16&255, c.id>>8&255, c.id&255)
	ul := fmt.Sprintf("\x1b[58:2::%d:%d:%dm", c.id>>16&255, c.id>>8&255, c.id&255)
	lines := make([]string, c.heightCells)
	for row := range c.heightCells {
		var b strings.Builder
		b.WriteString(indent)
		b.WriteString(fg)
		b.WriteString(ul)
		for col := range c.widthCells {
			b.WriteRune(placeholder)
			b.WriteRune(diacritics[row])
			b.WriteRune(diacritics[col])
		}
		b.WriteString("\x1b[39;59m")
		lines[row] = b.String()
	}
	return lines, true
}

// RenderImage renders an encoded PNG, JPEG, GIF, or WebP as a terminal-native
// image. The caller-supplied key is normally the content hash; bytes are
// decoded and re-encoded as PNG because Kitty's f=100 transport is explicit.
func RenderImage(key string, encoded []byte, maxWidthCells int, indent string) ([]string, bool) {
	if !Supported() || maxWidthCells < 1 || key == "" || len(encoded) == 0 {
		return nil, false
	}
	c, err := rasterImage(key, encoded, maxWidthCells)
	if err != nil || c.widthCells > len(diacritics) || c.heightCells > len(diacritics) {
		return nil, false
	}
	return placeholders(c, indent), true
}

// Drain returns terminal protocol bytes queued by Render. It separates raw
// graphics I/O from the Bubble Tea cell buffer, which intentionally accepts
// only SGR and hyperlinks in View content.
func Drain() string {
	images.Lock()
	defer images.Unlock()
	if len(images.pending) == 0 {
		return ""
	}
	var b strings.Builder
	for id, sequence := range images.pending {
		b.WriteString(sequence)
		images.loaded[id] = true
		delete(images.pending, id)
	}
	return b.String()
}

func queueGraphics(c cached) {
	images.Lock()
	defer images.Unlock()
	if images.loaded[c.id] || images.pending[c.id] != "" {
		return
	}
	place := fmt.Sprintf("\x1b_Ga=p,U=1,q=2,i=%d,p=%d,c=%d,r=%d\x1b\\", c.id, c.id, c.widthCells, c.heightCells)
	images.pending[c.id] = kittyTransmit(c.png, c.id) + place
}

func placeholders(c cached, indent string) []string {
	queueGraphics(c)
	fg := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c.id>>16&255, c.id>>8&255, c.id&255)
	ul := fmt.Sprintf("\x1b[58:2::%d:%d:%dm", c.id>>16&255, c.id>>8&255, c.id&255)
	lines := make([]string, c.heightCells)
	for row := range c.heightCells {
		var b strings.Builder
		b.WriteString(indent)
		b.WriteString(fg)
		b.WriteString(ul)
		for col := range c.widthCells {
			b.WriteRune(placeholder)
			b.WriteRune(diacritics[row])
			b.WriteRune(diacritics[col])
		}
		b.WriteString("\x1b[39;59m")
		lines[row] = b.String()
	}
	return lines
}

func rasterImage(key string, encoded []byte, maxWidthCells int) (cached, error) {
	cacheKey := fmt.Sprintf("image:%d:%s", maxWidthCells, key)
	images.Lock()
	if c, ok := images.m[cacheKey]; ok {
		images.Unlock()
		return c, nil
	}
	images.Unlock()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(encoded))
	if err != nil {
		return cached{}, err
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxImageSide || cfg.Height > maxImageSide || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return cached{}, errors.New("mathimage: decoded image dimensions exceed preview limit")
	}
	decoded, _, err := image.Decode(bytes.NewReader(encoded))
	if err != nil {
		return cached{}, err
	}
	var out bytes.Buffer
	if err := png.Encode(&out, decoded); err != nil {
		return cached{}, err
	}
	bounds := decoded.Bounds()
	w := max(1, (bounds.Dx()+cellWidthPx-1)/cellWidthPx)
	h := max(1, (bounds.Dy()+cellHeightPx-1)/cellHeightPx)
	if w > maxWidthCells {
		h = max(1, (h*maxWidthCells+w-1)/w)
		w = maxWidthCells
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(cacheKey))
	id := hash.Sum32() & 0xffffff
	if id == 0 {
		id = 1
	}
	c := cached{png: out.Bytes(), widthCells: w, heightCells: h, id: id}
	images.Lock()
	defer images.Unlock()
	if len(images.m) >= maxImages {
		return cached{}, errors.New("mathimage: live image budget exhausted")
	}
	images.m[cacheKey] = c
	return c, nil
}

func raster(tex string, maxWidthCells int) (cached, error) {
	key := fmt.Sprintf("%d:%s", maxWidthCells, tex)
	images.Lock()
	if c, ok := images.m[key]; ok {
		images.Unlock()
		return c, nil
	}
	images.Unlock()
	nodes, err := parser.Parse(tex)
	if err != nil {
		return cached{}, err
	}
	opts := layout.DefaultOptions().WithColor(layout.Color{R: 0xc6, G: 0xa6, B: 0xea, A: 0xff})
	dl := layout.ToDisplayList(layout.Layout(nodes, opts))
	b, err := render.PNG(dl, render.WithFontSize(25), render.WithPadding(3), render.WithStrokeWidth(1.1))
	if err != nil {
		return cached{}, err
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return cached{}, err
	}
	w := max(1, (cfg.Width+cellWidthPx-1)/cellWidthPx)
	h := max(1, (cfg.Height+cellHeightPx-1)/cellHeightPx)
	if w > maxWidthCells {
		h = max(1, (h*maxWidthCells+w-1)/w)
		w = maxWidthCells
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(key))
	id := hash.Sum32() & 0xffffff
	if id == 0 {
		id = 1
	}
	c := cached{png: b, widthCells: w, heightCells: h, id: id}
	images.Lock()
	if len(images.m) >= maxImages {
		images.Unlock()
		return cached{}, errors.New("mathimage: live image budget exhausted")
	}
	images.m[key] = c
	images.Unlock()
	return c, nil
}

func kittyTransmit(pngBytes []byte, id uint32) string {
	data := base64.StdEncoding.EncodeToString(pngBytes)
	const chunk = 4096
	var b strings.Builder
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		more := 0
		if end < len(data) {
			more = 1
		}
		if off == 0 {
			fmt.Fprintf(&b, "\x1b_Ga=t,f=100,q=2,i=%d,m=%d;%s\x1b\\", id, more, data[off:end])
		} else {
			fmt.Fprintf(&b, "\x1b_Gq=2,m=%d;%s\x1b\\", more, data[off:end])
		}
	}
	return b.String()
}
