package engine

// INIT-023 TASK-006 real-repository cases: the criss-cross bypass the
// design check reproduced (AC 28), the ADR-008 lifecycle exemption
// (AC 13), the status-cascade footprint (AC 14), artifacts_dir workflow
// directories (AC 30), the own-run evidence carve-out (AC 32), renames
// and deletions (AC 10), reject revoking an approval (AC 31) and a stale
// approval (AC 7).

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/testutil"
)

func (g *govRepo) requireHeld(why string) {
	g.t.Helper()
	r := g.store.runs[govRunID]
	if r.Status != domain.RunStatusCommitting {
		g.t.Fatalf("%s: run = %s, want committing (held)", why, r.Status)
	}
	if len(g.eventsOf(domain.EventGovernanceMergeHeld)) == 0 {
		g.t.Fatalf("%s: no governance.merge_held event", why)
	}
}

func (g *govRepo) requireMerged(why string) {
	g.t.Helper()
	if r := g.store.runs[govRunID]; r.Status != domain.RunStatusCompleted {
		g.t.Fatalf("%s: run = %s, want completed", why, r.Status)
	}
	if n := len(g.eventsOf(domain.EventGovernanceMergeHeld)); n != 0 {
		g.t.Fatalf("%s: %d governance.merge_held events, want none", why, n)
	}
}

func (g *govRepo) heldPaths() []string {
	g.t.Helper()
	held := g.eventsOf(domain.EventGovernanceMergeHeld)
	if len(held) == 0 {
		return nil
	}
	var p struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(held[len(held)-1].Payload, &p); err != nil {
		g.t.Fatal(err)
	}
	return p.Paths
}

