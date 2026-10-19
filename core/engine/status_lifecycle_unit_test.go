package engine

// INIT-023 TASK-017 against the fake binder: the rule table (AC 7), fail
// closed on retryable errors (AC 9), the quoted-path guard (AC 10, AC 23),
// refusal classification (AC 11), the SC merge gate (AC 12) and the error
// classes of DC-6 (AC 22).

import (
	"context"
	"errors"
	"os/exec"
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

// lcGit is fakeBinderGit with injectable Diff / MergeBase / ReadFile errors:
// for every call (diffErr, mbErr) or for one pair "a b" (diffErrs, mbErrs).
type lcGit struct {
	*fakeBinderGit
	diffErr  error
	mbErr    error
	diffErrs map[string]error
	mbErrs   map[string]error
	readErr  map[string]error // "ref:path"
}

func (g *lcGit) Diff(ctx context.Context, a, b string) ([]git.FileDiff, error) {
	if err := g.diffErrs[a+" "+b]; err != nil || g.diffErr != nil {
		g.record("Diff " + a + " " + b)
		if err == nil {
			err = g.diffErr
		}
		return nil, err
	}
	return g.fakeBinderGit.Diff(ctx, a, b)
}

func (g *lcGit) MergeBase(ctx context.Context, a, b string) (string, error) {
	if err := g.mbErrs[a+" "+b]; err != nil || g.mbErr != nil {
		g.record("MergeBase " + a + " " + b)
		if err == nil {
			err = g.mbErr
		}
		return "", err
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
	g := &lcGit{fakeBinderGit: f.git, readErr: map[string]error{}, diffErrs: map[string]error{}, mbErrs: map[string]error{}}
	f.orch.git = g
	if err := f.orch.EnableMergeBinding(MergeBindingConfig{}); err != nil {
		t.Fatal(err)
	}
	f.orch.WithStatusLifecycle(domain.DefaultStatusLifecycle())
	f.run.CommitMeta = map[string]string{"status": "Completed", CommitMetaBoundSHA: ulBound,
		CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
	f.git.files[ulBound+":"+ulTask] = ulDoc("Task", "Pending")
	f.git.files[ulMain+":"+ulTask] = ulDoc("Task", "Pending")
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
		want     string // the expected reason; empty when accepted
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
			// A counted attempt (INIT-023 wave 4 integration, TASK-012's
			// retry budget): one failed, transient primary outcome, no
			// merge commit.
			if po := f.primaryOutcome(); len(f.store.mergeOutcomes) != 1 || po == nil || po.Attempts != 1 ||
				po.Status != domain.RepositoryMergeStatusFailed || !po.FailureClass.IsTransient() || po.MergeCommitSHA != "" {
				t.Fatalf("a transient failure must record one failed, transient attempt: %+v", f.store.mergeOutcomes)
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
	// perPath, when set, answers per projection key instead of res; a key
	// it does not list passes, as validation.Engine passes a path the
	// projection does not know.
	perPath map[string]domain.ValidationResult
}

func (v *scValidator) Validate(context.Context, string) domain.ValidationResult {
	return domain.ValidationResult{Status: "passed"}
}

func (v *scValidator) ValidateStatusChange(_ context.Context, p string, s domain.ArtifactStatus) (domain.ValidationResult, error) {
	v.calls = append(v.calls, p+"="+string(s))
	if v.perPath != nil {
		if r, ok := v.perPath[p]; ok {
			return r, v.err
		}
		return domain.ValidationResult{Status: "passed"}, v.err
	}
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
		f.git.diffs[ulMain+" "+ulMerge] = []git.FileDiff{{Path: "tasks/u.md", Status: "modified"}}
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

// TestStatusLifecycle_MergeResultRules pins V1's rules on the fake (review
// round 1): it judges Diff(main before the merge, merge result) relative to
// main — not the paths the plan listed — after checking that the merge was
// made against the merge-base the plan judged.
func TestStatusLifecycle_MergeResultRules(t *testing.T) {
	const retry = "retry"
	mainM := ulMain + " " + ulMerge
	transient := &git.GitError{Kind: git.ErrKindTransient, Op: "x", Message: "repository locked"}
	permanent := &git.GitError{Kind: git.ErrKindPermanent, Op: "x", Message: "fatal: bad object"}
	inMerge := func(f *bindFix, d ...git.FileDiff) { f.git.diffs[mainM] = d }
	sib := func(f *bindFix, onMain, inResult string) {
		inMerge(f, git.FileDiff{Path: "tasks/u.md", Status: "modified"})
		f.git.files[ulMain+":tasks/u.md"] = onMain
		f.git.files[ulMerge+":tasks/u.md"] = inResult
	}
	for _, c := range []struct {
		name string
		set  func(f *bindFix, g *lcGit)
		want string // "" merges, retry stays in committing, else a substring of the refusal
	}{
		{"a non-owned body edit keeps main's status", func(f *bindFix, _ *lcGit) {
			sib(f, ulDoc("Task", "Cancelled"), ulDoc("Task", "Cancelled")+"edit\n")
		}, ""},
		{"a non-owned status change the plan never saw", func(f *bindFix, _ *lcGit) {
			sib(f, ulDoc("Task", "Completed"), ulDoc("Task", "Pending"))
		}, "tasks/u.md: merge_result_mismatch (Task Completed -> Pending)"},
		{"a non-owned retype", func(f *bindFix, _ *lcGit) {
			sib(f, ulDoc("Task", "Completed"), ulDoc("Epic", "Completed"))
		}, "tasks/u.md: merge_result_mismatch (Task Completed -> Completed)"},
		{"a non-owned artifact that stops parsing", func(f *bindFix, _ *lcGit) {
			sib(f, ulDoc("Task", "Completed"), "---\nid: X-1\ntype: Task\nstatus: Completed\nstatus: Pending\n---\n")
		}, `tasks/u.md: merge_result_mismatch (Task Completed -> "")`},
		{"a non-owned artifact the projection would drop", func(f *bindFix, _ *lcGit) {
			// Type and status still read, but artifact.Parse refuses it (the
			// required title is gone): DiscoverChanges would delete its row.
			full := func(title string) string {
				return "---\nid: TASK-002\ntype: Task\n" + title + "status: Completed\n" +
					"epic: /initiatives/i/epics/e/epic.md\ninitiative: /initiatives/i/initiative.md\n---\n# TASK-002\n"
			}
			sib(f, full("title: Two\n"), full(""))
		}, "tasks/u.md: merge_result_mismatch (Task Completed -> Completed)"},
		{"a non-owned artifact deleted", func(f *bindFix, _ *lcGit) {
			inMerge(f, git.FileDiff{Path: "tasks/u.md", Status: "deleted"})
			f.git.files[ulMain+":tasks/u.md"] = ulDoc("Task", "Completed")
		}, `tasks/u.md: merge_result_mismatch (Task Completed -> "")`},
		{"a non-owned artifact renamed out of scope", func(f *bindFix, _ *lcGit) {
			inMerge(f, git.FileDiff{OldPath: "tasks/u.md", Path: "tasks/u.txt", Status: "renamed"})
			f.git.files[ulMain+":tasks/u.md"] = ulDoc("Task", "Completed")
		}, `tasks/u.md: merge_result_mismatch (Task Completed -> "")`},
		{"a non-owned rename with the status kept", func(f *bindFix, _ *lcGit) {
			inMerge(f, git.FileDiff{OldPath: "tasks/u.md", Path: "tasks/v.md", Status: "renamed"})
			f.git.files[ulMain+":tasks/u.md"] = ulDoc("Task", "Completed")
			f.git.files[ulMerge+":tasks/v.md"] = ulDoc("Task", "Completed")
		}, ""},
		{"an addition without an initial status", func(f *bindFix, _ *lcGit) {
			inMerge(f, git.FileDiff{Path: "tasks/n.md", Status: "added"})
			f.git.files[ulMerge+":tasks/n.md"] = ulDoc("Task", "Completed")
		}, `tasks/n.md: merge_result_mismatch (Task "" -> Completed)`},
		{"an addition with an initial status", func(f *bindFix, _ *lcGit) {
			inMerge(f, git.FileDiff{Path: "tasks/n.md", Status: "added"})
			f.git.files[ulMerge+":tasks/n.md"] = ulDoc("Task", "Draft")
		}, ""},
		{"a template is out of scope", func(f *bindFix, _ *lcGit) {
			inMerge(f, git.FileDiff{Path: "templates/x.md", Status: "modified"})
			f.git.files[ulMain+":templates/x.md"] = ulDoc("Task", "Completed")
			f.git.files[ulMerge+":templates/x.md"] = ulDoc("Task", "Pending")
		}, ""},
		{"the owned artifact over a status main changed", func(f *bindFix, _ *lcGit) {
			inMerge(f, git.FileDiff{Path: ulTask, Status: "modified"})
			f.git.files[ulMain+":"+ulTask] = ulDoc("Task", "Cancelled")
		}, ulTask + ": merge_result_mismatch (Task Pending -> Cancelled)"},
		{"main already holds the planned status", func(f *bindFix, _ *lcGit) {
			inMerge(f, git.FileDiff{Path: ulTask, Status: "modified"})
			f.git.files[ulMain+":"+ulTask] = ulDoc("Task", "Completed")
			f.git.files[ulMerge+":"+ulTask] = ulDoc("Task", "Completed") + "edit\n"
		}, ""},
		{"the owned artifact removed by the merge", func(f *bindFix, _ *lcGit) {
			inMerge(f, git.FileDiff{Path: ulTask, Status: "deleted"})
		}, ulTask + `: merge_result_mismatch (Task Completed -> "")`},
		{"another artifact renamed onto the owned path", func(f *bindFix, _ *lcGit) {
			delete(f.git.files, ulMain+":"+ulTask)
			inMerge(f, git.FileDiff{OldPath: "tasks/u.md", Path: ulTask, Status: "renamed"})
			f.git.files[ulMain+":tasks/u.md"] = ulDoc("Task", "Pending")
		}, `tasks/u.md: merge_result_mismatch (Task Pending -> ""); ` + ulTask + `: merge_result_mismatch (Task Pending -> "")`},
		{"main renamed the owned TaskPath after the merge-base", func(f *bindFix, _ *lcGit) {
			const moved = "tasks/t-moved.md"
			delete(f.git.files, ulMain+":"+ulTask)
			delete(f.git.files, ulMerge+":"+ulTask)
			f.git.diffs[ulBase+" "+ulMain] = []git.FileDiff{{OldPath: ulTask, Path: moved, Status: "renamed"}}
			f.git.files[ulMain+":"+moved] = ulDoc("Task", "Pending")
			inMerge(f, git.FileDiff{Path: moved, Status: "modified"})
			f.git.files[ulMerge+":"+moved] = ulDoc("Task", "Completed")
		}, ""},
		{"main's renames unreadable transiently", func(f *bindFix, g *lcGit) {
			delete(f.git.files, ulMain+":"+ulTask)
			g.diffErrs[ulBase+" "+ulMain] = transient
		}, retry},
		{"a quoted path in the merge diff", func(f *bindFix, _ *lcGit) {
			inMerge(f, git.FileDiff{Path: `"tasks/\303\251.md"`, Status: "added"})
		}, LifecycleUnparseablePath},
		{"a permanent read of the merge result", func(f *bindFix, g *lcGit) {
			sib(f, ulDoc("Task", "Pending"), "")
			g.readErr[ulMerge+":tasks/u.md"] = permanent
		}, "tasks/u.md: " + LifecycleUnreadableArtifact},
		{"main gained commits of the bound history", func(f *bindFix, _ *lcGit) {
			f.git.mergeBases[ulMain+" "+ulCasc] = sha40('9')
		}, retry},
		{"the merge's merge-base unreadable transiently", func(_ *bindFix, g *lcGit) {
			g.mbErrs[ulMain+" "+ulCasc] = transient
		}, retry},
		{"the merge's merge-base unreadable permanently", func(_ *bindFix, g *lcGit) {
			g.mbErrs[ulMain+" "+ulCasc] = permanent
		}, LifecycleUnreadableDiff},
		{"the merge diff unreadable transiently", func(_ *bindFix, g *lcGit) { g.diffErrs[mainM] = transient }, retry},
		{"the merge diff unreadable permanently", func(_ *bindFix, g *lcGit) { g.diffErrs[mainM] = permanent }, LifecycleUnreadableDiff},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, g := lcFix(t, true)
			c.set(f, g)
			if err := f.merge(); err != nil {
				t.Fatalf("MergeRunBranch: %v", err)
			}
			po := f.primaryOutcome()
			switch c.want {
			case "":
				if f.run.Status != domain.RunStatusCompleted || len(f.git.resets) != 0 {
					t.Fatalf("run %s, resets %v (outcome %+v); want completed", f.run.Status, f.git.resets, po)
				}
			case retry:
				if f.run.Status != domain.RunStatusCommitting || len(f.git.resets) != 1 || len(f.git.pushes) != 0 {
					t.Fatalf("run %s, resets %v, pushes %v (outcome %+v); want committing, one take-back, no push",
						f.run.Status, f.git.resets, f.git.pushes, po)
				}
			default:
				if f.run.Status != domain.RunStatusFailed || len(f.git.resets) != 1 || len(f.git.pushes) != 0 {
					t.Fatalf("run %s, resets %v, pushes %v (outcome %+v); want failed, one take-back, no push",
						f.run.Status, f.git.resets, f.git.pushes, po)
				}
				if po == nil || po.FailureClass != domain.MergeFailurePrecondition || !strings.Contains(po.FailureDetail, c.want) {
					t.Fatalf("outcome = %+v, want a precondition refusal containing %q", po, c.want)
				}
			}
		})
	}

	t.Run("a planned creation onto main's artifact", func(t *testing.T) {
		f, _ := lcFix(t, true)
		f.diff(git.FileDiff{Path: ulTask, Status: "added"}) // created by the run: O2
		inMerge(f, git.FileDiff{Path: ulTask, Status: "modified"})
		f.git.files[ulMain+":"+ulTask] = ulDoc("Task", "Cancelled")
		if err := f.merge(); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		if po := f.primaryOutcome(); f.run.Status != domain.RunStatusFailed || po == nil ||
			!strings.Contains(po.FailureDetail, ulTask+`: merge_result_mismatch (Task "" -> Cancelled)`) {
			t.Fatalf("run %s, outcome %+v; want refused", f.run.Status, po)
		}
	})
	t.Run("control: a planned creation main does not hold", func(t *testing.T) {
		f, _ := lcFix(t, true)
		f.diff(git.FileDiff{Path: ulTask, Status: "added"})
		delete(f.git.files, ulMain+":"+ulTask)
		inMerge(f, git.FileDiff{Path: ulTask, Status: "added"})
		if err := f.merge(); err != nil || f.run.Status != domain.RunStatusCompleted {
			t.Fatalf("MergeRunBranch = %v, run %s (outcome %+v)", err, f.run.Status, f.primaryOutcome())
		}
	})
}

// TestStatusLifecycle_NonCanonicalTaskPath (codex review, round 1
// follow-up): StartRun accepts a TaskPath spelled "./tasks/t.md" at the
// repository root (artifact.Service.Read trims only a leading "/"), while
// git diffs name the canonical "tasks/t.md". The run must still own it, and
// the cascade must rewrite it under the canonical name (CommitFilesOnto
// refuses a "." component, which would keep the run in committing).
func TestStatusLifecycle_NonCanonicalTaskPath(t *testing.T) {
	for _, spelled := range []string{"./" + ulTask, "/" + ulTask, "tasks//t.md"} {
		t.Run(spelled, func(t *testing.T) {
			f, _ := lcFix(t, true)
			f.run.TaskPath = spelled
			f.git.files[ulBase+":"+ulTask] = ulDoc("Task", "Pending")
			plan, le := planFor(t, f, "Completed")
			if le != nil {
				t.Fatalf("refused: %v", le)
			}
			if _, ok := plan.updates[ulTask]; !ok || len(plan.updates) != 1 {
				t.Fatalf("rewrites = %v, want exactly %s", plan.updates, ulTask)
			}
			if err := f.merge(); err != nil || f.run.Status != domain.RunStatusCompleted {
				t.Fatalf("MergeRunBranch = %v, run %s (outcome %+v)", err, f.run.Status, f.primaryOutcome())
			}
		})
	}
	t.Run("the branch completes its own TaskPath spelled ./", func(t *testing.T) {
		f, _ := lcFix(t, true)
		f.run.TaskPath = "./" + ulTask
		f.git.files[ulBase+":"+ulTask] = ulDoc("Task", "Pending")
		f.git.files[ulBound+":"+ulTask] = ulDoc("Task", "Completed")
		f.diff(git.FileDiff{Path: ulTask, Status: "modified"})
		if _, le := planFor(t, f, "Completed"); le != nil {
			t.Fatalf("refused: %v", le)
		}
	})
}

// TestStatusLifecycle_SCGateMainHolds: the status-conflict gate (O4) is not
// asked about a change main already holds (review round 1, P3): the merge
// lands no status change there.
func TestStatusLifecycle_SCGateMainHolds(t *testing.T) {
	epicFix := func(t *testing.T, onMain string) (*bindFix, *lcGit, *scValidator) {
		f, g := lcFix(t, true)
		f.run.TaskPath = "epics/e.md"
		f.git.files[ulBase+":epics/e.md"] = ulDoc("Epic", "Pending")
		f.git.files[ulBound+":epics/e.md"] = ulDoc("Epic", "Pending")
		f.git.files[ulMerge+":epics/e.md"] = ulDoc("Epic", "Completed")
		f.git.setHead("main", ulMain)
		f.git.files[ulMain+":epics/e.md"] = ulDoc("Epic", onMain)
		v := &scValidator{res: domain.ValidationResult{Status: "failed", Errors: []domain.ValidationError{{RuleID: "SC-002", Severity: "error"}}}}
		f.orch.WithValidator(v)
		return f, g, v
	}
	t.Run("main holds the target: not asked", func(t *testing.T) {
		f, _, v := epicFix(t, "Completed")
		if _, le := planFor(t, f, "Completed"); le != nil || len(v.calls) != 0 {
			t.Fatalf("refused %v / calls %v", le, v.calls)
		}
	})
	t.Run("main holds another status: asked", func(t *testing.T) {
		f, _, v := epicFix(t, "Pending")
		if _, le := planFor(t, f, "Completed"); le == nil || len(v.calls) != 1 {
			t.Fatalf("refused %v / calls %v; want status_conflict", le, v.calls)
		}
	})
	t.Run("main's head unreadable transiently: committing", func(t *testing.T) {
		f, _, _ := epicFix(t, "Completed")
		f.git.headErrs["main"] = &git.GitError{Kind: git.ErrKindTransient, Op: "x", Message: "repository locked"}
		if err := f.merge(); err == nil || f.run.Status != domain.RunStatusCommitting || len(f.git.merges) != 0 {
			t.Fatalf("MergeRunBranch = %v, run %s, merges %d", err, f.run.Status, len(f.git.merges))
		}
	})
	t.Run("main's file unreadable transiently: committing", func(t *testing.T) {
		f, g, _ := epicFix(t, "Completed")
		g.readErr[ulMain+":epics/e.md"] = &git.GitError{Kind: git.ErrKindTransient, Op: "x", Message: "repository locked"}
		if err := f.merge(); err == nil || f.run.Status != domain.RunStatusCommitting || len(f.git.merges) != 0 {
			t.Fatalf("MergeRunBranch = %v, run %s, merges %d", err, f.run.Status, len(f.git.merges))
		}
	})
	// Codex review (round 1 follow-up): main renamed the epic after the
	// fork, so the projection keys it under the new name; asking under the
	// old one passes (an unknown path), while V1 follows the rename and
	// lands Completed. The gate must ask under main's name.
	renamed := func(t *testing.T) (*bindFix, *scValidator) {
		f, _, v := epicFix(t, "Pending")
		delete(f.git.files, ulMain+":epics/e.md")
		f.git.files[ulMain+":epics/e2.md"] = ulDoc("Epic", "Pending")
		f.git.diffs[ulBase+" "+ulMain] = []git.FileDiff{{OldPath: "epics/e.md", Path: "epics/e2.md", Status: "renamed"}}
		v.perPath = map[string]domain.ValidationResult{"epics/e2.md": v.res}
		return f, v
	}
	t.Run("main renamed the artifact: asked under main's name", func(t *testing.T) {
		f, v := renamed(t)
		_, le := planFor(t, f, "Completed")
		if le == nil || len(le.Violations) != 1 || le.Violations[0].Reason != LifecycleStatusConflict ||
			!reflect.DeepEqual(v.calls, []string{"epics/e2.md=Completed"}) {
			t.Fatalf("violations %+v, calls %v; want status_conflict asked for epics/e2.md", le, v.calls)
		}
	})
	t.Run("main renamed it again before the merge: retried", func(t *testing.T) {
		f, v := renamed(t)
		v.perPath = map[string]domain.ValidationResult{} // the check passes
		const later = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb1"
		f.git.mergeRes = git.MergeResult{SHA: ulMerge, BaseSHA: later}
		f.git.parents[ulMerge] = []string{later, ulCasc}
		f.git.diffs[ulBase+" "+later] = []git.FileDiff{{OldPath: "epics/e.md", Path: "epics/e3.md", Status: "renamed"}}
		f.git.files[later+":epics/e3.md"] = ulDoc("Epic", "Pending")
		f.git.diffs[later+" "+ulMerge] = []git.FileDiff{{Path: "epics/e3.md", Status: "modified"}}
		f.git.files[ulMerge+":epics/e3.md"] = ulDoc("Epic", "Completed")
		if err := f.merge(); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		if f.run.Status != domain.RunStatusCommitting || len(f.git.resets) != 1 || len(f.git.pushes) != 0 {
			t.Fatalf("run %s, resets %v, pushes %v (outcome %+v); want committing, one take-back, no push",
				f.run.Status, f.git.resets, f.git.pushes, f.primaryOutcome())
		}
	})
	t.Run("main's head unknown: asked", func(t *testing.T) {
		f, _, v := epicFix(t, "Completed")
		delete(f.git.heads, "main")
		if _, le := planFor(t, f, "Completed"); le == nil || len(v.calls) != 1 {
			t.Fatalf("refused %v / calls %v; want status_conflict", le, v.calls)
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

// TestStatusLifecycle_TransientClassification pins DC-6's split: only a
// verdict git reached on the object is permanent.
func TestStatusLifecycle_TransientClassification(t *testing.T) {
	ctx := context.Background()
	wrap := func(err error) error {
		return &git.GitError{Kind: git.ErrKindPermanent, Op: "read_file", Message: "", Err: err}
	}
	runErr := func(name string, args ...string) error {
		return exec.Command(name, args...).Run() //nolint:gosec // G204: fixed test commands
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for _, c := range []struct {
		name      string
		ctx       context.Context
		err       error
		transient bool
	}{
		{"retryable", ctx, &git.GitError{Kind: git.ErrKindTransient, Op: "x", Message: "repository locked"}, true},
		{"non-git", ctx, errors.New("connection reset"), true},
		{"context done", cancelled, wrap(errors.New("signal: killed")), true},
		{"git could not start", ctx, wrap(runErr("spine-no-such-binary-017")), true},
		{"git killed by a signal", ctx, wrap(runErr("sh", "-c", "kill -9 $$")), true},
		{"git exited with an error", ctx, wrap(runErr("sh", "-c", "exit 128")), false},
		{"classified permanent", ctx, &git.GitError{Kind: git.ErrKindPermanent, Op: "read_file", Message: "fatal: not a blob"}, false},
		{"not found", ctx, &git.GitError{Kind: git.ErrKindNotFound, Op: "read_file", Message: "does not exist"}, false},
		{"blob too large", ctx, &git.GitError{Kind: git.ErrKindPermanent, Op: "read_file", Message: "blob exceeds read limit",
			Err: &git.BlobTooLargeError{Size: 5 << 20, Limit: 4 << 20}}, false},
	} {
		if got := lifecycleTransient(c.ctx, c.err); got != c.transient {
			t.Errorf("%s: transient = %v, want %v (%v)", c.name, got, c.transient, c.err)
		}
	}
}
