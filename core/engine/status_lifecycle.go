package engine

// Declared artifact-status lifecycle at the governed merge (INIT-023
// TASK-017; audit P3-21, Spine half).
//
// The defect. Artifact status was a plain front-matter field: a governed
// merge published whatever status the run branch carried. A run author
// could reopen a Completed task, complete a sibling task or an initiative,
// or retire an ADR, and the review had approved the run's work, not these
// side edits. The commit.status cascade made it worse: it stamped the
// outcome's status onto every Markdown artifact the branch ADDED, in
// standard runs too, so a task run that added a follow-up Task or an ADR
// minted it "Completed" (an ADR cannot even hold that status).
//
// The fix, opt-in through WithStatusLifecycle (zero value: today's
// behaviour), lives at the one choke point everything a run changes passes
// through — the governed merge — and judges the BOUND commit (TASK-005),
// so a push after the binding cannot slip a status edit past it:
//
//   - the "from" status is the merge-base's, never the branch's own value;
//   - the run OWNS its TaskPath and, in a planning run, the artifacts it
//     adds; it may change only their status, only along a declared edge
//     (domain.StatusLifecycle), and only when the workflow declares the
//     effect (commit.status);
//   - every other artifact's status, type and existence are frozen: a
//     changed status, a retype, a deletion, a rename out of scope or a
//     file that stops parsing is refused, and an artifact the run adds
//     without owning it must carry an initial status;
//   - the cascade rewrites only owned paths, and every rewrite is re-parsed
//     (R1) — the regex writer must produce exactly the status that was
//     judged, with nothing else changed;
//   - after the local merge and before any push, the merge result itself is
//     judged against main as it was before the merge (V1): whatever git's
//     merge did — a `*.md merge=union` attribute keeps two status lines
//     without a conflict; a criss-cross history merges against a virtual
//     base the plan never saw; main may gain commits of the bound history
//     between the judgement and the merge — the merge may change only the
//     status of the run's own artifacts, to the planned status, from the
//     judged one.
//
// Errors fail closed: a retryable git error (or a non-git store/transport
// error) keeps the run in committing for a retry; a read the author's
// content makes impossible — an oversized blob, a gitlink, a path the diff
// lists that does not read — is a permanent refusal (unreadable_artifact),
// never an endless retry (DC-6).

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// ErrStatusLifecycleViolation is matched (errors.Is) by every
// *StatusLifecycleError.
var ErrStatusLifecycleViolation = errors.New("status lifecycle violation")

// Stable reason codes of a StatusLifecycleViolation.
const (
	// LifecycleTypeChanged: an artifact's type differs between the
	// merge-base and the bound commit.
	LifecycleTypeChanged = "type_changed"
	// LifecycleUndeclaredStatus: the run's own artifact holds, at the
	// merge-base, a status its type does not declare, so no transition
	// from it is legal.
	LifecycleUndeclaredStatus = "undeclared_status"
	// LifecycleIllegalTransition: the run's own status change is not a
	// declared edge (From -> To).
	LifecycleIllegalTransition = "illegal_transition"
	// LifecycleStatusNotInType: an artifact the run creates would be
	// stamped with a status its type does not declare.
	LifecycleStatusNotInType = "status_not_in_type"
	// LifecycleNotAnArtifact: the run's TaskPath is not a lifecycle
	// artifact at the bound commit (no front matter, unparseable, an
	// unknown type, deleted or renamed away) although the workflow
	// declares a status effect or it was one at the merge-base.
	LifecycleNotAnArtifact = "not_an_artifact"
	// LifecycleStatusConflict: the run's status change trips a blocking
	// status-conflict rule (Rules names them).
	LifecycleStatusConflict = "status_conflict"
	// LifecycleNonOwnedStatusChange: the branch changes the status of an
	// artifact the run does not own.
	LifecycleNonOwnedStatusChange = "non_owned_status_change"
	// LifecycleIllegalInitialStatus: an artifact the branch adds, and
	// whose status no declared effect sets, does not carry an initial
	// status of its type.
	LifecycleIllegalInitialStatus = "illegal_initial_status"
	// LifecycleUnparseablePath: a diff path that is C-quoted and ends in
	// `.md"` (a GitOperator without NUL-delimited diffs); it cannot be
	// judged, so it is refused.
	LifecycleUnparseablePath = "unparseable_path"
	// LifecycleStatusChangeWithoutEffect: the branch changes its own
	// artifact's status although the workflow declares no status effect.
	LifecycleStatusChangeWithoutEffect = "status_change_without_effect"
	// LifecycleNonOwnedRemoval: an artifact the run does not own existed
	// at the merge-base and is deleted, renamed out of the artifact
	// scope, or no longer parses (a duplicate key, an unknown type, a
	// symlink or gitlink in its place, a front matter the projection
	// rejects).
	LifecycleNonOwnedRemoval = "non_owned_removal"
	// LifecycleStatusRewriteFailed: the cascade's rewrite of an owned
	// artifact does not re-parse to exactly the judged status with
	// nothing else changed (an exotic status encoding the byte-preserving
	// writer cannot handle).
	LifecycleStatusRewriteFailed = "status_rewrite_failed"
	// LifecycleUnreadableArtifact: a path the diff lists cannot be read at
	// the merge-base or the bound commit — or, for the merge-result check,
	// on main before the merge or in the merge result — for a reason a
	// retry cannot change (too large, not a blob, not found).
	LifecycleUnreadableArtifact = "unreadable_artifact"
	// LifecycleUnreadableDiff: the bound diff itself cannot be computed
	// for a reason a retry cannot change (no merge-base with main — an
	// unrelated history — or a permanent merge-base or diff failure).
	// Added by the implementation (not in the design's table): without it
	// such a run would sit in committing forever.
	LifecycleUnreadableDiff = "unreadable_diff"
	// LifecycleMergeResultMismatch: the merge changes, relative to main
	// before the merge, something the lifecycle did not judge (V1): a
	// status, type or existence of an artifact the run does not own, the
	// run's own artifact to other than the planned status or from other
	// than the judged one, or a new artifact without an initial status.
	// From is the status V1 expected and To the status it found — in the
	// merge result, or on main for the judged from-status of the run's own
	// artifact; "" stands for "no lifecycle artifact" (removed, never
	// there, or no longer parsing) and, as the expectation for an artifact
	// the merge adds, for "an initial status of its type".
	LifecycleMergeResultMismatch = "merge_result_mismatch"
)

