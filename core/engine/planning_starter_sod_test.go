package engine

// INIT-024 TASK-001 (2026-10-22 review finding N-3, also GV-1 / SA-1):
// separation of duties counts the actor who started a planning-mode run as
// a run author. A planning starter writes the run's content onto its branch
// at start and is bound to no execution, so before this task the starter
// could let another actor (an AI draft step) submit the draft unchanged and
// then review and accept their own content.
//
// The tests name the refusal through its pseudo-step strings ("(run
// start)", "(run start: starter not recorded)") rather than the constants,
// so the file also compiles at the scaffold base, where the regression
// tests are shown failing.

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
)

const (
	sodStarter        = "starter"
	sodRunStartStep   = "(run start)"
	sodStarterUnknown = "(run start: starter not recorded)"
	sodPlanningPath   = "initiatives/INIT-099/initiative.md"
)

// sodPlanningWF is the creation workflow the real-git tests run: an
// execute step a human or an AI agent may take (the "draft"), a human-only
// review needing the review skill, and the engine's publish merge.
func sodPlanningWF() *domain.WorkflowDefinition {
	wf := govPublishWF()
	wf.ID, wf.Path, wf.AppliesTo, wf.Mode = "artifact-creation", "workflows/artifact-creation.yaml", []string{"Initiative"}, "creation"
	wf.Steps[0].Execution = &domain.ExecutionConfig{Mode: domain.ExecModeHybrid, EligibleActorTypes: []string{"human", "ai_agent"}}
	wf.Steps[1].Execution = &domain.ExecutionConfig{Mode: domain.ExecModeHumanOnly, EligibleActorTypes: []string{"human"}, RequiredSkills: []string{"review"}}
	return wf
}

// sodPlanningActors are the actors of the N-3 PoC: the starter and an
// independent reviewer (both human reviewers with the review skill) and the
// AI agent that "drafts".
func sodPlanningActors() map[string]*domain.Actor {
	return map[string]*domain.Actor{
		sodStarter: {ActorID: sodStarter, Type: domain.ActorTypeHuman, Status: domain.ActorStatusActive, Role: domain.RoleReviewer},
		"reviewer": {ActorID: "reviewer", Type: domain.ActorTypeHuman, Status: domain.ActorStatusActive, Role: domain.RoleReviewer},
		"bot":      {ActorID: "bot", Type: domain.ActorTypeAIAgent, Status: domain.ActorStatusActive},
	}
}

type sodActorMap map[string]*domain.Actor

func (m sodActorMap) ResolveEntryActor(_ context.Context, id string) (*domain.Actor, error) {
	if a, ok := m[id]; ok {
		return a, nil
	}
	return nil, domain.NewError(domain.ErrNotFound, "no actor")
}

// installStrictSoD installs `spine serve`'s step-entry policy with the PoC
// actors and their skills.
func installStrictSoD(o *Orchestrator) {
	bs := newClaimBlockingStore()
	acts := sodPlanningActors()
	for k, v := range acts {
		bs.actors[k] = v
	}
	for _, id := range []string{sodStarter, "reviewer"} {
		bs.skills[id] = []domain.Skill{{Name: "review", Status: domain.SkillStatusActive}}
	}
	p := StrictStepEntryPolicy()
	p.Actors = sodActorMap(acts)
	o.blocking = bs
	o.WithStepEntryPolicy(p)
}

// branchCommitWriter is the artifact writer of a real repository: it
// commits the content on the branch the write context names, as the
// artifact service does for a planning run.
type branchCommitWriter struct {
	g     *govRepo
	calls int
}

func (w *branchCommitWriter) Create(ctx context.Context, path, content string) (*artifact.WriteResult, error) {
	w.calls++
	wc := artifact.GetWriteContext(ctx)
	if wc == nil || wc.Branch == "" {
		return nil, errors.New("planning write without a branch")
	}
	w.g.git("checkout", "-q", wc.Branch)
	w.g.writeCommit(map[string]string{path: content}, "create "+path)
	w.g.git("checkout", "-q", "main")
	return &artifact.WriteResult{}, nil
}

