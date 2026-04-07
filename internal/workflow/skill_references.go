package workflow

import (
	"context"
	"encoding/json"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// SkillReferenceStore defines the store operations needed for skill reference checks.
type SkillReferenceStore interface {
	ListActiveWorkflowProjections(ctx context.Context) ([]store.WorkflowProjection, error)
}

// SkillReference describes a workflow that references a skill.
type SkillReference struct {
	WorkflowID   string `json:"workflow_id"`
	WorkflowPath string `json:"workflow_path"`
}

// FindWorkflowsReferencingSkill returns active workflows whose step definitions
// include the given skill name in required_skills.
func FindWorkflowsReferencingSkill(ctx context.Context, skillName string, st SkillReferenceStore) ([]SkillReference, error) {
	projections, err := st.ListActiveWorkflowProjections(ctx)
	if err != nil {
		return nil, err
	}

	var refs []SkillReference
	for i := range projections {
		if workflowReferencesSkill(projections[i].Definition, skillName) {
			refs = append(refs, SkillReference{
				WorkflowID:   projections[i].WorkflowID,
				WorkflowPath: projections[i].WorkflowPath,
			})
		}
	}
	return refs, nil
}

// workflowReferencesSkill checks if a workflow definition JSONB contains the
// given skill name in any step's required_skills.
func workflowReferencesSkill(definition []byte, skillName string) bool {
	if len(definition) == 0 {
		return false
	}

	var wf domain.WorkflowDefinition
	if err := json.Unmarshal(definition, &wf); err != nil {
		return false
	}

	for i := range wf.Steps {
		step := &wf.Steps[i]
		if step.Execution == nil {
			continue
		}
		for _, skill := range step.Execution.RequiredSkills {
			if skill == skillName {
				return true
			}
		}
	}
	return false
}
