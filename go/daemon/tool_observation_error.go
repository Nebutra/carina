package daemon

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

type ToolObservationStatus string
type ToolObservationCategory string
type ToolRecoveryVerb string

const (
	toolObservationFailed    ToolObservationStatus = "failed"
	toolObservationDenied    ToolObservationStatus = "denied"
	toolObservationTimedOut  ToolObservationStatus = "timed_out"
	toolObservationCancelled ToolObservationStatus = "cancelled"

	toolCategoryInvalidRequest ToolObservationCategory = "invalid_request"
	toolCategoryPermission     ToolObservationCategory = "permission"
	toolCategoryConflict       ToolObservationCategory = "conflict"
	toolCategoryStaleState     ToolObservationCategory = "stale_state"
	toolCategoryResourceLimit  ToolObservationCategory = "resource_limit"
	toolCategoryUnavailable    ToolObservationCategory = "unavailable"
	toolCategoryTimeout        ToolObservationCategory = "timeout"
	toolCategoryCancelled      ToolObservationCategory = "cancelled"
	toolCategoryInternal       ToolObservationCategory = "internal"

	toolRecoveryRevise          ToolRecoveryVerb = "revise"
	toolRecoveryRetry           ToolRecoveryVerb = "retry"
	toolRecoveryReread          ToolRecoveryVerb = "re_read"
	toolRecoveryRequestApproval ToolRecoveryVerb = "request_approval"
	toolRecoveryNone            ToolRecoveryVerb = "none"
)

type ToolObservationError struct {
	Status    ToolObservationStatus   `json:"status"`
	Category  ToolObservationCategory `json:"category"`
	Retryable bool                    `json:"retryable"`
	Recovery  ToolRecoveryVerb        `json:"recovery"`
	Message   string                  `json:"message"`
}

type toolObservationRule struct {
	category  ToolObservationCategory
	retryable bool
	recovery  ToolRecoveryVerb
	message   string
	expose    bool
}

