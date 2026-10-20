package validation_test

// INIT-023 TASK-017 AC 4: blocking status conflicts (engine-level
// promotion of SC-002 / SC-003) and the merge-time hook
// ValidateStatusChange.

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/validation"
)

const (
	scEpic = "initiatives/test/epics/e1.md"
	scTask = "initiatives/test/tasks/t1.md"
)

func epicWithChild(epicStatus, childStatus string) *fakeStore {
	fs := newFakeStore()
	addArtifact(fs, scEpic, "Epic", epicStatus,
		map[string]string{"initiative": "/initiatives/test/initiative.md"}, nil)
	addArtifact(fs, scTask, "Task", childStatus,
		map[string]string{"epic": "/" + scEpic}, nil)
	return fs
}

func blocking() validation.Option {
	return validation.WithBlockingStatusConflicts(validation.DefaultBlockingStatusConflicts...)
}

func TestBlockingStatusConflicts_Default(t *testing.T) {
	if want := []string{"SC-002", "SC-003"}; !reflect.DeepEqual(validation.DefaultBlockingStatusConflicts, want) {
		t.Fatalf("DefaultBlockingStatusConflicts = %v, want %v", validation.DefaultBlockingStatusConflicts, want)
	}
	if got := validation.NewEngine(newFakeStore(), blocking()).BlockingStatusConflicts(); !reflect.DeepEqual(got, []string{"SC-002", "SC-003"}) {
		t.Fatalf("BlockingStatusConflicts() = %v", got)
	}
	if got := validation.NewEngine(newFakeStore()).BlockingStatusConflicts(); got != nil {
		t.Fatalf("no option: BlockingStatusConflicts() = %v, want none", got)
	}
	var nilEngine *validation.Engine
	if nilEngine.BlockingStatusConflicts() != nil {
		t.Fatal("nil engine has a blocking set")
	}
}

func TestBlockingStatusConflicts_Promotion(t *testing.T) {
	ctx := context.Background()
	t.Run("completed epic with a pending child is an error", func(t *testing.T) {
		e := validation.NewEngine(epicWithChild("Completed", "Pending"), blocking())
		res := e.Validate(ctx, scEpic)
		if res.Status != "failed" || !hasRuleError(res, "SC-002") {
			t.Fatalf("result = %+v, want failed with SC-002 in Errors", res)
		}
		if hasRuleWarning(res, "SC-002") {
			t.Fatal("SC-002 must not also be a warning")
		}
	})
	t.Run("without the option it stays a warning", func(t *testing.T) {
		e := validation.NewEngine(epicWithChild("Completed", "Pending"))
		res := e.Validate(ctx, scEpic)
		if !hasRuleWarning(res, "SC-002") || hasRuleError(res, "SC-002") {
			t.Fatalf("result = %+v, want an SC-002 warning only", res)
		}
	})
	t.Run("SC-001 stays a warning", func(t *testing.T) {
		fs := newFakeStore()
		addArtifact(fs, scTask, "Task", "Completed", nil, nil)
		res := validation.NewEngine(fs, blocking()).Validate(ctx, scTask)
		if !hasRuleWarning(res, "SC-001") || hasRuleError(res, "SC-001") {
			t.Fatalf("result = %+v, want SC-001 as a warning", res)
		}
	})
	t.Run("unknown ids are ignored", func(t *testing.T) {
		e := validation.NewEngine(epicWithChild("Completed", "Pending"), validation.WithBlockingStatusConflicts("SC-999"))
		if res := e.Validate(ctx, scEpic); hasRuleError(res, "SC-002") {
			t.Fatalf("an unknown id promoted SC-002: %+v", res)
		}
	})
}

// failingStore fails every projection read.
type failingStore struct {
	*fakeStore
	getErr, queryErr error
}

func (f *failingStore) GetArtifactProjection(ctx context.Context, path string) (*store.ArtifactProjection, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.fakeStore.GetArtifactProjection(ctx, path)
}

func (f *failingStore) QueryArtifacts(ctx context.Context, q store.ArtifactQuery) (*store.ArtifactQueryResult, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.fakeStore.QueryArtifacts(ctx, q)
}

