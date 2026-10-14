package engine

// INIT-023 TASK-008, audit P3-19: step-output evidence.
// TestStepCompleted_FlagsRequiredOutputsWithoutBranchChange is the
// committed form of the design-time PoC
// TestTASK008_PoC_StepCompletedCarriesNoBranchEvidence (HEAD:
// "step_completed has no payload"), with TASK-005's real captureHead and a
// fake CommitBinder whose ResolveBranchHead returns the scripted head.
// Mutations: replacing len(diffs) with 0 fails (b); dropping the
// RequiredOutputs condition fails (e).

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// evidenceGit is the fake binder with a scriptable Diff.
type evidenceGit struct {
	*fakeBinderGit
	diffErr   error
	diffBlock bool
	diffN     int
}

func (g *evidenceGit) Diff(ctx context.Context, a, b string) ([]git.FileDiff, error) {
	g.record("Diff " + a + " " + b)
	if g.diffBlock {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if g.diffErr != nil {
		return nil, g.diffErr
	}
	return make([]git.FileDiff, g.diffN), nil
}

type evidenceFix struct {
	*casFix
	eg *evidenceGit
}

func newEvidenceFix(t *testing.T, wf *domain.WorkflowDefinition, bindingOn bool) *evidenceFix {
	t.Helper()
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")
	f := newCasFix(t, wf, false)
	eg := &evidenceGit{fakeBinderGit: f.git}
	f.orch.git = eg
	if bindingOn {
		if err := f.orch.EnableMergeBinding(MergeBindingConfig{}); err != nil {
			t.Fatalf("EnableMergeBinding: %v", err)
		}
	}
	return &evidenceFix{casFix: f, eg: eg}
}

func (f *evidenceFix) seedBinding(sha, by string) {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	r := f.st.runs[f.runID]
	if r.CommitMeta == nil {
		r.CommitMeta = map[string]string{}
	}
	r.CommitMeta[CommitMetaBoundSHA] = sha
	r.CommitMeta[CommitMetaBoundBy] = by
}

// completedPayload decodes the single step_completed event for execID.
func (f *evidenceFix) completedPayload(t *testing.T, execID string) (domain.Event, stepCompletedPayload, map[string]any) {
	t.Helper()
	var found []domain.Event
	evs := f.events(domain.EventStepCompleted)
	for i := range evs {
		if strings.Contains(evs[i].EventID, execID) {
			found = append(found, evs[i])
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d step_completed events for %s, want 1 (events: %+v)", len(found), execID, f.events(domain.EventStepCompleted))
	}
	if len(found[0].Payload) == 0 {
		t.Fatal("step_completed has no payload")
	}
	var p stepCompletedPayload
	if err := json.Unmarshal(found[0].Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	var raw map[string]any
	_ = json.Unmarshal(found[0].Payload, &raw)
	return found[0], p, raw
}

func (f *evidenceFix) storedEvidence(t *testing.T, execID string) (stepEvidenceRecord, bool) {
	t.Helper()
	v, ok := f.st.run(f.runID).CommitMeta[CommitMetaEvidencePrefix+execID]
	if !ok {
		return stepEvidenceRecord{}, false
	}
	var rec stepEvidenceRecord
	if err := json.Unmarshal([]byte(v), &rec); err != nil {
		t.Fatalf("stored evidence does not decode: %v (%s)", err, v)
	}
	return rec, true
}

func TestStepCompleted_FlagsRequiredOutputsWithoutBranchChange(t *testing.T) {
	ctx := context.Background()
	base := sha40('b') // the run branch's baseline (newCasFix)
	branch := "spine/run/run-1"

	t.Run("(a) recorded head == baseline: unchanged, flagged, no diff", func(t *testing.T) {
		f := newEvidenceFix(t, casWorkflow(), true)
		f.git.setHead(branch, base)
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		if err := f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
			t.Fatalf("submit: %v", err)
		}
		_, p, _ := f.completedPayload(t, e.ExecutionID)
		want := BranchChange{RepositoryID: "spine", Status: BranchChangeUnchanged, HeadSHA: base,
			PreviousSHA: base, PreviousSource: EvidencePreviousSourceBaseline, ChangedPaths: intPtr(0)}
		if !reflect.DeepEqual(p.BranchChange, &want) {
			t.Fatalf("branch_change = %+v, want %+v", p.BranchChange, want)
		}
		if p.RequiredOutputsUnchanged == nil || !*p.RequiredOutputsUnchanged {
			t.Fatal("required_outputs_unchanged not set for a step that declared outputs and changed nothing")
		}
		if n := f.git.callCount("Diff"); n != 0 {
			t.Fatalf("%d Diff calls for an unchanged branch", n)
		}
		// (i) run detail copy equals the event.
		rec, ok := f.storedEvidence(t, e.ExecutionID)
		if !ok || !reflect.DeepEqual(rec.BranchChange, want) || !rec.RequiredOutputsUnchanged {
			t.Fatalf("stored evidence = %+v (present %v), want the event's", rec, ok)
		}
		// One commit_meta write for the submit: the binding and the
		// evidence together (never a second whole-map replace).
		if len(f.st.metaWrites) != 1 {
			t.Fatalf("%d commit_meta writes, want 1: %v", len(f.st.metaWrites), f.st.metaWrites)
		}
		w := f.st.metaWrites[0]
		if w[CommitMetaBoundSHA] != base || w[CommitMetaEvidencePrefix+e.ExecutionID] == "" {
			t.Fatalf("the single write %v lacks the binding or the evidence", w)
		}
	})

	t.Run("(b) head != previous: changed with the diff count", func(t *testing.T) {
		f := newEvidenceFix(t, casWorkflow(), true)
		f.eg.diffN = 3
		f.git.setHead(branch, sha40('1'))
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		if err := f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
			t.Fatalf("submit: %v", err)
		}
		_, p, _ := f.completedPayload(t, e.ExecutionID)
		want := BranchChange{RepositoryID: "spine", Status: BranchChangeChanged, HeadSHA: sha40('1'),
			PreviousSHA: base, PreviousSource: EvidencePreviousSourceBaseline, ChangedPaths: intPtr(3)}
		if !reflect.DeepEqual(p.BranchChange, &want) {
			t.Fatalf("branch_change = %+v, want %+v", p.BranchChange, want)
		}
		if p.RequiredOutputsUnchanged == nil || *p.RequiredOutputsUnchanged {
			t.Fatal("required_outputs_unchanged must be false for a changed branch")
		}
		if got := f.git.callCount("Diff " + base + " " + sha40('1')); got != 1 {
			t.Fatalf("Diff(previous, head) calls = %d, want 1 (calls %v)", got, f.git.calls)
		}
	})

	t.Run("(c) a failed diff: unknown, kind only, submit succeeds", func(t *testing.T) {
		f := newEvidenceFix(t, casWorkflow(), true)
		f.eg.diffErr = &git.GitError{Kind: git.ErrKindTransient, Op: "diff", Message: "secret-looking stderr /home/x"}
		f.git.setHead(branch, sha40('1'))
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		if err := f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
			t.Fatalf("submit: %v", err)
		}
		ev, p, _ := f.completedPayload(t, e.ExecutionID)
		want := BranchChange{RepositoryID: "spine", Status: BranchChangeUnknown, HeadSHA: sha40('1'),
			PreviousSHA: base, PreviousSource: EvidencePreviousSourceBaseline, Reason: "diff_failed:transient"}
		if !reflect.DeepEqual(p.BranchChange, &want) {
			t.Fatalf("branch_change = %+v, want %+v", p.BranchChange, want)
		}
		if strings.Contains(string(ev.Payload), "secret-looking") {
			t.Fatalf("git error text leaked into the event: %s", ev.Payload)
		}
	})

	t.Run("(d) rework attempt 2 is compared with the review's binding", func(t *testing.T) {
		f := newEvidenceFix(t, casWorkflow(), true)
		reviewBy := boundByStep("review", "run-1-review-1")
		f.seedBinding(sha40('7'), reviewBy)
		f.git.setHead(branch, sha40('7'))
		f.addExec("execute", domain.StepStatusCompleted, "actor-a")
		f.addExec("review", domain.StepStatusCompleted, "actor-r")
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		if e.Attempt != 2 {
			t.Fatalf("attempt = %d, want 2", e.Attempt)
		}
		if err := f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
			t.Fatalf("submit: %v", err)
		}
		_, p, _ := f.completedPayload(t, e.ExecutionID)
		want := BranchChange{RepositoryID: "spine", Status: BranchChangeUnchanged, HeadSHA: sha40('7'),
			PreviousSHA: sha40('7'), PreviousSource: reviewBy, ChangedPaths: intPtr(0)}
		if !reflect.DeepEqual(p.BranchChange, &want) {
			t.Fatalf("branch_change = %+v, want %+v", p.BranchChange, want)
		}
		if !*p.RequiredOutputsUnchanged {
			t.Fatal("an execute rework that pushed nothing since review must be flagged")
		}
	})

	t.Run("(e) a step without required_outputs is never flagged", func(t *testing.T) {
		f := newEvidenceFix(t, casWorkflow(), true)
		f.seedBinding(base, boundByStep("execute", "run-1-execute-1"))
		f.git.setHead(branch, base)
		e := f.addExec("review", domain.StepStatusInProgress, "actor-r")
		if err := f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "rejected"}); err != nil {
			t.Fatalf("submit: %v", err)
		}
		_, p, raw := f.completedPayload(t, e.ExecutionID)
		if p.BranchChange.Status != BranchChangeUnchanged {
			t.Fatalf("status = %s, want unchanged", p.BranchChange.Status)
		}
		if *p.RequiredOutputsUnchanged {
			t.Fatal("required_outputs_unchanged set for a step without required_outputs")
		}
		if _, ok := raw["required_outputs"]; ok {
			t.Fatalf("required_outputs present for a step that declares none: %v", raw["required_outputs"])
		}
	})

	t.Run("(f) artifacts_produced is capped, the count exact", func(t *testing.T) {
		f := newEvidenceFix(t, casWorkflow(), true)
		f.git.setHead(branch, base)
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		arts := []string{"deliverable", strings.Repeat("é", 400)} // 800 bytes
		for len(arts) < 40 {
			arts = append(arts, "a.md")
		}
		if err := f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: arts}); err != nil {
			t.Fatalf("submit: %v", err)
		}
		_, p, _ := f.completedPayload(t, e.ExecutionID)
		if len(p.ArtifactsProduced) != maxEventArtifacts || p.ArtifactsProducedCount == nil || *p.ArtifactsProducedCount != 40 {
			t.Fatalf("artifacts_produced = %d entries, count %v; want %d and 40", len(p.ArtifactsProduced), p.ArtifactsProducedCount, maxEventArtifacts)
		}
		if l := len(p.ArtifactsProduced[1]); l > maxEventArtifactLength || !strings.HasPrefix(arts[1], p.ArtifactsProduced[1]) {
			t.Fatalf("long entry = %d bytes, want a UTF-8-safe prefix of at most %d", l, maxEventArtifactLength)
		}
	})

	t.Run("(g) binding off: unknown/head_sha_unrecorded, no git, nothing stored", func(t *testing.T) {
		f := newEvidenceFix(t, casWorkflow(), false)
		f.git.setHead(branch, base)
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		if err := f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
			t.Fatalf("submit: %v", err)
		}
		_, p, _ := f.completedPayload(t, e.ExecutionID)
		want := BranchChange{RepositoryID: "spine", Status: BranchChangeUnknown, Reason: EvidenceReasonHeadUnrecorded}
		if !reflect.DeepEqual(p.BranchChange, &want) {
			t.Fatalf("branch_change = %+v, want %+v", p.BranchChange, want)
		}
		if len(f.git.calls) != 0 {
			t.Fatalf("binding off made git calls: %v", f.git.calls)
		}
		for k := range f.st.run(f.runID).CommitMeta {
			if strings.HasPrefix(k, CommitMetaEvidencePrefix) {
				t.Fatalf("binding off stored %s", k)
			}
		}
		for _, w := range f.st.metaWrites {
			for k := range w {
				if strings.HasPrefix(k, CommitMetaEvidencePrefix) {
					t.Fatalf("binding off wrote %s", k)
				}
			}
		}
	})

	t.Run("(h) a diff that blocks past the bound: timeout, submit returns", func(t *testing.T) {
		if evidenceDiffTimeout != 3*time.Second {
			t.Fatalf("evidenceDiffTimeout = %v, want 3s (SMP's per-execution lock budget is 10s)", evidenceDiffTimeout)
		}
		old := evidenceDiffTimeout
		evidenceDiffTimeout = 50 * time.Millisecond
		t.Cleanup(func() { evidenceDiffTimeout = old })
		f := newEvidenceFix(t, casWorkflow(), true)
		f.eg.diffBlock = true
		f.git.setHead(branch, sha40('1'))
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		done := make(chan error, 1)
		go func() {
			done <- f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}})
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a blocking diff blocked the submit")
		}
		_, p, _ := f.completedPayload(t, e.ExecutionID)
		if p.BranchChange.Status != BranchChangeUnknown || p.BranchChange.Reason != "diff_failed:timeout" || p.BranchChange.ChangedPaths != nil {
			t.Fatalf("branch_change = %+v, want unknown/diff_failed:timeout without a count", p.BranchChange)
		}
	})

	t.Run("(i) a workflow cannot forge an evidence key", func(t *testing.T) {
		wf := casWorkflow()
		wf.Steps[0].Outcomes[0].Commit = map[string]string{"spine.evidence.run-1-execute-1": `{"branch_change":{"status":"changed"}}`, "note": "x"}
		f := newEvidenceFix(t, wf, true)
		f.git.setHead(branch, base)
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		if err := f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
			t.Fatalf("submit: %v", err)
		}
		rec, ok := f.storedEvidence(t, e.ExecutionID)
		if !ok || rec.BranchChange.Status != BranchChangeUnchanged {
			t.Fatalf("stored evidence = %+v (present %v), want the engine's unchanged record", rec, ok)
		}
		if got := f.st.run(f.runID).CommitMeta["note"]; got != "x" {
			t.Fatalf("non-reserved outcome key lost: %q", got)
		}
	})
}

