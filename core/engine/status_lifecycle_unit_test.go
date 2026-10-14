package engine

// INIT-023 TASK-017 against the fake binder: the rule table (AC 7), fail
// closed on retryable errors (AC 9), the quoted-path guard (AC 10, AC 23),
// refusal classification (AC 11), the SC merge gate (AC 12) and the error
// classes of DC-6 (AC 22).

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

var (
	ulBound = sha40('a')
	ulBase  = sha40('0') // fakeBinderGit's default MergeBase answer
	ulCasc  = sha40('c')
	ulMerge = sha40('d')
	ulMain  = sha40('b')
)

const ulTask = "tasks/t.md"

// lcGit is fakeBinderGit with injectable Diff / MergeBase / ReadFile errors.
type lcGit struct {
	*fakeBinderGit
	diffErr error
	mbErr   error
	readErr map[string]error // "ref:path"
}

func (g *lcGit) Diff(ctx context.Context, a, b string) ([]git.FileDiff, error) {
	if g.diffErr != nil {
		g.record("Diff " + a + " " + b)
		return nil, g.diffErr
	}
	return g.fakeBinderGit.Diff(ctx, a, b)
}

func (g *lcGit) MergeBase(ctx context.Context, a, b string) (string, error) {
	if g.mbErr != nil {
		g.record("MergeBase " + a + " " + b)
		return "", g.mbErr
	}
	return g.fakeBinderGit.MergeBase(ctx, a, b)
}

func (g *lcGit) ReadFile(ctx context.Context, ref, p string) ([]byte, error) {
	if err := g.readErr[ref+":"+p]; err != nil {
		g.record("ReadFile " + ref + ":" + p)
		return nil, err
	}
	return g.fakeBinderGit.ReadFile(ctx, ref, p)
}

func ulDoc(typ, status string) string {
	return "---\nid: X-1\ntype: " + typ + "\ntitle: X\nstatus: " + status + "\n---\n# X\n"
}

