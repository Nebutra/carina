package provider

// Seed is a small offline catalog used before the first models.dev refresh.
// The refresh path fills this out with the full public provider enumeration.
func Seed() Catalog {
	return Catalog{
		"anthropic": {
			ID:   "anthropic",
			Name: "Anthropic",
			API:  "https://api.anthropic.com/v1",
			Env:  []string{"ANTHROPIC_API_KEY"},
			NPM:  "@ai-sdk/anthropic",
			Models: map[string]Model{
				"claude-sonnet-4-5-20250929": {
					ID:          "claude-sonnet-4-5-20250929",
					Name:        "Claude Sonnet 4.5",
					ReleaseDate: "2025-09-29",
					Reasoning:   true,
					ToolCall:    true,
					Limit:       ModelLimit{Context: 200000, Output: 64000},
				},
			},
		},
		"openai": {
			ID:   "openai",
			Name: "OpenAI",
			API:  "https://api.openai.com/v1",
			Env:  []string{"OPENAI_API_KEY"},
			NPM:  "@ai-sdk/openai",
			Models: map[string]Model{
				"gpt-5": {
					ID:        "gpt-5",
					Name:      "GPT-5",
					Reasoning: true,
					ToolCall:  true,
					Limit:     ModelLimit{Context: 400000, Output: 128000},
				},
			},
		},
		"openrouter": {
			ID:   "openrouter",
			Name: "OpenRouter",
			API:  "https://openrouter.ai/api/v1",
			Env:  []string{"OPENROUTER_API_KEY"},
			NPM:  "@openrouter/ai-sdk-provider",
		},
		"google": {
			ID:   "google",
			Name: "Google Gemini",
			API:  "https://generativelanguage.googleapis.com/v1beta",
			Env:  []string{"GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"},
			NPM:  "@ai-sdk/google",
		},
		"local": {
			ID:   "local",
			Name: "Local / OpenAI-compatible",
			API:  "http://localhost:1234/v1",
			Env:  []string{"CARINA_LOCAL_API_KEY"},
			NPM:  "@ai-sdk/openai-compatible",
		},
	}
}
