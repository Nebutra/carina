package microcopy

import (
	"strconv"
	"strings"
)

// PluralCategory is the CLDR cardinal category used by the small count API.
// The current product locales require only one and other for integer counts.
type PluralCategory string

const (
	PluralOne   PluralCategory = "one"
	PluralOther PluralCategory = "other"
)

// PluralCategoryFor applies the CLDR integer cardinal rules used by Carina.
func PluralCategoryFor(locale string, count int) PluralCategory {
	switch NormalizeLocale(locale) {
	case "fr":
		if count == 0 || count == 1 {
			return PluralOne
		}
	case "en", "es":
		if count == 1 {
			return PluralOne
		}
	}
	return PluralOther
}

type pluralForms struct {
	one   string
	other string
}

var governedCountForms = map[Code]map[string]pluralForms{
	GovernedDestructiveConfirm: {
		"en": {one: "{count} file", other: "{count} files"},
		"zh": {other: "{count} 个文件"},
		"ja": {other: "{count} ファイル"},
		"ko": {other: "파일 {count}개"},
		"es": {one: "{count} archivo", other: "{count} archivos"},
		"fr": {one: "{count} fichier", other: "{count} fichiers"},
	},
}

var degradeCountForms = map[DegradeStatus]map[string]pluralForms{
	DegradeTimedOut: {
		"en": {one: "{count} second", other: "{count} seconds"},
		"zh": {other: "{count} 秒"},
		"ja": {other: "{count} 秒"},
		"ko": {other: "{count}초"},
		"es": {one: "{count} segundo", other: "{count} segundos"},
		"fr": {one: "{count} seconde", other: "{count} secondes"},
	},
}

// GovernedCount renders a governed template with a reviewed, locale-aware
// count label. Unsupported codes or negative counts return the safe fallback.
func GovernedCount(code Code, count int, args Args, opts ...Option) string {
	o := resolveOptions(opts)
	locale := NormalizeLocale(o.locale)
	forms, ok := governedCountForms[code][locale]
	if !ok || count < 0 {
		return governedFallback[locale]
	}
	args = cloneArgs(args)
	args["count_label"] = selectPluralForm(locale, count, forms)
	return Governed(code, args, opts...)
}

// DegradeCount renders a degrade template with a reviewed, locale-aware
// count label. Unsupported statuses or negative counts return the safe fallback.
func DegradeCount(status DegradeStatus, count int, args Args, opts ...Option) string {
	o := resolveOptions(opts)
	locale := NormalizeLocale(o.locale)
	forms, ok := degradeCountForms[status][locale]
	if !ok || count < 0 {
		line := degradeFallback[locale]
		if o.plain {
			return line
		}
		return "~ " + line
	}
	args = cloneArgs(args)
	args["duration"] = selectPluralForm(locale, count, forms)
	return Degrade(status, args, opts...)
}

func selectPluralForm(locale string, count int, forms pluralForms) string {
	form := forms.other
	if PluralCategoryFor(locale, count) == PluralOne && forms.one != "" {
		form = forms.one
	}
	return strings.ReplaceAll(form, "{count}", strconv.Itoa(count))
}

func cloneArgs(args Args) Args {
	out := make(Args, len(args)+1)
	for key, value := range args {
		out[key] = value
	}
	return out
}
