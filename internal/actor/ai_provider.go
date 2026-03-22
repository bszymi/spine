package actor

import "context"

// AIProvider defines the interface for AI agent execution.
// Per Actor Model §6.
type AIProvider interface {
	Execute(ctx context.Context, prompt AIPrompt) (*AIResponse, error)
}

// AIPrompt contains the structured input for an AI provider.
type AIPrompt struct {
	SystemPrompt string  `json:"system_prompt"`
	UserPrompt   string  `json:"user_prompt"`
	MaxTokens    int     `json:"max_tokens,omitempty"`
	Temperature  float64 `json:"temperature,omitempty"`
}

// AIResponse contains the parsed output from an AI provider.
type AIResponse struct {
	Content   string `json:"content"`
	OutcomeID string `json:"outcome_id"`
}