// StatusLifecycleViolation is one refused change. Path is the
// repository-relative path; for a removal it is the path at the merge-base.
type StatusLifecycleViolation struct {
	Path     string
	Type     domain.ArtifactType
	From, To domain.ArtifactStatus
	Reason   string
	Rules    []string // SC rule IDs for reason status_conflict
}

// StatusLifecycleError is the refusal of a governed merge by the status
// lifecycle. It is permanent: the run is routed like any other merge
// refusal (classification status_lifecycle_violation, merge-outcome class
// precondition).
type StatusLifecycleError struct {
	Violations []StatusLifecycleViolation
	// Context, when set, is appended to Error(): for a merge-result
	// refusal, what happened to the local target branch.
	Context string
}

// maxReportedViolations bounds Error()'s list; the rest is counted.
const maxReportedViolations = 20

// Error renders "status_lifecycle_violation: <path>: <reason> (<type>
// <from> -> <to>); …", the first 20 violations, then "and N more". Paths
// and statuses are author-controlled text: anything that is not short,
// printable, single-line text is quoted.
func (e *StatusLifecycleError) Error() string {
	var b strings.Builder
	b.WriteString("status_lifecycle_violation: ")
	for i, v := range e.Violations {
		if i == maxReportedViolations {
			fmt.Fprintf(&b, "; and %d more", len(e.Violations)-maxReportedViolations)
			break
		}
		if i > 0 {
			b.WriteString("; ")
		}
		path := v.Path
		if path == "" {
			path = "(diff)"
		}
		fmt.Fprintf(&b, "%s: %s", lifecycleText(path, 256), v.Reason)
		if v.Type != "" || v.From != "" || v.To != "" {
			fmt.Fprintf(&b, " (%s %s -> %s)", lifecycleText(string(v.Type), 64),
				lifecycleText(string(v.From), 64), lifecycleText(string(v.To), 64))
		}
		if len(v.Rules) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(v.Rules, ", "))
		}
	}
	if len(e.Violations) == 0 {
		b.WriteString("refused")
	}
	if e.Context != "" {
		b.WriteString("; ")
		b.WriteString(e.Context)
	}
	return b.String()
}

// Is makes errors.Is(err, ErrStatusLifecycleViolation) hold.
func (e *StatusLifecycleError) Is(target error) bool { return target == ErrStatusLifecycleViolation }

// lifecycleText renders author-controlled text for logs and outcome rows:
// "" as `""`, short printable single-line text as is, anything else quoted
// and cut to limit bytes.
func lifecycleText(s string, limit int) string {
	if s == "" {
		return `""`
	}
	plain := utf8.ValidString(s) && len(s) <= limit
	if plain {
		for _, r := range s {
			if !unicode.IsPrint(r) {
				plain = false
				break
			}
		}
	}
	if plain {
		return s
	}
	if len(s) > limit {
		s = s[:limit]
	}
	return strconv.QuoteToASCII(s)
}

// lifecyclePlan is what planStatusLifecycle decided for one bound diff.
type lifecyclePlan struct {
	// base is the merge-base the bound diff was judged against; V1
	// requires the merge to have been made against it.
	base string
	// updates are the owned rewrites (repository path → content), each
	// verified by R1.
	updates map[string]string
	// owned lists the status changes the plan allows the merge to land:
	// the run's own artifacts whose status the merge sets (V1). Every
	// other artifact's status, type and existence must reach the merge
	// result as main holds them, and an artifact the merge adds must carry
	// an initial status.
	owned []ownedChange
}

// ownedChange is one status change the merge result may land (V1): the
// run's own artifact at path (repository path at the bound commit), of
// type typ, set to final — from the merge-base status from, or, when
// isNew, created (no lifecycle artifact at the merge-base).
type ownedChange struct {
	path  string
	typ   domain.ArtifactType
	from  domain.ArtifactStatus
	final domain.ArtifactStatus
	isNew bool
	// scAt is the name on main the status-conflict gate (O4) judged the
	// change under — main renames the artifact after the fork, the
	// projection follows — or "" when the gate did not run. V1 retries a
	// merge that lands it under another name.
	scAt string
}

// diffItem is one diff entry as the lifecycle sees it: the path at the
// bound commit (tip, "" when removed) and at the merge-base (base, "" when
// new; the old path of a rename).
type diffItem struct {
	tip, base string
	status    string
}

// side is one side of a judged artifact.
type side struct {
	read    bool // the side was read (it exists in the diff's view)
	ok      bool // a lifecycle artifact
	typ     domain.ArtifactType
	status  domain.ArtifactStatus
	parses  bool // artifact.Parse accepts it (the projection keeps a row)
	content []byte
}

func readSide(content []byte, path string) side {
	t, s, ok := artifact.LifecycleFields(content)
	sd := side{read: true, ok: ok, typ: t, status: s, content: content}
	if ok {
		_, err := artifact.Parse(path, content)
		sd.parses = err == nil
	}
	return sd
}

// lifecycleTransient classifies a git read error (DC-6). Transient — the
// caller returns it and the run stays in committing — are a retryable
// *git.GitError, any non-git (store, transport) error, and every sign that
// git itself did not get to judge the object: the context is done, git
// could not start (*exec.Error, *fs.PathError) or was killed by a signal.
// The CLI client reports all of those as an ErrKindPermanent GitError with
// empty stderr, and treating them as a verdict on the author's content
// would refuse — route back, or fail — a run over a shutdown or a missing
// binary. Every other git error is permanent: an oversized blob or output,
// a not-found for a path the diff lists, a git that ran to completion and
// refused the object (a gitlink, a tree).
func lifecycleTransient(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	var gitErr *git.GitError
	if !errors.As(err, &gitErr) {
		return true
	}
	if gitErr.IsRetryable() {
		return true
	}
	var execErr *exec.Error
	var pathErr *fs.PathError
	if errors.As(err, &execErr) || errors.As(err, &pathErr) {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && !exitErr.Exited() {
		return true
	}
	return false
}

// cQuotedMarkdown reports a diff path that git C-quoted (core.quotePath)
// and that names a Markdown file (`"<path>.md"`). Rule Q (and Q′ for the old
// side of a rename): a transitional guard for GitOperator implementations
// whose Diff is not NUL-delimited (adapters/git has been since INIT-023
// TASK-003, so for the CLI client it is dead code).
func cQuotedMarkdown(p string) bool {
	return len(p) >= 2 && strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `.md"`)
}