var toolObservationRules = map[string]toolObservationRule{
	"invalid_args":             {toolCategoryInvalidRequest, true, toolRecoveryRevise, "", true},
	"invalid_arguments":        {toolCategoryInvalidRequest, true, toolRecoveryRevise, "", true},
	"invalid_command":          {toolCategoryInvalidRequest, true, toolRecoveryRevise, "", true},
	"invalid_input":            {toolCategoryInvalidRequest, true, toolRecoveryRevise, "", true},
	"invalid_job_request":      {toolCategoryInvalidRequest, true, toolRecoveryRevise, "", true},
	"job_not_found":            {toolCategoryInvalidRequest, false, toolRecoveryRevise, "job not found", false},
	"invalid_memory_write":     {toolCategoryInvalidRequest, true, toolRecoveryRevise, "", true},
	"invalid_query":            {toolCategoryInvalidRequest, true, toolRecoveryRevise, "", true},
	"invalid_read_range":       {toolCategoryInvalidRequest, true, toolRecoveryRevise, "", true},
	"invalid_text":             {toolCategoryInvalidRequest, true, toolRecoveryRevise, "", true},
	"invalid_url":              {toolCategoryInvalidRequest, true, toolRecoveryRevise, "", true},
	"unsupported_media_type":   {toolCategoryInvalidRequest, true, toolRecoveryRevise, "", true},
	"unsupported_read_range":   {toolCategoryInvalidRequest, true, toolRecoveryRevise, "", true},
	"unknown_tool":             {toolCategoryInvalidRequest, false, toolRecoveryRevise, "unknown tool; choose a registered tool", false},
	"policy_denied":            {toolCategoryPermission, false, toolRecoveryRevise, "request denied by policy", false},
	"plan_mode":                {toolCategoryPermission, true, toolRecoveryRevise, "tool is unavailable in Plan mode; continue read-only or finish the plan", false},
	"hook_denied":              {toolCategoryPermission, true, toolRecoveryRevise, "request blocked by a workspace hook", false},
	"tool_denied":              {toolCategoryPermission, true, toolRecoveryRevise, "tool request was denied", false},
	"tool_not_allowed":         {toolCategoryPermission, false, toolRecoveryRevise, "tool is not allowed for this agent", false},
	"tool_restricted":          {toolCategoryPermission, false, toolRecoveryRevise, "tool is restricted for this agent", false},
	"approval_denied":          {toolCategoryPermission, true, toolRecoveryRequestApproval, "required approval was not granted", false},
	"provider_challenged":      {toolCategoryPermission, true, toolRecoveryRequestApproval, "provider requires operator action", false},
	"workspace_untrusted":      {toolCategoryPermission, true, toolRecoveryRequestApproval, "workspace trust approval is required", false},
	"edit_span_rejected":       {toolCategoryConflict, true, toolRecoveryReread, "", true},
	"write_provenance_denied":  {toolCategoryStaleState, true, toolRecoveryReread, "", true},
	"write_provenance_refused": {toolCategoryStaleState, true, toolRecoveryReread, "", true},
	"write_provenance_stale":   {toolCategoryStaleState, true, toolRecoveryReread, "", true},
	"stale_read":               {toolCategoryStaleState, true, toolRecoveryReread, "", true},
	"read_limit":               {toolCategoryResourceLimit, true, toolRecoveryRevise, "", true},
	"response_too_large":       {toolCategoryResourceLimit, true, toolRecoveryRevise, "response exceeded the tool limit; narrow the request", false},
	"depth_limit":              {toolCategoryResourceLimit, true, toolRecoveryRevise, "delegation depth limit reached; finish in the current agent", false},
	"io_error":                 {toolCategoryUnavailable, true, toolRecoveryRetry, "file operation did not complete", false},
	"network_error":            {toolCategoryUnavailable, true, toolRecoveryRetry, "network operation did not complete", false},
	"http_error":               {toolCategoryUnavailable, true, toolRecoveryRetry, "remote service returned an error", false},
	"mcp_error":                {toolCategoryUnavailable, true, toolRecoveryRetry, "MCP tool did not complete", false},
	"runner_error":             {toolCategoryUnavailable, true, toolRecoveryRetry, "command runner did not complete", false},
	"tool_error":               {toolCategoryUnavailable, true, toolRecoveryRetry, "tool did not complete", false},
	"workflow_error":           {toolCategoryUnavailable, true, toolRecoveryRetry, "workflow did not complete", false},
	"nonzero_exit":             {toolCategoryConflict, true, toolRecoveryRevise, "", true},
	"feature_disabled":         {toolCategoryPermission, false, toolRecoveryRevise, "requested feature is disabled", false},
	"skill_unavailable":        {toolCategoryUnavailable, true, toolRecoveryRevise, "", true},
	"redirect_refused":         {toolCategoryPermission, true, toolRecoveryRevise, "redirect target was not approved", false},
	"not_subscribed":           {toolCategoryUnavailable, true, toolRecoveryRetry, "live channel is not subscribed", false},
	"swarm_not_bound":          {toolCategoryUnavailable, true, toolRecoveryRevise, "live workflow channel is unavailable", false},
	"best_of_n_no_winner":      {toolCategoryConflict, true, toolRecoveryRevise, "no candidate passed validation; retry with a direct edit", false},
	"job_terminal_conflict":    {toolCategoryConflict, false, toolRecoveryNone, "job is already terminal and cannot be cancelled", false},
	"job_cancel_error":         {toolCategoryInternal, false, toolRecoveryNone, "job cancellation did not complete", false},
	"git_not_repository":       {toolCategoryInvalidRequest, false, toolRecoveryRevise, "workspace is not a Git repository", false},
	"git_bare_repository":      {toolCategoryInvalidRequest, false, toolRecoveryRevise, "bare Git repositories are not supported", false},
	"git_workspace_escape":     {toolCategoryPermission, false, toolRecoveryRevise, "Git worktree is outside the session workspace", false},
	"git_unsafe_configuration": {toolCategoryPermission, false, toolRecoveryRevise, "Git local configuration is not workspace-contained", false},
	"git_revision_unavailable": {toolCategoryConflict, true, toolRecoveryRevise, "requested Git revision is unavailable", false},
	"git_unavailable":          {toolCategoryUnavailable, true, toolRecoveryRetry, "Git executable is unavailable", false},
	"git_error":                {toolCategoryUnavailable, true, toolRecoveryRetry, "Git inspection did not complete", false},
	"timeout":                  {toolCategoryTimeout, true, toolRecoveryRetry, "tool timed out", false},
	"operator_cancelled":       {toolCategoryCancelled, false, toolRecoveryNone, "tool was cancelled", false},
	"cancelled":                {toolCategoryCancelled, false, toolRecoveryNone, "tool was cancelled", false},
	"governance_error":         {toolCategoryInternal, false, toolRecoveryNone, "tool governance could not complete", false},
	"audit_persistence_error":  {toolCategoryInternal, false, toolRecoveryNone, "tool result could not be persisted safely", false},
	"patch_apply_error":        {toolCategoryInternal, false, toolRecoveryNone, "transactional patch could not be applied", false},
	"patch_propose_error":      {toolCategoryInternal, false, toolRecoveryNone, "transactional patch could not be proposed", false},
	"memory_write_error":       {toolCategoryInternal, false, toolRecoveryNone, "memory update could not be committed", false},
}

