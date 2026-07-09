package microcopy

import (
	"hash/fnv"
	"regexp"
	"strings"
)

// contextPatterns route a seed (an RPC method or tool name as it appears on
// the wire) to a Carina-native Ambient context pool. Order matters: the
// first match wins, so "kernel.patch.apply" is a patch moment before it is a
// kernel one. Unmatched seeds fall to the generic pool. The approval context
// deliberately owns no Ambient pool — permission moments speak Governed.
var contextPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"approval", regexp.MustCompile(`(?i)approve|deny|decision|permission`)},
	{"patch", regexp.MustCompile(`(?i)patch|diff|rollback|apply`)},
	{"codeintel", regexp.MustCompile(`(?i)code\.(search|symbols|map|def|refs|impact)|index`)},
	{"audit", regexp.MustCompile(`(?i)audit|chain|verify|replay|export`)},
	{"session", regexp.MustCompile(`(?i)session|resume|fork`)},
	{"policy", regexp.MustCompile(`(?i)policy|rule`)},
	{"kernel", regexp.MustCompile(`(?i)kernel`)},
	{"provider", regexp.MustCompile(`(?i)provider|model|router|auth`)},
	{"git", regexp.MustCompile(`(?i)\bgit\b|commit|branch`)},
	{"file", regexp.MustCompile(`(?i)file|fs\.|workspace\.(read|write)`)},
	{"terminal", regexp.MustCompile(`(?i)command|exec|shell|terminal`)},
	{"agent", regexp.MustCompile(`(?i)agent|task|run`)},
}

func resolveContext(seed string) string {
	for _, cp := range contextPatterns {
		if cp.re.MatchString(seed) {
			return cp.name
		}
	}
	return "generic"
}

// NormalizeLocale maps a raw locale value ("zh_CN.UTF-8", "en-US") to a
// supported pool locale by prefix; everything unsupported falls back to en.
// Exported so callers that branch on locale themselves (go/tui's degrade
// banner, for its reconnect-attempt suffix) apply the same normalization
// Governed/Degrade/Loading already do internally, instead of comparing a
// raw, unnormalized locale string like "zh-CN" against "zh" and missing it.
func NormalizeLocale(raw string) string {
	l := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(l, "zh"):
		return "zh"
	default:
		return "en"
	}
}

// pick is the deterministic selector: FNV-1a 32-bit over the seed, modulo
// the pool size — same seed, same line, snapshot-testable.
func pick(seed string, n int) int {
	h := fnv.New32a()
	h.Write([]byte(seed))
	return int(h.Sum32() % uint32(n))
}