// lcFix is a committing run (publish workflow, or legacy) with binding and
// the lifecycle on: the TaskPath is a Pending Task at the merge-base and the
// bound commit, the outcome commits Completed, and the fake answers a clean
// cascade and merge.
func lcFix(t *testing.T, legacy bool) (*bindFix, *lcGit) {
	t.Helper()
	f := committingFix(t, legacy, false)
	g := &lcGit{fakeBinderGit: f.git, readErr: map[string]error{}}
	f.orch.git = g
	if err := f.orch.EnableMergeBinding(MergeBindingConfig{}); err != nil {
		t.Fatal(err)
	}
	f.orch.WithStatusLifecycle(domain.DefaultStatusLifecycle())
	f.run.CommitMeta = map[string]string{"status": "Completed", CommitMetaBoundSHA: ulBound,
		CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
	f.git.files[ulBound+":"+ulTask] = ulDoc("Task", "Pending")
	f.git.ontoSHA = ulCasc
	f.git.parents[ulCasc] = []string{ulBound}
	f.git.mergeRes = git.MergeResult{SHA: ulMerge, BaseSHA: ulMain}
	f.git.parents[ulMerge] = []string{ulMain, ulCasc}
	f.git.files[ulMerge+":"+ulTask] = ulDoc("Task", "Completed")
	return f, g
}

func (f *bindFix) diff(d ...git.FileDiff) { f.git.diffs[ulBase+" "+ulBound] = d }

// planFor evaluates the lifecycle directly (no merge).
func planFor(t *testing.T, f *bindFix, status string) (*lifecyclePlan, *StatusLifecycleError) {
	t.Helper()
	plan, err := f.orch.planStatusLifecycle(context.Background(), f.run, status, ulBound, ulBase, true)
	var le *StatusLifecycleError
	if errors.As(err, &le) {
		return nil, le
	}
	if err != nil {
		t.Fatalf("planStatusLifecycle: %v", err)
	}
	return plan, nil
}

func reasons(le *StatusLifecycleError) []string {
	if le == nil {
		return nil
	}
	var out []string
	for _, v := range le.Violations {
		out = append(out, v.Reason)
	}
	return out
}

// TestStatusLifecycle_RuleTable is AC 7: each reason code fires on its
// case, and the corpus shapes merge.
func TestStatusLifecycle_RuleTable(t *testing.T) {
	const added = "tasks/new.md"
	type files map[string]string
	cases := []struct {
		name     string
		planning bool
		task     string // TaskPath ("" = ulTask)
		status   string
		base     files // at the merge-base
		tip      files // at the bound commit
		diff     []git.FileDiff
		want     string // reason, "" = accepted
		updates  []string
	}{
		{name: "illegal_transition", status: "Pending",
			base: files{ulTask: ulDoc("Task", "Completed")}, tip: files{ulTask: ulDoc("Task", "Completed")},
			want: LifecycleIllegalTransition},
		{name: "illegal_initial_status", status: "Completed",
			base: files{ulTask: ulDoc("Task", "Pending")}, tip: files{ulTask: ulDoc("Task", "Pending"), added: ulDoc("Task", "Completed")},
			diff: []git.FileDiff{{Path: added, Status: "added"}}, want: LifecycleIllegalInitialStatus},
		{name: "status_not_in_type", planning: true, task: "tasks/i.md", status: "Pending",
			tip:  files{"tasks/i.md": ulDoc("Initiative", "Draft"), "adr/ADR-1.md": ulDoc("ADR", "Proposed")},
			diff: []git.FileDiff{{Path: "adr/ADR-1.md", Status: "added"}, {Path: "tasks/i.md", Status: "added"}},
			want: LifecycleStatusNotInType},
		{name: "type_changed (owned)", status: "Completed",
			base: files{ulTask: ulDoc("Task", "Pending")}, tip: files{ulTask: ulDoc("Epic", "Pending")},
			diff: []git.FileDiff{{Path: ulTask, Status: "modified"}}, want: LifecycleTypeChanged},
		{name: "type_changed (non-owned)", status: "Completed",
			base: files{ulTask: ulDoc("Task", "Pending"), "tasks/u.md": ulDoc("Task", "Pending")},
			tip:  files{ulTask: ulDoc("Task", "Pending"), "tasks/u.md": ulDoc("Epic", "Pending")},
			diff: []git.FileDiff{{Path: "tasks/u.md", Status: "modified"}}, want: LifecycleTypeChanged},
		{name: "not_an_artifact", status: "Completed",
			base: files{ulTask: "# no front matter\n"}, tip: files{ulTask: "# no front matter\n"},
			want: LifecycleNotAnArtifact},
		{name: "not_an_artifact (non-.md TaskPath with an effect)", task: "workflows/x.yaml", status: "Completed",
			want: LifecycleNotAnArtifact},
		{name: "undeclared_status", status: "Completed",
			base: files{ulTask: ulDoc("Task", "Done")}, tip: files{ulTask: ulDoc("Task", "Done")},
			want: LifecycleUndeclaredStatus},

		{name: "Task Pending -> In Progress (SMP task-default)", status: "In Progress",
			base: files{ulTask: ulDoc("Task", "Pending")}, tip: files{ulTask: ulDoc("Task", "Pending")}, updates: []string{ulTask}},
		{name: "Epic Pending -> Completed", task: "epics/e.md", status: "Completed",
			base: files{"epics/e.md": ulDoc("Epic", "Pending")}, tip: files{"epics/e.md": ulDoc("Epic", "Pending")}, updates: []string{"epics/e.md"}},
		{name: "Epic In Progress -> Completed", task: "epics/e.md", status: "Completed",
			base: files{"epics/e.md": ulDoc("Epic", "In Progress")}, tip: files{"epics/e.md": ulDoc("Epic", "In Progress")}, updates: []string{"epics/e.md"}},
		{name: "ADR planning Proposed -> Accepted", planning: true, task: "adr/ADR-2.md", status: "Accepted",
			tip:  files{"adr/ADR-2.md": ulDoc("ADR", "Proposed")},
			diff: []git.FileDiff{{Path: "adr/ADR-2.md", Status: "added"}}, updates: []string{"adr/ADR-2.md"}},
		{name: "ADR planning Proposed -> Deprecated", planning: true, task: "adr/ADR-2.md", status: "Deprecated",
			tip:  files{"adr/ADR-2.md": ulDoc("ADR", "Proposed")},
			diff: []git.FileDiff{{Path: "adr/ADR-2.md", Status: "added"}}, updates: []string{"adr/ADR-2.md"}},
		{name: "document-creation identity (no cascade commit)", planning: true, task: "governance/g.md", status: "Living Document",
			tip:  files{"governance/g.md": ulDoc("Governance", "Living Document")},
			diff: []git.FileDiff{{Path: "governance/g.md", Status: "added"}}},
		{name: "workflow-lifecycle: non-.md TaskPath without an effect is skipped", planning: true, task: "workflows/x.yaml",
			diff: []git.FileDiff{{Path: "workflows/x.yaml", Status: "modified"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := lcFix(t, true)
			delete(f.git.files, ulBound+":"+ulTask)
			if c.task != "" {
				f.run.TaskPath = c.task
			}
			if c.planning {
				f.run.Mode = domain.RunModePlanning
			}
			for p, v := range c.base {
				f.git.files[ulBase+":"+p] = v
			}
			for p, v := range c.tip {
				f.git.files[ulBound+":"+p] = v
			}
			f.diff(c.diff...)
			plan, le := planFor(t, f, c.status)
			if c.want != "" {
				if got := reasons(le); len(got) != 1 || got[0] != c.want {
					t.Fatalf("reasons = %v (%v), want [%s]", got, le, c.want)
				}
				return
			}
			if le != nil {
				t.Fatalf("refused: %v", le)
			}
			var got []string
			for p := range plan.updates {
				got = append(got, p)
			}
			if strings.Join(got, ",") != strings.Join(c.updates, ",") {
				t.Fatalf("rewrites = %v, want %v", got, c.updates)
			}
		})
	}
}

// TestStatusLifecycle_FailsClosed is AC 9: a retryable error keeps the run
// in committing and nothing is merged; with the lifecycle off the same Diff
// error degrades as today.
func TestStatusLifecycle_FailsClosed(t *testing.T) {
	transient := &git.GitError{Kind: git.ErrKindTransient, Op: "x", Message: "repository locked"}
	for _, c := range []struct {
		name string
		set  func(f *bindFix, g *lcGit)
	}{
		{"Diff", func(_ *bindFix, g *lcGit) { g.diffErr = transient }},
		{"MergeBase", func(_ *bindFix, g *lcGit) { g.mbErr = transient }},
		{"ReadFile at the bound commit", func(_ *bindFix, g *lcGit) { g.readErr[ulBound+":"+ulTask] = transient }},
		{"ReadFile at the merge-base", func(f *bindFix, g *lcGit) {
			f.diff(git.FileDiff{Path: "tasks/u.md", Status: "modified"})
			f.git.files[ulBound+":tasks/u.md"] = ulDoc("Task", "Pending")
			g.readErr[ulBase+":tasks/u.md"] = transient
		}},
		{"a non-git error", func(_ *bindFix, g *lcGit) { g.readErr[ulBound+":"+ulTask] = errors.New("connection reset") }},
		{"CommitFilesOnto", func(f *bindFix, _ *lcGit) { f.git.ontoErr = errors.New("disk full") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, g := lcFix(t, false)
			c.set(f, g)
			if err := f.merge(); err == nil {
				t.Fatal("MergeRunBranch returned nil")
			}
			if f.run.Status != domain.RunStatusCommitting || len(f.git.merges) != 0 || f.git.callCount("Merge ") != 0 {
				t.Fatalf("run %s, MergeBound %d; want committing and no merge", f.run.Status, len(f.git.merges))
			}
			if len(f.store.mergeOutcomes) != 0 {
				t.Fatalf("a transient failure recorded an outcome: %+v", f.store.mergeOutcomes)
			}
		})
	}
	t.Run("lifecycle off: a Diff error degrades as today", func(t *testing.T) {
		f, g := lcFix(t, false)
		f.orch.WithStatusLifecycle(nil)
		g.diffErr = transient
		if err := f.merge(); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		if len(f.git.merges) != 1 {
			t.Fatalf("MergeBound calls = %d, want the merge to proceed", len(f.git.merges))
		}
	})
}

// TestStatusLifecycle_QuotedPaths is AC 10 and the OldPath half of AC 23.
func TestStatusLifecycle_QuotedPaths(t *testing.T) {
	quoted := `"tasks/\303\251.md"`
	for _, c := range []struct {
		name string
		d    git.FileDiff
		want []string
	}{
		{"added .md", git.FileDiff{Path: quoted, Status: "added"}, []string{LifecycleUnparseablePath}},
		{"renamed from a quoted .md", git.FileDiff{Path: "tasks/x.md", OldPath: quoted, Status: "renamed"}, []string{LifecycleUnparseablePath}},
		{"quoted non-markdown is ignored", git.FileDiff{Path: `"src/\303\251.go"`, Status: "added"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, _ := lcFix(t, true)
			f.git.files[ulBase+":"+ulTask] = ulDoc("Task", "Pending")
			f.diff(c.d)
			_, le := planFor(t, f, "Completed")
			if got := reasons(le); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("reasons = %v, want %v", got, c.want)
			}
		})
	}
}

// TestStatusLifecycle_RefusalClassification is AC 11's classification
// half: the step classification, the precondition merge class with a
// status_lifecycle_violation detail, and no new merge class.
func TestStatusLifecycle_RefusalClassification(t *testing.T) {
	f, _ := lcFix(t, false)
	f.git.files[ulBase+":"+ulTask] = ulDoc("Task", "Completed")
	f.git.files[ulBound+":"+ulTask] = ulDoc("Task", "Completed")
	f.run.CommitMeta["status"] = "Pending"
	if err := f.merge(); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}
	requireMergeFailedRoute(t, f, false)
	if len(f.git.merges) != 0 {
		t.Fatal("merged")
	}
	review := f.latestExec("review")
	if review.ErrorDetail == nil || review.ErrorDetail.Classification != domain.FailureStatusLifecycle ||
		!strings.Contains(review.ErrorDetail.Message, "status_lifecycle_violation: "+ulTask+": illegal_transition (Task Completed -> Pending)") {
		t.Fatalf("review error detail = %+v", review.ErrorDetail)
	}
	po := f.primaryOutcome()
	if po == nil || po.FailureClass != domain.MergeFailurePrecondition || !strings.HasPrefix(po.FailureDetail, "status_lifecycle_violation:") {
		t.Fatalf("primary outcome = %+v", po)
	}
	if domain.FailureStatusLifecycle.IsRetryable() {
		t.Fatal("status_lifecycle_violation must not be retryable")
	}
	want := []domain.MergeFailureClass{domain.MergeFailureUnknown, domain.MergeFailureConflict, domain.MergeFailureBranchProtection,
		domain.MergeFailurePrecondition, domain.MergeFailureAuth, domain.MergeFailureNetwork, domain.MergeFailureRemoteUnavailable}
	if !reflect.DeepEqual(domain.ValidMergeFailureClasses(), want) {
		t.Fatalf("ValidMergeFailureClasses changed: %v (a new class needs a migration)", domain.ValidMergeFailureClasses())
	}
}

