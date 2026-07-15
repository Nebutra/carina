package microcopy

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// The brand linter. It runs at test time over the embedded pools (a
// rule-breaking line cannot land) and is reused by the future offline
// `carina microcopy refresh` validator, so LLM-widened candidates face the
// same rules as authored copy.

// Astronomy/cosmos vocabulary is banned in every register: the nebula is a
// visual identity, never a verbal one (brand brief §1, §6).
var astronomyEN = regexp.MustCompile(`(?i)\b(nebulae?|cosmos|cosmic|galax(?:y|ies)|galactic|stars?|starlight|stellar|constellations?|orbits?|orbital|universe|supernovae?|astral|celestial|interstellar|comets?|meteors?|planets?|planetary|lunar|solar|eclipse|asteroids?)\b`)

var astronomyZH = []string{
	"星云", "星辰", "星空", "星系", "星际", "星球", "宇宙", "银河",
	"恒星", "彗星", "流星", "超新星", "天体", "太空", "航天", "轨道",
}

var astronomyTerms = map[string][]string{
	"ja": {"星雲", "宇宙", "銀河", "星空", "恒星", "惑星", "軌道", "天体", "流星", "彗星"},
	"ko": {"성운", "우주", "은하", "별빛", "별자리", "행성", "궤도", "천체", "유성", "혜성"},
	"es": {"nebulosa", "cosmos", "cósmic", "galax", "estelar", "constelaci", "órbita", "universo", "supernova", "astral", "celestial", "planeta", "lunar", "solar", "eclipse", "asteroide", "cometa", "meteor"},
	"fr": {"nébuleuse", "cosmos", "cosmique", "galax", "stellaire", "constellation", "orbite", "univers", "supernova", "astral", "céleste", "planète", "lunaire", "solaire", "éclipse", "astéroïde", "comète", "météor"},
}

// Metaphor terms are banned in the Governed and Degrade registers: sober
// lines use exact nouns — the field register's ledgers and kettles stay in
// the Ambient pools.
var metaphorEN = regexp.MustCompile(`(?i)\b(ledgers?|kettles?|drama|magic|journeys?|whimsy|budd(?:y|ies)|vibes?|sparkles?)\b`)

var metaphorZH = []string{"账本", "东风", "魔法", "戏法"}

var metaphorTerms = map[string][]string{
	"ja": {"魔法", "気分で", "お友達", "キラキラ"},
	"ko": {"마법", "분위기로", "친구처럼", "반짝"},
	"es": {"magia", "mágic", "aventura", "colega", "vibras", "chispa"},
	"fr": {"magie", "magique", "aventure", "copain", "ambiance", "paillettes"},
}

// LintAmbientLine checks one Ambient line against the field-register rules:
// no exclamation marks, no emoji, no astronomy/cosmos vocabulary.
func LintAmbientLine(locale, line string) []string {
	var v []string
	v = append(v, lintExclamation(line)...)
	v = append(v, lintEmoji(line)...)
	v = append(v, lintAstronomy(locale, line)...)
	return v
}

// LintGovernedTemplate checks one Governed template against the sober-register
// rules: every declared placeholder present, full sentences, no metaphor, no
// exclamation, no emoji, no astronomy vocabulary, no undeclared placeholders.
func LintGovernedTemplate(locale, tmpl string, placeholders []string) []string {
	var v []string
	v = append(v, lintPlaceholders(tmpl, placeholders)...)
	v = append(v, lintFullSentence(locale, tmpl)...)
	v = append(v, lintMetaphor(locale, tmpl)...)
	v = append(v, lintExclamation(tmpl)...)
	v = append(v, lintEmoji(tmpl)...)
	v = append(v, lintAstronomy(locale, tmpl)...)
	return v
}

// LintDegradeTemplate checks one Degrade template: calm-factual + remedy —
// placeholders present, no metaphor, no exclamation, no emoji, no astronomy
// vocabulary. Degrade lines are not held to the full-sentence rule
// ("Fix: carina doctor" is a remedy, not a sentence).
func LintDegradeTemplate(locale, tmpl string, placeholders []string) []string {
	var v []string
	v = append(v, lintPlaceholders(tmpl, placeholders)...)
	v = append(v, lintMetaphor(locale, tmpl)...)
	v = append(v, lintExclamation(tmpl)...)
	v = append(v, lintEmoji(tmpl)...)
	v = append(v, lintAstronomy(locale, tmpl)...)
	return v
}