// failTerminalOnce fails the first conditional write that completes an
// execution, after the binding and evidence were stored (b911c04's order).
type failTerminalOnce struct {
	condStore
	failed bool
}

func (s *failTerminalOnce) TransitionStepExecution(ctx context.Context, e *domain.StepExecution, from domain.StepExecutionStatus, actorID string) (bool, error) {
	if e.Status == domain.StepStatusCompleted && !s.failed {
		s.failed = true
		return false, errors.New("store: connection reset")
	}
	return s.condStore.TransitionStepExecution(ctx, e, from, actorID)
}

// Codex review, round 3: a submit whose terminal write failed after its
// binding and evidence were stored is retried against the comparison point
// that attempt recorded, not against its own capture — otherwise every
// change reads "unchanged" and the step is falsely flagged. Mutation:
// dropping the own-binding branch in branchChangeAtSubmit fails it.
func TestStepCompleted_RetryKeepsTheComparisonPoint(t *testing.T) {
	f := newEvidenceFix(t, casWorkflow(), true)
	fs := &failTerminalOnce{condStore: condStore{f.st}}
	f.orch.store = fs
	f.eg.diffN = 2
	f.git.setHead("spine/run/run-1", sha40('1'))
	e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
	if err := f.orch.SubmitStepResult(context.Background(), e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err == nil {
		t.Fatal("first attempt: want the terminal write's error")
	}
	if got := f.st.exec(e.ExecutionID).Status; got != domain.StepStatusInProgress {
		t.Fatalf("after the failed write the execution is %s, want in_progress", got)
	}
	if err := f.orch.SubmitStepResult(context.Background(), e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	_, p, _ := f.completedPayload(t, e.ExecutionID)
	want := BranchChange{RepositoryID: "spine", Status: BranchChangeChanged, HeadSHA: sha40('1'),
		PreviousSHA: sha40('b'), PreviousSource: EvidencePreviousSourceBaseline, ChangedPaths: intPtr(2)}
	if !reflect.DeepEqual(p.BranchChange, &want) || *p.RequiredOutputsUnchanged {
		t.Fatalf("retry branch_change = %+v (flag %v), want %+v and no flag", p.BranchChange, *p.RequiredOutputsUnchanged, want)
	}
	if rec, ok := f.storedEvidence(t, e.ExecutionID); !ok || !reflect.DeepEqual(rec.BranchChange, want) {
		t.Fatalf("stored evidence = %+v, want %+v", rec.BranchChange, want)
	}
}

// The documented base payload, the per-execution event id and the
// envelope actor.
func TestStepCompleted_BasePayload(t *testing.T) {
	f := newEvidenceFix(t, casWorkflow(), false)
	e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
	if err := f.orch.SubmitStepResult(context.Background(), e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	ev, p, raw := f.completedPayload(t, e.ExecutionID)
	if ev.EventID != "evt-"+casTrace[:12]+"-"+e.ExecutionID+"-completed" {
		t.Fatalf("event id = %s", ev.EventID)
	}
	if ev.ActorID != "actor-a" {
		t.Fatalf("envelope actor = %q, want actor-a", ev.ActorID)
	}
	if p.StepID != "execute" || p.StepName != "Execute" || p.Outcome != "completed" || p.NextStep != "review" ||
		p.Attempt != 1 || p.HasCommit || p.ExecutionID != e.ExecutionID || p.ActorID != "actor-a" || p.DurationMS == nil {
		t.Fatalf("base payload = %+v", p)
	}
	if v, ok := raw["branch_id"]; !ok || v != nil {
		t.Fatalf("branch_id = %v (present %v), want null", v, ok)
	}
	if got := raw["required_outputs"]; !reflect.DeepEqual(got, []any{"deliverable"}) {
		t.Fatalf("required_outputs = %v", got)
	}
}

// dedupEmitter keeps the first event per id, like the event log's
// ON CONFLICT (event_id) DO NOTHING.
type dedupEmitter struct {
	seen   map[string]bool
	events []domain.Event
	all    []domain.Event
}

func (d *dedupEmitter) Emit(_ context.Context, e domain.Event) error {
	d.all = append(d.all, e)
	if d.seen[e.EventID] {
		return nil
	}
	d.seen[e.EventID] = true
	d.events = append(d.events, e)
	return nil
}

// AC 15: per-execution event ids; recovery re-emits the same id (and the
// log keeps the original payload); the publish step carries the base.
func TestStepCompleted_EventIDs(t *testing.T) {
	ctx := context.Background()
	t.Run("two attempts, two ids", func(t *testing.T) {
		f := newEvidenceFix(t, casWorkflow(), false)
		e1 := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		if err := f.orch.SubmitStepResult(ctx, e1.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
			t.Fatal(err)
		}
		r := f.st.exec(f.runID + "-review-1") // routed by the first submit
		if r == nil {
			t.Fatal("review not routed")
		}
		f.st.mutateExec(r.ExecutionID, func(x *domain.StepExecution) { x.Status, x.ActorID = domain.StepStatusInProgress, "actor-r" })
		if err := f.orch.SubmitStepResult(ctx, r.ExecutionID, StepResult{OutcomeID: "rejected"}); err != nil {
			t.Fatal(err)
		}
		e2 := f.st.exec(f.runID + "-execute-2")
		if e2 == nil {
			t.Fatal("rework attempt not created")
		}
		f.st.mutateExec(e2.ExecutionID, func(x *domain.StepExecution) { x.Status, x.ActorID = domain.StepStatusInProgress, "actor-a" })
		if err := f.orch.SubmitStepResult(ctx, e2.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, ev := range f.events(domain.EventStepCompleted) {
			if strings.Contains(ev.EventID, "-execute-") {
				ids[ev.EventID] = true
			}
		}
		if len(ids) != 2 || !ids[stepCompletedEventID(f.st.run(f.runID), e1)] || !ids[stepCompletedEventID(f.st.run(f.runID), e2)] {
			t.Fatalf("execute step_completed ids = %v, want one per attempt", ids)
		}
	})
	t.Run("recovery re-emits the same id; the log keeps the original", func(t *testing.T) {
		f := newEvidenceFix(t, casWorkflow(), true)
		d := &dedupEmitter{seen: map[string]bool{}}
		f.orch.events = d
		f.git.setHead("spine/run/run-1", sha40('b'))
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		if err := f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
			t.Fatal(err)
		}
		// The process stopped before routing: take the routed successor
		// back and the cursor with it, then recover.
		f.st.mu.Lock()
		delete(f.st.steps, f.runID+"-review-1")
		f.st.runs[f.runID].CurrentStepID = "execute"
		f.st.mu.Unlock()
		if err := f.orch.RecoverCompletedStep(ctx, e.ExecutionID); err != nil {
			t.Fatalf("RecoverCompletedStep: %v", err)
		}
		var emitted []domain.Event
		for _, ev := range d.all {
			if ev.Type == domain.EventStepCompleted && strings.Contains(ev.EventID, e.ExecutionID) {
				emitted = append(emitted, ev)
			}
		}
		if len(emitted) != 2 || emitted[0].EventID != emitted[1].EventID {
			t.Fatalf("emitted %d step_completed for %s (ids %v), want 2 with one id", len(emitted), e.ExecutionID, emitted)
		}
		var rec stepCompletedPayload
		_ = json.Unmarshal(emitted[1].Payload, &rec)
		if rec.BranchChange == nil || rec.BranchChange.Status != BranchChangeUnknown || rec.BranchChange.Reason != EvidenceReasonRecovered {
			t.Fatalf("recovery branch_change = %+v, want unknown/recovered", rec.BranchChange)
		}
		var kept []domain.Event
		for _, ev := range d.events {
			if ev.EventID == emitted[0].EventID {
				kept = append(kept, ev)
			}
		}
		var orig stepCompletedPayload
		_ = json.Unmarshal(kept[0].Payload, &orig)
		if len(kept) != 1 || orig.BranchChange.Status != BranchChangeUnchanged {
			t.Fatalf("the log kept %+v, want the original unchanged payload", orig.BranchChange)
		}
	})
	t.Run("the publish step carries the documented base fields", func(t *testing.T) {
		f := newEvidenceFix(t, publishWorkflow(), false)
		f.st.setRunStatus(f.runID, domain.RunStatusCommitting)
		e := f.addExec("publish", domain.StepStatusInProgress, EngineMergeActorID)
		if err := f.orch.advancePublishStepIfAny(ctx, f.st.run(f.runID), "published"); err != nil {
			t.Fatalf("advancePublishStepIfAny: %v", err)
		}
		ev, p, raw := f.completedPayload(t, e.ExecutionID)
		if ev.EventID != stepCompletedEventID(f.st.run(f.runID), e) {
			t.Fatalf("publish event id = %s", ev.EventID)
		}
		if p.StepID != "publish" || p.Outcome != "published" || p.NextStep != "end" || !p.HasCommit || p.ExecutionID != e.ExecutionID {
			t.Fatalf("publish payload = %+v", p)
		}
		if _, ok := raw["branch_change"]; ok {
			t.Fatalf("the engine-owned publish step carries branch_change: %v", raw)
		}
	})
}

// AC 16, engine part: run detail from the spine.evidence.* keys only.
func TestStepOutputEvidence_FromCommitMeta(t *testing.T) {
	ctx := context.Background()
	f := newEvidenceFix(t, casWorkflow(), true)
	f.git.setHead("spine/run/run-1", sha40('b'))
	e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
	if err := f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
		t.Fatal(err)
	}
	// An execution completed with binding off (no key) is omitted.
	f.addExec("review", domain.StepStatusCompleted, "actor-r")
	f.git.calls = nil
	f.ev.events = nil // run detail must not depend on the event log

	got, err := f.orch.StepOutputEvidence(ctx, f.runID)
	if err != nil {
		t.Fatalf("StepOutputEvidence: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("%d entries, want 1: %+v", len(got), got)
	}
	want := StepOutputEvidence{ExecutionID: e.ExecutionID, StepID: "execute", Attempt: 1, ActorID: "actor-a",
		OutcomeID: "completed", RequiredOutputs: []string{"deliverable"},
		BranchChange: BranchChange{RepositoryID: "spine", Status: BranchChangeUnchanged, HeadSHA: sha40('b'),
			PreviousSHA: sha40('b'), PreviousSource: EvidencePreviousSourceBaseline, ChangedPaths: intPtr(0)},
		RequiredOutputsUnchanged: true}
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("entry = %+v, want %+v", got[0], want)
	}
	if len(f.git.calls) != 0 {
		t.Fatalf("run detail made git calls: %v", f.git.calls)
	}

	// No key at all: nil, without loading the workflow.
	f2 := newEvidenceFix(t, casWorkflow(), false)
	f2.orch.wfLoader = &mockWorkflowLoader{err: domain.NewError(domain.ErrInternal, "must not load")}
	if got, err := f2.orch.StepOutputEvidence(ctx, f2.runID); err != nil || got != nil {
		t.Fatalf("no evidence = %+v, %v; want nil, nil", got, err)
	}
}

// The evidence keys ride headBinding.extra through mergeCommitMeta: an
// earlier execution's key is kept like every reserved key, the new one is
// added, and a non-reserved extra key is never written.
func TestEvidenceKeysRideTheBindingWrite(t *testing.T) {
	earlier := CommitMetaEvidencePrefix + "run-1-execute-1"
	current := map[string]string{
		"status": "Completed", earlier: "old",
		CommitMetaBoundSHA: sha40('1'), CommitMetaBoundBy: boundByStep("execute", "run-1-execute-1"),
	}
	hb := &headBinding{head: sha40('2'), by: boundByStep("review", "run-1-review-1"), stepID: "review", replaceRepoKeys: true,
		extra: map[string]string{CommitMetaEvidencePrefix + "run-1-review-1": "new", "not.reserved": "x"}}
	out, write := mergeCommitMeta(current, nil, false, true, hb)
	if !write {
		t.Fatal("a capture must be written")
	}
	if out[earlier] != "old" || out[CommitMetaEvidencePrefix+"run-1-review-1"] != "new" {
		t.Fatalf("evidence keys = %v", out)
	}
	if _, ok := out["not.reserved"]; ok {
		t.Fatalf("a non-reserved extra key was written: %v", out)
	}
	if out[CommitMetaBoundSHA] != sha40('2') || out["status"] != "Completed" {
		t.Fatalf("binding or workflow keys wrong: %v", out)
	}
	// Binding off never stores reserved keys (mergeCommitMeta's replace
	// semantics), so turning it off retires the evidence keys too.
	if out, write := mergeCommitMeta(current, map[string]string{"status": "Done"}, true, false, nil); !write || len(out) != 1 {
		t.Fatalf("binding off = %v, %v; want only the outcome map", out, write)
	}
}
