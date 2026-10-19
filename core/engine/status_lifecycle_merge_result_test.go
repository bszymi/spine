package engine

// INIT-023 TASK-017 review round 1: the merge-result check (V1) judges
// what the merge lands RELATIVE TO MAIN, not the paths the plan listed.
//
// The plan judges Diff(merge-base, bound) for one `git merge-base main
// bound`. `git merge` (ort) merges against the virtual base of ALL best
// common ancestors, and against main as it is when the merge runs. When
// the two bases differ — a criss-cross history, or main gaining commits of
// the bound history between the judgement and the merge — a status change
// or a deletion the plan never saw lands with the branch's content. These
// tests build each shape on real git and require the merge to be refused
// (nothing pushed, the local main taken back), plus the controls: a
// criss-cross whose result is fine, and main-side renames git merges
// cleanly (review finding P2: V1 read fixed path names and refused them).

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// gitAt runs git with the author and committer date set.
func (r *lcRepo) gitAt(date string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// onAt is on with a commit date.
func (r *lcRepo) onAt(branch, date, msg string, edit func()) string {
	r.t.Helper()
	r.git("checkout", "-q", branch)
	edit()
	r.git("add", "-A")
	r.gitAt(date, "commit", "-q", "--allow-empty", "-m", msg)
	head := r.git("rev-parse", "HEAD")
	r.git("checkout", "-q", "main")
	return head
}

// lcDate is now plus h hours, as git reads it.
func lcDate(h int) string {
	return time.Now().Add(time.Duration(h) * time.Hour).UTC().Format(time.RFC3339)
}

// crissCross builds the history `git merge` resolves against a virtual base:
//
//	main:  C0 ── C1 (unrelated) ── M1 = merge --no-ff b1
//	other: C0 ── b1 (carry: another run's commit)
//	run:   C1 + b1 merged, the conflict-free merge resolved by resolve
//
// `git merge-base --all main run` is {C1, b1}; the plain merge-base the
// lifecycle judges against is the newer one by date, C1, so Diff(C1, run)
// does not list what resolve undid of b1. b1Date sets b1's date: lcDate(1)
// is the natural order (b1 made before C1), "2001-01-01T00:00:00Z" an
// author-chosen one. It returns the repository and C1.
func crissCross(t *testing.T, seed map[string]string, carry, resolve func(r *lcRepo), b1Date string) (*lcRepo, string) {
	t.Helper()
	r := newLCRepo(t, seed)
	r.git("branch", "other")
	b1 := r.onAt("other", b1Date, "b1: another run's change", func() { carry(r) })
	c1 := r.onAt("main", lcDate(2), "C1: unrelated", func() { r.write("unrelated.txt", "c1\n") })
	r.gitAt(lcDate(3), "merge", "--no-ff", "-q", "-m", "M1: merge b1", "other")

	r.git("checkout", "-q", lcBranch)
	r.git("reset", "-q", "--hard", c1)
	r.gitAt(lcDate(4), "merge", "--no-ff", "--no-commit", "-q", "other")
	resolve(r)
	r.write("src/run.txt", "the run's work\n")
	r.git("add", "-A")
	r.gitAt(lcDate(4), "commit", "-q", "-m", "run: merge main and b1")
	r.git("checkout", "-q", "main")

	bases := strings.Fields(r.git("merge-base", "--all", "main", lcBranch))
	if len(bases) != 2 || r.git("merge-base", "main", lcBranch) != c1 {
		t.Fatalf("not a criss-cross with C1 as the plain merge-base: --all %v, plain %s (C1 %s, b1 %s)",
			bases, r.git("merge-base", "main", lcBranch), c1, b1)
	}
	return r, c1
}

// requireMergeResultRefused merges and asserts a V1 refusal: the merge was
// attempted, the local main is back where it was, the outcome is
// failed/precondition merge_result_mismatch naming every path in paths,
// and the (legacy) run failed.
func (e *lcEnv) requireMergeResultRefused(paths ...string) {
	t := e.r.t
	t.Helper()
	e.merge()
	if got := e.r.head("main"); got != e.main0 {
		t.Fatalf("local main holds the refused merge: %s (was %s)", got, e.main0)
	}
	if e.git.merges == 0 {
		t.Fatal("no merge was attempted: the plan refused, not V1")
	}
	o := e.outcome()
	if o == nil || o.Status != domain.RepositoryMergeStatusFailed || o.FailureClass != domain.MergeFailurePrecondition ||
		!strings.HasPrefix(o.FailureDetail, "status_lifecycle_violation:") {
		t.Fatalf("primary outcome = %+v, want failed/precondition status_lifecycle_violation", o)
	}
	for _, p := range paths {
		if !strings.Contains(o.FailureDetail, p+": "+LifecycleMergeResultMismatch) {
			t.Fatalf("outcome detail %q does not name %s: %s", o.FailureDetail, p, LifecycleMergeResultMismatch)
		}
	}
	if e.run.Status != domain.RunStatusFailed {
		t.Fatalf("run status = %s, want failed", e.run.Status)
	}
}

// TestStatusLifecycle_MergeResultCrissCross: a criss-cross bound commit
// lands nothing the lifecycle did not judge (review round 1, P1).
func TestStatusLifecycle_MergeResultCrissCross(t *testing.T) {
	seed := func() map[string]string {
		return map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending"), lcSibling: lcArt("TASK-002", "Task", "Pending")}
	}
	completeSibling := func(r *lcRepo) { r.write(lcSibling, lcArt("TASK-002", "Task", "Completed")) }

	for _, c := range []struct {
		name    string
		b1Date  string
		carry   func(r *lcRepo)
		resolve func(r *lcRepo)
		path    string // the artifact whose status, type or existence must not change
		want    string // its status on main, before and after
	}{
		{"a Completed sibling reopened (natural dates)", lcDate(1), completeSibling,
			func(r *lcRepo) { r.write(lcSibling, lcArt("TASK-002", "Task", "Pending")) }, lcSibling, "Completed"},
		{"a Completed sibling reopened (author-chosen date)", "2001-01-01T00:00:00Z", completeSibling,
			func(r *lcRepo) { r.write(lcSibling, lcArt("TASK-002", "Task", "Pending")) }, lcSibling, "Completed"},
		{"a non-owned Completed task deleted", lcDate(1),
			func(r *lcRepo) { r.write(lcNewTask, lcArt("TASK-003", "Task", "Completed")) },
			func(r *lcRepo) { r.git("rm", "-q", "-f", lcNewTask) }, lcNewTask, "Completed"},
		{"a Completed task judged as a Draft addition", lcDate(1),
			func(r *lcRepo) { r.write(lcNewTask, lcArt("TASK-003", "Task", "Completed")) },
			func(r *lcRepo) { r.write(lcNewTask, lcArt("TASK-003", "Task", "Draft")) }, lcNewTask, "Completed"},
		{"the owned TaskPath completed over a Cancelled main", lcDate(1),
			func(r *lcRepo) { r.write(lcTask, lcArt("TASK-001", "Task", "Cancelled")) },
			func(r *lcRepo) { r.write(lcTask, lcArt("TASK-001", "Task", "Pending")) }, lcTask, "Cancelled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, _ := crissCross(t, seed(), c.carry, c.resolve, c.b1Date)
			if got := r.statusAt("main", c.path); got != c.want {
				t.Fatalf("setup: main %s = %s, want %s", c.path, got, c.want)
			}
			e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
			e.requireMergeResultRefused(c.path)
			if got := r.statusAt("main", c.path); got != c.want {
				t.Fatalf("main %s = %s, want %s", c.path, got, c.want)
			}
		})
	}

	// A criss-cross alone is not refused: when the other merge-base carries
	// no status change, the plan's verdict and the merge agree. (One that
	// does is attributed to the run by the plan, which judges against the
	// newer base only, and refused there: fail closed, KNOWN-LIMITATIONS.)
	t.Run("control: a criss-cross whose result changes nothing unjudged merges", func(t *testing.T) {
		r, _ := crissCross(t, seed(), func(r *lcRepo) { r.write("src/other.txt", "b1\n") }, func(*lcRepo) {}, lcDate(1))
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
		e.requireMerged()
		if got := r.statusAt("main", lcTask); got != "Completed" {
			t.Fatalf("main task-001 = %s, want Completed", got)
		}
		if got := r.statusAt("main", lcSibling); got != "Pending" {
			t.Fatalf("main task-002 = %s, want Pending", got)
		}
	})
}