// scValidator is a CrossArtifactValidator that is also a
// StatusChangeValidator.
type scValidator struct {
	res   domain.ValidationResult
	err   error
	calls []string
}

func (v *scValidator) Validate(context.Context, string) domain.ValidationResult {
	return domain.ValidationResult{Status: "passed"}
}

func (v *scValidator) ValidateStatusChange(_ context.Context, p string, s domain.ArtifactStatus) (domain.ValidationResult, error) {
	v.calls = append(v.calls, p+"="+string(s))
	return v.res, v.err
}

type plainValidator struct{}

func (plainValidator) Validate(context.Context, string) domain.ValidationResult {
	return domain.ValidationResult{Status: "failed", Errors: []domain.ValidationError{{RuleID: "SC-002", Severity: "error"}}}
}

// TestStatusLifecycle_SCGate is AC 12.
func TestStatusLifecycle_SCGate(t *testing.T) {
	epicFix := func(t *testing.T) *bindFix {
		f, _ := lcFix(t, true)
		f.run.TaskPath = "epics/e.md"
		f.git.files[ulBase+":epics/e.md"] = ulDoc("Epic", "Pending")
		f.git.files[ulBound+":epics/e.md"] = ulDoc("Epic", "Pending")
		f.git.files[ulMerge+":epics/e.md"] = ulDoc("Epic", "Completed")
		return f
	}
	t.Run("blocking finding refuses", func(t *testing.T) {
		f := epicFix(t)
		v := &scValidator{res: domain.ValidationResult{Status: "failed", Errors: []domain.ValidationError{{RuleID: "SC-002", Severity: "error"}}}}
		f.orch.WithValidator(v)
		_, le := planFor(t, f, "Completed")
		if le == nil || len(le.Violations) != 1 || le.Violations[0].Reason != LifecycleStatusConflict ||
			!reflect.DeepEqual(le.Violations[0].Rules, []string{"SC-002"}) {
			t.Fatalf("violations = %+v, want status_conflict [SC-002]", le)
		}
		if !reflect.DeepEqual(v.calls, []string{"epics/e.md=Completed"}) {
			t.Fatalf("calls = %v", v.calls)
		}
	})
	t.Run("warnings only: the merge proceeds", func(t *testing.T) {
		f := epicFix(t)
		f.orch.WithValidator(&scValidator{res: domain.ValidationResult{Status: "warnings", Warnings: []domain.ValidationError{{RuleID: "SC-002", Severity: "warning"}}}})
		if err := f.merge(); err != nil || f.run.Status != domain.RunStatusCompleted {
			t.Fatalf("MergeRunBranch = %v, run %s", err, f.run.Status)
		}
	})
	t.Run("a validator error keeps the run in committing", func(t *testing.T) {
		f := epicFix(t)
		f.orch.WithValidator(&scValidator{err: errors.New("projection unavailable")})
		if err := f.merge(); err == nil || f.run.Status != domain.RunStatusCommitting || len(f.git.merges) != 0 {
			t.Fatalf("MergeRunBranch = %v, run %s, merges %d", err, f.run.Status, len(f.git.merges))
		}
	})
	t.Run("a validator without the interface skips the gate", func(t *testing.T) {
		f := epicFix(t)
		f.orch.WithValidator(plainValidator{})
		if _, le := planFor(t, f, "Completed"); le != nil {
			t.Fatalf("refused: %v", le)
		}
	})
	t.Run("an identity transition is not checked", func(t *testing.T) {
		f := epicFix(t)
		f.git.files[ulBase+":epics/e.md"] = ulDoc("Epic", "Completed")
		f.git.files[ulBound+":epics/e.md"] = ulDoc("Epic", "Completed")
		v := &scValidator{err: errors.New("must not be called")}
		f.orch.WithValidator(v)
		if _, le := planFor(t, f, "Completed"); le != nil || len(v.calls) != 0 {
			t.Fatalf("refused %v / calls %v", le, v.calls)
		}
	})
}

