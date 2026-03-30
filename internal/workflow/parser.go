package workflow

import (
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"gopkg.in/yaml.v3"
)

// Parse parses a workflow YAML file into a domain WorkflowDefinition.
func Parse(path string, content []byte) (*domain.WorkflowDefinition, error) {
	var wf domain.WorkflowDefinition
	if err := yaml.Unmarshal(content, &wf); err != nil {
		return nil, fmt.Errorf("parse workflow %s: %w", path, err)
	}
	wf.Path = path
	// Default mode to "execution" for backward compatibility.
	if wf.Mode == "" {
		wf.Mode = "execution"
	}
	if wf.Mode != "execution" && wf.Mode != "creation" {
		return nil, fmt.Errorf("parse workflow %s: invalid mode %q (expected execution or creation)", path, wf.Mode)
	}
	return &wf, nil
}

// ValidateSchema performs schema validation per Workflow Validation §3.
func ValidateSchema(wf *domain.WorkflowDefinition) []domain.ValidationError {
	var errors []domain.ValidationError

	// §3.1 Top-level required fields
	if wf.ID == "" {
		errors = append(errors, schemaError("id", "required field missing"))
	}
	if wf.Name == "" {
		errors = append(errors, schemaError("name", "required field missing"))
	}
	if wf.Version == "" {
		errors = append(errors, schemaError("version", "required field missing"))
	}
	if wf.Status == "" {
		errors = append(errors, schemaError("status", "required field missing"))
	} else if wf.Status != domain.WorkflowStatusActive && wf.Status != domain.WorkflowStatusDeprecated && wf.Status != "Superseded" {
		errors = append(errors, schemaError("status", fmt.Sprintf("invalid status %q (expected Active, Deprecated, or Superseded)", wf.Status)))
	}
	if wf.Description == "" {
		errors = append(errors, schemaError("description", "required field missing"))
	}
	if len(wf.AppliesTo) == 0 {
		errors = append(errors, schemaError("applies_to", "must be non-empty"))
	}
	if wf.EntryStep == "" {
		errors = append(errors, schemaError("entry_step", "required field missing"))
	}
	if len(wf.Steps) == 0 {
		errors = append(errors, schemaError("steps", "must have at least one step"))
	}

	// Collect step IDs for cross-reference and detect duplicates
	stepIDs := make(map[string]bool)
	for i := range wf.Steps {
		id := wf.Steps[i].ID
		if id != "" && stepIDs[id] {
			errors = append(errors, schemaError(fmt.Sprintf("steps[%d].id", i), fmt.Sprintf("duplicate step id %q", id)))
		}
		stepIDs[id] = true
	}

	// Verify entry_step references a valid step
	if wf.EntryStep != "" && !stepIDs[wf.EntryStep] {
		errors = append(errors, schemaError("entry_step", fmt.Sprintf("references unknown step %q", wf.EntryStep)))
	}

	// §3.2-3.5 Step validation
	for i := range wf.Steps {
		prefix := fmt.Sprintf("steps[%d]", i)
		errors = append(errors, validateStep(&wf.Steps[i], prefix, stepIDs, wf)...)
	}

	return errors
}