// datedGit runs git in dir with pinned dates (the criss-cross bases are
// ordered by date, which decides the one `git merge-base` prints).
func datedGit(t *testing.T, dir, date string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// crissCross builds the design-check history on g: another run (r2)
// tightens path; main moves; the run branch does real work, merges r2 and
// resolves path back to the old content; r2 then lands on main. main and
// the run branch now have two merge bases, and only one of them shows the
// revert.
func crissCross(t *testing.T, g *govRepo, path, weak, strong string) {
	t.Helper()
	d := g.dir
	write := func(p, c string) { testutil.WriteFile(t, d, p, c) }
	write(path, weak)
	datedGit(t, d, "2026-02-01T00:00:00Z", "add", "-A")
	datedGit(t, d, "2026-02-01T00:00:00Z", "commit", "-qm", "weak "+path)
	datedGit(t, d, "2026-02-01T00:00:00Z", "checkout", "-q", "-b", "spine/run/r2")
	write(path, strong)
	datedGit(t, d, "2026-02-02T00:00:00Z", "commit", "-qam", "r2 tightens")
	datedGit(t, d, "2026-02-02T00:00:00Z", "checkout", "-q", "main")
	write("b.txt", "b\n")
	datedGit(t, d, "2026-02-03T00:00:00Z", "add", "-A")
	datedGit(t, d, "2026-02-03T00:00:00Z", "commit", "-qm", "main moves")
	datedGit(t, d, "2026-02-03T00:00:00Z", "branch", "-f", g.branch, "main")
	datedGit(t, d, "2026-02-03T00:00:00Z", "checkout", "-q", g.branch)
	write("feat.txt", "feature\n")
	datedGit(t, d, "2026-02-04T00:00:00Z", "add", "-A")
	datedGit(t, d, "2026-02-04T00:00:00Z", "commit", "-qm", "work")
	cmd := exec.Command("git", "-C", d, "merge", "--no-ff", "--no-commit", "spine/run/r2")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-02-05T00:00:00Z", "GIT_COMMITTER_DATE=2026-02-05T00:00:00Z")
	_, _ = cmd.CombinedOutput()
	write(path, weak)
	datedGit(t, d, "2026-02-05T00:00:00Z", "add", "-A")
	datedGit(t, d, "2026-02-05T00:00:00Z", "commit", "-qm", "merge r2, keep the old file")
	datedGit(t, d, "2026-02-05T00:00:00Z", "checkout", "-q", "main")
	datedGit(t, d, "2026-02-06T00:00:00Z", "merge", "-q", "--no-ff", "-m", "merge run r2", "spine/run/r2")
	if got := g.mainFile(path); got != strong {
		t.Fatalf("precondition: main carries %q, want the tightened file", got)
	}
}

const (
	bpWeak       = "version: 1\nrules:\n  - branch: main\n    protections: [no-delete]\n"
	bpStrong     = govProtected
	wfWithReview = govTaskDefault
	wfMore       = "id: task-default\nname: Default\nversion: \"8.2\"\nstatus: Active\napplies_to: [Task]\nentry_step: execute\nsteps:\n  - id: execute\n  - id: review\n  - id: security_review\n  - id: publish\n"
)

// TestGovernanceGuard_CrissCrossRevertIsCaught is AC 28 (the design-check
// probe as a regression test): (a) with every merge base the revert is
// held and named; (b) with only the single base the first computation
// used, the post-merge assertion refuses the merge — nothing published,
// local main restored to the base, the working tree clean, the tightened
// bytes still on main. Mutations: reducing the union to one base fails
// (a); removing the assertion fails (b).
func TestGovernanceGuard_CrissCrossRevertIsCaught(t *testing.T) {
	for _, file := range []struct{ path, weak, strong string }{
		{".spine/branch-protection.yaml", bpWeak, bpStrong},
		{"workflows/task-default.yaml", wfWithReview, wfMore},
	} {
		t.Run(file.path+"/every merge base", func(t *testing.T) {
			g := newGovRepo(t, govRepoOpts{mainFiles: map[string]string{"README.md": "r\n"}, guard: defaultGuard()})
			crissCross(t, g, file.path, file.weak, file.strong)
			g.toMerge()
			g.requireHeld("criss-cross")
			if paths := g.heldPaths(); !hasPath(paths, file.path) {
				t.Fatalf("held paths %v do not name %s", paths, file.path)
			}
			if got := g.mainFile(file.path); got != file.strong {
				t.Fatalf("main %s = %q, want the tightened file", file.path, got)
			}
		})
		t.Run(file.path+"/single base: post-merge assertion", func(t *testing.T) {
			g := newGovRepo(t, govRepoOpts{mainFiles: map[string]string{"README.md": "r\n"}, guard: defaultGuard(),
				wrap: func(c *git.CLIClient) GitOperator { return singleBaseCLI{c} }})
			crissCross(t, g, file.path, file.weak, file.strong)
			base := g.mainHead()
			g.toMerge()
			if n := len(g.eventsOf(domain.EventGovernanceMergeHeld)); n != 1 {
				t.Fatalf("merge_held events = %d, want 1 (the post-merge refusal)", n)
			}
			ev := g.eventsOf(domain.EventGovernanceMergeHeld)[0]
			if !strings.Contains(string(ev.Payload), `"post_merge":true`) || !strings.Contains(string(ev.Payload), file.path) {
				t.Fatalf("post-merge event payload = %s", ev.Payload)
			}
			if got := g.mainHead(); got != base {
				t.Fatalf("local main = %s, want the base %s (taken back)", got, base)
			}
			if got := g.mainFile(file.path); got != file.strong {
				t.Fatalf("main %s = %q, want the tightened file", file.path, got)
			}
			if !g.workingTreeClean() {
				t.Fatalf("working tree not clean: %s", g.git("status", "--porcelain"))
			}
			r := g.store.runs[govRunID]
			if r.Status != domain.RunStatusActive || r.CurrentStepID != "execute" {
				t.Fatalf("run = %s at %s, want routed back to execute (permanent refusal)", r.Status, r.CurrentStepID)
			}
			if out := g.primaryOutcome(); out == nil || out.Status != domain.RepositoryMergeStatusFailed {
				t.Fatalf("primary outcome = %+v, want failed", out)
			}
		})
	}
}

const (
	lifecyclePath = "workflows/workflow-lifecycle.yaml"
	lifecycleBody = "id: workflow-lifecycle\nname: WL\nversion: \"1.0\"\nstatus: Active\nmode: creation\napplies_to: [Workflow]\nentry_step: draft\nsteps:\n  - id: draft\n  - id: review\n"
)

func fooWorkflow(id, appliesTo string) string {
	return "id: " + id + "\nname: Foo\nversion: \"1.0\"\nstatus: Active\napplies_to: [" + appliesTo + "]\nentry_step: a\nsteps:\n  - id: a\n"
}

func lifecycleRun(taskPath string) func(*domain.Run) {
	return func(r *domain.Run) {
		r.Mode = domain.RunModePlanning
		r.TaskPath = taskPath
	}
}

// TestGovernanceGuard_LifecycleExemption is AC 13 with the design check's
// three extra cases (owner questions 3 and 10).
func TestGovernanceGuard_LifecycleExemption(t *testing.T) {
	main := map[string]string{lifecyclePath: lifecycleBody, ".spine/branch-protection.yaml": govProtected}
	guard := func(exempt bool) *GovernanceGuard {
		return &GovernanceGuard{Enabled: true, ExemptWorkflowLifecycleTaskPath: exempt, ExemptOwnRunEvidence: true}
	}
	type tc struct {
		name     string
		taskPath string
		files    map[string]string
		mainPlus map[string]string
		exempt   bool
		reviewer string
		wfPath   string
		held     bool
	}
	for _, c := range []tc{
		{name: "own workflow file only: exempt", taskPath: "workflows/foo.yaml",
			files: map[string]string{"workflows/foo.yaml": fooWorkflow("foo", "Task")}, exempt: true, held: false},
		{name: "also touches .spine/: held", taskPath: "workflows/foo.yaml",
			files: map[string]string{"workflows/foo.yaml": fooWorkflow("foo", "Task"), ".spine/x.yaml": "x: 1\n"}, exempt: true, held: true},
		{name: "task path is the governing workflow: held", taskPath: "workflows/wl.yaml", wfPath: "workflows/wl.yaml",
			files: map[string]string{"workflows/wl.yaml": fooWorkflow("wl", "Task")}, exempt: true, held: true},
		{name: "exemption off: held", taskPath: "workflows/foo.yaml",
			files: map[string]string{"workflows/foo.yaml": fooWorkflow("foo", "Task")}, exempt: false, held: true},
		{name: "reviewer is the draft author: held", taskPath: "workflows/foo.yaml",
			files: map[string]string{"workflows/foo.yaml": fooWorkflow("foo", "Task")}, exempt: true, reviewer: "author", held: true},
		{name: "edited workflow governs workflows: held", taskPath: "workflows/foo.yaml",
			files: map[string]string{"workflows/foo.yaml": fooWorkflow("foo", "Workflow")}, exempt: true, held: true},
		{name: "previous version governs workflows: held", taskPath: "workflows/foo.yaml",
			mainPlus: map[string]string{"workflows/foo.yaml": fooWorkflow("foo", "Workflow")},
			files:    map[string]string{"workflows/foo.yaml": fooWorkflow("foo", "Task")}, exempt: true, held: true},
		{name: "task path case differs from the parsed id: held", taskPath: "workflows/Foo.yaml",
			files: map[string]string{"workflows/Foo.yaml": fooWorkflow("foo", "Task")}, exempt: true, held: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			wf := govLifecycleWF()
			if c.wfPath != "" {
				wf.Path = c.wfPath
			}
			seed := map[string]string{}
			for k, v := range main {
				seed[k] = v
			}
			for k, v := range c.mainPlus {
				seed[k] = v
			}
			g := newGovRepo(t, govRepoOpts{wf: wf, mainFiles: seed, runFiles: c.files,
				guard: guard(c.exempt), tweak: lifecycleRun(c.taskPath)})
			reviewer := c.reviewer
			if reviewer == "" {
				reviewer = "reviewer"
			}
			g.complete("draft", "author", "submitted")
			g.complete("review", reviewer, "approved")
			if c.held {
				g.requireHeld(c.name)
				if g.mainFile(c.taskPath) == c.files[c.taskPath] {
					t.Fatal("held change reached main")
				}
				// The authorised lifecycle edit still merges after an
				// operator approval.
				res, err := g.decide(GovernanceApprove, g.runHead(), govOperator, "")
				if err != nil || res.MergeError != "" {
					t.Fatalf("approve: %v %+v", err, res)
				}
				if r := g.store.runs[govRunID]; r.Status != domain.RunStatusCompleted {
					t.Fatalf("after approval run = %s, want completed", r.Status)
				}
			} else {
				g.requireMerged(c.name)
			}
			if got := g.mainFile(c.taskPath); got != c.files[c.taskPath] {
				t.Fatalf("main %s = %q after the run", c.taskPath, got)
			}
		})
	}
}