// LintPools lints every embedded pool and returns all violations, each
// prefixed with its register, locale and key. Tests assert it is empty.
func LintPools() []string {
	var out []string
	for locale, contexts := range registry.ambient.Locales {
		for context, lines := range contexts {
			for _, line := range lines {
				for _, v := range LintAmbientLine(locale, line) {
					out = append(out, fmt.Sprintf("ambient/%s/%s %q: %s", locale, context, line, v))
				}
			}
		}
	}
	for _, ov := range registry.ambient.Overrides {
		for locale, lines := range ov.Locales {
			for _, line := range lines {
				for _, v := range LintAmbientLine(locale, line) {
					out = append(out, fmt.Sprintf("ambient/%s/override(%s) %q: %s", locale, ov.Pattern, line, v))
				}
			}
		}
	}
	for key, tmpl := range registry.governed {
		for locale, body := range tmpl.Text {
			for _, v := range LintGovernedTemplate(locale, body, tmpl.Placeholders) {
				out = append(out, fmt.Sprintf("governed/%s/%s %q: %s", locale, key, body, v))
			}
		}
	}
	for key, tmpl := range registry.degrade {
		for locale, body := range tmpl.Text {
			for _, v := range LintDegradeTemplate(locale, body, tmpl.Placeholders) {
				out = append(out, fmt.Sprintf("degrade/%s/%s %q: %s", locale, key, body, v))
			}
		}
	}
	for locale, body := range governedFallback {
		for _, v := range LintGovernedTemplate(locale, body, nil) {
			out = append(out, fmt.Sprintf("governed/%s/fallback %q: %s", locale, body, v))
		}
	}
	for locale, body := range degradeFallback {
		for _, v := range LintDegradeTemplate(locale, body, nil) {
			out = append(out, fmt.Sprintf("degrade/%s/fallback %q: %s", locale, body, v))
		}
	}
	return out
}

var placeholderShape = regexp.MustCompile(`\{[a-z][a-z0-9_]*\}`)

func lintPlaceholders(tmpl string, placeholders []string) []string {
	var v []string
	declared := map[string]bool{}
	for _, ph := range placeholders {
		declared[ph] = true
		if !strings.Contains(tmpl, "{"+ph+"}") {
			v = append(v, fmt.Sprintf("missing declared placeholder {%s}", ph))
		}
	}
	for _, m := range placeholderShape.FindAllString(tmpl, -1) {
		name := strings.Trim(m, "{}")
		if !declared[name] {
			v = append(v, fmt.Sprintf("undeclared placeholder {%s}", name))
		}
	}
	return v
}

func lintFullSentence(locale, tmpl string) []string {
	runes := []rune(tmpl)
	if len(runes) == 0 {
		return []string{"not a full sentence: empty template"}
	}
	last := runes[len(runes)-1]
	switch locale {
	case "zh", "ja":
		if last != '。' && last != '？' {
			return []string{"not a full sentence: must end with 。 or ？"}
		}
	case "ko":
		if last != '.' && last != '?' {
			return []string{"not a full sentence: must end with . or ?"}
		}
	default:
		firstLetter := rune(0)
		for _, r := range runes {
			if unicode.IsLetter(r) {
				firstLetter = r
				break
			}
		}
		if firstLetter == 0 || !unicode.IsUpper(firstLetter) {
			return []string{"not a full sentence: must start with an uppercase letter"}
		}
		if last != '.' && last != '?' {
			return []string{"not a full sentence: must end with . or ?"}
		}
	}
	return nil
}

func lintMetaphor(locale, s string) []string {
	var v []string
	if locale == "zh" {
		for _, term := range metaphorZH {
			if strings.Contains(s, term) {
				v = append(v, fmt.Sprintf("metaphor term %q", term))
			}
		}
		return v
	}
	if terms, ok := metaphorTerms[locale]; ok {
		lower := strings.ToLower(s)
		for _, term := range terms {
			if strings.Contains(lower, term) {
				v = append(v, fmt.Sprintf("metaphor term %q", term))
			}
		}
		return v
	}
	if m := metaphorEN.FindString(s); m != "" {
		v = append(v, fmt.Sprintf("metaphor term %q", m))
	}
	return v
}

func lintExclamation(s string) []string {
	if strings.ContainsAny(s, "!！") {
		return []string{"exclamation mark"}
	}
	return nil
}

func lintAstronomy(locale, s string) []string {
	var v []string
	if locale == "zh" {
		for _, term := range astronomyZH {
			if strings.Contains(s, term) {
				v = append(v, fmt.Sprintf("astronomy/cosmos term %q", term))
			}
		}
		return v
	}
	if terms, ok := astronomyTerms[locale]; ok {
		lower := strings.ToLower(s)
		for _, term := range terms {
			if strings.Contains(lower, term) {
				v = append(v, fmt.Sprintf("astronomy/cosmos term %q", term))
			}
		}
		return v
	}
	if m := astronomyEN.FindString(s); m != "" {
		v = append(v, fmt.Sprintf("astronomy/cosmos term %q", m))
	}
	return v
}

// lintEmoji flags emoji and pictographic symbols; brand copy is plain text
// in every register.
func lintEmoji(s string) []string {
	for _, r := range s {
		if (r >= 0x1F000 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF) ||
			(r >= 0x2B00 && r <= 0x2BFF) || r == 0xFE0F {
			return []string{fmt.Sprintf("emoji %q", r)}
		}
	}
	return nil
}
