package actor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

// MockResponse configures how the mock provider responds to a step.
type MockResponse struct {
	OutcomeID         string        // Outcome to return (required for success)
	ArtifactsProduced []string      // Artifacts produced by the step
	Summary           string        // Optional summary text
	Err               error         // If set, Execute returns this error
	Delay             time.Duration // Simulate execution time
}

// MockProvider is a deterministic actor provider for testing.
// It can be configured with per-step responses or a default response.
type MockProvider struct {
	mu       sync.RWMutex
	defaults MockResponse
	byStep   map[string]MockResponse // keyed by step ID
	executed []AssignmentRequest     // records all executed assignments
	handles  map[domain.ActorType]bool
}

// NewMockProvider creates a mock provider that handles the given actor types.
// If no types are specified, it handles all types.
func NewMockProvider(actorTypes ...domain.ActorType) *MockProvider {
	handles := make(map[domain.ActorType]bool)
	for _, t := range actorTypes {
		handles[t] = true
	}
	return &MockProvider{
		byStep:  make(map[string]MockResponse),
		handles: handles,
	}
}

// SetDefault configures the default response for any step without a specific config.
func (m *MockProvider) SetDefault(resp MockResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaults = resp
}

// SetStepResponse configures the response for a specific step ID.
func (m *MockProvider) SetStepResponse(stepID string, resp MockResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byStep[stepID] = resp
}

// Executed returns all assignment requests that were executed.
func (m *MockProvider) Executed() []AssignmentRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]AssignmentRequest, len(m.executed))
	copy(result, m.executed)
	return result
}

// CanHandle returns true if this provider handles the given actor type.
// If no specific types were configured, it handles all types.
func (m *MockProvider) CanHandle(actorType domain.ActorType) bool {
	if len(m.handles) == 0 {
		return true
	}
	return m.handles[actorType]
}

// Execute returns the configured response for the step, or the default.
// Satisfies engine.ActorProvider interface.
func (m *MockProvider) Execute(ctx context.Context, req AssignmentRequest) (*AssignmentResult, error) {
	m.mu.Lock()
	resp, ok := m.byStep[req.StepID]
	if !ok {
		resp = m.defaults
	}
	m.executed = append(m.executed, req)
	m.mu.Unlock()

	// Simulate execution delay.
	if resp.Delay > 0 {
		select {
		case <-time.After(resp.Delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Return error if configured.
	if resp.Err != nil {
		return nil, resp.Err
	}

	// Validate outcome is set for success.
	if resp.OutcomeID == "" {
		return nil, fmt.Errorf("mock provider: no outcome configured for step %s", req.StepID)
	}

	return &AssignmentResult{
		AssignmentID:      req.AssignmentID,
		RunID:             req.RunID,
		TraceID:           req.TraceID,
		ActorID:           "mock-actor",
		OutcomeID:         resp.OutcomeID,
		ArtifactsProduced: resp.ArtifactsProduced,
		Summary:           resp.Summary,
	}, nil
}

// ExecuteAI implements the AIProvider interface for AI-specific testing.
func (m *MockProvider) ExecuteAI(ctx context.Context, prompt AIPrompt) (*AIResponse, error) {
	m.mu.RLock()
	resp := m.defaults
	m.mu.RUnlock()

	if resp.Delay > 0 {
		select {
		case <-time.After(resp.Delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if resp.Err != nil {
		return nil, resp.Err
	}

	return &AIResponse{
		Content:   resp.Summary,
		OutcomeID: resp.OutcomeID,
	}, nil
}