// planner carries one planStatusLifecycle evaluation.
type planner struct {
	o         *Orchestrator
	run       *domain.Run
	l         *domain.StatusLifecycle
	scope     artifact.PathScope
	newStatus domain.ArtifactStatus
	bound     string
	base      string
	taskRepo  string
	taskIsMd  bool // a non-.md TaskPath (workflows/<id>.yaml) is never an artifact
	planning  bool
	viol      []StatusLifecycleViolation
	plan      *lifecyclePlan
	scChecks  []scCheck
	mainTip   string // main's tip for O4's "already holds" check, resolved on first use
}

// scCheck is one O4 candidate, evaluated once nothing else was refused.
type scCheck struct {
	path, key string
	typ       domain.ArtifactType
	from, to  domain.ArtifactStatus
}

func (p *planner) refuse(v StatusLifecycleViolation) { p.viol = append(p.viol, v) }

// read reads path at ref. A transient failure is returned as err; a
// permanent one is recorded as unreadable_artifact and reported as
// (side{}, false, nil). absentOK makes a not-found an absent side
// (side{read:false}) instead — only for a path the diff does not list.
func (p *planner) read(ctx context.Context, ref, path string, absentOK bool) (side, bool, error) {
	content, err := p.o.git.ReadFile(ctx, ref, path)
	if err == nil {
		return readSide(content, path), true, nil
	}
	if lifecycleTransient(ctx, err) {
		return side{}, false, fmt.Errorf("status lifecycle: read %s at %s: %w", path, ref, err)
	}
	var gitErr *git.GitError
	if absentOK && errors.As(err, &gitErr) && gitErr.Kind == git.ErrKindNotFound {
		return side{}, true, nil
	}
	p.refuse(StatusLifecycleViolation{Path: path, Reason: LifecycleUnreadableArtifact})
	return side{}, false, nil
}

// planStatusLifecycle evaluates the bound diff base..bound (TASK-017 §5
// with the design-check rows). It returns the owned paths to rewrite —
// with the content read at the bound commit, each rewrite verified — and
// the V1 expectations, or a *StatusLifecycleError listing every
// violation. A transient read error is returned as-is (the caller keeps
// the run in committing). scGate runs the blocking status-conflict check
// (O4); the replay recomputation (thisRunsCascadeMerge) leaves it off,
// because it only needs the cascade commit's content.
func (o *Orchestrator) planStatusLifecycle(ctx context.Context, run *domain.Run, newStatus, bound, base string, scGate bool) (*lifecyclePlan, error) {
	p := &planner{
		o: o, run: run, l: o.lifecycle, scope: o.artifactScope,
		newStatus: domain.ArtifactStatus(newStatus), bound: bound, base: base,
		taskRepo: o.artifactScope.RepoPath(run.TaskPath),
		planning: run.Mode == domain.RunModePlanning,
		plan:     &lifecyclePlan{base: base, updates: map[string]string{}},
	}
	p.taskIsMd = strings.HasSuffix(p.taskRepo, ".md")

	diffs, err := o.git.Diff(ctx, base, bound)
	if err != nil {
		if lifecycleTransient(ctx, err) {
			return nil, fmt.Errorf("status lifecycle: diff %s..%s: %w", base, bound, err)
		}
		return nil, &StatusLifecycleError{Violations: []StatusLifecycleViolation{{Reason: LifecycleUnreadableDiff}}}
	}

	taskAtTip, taskTouched := false, false
	for _, d := range diffs {
		if cQuotedMarkdown(d.Path) {
			p.refuse(StatusLifecycleViolation{Path: d.Path, Reason: LifecycleUnparseablePath})
			continue
		}
		if d.Status == "renamed" && cQuotedMarkdown(d.OldPath) {
			p.refuse(StatusLifecycleViolation{Path: d.OldPath, Reason: LifecycleUnparseablePath})
			continue
		}
		it := diffItem{status: d.Status}
		switch d.Status {
		case "added":
			it.tip = d.Path
		case "deleted":
			it.base = d.Path
		case "renamed":
			it.tip, it.base = d.Path, d.OldPath
		default: // modified (and a type change, which the client reports as modified)
			it.tip, it.base = d.Path, d.Path
		}
		if p.isTaskPath(it.tip) {
			taskAtTip = true
		}
		if p.isTaskPath(it.tip) || p.isTaskPath(it.base) {
			taskTouched = true
		}
		if err := p.judge(ctx, it); err != nil {
			return nil, err
		}
	}
	if err := p.judgeTaskPath(ctx, taskAtTip, taskTouched); err != nil {
		return nil, err
	}

	if len(p.viol) == 0 && scGate {
		if err := p.runSCChecks(ctx); err != nil {
			return nil, err
		}
	}
	if len(p.viol) > 0 {
		return nil, &StatusLifecycleError{Violations: p.viol}
	}
	return p.plan, nil
}

// isTaskPath reports whether a repository path is the run's (Markdown)
// TaskPath.
func (p *planner) isTaskPath(repoPath string) bool {
	return p.taskIsMd && repoPath != "" && repoPath == p.taskRepo
}