// TestStatusLifecycle_PermanentReadErrors is AC 22 (DC-6): a read the
// author's content makes impossible is a permanent refusal, never an
// endless retry.
func TestStatusLifecycle_PermanentReadErrors(t *testing.T) {
	tooLarge := &git.GitError{Kind: git.ErrKindPermanent, Op: "read_file", Message: "blob exceeds read limit",
		Err: &git.BlobTooLargeError{Ref: ulBound, Path: "tasks/u.md", Size: 5 << 20, Limit: 4 << 20}}
	permanent := &git.GitError{Kind: git.ErrKindPermanent, Op: "read_file", Message: "fatal: not a blob"}
	notFound := &git.GitError{Kind: git.ErrKindNotFound, Op: "read_file", Message: "does not exist"}
	for _, c := range []struct {
		name string
		err  error
	}{
		{"blob too large", tooLarge},
		{"permanent read error", permanent},
		{"not found for a diff-listed path", notFound},
	} {
		for _, legacy := range []bool{false, true} {
			name := c.name + "/publish"
			if legacy {
				name = c.name + "/legacy"
			}
			t.Run(name, func(t *testing.T) {
				f, g := lcFix(t, legacy)
				f.git.files[ulBase+":tasks/u.md"] = ulDoc("Task", "Pending")
				f.diff(git.FileDiff{Path: "tasks/u.md", Status: "modified"})
				g.readErr[ulBound+":tasks/u.md"] = c.err
				_, le := planFor(t, f, "Completed")
				if got := reasons(le); !reflect.DeepEqual(got, []string{LifecycleUnreadableArtifact}) {
					t.Fatalf("reasons = %v, want [unreadable_artifact]", got)
				}
				if err := f.merge(); err != nil {
					t.Fatalf("MergeRunBranch: %v", err)
				}
				if f.run.Status == domain.RunStatusCommitting || len(f.git.merges) != 0 {
					t.Fatalf("run %s, merges %d; want routed, no merge", f.run.Status, len(f.git.merges))
				}
				requireMergeFailedRoute(t, f, legacy)
			})
		}
	}
	t.Run("no merge-base (unrelated history)", func(t *testing.T) {
		f, g := lcFix(t, true)
		g.mbErr = &git.GitError{Kind: git.ErrKindPermanent, Op: "merge-base", Message: ""}
		if err := f.merge(); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		po := f.primaryOutcome()
		if f.run.Status != domain.RunStatusFailed || po == nil || !strings.Contains(po.FailureDetail, LifecycleUnreadableDiff) {
			t.Fatalf("run %s, outcome %+v; want failed with unreadable_diff", f.run.Status, po)
		}
	})
}

