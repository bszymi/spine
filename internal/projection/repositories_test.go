package projection

import (
	"context"
	"reflect"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// repoFakeStore captures the artifact and execution projections written
// by projectArtifact. Embeds store.Store so unimplemented methods will
// nil-dereference if a future change starts touching them — failures
// are loud rather than silent.
type repoFakeStore struct {
	store.Store
	lastArtifact  *store.ArtifactProjection
	lastExecution *store.ExecutionProjection
}

func (s *repoFakeStore) UpsertArtifactProjection(_ context.Context, proj *store.ArtifactProjection) error {
	cp := *proj
	s.lastArtifact = &cp
	return nil
}

func (s *repoFakeStore) UpsertArtifactLinks(_ context.Context, _ string, _ []store.ArtifactLink, _ string) error {
	return nil
}

func (s *repoFakeStore) UpsertExecutionProjection(_ context.Context, proj *store.ExecutionProjection) error {
	cp := *proj
	s.lastExecution = &cp
	return nil
}

func TestProjectArtifact_TaskRepositories(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "missing", in: nil, want: nil},
		{name: "empty", in: []string{}, want: nil},
		{name: "single", in: []string{"payments-service"}, want: []string{"payments-service"}},
		{name: "multi", in: []string{"payments-service", "api-gateway"}, want: []string{"payments-service", "api-gateway"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &repoFakeStore{}
			svc := NewService(nil, st, nil, 0)

			a := &domain.Artifact{
				Path:         "initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001-x.md",
				ID:           "TASK-001",
				Type:         domain.ArtifactTypeTask,
				Title:        "Test task",
				Status:       domain.StatusPending,
				Repositories: tt.in,
			}

			if err := svc.projectArtifact(context.Background(), a, "deadbeef"); err != nil {
				t.Fatalf("projectArtifact: %v", err)
			}

			if st.lastArtifact == nil {
				t.Fatal("artifact projection not written")
			}
			if !equalStringSlice(st.lastArtifact.Repositories, tt.want) {
				t.Errorf("artifact.Repositories: got %v, want %v", st.lastArtifact.Repositories, tt.want)
			}

			if st.lastExecution == nil {
				t.Fatal("execution projection not written for Task")
			}
			if !equalStringSlice(st.lastExecution.Repositories, tt.want) {
				t.Errorf("execution.Repositories: got %v, want %v", st.lastExecution.Repositories, tt.want)
			}
		})
	}
}

func TestProjectArtifact_NonTaskOmitsExecutionProjection(t *testing.T) {
	// An Epic must not produce an execution projection at all, and the
	// artifact projection must carry no repositories — non-Task artifacts
	// can't declare them per the schema (TASK-001 enforces this at parse).
	st := &repoFakeStore{}
	svc := NewService(nil, st, nil, 0)
	a := &domain.Artifact{
		Path:   "initiatives/INIT-001/epics/EPIC-001/epic.md",
		ID:     "EPIC-001",
		Type:   domain.ArtifactTypeEpic,
		Title:  "An Epic",
		Status: domain.StatusPending,
	}
	if err := svc.projectArtifact(context.Background(), a, "deadbeef"); err != nil {
		t.Fatalf("projectArtifact: %v", err)
	}
	if st.lastArtifact == nil {
		t.Fatal("artifact projection not written")
	}
	if len(st.lastArtifact.Repositories) != 0 {
		t.Errorf("Epic artifact must have empty Repositories, got %v", st.lastArtifact.Repositories)
	}
	if st.lastExecution != nil {
		t.Errorf("non-Task artifact must not produce execution projection, got %+v", st.lastExecution)
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