// TestGovernanceGuard_CascadeFootprintIsEvaluated is AC 14 and part of
// AC 15: a run whose task lives under .spine/ and whose outcome sets a
// status is held although its own diff does not touch the task file —
// the bound status cascade rewrites it, and the guard diffs the commit
// that merges (the cascade commit), not the bound one. The run branch
// gets no status commit. Mutation: diffing src.bound instead of src.sha
// merges the run.
func TestGovernanceGuard_CascadeFootprintIsEvaluated(t *testing.T) {
	wf := govLegacyWF()
	wf.Steps[1].Outcomes[0].Commit = map[string]string{"status": "Completed"}
	g := newGovRepo(t, govRepoOpts{wf: wf,
		mainFiles: map[string]string{".spine/tasks/TASK-9.md": strings.Replace(govTaskPending, "TASK-001", "TASK-9", 1)},
		runFiles:  map[string]string{"src/x.go": "package src\n"},
		guard:     defaultGuard(), tweak: func(r *domain.Run) { r.TaskPath = ".spine/tasks/TASK-9.md" }})
	head := g.runHead()
	g.toMerge()
	g.requireHeld("cascade footprint")
	if paths := g.heldPaths(); strings.Join(paths, ",") != ".spine/tasks/TASK-9.md" {
		t.Fatalf("held paths = %v, want the cascaded task file", paths)
	}
	if g.runHead() != head {
		t.Fatal("the held run's branch moved (a status commit on the run branch)")
	}
	res, err := g.decide(GovernanceApprove, head, govOperator, "")
	if err != nil || res.RunStatus != domain.RunStatusCompleted {
		t.Fatalf("approve: %v %+v", err, res)
	}
	if !strings.Contains(g.mainFile(".spine/tasks/TASK-9.md"), "status: Completed") {
		t.Fatal("cascade not merged after approval")
	}
}