// judge applies the rules to one diff entry.
func (p *planner) judge(ctx context.Context, it diffItem) error {
	_, tipIn := p.scope.GovernedArtifactKey(it.tip)
	_, baseIn := p.scope.GovernedArtifactKey(it.base)
	isTask := p.isTaskPath(it.tip)
	if it.tip == "" {
		tipIn = false
	}
	if it.base == "" {
		baseIn = false
	}
	if !tipIn && !isTask && !baseIn {
		return nil // out of scope on both sides: the projection never reads it
	}
	if (it.tip == "" || (!tipIn && !isTask)) && p.isTaskPath(it.base) {
		return nil // the TaskPath removed: judged once, by judgeTaskPath
	}

	var base side
	if it.base != "" && (baseIn || p.isTaskPath(it.base)) {
		sd, ok, err := p.read(ctx, p.base, it.base, false)
		if err != nil || !ok {
			return err
		}
		base = sd
	}

	if it.tip == "" || (!tipIn && !isTask) {
		// Removed: deleted, or renamed to a path out of scope (outside
		// artifacts_dir, under templates/, not .md, unsafe).
		if base.ok {
			p.refuse(StatusLifecycleViolation{Path: it.base, Type: base.typ, From: base.status, Reason: LifecycleNonOwnedRemoval})
		}
		return nil
	}
	if isTask && it.base != "" && !p.isTaskPath(it.base) && base.ok {
		// A non-owned artifact renamed onto the TaskPath (DC-7): it is
		// judged below as the run's own artifact with its merge-base
		// status, and its old identity is gone — a removal (N3).
		p.refuse(StatusLifecycleViolation{Path: it.base, Type: base.typ, From: base.status, Reason: LifecycleNonOwnedRemoval})
	}

	tip, ok, err := p.read(ctx, p.bound, it.tip, false)
	if err != nil || !ok {
		return err
	}
	if isTask || (p.planning && it.status == "added" && tipIn) {
		p.judgeOwned(it, isTask, base, tip)
	} else {
		p.judgeNonOwned(it, base, tip)
	}
	return nil
}

// judgeNonOwned is rows N1, N2 and N3.
func (p *planner) judgeNonOwned(it diffItem, base, tip side) {
	if base.ok {
		switch {
		case !tip.ok:
			p.refuse(StatusLifecycleViolation{Path: it.tip, Type: base.typ, From: base.status, Reason: LifecycleNonOwnedRemoval})
		case tip.typ != base.typ:
			p.refuse(StatusLifecycleViolation{Path: it.tip, Type: base.typ, From: base.status, To: tip.status, Reason: LifecycleTypeChanged})
		case tip.status != base.status:
			// Raw comparison (DC-9): an undeclared status already on main
			// is kept as it is, never "normalised" by a side edit.
			p.refuse(StatusLifecycleViolation{Path: it.tip, Type: base.typ, From: base.status, To: tip.status, Reason: LifecycleNonOwnedStatusChange})
		case base.parses && !tip.parses:
			// The projection drops the row of an artifact that stops
			// parsing (core/artifact DiscoverChanges): a removal by edit.
			p.refuse(StatusLifecycleViolation{Path: it.tip, Type: base.typ, From: base.status, To: tip.status, Reason: LifecycleNonOwnedRemoval})
		}
		return
	}
	// New for the lifecycle: added, renamed in from out of scope, or a
	// file that becomes an artifact. Not an artifact at the bound commit:
	// nothing to judge.
	if !tip.ok {
		return
	}
	if !p.l.IsInitial(tip.typ, tip.status) {
		p.refuse(StatusLifecycleViolation{Path: it.tip, Type: tip.typ, To: tip.status, Reason: LifecycleIllegalInitialStatus})
	}
}

// judgeOwned is rows O1 (with O1′ and O4), O2 (with O2′), O3 and O3′ for
// an owned path at the bound commit.
func (p *planner) judgeOwned(it diffItem, isTask bool, base, tip side) {
	if !tip.ok {
		// Not a lifecycle artifact at the bound commit. For the TaskPath
		// that is refused when the workflow declares an effect (O3) or it
		// was an artifact at the merge-base (O3′); a planning addition that
		// is not an artifact is ignored, as the projection ignores it.
		if isTask && (base.ok || p.newStatus != "") {
			p.refuse(StatusLifecycleViolation{Path: it.tip, Type: base.typ, From: base.status, To: p.newStatus, Reason: LifecycleNotAnArtifact})
		}
		return
	}
	if base.ok {
		if tip.typ != base.typ {
			p.refuse(StatusLifecycleViolation{Path: it.tip, Type: base.typ, From: base.status, To: tip.status, Reason: LifecycleTypeChanged})
			return
		}
		if p.newStatus == "" {
			// O1′: with no declared effect the run may not move its own
			// artifact either.
			if tip.status != base.status {
				p.refuse(StatusLifecycleViolation{Path: it.tip, Type: base.typ, From: base.status, To: tip.status, Reason: LifecycleStatusChangeWithoutEffect})
			}
			return
		}
		final := p.newStatus
		switch {
		case !p.l.IsState(base.typ, base.status):
			p.refuse(StatusLifecycleViolation{Path: it.tip, Type: base.typ, From: base.status, To: final, Reason: LifecycleUndeclaredStatus})
			return
		case !p.l.Allows(base.typ, base.status, final):
			p.refuse(StatusLifecycleViolation{Path: it.tip, Type: base.typ, From: base.status, To: final, Reason: LifecycleIllegalTransition})
			return
		}
		if base.status != final {
			if key, ok := p.scope.ArtifactKey(it.tip); ok {
				p.scChecks = append(p.scChecks, scCheck{path: it.tip, key: key, typ: base.typ, from: base.status, to: final})
			}
		}
		if !p.rewrite(it.tip, tip, final) {
			return
		}
		if final != base.status {
			p.own(ownedChange{path: it.tip, typ: tip.typ, from: base.status, final: final})
		}
		return
	}
	// O2 / O2′: new for the lifecycle (a planning addition, or a TaskPath
	// absent — or not an artifact — at the merge-base).
	if p.newStatus == "" {
		if !p.l.IsInitial(tip.typ, tip.status) {
			p.refuse(StatusLifecycleViolation{Path: it.tip, Type: tip.typ, To: tip.status, Reason: LifecycleIllegalInitialStatus})
		}
		return
	}
	final := p.newStatus
	if !p.l.IsState(tip.typ, final) {
		p.refuse(StatusLifecycleViolation{Path: it.tip, Type: tip.typ, From: tip.status, To: final, Reason: LifecycleStatusNotInType})
		return
	}
	if !p.rewrite(it.tip, tip, final) {
		return
	}
	p.own(ownedChange{path: it.tip, typ: tip.typ, final: final, isNew: true})
}

