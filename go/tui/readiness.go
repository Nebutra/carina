package tui

import (
	"strings"

	"github.com/Nebutra/carina/go/tui/theme"
)

type readinessAssessment struct {
	State   conversationReadiness
	Model   string
	Backend string
	Reason  string
}

func qualifyInventoryModel(providerID, modelID string) string {
	providerID = strings.TrimSpace(providerID)
	modelID = strings.TrimSpace(modelID)
	if providerID == "" || modelID == "" {
		return ""
	}
	if strings.HasPrefix(modelID, providerID+"/") {
		return modelID
	}
	return providerID + "/" + modelID
}

func effectiveInventoryDefault(response modelListResponse) string {
	if model := strings.TrimSpace(response.DefaultModel); model != "" && model != "default" && inventoryModelRunnable(response, model) {
		return model
	}
	for _, provider := range response.Providers {
		if !provider.Registered || !provider.Available {
			continue
		}
		if model := qualifyInventoryModel(provider.ID, provider.DefaultModel); model != "" {
			return model
		}
		for _, model := range provider.Models {
			if model.Available && strings.TrimSpace(model.ID) != "" {
				return strings.TrimSpace(model.ID)
			}
		}
	}
	return ""
}

func inventoryModelRunnable(response modelListResponse, selected string) bool {
	selected = strings.TrimSpace(selected)
	if selected == "" || selected == "default" {
		return false
	}
	for _, provider := range response.Providers {
		if !provider.Registered || !provider.Available {
			continue
		}
		prefix := strings.TrimSpace(provider.ID) + "/"
		if !strings.HasPrefix(selected, prefix) {
			continue
		}
		if selected == qualifyInventoryModel(provider.ID, provider.DefaultModel) {
			return true
		}
		if provider.DynamicModels {
			return strings.TrimPrefix(selected, prefix) != ""
		}
		for _, model := range provider.Models {
			if model.Available && strings.TrimSpace(model.ID) == selected {
				return true
			}
		}
		return false
	}
	return false
}

func assessModelReadiness(response modelListResponse, selected string) readinessAssessment {
	selected = strings.TrimSpace(selected)
	if selected == "default" {
		selected = ""
	}
	backend := ""
	if response.Reasoner != nil {
		backend = strings.TrimSpace(response.Reasoner.Backend)
		if !response.Reasoner.Available {
			return readinessAssessment{State: readinessBlocked, Backend: backend, Reason: "no configured runnable reasoner"}
		}
		if backend != "" && backend != "model-router" {
			if !response.Reasoner.Explicit {
				return readinessAssessment{State: readinessBlocked, Backend: backend, Reason: "reasoner backend is not explicitly configured"}
			}
			model := selected
			if model == "" {
				model = strings.TrimSpace(response.Reasoner.Model)
			}
			return readinessAssessment{State: readinessReady, Model: model, Backend: backend}
		}
	}

	model := selected
	if model == "" {
		model = effectiveInventoryDefault(response)
	}
	if model != "" && inventoryModelRunnable(response, model) {
		return readinessAssessment{State: readinessReady, Model: model, Backend: stringOr(backend, "model-router")}
	}
	if selected != "" {
		return readinessAssessment{State: readinessBlocked, Model: selected, Backend: backend, Reason: "selected model is not runnable"}
	}
	return readinessAssessment{State: readinessBlocked, Backend: backend, Reason: "no runnable provider model"}
}

func (m *Model) applyModelInventory(response modelListResponse) {
	m.runtime.ModelInventory = response
	m.runtime.HasModelInventory = true
	assessment := assessModelReadiness(response, m.model)
	m.runtime.DefaultModel = assessment.Model
	m.runtime.ReasonerBackend = assessment.Backend
	m.runtime.ReadinessReason = assessment.Reason
	m.runtime.ReasonerModel = ""
	if response.Reasoner != nil {
		m.runtime.ReasonerModel = strings.TrimSpace(response.Reasoner.Model)
	}
	m.applyConversation(conversationTransition{
		Kind: transitionReadiness, Readiness: assessment.State,
		EventType: "model.inventory", Status: assessment.Reason,
	})
}

func (m *Model) refreshReadinessFromInventory() {
	if m.runtime.HasModelInventory {
		m.applyModelInventory(m.runtime.ModelInventory)
	}
}

func (m *Model) runtimeModelLabel() (string, bool) {
	if backend := strings.TrimSpace(m.runtime.ReasonerBackend); backend != "" && backend != "model-router" {
		return backend, false
	}
	if model := strings.TrimSpace(m.model); model != "" && model != "default" {
		return model, true
	}
	if model := strings.TrimSpace(m.runtime.DefaultModel); model != "" && model != "default" {
		return model, true
	}
	return "", true
}

func (m *Model) newTaskReady() bool {
	return m.conversationSnapshot().Readiness == readinessReady
}

func (m *Model) blockNewTaskForReadiness() {
	reason := strings.TrimSpace(m.runtime.ReadinessReason)
	if reason == "" {
		reason = m.text(MsgStatusChecking, nil)
	}
	m.push(m.th.Style(theme.RoleWarning).Render(m.text(MsgReadinessSubmissionBlocked, MessageArgs{"reason": reason})))
	m.openSettings(settingsTabModel)
}

func slashRequiresNewTask(text string) bool {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return false
	}
	switch strings.TrimPrefix(parts[0], "/") {
	case "review", "commit", "btw", "side":
		return true
	case "plan":
		return len(parts) > 1
	default:
		return false
	}
}
