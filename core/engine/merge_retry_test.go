package engine_test

// INIT-023 TASK-012, audit P3-27 (AC 15): a committing run whose push
// keeps failing transiently is retried a bounded number of times, then
// AbandonExhaustedMerge routes it like a permanent merge failure. Run with
// merge binding off (Push fails) and on (PushCommit fails), against the
// real git.CLIClient with a bare origin.
//
// AC 26 (design-check DC-8) is here too: a refused bound merge is taken
// back before the shared tree is released, so an in-place write waiting
// for the tree never builds on, or publishes, the refused merge.

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/testutil"
)

// pushFailCLI is the shared client whose pushes of main always fail with
// a transient network error.
type pushFailCLI struct{ *git.CLIClient }

var errNetwork = &git.GitError{Kind: git.ErrKindTransient, Op: "push", Message: "network error"}

func (pushFailCLI) Push(context.Context, string, string) error               { return errNetwork }
func (pushFailCLI) PushCommit(context.Context, string, string, string) error { return errNetwork }

type retryEnv struct {
	t      *testing.T
	dir    string
	origin string
	cli    *git.CLIClient
	store  *outcomeStore
	actors *mockActorGateway
	orch   *engine.Orchestrator
	run    *domain.Run
}

// newRetryEnv starts a run of wf on a shared, home-branch client with a
// bare origin; op wraps the client (nil: the client itself), wrap wraps
// the store the orchestrator writes through (nil: the store itself).
func newRetryEnv(t *testing.T, wf *domain.WorkflowDefinition, binding bool, op func(*git.CLIClient) engine.GitOperator,
	wrap func(*outcomeStore, string) engine.RunStore) *retryEnv {
	t.Helper()
	testutil.HermeticGit(t)
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	dir := t.TempDir()
	e := &retryEnv{t: t, dir: dir}
	regGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "tasks/t.md", taskPending)
	regGit(t, dir, "add", "-A")
	regGit(t, dir, "commit", "-q", "-m", "seed")
	e.origin = filepath.Join(t.TempDir(), "origin.git")
	regGit(t, dir, "init", "-q", "--bare", e.origin)
	regGit(t, dir, "remote", "add", "origin", e.origin)
	regGit(t, dir, "push", "-q", "origin", "main")
	e.cli = git.NewCLIClient(dir, git.WithHomeBranch("main"))
	var gop engine.GitOperator = e.cli
	if op != nil {
		gop = op(e.cli)
	}
	e.store = newOutcomeStore()
	var st engine.RunStore = e.store
	if wrap != nil {
		st = wrap(e.store, dir)
	}
	e.actors = &mockActorGateway{}
	orch, err := engine.New(&memWorkflowResolver{wfDef: wf}, st, e.actors,
		&memArtifactReader{artifacts: map[string]*domain.Artifact{
			"tasks/t.md": {Path: "tasks/t.md", ID: "TASK-001", Type: "Task", Status: "Pending"},
		}},
		&memEventEmitter{}, gop, &memWorkflowLoader{wfDef: wf})
	if err != nil {
		t.Fatal(err)
	}
	orch.WithBranchProtectPolicy(branchprotect.New(branchprotect.StaticRules(branchprotect.BootstrapDefaults())))
	orch.WithActorSelector(&fixedActorSelector{actor: &domain.Actor{ActorID: "bot-1", Type: domain.ActorTypeAutomated, Status: domain.ActorStatusActive}})
	if binding {
		if err := orch.EnableMergeBinding(engine.MergeBindingConfig{}); err != nil {
			t.Fatalf("EnableMergeBinding: %v", err)
		}
	}
	e.orch = orch
	res, err := orch.StartRun(context.Background(), "tasks/t.md")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	e.run = res.Run
	return e
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	testutil.WriteFile(t, dir, rel, content)
}

func (e *retryEnv) commitOnRun(files map[string]string) {
	e.t.Helper()
	regGit(e.t, e.dir, "checkout", "-q", e.run.BranchName)
	for p, c := range files {
		writeFile(e.t, e.dir, p, c)
	}
	regGit(e.t, e.dir, "add", "-A")
	regGit(e.t, e.dir, "commit", "-q", "-m", "work")
	regGit(e.t, e.dir, "checkout", "-q", "main")
}

func (e *retryEnv) submit(i int, outcome string) {
	e.t.Helper()
	if i >= len(e.actors.assignments) {
		e.t.Fatalf("no assignment %d (have %d)", i, len(e.actors.assignments))
	}
	if err := e.orch.SubmitStepResult(context.Background(), e.actors.assignments[i].AssignmentID,
		engine.StepResult{OutcomeID: outcome}); err != nil {
		e.t.Fatalf("submit %d %s: %v", i, outcome, err)
	}
}

func (e *retryEnv) primary() domain.RepositoryMergeOutcome {
	e.t.Helper()
	o, err := e.store.GetRepositoryMergeOutcome(context.Background(), e.run.RunID, domain.PrimaryRepositoryID)
	if err != nil {
		e.t.Fatalf("primary outcome: %v", err)
	}
	return *o
}

