package actor

import (
	"fmt"
	"strings"
)

// BuildPrompt constructs an AI prompt from an assignment request and system prompt.
// Per Actor Model §6.2: includes system context, step instructions, constraints.
func BuildPrompt(req AssignmentRequest, systemPrompt string) AIPrompt {
	var userParts []string

	userParts = append(userParts,
		fmt.Sprintf("## Step: %s (%s)", req.StepName, req.StepID),
		fmt.Sprintf("Task: %s", req.Context.TaskPath),
	)

	if req.Context.Instructions != "" {
		userParts = append(userParts, fmt.Sprintf("\n## Instructions\n%s", req.Context.Instructions))
	}

	if len(req.Context.RequiredInputs) > 0 {
		userParts = append(userParts, fmt.Sprintf("\n## Required Inputs\n- %s", strings.Join(req.Context.RequiredInputs, "\n- ")))
	}

	if len(req.Context.RequiredOutputs) > 0 {
		userParts = append(userParts, fmt.Sprintf("\n## Required Outputs\n- %s", strings.Join(req.Context.RequiredOutputs, "\n- ")))
	}

	if len(req.Constraints.ExpectedOutcomes) > 0 {
		userParts = append(userParts, fmt.Sprintf("\n## Expected Outcomes\nRespond with one of: %s", strings.Join(req.Constraints.ExpectedOutcomes, ", ")))
	}

	if req.Constraints.Timeout != "" {
		userParts = append(userParts, fmt.Sprintf("\nTimeout: %s", req.Constraints.Timeout))
	}

	return AIPrompt{
		SystemPrompt: systemPrompt,
		UserPrompt:   strings.Join(userParts, "\n"),
	}
}