// startSoDPlanningRun builds a real repository whose orchestrator starts
// planning runs through StartPlanningRun under the strict policy, starts one
// as sodStarter and has the AI agent complete execute unchanged. g.run is
// the started run; its review execution is waiting.
func startSoDPlanningRun(t *testing.T) *govRepo {
	t.Helper()
	wf := sodPlanningWF()
	g := newGovRepo(t, govRepoOpts{wf: wf, merger: true})
	// Only runs StartPlanningRun creates: drop the fixture's hand-built run.
	g.store.runs = map[string]*domain.Run{}
	g.store.createdSteps = nil
	g.orch.workflows = &mockWorkflowResolver{result: &workflow.BindingResult{Workflow: wf, CommitSHA: "v1", VersionLabel: "1"}}
	g.orch.artifactWriter = &branchCommitWriter{g: g}
	installStrictSoD(g.orch)

	ctx := domain.WithActor(context.Background(), sodPlanningActors()[sodStarter])
	res, err := g.orch.StartPlanningRun(ctx, sodPlanningPath, validArtifactContent)
	if err != nil {
		t.Fatalf("StartPlanningRun: %v", err)
	}
	g.run, g.branch = res.Run, res.Run.BranchName
	if !strings.HasPrefix(g.branch, "spine/plan/") {
		t.Fatalf("planning branch = %q", g.branch)
	}
	if got := g.git("show", g.branch+":"+sodPlanningPath); !strings.Contains(got, "INIT-099") {
		t.Fatalf("the starter's content is not on the run branch: %q", got)
	}
	g.complete("execute", "bot", "completed") // the "draft", submitted unchanged
	if e := g.latest("review"); e == nil || e.Status != domain.StepStatusWaiting {
		t.Fatalf("review not waiting: %+v", e)
	}
	return g
}

// AC 1 (regression; the committed form of the review's
// TestGV_SoD_PlanningStarterSelfReview). At the scaffold base the claim
// succeeds, the starter's accept merges and main gains the artifact.
func TestSoD_PlanningStarterCannotReviewOwnContent(t *testing.T) {
	g := startSoDPlanningRun(t)
	ctx := context.Background()
	review := g.latest("review")

	_, claimErr := g.orch.ClaimStep(ctx, ClaimRequest{ActorID: sodStarter, ExecutionID: review.ExecutionID})
	if claimErr == nil {
		_, err := g.orch.IngestResult(ctx, SubmitRequest{ExecutionID: review.ExecutionID, OutcomeID: "accepted", ActorID: sodStarter})
		run, _ := g.store.GetRun(ctx, g.run.RunID)
		t.Fatalf("N-3: the starter claimed the review of its own planning content (accept: err=%v, run %s, main has the artifact: %v)",
			err, run.Status, g.mainFile(sodPlanningPath) != "")
	}
	_, assignErr := g.orch.AssignStep(ctx, AssignRequest{RunID: g.run.RunID, StepID: "review", ActorID: sodStarter})
	for what, err := range map[string]error{"claim": claimErr, "assign": assignErr} {
		requireCode(t, err, domain.ErrForbidden, what+" of the review by the planning starter")
		if !strings.Contains(err.Error(), sodRunStartStep) {
			t.Fatalf("%s refusal %q must name %s", what, err, sodRunStartStep)
		}
	}
	if e := g.latest("review"); e.Status != domain.StepStatusWaiting || e.ActorID != "" {
		t.Fatalf("refused binds changed the review: %+v", e)
	}
	if g.mainFile(sodPlanningPath) != "" {
		t.Fatal("main gained the starter's artifact")
	}
}