// TestStatusLifecycle_MergeResultMainMoved: main gains commits of the
// bound history between the judgement and the merge (no criss-cross). The
// merge-base the plan judged against is then not the one git merges with:
// the refused merge is taken back and the attempt retried, and the retry
// judges against the new base (review round 1, P1 "race").
func TestStatusLifecycle_MergeResultMainMoved(t *testing.T) {
	r := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending"), lcSibling: lcArt("TASK-002", "Task", "Pending")})
	// R1's branch completes task-002 (not merged yet); this run's branch
	// builds on it and puts task-002 back to Pending: against main (C0)
	// task-002 is unchanged, so the plan does not list it.
	r.git("branch", "r1")
	r.on("r1", "R1 completes task-002", func() { r.write(lcSibling, lcArt("TASK-002", "Task", "Completed")) })
	r.git("checkout", "-q", lcBranch)
	r.git("merge", "-q", "--ff-only", "r1")
	r.git("checkout", "-q", "main")
	r.onRun("run: reopen task-002", func() {
		r.write(lcSibling, lcArt("TASK-002", "Task", "Pending"))
		r.write("src/run.txt", "work\n")
	})

	e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
	var m1 string
	e.git.beforeMergeBound = func() { // R1 lands between the plan and the merge
		r.git("checkout", "-q", "main")
		r.git("merge", "--no-ff", "-q", "-m", "M1: merge R1", "r1")
		m1 = r.head("main")
	}
	e.merge()
	if e.run.Status != domain.RunStatusCommitting {
		t.Fatalf("first attempt: run %s (outcome %+v), want committing (a retry, not a verdict)", e.run.Status, e.outcome())
	}
	if got := r.head("main"); got != m1 {
		t.Fatalf("first attempt: main = %s, want R1's merge %s (the refused merge taken back, R1 kept)", got, m1)
	}
	if got := r.statusAt("main", lcSibling); got != "Completed" {
		t.Fatalf("first attempt: main task-002 = %s, want Completed", got)
	}

	// The retry judges against the new merge-base (R1's commit): the
	// branch's reopening of task-002 is now in its diff.
	e.main0 = m1
	e.merge()
	if e.run.Status != domain.RunStatusFailed {
		t.Fatalf("retry: run %s (outcome %+v), want failed", e.run.Status, e.outcome())
	}
	if o := e.outcome(); o == nil || !strings.Contains(o.FailureDetail, lcSibling+": "+LifecycleNonOwnedStatusChange+" (Task Completed -> Pending)") {
		t.Fatalf("retry: outcome = %+v, want non_owned_status_change on task-002", o)
	}
	if got := r.head("main"); got != m1 {
		t.Fatalf("retry: main moved: %s -> %s", m1, got)
	}
	if got := r.statusAt("main", lcSibling); got != "Completed" {
		t.Fatalf("main task-002 = %s, want Completed", got)
	}
}