// TestStatusLifecycle_MergeResultMismatchUnit pins V1's expectation rule on
// the fake: the owned path must hold the planned status; a judged path
// whose status the plan leaves alone must hold main's.
func TestStatusLifecycle_MergeResultMismatchUnit(t *testing.T) {
	t.Run("owned path", func(t *testing.T) {
		f, _ := lcFix(t, true)
		f.git.files[ulMerge+":"+ulTask] = ulDoc("Task", "Pending")
		if err := f.merge(); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		po := f.primaryOutcome()
		if po == nil || !strings.Contains(po.FailureDetail, "merge_result_mismatch (Task Completed -> Pending)") {
			t.Fatalf("outcome = %+v", po)
		}
		if len(f.git.pushes) != 0 || len(f.git.resets) != 1 {
			t.Fatalf("pushes %v, resets %v; want no push and one take-back", f.git.pushes, f.git.resets)
		}
	})
	t.Run("non-owned path follows main", func(t *testing.T) {
		f, _ := lcFix(t, true)
		f.diff(git.FileDiff{Path: "tasks/u.md", Status: "modified"})
		f.git.files[ulBase+":tasks/u.md"] = ulDoc("Task", "Pending")
		f.git.files[ulBound+":tasks/u.md"] = ulDoc("Task", "Pending") + "body edit\n"
		f.git.files[ulMain+":tasks/u.md"] = ulDoc("Task", "Cancelled")
		f.git.files[ulMerge+":tasks/u.md"] = ulDoc("Task", "Cancelled") + "body edit\n"
		if err := f.merge(); err != nil || f.run.Status != domain.RunStatusCompleted {
			t.Fatalf("MergeRunBranch = %v, run %s (outcome %+v)", err, f.run.Status, f.primaryOutcome())
		}
	})
	t.Run("a transient read retries", func(t *testing.T) {
		f, g := lcFix(t, true)
		g.readErr[ulMerge+":"+ulTask] = &git.GitError{Kind: git.ErrKindTransient, Op: "read_file", Message: "repository locked"}
		if err := f.merge(); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		if f.run.Status != domain.RunStatusCommitting || len(f.git.pushes) != 0 {
			t.Fatalf("run %s, pushes %v; want committing, no push", f.run.Status, f.git.pushes)
		}
	})
	t.Run("a replay (merge adds nothing) is not re-read", func(t *testing.T) {
		f, _ := lcFix(t, true)
		f.git.mergeRes = git.MergeResult{SHA: ulMain, BaseSHA: ulMain}
		delete(f.git.files, ulMerge+":"+ulTask)
		if err := f.merge(); err != nil || f.run.Status != domain.RunStatusCompleted {
			t.Fatalf("MergeRunBranch = %v, run %s (outcome %+v)", err, f.run.Status, f.primaryOutcome())
		}
	})
}