// TestGovernanceGuard_ArtifactsDirWorkflows is AC 30: with WorkflowDirs
// from projection.WorkflowDirs("spine") — ["workflows/",
// "spine/workflows/"], pinned in adapters/projection and the standalone
// wiring test — both workflow directories are guarded and other
// artifacts_dir content is not.
func TestGovernanceGuard_ArtifactsDirWorkflows(t *testing.T) {
	dirs := []string{"workflows/", "spine/workflows/"}
	for _, c := range []struct {
		name string
		path string
		dirs []string
		held bool
	}{
		{"spine/workflows held", "spine/workflows/task-default.yaml", dirs, true},
		{"root workflows held", "workflows/x.yaml", dirs, true},
		{"spine/docs not held", "spine/docs/a.md", dirs, false},
		{"spine/workflows unguarded without WorkflowDirs", "spine/workflows/task-default.yaml", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newGovRepo(t, govRepoOpts{runFiles: map[string]string{c.path: "id: x\n"},
				guard: &GovernanceGuard{Enabled: true, WorkflowDirs: c.dirs}})
			g.toMerge()
			if c.held {
				g.requireHeld(c.name)
			} else {
				g.requireMerged(c.name)
			}
		})
	}
}

// TestGovernanceGuard_OwnRunEvidenceCarveOut is AC 32 (owner question 9).
func TestGovernanceGuard_OwnRunEvidenceCarveOut(t *testing.T) {
	own := ".spine/runs/" + govRunID + "/evidence/spine.yaml"
	for _, c := range []struct {
		name    string
		path    string
		carve   bool
		held    bool
		message string
	}{
		{"own evidence merges", own, true, false, ""},
		{"another run's evidence is held", ".spine/runs/run-other123/evidence/spine.yaml", true, true, ""},
		{"own run, outside evidence/, is held", ".spine/runs/" + govRunID + "/x.yaml", true, true, ""},
		{"own evidence without the carve-out is held", own, false, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newGovRepo(t, govRepoOpts{runFiles: map[string]string{c.path: "evidence: 1\n"},
				guard: &GovernanceGuard{Enabled: true, ExemptOwnRunEvidence: c.carve}})
			g.toMerge()
			if c.held {
				g.requireHeld(c.name)
			} else {
				g.requireMerged(c.name)
			}
		})
	}
}

// TestGovernanceGuard_RenameAndDeletion: a rename out of a governed tree
// (through OldPath) and a deletion of the protection config are
// governance changes (AC 10, engine side). Mutation: dropping the OldPath
// append lets the rename through the gate.
func TestGovernanceGuard_RenameAndDeletion(t *testing.T) {
	t.Run("rename out of workflows/", func(t *testing.T) {
		g := newGovRepo(t, govRepoOpts{mainFiles: map[string]string{"workflows/a.yaml": "id: a\nname: A\n"}, guard: defaultGuard()})
		g.git("checkout", "-q", g.branch)
		if err := os.MkdirAll(g.dir+"/docs", 0o755); err != nil {
			t.Fatal(err)
		}
		g.git("mv", "workflows/a.yaml", "docs/a.yaml")
		g.git("commit", "-qm", "move")
		g.git("checkout", "-q", "main")
		g.toMerge()
		g.requireHeld("rename")
		if paths := g.heldPaths(); !hasPath(paths, "workflows/a.yaml") {
			t.Fatalf("held paths %v lack the rename source", paths)
		}
	})
	t.Run("deletion of the protection config", func(t *testing.T) {
		g := newGovRepo(t, govRepoOpts{runFiles: map[string]string{".spine/branch-protection.yaml": ""}, guard: defaultGuard()})
		g.toMerge()
		g.requireHeld("deletion")
	})
}