// AC 8: a third actor reviews the starter's run, accepts, and the publish
// merge lands the artifact on main.
func TestSoD_IndependentReviewerAccepts(t *testing.T) {
	g := startSoDPlanningRun(t)
	ctx := context.Background()
	review := g.latest("review")
	if _, err := g.orch.ClaimStep(ctx, ClaimRequest{ActorID: "reviewer", ExecutionID: review.ExecutionID}); err != nil {
		t.Fatalf("independent reviewer refused: %v", err)
	}
	if _, err := g.orch.IngestResult(ctx, SubmitRequest{ExecutionID: review.ExecutionID, OutcomeID: "accepted", ActorID: "reviewer"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	run, err := g.store.GetRun(ctx, g.run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunStatusCompleted {
		t.Fatalf("run %s, want completed", run.Status)
	}
	if !strings.Contains(g.mainFile(sodPlanningPath), "INIT-099") {
		t.Fatal("the reviewed artifact did not reach main")
	}
	// The record survived every binding write of the run.
	if got := run.CommitMeta[CommitMetaGovernanceStartedBy]; got != sodStarter {
		t.Fatalf("recorded starter after the merge = %q, want %q", got, sodStarter)
	}
}

// metaCallStore counts commit_meta writes through either path (the atomic
// MergeCommitMeta the engine prefers, and SetCommitMeta), and can fail
// either.
type metaCallStore struct {
	*mockRunStore
	sets, merges int
	setErr       error
	mergeErr     error
	noMerger     bool // hide MergeCommitMeta behind metaFallbackStore
}

func (s *metaCallStore) SetCommitMeta(ctx context.Context, runID string, meta map[string]string) error {
	s.sets++
	if s.setErr != nil {
		return s.setErr
	}
	cp := make(map[string]string, len(meta))
	for k, v := range meta {
		cp[k] = v
	}
	return s.mockRunStore.SetCommitMeta(ctx, runID, cp)
}

func (s *metaCallStore) MergeCommitMeta(_ context.Context, runID string, patch map[string]string) error {
	s.merges++
	if s.mergeErr != nil {
		return s.mergeErr
	}
	r, ok := s.runs[runID]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "run not found")
	}
	meta := make(map[string]string, len(r.CommitMeta)+len(patch))
	for k, v := range r.CommitMeta {
		meta[k] = v
	}
	for k, v := range patch {
		meta[k] = v
	}
	r.CommitMeta = meta
	return nil
}

// metaFallbackStore hides MergeCommitMeta, so the engine takes its
// read-modify-write fallback (GetRun + SetCommitMeta).
type metaFallbackStore struct{ *metaCallStore }

func (s metaFallbackStore) MergeCommitMeta() {} // shadows the capability's signature

// planningStarterKind is one of the three planning starters.
type planningStarterKind struct {
	name  string
	start func(o *Orchestrator, ctx context.Context) (*StartRunResult, error)
	// orch builds an orchestrator for the starter over st and gitOp, and
	// returns a probe reporting how many content writes it saw.
	orch func(st RunStore, gitOp GitOperator) (*Orchestrator, func() int)
}

func planningStarterKinds() []planningStarterKind {
	artifactOrch := func(st RunStore, gitOp GitOperator) (*Orchestrator, func() int) {
		w := &mockArtifactWriter{}
		calls := 0
		o := planningOrchestrator(countingArtifactWriter{w, &calls}, defaultPlanningResolver(), &mockRunStore{}, &mockEventEmitter{}, gitOp)
		o.store = st
		return o, func() int { return calls }
	}
	workflowOrch := func(st RunStore, gitOp GitOperator) (*Orchestrator, func() int) {
		w := &mockWorkflowWriter{}
		o := workflowPlanningOrchestrator(w, workflowPlanningResolver(), &mockRunStore{}, &mockEventEmitter{}, gitOp)
		o.store = st
		return o, func() int { return w.createCalls + w.updateCalls }
	}
	return []planningStarterKind{
		{name: "StartPlanningRun", orch: artifactOrch,
			start: func(o *Orchestrator, ctx context.Context) (*StartRunResult, error) {
				return o.StartPlanningRun(ctx, sodPlanningPath, validArtifactContent)
			}},
		{name: "StartWorkflowPlanningRun", orch: workflowOrch,
			start: func(o *Orchestrator, ctx context.Context) (*StartRunResult, error) {
				return o.StartWorkflowPlanningRun(ctx, "new-workflow", validWorkflowBody)
			}},
		{name: "StartWorkflowUpdatePlanningRun", orch: workflowOrch,
			start: func(o *Orchestrator, ctx context.Context) (*StartRunResult, error) {
				return o.StartWorkflowUpdatePlanningRun(ctx, "new-workflow", bumpedWorkflowBody)
			}},
	}
}

// countingArtifactWriter counts Create calls on an artifact writer.
type countingArtifactWriter struct {
	w     *mockArtifactWriter
	calls *int
}

func (c countingArtifactWriter) Create(ctx context.Context, path, content string) (*artifact.WriteResult, error) {
	*c.calls++
	return c.w.Create(ctx, path, content)
}

// sodGit serves the existing definition the update starter needs and
// records branch creation and deletion.
func sodGit() *workflowReadGit {
	return &workflowReadGit{files: map[string]string{}}
}

func sodGitFor(kind string) *workflowReadGit {
	g := sodGit()
	if kind == "StartWorkflowUpdatePlanningRun" {
		g.files["workflows/new-workflow.yaml"] = validWorkflowBody
	}
	return g
}

func starterCtx(id string) context.Context {
	return domain.WithActor(context.Background(), &domain.Actor{ActorID: id, Type: domain.ActorTypeHuman, Status: domain.ActorStatusActive})
}

// AC 3: under separation of duties (guard off) every planning starter
// records the authenticated starter, through either commit_meta write
// path, and the returned run carries it.
func TestSoD_PlanningStarterRecorded(t *testing.T) {
	for _, k := range planningStarterKinds() {
		for _, fallback := range []bool{false, true} {
			k, fallback := k, fallback
			name := k.name + "/merge"
			if fallback {
				name = k.name + "/read-modify-write"
			}
			t.Run(name, func(t *testing.T) {
				ms := &metaCallStore{mockRunStore: &mockRunStore{}}
				var st RunStore = ms
				if fallback {
					st = metaFallbackStore{ms}
				}
				o, _ := k.orch(st, sodGitFor(k.name))
				o.WithStepEntryPolicy(StepEntryPolicy{SeparationOfDuties: true})
				res, err := k.start(o, starterCtx(sodStarter))
				if err != nil {
					t.Fatalf("start: %v", err)
				}
				stored := ms.runs[res.Run.RunID]
				if got := stored.CommitMeta[CommitMetaGovernanceStartedBy]; got != sodStarter {
					t.Fatalf("stored commit_meta[%s] = %q, want %q", CommitMetaGovernanceStartedBy, got, sodStarter)
				}
				if got := res.Run.CommitMeta[CommitMetaGovernanceStartedBy]; got != sodStarter {
					t.Fatalf("returned run's commit_meta[%s] = %q, want %q", CommitMetaGovernanceStartedBy, got, sodStarter)
				}
				if fallback && (ms.sets != 1 || ms.merges != 0) || !fallback && (ms.merges != 1 || ms.sets != 0) {
					t.Fatalf("commit_meta writes: merges=%d sets=%d", ms.merges, ms.sets)
				}
				if res.Run.Status != domain.RunStatusActive {
					t.Fatalf("run %s, want active", res.Run.Status)
				}
			})
		}
	}
}

// AC 4: with no policy and no guard nothing is recorded — no commit_meta
// write of either kind during the start — and an actor-less start runs as
// before.
func TestSoD_ZeroPolicyWritesNoStarter(t *testing.T) {
	for _, k := range planningStarterKinds() {
		for _, withActor := range []bool{false, true} {
			k, withActor := k, withActor
			t.Run(k.name+map[bool]string{false: "/no actor", true: "/actor"}[withActor], func(t *testing.T) {
				ms := &metaCallStore{mockRunStore: &mockRunStore{}}
				o, writes := k.orch(ms, sodGitFor(k.name))
				ctx := context.Background()
				if withActor {
					ctx = starterCtx(sodStarter)
				}
				res, err := k.start(o, ctx)
				if err != nil {
					t.Fatalf("start: %v", err)
				}
				if ms.sets != 0 || ms.merges != 0 {
					t.Fatalf("zero value wrote commit_meta: sets=%d merges=%d", ms.sets, ms.merges)
				}
				if _, ok := res.Run.CommitMeta[CommitMetaGovernanceStartedBy]; ok {
					t.Fatalf("zero value recorded a starter: %v", res.Run.CommitMeta)
				}
				if res.Run.Status != domain.RunStatusActive || writes() != 1 {
					t.Fatalf("run %s, content writes %d; want active, 1", res.Run.Status, writes())
				}
			})
		}
	}
}

// AC 5: under separation of duties an actor-less start (no actor, or an
// actor with an empty id) is ErrUnauthorized before anything exists: no run
// row, no branch, no git read and no content write.
func TestSoD_PlanningStartWithoutActorRefused(t *testing.T) {
	for _, k := range planningStarterKinds() {
		for name, ctx := range map[string]context.Context{
			"no actor":       context.Background(),
			"empty actor id": domain.WithActor(context.Background(), &domain.Actor{Type: domain.ActorTypeHuman}),
		} {
			k, ctx := k, ctx
			t.Run(k.name+"/"+name, func(t *testing.T) {
				ms := &metaCallStore{mockRunStore: &mockRunStore{}}
				gitOp := sodGitFor(k.name)
				o, writes := k.orch(ms, gitOp)
				o.WithStepEntryPolicy(StrictStepEntryPolicy())
				_, err := k.start(o, ctx)
				requireCode(t, err, domain.ErrUnauthorized, "actor-less planning start")
				if ms.createdRun != nil || len(ms.runs) != 0 {
					t.Fatalf("a run row exists: %+v", ms.createdRun)
				}
				for _, b := range gitOp.created {
					if strings.HasPrefix(b, "spine/plan/") {
						t.Fatalf("a planning branch was created: %v", gitOp.created)
					}
				}
				if len(gitOp.created) != 0 || len(gitOp.readRefs) != 0 || writes() != 0 || ms.sets+ms.merges != 0 {
					t.Fatalf("refused start touched git or the store: created=%v reads=%v writes=%d meta=%d",
						gitOp.created, gitOp.readRefs, writes(), ms.sets+ms.merges)
				}
			})
		}
	}
}

// AC 6: under separation of duties a failed starter record aborts the
// start before any content is written: the error is returned, the branch
// is deleted, the content writer is never called, and the pending row is
// marked failed (not left for recovery to activate). Both write paths.
func TestSoD_PlanningStarterRecordFailureAbortsStart(t *testing.T) {
	boom := errors.New("commit_meta write failed")
	for _, k := range planningStarterKinds() {
		for _, fallback := range []bool{false, true} {
			k, fallback := k, fallback
			t.Run(k.name+map[bool]string{false: "/merge", true: "/read-modify-write"}[fallback], func(t *testing.T) {
				ms := &metaCallStore{mockRunStore: &mockRunStore{}}
				var st RunStore = ms
				if fallback {
					ms.setErr = boom
					st = metaFallbackStore{ms}
				} else {
					ms.mergeErr = boom
				}
				gitOp := sodGitFor(k.name)
				o, writes := k.orch(st, gitOp)
				o.WithStepEntryPolicy(StepEntryPolicy{SeparationOfDuties: true})
				_, err := k.start(o, starterCtx(sodStarter))
				if !errors.Is(err, boom) {
					t.Fatalf("start error = %v, want the record failure", err)
				}
				if writes() != 0 {
					t.Fatalf("content written %d times after the record failed", writes())
				}
				if len(gitOp.created) != 1 || len(gitOp.deleted) != 1 || gitOp.deleted[0] != gitOp.created[0] {
					t.Fatalf("branch not cleaned up: created=%v deleted=%v", gitOp.created, gitOp.deleted)
				}
				if ms.createdRun == nil || ms.createdRun.Status != domain.RunStatusFailed {
					t.Fatalf("run row after a failed record: %+v, want failed", ms.createdRun)
				}
				if len(ms.createdSteps) != 0 {
					t.Fatalf("entry step created after a failed record: %d", len(ms.createdSteps))
				}
			})
		}
	}
}

// planningSoDFixture is newSoDFixture over a planning-mode run with meta as
// its commit_meta: bob completed execute, the review waits.
func planningSoDFixture(p StepEntryPolicy, meta map[string]string) sodFixture {
	f := newSoDFixture(p,
		sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "bob"),
		sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""))
	r := f.st.runs["run-1"]
	r.Mode, r.CommitMeta = domain.RunModePlanning, meta
	return f
}

