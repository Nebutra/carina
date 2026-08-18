package daemon

import "strings"

// Named HITL presets are labels over existing product mode + permission
// profile pairs. They do not add capabilities, do not mutate the session
// profile, and never include always-approve / yolo.
const (
	hitlPresetReadOnly    = "read-only"
	hitlPresetAgent       = "agent"
	hitlPresetAcceptEdits = "accept-edits"
)

type hitlPreset struct {
	ID          string `json:"id"`
	ProductMode string `json:"product_mode"`
	Profile     string `json:"profile"`
}

func hitlPresetCatalog() []hitlPreset {
	return []hitlPreset{
		{ID: hitlPresetReadOnly, ProductMode: approvalModeAsk, Profile: "read-only"},
		{ID: hitlPresetAgent, ProductMode: approvalModeAsk, Profile: "safe-edit"},
		{ID: hitlPresetAcceptEdits, ProductMode: approvalModeAcceptEdits, Profile: "safe-edit"},
	}
}

func resolveHITLPreset(name string) (hitlPreset, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case hitlPresetReadOnly, "readonly", "read_only":
		return hitlPresetCatalog()[0], true
	case hitlPresetAgent:
		return hitlPresetCatalog()[1], true
	case hitlPresetAcceptEdits, "accept_edits", "acceptedits":
		return hitlPresetCatalog()[2], true
	default:
		return hitlPreset{}, false
	}
}

func matchHITLPreset(productMode, profile string) string {
	productMode = strings.TrimSpace(productMode)
	profile = strings.TrimSpace(profile)
	switch {
	case productMode == approvalModeAsk && profile == "read-only":
		return hitlPresetReadOnly
	case productMode == approvalModeAsk && (profile == "safe-edit" || profile == ""):
		return hitlPresetAgent
	case productMode == approvalModeAcceptEdits:
		return hitlPresetAcceptEdits
	default:
		return ""
	}
}

func hitlPresetIDs() []string {
	out := make([]string, 0, 3)
	for _, preset := range hitlPresetCatalog() {
		out = append(out, preset.ID)
	}
	return out
}