// judgeTaskPath covers the TaskPath when no diff entry lands on it: it is
// unchanged between the merge-base and the bound commit (judged as O1 /
// O1′ with the same content on both sides), or it was deleted or renamed
// away (O3′, and O3 when an effect is declared). A non-.md TaskPath (a
// workflow-lifecycle run's workflows/<id>.yaml) is skipped when no effect
// is declared and refused as not_an_artifact when one is.
func (p *planner) judgeTaskPath(ctx context.Context, atTip, touched bool) error {
	if !p.taskIsMd {
		if p.newStatus != "" {
			p.refuse(StatusLifecycleViolation{Path: p.taskRepo, To: p.newStatus, Reason: LifecycleNotAnArtifact})
		}
		return nil
	}
	if atTip {
		return nil // judged with its diff entry
	}
	if touched {
		// Deleted, or renamed away with nothing renamed onto it.
		base, ok, err := p.read(ctx, p.base, p.taskRepo, false)
		if err != nil || !ok {
			return err
		}
		if base.ok || p.newStatus != "" {
			p.refuse(StatusLifecycleViolation{Path: p.taskRepo, Type: base.typ, From: base.status, To: p.newStatus, Reason: LifecycleNotAnArtifact})
		}
		return nil
	}
	// Not in the diff: identical at the merge-base and the bound commit.
	tip, ok, err := p.read(ctx, p.bound, p.taskRepo, true)
	if err != nil || !ok {
		return err
	}
	if !tip.read {
		// Absent on both sides.
		if p.newStatus != "" {
			p.refuse(StatusLifecycleViolation{Path: p.taskRepo, To: p.newStatus, Reason: LifecycleNotAnArtifact})
		}
		return nil
	}
	base := tip
	base.content = nil
	p.judgeOwned(diffItem{tip: p.taskRepo, base: p.taskRepo, status: "unchanged"}, true, base, tip)
	return nil
}

// rewrite builds and verifies (R1) the cascade rewrite of an owned
// artifact to final. It records nothing and returns true when the
// artifact already holds final.
func (p *planner) rewrite(path string, tip side, final domain.ArtifactStatus) bool {
	if tip.status == final {
		return true
	}
	fail := func() bool {
		p.refuse(StatusLifecycleViolation{Path: path, Type: tip.typ, From: tip.status, To: final, Reason: LifecycleStatusRewriteFailed})
		return false
	}
	updated, err := rewriteStatusField(string(tip.content), string(final), path)
	if err != nil {
		return fail()
	}
	t, s, ok := artifact.LifecycleFields([]byte(updated))
	if !ok || t != tip.typ || s != final || !artifact.EqualExceptStatus(tip.content, []byte(updated)) {
		return fail()
	}
	p.plan.updates[path] = updated
	return true
}

// own records a status change V1 lets the merge land. An owned artifact
// whose status the plan leaves as it was at the merge-base, and an
// artifact the run adds with its authored initial status, need no entry:
// V1 holds them to main's state, or to an initial status, like any other.
func (p *planner) own(c ownedChange) { p.plan.owned = append(p.plan.owned, c) }

// runSCChecks is O4: an owned transition the orchestrator's validator
// refuses with a blocking (error-severity) status-conflict finding. A
// validator without StatusChangeValidator skips the gate; a validator
// error is transient.
//
// The check is made under main's current name for the artifact (codex
// review, round 1 follow-up): when main renamed it after the fork, the
// projection keys it there, and asking under the old name passes (no row),
// while V1 lets the merge land the change under main's name.
//
// A change main already holds is not asked about (review round 1): the
// merge then lands no status change there, so it cannot be the one that
// trips a status-conflict rule, while the projection the rules read may
// have moved since main took it. That is the resume of a run whose primary
// merge already landed when the replay is not recognised as one — the
// binding moved to a recovery-ledger commit (a child of the merged bound
// commit, so the merge-base is not the bound commit), or this run's merge
// lies beyond priorMergeWalkLimit — as well as a status another run set
// on main meanwhile.
func (p *planner) runSCChecks(ctx context.Context) error {
	if len(p.scChecks) == 0 || p.o.validator == nil {
		return nil
	}
	scv, ok := p.o.validator.(StatusChangeValidator)
	if !ok {
		return nil
	}
	for _, c := range p.scChecks {
		at, held, err := p.onMain(ctx, c)
		if err != nil {
			return err
		}
		for i := range p.plan.owned {
			if p.plan.owned[i].path == c.path {
				p.plan.owned[i].scAt = at
			}
		}
		if held {
			continue
		}
		key := c.key
		if at != c.path {
			// The projection keys the artifact under main's name; under the
			// old one the rules would find no row and pass.
			if k, ok := p.scope.ArtifactKey(at); ok {
				key = k
			}
		}
		res, err := scv.ValidateStatusChange(ctx, key, c.to)
		if err != nil {
			return fmt.Errorf("status lifecycle: status-conflict check for %s: %w", c.path, err)
		}
		var rules []string
		seen := map[string]bool{}
		for i := range res.Errors {
			if id := res.Errors[i].RuleID; res.Errors[i].Severity == "error" && !seen[id] {
				seen[id] = true
				rules = append(rules, id)
			}
		}
		if len(rules) > 0 {
			p.refuse(StatusLifecycleViolation{Path: c.path, Type: c.typ, From: c.from, To: c.to, Reason: LifecycleStatusConflict, Rules: rules})
		}
	}
	return nil
}