// AC 7: a planning run with no starter record (started before the upgrade,
// or while separation of duties was off) refuses every candidate for its
// review, through assign and claim — the drafter, an independent actor and
// the (unknown) starter alike. Decision 2: fail closed.
func TestSoD_PlanningRunWithoutStarterRecordRefusesReview(t *testing.T) {
	ctx := context.Background()
	for name, meta := range map[string]map[string]string{
		"no commit_meta":  nil,
		"no starter key":  {"merge": "true"},
		"empty starter":   {CommitMetaGovernanceStartedBy: ""},
		"binding keys on": {CommitMetaBoundBy: "step:execute:run-1-execute-1"},
	} {
		for _, actorID := range []string{"bob", "alice", "dana"} {
			meta, actorID := meta, actorID
			t.Run(name+"/"+actorID, func(t *testing.T) {
				f := planningSoDFixture(StepEntryPolicy{SeparationOfDuties: true}, meta)
				before := snapshotExecs(f.st)
				_, aErr := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: actorID})
				_, cErr := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: actorID, ExecutionID: "run-1-review-1"})
				for what, err := range map[string]error{"assign": aErr, "claim": cErr} {
					requireCode(t, err, domain.ErrForbidden, what+" of an unrecorded planning run's review")
					if !strings.Contains(err.Error(), sodStarterUnknown) {
						t.Fatalf("%s refusal %q must name %s", what, err, sodStarterUnknown)
					}
				}
				requireExecsUnchanged(t, f.st, before, "refused binds")
			})
		}
	}
	t.Run("engine auto-selection leaves the review waiting", func(t *testing.T) {
		f := newSoDFixture(StepEntryPolicy{SeparationOfDuties: true},
			sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "bob"),
			sodExec("run-1-ai-review-1", "ai-review", domain.StepStatusWaiting, ""))
		f.st.runs["run-1"].Mode = domain.RunModePlanning
		f.orch.actorSelector = &mockActorSelector{actor: activeActor("agent-2", domain.ActorTypeAIAgent)}
		if err := f.orch.ActivateStep(ctx, "run-1", "ai-review"); err != nil {
			t.Fatalf("ActivateStep: %v", err)
		}
		if e := execByID(f.st, "run-1-ai-review-1"); e.ActorID != "" || e.Status != domain.StepStatusWaiting || len(f.gw.delivered) != 0 {
			t.Fatalf("auto-selection bound an unrecorded planning run's review: %+v deliveries=%d", e, len(f.gw.delivered))
		}
	})
	t.Run("the zero value still binds, as before", func(t *testing.T) {
		f := planningSoDFixture(StepEntryPolicy{}, nil)
		if _, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "dana", ExecutionID: "run-1-review-1"}); err != nil {
			t.Fatalf("zero value refused a claim: %v", err)
		}
	})
}