func validateStep(step *domain.StepDefinition, prefix string, stepIDs map[string]bool, wf *domain.WorkflowDefinition) []domain.ValidationError {
	var errors []domain.ValidationError

	if step.ID == "" {
		errors = append(errors, schemaError(prefix+".id", "required field missing"))
	}
	if step.Name == "" {
		errors = append(errors, schemaError(prefix+".name", "required field missing"))
	}
	if step.Type == "" {
		errors = append(errors, schemaError(prefix+".type", "required field missing"))
	} else if step.Type != domain.StepTypeManual && step.Type != domain.StepTypeAutomated &&
		step.Type != domain.StepTypeReview && step.Type != domain.StepTypeConvergence {
		errors = append(errors, schemaError(prefix+".type", fmt.Sprintf("invalid step type %q", step.Type)))
	}
	if len(step.Outcomes) == 0 {
		errors = append(errors, schemaError(prefix+".outcomes", "must have at least one outcome"))
	}

	// §3.3 Conditional requirements
	if step.Type == domain.StepTypeAutomated {
		if step.Retry == nil || step.Retry.Limit < 1 {
			errors = append(errors, schemaError(prefix+".retry", "automated steps must have retry with limit >= 1"))
		}
	}
	if step.Timeout != "" && step.TimeoutOutcome == "" {
		errors = append(errors, schemaError(prefix+".timeout_outcome", "required when timeout is set"))
	}

	// §3.4 Outcome validation
	outcomeIDs := make(map[string]bool)
	for j := range step.Outcomes {
		outcome := &step.Outcomes[j]
		oPrefix := fmt.Sprintf("%s.outcomes[%d]", prefix, j)
		if outcome.ID == "" {
			errors = append(errors, schemaError(oPrefix+".id", "required field missing"))
		}
		if outcomeIDs[outcome.ID] {
			errors = append(errors, schemaError(oPrefix+".id", fmt.Sprintf("duplicate outcome id %q", outcome.ID)))
		}
		outcomeIDs[outcome.ID] = true

		if outcome.Name == "" {
			errors = append(errors, schemaError(oPrefix+".name", "required field missing"))
		}
		if outcome.NextStep == "" {
			errors = append(errors, schemaError(oPrefix+".next_step", "required field missing"))
		} else if outcome.NextStep != "end" && !stepIDs[outcome.NextStep] {
			errors = append(errors, schemaError(oPrefix+".next_step", fmt.Sprintf("references unknown step %q", outcome.NextStep)))
		}
	}

	// Verify timeout_outcome references a valid outcome
	if step.TimeoutOutcome != "" && !outcomeIDs[step.TimeoutOutcome] {
		errors = append(errors, schemaError(prefix+".timeout_outcome", fmt.Sprintf("references unknown outcome %q", step.TimeoutOutcome)))
	}

	// Validate diverge/converge references
	if step.Diverge != "" {
		divFound := false
		for i := range wf.DivergencePoints {
			if wf.DivergencePoints[i].ID == step.Diverge {
				divFound = true
				break
			}
		}
		if !divFound {
			errors = append(errors, schemaError(prefix+".diverge", fmt.Sprintf("references unknown divergence point %q", step.Diverge)))
		}
	}
	if step.Converge != "" {
		convFound := false
		for i := range wf.ConvergencePoints {
			if wf.ConvergencePoints[i].ID == step.Converge {
				convFound = true
				break
			}
		}
		if !convFound {
			errors = append(errors, schemaError(prefix+".converge", fmt.Sprintf("references unknown convergence point %q", step.Converge)))
		}
	}

	// §3.5 Execution block validation
	if step.Execution != nil {
		ePrefix := prefix + ".execution"
		if step.Execution.Mode != "" {
			validModes := map[domain.ExecutionMode]bool{
				domain.ExecModeAutomatedOnly: true,
				domain.ExecModeAIOnly:        true,
				domain.ExecModeHumanOnly:     true,
				domain.ExecModeHybrid:        true,
			}
			if !validModes[step.Execution.Mode] {
				errors = append(errors, schemaError(ePrefix+".mode", fmt.Sprintf("invalid execution mode %q", step.Execution.Mode)))
			}
		}
	}

	return errors
}

// ValidateStructure performs structural validation per Workflow Validation §4.
func ValidateStructure(wf *domain.WorkflowDefinition) []domain.ValidationError {
	var errors []domain.ValidationError

	if len(wf.Steps) == 0 || wf.EntryStep == "" {
		return errors
	}

	// §4.1 Reachability
	reachable := computeReachable(wf)
	for i := range wf.Steps {
		if !reachable[wf.Steps[i].ID] {
			errors = append(errors, structError(fmt.Sprintf("step %q is unreachable from entry_step %q", wf.Steps[i].ID, wf.EntryStep)))
		}
	}

	// §4.2 Termination
	if !canTerminate(wf) {
		errors = append(errors, structError("workflow may not terminate: not all paths reach 'end'"))
	}

	return errors
}

