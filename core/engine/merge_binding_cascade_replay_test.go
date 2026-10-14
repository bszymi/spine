package engine_test

// INIT-023 wave 3 review (P2, TASK-005): the binding-on status cascade's
// replay check (cascadeBaseAt) took any merge on main's first-parent chain
// whose second parent was the bound commit, or a child of it, for this
// run's own earlier merge. A run whose branch was reset onto an earlier
// run's merged bound commit B0 (the bound actor may force-push its branch;
// TASK-016) then diffed the cascade against main before that run's merge
// and rewrote the status of every artifact the earlier run added, although
// its own reviewed diff (main...run) was empty. Real git.CLIClient on a
// temporary repository, binding on; the binding-off control shows the
// same main content.

import (
	"context"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
)

// secondRun starts another run on e's repository (tasks/t.md again; the
// first run is completed) under its own orchestrator and store, with
// merge binding on or off.
type secondRun struct {
	orch   *engine.Orchestrator
	store  *outcomeStore
	actors *mockActorGateway
	run    *domain.Run
}

func startSecondRun(t *testing.T, e *regEnv, wf *domain.WorkflowDefinition, bindingOn bool) *secondRun {
	t.Helper()
	r := &secondRun{store: newOutcomeStore(), actors: &mockActorGateway{}}
	orch, err := engine.New(&memWorkflowResolver{wfDef: wf}, r.store, r.actors,
		&memArtifactReader{artifacts: map[string]*domain.Artifact{
			"tasks/t.md": {Path: "tasks/t.md", ID: "TASK-001", Type: "Task", Status: "Pending"},
		}},
		&memEventEmitter{}, &flakyCLI{CLIClient: git.NewCLIClient(e.dir)}, &memWorkflowLoader{wfDef: wf})
	if err != nil {
		t.Fatal(err)
	}
	orch.WithBranchProtectPolicy(branchprotect.New(branchprotect.StaticRules(branchprotect.BootstrapDefaults())))
	orch.WithActorSelector(&fixedActorSelector{actor: &domain.Actor{ActorID: "bot-1", Type: domain.ActorTypeAutomated, Status: domain.ActorStatusActive}})
	if bindingOn {
		if err := orch.EnableMergeBinding(engine.MergeBindingConfig{}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := orch.StartRun(context.Background(), "tasks/t.md")
	if err != nil {
		t.Fatalf("StartRun (second run): %v", err)
	}
	r.orch, r.run = orch, res.Run
	return r
}

func (r *secondRun) submit(t *testing.T, i int, outcome string) {
	t.Helper()
	if err := r.orch.SubmitStepResult(context.Background(), r.actors.assignments[i].AssignmentID, engine.StepResult{OutcomeID: outcome}); err != nil {
		t.Fatalf("second run, assignment %d (%s): %v", i, outcome, err)
	}
}

func (r *secondRun) runNow(t *testing.T) *domain.Run {
	t.Helper()
	got, err := r.store.GetRun(context.Background(), r.run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func frontMatterStatus(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if v, ok := strings.CutPrefix(line, "status: "); ok {
			return v
		}
	}
	return ""
}

func TestMergeBindingRegression_ReplayCheckIgnoresAnotherRunsMerge(t *testing.T) {
	const childPath = "docs/c.md"
	child := func(status string) string {
		return "---\nid: EPIC-009\ntype: Epic\ntitle: C\nstatus: " + status + "\n---\n# C\n"
	}
	for _, tc := range []struct {
		name        string
		childStatus string // the child artifact as the first run adds it
		bindingOff  bool   // control: the second run with binding off
	}{
		// The first run's commit status (Pending) already matches: its
		// merge merged its bound commit B0 itself, [main0, B0].
		{name: "the first run merged its bound commit", childStatus: "Pending"},
		{name: "the first run merged its bound commit, binding off control", childStatus: "Pending", bindingOff: true},
		// The child needs a rewrite: the first run's merge merged its
		// cascade commit C0, [main0, C0], C0 a child of B0. Before the fix
		// this run took it for its own and its cascade conflicted with C0.
		{name: "the first run merged a cascade commit", childStatus: "Draft"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The first run R0 (commit status Pending) adds docs/c.md.
			wf0 := reviewWorkflow()
			wf0.Steps[1].Outcomes[0].Commit = map[string]string{"status": "Pending"}
			e := newRegEnv(t, wf0, regOpts{})
			e.commitOnRun("r0 work", map[string]string{childPath: child(tc.childStatus)})
			if err := e.submit(0, "completed", ""); err != nil {
				t.Fatalf("r0 execute: %v", err)
			}
			b0 := e.runNow().CommitMeta[engine.CommitMetaBoundSHA]
			if err := e.submit(1, "accepted", ""); err != nil {
				t.Fatalf("r0 review: %v", err)
			}
			if r := e.runNow(); r.Status != domain.RunStatusCompleted {
				t.Fatalf("r0 status %s", r.Status)
			}
			if got := frontMatterStatus(e.mainFile("main", childPath)); got != "Pending" {
				t.Fatalf("precondition: r0 merged %s at status %q, want Pending", childPath, got)
			}

			// R (commit status Completed): its executor force-pushes the
			// run branch onto R0's bound commit, so its reviewed diff is
			// empty.
			r := startSecondRun(t, e, reviewWorkflow(), !tc.bindingOff)
			e.git("update-ref", "refs/heads/"+r.run.BranchName, b0)
			if d := e.git("diff", "--name-only", "main..."+r.run.BranchName); d != "" {
				t.Fatalf("precondition: R's reviewed diff is not empty: %q", d)
			}
			r.submit(t, 0, "completed")
			r.submit(t, 1, "accepted")
			if got := r.runNow(t); got.Status != domain.RunStatusCompleted {
				t.Fatalf("R status = %s, want completed", got.Status)
			}
			if got := frontMatterStatus(e.mainFile("main", childPath)); got != "Pending" {
				t.Fatalf("main:%s status = %q after R, want R0's Pending: R's cascade rewrote an artifact outside its reviewed diff", childPath, got)
			}
			if got := frontMatterStatus(e.mainFile("main", "tasks/t.md")); got != "Completed" {
				t.Fatalf("main:tasks/t.md status = %q, want R's Completed", got)
			}
			if tc.bindingOff {
				return
			}

			// R's own replay still recognises R's merge: nothing is added.
			before := e.git("rev-parse", "refs/heads/main")
			if err := r.store.UpdateRunStatus(context.Background(), r.run.RunID, domain.RunStatusCommitting); err != nil {
				t.Fatal(err)
			}
			if err := r.orch.MergeRunBranch(context.Background(), r.run.RunID); err != nil {
				t.Fatalf("replay: %v", err)
			}
			if got := e.git("rev-parse", "refs/heads/main"); got != before {
				t.Fatalf("R's replay moved main %s -> %s:\n%s", before, got, e.git("log", "--oneline", "--graph", "main"))
			}
		})
	}
}