// onMain returns the name main's tip gives c's artifact — c.path, or the
// name main renamed it to since the merge-base — and whether main already
// holds it at c's target type and status there. A transient failure is
// returned; anything else that keeps it from telling (no binder, no
// branch, the file unreadable on main) is (c.path, false): the gate is
// asked under the bound commit's name, as before.
func (p *planner) onMain(ctx context.Context, c scCheck) (string, bool, error) {
	if p.o.binder == nil {
		return c.path, false, nil
	}
	if p.mainTip == "" {
		x, err := p.o.binder.ResolveBranchHead(ctx, authoritativeBranch)
		if err != nil {
			if lifecycleTransient(ctx, err) {
				return "", false, fmt.Errorf("status lifecycle: resolve %s for the status-conflict check: %w", authoritativeBranch, err)
			}
			return c.path, false, nil
		}
		if !isBoundObjectID(x) {
			return c.path, false, nil
		}
		p.mainTip = x
	}
	at := c.path
	content, err := p.o.git.ReadFile(ctx, p.mainTip, at)
	var gitErr *git.GitError
	if err != nil && !lifecycleTransient(ctx, err) && errors.As(err, &gitErr) && gitErr.Kind == git.ErrKindNotFound {
		// Gone under this name on main: renamed since the merge-base?
		diffs, derr := p.o.git.Diff(ctx, p.base, p.mainTip)
		if derr != nil {
			if lifecycleTransient(ctx, derr) {
				return "", false, fmt.Errorf("status lifecycle: main's renames for the status-conflict check: %w", derr)
			}
			return c.path, false, nil
		}
		for _, d := range diffs {
			if d.Status == "renamed" && d.OldPath == c.path && d.Path != "" {
				at = d.Path
				content, err = p.o.git.ReadFile(ctx, p.mainTip, at)
				break
			}
		}
	}
	if err != nil {
		if lifecycleTransient(ctx, err) {
			return "", false, fmt.Errorf("status lifecycle: read %s on %s for the status-conflict check: %w", at, authoritativeBranch, err)
		}
		return at, false, nil
	}
	t, s, ok := artifact.LifecycleFields(content)
	return at, ok && t == c.typ && s == c.to, nil
}

// applyStatusLifecycleAt is the lifecycle-on replacement of
// applyCommitStatusAt (resolveMergeSource step 3). It runs whether or not
// the outcome declares a status, because non-owned edits must be judged
// either way, and it never degrades: a refusal is a *StatusLifecycleError,
// any other error keeps the run in committing. The merge-base is
// cascadeBaseAt's, so a replay after a landed merge judges — and builds
// the same cascade commit — against main as it was before this run merged.
// On such a replay the status-conflict gate (O4) is skipped: unlike every
// other rule it reads the projection, which may have moved since the merge
// it judged landed, and refusing now would route back a run whose content
// is already on the local main.
func (o *Orchestrator) applyStatusLifecycleAt(ctx context.Context, run *domain.Run, newStatus, bound string) (string, *lifecyclePlan, error) {
	base, replay, err := o.cascadeBase(ctx, run, newStatus, bound)
	if err != nil {
		if lifecycleTransient(ctx, err) {
			return "", nil, fmt.Errorf("status lifecycle: merge-base of main and %s: %w", bound, err)
		}
		return "", nil, &StatusLifecycleError{Violations: []StatusLifecycleViolation{{Reason: LifecycleUnreadableDiff}}}
	}
	if !isBoundObjectID(base) {
		return "", nil, &StatusLifecycleError{Violations: []StatusLifecycleViolation{{Reason: LifecycleUnreadableDiff}}}
	}
	return o.lifecycleCascadeFrom(ctx, run, newStatus, bound, base, !replay)
}

// lifecycleCascadeFrom plans the bound diff against base and commits the
// owned rewrites onto bound (one CommitFilesOnto commit, today's message
// and trailers). It returns bound unchanged when nothing needs rewriting.
// Deterministic for the same run, status, bound commit and base, like
// cascadeCommitFrom, so a replay recomputes the same commit.
func (o *Orchestrator) lifecycleCascadeFrom(ctx context.Context, run *domain.Run, newStatus, bound, base string, scGate bool) (string, *lifecyclePlan, error) {
	plan, err := o.planStatusLifecycle(ctx, run, newStatus, bound, base, scGate)
	if err != nil {
		return "", nil, err
	}
	if len(plan.updates) == 0 {
		return bound, plan, nil
	}
	c, err := o.commitCascadeOnto(ctx, run, newStatus, bound, plan.updates)
	if err != nil {
		return "", nil, err
	}
	return c, plan, nil
}

// resolveLifecycleSource is resolveMergeSource step 3 with the lifecycle
// on (DC-2): no "log at Warn and merge bound" — a refusal is recorded and
// routed, any other error is returned so the run stays in committing.
func (o *Orchestrator) resolveLifecycleSource(ctx context.Context, run *domain.Run, src mergeSource) (mergeSource, bool, error) {
	c, plan, err := o.applyStatusLifecycleAt(ctx, run, run.CommitMeta["status"], src.bound)
	var le *StatusLifecycleError
	switch {
	case errors.As(err, &le):
		return mergeSource{}, true, o.refuseStatusLifecycle(ctx, run, "", le)
	case errors.Is(err, errNotChildOfBound):
		return mergeSource{}, true, o.refuseNotChildOfBound(ctx, run, src.bound, err)
	case err != nil:
		observe.Logger(ctx).Warn("status lifecycle: transient failure judging the bound commit; the run stays in committing",
			"run_id", run.RunID, "bound_sha", src.bound, "error", err)
		return mergeSource{}, true, err
	}
	src.sha = c
	src.lifecycle = plan
	return src, false, nil
}

// refuseStatusLifecycle records a lifecycle refusal and routes it: an
// audit line, the primary merge outcome (failed, class precondition, the
// violations as its detail), then handlePermanentMergeFailure — a publish
// workflow's merge_failed sends the run back to its producer, a legacy
// terminal-commit workflow fails the run. A failure to record the outcome
// is returned (the run stays in committing). mergeSHA is the refused local
// merge for a merge-result (V1) refusal, "" before the merge.
func (o *Orchestrator) refuseStatusLifecycle(ctx context.Context, run *domain.Run, mergeSHA string, le *StatusLifecycleError) error {
	observe.Logger(ctx).Warn("status lifecycle: governed merge refused; nothing pushed",
		"run_id", run.RunID, "branch", run.BranchName, "violations", len(le.Violations), "detail", le.Error())
	observe.AuditLog(ctx, "status_lifecycle_refused",
		"run_id", run.RunID, "task_path", run.TaskPath, "violations", le.Error())
	if recErr := o.recordPrimaryMergeOutcome(ctx, run, mergeSHA, le, ""); recErr != nil {
		return recErr
	}
	return o.handlePermanentMergeFailure(ctx, run, le)
}