var sensitiveAssignmentPattern = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|authorization|password|secret|token)\s*[:=]\s*\S+`)

func newToolObservationError(status, category, display string) *ToolObservationError {
	if status == "completed" {
		return nil
	}
	publicStatus := ToolObservationStatus(status)
	switch publicStatus {
	case toolObservationFailed, toolObservationDenied, toolObservationTimedOut, toolObservationCancelled:
	default:
		publicStatus = toolObservationFailed
		category = ""
	}
	rule, ok := toolObservationRules[category]
	if !ok {
		switch status {
		case "denied":
			rule = toolObservationRule{category: toolCategoryPermission, recovery: toolRecoveryRevise, message: "tool request was denied"}
		case "timed_out":
			rule = toolObservationRule{category: toolCategoryTimeout, retryable: true, recovery: toolRecoveryRetry, message: "tool timed out"}
		case "cancelled":
			rule = toolObservationRule{category: toolCategoryCancelled, recovery: toolRecoveryNone, message: "tool was cancelled"}
		default:
			rule = toolObservationRule{category: toolCategoryInternal, recovery: toolRecoveryNone, message: "tool did not complete successfully"}
		}
	}
	message := rule.message
	if rule.expose {
		message = sanitizeToolObservationMessage(operatorFacingToolError(display))
	}
	if message == "" {
		message = "tool did not complete successfully"
	}
	return &ToolObservationError{Status: publicStatus, Category: rule.category, Retryable: rule.retryable, Recovery: rule.recovery, Message: message}
}

func sanitizeToolObservationMessage(message string) string {
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	message = sensitiveAssignmentPattern.ReplaceAllString(message, "$1=<redacted>")
	fields := strings.Fields(message)
	for i, field := range fields {
		core := strings.Trim(field, `"'()[]{}:,;`)
		if filepath.IsAbs(core) || (len(core) >= 3 && core[1] == ':' && (core[2] == '\\' || core[2] == '/')) {
			fields[i] = strings.Replace(field, core, "<path>", 1)
		}
	}
	return truncateUTF8Bytes(strings.Join(fields, " "), 240)
}

func (e *ToolObservationError) modelJSON() string {
	if e == nil {
		return ""
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return `{"status":"failed","category":"internal","retryable":false,"recovery":"none","message":"tool did not complete successfully"}`
	}
	return string(raw)
}
