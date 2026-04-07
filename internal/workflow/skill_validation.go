package workflow

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// ValidateSkills checks that required_skills values on workflow steps
// reference registered skill names. Unregistered skills produce warnings
// (not errors) to allow skills to be registered after workflow creation.
func ValidateSkills(ctx context.Context, wf *domain.WorkflowDefinition, st store.Store) []domain.ValidationError {
	skills, err := st.ListSkills(ctx)
	if err != nil {
		// If we can't load skills, skip validation silently — this is a warning-only check
		return nil
	}

	skillNames := make(map[string]bool, len(skills))
	for i := range skills {
		if skills[i].Status == domain.SkillStatusActive {
			skillNames[skills[i].Name] = true
		}
	}

	// If no skills are registered yet, skip
	if len(skillNames) == 0 {
		return nil
	}

	var warnings []domain.ValidationError
	for i := range wf.Steps {
		if wf.Steps[i].Execution == nil || len(wf.Steps[i].Execution.RequiredSkills) == 0 {
			continue
		}
		for _, skill := range wf.Steps[i].Execution.RequiredSkills {
			if !skillNames[skill] {
				warnings = append(warnings, domain.ValidationError{
					RuleID:   "skill_registry",
					Field:    fmt.Sprintf("steps[%d].execution.required_skills", i),
					Severity: "warning",
					Message:  fmt.Sprintf("skill %q is not registered", skill),
				})
			}
		}
	}

	return warnings
}
