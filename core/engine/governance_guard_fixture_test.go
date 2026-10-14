package engine

// Fixtures for the INIT-023 TASK-006 governance-path guard tests: a real
// git repository (git.CLIClient) with a run branch, an in-memory store
// that counts step writes and can offer the atomic commit_meta merge, and
// a driver that completes steps the way a runner would (AssignStep, then
// IngestResult), so the publish step's merge runs through the real
// handler with merge binding on.

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/testutil"
)

const (
	govRunID       = "run-gov12345"
	govTrace       = "trace-0123456789abcdef"
	govProtected   = "version: 1\nrules:\n  - branch: main\n    protections: [no-delete, no-direct-write]\n"
	govRelaxed     = "version: 1\nrules: []\n"
	govTaskPending = "---\nid: TASK-001\ntype: Task\ntitle: T\nstatus: Pending\n---\n# T\n"
	govTaskDefault = "id: task-default\nname: Default\nversion: \"8.0\"\nstatus: Active\napplies_to: [Task]\nentry_step: execute\nsteps:\n  - id: execute\n  - id: review\n  - id: publish\n"
	govReviewLess  = "id: task-default\nname: Default\nversion: \"8.1\"\nstatus: Active\napplies_to: [Task]\nentry_step: execute\nsteps:\n  - id: execute\n  - id: publish\n"
)

// govPublishWF is task-default's shape: execute → review → publish
// (internal merge handler), merge_failed back to execute.
func govPublishWF() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "task-default", Path: "workflows/task-default.yaml", EntryStep: "execute",
		AppliesTo: []string{"Task"},
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "completed", Name: "Done", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Accepted", NextStep: "publish"},
					{ID: "needs_rework", Name: "Rework", NextStep: "execute"},
				}},
			{ID: "publish", Name: "Publish", Type: domain.StepTypeInternal,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly, Handler: "merge"},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "published", Name: "Published", NextStep: "end"},
					{ID: "merge_failed", Name: "Merge failed", NextStep: "execute"},
				}},
		},
	}
}

// govLegacyWF commits on the review's terminal outcome (no publish step),
// the artifact-creation / workflow-lifecycle shape.
func govLegacyWF() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "artifact-creation", Path: "workflows/artifact-creation.yaml", EntryStep: "execute",
		AppliesTo: []string{"Task"},
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "completed", Name: "Done", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Accepted", NextStep: "end", Commit: map[string]string{"merge": "true"}},
					{ID: "needs_rework", Name: "Rework", NextStep: "execute"},
				}},
		},
	}
}

// govLifecycleWF is workflow-lifecycle's shape: draft → review, the
// review's approval commits; it governs Workflow artifacts.
func govLifecycleWF() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "workflow-lifecycle", Path: "workflows/workflow-lifecycle.yaml", EntryStep: "draft",
		AppliesTo: []string{"Workflow"}, Mode: "creation",
		Steps: []domain.StepDefinition{
			{ID: "draft", Name: "Draft", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "submitted", Name: "Submitted", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "approved", Name: "Approved", NextStep: "end", Commit: map[string]string{"merge": "true"}},
					{ID: "needs_rework", Name: "Rework", NextStep: "draft"},
				}},
		},
	}
}

// govStore is mockRunStore that counts step-execution writes and records
// every governance hold write.
type govStore struct {
	*mockRunStore
	mu          sync.Mutex
	stepWrites  int
	holdWrites  int
	setMetaErr  error
	beforeWrite func(kind string) // called once per commit_meta write, before it applies
}

func newGovStore(run *domain.Run, steps ...*domain.StepExecution) *govStore {
	return &govStore{mockRunStore: &mockRunStore{runs: map[string]*domain.Run{run.RunID: run}, createdSteps: steps}}
}

func (s *govStore) UpdateStepExecution(ctx context.Context, e *domain.StepExecution) error {
	s.mu.Lock()
	s.stepWrites++
	if e.ErrorDetail != nil && e.ErrorDetail.Classification == domain.FailureGovernanceApprovalRequired {
		s.holdWrites++
	}
	s.mu.Unlock()
	return s.mockRunStore.UpdateStepExecution(ctx, e)
}

func (s *govStore) SetCommitMeta(ctx context.Context, runID string, meta map[string]string) error {
	if s.beforeWrite != nil {
		s.beforeWrite("set")
	}
	if s.setMetaErr != nil {
		return s.setMetaErr
	}
	cp := make(map[string]string, len(meta))
	for k, v := range meta {
		cp[k] = v
	}
	return s.mockRunStore.SetCommitMeta(ctx, runID, cp)
}