func TestStatusLifecycleError_Rendering(t *testing.T) {
	var vs []StatusLifecycleViolation
	for i := 0; i < 23; i++ {
		vs = append(vs, StatusLifecycleViolation{Path: "tasks/t.md", Type: "Task", From: "Pending", To: "Completed", Reason: LifecycleNonOwnedStatusChange})
	}
	le := &StatusLifecycleError{Violations: vs}
	if msg := le.Error(); !strings.HasPrefix(msg, "status_lifecycle_violation: tasks/t.md: non_owned_status_change (Task Pending -> Completed); ") ||
		!strings.HasSuffix(msg, "; and 3 more") || strings.Count(msg, "non_owned_status_change") != 20 {
		t.Fatalf("Error() = %q", msg)
	}
	odd := &StatusLifecycleError{Violations: []StatusLifecycleViolation{
		{Path: "tasks/t.md", Type: "Task", From: "", To: "Done\nINJECTED: yes", Reason: LifecycleNonOwnedStatusChange},
		{Path: "epics/e.md", Type: "Epic", From: "Pending", To: "Completed", Reason: LifecycleStatusConflict, Rules: []string{"SC-002"}},
		{Reason: LifecycleUnreadableDiff},
	}, Context: "the local main was restored"}
	msg := odd.Error()
	if strings.Contains(msg, "\n") || !strings.Contains(msg, `(Task "" -> "Done\nINJECTED: yes")`) ||
		!strings.Contains(msg, "[SC-002]") || !strings.Contains(msg, "(diff): unreadable_diff") ||
		!strings.HasSuffix(msg, "; the local main was restored") {
		t.Fatalf("Error() = %q", msg)
	}
	if !errors.Is(odd, ErrStatusLifecycleViolation) {
		t.Fatal("errors.Is(ErrStatusLifecycleViolation) must hold")
	}
	if class, detail := classifyMergeFailure(odd); class != domain.MergeFailurePrecondition || detail != msg {
		t.Fatalf("classifyMergeFailure = %s %q", class, detail)
	}
}

