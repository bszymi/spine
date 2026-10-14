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
//     re-read (V1): what lands is what was judged, whatever git's merge did
//     (a `*.md merge=union` attribute keeps two status lines without a
//     conflict).
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
	// the merge-base or the bound commit for a reason a retry cannot
	// change (too large, not a blob, not found).
	LifecycleUnreadableArtifact = "unreadable_artifact"
	// LifecycleUnreadableDiff: the bound diff itself cannot be computed
	// for a reason a retry cannot change (no merge-base with main — an
	// unrelated history — or a permanent merge-base or diff failure).
	// Added by the implementation (not in the design's table): without it
	// such a run would sit in committing forever.
	LifecycleUnreadableDiff = "unreadable_diff"
	// LifecycleMergeResultMismatch: the merge result does not hold what
	// was judged (V1). From is the expected status, To the status the
	// merge result holds ("" when the file does not parse there).
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
	base string
	// updates are the owned rewrites (repository path → content), each
	// verified by R1.
	updates map[string]string
	// judged lists every artifact the bound commit holds that the plan
	// judged, with the status the merge result must hold (V1).
	judged []judgedArtifact
}

// judgedArtifact is one V1 expectation: path must be a lifecycle artifact
// of type typ in the merge result, with status — or, when fromMain, with
// the status mainPath has on main before the merge.
type judgedArtifact struct {
	path     string
	typ      domain.ArtifactType
	status   domain.ArtifactStatus
	fromMain bool
	mainPath string
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
		default:
			p.expectMain(it.tip, tip.typ, it.base)
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
		return
	}
	p.expect(it.tip, tip.typ, tip.status)
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
				return
			}
			p.expectMain(it.tip, tip.typ, it.base)
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
			p.expect(it.tip, tip.typ, final)
		} else {
			p.expectMain(it.tip, tip.typ, it.base)
		}
		return
	}
	// O2 / O2′: new for the lifecycle (a planning addition, or a TaskPath
	// absent — or not an artifact — at the merge-base).
	if p.newStatus == "" {
		if !p.l.IsInitial(tip.typ, tip.status) {
			p.refuse(StatusLifecycleViolation{Path: it.tip, Type: tip.typ, To: tip.status, Reason: LifecycleIllegalInitialStatus})
			return
		}
		p.expect(it.tip, tip.typ, tip.status)
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
	p.expect(it.tip, tip.typ, final)
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

func (p *planner) expect(path string, t domain.ArtifactType, s domain.ArtifactStatus) {
	p.plan.judged = append(p.plan.judged, judgedArtifact{path: path, typ: t, status: s})
}

func (p *planner) expectMain(path string, t domain.ArtifactType, mainPath string) {
	p.plan.judged = append(p.plan.judged, judgedArtifact{path: path, typ: t, fromMain: true, mainPath: mainPath})
}

// runSCChecks is O4: an owned transition the orchestrator's validator
// refuses with a blocking (error-severity) status-conflict finding. A
// validator without StatusChangeValidator skips the gate; a validator
// error is transient.
func (p *planner) runSCChecks(ctx context.Context) error {
	if len(p.scChecks) == 0 || p.o.validator == nil {
		return nil
	}
	scv, ok := p.o.validator.(StatusChangeValidator)
	if !ok {
		return nil
	}
	for _, c := range p.scChecks {
		res, err := scv.ValidateStatusChange(ctx, c.key, c.to)
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

// verifyLifecycleMerge is V1 (DC-8): before any push, every artifact the
// plan judged must be, in the merge result M, a lifecycle artifact of the
// judged type with the expected status — the planned status, or, for an
// artifact whose status the plan leaves alone, the status it has on main
// before the merge (BaseSHA). A merge that adds nothing (M == BaseSHA, a
// replay of a merge that already landed) has nothing to verify. A
// transient read error is returned as a retryable *git.GitError (the
// take-back runs, then the attempt is retried); a mismatch, or a permanent
// read failure, is a *StatusLifecycleError (merge_result_mismatch).
func (o *Orchestrator) verifyLifecycleMerge(ctx context.Context, plan *lifecyclePlan, res git.MergeResult) error {
	m, base := res.SHA, res.BaseSHA
	if plan == nil || m == base {
		return nil
	}
	transient := func(err error) error {
		return &git.GitError{Kind: git.ErrKindTransient, Op: "status_lifecycle",
			Message: "the merge result could not be read for the status lifecycle check", Err: err}
	}
	var viol []StatusLifecycleViolation
	mismatch := func(j judgedArtifact, want, got domain.ArtifactStatus) {
		viol = append(viol, StatusLifecycleViolation{Path: j.path, Type: j.typ, From: want, To: got, Reason: LifecycleMergeResultMismatch})
	}
	for _, j := range plan.judged {
		want := j.status
		if j.fromMain {
			c, err := o.git.ReadFile(ctx, base, j.mainPath)
			if err != nil {
				if lifecycleTransient(ctx, err) {
					return transient(err)
				}
				mismatch(j, "", "")
				continue
			}
			t, s, ok := artifact.LifecycleFields(c)
			if !ok || t != j.typ {
				mismatch(j, "", "")
				continue
			}
			want = s
		}
		c, err := o.git.ReadFile(ctx, m, j.path)
		if err != nil {
			if lifecycleTransient(ctx, err) {
				return transient(err)
			}
			mismatch(j, want, "")
			continue
		}
		t, s, ok := artifact.LifecycleFields(c)
		if !ok || t != j.typ || s != want {
			mismatch(j, want, s)
		}
	}
	if len(viol) > 0 {
		return &StatusLifecycleError{Violations: viol}
	}
	return nil
}
