package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui/theme"
)

// entry is one transcript item with its render cached at push time (the R18
// per-entry cache: a growing transcript never re-renders old entries).
type entry struct {
	rendered string
}

// transcript accumulates rendered entries plus the flattened line slice the
// viewport consumes.
type transcript struct {
	entries []entry
	lines   []string
}

func (t *transcript) push(rendered string) {
	t.entries = append(t.entries, entry{rendered: rendered})
	t.lines = append(t.lines, strings.Split(rendered, "\n")...)
}

// Status glyphs: the four-glyph vocabulary from the plan (P1.3) with ASCII
// fallbacks under Mono/NO_COLOR. The exact ASCII set is a standing brand
// question (brand-brief §6 Q2); these are the documented placeholder
// defaults.
func glyphOK(th theme.Theme) string {
	if th.Profile() == theme.Mono {
		return "+"
	}
	return th.Style(theme.RoleSuccess).Render("✓")
}

func glyphNeedsAuth(th theme.Theme) string {
	if th.Profile() == theme.Mono {
		return "!"
	}
	return th.Style(theme.RoleWarning).Render("⚿")
}

func glyphFailed(th theme.Theme) string {
	if th.Profile() == theme.Mono {
		return "x"
	}
	return th.Style(theme.RoleError).Render("✗")
}

func glyphNeutral(th theme.Theme) string {
	if th.Profile() == theme.Mono {
		return "-"
	}
	return th.Style(theme.RoleMuted).Render("·")
}

// renderEvent turns one session.events.stream envelope into a transcript
// line. Governance moments render through the microcopy registers — the
// daemon ships machine codes, never prose. Rendered once per event; the
// transcript caches the result.
func renderEvent(ev map[string]any, th theme.Theme, locale string) string {
	ts, _ := ev["timestamp"].(string)
	if len(ts) >= 19 {
		ts = ts[11:19]
	}
	typ, _ := ev["type"].(string)
	muted := th.Style(theme.RoleMuted)
	title := th.Style(theme.RoleTitle)

	switch typ {
	case "permission.request":
		line := microcopy.Governed(microcopy.GovernedApprovalRequired, microcopy.Args{
			"action":      str(ev["capability"]),
			"path":        str(ev["resource"]),
			"decision_id": str(ev["decision_id"]),
		}, microcopy.WithLocale(locale))
		return fmt.Sprintf("%s %s %s", muted.Render(ts), glyphNeedsAuth(th), line)
	case "task.completed":
		status := str(ev["status"])
		glyph := glyphOK(th)
		switch status {
		case "failed", "cancelled", "degraded":
			glyph = glyphFailed(th)
		}
		detail := fmt.Sprintf("task %s %s", str(ev["task_id"]), status)
		if summary := str(ev["summary"]); summary != "" {
			detail += " — " + truncate(summary, 160)
		}
		return fmt.Sprintf("%s %s %s %s", muted.Render(ts), glyph, title.Render(typ), detail)
	}

	var detail string
	if p, ok := ev["payload"].(map[string]any); ok {
		switch {
		case str(p["command"]) != "":
			detail = str(p["command"])
		case str(p["chunk"]) != "":
			detail = str(p["chunk"])
		case str(p["status"]) != "":
			detail = str(p["status"])
		}
	}
	if detail == "" {
		b, _ := json.Marshal(ev["payload"])
		detail = truncate(string(b), 120)
	}
	// The payload is attacker/model-controlled (command stdout, in
	// particular): strip any ANSI/control sequences before it ever reaches
	// the terminal, so executed command output can't inject escape
	// sequences into the TUI (spoofed lines, cursor moves, screen clears).
	return fmt.Sprintf("%s %s %s %s", muted.Render(ts), glyphNeutral(th), title.Render(typ), ansi.Strip(detail))
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