// The recorded starter is refused on every binding path (assign, claim and
// the engine's auto-selection) while a different actor binds; the compare
// is exact, so a spelling variant of the starter is not the starter (it is
// a different actor id, which the engine's actor lookup must resolve on its
// own — StepEntryPolicy.Actors).
func TestSoD_RecordedStarterRefusedOnEveryBindPath(t *testing.T) {
	ctx := context.Background()
	meta := map[string]string{CommitMetaGovernanceStartedBy: "alice"}
	t.Run("assign and claim", func(t *testing.T) {
		f := planningSoDFixture(StepEntryPolicy{SeparationOfDuties: true}, meta)
		_, aErr := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: "alice"})
		_, cErr := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "alice", ExecutionID: "run-1-review-1"})
		for what, err := range map[string]error{"assign": aErr, "claim": cErr} {
			requireCode(t, err, domain.ErrForbidden, what+" of the starter")
			if !strings.Contains(err.Error(), sodRunStartStep) || strings.Contains(err.Error(), sodStarterUnknown) {
				t.Fatalf("%s refusal %q must name %s", what, err, sodRunStartStep)
			}
		}
		// The drafter is still refused through its execution.
		_, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "bob", ExecutionID: "run-1-review-1"})
		requireCode(t, err, domain.ErrForbidden, "claim of the drafter")
		if !strings.Contains(err.Error(), `"execute"`) {
			t.Fatalf("drafter refusal %q must name execute", err)
		}
	})
	for _, variant := range []string{"Alice", "alice ", " alice"} {
		variant := variant
		t.Run("variant "+variant+" is another actor id", func(t *testing.T) {
			f := planningSoDFixture(StepEntryPolicy{SeparationOfDuties: true}, meta)
			if _, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: variant, ExecutionID: "run-1-review-1"}); err != nil {
				t.Fatalf("separation of duties is an exact compare; %q: %v", variant, err)
			}
		})
	}
	t.Run("strict policy: a variant does not resolve", func(t *testing.T) {
		f := planningSoDFixture(StrictStepEntryPolicy(), meta)
		bs := newClaimBlockingStore()
		bs.actors["alice"] = &domain.Actor{ActorID: "alice", Type: domain.ActorTypeHuman, Status: domain.ActorStatusActive}
		f.orch.blocking = bs
		for _, variant := range []string{"Alice", "alice "} {
			_, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: variant})
			requireCode(t, err, domain.ErrNotFound, "assign of a spelling variant under the strict policy")
		}
	})
	for _, tc := range []struct{ selected, want string }{{"alice", ""}, {"agent-2", "agent-2"}} {
		tc := tc
		t.Run("auto-selection of "+tc.selected, func(t *testing.T) {
			f := newSoDFixture(StepEntryPolicy{SeparationOfDuties: true},
				sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "bob"),
				sodExec("run-1-ai-review-1", "ai-review", domain.StepStatusWaiting, ""))
			r := f.st.runs["run-1"]
			r.Mode, r.CommitMeta = domain.RunModePlanning, meta
			f.orch.actorSelector = &mockActorSelector{actor: activeActor(tc.selected, domain.ActorTypeAIAgent)}
			if err := f.orch.ActivateStep(ctx, "run-1", "ai-review"); err != nil {
				t.Fatalf("ActivateStep: %v", err)
			}
			if e := execByID(f.st, "run-1-ai-review-1"); e.ActorID != tc.want {
				t.Fatalf("auto-selected review actor = %q, want %q", e.ActorID, tc.want)
			}
		})
	}
}

