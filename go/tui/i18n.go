package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/Nebutra/carina/go/microcopy"
)

// Locale is the normalized UI locale. Protocol values, command names and
// persisted status values deliberately remain locale-independent.
type Locale string

const (
	LocaleEnglish  Locale = "en"
	LocaleChinese  Locale = "zh"
	LocaleJapanese Locale = "ja"
	LocaleKorean   Locale = "ko"
	LocaleSpanish  Locale = "es"
	LocaleFrench   Locale = "fr"
)

var supportedLocales = []Locale{
	LocaleEnglish, LocaleChinese, LocaleJapanese,
	LocaleKorean, LocaleSpanish, LocaleFrench,
}

var unavailableMessage = map[Locale]string{
	LocaleEnglish:  "Carina message unavailable",
	LocaleChinese:  "Carina 消息暂不可用",
	LocaleJapanese: "Carina のメッセージを表示できません",
	LocaleKorean:   "Carina 메시지를 표시할 수 없습니다",
	LocaleSpanish:  "Mensaje de Carina no disponible",
	LocaleFrench:   "Message Carina indisponible",
}

// MessageID is intentionally closed over the constants in i18n_catalog.go.
// Call sites cannot accidentally use a translated sentence as an identifier.
type MessageID string

type MessageArgs map[string]any

type messageTemplate struct {
	One   string
	Other string
}

type catalogRow struct {
	ID                                       MessageID
	EN, ZH, JA, KO, ES, FR                   string
	ENOne, ZHOne, JAOne, KOOne, ESOne, FROne string
}

func catalog(id MessageID, en, zh, ja, ko, es, fr string) catalogRow {
	return catalogRow{ID: id, EN: en, ZH: zh, JA: ja, KO: ko, ES: es, FR: fr}
}

type localizer struct {
	locale Locale
}

var placeholderPattern = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

var uiCatalog = buildCatalog()

func buildCatalog() map[Locale]map[MessageID]messageTemplate {
	out := make(map[Locale]map[MessageID]messageTemplate, len(supportedLocales))
	for _, locale := range supportedLocales {
		out[locale] = make(map[MessageID]messageTemplate, len(catalogRowsData))
	}
	for _, row := range catalogRowsData {
		values := map[Locale]messageTemplate{
			LocaleEnglish:  {One: row.ENOne, Other: row.EN},
			LocaleChinese:  {One: row.ZHOne, Other: row.ZH},
			LocaleJapanese: {One: row.JAOne, Other: row.JA},
			LocaleKorean:   {One: row.KOOne, Other: row.KO},
			LocaleSpanish:  {One: row.ESOne, Other: row.ES},
			LocaleFrench:   {One: row.FROne, Other: row.FR},
		}
		for locale, value := range values {
			out[locale][row.ID] = value
		}
	}
	return out
}

// normalizeUILocale accepts POSIX and BCP-47 forms. Unsupported languages
// intentionally select English instead of leaking a message ID into the UI.
func normalizeUILocale(raw string) Locale {
	normalized := microcopy.NormalizeLocale(raw)
	switch Locale(normalized) {
	case LocaleChinese, LocaleJapanese, LocaleKorean, LocaleSpanish, LocaleFrench:
		return Locale(normalized)
	default:
		return LocaleEnglish
	}
}

func newLocalizer(raw string) localizer {
	return localizer{locale: normalizeUILocale(raw)}
}

func (m *Model) text(id MessageID, args MessageArgs) string {
	return newLocalizer(m.locale).Text(id, args)
}

func (m *Model) countText(id MessageID, count int, args MessageArgs) string {
	copyArgs := make(MessageArgs, len(args)+1)
	for key, value := range args {
		copyArgs[key] = value
	}
	copyArgs["count"] = count
	return newLocalizer(m.locale).Count(id, count, copyArgs)
}

func (l localizer) Text(id MessageID, args MessageArgs) string {
	return l.render(id, false, 0, args)
}

func (l localizer) Count(id MessageID, count int, args MessageArgs) string {
	return l.render(id, true, count, args)
}

func (l localizer) render(id MessageID, counted bool, count int, args MessageArgs) string {
	template, ok := uiCatalog[l.locale][id]
	if !ok || strings.TrimSpace(template.Other) == "" {
		template = uiCatalog[LocaleEnglish][id]
	}
	value := template.Other
	if counted && pluralFormForInteger(l.locale, count) == "one" && template.One != "" {
		value = template.One
	}
	if strings.TrimSpace(value) == "" {
		// This is deliberately a sentence rather than the ID. Catalog tests make
		// the branch unreachable for declared IDs, while embedders still get a
		// safe, legible result for an invalid value.
		return unavailableMessage[l.locale]
	}
	return placeholderPattern.ReplaceAllStringFunc(value, func(token string) string {
		name := token[1 : len(token)-1]
		value, ok := args[name]
		if !ok {
			return "?"
		}
		return sanitizeCatalogArg(value)
	})
}

// pluralFormForInteger follows the current CLDR cardinal rules for the six
// supported languages. Only one/other can occur for integer counts here.
func pluralFormForInteger(locale Locale, count int) string {
	switch locale {
	case LocaleEnglish, LocaleSpanish:
		if count == 1 {
			return "one"
		}
	case LocaleFrench:
		if count == 0 || count == 1 {
			return "one"
		}
	}
	return "other"
}

func sanitizeCatalogArg(value any) string {
	var raw string
	switch v := value.(type) {
	case string:
		raw = v
	case int:
		raw = strconv.Itoa(v)
	case int64:
		raw = strconv.FormatInt(v, 10)
	default:
		raw = fmt.Sprint(v)
	}
	raw = sanitize(raw)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, raw)
}

func templatePlaceholders(value string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, match := range placeholderPattern.FindAllStringSubmatch(value, -1) {
		out[match[1]] = struct{}{}
	}
	return out
}