// TestStatusLifecycle_MergeResultMainSideRename: a rename main made after
// the fork merges cleanly with the branch's edit, and V1 accepts it — for
// a non-owned artifact whose body the branch edited and for the owned
// TaskPath the cascade completes (review round 1, P2: V1 read the old name
// at main and at the merge result and refused merge_result_mismatch on
// every attempt).
func TestStatusLifecycle_MergeResultMainSideRename(t *testing.T) {
	const moved = "initiatives/init-001/epics/epic-001/tasks/task-002-renamed.md"
	const taskMoved = "initiatives/init-001/epics/epic-001/tasks/task-001-moved.md"
	sib := func(body string) string { return lcArt("TASK-002", "Task", "Completed") + body }

	t.Run("a non-owned artifact the branch edits", func(t *testing.T) {
		r := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending"), lcSibling: sib("v1\n")})
		r.onRun("edit the sibling's body", func() { r.write(lcSibling, sib("v2\n")) })
		r.onMain("move the sibling", func() { r.git("mv", lcSibling, moved) })
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
		e.requireMerged()
		if got := r.statusAt("main", moved); got != "Completed" {
			t.Fatalf("main %s = %s, want Completed", moved, got)
		}
		if out := r.git("show", "main:"+moved); !strings.Contains(out, "v2") {
			t.Fatalf("the branch's edit did not land on the moved file:\n%s", out)
		}
	})
	t.Run("the owned TaskPath", func(t *testing.T) {
		r := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending")})
		r.onRun("work", func() { r.write("src/run.txt", "work\n") })
		r.onMain("move the task", func() { r.git("mv", lcTask, taskMoved) })
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
		e.requireMerged()
		if got := r.statusAt("main", taskMoved); got != "Completed" {
			t.Fatalf("main %s = %s, want Completed", taskMoved, got)
		}
		if got := r.statusAt("main", lcTask); got != "-" {
			t.Fatalf("main %s = %s, want absent", lcTask, got)
		}
	})
	t.Run("the owned epic moved: the status-conflict gate asks under main's name", func(t *testing.T) {
		// Codex review (round 1 follow-up): the projection keys the moved
		// epic under its new name, where SC-002 trips; under the old name
		// it has no row and would pass, and V1 lets the rename land.
		const epic = "initiatives/init-001/epics/epic-001/epic.md"
		const epicMoved = "initiatives/init-001/epics/epic-001/epic-renamed.md"
		r := newLCRepo(t, map[string]string{epic: lcArt("EPIC-001", "Epic", "Pending")})
		r.onRun("work", func() { r.write("src/run.txt", "work\n") })
		r.onMain("move the epic", func() { r.git("mv", epic, epicMoved) })
		e := newLCEnv(t, r, lcOpts{taskPath: epic, meta: map[string]string{"status": "Completed"}})
		v := &scValidator{perPath: map[string]domain.ValidationResult{epicMoved: {Status: "failed",
			Errors: []domain.ValidationError{{RuleID: "SC-002", Severity: "error"}}}}}
		e.orch.WithValidator(v)
		e.requireRefused(LifecycleStatusConflict)
		if got := r.statusAt("main", epicMoved); got != "Pending" {
			t.Fatalf("main %s = %s, want Pending", epicMoved, got)
		}
	})
	t.Run("the owned TaskPath moved and cancelled on main", func(t *testing.T) {
		// git merges the rename and the status lines conflict-free only
		// under merge=union; whatever git does, a status main changed
		// since the merge-base is not overwritten.
		r := newLCRepo(t, map[string]string{".gitattributes": "*.md merge=union\n", lcTask: lcArt("TASK-001", "Task", "Pending")})
		r.onRun("work", func() { r.write("src/run.txt", "work\n") })
		r.onMain("move and cancel the task", func() {
			r.git("mv", lcTask, taskMoved)
			r.write(taskMoved, lcArt("TASK-001", "Task", "Cancelled"))
		})
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
		e.requireMergeResultRefused(taskMoved)
		if got := r.statusAt("main", taskMoved); got != "Cancelled" {
			t.Fatalf("main %s = %s, want Cancelled", taskMoved, got)
		}
	})
}

// TestStatusLifecycle_NonCanonicalTaskPathOnGit (codex review, round 1
// follow-up): a run started with "./<path>" at the repository root (StartRun
// accepts it) owns its artifact and the cascade lands its status.
func TestStatusLifecycle_NonCanonicalTaskPathOnGit(t *testing.T) {
	r := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending")})
	r.onRun("work", func() { r.write("src/run.txt", "work\n") })
	e := newLCEnv(t, r, lcOpts{taskPath: "./" + lcTask, meta: map[string]string{"status": "Completed"}})
	e.requireMerged()
	if got := r.statusAt("main", lcTask); got != "Completed" {
		t.Fatalf("main %s = %s, want Completed", lcTask, got)
	}
}