// AC 9: the pull-model listing hides a planning run's waiting review from
// its starter and offers it to an independent actor; an unrecorded planning
// run's review is offered to nobody.
func TestSoD_CandidateListingHidesPlanningReviewFromStarter(t *testing.T) {
	ctx := context.Background()
	newOrch := func(meta map[string]string) *Orchestrator {
		run := runFixture("run-1", domain.RunStatusActive)
		run.Mode, run.CommitMeta = domain.RunModePlanning, meta
		st := &mockRunStore{runs: map[string]*domain.Run{"run-1": run}}
		st.createdSteps = []*domain.StepExecution{
			sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "bot"),
			sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""),
		}
		bs := &stepListingBlockingStore{runs: st.runs, execs: []domain.StepExecution{*st.createdSteps[1]}}
		o := newStepListingOrch(bs, sodWorkflow())
		o.store = st
		o.assignments = newMemAssignmentStore()
		o.WithStepEntryPolicy(StepEntryPolicy{SeparationOfDuties: true})
		return o
	}
	cases := []struct {
		name  string
		meta  map[string]string
		actor string
		want  string
	}{
		{"starter is not offered its run's review", map[string]string{CommitMetaGovernanceStartedBy: sodStarter}, sodStarter, ""},
		{"drafter is not offered it", map[string]string{CommitMetaGovernanceStartedBy: sodStarter}, "bot", ""},
		{"independent actor is offered it", map[string]string{CommitMetaGovernanceStartedBy: sodStarter}, "reviewer", "run-1-review-1"},
		{"unrecorded run: offered to nobody", nil, "reviewer", ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			items, err := newOrch(c.meta).ListStepExecutions(ctx, StepExecutionQuery{ActorID: c.actor, Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, it := range items {
				got = append(got, it.ExecutionID)
			}
			sort.Strings(got)
			if strings.Join(got, ",") != c.want {
				t.Fatalf("listing for %s = %v, want [%s]", c.actor, got, c.want)
			}
		})
	}
}