// verifyLifecycleMerge is V1 (DC-8; review round 1): before any push, the
// merge result M must change, relative to main as it was before the merge
// (BaseSHA), nothing the lifecycle did not allow.
//
// It judges what the merge changes, not the paths the plan listed: the
// plan judged Diff(merge-base, bound) for ONE `git merge-base`, while
// `git merge` (ort) merges against the virtual base of every best common
// ancestor, against main as it is when the merge runs. In a criss-cross
// history, or after main gained commits of the bound history between the
// judgement and the merge, a status change or a deletion the plan never
// saw would otherwise land with the branch's content; and a rename main
// made after the fork, which git merges cleanly, would be refused because
// the old name reads nowhere (review finding P2).
//
//  1. The merge-base of BaseSHA and the merged commit must still be the
//     plan's. If main gained commits of the bound history since the
//     judgement it is not, and the attempt is retried (a transient error:
//     the caller takes the merge back and stays in committing) so the next
//     attempt judges against the base git now merges with.
//  2. Every in-scope path Diff(BaseSHA, M) lists (renames detected) is
//     judged: the run's own artifact whose status the plan sets (owned)
//     must land at the planned status and type, from the judged status on
//     main (or main's already holding the planned one) — or, when the plan
//     created it, onto no lifecycle artifact on main; any other artifact
//     main holds keeps its type, its status, its existence and, if it
//     parses for the projection on main, that too; an artifact the merge
//     adds must carry an initial status of its type.
//  3. An owned change the diff does not list was not landed by this merge:
//     main must already hold the planned status there.
//
// The owned TaskPath is looked up under the name main gave it when main
// renamed it after the merge-base (Diff(merge-base, BaseSHA)). A merge
// that adds nothing (M == BaseSHA, a replay of a merge that already
// landed) has nothing to verify. A transient read error is returned as a
// retryable *git.GitError; a mismatch, or a read or diff a retry cannot
// change, is a *StatusLifecycleError.
func (o *Orchestrator) verifyLifecycleMerge(ctx context.Context, plan *lifecyclePlan, merged string, res git.MergeResult) error {
	if plan == nil || res.SHA == res.BaseSHA {
		return nil
	}
	v := &mergeVerifier{o: o, plan: plan, m: res.SHA, main: res.BaseSHA, renamed: map[string]string{}}
	if err := v.verify(ctx, merged); err != nil {
		return err
	}
	if len(v.viol) > 0 {
		return &StatusLifecycleError{Violations: v.viol}
	}
	return nil
}

// mergeVerifier carries one V1 evaluation.
type mergeVerifier struct {
	o       *Orchestrator
	plan    *lifecyclePlan
	m, main string // the merge result and main before the merge
	viol    []StatusLifecycleViolation
	// renamed maps a path at the plan's merge-base to its name on main
	// (main-side renames since the merge-base), loaded on first use.
	renamed       map[string]string
	renamedLoaded bool
}

// lifecycleRetry is V1's "not a verdict, merge again" error: retryable,
// so takeBackRefusedMerge's caller keeps the run in committing.
func lifecycleRetry(msg string, err error) error {
	return &git.GitError{Kind: git.ErrKindTransient, Op: "status_lifecycle", Message: msg, Err: err}
}

func (v *mergeVerifier) mismatch(path string, t domain.ArtifactType, want, got domain.ArtifactStatus) {
	v.viol = append(v.viol, StatusLifecycleViolation{Path: path, Type: t, From: want, To: got, Reason: LifecycleMergeResultMismatch})
}

func (v *mergeVerifier) unreadable(path string) {
	v.viol = append(v.viol, StatusLifecycleViolation{Path: path, Reason: LifecycleUnreadableArtifact})
}

// read reads path at ref. A transient failure is returned; a not-found is
// an absent side when absentOK; any other failure reports bad (what cannot
// be read cannot be verified: the caller refuses unreadable_artifact).
func (v *mergeVerifier) read(ctx context.Context, ref, path string, absentOK bool) (sd side, bad bool, err error) {
	content, err := v.o.git.ReadFile(ctx, ref, path)
	if err == nil {
		return readSide(content, path), false, nil
	}
	if lifecycleTransient(ctx, err) {
		return side{}, false, lifecycleRetry("the merge result could not be read for the status lifecycle check", err)
	}
	var gitErr *git.GitError
	if absentOK && errors.As(err, &gitErr) && gitErr.Kind == git.ErrKindNotFound {
		return side{}, false, nil
	}
	return side{}, true, nil
}

// mainName returns the name main gives a path of the plan's merge-base:
// the path itself, or its new name when main renamed it since. ok is false
// when main holds neither.
func (v *mergeVerifier) mainName(ctx context.Context, path string) (string, bool, error) {
	if !v.renamedLoaded {
		diffs, err := v.o.git.Diff(ctx, v.plan.base, v.main)
		if err != nil {
			if lifecycleTransient(ctx, err) {
				return "", false, lifecycleRetry("main's renames since the merge-base could not be read for the status lifecycle check", err)
			}
			return "", false, nil
		}
		for _, d := range diffs {
			if d.Status == "renamed" && d.OldPath != "" {
				v.renamed[d.OldPath] = d.Path
			}
		}
		v.renamedLoaded = true
	}
	n, ok := v.renamed[path]
	return n, ok, nil
}

// diffSides maps a diff entry to its path on main before the merge and in
// the merge result ("" for the side that does not exist).
func diffSides(d git.FileDiff) (mainPath, mPath string) {
	switch d.Status {
	case "added":
		return "", d.Path
	case "deleted":
		return d.Path, ""
	case "renamed":
		return d.OldPath, d.Path
	default:
		return d.Path, d.Path
	}
}