// govMergerStore adds the atomic MergeCommitMeta capability.
type govMergerStore struct {
	*govStore
	mergeErr   error
	mergeCalls []map[string]string
}

func (s *govMergerStore) MergeCommitMeta(_ context.Context, runID string, patch map[string]string) error {
	if s.beforeWrite != nil {
		s.beforeWrite("merge")
	}
	s.mergeCalls = append(s.mergeCalls, patch)
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

type staticRules []config.Rule

func (s staticRules) Rules(context.Context) ([]config.Rule, error) { return s, nil }

type govRepoOpts struct {
	wf        *domain.WorkflowDefinition // nil: govPublishWF
	mainFiles map[string]string          // nil: protected config, task-default, the task
	runFiles  map[string]string          // committed on the run branch before the run starts
	guard     *GovernanceGuard           // nil: guard off
	noBinding bool
	merger    bool                 // the store implements MergeCommitMeta
	policy    branchprotect.Policy // nil: the real policy over main's protected rule
	wrap      func(*git.CLIClient) GitOperator
	tweak     func(*domain.Run)
}

// govRepo is a real repository with one active run, its first step
// waiting.
type govRepo struct {
	t      *testing.T
	dir    string
	cli    *git.CLIClient
	store  *govStore
	merger *govMergerStore
	events *mockEventEmitter
	orch   *Orchestrator
	run    *domain.Run
	branch string
	wf     *domain.WorkflowDefinition
}

func defaultGuard() *GovernanceGuard {
	return &GovernanceGuard{Enabled: true, ExemptOwnRunEvidence: true}
}

func newGovRepo(t *testing.T, o govRepoOpts) *govRepo {
	t.Helper()
	testutil.HermeticGit(t)
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")
	wf := o.wf
	if wf == nil {
		wf = govPublishWF()
	}
	dir := testutil.NewTempRepo(t)
	g := &govRepo{t: t, dir: dir, cli: git.NewCLIClient(dir), wf: wf, branch: "spine/run/" + govRunID}
	main := o.mainFiles
	if main == nil {
		main = map[string]string{
			".spine/branch-protection.yaml": govProtected,
			"workflows/task-default.yaml":   govTaskDefault,
			"tasks/TASK-001.md":             govTaskPending,
		}
	}
	g.writeCommit(main, "seed governed main")
	g.git("checkout", "-q", "-b", g.branch)
	if len(o.runFiles) > 0 {
		g.writeCommit(o.runFiles, "run work")
	}
	g.git("checkout", "-q", "main")

	run := &domain.Run{
		RunID: govRunID, TraceID: govTrace, Status: domain.RunStatusActive, Mode: domain.RunModeStandard,
		TaskPath: "tasks/TASK-001.md", WorkflowPath: wf.Path, WorkflowID: wf.ID, WorkflowVersion: "v1",
		CurrentStepID: wf.EntryStep, BranchName: g.branch,
	}
	if o.tweak != nil {
		o.tweak(run)
	}
	g.run = run
	first := &domain.StepExecution{ExecutionID: govRunID + "-" + wf.EntryStep + "-1", RunID: govRunID,
		StepID: wf.EntryStep, Status: domain.StepStatusWaiting, Attempt: 1}
	g.store = newGovStore(run, first)
	var st RunStore = g.store
	if o.merger {
		g.merger = &govMergerStore{govStore: g.store}
		st = g.merger
	}
	var op GitOperator = g.cli
	if o.wrap != nil {
		op = o.wrap(g.cli)
	}
	pol := o.policy
	if pol == nil {
		pol = branchprotect.New(staticRules{{Branch: "main",
			Protections: []config.RuleKind{config.KindNoDelete, config.KindNoDirectWrite}}})
	}
	g.events = &mockEventEmitter{}
	g.orch = &Orchestrator{
		workflows: &mockWorkflowResolver{}, store: st, actors: &mockActorAssigner{},
		artifacts: &mockArtifactReader{}, events: g.events, git: op,
		wfLoader: &mockWorkflowLoader{wfDef: wf}, policy: pol,
	}
	if !o.noBinding {
		if err := g.orch.EnableMergeBinding(MergeBindingConfig{}); err != nil {
			t.Fatalf("EnableMergeBinding: %v", err)
		}
	}
	if o.guard != nil {
		if err := g.orch.WithGovernanceGuard(*o.guard); err != nil {
			t.Fatalf("WithGovernanceGuard: %v", err)
		}
	}
	return g
}

func (g *govRepo) git(args ...string) string {
	g.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", g.dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g *govRepo) writeCommit(files map[string]string, msg string) {
	g.t.Helper()
	for p, c := range files {
		if c == "" {
			g.git("rm", "-q", "--", p)
			continue
		}
		testutil.WriteFile(g.t, g.dir, p, c)
	}
	g.git("add", "-A")
	g.git("commit", "-q", "--allow-empty", "-m", msg)
}

// commitOnRun commits files on the run branch (as a runner push would) and
// returns the new head; "" content deletes a file.
func (g *govRepo) commitOnRun(files map[string]string) string {
	g.t.Helper()
	g.git("checkout", "-q", g.branch)
	g.writeCommit(files, "run work")
	head := g.git("rev-parse", "HEAD")
	g.git("checkout", "-q", "main")
	return head
}

func (g *govRepo) runHead() string  { return g.git("rev-parse", "refs/heads/"+g.branch) }
func (g *govRepo) mainHead() string { return g.git("rev-parse", "refs/heads/main") }

func (g *govRepo) mainFile(path string) string {
	g.t.Helper()
	cmd := exec.Command("git", "-C", g.dir, "show", "refs/heads/main:"+path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

func (g *govRepo) workingTreeClean() bool {
	return g.git("status", "--porcelain") == ""
}

// latest returns the newest execution of stepID.
func (g *govRepo) latest(stepID string) *domain.StepExecution {
	var out *domain.StepExecution
	for _, e := range g.store.createdSteps {
		if e.StepID == stepID && (out == nil || e.Attempt > out.Attempt) {
			out = e
		}
	}
	return out
}

// complete assigns stepID's newest execution to actor and submits outcome.
func (g *govRepo) complete(stepID, actor, outcome string) {
	g.t.Helper()
	ctx := context.Background()
	if _, err := g.orch.AssignStep(ctx, AssignRequest{RunID: g.run.RunID, StepID: stepID, ActorID: actor}); err != nil {
		g.t.Fatalf("assign %s to %s: %v", stepID, actor, err)
	}
	e := g.latest(stepID)
	if _, err := g.orch.IngestResult(ctx, SubmitRequest{ExecutionID: e.ExecutionID, OutcomeID: outcome}); err != nil {
		g.t.Fatalf("submit %s/%s: %v", stepID, outcome, err)
	}
}

// toMerge drives execute (author) and review (reviewer, accepted).
func (g *govRepo) toMerge() {
	g.t.Helper()
	first := g.wf.EntryStep
	next := "completed"
	accept := "accepted"
	if first == "draft" {
		next, accept = "submitted", "approved"
	}
	g.complete(first, "author", next)
	g.complete("review", "reviewer", accept)
}

func (g *govRepo) eventsOf(t domain.EventType) []domain.Event {
	var out []domain.Event
	for _, e := range g.events.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

func (g *govRepo) decide(d GovernanceDecision, sha string, actor domain.Actor, reason string) (*GovernanceDecisionResult, error) {
	return g.orch.DecideGovernanceMerge(context.Background(), GovernanceDecisionRequest{
		RunID: g.run.RunID, CommitSHA: sha, Decision: d, Actor: actor, Reason: reason,
	})
}

func (g *govRepo) primaryOutcome() *domain.RepositoryMergeOutcome {
	for i := range g.store.mergeOutcomes {
		if g.store.mergeOutcomes[i].RepositoryID == domain.PrimaryRepositoryID {
			return &g.store.mergeOutcomes[i]
		}
	}
	return nil
}

var (
	govOperator = domain.Actor{ActorID: "op-1", Role: domain.RoleOperator}
	govAdmin    = domain.Actor{ActorID: "admin-1", Role: domain.RoleAdmin}
)

func requireSpineCode(t *testing.T, err error, code domain.ErrorCode) {
	t.Helper()
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func governanceKeys(meta map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range meta {
		if strings.HasPrefix(k, domain.CommitMetaGovernanceApprovalPrefix) {
			out[k] = v
		}
	}
	return out
}

// singleBaseCLI hides MergeBases' other bases: it answers with the one
// merge base `git merge-base` prints (the design's first computation).
type singleBaseCLI struct{ *git.CLIClient }

func (c singleBaseCLI) MergeBases(ctx context.Context, a, b string) ([]string, error) {
	base, err := c.MergeBase(ctx, a, b)
	if err != nil {
		return nil, err
	}
	return []string{base}, nil
}