// AC 10: standard runs are unchanged. StartRun records nothing, and the
// actor who started a standard run (its starter wrote no content) may
// review it as before — even if its commit_meta somehow named them.
func TestSoD_StandardRunStarterIsNotAnAuthor(t *testing.T) {
	ctx := context.Background()
	t.Run("StartRun records no starter", func(t *testing.T) {
		ms := &metaCallStore{mockRunStore: &mockRunStore{}}
		resolver := &mockWorkflowResolver{result: &workflow.BindingResult{
			Workflow: &domain.WorkflowDefinition{ID: "wf-task", Path: "workflows/task.yaml", Version: "1.0.0", EntryStep: "start",
				Steps: []domain.StepDefinition{{ID: "start", Name: "Start"}}},
			CommitSHA: "abc123", VersionLabel: "1.0.0",
		}}
		o := testOrchestrator(&mockArtifactReader{artifact: &domain.Artifact{Type: "task", Path: "tasks/my-task.md"}}, resolver, ms.mockRunStore, &mockEventEmitter{})
		o.store = ms
		o.WithStepEntryPolicy(StepEntryPolicy{SeparationOfDuties: true})
		res, err := o.StartRun(starterCtx(sodStarter), "tasks/my-task.md")
		if err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		if ms.sets+ms.merges != 0 || res.Run.CommitMeta[CommitMetaGovernanceStartedBy] != "" {
			t.Fatalf("a standard run recorded a starter: meta writes=%d commit_meta=%v", ms.sets+ms.merges, res.Run.CommitMeta)
		}
		if _, err := o.StartRun(context.Background(), "tasks/my-task.md"); err != nil {
			t.Fatalf("an actor-less standard start is refused: %v", err)
		}
	})
	for name, meta := range map[string]map[string]string{
		"no record":         nil,
		"a stray record":    {CommitMetaGovernanceStartedBy: sodStarter},
		"standard mode set": nil,
	} {
		meta, name := meta, name
		t.Run("the starter reviews: "+name, func(t *testing.T) {
			f := newSoDFixture(StepEntryPolicy{SeparationOfDuties: true},
				sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "bob"),
				sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""))
			r := f.st.runs["run-1"]
			r.CommitMeta = meta
			if name == "standard mode set" {
				r.Mode = domain.RunModeStandard
			}
			if _, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: sodStarter}); err != nil {
				t.Fatalf("the starter of a standard run was refused its review: %v", err)
			}
		})
	}
}

// Under the guard and the lifecycle exemption alone (separation of duties
// off) the record keeps its pre-INIT-024 behaviour for every starter: no
// actor records nothing and starts, a failed write is logged and the run
// starts (fail toward an operator approval, not toward a refused start).
func TestPlanningStarterRecord_GuardOnlyKeepsBestEffort(t *testing.T) {
	exemptGuard := &governanceGuard{cfg: GovernanceGuard{Enabled: true, ExemptWorkflowLifecycleTaskPath: true}}
	for _, k := range planningStarterKinds() {
		k := k
		t.Run(k.name+"/no actor", func(t *testing.T) {
			ms := &metaCallStore{mockRunStore: &mockRunStore{}}
			o, writes := k.orch(ms, sodGitFor(k.name))
			o.governance = exemptGuard
			res, err := k.start(o, context.Background())
			if err != nil || res.Run.Status != domain.RunStatusActive || writes() != 1 || ms.sets+ms.merges != 0 {
				t.Fatalf("start: err=%v meta writes=%d content writes=%d", err, ms.sets+ms.merges, writes())
			}
		})
		t.Run(k.name+"/failed write", func(t *testing.T) {
			ms := &metaCallStore{mockRunStore: &mockRunStore{}, mergeErr: errors.New("boom")}
			o, writes := k.orch(ms, sodGitFor(k.name))
			o.governance = exemptGuard
			res, err := k.start(o, starterCtx(sodStarter))
			if err != nil || res.Run.Status != domain.RunStatusActive || writes() != 1 {
				t.Fatalf("start: err=%v content writes=%d", err, writes())
			}
			if _, ok := res.Run.CommitMeta[CommitMetaGovernanceStartedBy]; ok {
				t.Fatalf("a failed write was mirrored into the run: %v", res.Run.CommitMeta)
			}
		})
		t.Run(k.name+"/recorded", func(t *testing.T) {
			ms := &metaCallStore{mockRunStore: &mockRunStore{}}
			o, _ := k.orch(ms, sodGitFor(k.name))
			o.governance = exemptGuard
			res, err := k.start(o, starterCtx(sodStarter))
			if err != nil || ms.runs[res.Run.RunID].CommitMeta[CommitMetaGovernanceStartedBy] != sodStarter {
				t.Fatalf("start: err=%v stored=%v", err, ms.runs[res.Run.RunID].CommitMeta)
			}
		})
	}
}

