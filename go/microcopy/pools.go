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
// template body, declared placeholder names, and machine-auditable Governed
// facts/terminology metadata.
type template struct {
	Placeholders []string          `json:"placeholders"`
	Facts        []string          `json:"facts"`
	Terms        []string          `json:"terms"`
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

var governedTermVocabulary = map[string]struct{}{
	"action": {}, "agent": {}, "approval": {}, "audit_chain": {},
	"audit_record": {}, "checkpoint": {}, "command": {}, "decision": {},
	"external_endpoint": {}, "operator": {}, "policy": {}, "restore_point": {},
	"rollback": {}, "scope": {}, "secret": {}, "transaction": {}, "transcript": {},
}

var metadataID = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

var registry = loadRegistry()

var registeredRegistries = map[int]*poolRegistry{
	registry.version: registry,
}

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
	governedVersion, governed := decodeTemplates("pools/governed.json")
	degradeVersion, degrade := decodeTemplates("pools/degrade.json")
	if err := validatePoolVersions(r.ambient.Version, governedVersion, degradeVersion); err != nil {
		panic(err)
	}
	r.version = r.ambient.Version
	r.governed = governed
	r.degrade = degrade
	r.validateLocaleParity()
	return r
}

func validatePoolVersions(ambient, governed, degrade int) error {
	if ambient <= 0 || ambient != governed || ambient != degrade {
		return fmt.Errorf("microcopy: pool version mismatch: ambient=%d governed=%d degrade=%d", ambient, governed, degrade)
	}
	return nil
}

// ResolvePoolVersion reports the effective registered version. Unknown
// requests explicitly fall back to the embedded default and return exact=false.
func ResolvePoolVersion(requested int) (effective int, exact bool) {
	if _, ok := registeredRegistries[requested]; ok {
		return requested, true
	}
	return registry.version, false
}

func registryForVersion(requested int) *poolRegistry {
	effective, _ := ResolvePoolVersion(requested)
	return registeredRegistries[effective]
}

func (r *poolRegistry) validateLocaleParity() {
	baseContexts := r.ambient.Locales["en"]
	for _, locale := range supportedLocales {
		contexts := r.ambient.Locales[locale]
		for context := range baseContexts {
			if len(contexts[context]) == 0 {
				panic(fmt.Sprintf("microcopy: ambient locale %s missing context %s", locale, context))
			}
		}
		for _, ov := range r.ambient.Overrides {
			if len(ov.Locales[locale]) == 0 {
				panic(fmt.Sprintf("microcopy: ambient locale %s missing override %s", locale, ov.Pattern))
			}
		}
	}
	for register, templates := range map[string]map[string]*template{
		"governed": r.governed,
		"degrade":  r.degrade,
	} {
		for key, tmpl := range templates {
			if register == "governed" {
				if err := validateGovernedMetadata(key, tmpl); err != nil {
					panic(err)
				}
			}
			for _, locale := range supportedLocales {
				if strings.TrimSpace(tmpl.Text[locale]) == "" {
					panic(fmt.Sprintf("microcopy: %s %s missing locale %s", register, key, locale))
				}
			}
		}
	}
}

func validateGovernedMetadata(key string, tmpl *template) error {
	if len(tmpl.Facts) == 0 || len(tmpl.Terms) == 0 {
		return fmt.Errorf("microcopy: governed %s requires facts and terms metadata", key)
	}
	seen := map[string]bool{}
	for _, fact := range tmpl.Facts {
		if !metadataID.MatchString(fact) || seen["fact:"+fact] {
			return fmt.Errorf("microcopy: governed %s invalid or duplicate fact %q", key, fact)
		}
		seen["fact:"+fact] = true
	}
	for _, term := range tmpl.Terms {
		if _, ok := governedTermVocabulary[term]; !ok || seen["term:"+term] {
			return fmt.Errorf("microcopy: governed %s invalid or duplicate term %q", key, term)
		}
		seen["term:"+term] = true
	}
	return nil
}

// templateFile is the on-disk shape of governed.json / degrade.json: every
// non-placeholders key of an entry is a locale.
type templateFile struct {
	Version   int                                   `json:"version"`
	Templates map[string]map[string]json.RawMessage `json:"templates"`
}

func decodeTemplates(path string) (int, map[string]*template) {
	var f templateFile
	mustDecode(path, &f)
	out := make(map[string]*template, len(f.Templates))
	for key, entry := range f.Templates {
		t := &template{Text: map[string]string{}}
		for field, raw := range entry {
			switch field {
			case "placeholders":
				if err := json.Unmarshal(raw, &t.Placeholders); err != nil {
					panic(fmt.Sprintf("microcopy: %s %s placeholders: %v", path, key, err))
				}
				continue
			case "facts":
				if err := json.Unmarshal(raw, &t.Facts); err != nil {
					panic(fmt.Sprintf("microcopy: %s %s facts: %v", path, key, err))
				}
				continue
			case "terms":
				if err := json.Unmarshal(raw, &t.Terms); err != nil {
					panic(fmt.Sprintf("microcopy: %s %s terms: %v", path, key, err))
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
	return f.Version, out
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