// flakyMergeCLI fails MergeBound transiently n times.
type flakyMergeCLI struct {
	*git.CLIClient
	mu sync.Mutex
	n  int
}

func (f *flakyMergeCLI) MergeBound(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	f.mu.Lock()
	fail := f.n > 0
	if fail {
		f.n--
	}
	f.mu.Unlock()
	if fail {
		return git.MergeResult{}, &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "repository locked"}
	}
	return f.CLIClient.MergeBound(ctx, opts)
}

// TestGovernanceGuard_RejectRevokesTheApproval is AC 31: approve X while
// the merge fails transiently (the run stays committing), then reject;
// the approval is tombstoned, and when the author resubmits X unchanged
// and review accepts, the run is held again under a new event id.
// Mutation: removing the tombstone write merges X on the resubmit.
func TestGovernanceGuard_RejectRevokesTheApproval(t *testing.T) {
	var flaky *flakyMergeCLI
	g := newGovRepo(t, govRepoOpts{runFiles: slipFiles, guard: defaultGuard(), merger: true,
		wrap: func(c *git.CLIClient) GitOperator { flaky = &flakyMergeCLI{CLIClient: c, n: 1}; return flaky }})
	g.toMerge()
	x := g.runHead()
	firstHold := g.eventsOf(domain.EventGovernanceMergeHeld)[0].EventID

	res, err := g.decide(GovernanceApprove, x, govOperator, "")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if res.RunStatus != domain.RunStatusCommitting {
		t.Fatalf("after a transient merge failure run = %s, want committing", res.RunStatus)
	}
	if _, err := g.decide(GovernanceReject, x, govOperator, "changed my mind"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if sha := g.store.runs[govRunID].CommitMeta[domain.CommitMetaGovernanceApprovalSHA]; sha != "" {
		t.Fatalf("approval sha after reject = %q, want the empty tombstone", sha)
	}
	if g.runHead() != x {
		t.Fatal("precondition: the run branch must still be at X")
	}
	g.toMerge() // resubmit X unchanged; review accepts
	g.requireHeld("resubmitted after reject")
	if g.mainFile(".spine/branch-protection.yaml") != govProtected {
		t.Fatal("the rejected change merged on the resubmit")
	}
	held := g.eventsOf(domain.EventGovernanceMergeHeld)
	if last := held[len(held)-1].EventID; last == firstHold {
		t.Fatalf("second hold reused the first episode's event id %s", last)
	}
}

// TestGovernanceGuard_StaleApprovalIsNotHonoured is AC 7's scenario: an
// approval of X; the merge fails permanently (a conflict) and routes back;
// a new commit makes the bound commit Y; review accepts again. The guard
// holds Y and names the stale approval, and a decision naming X is a
// conflict. Mutation: dropping the SHA equality merges Y on the old
// approval.
func TestGovernanceGuard_StaleApprovalIsNotHonoured(t *testing.T) {
	g := newGovRepo(t, govRepoOpts{runFiles: map[string]string{
		"README.md": "run\n", ".spine/branch-protection.yaml": govRelaxed}, guard: defaultGuard()})
	g.toMerge()
	x := g.runHead()
	// main moves in a way the run conflicts with.
	g.writeCommit(map[string]string{"README.md": "main\n"}, "main moves")
	res, err := g.decide(GovernanceApprove, x, govOperator, "")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if r := g.store.runs[govRunID]; r.Status != domain.RunStatusActive || r.CurrentStepID != "execute" {
		t.Fatalf("after a conflicting merge run = %s at %s (result %+v), want routed back to execute", r.Status, r.CurrentStepID, res)
	}
	y := g.commitOnRun(map[string]string{"README.md": "main\n"})
	g.toMerge()
	g.requireHeld("stale approval")
	held := g.eventsOf(domain.EventGovernanceMergeHeld)
	if p := string(held[len(held)-1].Payload); !strings.Contains(p, `"stale_approval_sha":"`+x+`"`) || !strings.Contains(p, `"bound_sha":"`+y+`"`) {
		t.Fatalf("hold payload = %s, want stale approval %s and bound %s", p, x, y)
	}
	if pub := g.latest("publish"); pub.ErrorDetail == nil || !strings.Contains(pub.ErrorDetail.Message, "approval recorded for "+x) {
		t.Fatalf("hold detail = %+v, want the stale approval named", pub.ErrorDetail)
	}
	_, err = g.decide(GovernanceApprove, x, govOperator, "")
	requireSpineCode(t, err, domain.ErrConflict)
}
