package domain_test

import (
	"reflect"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

func TestAffectedRepositoriesForTask(t *testing.T) {
	tests := []struct {
		name string
		task *domain.Artifact
		want []string
	}{
		{
			name: "nil task produces primary-only",
			task: nil,
			want: []string{domain.PrimaryRepositoryID},
		},
		{
			name: "task with no repositories field produces primary-only",
			task: &domain.Artifact{Type: domain.ArtifactTypeTask},
			want: []string{domain.PrimaryRepositoryID},
		},
		{
			name: "task with empty repositories slice produces primary-only",
			task: &domain.Artifact{Type: domain.ArtifactTypeTask, Repositories: []string{}},
			want: []string{domain.PrimaryRepositoryID},
		},
		{
			name: "task code repos are appended after primary in declared order",
			task: &domain.Artifact{
				Type:         domain.ArtifactTypeTask,
				Repositories: []string{"payments-service", "api-gateway"},
			},
			want: []string{domain.PrimaryRepositoryID, "payments-service", "api-gateway"},
		},
		{
			name: "primary id in task repos is deduped, not duplicated",
			task: &domain.Artifact{
				Type:         domain.ArtifactTypeTask,
				Repositories: []string{domain.PrimaryRepositoryID, "api-gateway"},
			},
			want: []string{domain.PrimaryRepositoryID, "api-gateway"},
		},
		{
			name: "duplicate code repos are deduped, first occurrence wins order",
			task: &domain.Artifact{
				Type:         domain.ArtifactTypeTask,
				Repositories: []string{"api-gateway", "payments-service", "api-gateway"},
			},
			want: []string{domain.PrimaryRepositoryID, "api-gateway", "payments-service"},
		},
		{
			name: "empty-string repo ids are skipped",
			task: &domain.Artifact{
				Type:         domain.ArtifactTypeTask,
				Repositories: []string{"", "api-gateway", ""},
			},
			want: []string{domain.PrimaryRepositoryID, "api-gateway"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.AffectedRepositoriesForTask(tt.task)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AffectedRepositoriesForTask = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAffectedRepositoriesForTask_DoesNotAliasInput guards against the
// helper returning a slice that shares backing memory with the caller's
// task.Repositories — a foot-gun if the caller later mutates the input.
func TestAffectedRepositoriesForTask_DoesNotAliasInput(t *testing.T) {
	task := &domain.Artifact{
		Type:         domain.ArtifactTypeTask,
		Repositories: []string{"a", "b"},
	}
	got := domain.AffectedRepositoriesForTask(task)
	task.Repositories[0] = "MUTATED"
	if got[1] == "MUTATED" {
		t.Fatalf("affected slice aliases task.Repositories: got %v", got)
	}
}
