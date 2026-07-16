package microcopy

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
)

// Product locales. Runtime key "zh" is Simplified Chinese (zh-Hans/zh-CN);
// "zh-Hant" is Traditional Chinese (zh-Hant/zh-TW/zh-HK/zh-MO), derived from
// the Simplified catalogs via OpenCC-compatible conversion.
var supportedLocales = [...]string{"en", "zh", "zh-Hant", "ja", "ko", "es", "fr"}

// SupportedLocales returns the complete set of authored copy locales. The
// order is stable; callers receive a copy so package invariants cannot be
// mutated from outside.
func SupportedLocales() []string {
	return append([]string(nil), supportedLocales[:]...)
}

var supportedLocaleSet = map[string]struct{}{
	"en":      {},
	"zh":      {},
	"zh-Hant": {},
	"ja":      {},
	"ko":      {},
	"es":      {},
	"fr":      {},
}

var localeSubtag = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

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

// NormalizeLocale maps a BCP47-ish or POSIX locale value ("zh_CN.UTF-8",
// "es-419", "ja-JP-u-ca-japanese") to an authored runtime key. The zh key
// specifically means Simplified Chinese; zh-Hant is Traditional. Encoding and
// modifier suffixes are ignored; unsupported and empty values fall back to en.
// Exported so callers that branch on locale themselves (go/tui's degrade
// banner, for its reconnect-attempt suffix) apply the same normalization
// Governed/Degrade/Loading already do internally, instead of comparing a
// raw, unnormalized locale string like "zh-CN" against "zh" and missing it.
func NormalizeLocale(raw string) string {
	if locale, ok := canonicalLocale(raw); ok {
		return locale
	}
	return "en"
}

// CanonicalLocale validates an explicit user or configuration locale. Unlike
// NormalizeLocale, it does not silently turn an unsupported explicit choice
// into English.
func CanonicalLocale(raw string) (string, error) {
	if locale, ok := canonicalLocale(raw); ok {
		return locale, nil
	}
	return "", fmt.Errorf("unsupported locale; use en, zh, zh-CN, zh-Hans, zh-Hant, zh-TW, zh-HK, ja, ko, es, or fr")
}

func canonicalLocale(raw string) (string, bool) {
	l := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(l, ".@"); i >= 0 {
		l = l[:i]
	}
	l = strings.ReplaceAll(l, "_", "-")
	parts := strings.Split(l, "-")
	if len(parts) == 0 || parts[0] == "" {
		return "", false
	}
	for _, part := range parts {
		if !localeSubtag.MatchString(part) {
			return "", false
		}
	}
	base := parts[0]

	// Chinese script/region tags must be resolved before the base-only check.
	if base == "zh" {
		if len(parts) == 1 {
			return "zh", true // bare zh = Simplified product catalog
		}
		traditional, simplified := false, false
		for _, subtag := range parts[1:] {
			switch subtag {
			case "hant", "tw", "hk", "mo":
				traditional = true
			case "hans", "cn", "sg":
				simplified = true
			}
		}
		if traditional && !simplified {
			return "zh-Hant", true
		}
		if simplified && !traditional {
			return "zh", true
		}
		// Ambiguous multi-tags (should not happen in practice) → unsupported.
		if traditional && simplified {
			return "", false
		}
		// e.g. zh-Latn or other unknown extensions: unsupported for honesty.
		return "", false
	}

	if _, ok := supportedLocaleSet[base]; !ok {
		return "", false
	}
	return base, true
}

// pick is the deterministic selector: FNV-1a 32-bit over the seed, modulo
// the pool size — same seed, same line, snapshot-testable.
func pick(seed string, n int) int {
	h := fnv.New32a()
	h.Write([]byte(seed))
	return int(h.Sum32() % uint32(n))
}