func (e *retryEnv) runNow() *domain.Run {
	e.t.Helper()
	r, err := e.store.GetRun(context.Background(), e.run.RunID)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// driveToCommitting submits the workflow's steps up to the merge, which
// fails its first push (attempt 1).
func (e *retryEnv) driveToCommitting(publish bool) {
	e.t.Helper()
	e.commitOnRun(map[string]string{"deliverable.md": "work\n"})
	if publish {
		e.submit(0, "completed")
		e.submit(1, "passed")
		e.submit(2, "accepted")
	} else {
		e.submit(0, "completed")
		e.submit(1, "accepted")
	}
}

func TestMergeRetry_TransientPushIsBoundedThenAbandoned(t *testing.T) {
	for _, tc := range []struct {
		name    string
		binding bool
		publish bool
	}{
		{"binding off, legacy workflow", false, false},
		{"binding off, publish-step workflow", false, true},
		{"binding on, legacy workflow", true, false},
		{"binding on, publish-step workflow", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			wf := reviewWorkflow()
			if tc.publish {
				wf = taskDefaultWorkflow()
			}
			e := newRetryEnv(t, wf, tc.binding, func(c *git.CLIClient) engine.GitOperator { return pushFailCLI{c} }, nil)
			e.driveToCommitting(tc.publish)
			for attempt := 1; ; attempt++ {
				if r := e.runNow(); r.Status != domain.RunStatusCommitting {
					t.Fatalf("attempt %d: run %s, want committing", attempt, r.Status)
				}
				o := e.primary()
				if o.Attempts != attempt || o.Status != domain.RepositoryMergeStatusFailed || o.FailureClass != domain.MergeFailureNetwork {
					t.Fatalf("attempt %d: outcome = %d/%s/%s, want %d/failed/network", attempt, o.Attempts, o.Status, o.FailureClass, attempt)
				}
				if attempt == 4 {
					break
				}
				if attempt == 3 {
					before := e.primary()
					if ok, err := e.orch.AbandonExhaustedMerge(ctx, e.run.RunID, 3); ok || err != nil {
						t.Fatalf("AbandonExhaustedMerge at attempts 3 = %v, %v; want false", ok, err)
					}
					if after := e.primary(); after.FailureDetail != before.FailureDetail || after.Attempts != before.Attempts {
						t.Fatal("a non-exhausted check changed the outcome")
					}
				}
				if err := e.orch.MergeRunBranch(ctx, e.run.RunID); err != nil { // the scheduler's retry
					t.Fatalf("retry %d: %v", attempt, err)
				}
			}
			if ok, err := e.orch.AbandonExhaustedMerge(ctx, e.run.RunID, 0); ok || err != nil {
				t.Fatalf("maxRetries 0 = %v, %v; want false", ok, err)
			}
			ok, err := e.orch.AbandonExhaustedMerge(ctx, e.run.RunID, 3)
			if !ok || err != nil {
				t.Fatalf("AbandonExhaustedMerge at attempts 4 = %v, %v; want true", ok, err)
			}
			r := e.runNow()
			if tc.publish {
				if r.Status != domain.RunStatusActive || r.CurrentStepID != "execute" {
					t.Fatalf("publish workflow: run %s at %s, want active at execute (merge_failed)", r.Status, r.CurrentStepID)
				}
			} else {
				if r.Status != domain.RunStatusFailed {
					t.Fatalf("legacy workflow: run %s, want failed", r.Status)
				}
				var last *domain.StepExecution
				for _, s := range e.store.steps {
					if s.RunID == e.run.RunID && s.Status == domain.StepStatusCompleted && s.ErrorDetail != nil {
						last = s
					}
				}
				if last == nil || last.ErrorDetail.Classification != domain.FailureMergeRetriesExhausted ||
					!strings.Contains(last.ErrorDetail.Message, "attempts") {
					t.Fatalf("step error detail = %+v, want classification %s", last, domain.FailureMergeRetriesExhausted)
				}
			}
			o := e.primary()
			const prefix = "retries exhausted (attempts=4, max_retries=3): "
			if o.Attempts != 4 || o.FailureClass != domain.MergeFailureNetwork || !strings.HasPrefix(o.FailureDetail, prefix) ||
				strings.Count(o.FailureDetail, "retries exhausted (") != 1 {
				t.Fatalf("outcome after abandon = %d/%s/%q", o.Attempts, o.FailureClass, o.FailureDetail)
			}
			if ok, err := e.orch.AbandonExhaustedMerge(ctx, e.run.RunID, 3); ok || err != nil {
				t.Fatalf("a second AbandonExhaustedMerge = %v, %v; want false", ok, err)
			}
		})
	}
}

// refuseCLI is the shared client with a verifyBoundMerge refusal injected
// after MergeBound: CommitParents lies about the merge commit's second
// parent. While the merge block still holds the tree it starts an in-place
// artifact write, which must wait for the tree.
type refuseCLI struct {
	*git.CLIClient
	mu        sync.Mutex
	merge     git.MergeResult
	onMerged  func(git.MergeResult)
	triggered bool
}