func computeReachable(wf *domain.WorkflowDefinition) map[string]bool {
	reachable := make(map[string]bool)
	queue := []string{wf.EntryStep}

	stepMap := make(map[string]*domain.StepDefinition)
	for i := range wf.Steps {
		stepMap[wf.Steps[i].ID] = &wf.Steps[i]
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if reachable[current] || current == "end" {
			continue
		}
		reachable[current] = true

		step, ok := stepMap[current]
		if !ok {
			continue
		}

		for j := range step.Outcomes {
			if !reachable[step.Outcomes[j].NextStep] {
				queue = append(queue, step.Outcomes[j].NextStep)
			}
		}

		// Follow divergence edges
		if step.Diverge != "" {
			for i := range wf.DivergencePoints {
				if wf.DivergencePoints[i].ID == step.Diverge {
					for j := range wf.DivergencePoints[i].Branches {
						if !reachable[wf.DivergencePoints[i].Branches[j].StartStep] {
							queue = append(queue, wf.DivergencePoints[i].Branches[j].StartStep)
						}
					}
				}
			}
		}

		// Follow convergence edges
		if step.Converge != "" {
			for i := range wf.ConvergencePoints {
				if wf.ConvergencePoints[i].ID == step.Converge {
					if !reachable[wf.ConvergencePoints[i].EvaluationStep] {
						queue = append(queue, wf.ConvergencePoints[i].EvaluationStep)
					}
				}
			}
		}
	}

	return reachable
}

func canTerminate(wf *domain.WorkflowDefinition) bool {
	stepMap := make(map[string]*domain.StepDefinition)
	for i := range wf.Steps {
		stepMap[wf.Steps[i].ID] = &wf.Steps[i]
	}

	for i := range wf.Steps {
		if !canReachEnd(wf.Steps[i].ID, stepMap, make(map[string]bool)) {
			return false
		}
	}
	return true
}

func canReachEnd(stepID string, stepMap map[string]*domain.StepDefinition, visited map[string]bool) bool {
	if stepID == "end" {
		return true
	}
	if visited[stepID] {
		return false
	}
	visited[stepID] = true

	step, ok := stepMap[stepID]
	if !ok {
		return false
	}

	for j := range step.Outcomes {
		if canReachEnd(step.Outcomes[j].NextStep, stepMap, copyMap(visited)) {
			return true
		}
	}
	return false
}

func copyMap(m map[string]bool) map[string]bool {
	c := make(map[string]bool, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// ValidateSemantic performs semantic validation per Workflow Validation §5.
func ValidateSemantic(wf *domain.WorkflowDefinition) []domain.ValidationError {
	var errors []domain.ValidationError

	for _, at := range wf.AppliesTo {
		valid := false
		for _, t := range domain.ValidArtifactTypes() {
			if domain.ArtifactType(at) == t {
				valid = true
				break
			}
		}
		if !valid {
			errors = append(errors, semanticError(fmt.Sprintf("applies_to contains unknown artifact type %q", at)))
		}
	}

	return errors
}

// Validate runs all validation levels and returns a combined result.
func Validate(wf *domain.WorkflowDefinition) domain.ValidationResult {
	var allErrors []domain.ValidationError

	allErrors = append(allErrors, ValidateSchema(wf)...)
	if len(allErrors) == 0 {
		allErrors = append(allErrors, ValidateStructure(wf)...)
		allErrors = append(allErrors, ValidateSemantic(wf)...)
	}

	status := "passed"
	if len(allErrors) > 0 {
		status = "failed"
	}

	return domain.ValidationResult{
		Status: status,
		Errors: allErrors,
	}
}

func schemaError(field, message string) domain.ValidationError {
	return domain.ValidationError{
		RuleID:   "schema",
		Field:    field,
		Severity: "error",
		Message:  message,
	}
}

func structError(message string) domain.ValidationError {
	return domain.ValidationError{
		RuleID:   "structural",
		Severity: "error",
		Message:  message,
	}
}

func semanticError(message string) domain.ValidationError {
	return domain.ValidationError{
		RuleID:   "semantic",
		Severity: "error",
		Message:  message,
	}
}
