package daemon

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// presentDoneSummary converts a model-authored report wrapper into readable
// Markdown. Generic JSON remains JSON; only objects with a prose summary field
// are treated as an accidental presentation envelope.
func presentDoneSummary(summary string) string {
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" {
		return ""
	}
	var report map[string]any
	if json.Unmarshal([]byte(trimmed), &report) != nil {
		return trimmed
	}
	lead, ok := report["summary"].(string)
	if !ok || strings.TrimSpace(lead) == "" {
		return trimmed
	}

	var out strings.Builder
	out.WriteString(strings.TrimSpace(lead))
	keys := make([]string, 0, len(report)-1)
	for key := range report {
		if key != "summary" {
			keys = append(keys, key)
		}
	}
	sort.SliceStable(keys, func(i, j int) bool {
		left, right := reportSectionRank(keys[i]), reportSectionRank(keys[j])
		if left != right {
			return left < right
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		section := renderReportValue(report[key], 0)
		if section == "" {
			continue
		}
		out.WriteString("\n\n**")
		out.WriteString(reportSectionLabel(key))
		out.WriteString("**\n")
		out.WriteString(section)
	}
	return out.String()
}

func reportSectionRank(key string) int {
	for index, preferred := range []string{
		"result", "architecture", "engineering", "changes", "risks", "commands", "tests", "verification", "next_steps",
	} {
		if key == preferred {
			return index
		}
	}
	return 100
}

func reportSectionLabel(key string) string {
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(key))
	for index, word := range words {
		runes := []rune(word)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
			words[index] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

func renderReportValue(value any, depth int) string {
	if depth > 3 {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		lines := make([]string, 0, len(typed))
		for _, item := range typed {
			if rendered := renderReportValue(item, depth+1); rendered != "" {
				lines = append(lines, "- "+strings.ReplaceAll(rendered, "\n", "\n  "))
			}
		}
		return strings.Join(lines, "\n")
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		lines := make([]string, 0, len(keys))
		for _, key := range keys {
			if rendered := renderReportValue(typed[key], depth+1); rendered != "" {
				lines = append(lines, fmt.Sprintf("- **%s:** %s", reportSectionLabel(key), strings.ReplaceAll(rendered, "\n", "\n  ")))
			}
		}
		return strings.Join(lines, "\n")
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}