func (r *refuseCLI) MergeBound(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	res, err := r.CLIClient.MergeBound(ctx, opts)
	if err == nil {
		r.mu.Lock()
		r.merge = res
		first := !r.triggered
		r.triggered = true
		r.mu.Unlock()
		if first && r.onMerged != nil {
			r.onMerged(res)
		}
	}
	return res, err
}

func (r *refuseCLI) CommitParents(ctx context.Context, sha string) ([]string, error) {
	r.mu.Lock()
	m := r.merge
	r.mu.Unlock()
	if sha == m.SHA && m.BaseSHA != "" {
		return []string{m.BaseSHA, strings.Repeat("e", 40)}, nil
	}
	return r.CLIClient.CommitParents(ctx, sha)
}

// mainAtRecord records local main when the primary outcome is recorded.
type mainAtRecord struct {
	*outcomeStore
	dir    string
	mu     sync.Mutex
	mainAt []string
}

func (s *mainAtRecord) UpsertRepositoryMergeOutcome(ctx context.Context, o *domain.RepositoryMergeOutcome) error {
	if o.RepositoryID == domain.PrimaryRepositoryID && o.Status == domain.RepositoryMergeStatusFailed {
		out, _ := exec.Command("git", "-C", s.dir, "rev-parse", "refs/heads/main").Output()
		s.mu.Lock()
		s.mainAt = append(s.mainAt, strings.TrimSpace(string(out)))
		s.mu.Unlock()
	}
	return s.outcomeStore.UpsertRepositoryMergeOutcome(ctx, o)
}

// AC 26.
func TestF9_RefusedBoundMergeNeverVisibleToNextHolder(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "") // artifact auto-push follows SPINE_GIT_AUTO_PUSH (on)
	rc := &refuseCLI{}
	var rec *mainAtRecord
	e := newRetryEnv(t, reviewWorkflow(), true, func(c *git.CLIClient) engine.GitOperator {
		rc.CLIClient = c
		return rc
	}, func(st *outcomeStore, dir string) engine.RunStore {
		rec = &mainAtRecord{outcomeStore: st, dir: dir}
		return rec
	})
	svc := artifact.NewService(git.NewCLIClient(e.dir), nil, e.dir)
	svc.WithPolicy(branchprotect.NewPermissive())
	const doc = "---\ntype: Governance\ntitle: Concurrent\nstatus: Living Document\nversion: \"0.1\"\n---\n# Concurrent\n"

	var writeDone = make(chan error, 1)
	var waited bool
	rc.onMerged = func(git.MergeResult) {
		go func() {
			_, err := svc.Create(context.Background(), "governance/concurrent.md", doc)
			writeDone <- err
		}()
		select {
		case err := <-writeDone:
			writeDone <- err
		case <-time.After(400 * time.Millisecond):
			waited = true
		}
	}
	e.commitOnRun(map[string]string{"deliverable.md": "work\n"})
	e.submit(0, "completed")
	base := regGit(t, e.dir, "rev-parse", "refs/heads/main")
	e.submit(1, "accepted") // the bound merge runs here and is refused

	if !waited {
		t.Fatal("the in-place write did not wait for the merge block")
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("in-place write: %v", err)
	}
	rc.mu.Lock()
	m := rc.merge
	rc.mu.Unlock()
	if m.SHA == "" || m.BaseSHA != base {
		t.Fatalf("merge = %+v, want one computed on %s", m, base)
	}
	write := regGit(t, e.dir, "log", "-1", "--format=%H", "refs/heads/main", "--", "governance/concurrent.md")
	if parent := regGit(t, e.dir, "rev-parse", write+"^"); parent != base {
		t.Fatalf("the in-place write's parent is %s, want the base %s (the refused merge is %s)", parent, base, m.SHA)
	}
	if err := exec.Command("git", "-C", e.origin, "merge-base", "--is-ancestor", m.SHA, "refs/heads/main").Run(); err == nil {
		t.Fatal("origin main contains the refused merge")
	} else {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			// The object may simply be absent from origin: also not contained.
			if !strings.Contains(err.Error(), "exit status") {
				t.Fatalf("is-ancestor on origin: %v", err)
			}
		}
	}
	o := e.primary()
	if o.Status != domain.RepositoryMergeStatusFailed || !strings.Contains(o.FailureDetail, "merge binding violation") {
		t.Fatalf("primary outcome = %+v", o)
	}
	// When the refusal was recorded, local main no longer held the refused
	// merge: it was the base, or the in-place write that waited for the
	// tree and landed on the base after it was released.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.mainAt) == 0 {
		t.Fatal("no failed primary outcome was recorded")
	}
	for _, at := range rec.mainAt {
		if at != base && at != write {
			t.Fatalf("local main was %s when the refusal was recorded, want the base %s (or the write %s on it; the refused merge is %s)",
				at, base, write, m.SHA)
		}
	}
}