// With the lifecycle on, a workflow planning run whose body commits a
// status its applies_to types do not declare is refused before any branch
// or run record exists (workflow.Service would refuse it only inside the
// branch write, leaving a pending run).
func TestStatusLifecycle_WorkflowPlanningRunCommitStatus(t *testing.T) {
	body := strings.Replace(validWorkflowBody, "        next_step: end\n", "        next_step: end\n        commit:\n          status: Shipped\n", 1)
	for _, on := range []bool{true, false} {
		g := &workflowReadGit{}
		w := &mockWorkflowWriter{}
		store := &mockRunStore{}
		orch := workflowPlanningOrchestrator(w, workflowPlanningResolver(), store, &mockEventEmitter{}, g)
		if on {
			orch.WithStatusLifecycle(domain.DefaultStatusLifecycle())
		}
		_, err := orch.StartWorkflowPlanningRun(context.Background(), "new-workflow", body)
		if on {
			if spineErrCode(err) != domain.ErrValidationFailed || store.createdRun != nil || len(g.created) != 0 || w.createCalls != 0 {
				t.Fatalf("lifecycle on: err=%v run=%v branches=%v writer=%d", err, store.createdRun, g.created, w.createCalls)
			}
			continue
		}
		if err != nil || w.createCalls != 1 {
			t.Fatalf("lifecycle off (zero value): err=%v writer=%d", err, w.createCalls)
		}
	}
}