func (v *mergeVerifier) verify(ctx context.Context, merged string) error {
	// 1. The merge was made against the base the plan judged.
	mb, err := v.o.git.MergeBase(ctx, v.main, merged)
	if err != nil {
		if lifecycleTransient(ctx, err) {
			return lifecycleRetry("the merge-base of the merge could not be read for the status lifecycle check", err)
		}
		return &StatusLifecycleError{Violations: []StatusLifecycleViolation{{Reason: LifecycleUnreadableDiff}}}
	}
	if !isBoundObjectID(mb) {
		return &StatusLifecycleError{Violations: []StatusLifecycleViolation{{Reason: LifecycleUnreadableDiff}}}
	}
	if mb != v.plan.base {
		return lifecycleRetry(fmt.Sprintf("main gained commits of the bound history since the status lifecycle judged it "+
			"(the merge-base is now %s, the judgement was against %s); judging again", mb, v.plan.base), nil)
	}

	// Where each owned change lands: its path, or main's new name for it.
	landing := map[string]*ownedChange{}
	landingAt := make([]string, len(v.plan.owned))
	for i := range v.plan.owned {
		c := &v.plan.owned[i]
		at := c.path
		if !c.isNew {
			sd, bad, err := v.read(ctx, v.main, c.path, true)
			if err != nil {
				return err
			}
			if !bad && !sd.read {
				n, ok, err := v.mainName(ctx, c.path)
				if err != nil {
					return err
				}
				if ok {
					at = n
				}
			}
		}
		if c.scAt != "" && at != c.scAt {
			// main renamed the artifact again since the status-conflict gate
			// judged the change under c.scAt: judge it again under the name
			// it would land at.
			return lifecycleRetry(fmt.Sprintf("main renamed %s since the status-conflict check (judged under %s, now %s); judging again",
				lifecycleText(c.path, 256), lifecycleText(c.scAt, 256), lifecycleText(at, 256)), nil)
		}
		landing[at] = c
		landingAt[i] = at
	}

	// 2. Everything the merge changes relative to main.
	diffs, err := v.o.git.Diff(ctx, v.main, v.m)
	if err != nil {
		if lifecycleTransient(ctx, err) {
			return lifecycleRetry("the merge result's diff could not be read for the status lifecycle check", err)
		}
		return &StatusLifecycleError{Violations: []StatusLifecycleViolation{{Reason: LifecycleUnreadableDiff}}}
	}
	seen := map[string]bool{}
	for _, d := range diffs {
		if cQuotedMarkdown(d.Path) || (d.Status == "renamed" && cQuotedMarkdown(d.OldPath)) {
			v.viol = append(v.viol, StatusLifecycleViolation{Path: d.Path, Reason: LifecycleUnparseablePath})
			continue
		}
		mainPath, mPath := diffSides(d)
		if c := landing[mainPath]; mainPath != "" && c != nil && mPath != mainPath {
			// The run's own artifact moved away or removed by the merge.
			seen[mainPath] = true
			v.mismatch(mainPath, c.typ, c.final, "")
			continue
		}
		c := landing[mPath]
		if mPath == "" {
			c = nil
		}
		_, mainIn := v.o.artifactScope.GovernedArtifactKey(mainPath)
		_, mIn := v.o.artifactScope.GovernedArtifactKey(mPath)
		mainIn = mainIn && mainPath != ""
		mIn = mIn && mPath != ""
		if c == nil && !mainIn && !mIn {
			continue // out of scope on both sides: the projection never reads it
		}
		var before, after side
		if mainPath != "" && (mainIn || c != nil) {
			sd, bad, err := v.read(ctx, v.main, mainPath, false)
			if err != nil {
				return err
			}
			if bad {
				v.unreadable(mainPath)
				continue
			}
			before = sd
		}
		if mPath != "" && (mIn || c != nil) {
			sd, bad, err := v.read(ctx, v.m, mPath, false)
			if err != nil {
				return err
			}
			if bad {
				v.unreadable(mPath)
				continue
			}
			after = sd
		}
		if c != nil {
			seen[mPath] = true
			if mainPath != mPath {
				// Another path renamed onto the run's own artifact (DC-7 at
				// the merge): its identity on main is gone — a removal —
				// and it is not the owned artifact's judged from-status.
				v.judgeFrozen(mainPath, "", before, side{})
				before = side{}
			}
			v.judgeOwned(mPath, c, before, after)
			continue
		}
		v.judgeFrozen(mainPath, mPath, before, after)
	}

	// 3. An owned change this merge did not land: main must hold it.
	for i := range v.plan.owned {
		c, at := &v.plan.owned[i], landingAt[i]
		if seen[at] {
			continue
		}
		sd, bad, err := v.read(ctx, v.m, at, true)
		switch {
		case err != nil:
			return err
		case bad:
			v.unreadable(at)
		case !sd.ok || sd.typ != c.typ || sd.status != c.final:
			v.mismatch(at, c.typ, c.final, sd.status)
		}
	}
	return nil
}

// judgeOwned: the run's own artifact lands at the planned type and status,
// from the judged status on main (or main's holding the planned one
// already) — or, for an artifact the plan created, onto no lifecycle
// artifact on main.
func (v *mergeVerifier) judgeOwned(path string, c *ownedChange, before, after side) {
	switch {
	case !after.ok || after.typ != c.typ || after.status != c.final:
		v.mismatch(path, c.typ, c.final, after.status)
	case c.isNew && before.ok:
		v.mismatch(path, before.typ, "", before.status)
	case !c.isNew && (!before.ok || before.typ != c.typ):
		v.mismatch(path, c.typ, c.from, before.status)
	case !c.isNew && before.status != c.from && before.status != c.final:
		v.mismatch(path, c.typ, c.from, before.status)
	}
}

// judgeFrozen: an artifact main holds keeps its type, status and existence
// (and, when it parses for the projection on main, keeps parsing); an
// artifact the merge adds carries an initial status.
func (v *mergeVerifier) judgeFrozen(mainPath, mPath string, before, after side) {
	if before.ok {
		path := mPath
		if path == "" || !after.ok {
			path = mainPath
		}
		switch {
		case !after.ok, after.typ != before.typ, after.status != before.status, before.parses && !after.parses:
			v.mismatch(path, before.typ, before.status, after.status)
		}
		return
	}
	if after.ok && !v.o.lifecycle.IsInitial(after.typ, after.status) {
		v.mismatch(mPath, after.typ, "", after.status)
	}
}