func TestValidateStatusChange(t *testing.T) {
	ctx := context.Background()
	t.Run("pending epic to completed with a pending child: promoted", func(t *testing.T) {
		fs := epicWithChild("Pending", "Pending")
		res, err := validation.NewEngine(fs, blocking()).ValidateStatusChange(ctx, scEpic, domain.StatusCompleted)
		if err != nil || res.Status != "failed" || !hasRuleError(res, "SC-002") {
			t.Fatalf("ValidateStatusChange = %+v, %v, want an SC-002 error", res, err)
		}
		if fs.artifacts[scEpic].Status != "Pending" {
			t.Fatal("ValidateStatusChange modified the projection row")
		}
	})
	t.Run("not promoted: a warning", func(t *testing.T) {
		res, err := validation.NewEngine(epicWithChild("Pending", "Pending")).ValidateStatusChange(ctx, scEpic, domain.StatusCompleted)
		if err != nil || res.Status != "warnings" || !hasRuleWarning(res, "SC-002") || len(res.Errors) != 0 {
			t.Fatalf("ValidateStatusChange = %+v, %v, want an SC-002 warning", res, err)
		}
	})
	t.Run("terminal children: passed", func(t *testing.T) {
		res, err := validation.NewEngine(epicWithChild("Pending", "Completed"), blocking()).ValidateStatusChange(ctx, scEpic, domain.StatusCompleted)
		if err != nil || res.Status != "passed" {
			t.Fatalf("ValidateStatusChange = %+v, %v", res, err)
		}
	})
	t.Run("only status-conflict rules run", func(t *testing.T) {
		// An epic with a dangling parent link would fail structural and
		// link rules in Validate; the hook ignores them.
		fs := newFakeStore()
		addArtifact(fs, scEpic, "Epic", "Pending", nil,
			[]domain.Link{{Type: "parent", Target: "/missing.md"}})
		res, err := validation.NewEngine(fs, blocking()).ValidateStatusChange(ctx, scEpic, domain.StatusCompleted)
		if err != nil || res.Status != "passed" {
			t.Fatalf("ValidateStatusChange = %+v, %v, want passed", res, err)
		}
		for _, e := range append(res.Errors, res.Warnings...) {
			if e.Classification != domain.ViolationStatusConflict {
				t.Fatalf("non status-conflict finding %+v", e)
			}
		}
	})
	t.Run("unknown path: passed", func(t *testing.T) {
		res, err := validation.NewEngine(newFakeStore(), blocking()).ValidateStatusChange(ctx, "nope.md", domain.StatusCompleted)
		if err != nil || res.Status != "passed" {
			t.Fatalf("ValidateStatusChange = %+v, %v", res, err)
		}
	})
	t.Run("projection read error", func(t *testing.T) {
		fs := &failingStore{fakeStore: epicWithChild("Pending", "Pending"), getErr: errors.New("db down")}
		if _, err := validation.NewEngine(fs, blocking()).ValidateStatusChange(ctx, scEpic, domain.StatusCompleted); err == nil {
			t.Fatal("a store error must be returned")
		}
	})
	t.Run("child query error in a blocking rule", func(t *testing.T) {
		// SC-002 swallows its own query error ("no open children"); the
		// hook must not turn that into a pass.
		fs := &failingStore{fakeStore: epicWithChild("Pending", "Pending"), queryErr: errors.New("db down")}
		if _, err := validation.NewEngine(fs, blocking()).ValidateStatusChange(ctx, scEpic, domain.StatusCompleted); err == nil {
			t.Fatal("a failed child listing must be an error, not a pass")
		}
		// Without promotion the rule cannot block, so the failure is moot.
		if _, err := validation.NewEngine(fs).ValidateStatusChange(ctx, scEpic, domain.StatusCompleted); err != nil {
			t.Fatalf("non-blocking rule's store error: %v", err)
		}
	})
	t.Run("nil engine: passed", func(t *testing.T) {
		var e *validation.Engine
		res, err := e.ValidateStatusChange(ctx, scEpic, domain.StatusCompleted)
		if err != nil || res.Status != "passed" {
			t.Fatalf("nil engine = %+v, %v", res, err)
		}
	})
}

// EvaluatePreconditions' cross_artifact_valid inherits the promotion.
func TestBlockingStatusConflicts_PreconditionInherits(t *testing.T) {
	step := domain.StepDefinition{ID: "review", Preconditions: []domain.Precondition{{Type: validation.PreconditionTypeCrossArtifactValid}}}
	fs := epicWithChild("Completed", "Pending")
	addArtifact(fs, "initiatives/test/initiative.md", "Initiative", "Pending", nil, nil)
	if res := validation.EvaluatePreconditions(context.Background(), validation.NewEngine(fs, blocking()), step, scEpic); res.Status != "failed" || !hasRuleError(*res, "SC-002") {
		t.Fatalf("promoted: %+v, want failed on SC-002", res)
	}
	if res := validation.EvaluatePreconditions(context.Background(), validation.NewEngine(fs), step, scEpic); res.Status == "failed" || !hasRuleWarning(*res, "SC-002") {
		t.Fatalf("not promoted: %+v, want an SC-002 warning only", res)
	}
}
