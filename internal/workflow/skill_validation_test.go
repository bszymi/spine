package workflow_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/workflow"
)

type skillFakeStore struct {
	store.Store
	skills []domain.Skill
}

func (f *skillFakeStore) ListSkills(_ context.Context) ([]domain.Skill, error) {
	return f.skills, nil
}

func TestValidateSkills_AllRegistered(t *testing.T) {
	st := &skillFakeStore{skills: []domain.Skill{
		{SkillID: "s1", Name: "code_review", Status: domain.SkillStatusActive},
		{SkillID: "s2", Name: "testing", Status: domain.SkillStatusActive},
	}}
	wf := &domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{
			{ID: "execute", Execution: &domain.ExecutionConfig{
				RequiredSkills: []string{"code_review", "testing"},
			}},
		},
	}

	warnings := workflow.ValidateSkills(context.Background(), wf, st)
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d: %v", len(warnings), warnings)
	}
}

func TestValidateSkills_UnregisteredSkill(t *testing.T) {
	st := &skillFakeStore{skills: []domain.Skill{
		{SkillID: "s1", Name: "code_review", Status: domain.SkillStatusActive},
	}}
	wf := &domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{
			{ID: "execute", Execution: &domain.ExecutionConfig{
				RequiredSkills: []string{"code_review", "unknown_skill"},
			}},
		},
	}

	warnings := workflow.ValidateSkills(context.Background(), wf, st)
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(warnings))
	}
	if warnings[0].Severity != "warning" {
		t.Errorf("expected severity warning, got %s", warnings[0].Severity)
	}
}

func TestValidateSkills_NoSkillsRegistered(t *testing.T) {
	st := &skillFakeStore{skills: nil}
	wf := &domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{
			{ID: "execute", Execution: &domain.ExecutionConfig{
				RequiredSkills: []string{"anything"},
			}},
		},
	}

	warnings := workflow.ValidateSkills(context.Background(), wf, st)
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings when no skills registered, got %d", len(warnings))
	}
}

func TestValidateSkills_DeprecatedSkillNotMatched(t *testing.T) {
	st := &skillFakeStore{skills: []domain.Skill{
		{SkillID: "s1", Name: "old_skill", Status: domain.SkillStatusDeprecated},
		{SkillID: "s2", Name: "active_skill", Status: domain.SkillStatusActive},
	}}
	wf := &domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{
			{ID: "execute", Execution: &domain.ExecutionConfig{
				RequiredSkills: []string{"old_skill"},
			}},
		},
	}

	warnings := workflow.ValidateSkills(context.Background(), wf, st)
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning for deprecated skill, got %d", len(warnings))
	}
}

func TestValidateSkills_NoExecutionBlock(t *testing.T) {
	st := &skillFakeStore{skills: []domain.Skill{
		{SkillID: "s1", Name: "code_review", Status: domain.SkillStatusActive},
	}}
	wf := &domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{
			{ID: "execute", Execution: nil},
		},
	}

	warnings := workflow.ValidateSkills(context.Background(), wf, st)
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings for step without execution block, got %d", len(warnings))
	}
}