// The record precedes the content: when the content write runs, the
// starter is already on the stored run (no window in which content exists
// on a run branch without its author on record).
func TestPlanningStarterRecord_BeforeContent(t *testing.T) {
	ms := &metaCallStore{mockRunStore: &mockRunStore{}}
	var seen string
	w := &probeArtifactWriter{probe: func() {
		if ms.createdRun != nil {
			seen = ms.runs[ms.createdRun.RunID].CommitMeta[CommitMetaGovernanceStartedBy]
		}
	}}
	o := planningOrchestrator(w, defaultPlanningResolver(), ms.mockRunStore, &mockEventEmitter{}, sodGit())
	o.store = ms
	o.WithStepEntryPolicy(StepEntryPolicy{SeparationOfDuties: true})
	if _, err := o.StartPlanningRun(starterCtx(sodStarter), sodPlanningPath, validArtifactContent); err != nil {
		t.Fatal(err)
	}
	if seen != sodStarter {
		t.Fatalf("starter on record when the content was written = %q, want %q", seen, sodStarter)
	}

	ms2 := &metaCallStore{mockRunStore: &mockRunStore{}}
	ww := &probeWorkflowWriter{probe: func() {
		if ms2.createdRun != nil {
			seen = ms2.runs[ms2.createdRun.RunID].CommitMeta[CommitMetaGovernanceStartedBy]
		}
	}}
	seen = ""
	o2 := workflowPlanningOrchestrator(ww, workflowPlanningResolver(), ms2.mockRunStore, &mockEventEmitter{}, sodGit())
	o2.store = ms2
	o2.WithStepEntryPolicy(StepEntryPolicy{SeparationOfDuties: true})
	if _, err := o2.StartWorkflowPlanningRun(starterCtx(sodStarter), "new-workflow", validWorkflowBody); err != nil {
		t.Fatal(err)
	}
	if seen != sodStarter {
		t.Fatalf("starter on record when the workflow body was written = %q, want %q", seen, sodStarter)
	}
}

type probeArtifactWriter struct{ probe func() }

func (p *probeArtifactWriter) Create(context.Context, string, string) (*artifact.WriteResult, error) {
	p.probe()
	return &artifact.WriteResult{}, nil
}

type probeWorkflowWriter struct{ probe func() }

func (p *probeWorkflowWriter) Create(context.Context, string, string) (*workflow.WriteResult, error) {
	p.probe()
	return &workflow.WriteResult{}, nil
}

func (p *probeWorkflowWriter) Update(context.Context, string, string) (*workflow.WriteResult, error) {
	p.probe()
	return &workflow.WriteResult{}, nil
}

// With merge binding off, a commit-carrying outcome replaces commit_meta;
// the starter record survives it, so separation of duties keeps counting
// the starter (and an independent reviewer is not locked out as
// "unrecorded"). Without a record nothing is added (the zero value).
func TestMergeCommitMeta_BindingOffKeepsPlanningStarter(t *testing.T) {
	current := map[string]string{CommitMetaGovernanceStartedBy: sodStarter, CommitMetaBoundBy: "step:x:y", "old": "1"}
	got, write := mergeCommitMeta(current, map[string]string{"status": "Draft"}, true, false, nil)
	if !write || got[CommitMetaGovernanceStartedBy] != sodStarter || got["status"] != "Draft" || len(got) != 2 {
		t.Fatalf("binding off: %v (write=%v), want status and the starter only", got, write)
	}
	got, _ = mergeCommitMeta(map[string]string{"old": "1"}, map[string]string{"status": "Draft"}, true, false, nil)
	if _, ok := got[CommitMetaGovernanceStartedBy]; ok || len(got) != 1 {
		t.Fatalf("binding off without a record: %v", got)
	}
	if _, write := mergeCommitMeta(current, nil, false, false, nil); write {
		t.Fatal("binding off without a commit must not write")
	}
}

// Every planning starter's record is the one separation of duties reads:
// the run each starter returns (its Mode and recorded commit_meta), put in
// front of a waiting review that another actor drafted, refuses its
// starter on assign and claim and admits an independent reviewer.
func TestSoD_EveryPlanningStarterIsRefusedItsReview(t *testing.T) {
	ctx := context.Background()
	for _, k := range planningStarterKinds() {
		k := k
		t.Run(k.name, func(t *testing.T) {
			ms := &metaCallStore{mockRunStore: &mockRunStore{}}
			o, _ := k.orch(ms, sodGitFor(k.name))
			o.WithStepEntryPolicy(StepEntryPolicy{SeparationOfDuties: true})
			res, err := k.start(o, starterCtx(sodStarter))
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			stored := ms.runs[res.Run.RunID]
			f := planningSoDFixture(StepEntryPolicy{SeparationOfDuties: true}, stored.CommitMeta)
			if f.st.runs["run-1"].Mode != stored.Mode {
				t.Fatalf("started run mode %q, want planning", stored.Mode)
			}
			_, aErr := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: sodStarter})
			_, cErr := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: sodStarter, ExecutionID: "run-1-review-1"})
			for what, err := range map[string]error{"assign": aErr, "claim": cErr} {
				requireCode(t, err, domain.ErrForbidden, what+" of the review by "+k.name+"'s starter")
				if !strings.Contains(err.Error(), sodRunStartStep) {
					t.Fatalf("%s refusal %q must name %s", what, err, sodRunStartStep)
				}
			}
			if _, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "dana", ExecutionID: "run-1-review-1"}); err != nil {
				t.Fatalf("an independent reviewer was refused: %v", err)
			}
		})
	}
}
