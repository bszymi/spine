package engine

// INIT-023 wave 3 integration (codex review of the merged branch): a
// captured binding is stored before the execution completes. Before the
// fix SubmitStepResult persisted the completed execution first and wrote
// commit_meta afterwards; a failed write (or a crash) in between lost the
// binding for good, because the scheduler's step recovery re-submits a
// completed execution without capturing the head again. A gate then had
// no binding to compare with and approved whatever the run branch held,
// including a commit pushed after the producer submitted.

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/core/domain"
)

// completionThenMetaFailStore fails every SetCommitMeta that comes after
// the target execution was persisted as completed, and (optionally) the
// SetCommitMeta calls made while it is still open.
type completionThenMetaFailStore struct {
	*metaRecordingStore
	target       string
	completed    bool
	failBefore   bool
	failedWrites int
}

func (s *completionThenMetaFailStore) UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	if exec.ExecutionID == s.target && exec.Status == domain.StepStatusCompleted {
		s.completed = true
	}
	return s.metaRecordingStore.UpdateStepExecution(ctx, exec)
}

func (s *completionThenMetaFailStore) SetCommitMeta(ctx context.Context, runID string, meta map[string]string) error {
	if s.completed || s.failBefore {
		s.failedWrites++
		return errors.New("store: connection reset")
	}
	return s.metaRecordingStore.SetCommitMeta(ctx, runID, meta)
}

func TestBindingSurvivesAFailureAfterCompletion(t *testing.T) {
	f := newBindFix(t, legacyWorkflow(), true, "")
	execute := f.exec("execute", domain.StepStatusInProgress)
	st := &completionThenMetaFailStore{metaRecordingStore: f.store, target: execute.ExecutionID}
	f.orch.store = st
	f.git.setHead(f.run.BranchName, sha40('1'))

	// The producer submits at head 1. Whatever fails once the execution
	// is completed must not lose the binding.
	_ = f.submit(execute, "completed", "")
	if got := f.latestExec("execute"); got.Status != domain.StepStatusCompleted {
		t.Fatalf("execute = %s, want completed", got.Status)
	}
	if f.run.CommitMeta[CommitMetaBoundSHA] != sha40('1') ||
		f.run.CommitMeta[CommitMetaBoundBy] != boundByStep("execute", execute.ExecutionID) {
		t.Fatalf("commit_meta after the completed submit = %v, want head 1 bound by %s",
			f.run.CommitMeta, execute.ExecutionID)
	}

	// A commit lands after the submit; the scheduler's recovery re-submits
	// the completed execution (it never captures again).
	f.git.setHead(f.run.BranchName, sha40('2'))
	f.orch.store = f.store
	if err := f.orch.SubmitStepResult(context.Background(), execute.ExecutionID, StepResult{OutcomeID: "completed"}); err != nil {
		t.Fatalf("recovery re-submit: %v", err)
	}
	if f.run.CommitMeta[CommitMetaBoundSHA] != sha40('1') {
		t.Fatalf("binding after recovery = %v, want head 1 (the head the producer submitted)", f.run.CommitMeta)
	}

	// The review is asked to approve the moved branch: refused, as for any
	// commit after the binding; the commit is never approved unvalidated.
	review := f.latestExec("review")
	if review == nil {
		review = f.exec("review", domain.StepStatusAssigned)
	}
	review.Status, review.ActorID = domain.StepStatusAssigned, "worker"
	err := f.submit(review, "accepted", "")
	requireBindingErr(t, err, domain.ErrConflict, ReasonRunBranchMovedSinceGate)
}

func TestBindingWriteFailureLeavesTheStepOpen(t *testing.T) {
	f := newBindFix(t, legacyWorkflow(), true, "")
	execute := f.exec("execute", domain.StepStatusInProgress)
	st := &completionThenMetaFailStore{metaRecordingStore: f.store, target: execute.ExecutionID, failBefore: true}
	f.orch.store = st
	f.git.setHead(f.run.BranchName, sha40('1'))

	if err := f.submit(execute, "completed", ""); err == nil {
		t.Fatal("submit succeeded although the binding could not be stored")
	}
	if st.completed {
		t.Fatal("the execution was completed although its binding was not stored")
	}
	got := f.latestExec("execute")
	if got.Status != domain.StepStatusInProgress || got.OutcomeID != "" {
		t.Fatalf("execute = %s/%q, want it still in progress with no outcome", got.Status, got.OutcomeID)
	}

	// A later submit, once the store answers, binds and completes.
	st.failBefore = false
	if err := f.submit(execute, "completed", ""); err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if f.run.CommitMeta[CommitMetaBoundSHA] != sha40('1') || f.latestExec("execute").Status != domain.StepStatusCompleted {
		t.Fatalf("second submit: commit_meta %v, execute %s", f.run.CommitMeta, f.latestExec("execute").Status)
	}
}

// The ordering change keeps the single write: a capture-carrying
// completion still makes exactly one SetCommitMeta, with the outcome's
// commit map and the binding together, and binding off is untouched.
func TestBindingWriteIsStillOneWrite(t *testing.T) {
	for _, on := range []bool{true, false} {
		f := newBindFix(t, legacyWorkflow(), on, "")
		f.git.setHead(f.run.BranchName, sha40('1'))
		if on {
			f.bindAt(sha40('1'), "execute")
		}
		review := f.exec("review", domain.StepStatusAssigned)
		_ = f.submit(review, "accepted", "") // the merge that follows is not under test
		if len(f.store.metaWrites) == 0 {
			t.Fatalf("binding=%v: no commit_meta write", on)
		}
		first := f.store.metaWrites[0]
		if first["status"] != "Completed" {
			t.Fatalf("binding=%v: first write %v lacks the outcome's commit map", on, first)
		}
		if on && (first[CommitMetaBoundSHA] != sha40('1') || first[CommitMetaBoundBy] != boundByStep("review", review.ExecutionID)) {
			t.Fatalf("binding on: first write %v lacks the review's binding", first)
		}
		if !on {
			if _, ok := first[CommitMetaBoundSHA]; ok {
				t.Fatalf("binding off wrote a binding: %v", first)
			}
		}
	}
}
