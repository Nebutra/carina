package microcopy

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// The embedded default pools. A future, governed `carina microcopy refresh`
// may append a user overlay (~/.carina/microcopy/pools.v{N}.json); this
// landing ships the deterministic embedded core only.
//
//go:embed pools/*.json
var poolFS embed.FS

// template is a Governed or Degrade register entry: locale → constant
// template body, plus the declared placeholder names the linter enforces.
type template struct {
	Placeholders []string          `json:"placeholders"`
	Text         map[string]string `json:"-"`
	res          []*regexp.Regexp  // anchored per-locale shapes, for Is()
}

func (t *template) text(locale string) string {
	if s := t.Text[locale]; s != "" {
		return s
	}
	return t.Text["en"]
}

// matches reports whether value is a rendering of this template in any
// locale (placeholders matched as non-empty spans).
func (t *template) matches(value string) bool {
	for _, re := range t.res {
		if re.MatchString(value) {
			return true
		}
	}
	return false
}

type ambientOverride struct {
	Pattern string              `json:"pattern"`
	Locales map[string][]string `json:"locales"`
	re      *regexp.Regexp
}

type ambientSet struct {
	Version   int                            `json:"version"`
	Overrides []ambientOverride              `json:"overrides"`
	Locales   map[string]map[string][]string `json:"locales"` // locale → context → lines
}

type poolRegistry struct {
	version      int
	ambient      ambientSet
	governed     map[string]*template
	degrade      map[string]*template
	ambientIndex map[string]bool
}

var registry = loadRegistry()

func loadRegistry() *poolRegistry {
	r := &poolRegistry{ambientIndex: map[string]bool{}}

	mustDecode("pools/ambient.json", &r.ambient)
	for i := range r.ambient.Overrides {
		r.ambient.Overrides[i].re = regexp.MustCompile(r.ambient.Overrides[i].Pattern)
		for _, lines := range r.ambient.Overrides[i].Locales {
			for _, line := range lines {
				r.ambientIndex[line] = true
			}
		}
	}
	for _, contexts := range r.ambient.Locales {
		for _, lines := range contexts {
			for _, line := range lines {
				r.ambientIndex[line] = true
			}
		}
	}
	r.version = r.ambient.Version

	r.governed = decodeTemplates("pools/governed.json")
	r.degrade = decodeTemplates("pools/degrade.json")
	return r
}

// templateFile is the on-disk shape of governed.json / degrade.json: every
// non-placeholders key of an entry is a locale.
type templateFile struct {
	Version   int                                   `json:"version"`
	Templates map[string]map[string]json.RawMessage `json:"templates"`
}

func decodeTemplates(path string) map[string]*template {
	var f templateFile
	mustDecode(path, &f)
	out := make(map[string]*template, len(f.Templates))
	for key, entry := range f.Templates {
		t := &template{Text: map[string]string{}}
		for field, raw := range entry {
			if field == "placeholders" {
				if err := json.Unmarshal(raw, &t.Placeholders); err != nil {
					panic(fmt.Sprintf("microcopy: %s %s placeholders: %v", path, key, err))
				}
				continue
			}
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				panic(fmt.Sprintf("microcopy: %s %s locale %s: %v", path, key, field, err))
			}
			t.Text[field] = s
		}
		for _, body := range t.Text {
			t.res = append(t.res, templateShape(body, t.Placeholders))
		}
		out[key] = t
	}
	return out
}

// templateShape compiles an anchored regexp matching any rendering of the
// template body, with each declared placeholder matched as a non-empty span.
func templateShape(body string, placeholders []string) *regexp.Regexp {
	quoted := regexp.QuoteMeta(body)
	for _, ph := range placeholders {
		quoted = strings.ReplaceAll(quoted, regexp.QuoteMeta("{"+ph+"}"), `.+?`)
	}
	return regexp.MustCompile(`^` + quoted + `$`)
}

// matchesTemplate reports whether line is a rendering of any template in the
// given register pool — the cross-register segregation check used in tests.
func matchesTemplate(line string, tmpls map[string]*template) bool {
	for _, t := range tmpls {
		if t.matches(line) {
			return true
		}
	}
	return false
}

func mustDecode(path string, v any) {
	data, err := poolFS.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("microcopy: embedded pool %s: %v", path, err))
	}
	if err := json.Unmarshal(data, v); err != nil {
		panic(fmt.Sprintf("microcopy: embedded pool %s: %v", path, err))
	}
}

// ambientPool returns the Ambient lines for a locale and context, falling
// back to the locale's generic pool when the context has none.
func (r *poolRegistry) ambientPool(locale, context string) []string {
	contexts := r.Locale(locale)
	if lines := contexts[context]; len(lines) > 0 {
		return lines
	}
	return contexts["generic"]
}

// Locale returns the context map for a pool locale, falling back to en.
func (r *poolRegistry) Locale(locale string) map[string][]string {
	if contexts, ok := r.ambient.Locales[locale]; ok {
		return contexts
	}
	return r.ambient.Locales["en"]
}

func (r *poolRegistry) isAmbientLine(s string) bool { return r.ambientIndex[s] }

// ambientCount totals a locale's authored Ambient lines (context pools plus
// overrides). en and zh are authored independently — the counts are asserted
// separately in tests and are deliberately not equal.
func (r *poolRegistry) ambientCount(locale string) int {
	n := 0
	for _, lines := range r.ambient.Locales[locale] {
		n += len(lines)
	}
	for _, ov := range r.ambient.Overrides {
		n += len(ov.Locales[locale])
	}
	return n
}
