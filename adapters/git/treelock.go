package git

// Shared working-tree safety (INIT-023 TASK-012; audit F9, the sink of F8,
// and the read half of P3-22).
//
// The defect. Standalone `spine serve` builds ONE CLIClient per workspace
// and hands it to the engine, the divergence service, the artifact and
// workflow services and the projection. Every stateful operation runs in
// that client's single on-disk working tree (one HEAD, one index, one set
// of files), and nothing serialised them: the engine's multi-step
// sequences — the status cascade (checkout run → write → commit → checkout
// main), the governed merge (merge → abort → push), a run start (HEAD →
// branch), the recovery ledger — interleaved with each other and with
// in-place artifact writes. Two concurrent approvals produced spurious
// merge failures, a status commit landed on main outside the governed
// merge, a run branch was cut from another run's unmerged branch, and a
// tree left dirty or off main failed every later governed merge in the
// workspace until an operator repaired it by hand.
//
// The fix has four parts, all keyed by the tree's canonical path, so two
// clients (or an in-place write scope and a client) that name the same
// directory differently share one state:
//
//  1. A process-wide tree lock. Each stateful CLIClient call (Checkout,
//     Commit, Merge, CreateBranch, DeleteBranch, WriteAndStageFile,
//     MergeBound, ResetBranch) takes it for its own duration, and a caller
//     that runs a multi-step sequence takes it once for the whole sequence
//     through WithWorkingTree. Acquisition hands out a ticket carried in
//     the context, so calls nested inside a sequence re-enter instead of
//     deadlocking; a ticket is honoured only while it is the lock's live
//     holder, so a context that escapes a released sequence (a stored ctx,
//     an async handler) cannot bypass the lock. Waiting honours the
//     caller's context (*TreeWaitError); a context that is already done
//     never acquires.
//  2. A home branch (WithHomeBranch), registered for the tree. Only
//     standalone's shared workspace trees register one. Whoever TAKES such
//     a tree first restores it to a clean checkout of the home branch
//     (restoreHeld): an in-progress merge is aborted, tracked changes are
//     quarantined into an unreferenced commit (`git stash create`, never a
//     ref, so nothing is advertised by the git HTTP endpoint), HEAD is
//     switched back home. Untracked files are never touched, and a stale
//     .git/index.lock is reported, never deleted (owner question 5). A
//     poisoned tree therefore heals itself at the next governed operation
//     instead of failing it. A tracked difference no hard reset removes (a
//     phantom: line-ending attributes over a CRLF blob, case-only path
//     collisions) is left in place rather than failing every later take,
//     and a take-back of a refused merge that a failed ResetBranch left
//     undone is retried before the tree is used (review round 1).
//  3. A taken sequence on a home-branch tree is never abandoned half-way:
//     WithWorkingTree runs the entry restore, fn and the exit restore on a
//     context detached from the caller's cancellation and bounded by
//     treeSequenceTimeout (owner question 6). Only the lock wait honours
//     the caller's context.
//     Together with SIGTERM-on-cancel (gitcmd.go) a client disconnect can
//     no longer leave the tree half checked out or locked.
//  4. On a home-branch tree, a read method's "HEAD" means
//     refs/heads/<home> (ReadFile, ListFiles, TreeEntryType, Head, Diff,
//     Log, MergeBase, RefSHA). Reads are not serialised (that would make
//     every API read wait behind merges and pushes), and inside a sequence
//     HEAD is a run branch: without the rewrite a concurrent StartRun read
//     the task at HEAD and bound the workflow an unreviewed branch chose.
//
// Assumptions (KNOWN-LIMITATIONS.md, "Shared working tree"): one process
// per repository path. A second process, or an operator's git, on the same
// tree is not serialised, and a restore can quarantine its in-flight work.
//
// Embedders. A tree no client registered a home branch for (SMP's
// per-request worktrees, code-repository clones, plain NewCLIClient users)
// is never restored, never detached, and its reads keep the real HEAD; its
// stateful calls take an uncontended lock and the registry entry is
// removed when the last holder or waiter leaves.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/worktreefs"
)

const (
	// treeSequenceTimeout bounds a sequence that has taken the tree (the
	// entry restore, fn and the exit restore), and an in-place write scope
	// on a home-branch tree. It also bounds a hung push inside a governed
	// merge. A constant (owner question 6).
	treeSequenceTimeout = 5 * time.Minute
	// treeRestoreTimeout bounds one restore.
	treeRestoreTimeout = 30 * time.Second
)

// WorkingTreeGuard is implemented by clients whose stateful operations
// share one on-disk working tree (*CLIClient). A caller that runs a
// multi-step sequence on that tree (checkout → write → commit → checkout,
// merge → abort → push) runs it inside WithWorkingTree, and passes the ctx
// fn receives to every call it makes in the sequence.
//
// Never emit events, route steps or write to a store inside fn: a queue
// whose Publish blocks while full would deadlock with a consumer that
// needs the tree. Locked sections are git-only.
type WorkingTreeGuard interface {
	WithWorkingTree(ctx context.Context, fn func(ctx context.Context) error) error
}

// TreeWaitError is returned when the caller's context ended before the
// tree could be taken (including a context that was already done). Nothing
// ran: no restore, no fn, no git.
type TreeWaitError struct {
	Err error // the context's error
}

func (e *TreeWaitError) Error() string {
	return fmt.Sprintf("git: gave up waiting for the shared working tree: %v", e.Err)
}

func (e *TreeWaitError) Unwrap() error { return e.Err }

// TreeRestoreError is returned when a shared tree could not be brought
// back to a clean checkout of its home branch. Err is the *GitError (or
// grammar refusal) that stopped the restore, so the transient/permanent
// classification survives errors.As. QuarantineSHA is set when the
// restore had already quarantined tracked changes before a later step
// failed (codex review round 2): the edits are off the disk and only that
// commit holds them.
type TreeRestoreError struct {
	Branch        string
	Err           error
	QuarantineSHA string
}

func (e *TreeRestoreError) Error() string {
	if e.QuarantineSHA != "" {
		return fmt.Sprintf("restore working tree to %s: %v (tracked changes were quarantined in %s; recover them with `git stash apply %s`)",
			e.Branch, e.Err, e.QuarantineSHA, e.QuarantineSHA)
	}
	return fmt.Sprintf("restore working tree to %s: %v", e.Branch, e.Err)
}

func (e *TreeRestoreError) Unwrap() error { return e.Err }

// TreeRestoreReport describes what a restore repaired.
type TreeRestoreReport struct {
	// PreviousRef is the ref HEAD was attached to before the restore
	// (`git symbolic-ref -q HEAD`), "" when it was detached.
	PreviousRef string
	// AbortedMerge: a merge was in progress (MERGE_HEAD) and was aborted.
	AbortedMerge bool
	// QuarantineSHA is the unreferenced commit (`git stash create`) that
	// holds the tracked changes the restore discarded; "" when there were
	// none. Recover with `git stash apply <sha>` until gc prunes it.
	QuarantineSHA string
	// SwitchedHEAD: HEAD was not on the home branch and was switched.
	SwitchedHEAD bool
	// TookBack is the refused merge a failed ResetBranch had left on a
	// branch of this tree, which the restore took back (pendingTakeBack);
	// "" when there was none.
	TookBack string
	// Phantoms are tracked paths whose working-tree difference survived
	// `git reset --hard` — a line-ending conversion the attributes demand
	// but the committed blob lacks, two paths that differ only in case on
	// a case-insensitive filesystem. They are left in place (nothing can
	// remove them but a content change); nil when there are none.
	Phantoms []string
}

// Repaired reports whether the restore changed anything.
func (r TreeRestoreReport) Repaired() bool {
	return r.AbortedMerge || r.QuarantineSHA != "" || r.SwitchedHEAD || r.TookBack != ""
}

// --- Registry: locks and home branches, keyed by the tree's path. ---

type treeLock struct {
	sem    chan struct{} // capacity 1: the lock itself
	refs   int           // holders + waiters; the entry is deleted at zero
	holder *treeTicket   // the live holder's ticket, nil while free
}

// treeTicket identifies one acquisition. Compared by pointer.
type treeTicket struct{ key string }

// heldTickets is the immutable list of tickets a context carries (a
// sequence on the primary tree may hold a code repository's tree too).
type heldTickets struct {
	t    *treeTicket
	next *heldTickets
}

type heldTicketsKey struct{}

var (
	treeMu    sync.Mutex
	treeLocks = map[string]*treeLock{}
	treeHomes = map[string]string{} // abs and symlink-free path -> home branch
	// treePhantoms: canonical tree key -> phantom path -> its blobs, as
	// the last restore that found phantoms saw them (restoreHeld).
	treePhantoms = map[string]map[string]phantomState{}
	// treeTakeBacks: canonical tree key -> branch -> the take-back a
	// failed ResetBranch left undone (pendingTakeBack).
	treeTakeBacks = map[string]map[string]pendingTakeBack{}
)

// phantomState identifies a phantom's state: the index entry's blob and
// a fingerprint of the working-tree entry at its path (phantomStatesOf).
type phantomState struct{ index, worktree string }

// pendingTakeBack is a take-back ResetBranch was asked for and did not
// complete (review round 1 of INIT-023 TASK-012): the engine refused a
// bound merge after MergeBound moved the local branch to expected, and
// moving it back to `to` failed — a ref lock held by git's detached
// auto-gc, for example. Before this record existed the merge block still
// released the tree, and the tree looked healthy (on its home branch,
// clean) with the refused merge on the branch: the next holder — an
// in-place artifact write whose auto-push publishes main, another run's
// MergeBound — built on it or published it. Now every later take of the
// tree first settles it (settlePendingTakeBack): it retries the
// compare-and-swap, and refuses the tree while the branch still contains
// the refused merge. In-process, like the lock (KNOWN-LIMITATIONS).
type pendingTakeBack struct{ to, expected string }

func notePendingTakeBack(dir, branch, to, expected string) {
	key := canonicalTreeKey(dir)
	treeMu.Lock()
	defer treeMu.Unlock()
	m := treeTakeBacks[key]
	if m == nil {
		m = map[string]pendingTakeBack{}
		treeTakeBacks[key] = m
	}
	m[branch] = pendingTakeBack{to: to, expected: expected}
}

// clearPendingTakeBack drops the record of branch's take-back of the
// refused merge expected. A record of another refused merge on the branch
// is kept (review round 2): a take-back that succeeded says nothing about
// whether the branch still contains that one, and the next settle decides
// it.
func clearPendingTakeBack(dir, branch, expected string) {
	key := canonicalTreeKey(dir)
	treeMu.Lock()
	defer treeMu.Unlock()
	if m := treeTakeBacks[key]; m != nil {
		if p, ok := m[branch]; ok && p.expected == expected {
			delete(m, branch)
		}
		if len(m) == 0 {
			delete(treeTakeBacks, key)
		}
	}
}

func pendingTakeBacksOf(dir string) map[string]pendingTakeBack {
	treeMu.Lock()
	empty := len(treeTakeBacks) == 0
	treeMu.Unlock()
	if empty {
		return nil
	}
	key := canonicalTreeKey(dir)
	treeMu.Lock()
	defer treeMu.Unlock()
	if len(treeTakeBacks[key]) == 0 {
		return nil
	}
	out := make(map[string]pendingTakeBack, len(treeTakeBacks[key]))
	for b, p := range treeTakeBacks[key] {
		out[b] = p
	}
	return out
}

func ticketsIn(ctx context.Context) *heldTickets {
	h, _ := ctx.Value(heldTicketsKey{}).(*heldTickets)
	return h
}

func (h *heldTickets) has(t *treeTicket) bool {
	for ; h != nil; h = h.next {
		if h.t == t {
			return true
		}
	}
	return false
}

func withTicket(ctx context.Context, t *treeTicket) context.Context {
	return context.WithValue(ctx, heldTicketsKey{}, &heldTickets{t: t, next: ticketsIn(ctx)})
}

// treeAbs is the cleaned absolute path of dir.
func treeAbs(dir string) string {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	return abs
}

// treeReal is abs resolved through symlinks, or "" when it cannot be
// (the directory does not exist yet).
func treeReal(abs string) string {
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ""
	}
	return resolved
}

// canonicalTreeKey is the lock key: the symlink-free path when it
// resolves, else the cleaned absolute path. /var/x and /private/var/x,
// repo/. and repo share one lock.
func canonicalTreeKey(dir string) string {
	abs := treeAbs(dir)
	if resolved := treeReal(abs); resolved != "" {
		return resolved
	}
	return abs
}

// registerHomeBranch records branch as the home of the tree at dir, under
// its absolute path and, when it resolves, its symlink-free path.
func registerHomeBranch(dir, branch string) {
	abs := treeAbs(dir)
	resolved := treeReal(abs)
	treeMu.Lock()
	defer treeMu.Unlock()
	treeHomes[abs] = branch
	if resolved != "" {
		treeHomes[resolved] = branch
	}
}

// homeBranchOf returns the home branch registered for the tree at dir, or
// "". Embedders that register none (SMP) pay one map check.
func homeBranchOf(dir string) string {
	treeMu.Lock()
	empty := len(treeHomes) == 0
	treeMu.Unlock()
	if empty {
		return ""
	}
	abs := treeAbs(dir)
	treeMu.Lock()
	b, ok := treeHomes[abs]
	treeMu.Unlock()
	if ok {
		return b
	}
	resolved := treeReal(abs)
	if resolved == "" || resolved == abs {
		return ""
	}
	treeMu.Lock()
	defer treeMu.Unlock()
	return treeHomes[resolved]
}

// treeHold is one acquisition of a tree, or a reentrant pass-through.
type treeHold struct {
	ctx     context.Context // carries the ticket (fresh) or is the caller's (reentrant)
	ticket  *treeTicket     // nil when reentrant
	fresh   bool
	release func() // idempotent; a no-op when reentrant
}

func noRelease() {}

// acquireTree takes the tree at dir, or re-enters it when ctx carries the
// live holder's ticket. Waiting honours ctx; a done ctx never acquires —
// checked before the wait (Go's select picks at random among ready cases,
// so a done ctx and a free lock would otherwise acquire about half the
// time) and again after it.
func acquireTree(ctx context.Context, dir string) (treeHold, error) {
	key := canonicalTreeKey(dir)
	treeMu.Lock()
	l := treeLocks[key]
	if l != nil && l.holder != nil && ticketsIn(ctx).has(l.holder) {
		treeMu.Unlock()
		return treeHold{ctx: ctx, release: noRelease}, nil
	}
	if err := ctx.Err(); err != nil {
		treeMu.Unlock()
		return treeHold{ctx: ctx, release: noRelease}, &TreeWaitError{Err: err}
	}
	if l == nil {
		l = &treeLock{sem: make(chan struct{}, 1)}
		treeLocks[key] = l
	}
	l.refs++
	treeMu.Unlock()

	drop := func() {
		treeMu.Lock()
		defer treeMu.Unlock()
		l.refs--
		if l.refs == 0 && treeLocks[key] == l {
			delete(treeLocks, key)
		}
	}
	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		drop()
		return treeHold{ctx: ctx, release: noRelease}, &TreeWaitError{Err: ctx.Err()}
	}
	if err := ctx.Err(); err != nil {
		<-l.sem
		drop()
		return treeHold{ctx: ctx, release: noRelease}, &TreeWaitError{Err: err}
	}
	t := &treeTicket{key: key}
	treeMu.Lock()
	l.holder = t
	treeMu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			// The ticket stops being honoured before the lock is free, so a
			// stale ticket can never pass while another holder runs.
			treeMu.Lock()
			l.holder = nil
			treeMu.Unlock()
			<-l.sem
			drop()
		})
	}
	return treeHold{ctx: withTicket(ctx, t), ticket: t, fresh: true, release: release}, nil
}

// --- CLIClient integration. ---

// WithHomeBranch declares that the client's working tree is a shared tree
// that must rest on a clean checkout of branch between sequences (INIT-023
// TASK-012). The home branch is a property of the tree: it is registered
// process-wide for the client's path, so every client and in-place write
// scope on that tree restores it, and their read methods resolve "HEAD" to
// refs/heads/<branch>.
//
// Only standalone's shared workspace clients set it (`spine serve` passes
// the governed-merge target, engine.AuthoritativeBranch). A tree with no
// home branch — code-repository clones, embedders' per-request worktrees,
// plain NewCLIClient users — is never restored and keeps the real HEAD.
//
// branch must be a short branch name (refs/heads/<n> is accepted and
// shortened). An invalid name registers nothing; that client's
// WithWorkingTree and RestoreWorkingTree then fail closed with a
// *TreeRestoreError.
func WithHomeBranch(branch string) CLIOption {
	return func(c *CLIClient) { c.homeBranch = branch }
}

// setupHomeBranch validates and registers the WithHomeBranch value. Called
// by NewCLIClient after every option.
func (c *CLIClient) setupHomeBranch() {
	if c.homeBranch == "" {
		return
	}
	short, _, err := QualifyBranch(c.homeBranch)
	if err != nil {
		c.homeErr = invalidRef("restore_tree", "home branch", c.homeBranch, err)
		return
	}
	c.homeBranch = short
	registerHomeBranch(c.repoPath, short)
}

// treeHome is the home branch of the client's tree, or "".
func (c *CLIClient) treeHome() string {
	if c.homeBranch != "" && c.homeErr == nil {
		return c.homeBranch
	}
	return homeBranchOf(c.repoPath)
}

// HomeBranch reports the home branch registered for the client's tree
// (by this client or another one on the same path), or "" when the tree
// has none. For wiring checks and operators.
func (c *CLIClient) HomeBranch() string { return c.treeHome() }

// homeRev is §2.9's read rule: on a home-branch tree a read method's
// "HEAD" is refs/heads/<home> (looked up exactly by resolveRev, so a
// missing home branch is ErrKindNotFound, never a DWIM guess); anything
// else, and every rev on a tree without a home branch, is unchanged.
func (c *CLIClient) homeRev(rev string) string {
	if rev != "HEAD" {
		return rev
	}
	if home := c.treeHome(); home != "" {
		return headsPrefix + home
	}
	return rev
}

// lockTree takes the client's tree for one stateful call (or re-enters a
// sequence that holds it).
func (c *CLIClient) lockTree(ctx context.Context) (treeHold, error) {
	return acquireTree(ctx, c.repoPath)
}

// WithWorkingTree runs fn while holding the client's working tree
// (WorkingTreeGuard). A ctx that already carries this tree's live ticket
// re-enters: fn runs at once with that ctx, nothing else happens.
//
// When this call TAKES a tree with a home branch (standalone's shared
// workspace tree):
//
//   - fn, and the restores around it, run on a context detached from the
//     caller's cancellation and bounded by treeSequenceTimeout (it keeps
//     the caller's values: actor, trace, logger fields). Only the wait for
//     the tree honours the caller's ctx (*TreeWaitError). A sequence the
//     server has started is therefore finished, never left half-way by a
//     client disconnect; one that hits the bound is stopped with SIGTERM
//     and healed by the next restore;
//   - the tree is restored before fn (a failure returns the
//     *TreeRestoreError and fn does not run) and after fn (a failure is
//     logged at Error; fn's result is returned).
//
// On a tree with no home branch (code-repository clones, embedders'
// per-request worktrees) it only holds the lock: fn gets the caller's ctx,
// cancellation included, with the ticket added, and nothing is restored
// (codex review round 1: such trees keep today's cancellation semantics).
// The one exception is a take-back a failed ResetBranch on this tree left
// undone (pendingTakeBack), settled before fn; embedders that never call
// ResetBranch never have one.
func (c *CLIClient) WithWorkingTree(ctx context.Context, fn func(ctx context.Context) error) error {
	if c.homeErr != nil {
		return &TreeRestoreError{Branch: c.homeBranch, Err: c.homeErr}
	}
	h, err := c.lockTree(ctx)
	if err != nil {
		return err
	}
	if !h.fresh {
		return fn(h.ctx)
	}
	defer h.release()
	home := c.treeHome()
	if home == "" {
		// Never restored, but a take-back a failed ResetBranch left undone
		// (a code repository's bound merge) is settled first, so the
		// sequence never builds on a refused merge (pendingTakeBack).
		if _, branch, err := c.settlePendingTakeBacks(h.ctx, "restore_tree"); err != nil {
			return &TreeRestoreError{Branch: branch, Err: err}
		}
		return fn(h.ctx)
	}
	seq, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), treeSequenceTimeout)
	defer cancel()
	if _, err := c.restoreHeld(seq, home); err != nil {
		return err
	}
	fnErr := fn(seq)
	// A fresh bound: fn may have spent the sequence's.
	if _, err := c.restoreHeld(h.ctx, home); err != nil {
		observe.Logger(ctx).Error("shared working tree not restored after a sequence; the next sequence retries the restore",
			"home_branch", home, "error", err)
	}
	return fnErr
}

// RestoreWorkingTree takes the tree and brings it to a clean checkout of
// its home branch (see restoreHeld), for operators and tests. A tree with
// no home branch is a permanent *GitError; nothing is changed.
func (c *CLIClient) RestoreWorkingTree(ctx context.Context) (TreeRestoreReport, error) {
	if c.homeErr != nil {
		return TreeRestoreReport{}, &TreeRestoreError{Branch: c.homeBranch, Err: c.homeErr}
	}
	home := c.treeHome()
	if home == "" {
		return TreeRestoreReport{}, &GitError{Kind: ErrKindPermanent, Op: "restore_tree",
			Message: "the working tree has no home branch (WithHomeBranch); nothing to restore to"}
	}
	h, err := c.lockTree(ctx)
	if err != nil {
		return TreeRestoreReport{}, err
	}
	defer h.release()
	return c.restoreHeld(h.ctx, home)
}

// restoreHeld brings the tree to a clean checkout of refs/heads/<home>.
// The caller holds the tree. It runs on its own context, detached from
// ctx's cancellation and bounded by treeRestoreTimeout, so a cancelled
// request can never stop it half-way.
//
//  0. A .git/index.lock fails the restore (transient "repository locked",
//     naming the file). With SIGTERM-on-cancel and detached sequences this
//     process never leaves one behind, so it is crash residue or another
//     process's (an operator's git, a second server): deleting it could
//     corrupt that process's index. Owner question 5: report, not delete.
//  1. HEAD's ref is recorded (`symbolic-ref -q HEAD`; "" when detached).
//  2. A merge in progress (MERGE_HEAD) is aborted; if the abort fails,
//     `reset --hard` discards the conflicted state of that merge — the
//     only state this restore discards without quarantine. (`stash
//     create` refuses an unmerged index, so the abort comes first.)
//  3. Tracked changes (`status --porcelain=v1 -z --untracked-files=no`)
//     are quarantined: `git stash create` writes a commit holding them but
//     no ref, then `reset --hard`. If stash create fails, or prints no
//     commit, the restore fails and nothing is reset. The SHA is logged at
//     Warn as soon as it exists, and a later failure carries it in the
//     *TreeRestoreError. Known phantoms (below) are not quarantined again.
//
// Before either `reset --hard` the restore refuses (and changes nothing)
// when an untracked or ignored file stands where the reset would write a
// tracked path — the path itself, a parent directory it would recreate,
// or content under the path (untrackedObstruction): `stash create` does
// not save untracked files and `reset --hard` would delete them (codex
// review round 2).
//  4. HEAD is switched to the home branch with the Checkout argv (`switch
//     --no-guess`, which never detaches onto a same-named tag) plus
//     `--no-overwrite-ignore`, so an ignored file in the way fails the
//     switch instead of being overwritten. When phantoms remain (below)
//     the plain switch would refuse ("your local changes would be
//     overwritten"), so the switch discards them (`--discard-changes`),
//     after the same untracked-obstruction check over every path the
//     switch writes (`diff HEAD refs/heads/<home>`): a forced switch
//     overwrites untracked and ignored files.
//  5. HEAD must now be refs/heads/<home> and the index must equal HEAD;
//     the working-tree-only differences left are remembered as phantoms
//     (below). This runs before step 6, so a take-back that stays pending
//     does not make every later take quarantine the phantoms again
//     (review round 2), and again after a step 6 that moved the tree.
//  6. A take-back a failed ResetBranch left undone is settled
//     (settlePendingTakeBacks): retried, or the restore fails while the
//     branch still holds the refused merge. On this tree a take-back
//     whose two-tree switch is refused quarantines and resets instead
//     (ResetBranch, review round 2), so content no checkout makes clean
//     cannot keep it pending.
//
// Phantoms (review round 1). A working-tree difference `reset --hard`
// cannot remove — a `text eol=lf` attribute over a blob committed with
// CRLF, two tracked paths that differ only in case on a case-insensitive
// filesystem — is content, not damage: git rewrites the file and still
// reports it modified. Such a difference used to fail step 5 ("tracked
// changes remain") on every later take, a workspace-wide outage that a
// divergence branch's content could trigger. Phantoms are now accepted: a
// path whose difference survived the reset is reported (Warn, once per
// state; TreeRestoreReport.Phantoms), and as long as its index blob and its
// working-tree entry stay the same, later restores neither quarantine nor
// reset it again (treePhantoms). The entry is identified without reading
// through it (phantomStatesOf, review round 2): its type, and a regular
// file's filtered blob or a symbolic link's own target — a collision of a
// file and a link is a type change, and the link is never followed. Only
// the index is held to HEAD; a staged change left after the reset still
// fails the restore.
//
// Untracked files are never removed. One that blocks step 4 fails the
// restore, and an operator resolves it.
func (c *CLIClient) restoreHeld(ctx context.Context, home string) (TreeRestoreReport, error) {
	const op = "restore_tree"
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), treeRestoreTimeout)
	defer cancel()
	var rep TreeRestoreReport
	fail := func(err error) (TreeRestoreReport, error) {
		if rep.QuarantineSHA != "" {
			observe.Logger(ctx).Error("shared working tree restore failed after tracked changes were quarantined",
				"home_branch", home, "previous_ref", rep.PreviousRef, "quarantine_sha", rep.QuarantineSHA, "error", err)
			observe.AuditLog(ctx, "working_tree_restore_failed",
				"home_branch", home, "previous_ref", rep.PreviousRef, "quarantine_sha", rep.QuarantineSHA)
		}
		return rep, &TreeRestoreError{Branch: home, Err: err, QuarantineSHA: rep.QuarantineSHA}
	}
	obstructed := func(p string) error {
		return &GitError{Kind: ErrKindPermanent, Op: op,
			Message: fmt.Sprintf("an untracked file is in the way of a tracked path the restore would rewrite (%q); "+
				"nothing was reset — move it out of the working tree", p)}
	}
	resetHard := func() error {
		if p, err := c.untrackedObstruction(ctx, op, "HEAD"); err != nil {
			return err
		} else if p != "" {
			return obstructed(p)
		}
		_, _, err := c.runWith(ctx, op, runOpts{}, "reset", "--hard", "-q")
		return err
	}
	short, full, err := checkBranch(op, "home branch", home)
	if err != nil {
		return fail(err)
	}

	gitDir, _, err := c.runWith(ctx, op, runOpts{}, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fail(err)
	}
	lockFile := filepath.Join(strings.TrimSpace(gitDir), "index.lock")
	if _, err := os.Lstat(lockFile); err == nil {
		return fail(&GitError{Kind: ErrKindTransient, Op: op,
			Message: fmt.Sprintf("repository locked: %s exists (left by a crashed git, or held by another process); "+
				"remove it once no git runs in this tree", lockFile)})
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fail(&GitError{Kind: ErrKindPermanent, Op: op, Message: "cannot check for index.lock", Err: err})
	}

	headRef := func() (string, error) {
		out, _, err := c.runWith(ctx, op, runOpts{}, "symbolic-ref", "-q", "HEAD")
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
				return "", nil // detached
			}
			return "", err
		}
		return strings.TrimSpace(out), nil
	}
	tracked := func() ([]trackedChange, error) {
		out, _, err := c.runWith(ctx, op, runOpts{}, "status", "--porcelain=v1", "-z", "--untracked-files=no")
		if err != nil {
			return nil, err
		}
		return parseTrackedStatus(out), nil
	}

	if rep.PreviousRef, err = headRef(); err != nil {
		return fail(err)
	}
	if _, _, err := c.runWith(ctx, op, runOpts{}, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		rep.AbortedMerge = true
		if _, _, abortErr := c.runWith(ctx, op, runOpts{}, "merge", "--abort"); abortErr != nil {
			if err := resetHard(); err != nil {
				return fail(err)
			}
		}
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return fail(err)
		}
	}

	changes, err := tracked()
	if err != nil {
		return fail(err)
	}
	// residual: the working-tree-only differences left once the tree is
	// as clean as a hard reset makes it (phantoms).
	var residual []trackedChange
	switch {
	case len(changes) == 0:
	case c.knownPhantoms(ctx, op, changes):
		residual = changes
	default:
		if p, err := c.untrackedObstruction(ctx, op, "HEAD"); err != nil {
			return fail(err)
		} else if p != "" {
			return fail(obstructed(p))
		}
		sha, err := c.quarantineTracked(ctx, op)
		if err != nil {
			return fail(err)
		}
		if sha == "" {
			return fail(&GitError{Kind: ErrKindPermanent, Op: op,
				Message: "tracked changes found but git stash create produced no commit; nothing was reset"})
		}
		rep.QuarantineSHA = sha
		observe.Logger(ctx).Warn("shared working tree: tracked changes quarantined",
			"home_branch", short, "previous_ref", rep.PreviousRef, "quarantine_sha", sha)
		if err := resetHard(); err != nil {
			return fail(err)
		}
		if residual, err = tracked(); err != nil {
			return fail(err)
		}
	}
	for _, ch := range residual {
		if ch.staged() {
			return fail(&GitError{Kind: ErrKindPermanent, Op: op,
				Message: fmt.Sprintf("staged changes remain after the reset (%q)", ch.path)})
		}
	}

	if rep.PreviousRef != full {
		args := []string{"switch", "--no-guess", "--no-overwrite-ignore", "--end-of-options", short}
		if len(residual) > 0 {
			// Phantoms make the plain switch refuse. A forced switch
			// overwrites untracked and ignored files, so check every path it
			// writes first.
			if p, err := c.untrackedObstruction(ctx, op, "HEAD", full); err != nil {
				return fail(err)
			} else if p != "" {
				return fail(obstructed(p))
			}
			args = []string{"switch", "--no-guess", "--discard-changes", "--end-of-options", short}
		}
		// --no-overwrite-ignore (codex review round 3): git's default
		// overwrites an ignored file at a path the home branch tracks;
		// the restore never touches untracked or ignored files, so such a
		// file fails the switch instead.
		if _, _, err := c.runWith(ctx, op, runOpts{}, args...); err != nil {
			return fail(err)
		}
		rep.SwitchedHEAD = true
	}

	// Step 5, and the phantoms. It runs before the take-backs are settled
	// as well as after a settle that changed the tree (review round 2): a
	// settle that fails leaves the phantoms remembered, so the next take
	// does not quarantine them again while the take-back stays pending.
	verify := func() error {
		ref, err := headRef()
		if err != nil {
			return err
		}
		if ref != full {
			return &GitError{Kind: ErrKindPermanent, Op: op,
				Message: fmt.Sprintf("HEAD is %q after the restore, not %s", ref, full)}
		}
		left, err := tracked()
		if err != nil {
			return err
		}
		for _, ch := range left {
			if ch.staged() {
				return &GitError{Kind: ErrKindPermanent, Op: op,
					Message: fmt.Sprintf("staged changes remain after the restore (%q)", ch.path)}
			}
		}
		rep.Phantoms = c.rememberPhantoms(ctx, op, short, left)
		return nil
	}
	if rep.Repaired() || len(residual) > 0 {
		if err := verify(); err != nil {
			return fail(err)
		}
	}

	took, _, err := c.settlePendingTakeBacks(ctx, op)
	if len(took) > 0 {
		rep.TookBack = took[0]
		if verr := verify(); verr != nil && err == nil {
			err = verr
		}
	}
	if err != nil {
		return fail(err)
	}

	if rep.Repaired() {
		observe.Logger(ctx).Warn("shared working tree restored",
			"home_branch", short, "previous_ref", rep.PreviousRef,
			"aborted_merge", rep.AbortedMerge, "quarantine_sha", rep.QuarantineSHA,
			"switched_head", rep.SwitchedHEAD, "took_back", rep.TookBack)
		observe.AuditLog(ctx, "working_tree_restored",
			"home_branch", short, "previous_ref", rep.PreviousRef,
			"aborted_merge", rep.AbortedMerge, "quarantine_sha", rep.QuarantineSHA,
			"took_back", rep.TookBack)
	}
	return rep, nil
}

// quarantineTracked writes the tracked changes of the index and working
// tree against HEAD into an unreferenced commit (`git stash create`: no
// ref, so the git HTTP endpoint never advertises it) and returns its id,
// or "" when git found nothing to save. A fixed identity: the quarantine
// commit is the server's, and stash create must work on a host with no
// user.name configured. Nothing in the tree or the index is changed.
func (c *CLIClient) quarantineTracked(ctx context.Context, op string) (string, error) {
	env := []string{
		"GIT_AUTHOR_NAME=Spine", "GIT_AUTHOR_EMAIL=spine@spine.local",
		"GIT_COMMITTER_NAME=Spine", "GIT_COMMITTER_EMAIL=spine@spine.local",
	}
	out, _, err := c.runWith(ctx, op, runOpts{env: env},
		"stash", "create", "spine: quarantined working-tree state")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if sha != "" && !IsCanonicalObjectName(sha) {
		return "", &GitError{Kind: ErrKindPermanent, Op: op, Message: "git stash create printed no commit id"}
	}
	return sha, nil
}

// trackedChange is one entry of `git status --porcelain=v1 -z
// --untracked-files=no`: X is the index against HEAD, Y the working tree
// against the index.
type trackedChange struct {
	x, y byte
	path string
}

// staged: the index differs from HEAD for the path (or it is unmerged).
func (ch trackedChange) staged() bool { return ch.x != ' ' }

// worktreeOnly: the index matches HEAD and the working-tree entry differs
// from it — modified, its type changed (a file where the index has a
// link, or the reverse) or missing. These are the differences a phantom
// can be (review round 2: a case collision of a file and a symbolic link
// is a type change, of a file and a directory a missing path).
func (ch trackedChange) worktreeOnly() bool {
	return ch.x == ' ' && (ch.y == 'M' || ch.y == 'T' || ch.y == 'D')
}

// parseTrackedStatus parses porcelain v1 -z output. A rename or copy
// entry ("R  to\0from\0") is followed by its source path, which is
// skipped.
func parseTrackedStatus(out string) []trackedChange {
	var changes []trackedChange
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 || f[2] != ' ' {
			continue
		}
		changes = append(changes, trackedChange{x: f[0], y: f[1], path: f[3:]})
		if f[0] == 'R' || f[0] == 'C' {
			i++
		}
	}
	return changes
}

// maxPhantoms bounds the phantom bookkeeping: a tree with more working-tree
// differences than this is quarantined as usual at every restore (and
// logged and audited each time; it never wedges).
const maxPhantoms = 64

// phantomStatesOf reads, for each path, the index entry's blob (`ls-files
// -s`) and a fingerprint of the working-tree entry at the path. A path
// with no stage-0 index entry is left out.
//
// The working-tree entry is identified without reading through it
// (review round 2). Every path is first looked at with
// worktreefs.LstatEntry, which follows nothing — no symbolic link at the
// leaf, no symlinked or non-directory component above it — and opens
// nothing at the leaf:
//
//   - a regular file: "blob <id>", its blob after git's clean filters
//     (`hash-object`, which writes nothing). Only these paths are passed
//     to hash-object, which opens and follows what it is given: before,
//     a symbolic link was hashed through to its target outside the tree,
//     and a link to a FIFO or to /dev/zero blocked the restore, or read
//     without bound, until treeRestoreTimeout;
//   - a symbolic link: "symlink <target>", the link's own text;
//   - any other type (a directory, a FIFO, a device): "type <mode>";
//   - nothing at the path: "absent";
//   - a component above the leaf that is a symbolic link or not a
//     directory (git itself then sees the path as missing): "unreachable
//     <reason> <component>".
//
// Any other refusal or failure is returned, and the caller treats the
// paths as unknown (quarantined).
func (c *CLIClient) phantomStatesOf(ctx context.Context, op string, paths []string) (map[string]phantomState, error) {
	idx, _, err := c.runWith(ctx, op, runOpts{env: []string{"GIT_LITERAL_PATHSPECS=1"}},
		append([]string{"ls-files", "-s", "-z", "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
	index := map[string]string{}
	for _, rec := range strings.Split(idx, "\x00") {
		meta, path, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		if f := strings.Fields(meta); len(f) == 3 && f[2] == "0" {
			index[path] = f[1]
		}
	}
	tree, err := worktreefs.Open(c.repoPath)
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	wt := make(map[string]string, len(paths))
	var regular []string
	for _, p := range paths {
		fi, target, err := tree.LstatEntry(p)
		var pe *worktreefs.PathError
		switch {
		case err == nil && fi.Mode().IsRegular():
			regular = append(regular, p)
		case err == nil && fi.Mode()&fs.ModeSymlink != 0:
			wt[p] = "symlink " + target
		case err == nil:
			wt[p] = "type " + fi.Mode().Type().String()
		case errors.Is(err, fs.ErrNotExist):
			wt[p] = "absent"
		case errors.As(err, &pe) && (pe.Reason == worktreefs.ReasonSymlink || pe.Reason == worktreefs.ReasonNotDir):
			wt[p] = "unreachable " + string(pe.Reason) + " " + pe.Component
		default:
			return nil, err
		}
	}
	if len(regular) > 0 {
		out, _, err := c.runWith(ctx, op, runOpts{}, append([]string{"hash-object", "--"}, regular...)...)
		if err != nil {
			return nil, err
		}
		sums := strings.Fields(out)
		if len(sums) != len(regular) {
			return nil, &GitError{Kind: ErrKindPermanent, Op: op, Message: "hash-object printed an unexpected number of ids"}
		}
		for i, p := range regular {
			wt[p] = "blob " + sums[i]
		}
	}
	states := make(map[string]phantomState, len(paths))
	for _, p := range paths {
		if index[p] != "" {
			states[p] = phantomState{index: index[p], worktree: wt[p]}
		}
	}
	return states, nil
}

// knownPhantoms reports whether changes are exactly phantoms an earlier
// restore of this tree already found: every entry a working-tree-only
// difference (worktreeOnly) of a remembered path whose index blob and
// working-tree fingerprint (phantomStatesOf) are unchanged. Anything else
// — a staged change, a new path, an edit to a phantom path, any error —
// is not, and is quarantined.
func (c *CLIClient) knownPhantoms(ctx context.Context, op string, changes []trackedChange) bool {
	if len(changes) > maxPhantoms {
		return false
	}
	key := canonicalTreeKey(c.repoPath)
	treeMu.Lock()
	known := treePhantoms[key]
	treeMu.Unlock()
	if len(known) == 0 {
		return false
	}
	paths := make([]string, 0, len(changes))
	for _, ch := range changes {
		if !ch.worktreeOnly() {
			return false
		}
		if _, ok := known[ch.path]; !ok {
			return false
		}
		paths = append(paths, ch.path)
	}
	cur, err := c.phantomStatesOf(ctx, op, paths)
	if err != nil {
		return false
	}
	for _, p := range paths {
		if b, ok := cur[p]; !ok || b != known[p] {
			return false
		}
	}
	return true
}

// rememberPhantoms records the working-tree-only differences left after a
// restore (the phantoms), logs them at Warn when they differ from what was
// remembered, and returns their paths. More than maxPhantoms, or any entry
// knownPhantoms would not accept, are reported but not remembered.
func (c *CLIClient) rememberPhantoms(ctx context.Context, op, home string, left []trackedChange) []string {
	key := canonicalTreeKey(c.repoPath)
	if len(left) == 0 {
		treeMu.Lock()
		delete(treePhantoms, key)
		treeMu.Unlock()
		return nil
	}
	paths := make([]string, 0, len(left))
	trackable := len(left) <= maxPhantoms
	for _, ch := range left {
		paths = append(paths, ch.path)
		// Remembered only as knownPhantoms can recognise them again.
		trackable = trackable && ch.worktreeOnly()
	}
	var cur map[string]phantomState
	if trackable {
		var err error
		if cur, err = c.phantomStatesOf(ctx, op, paths); err != nil {
			cur = nil
		}
	}
	treeMu.Lock()
	prev := treePhantoms[key]
	same := len(cur) > 0 && len(prev) == len(cur)
	for p, b := range cur {
		if prev[p] != b {
			same = false
		}
	}
	if len(cur) > 0 {
		treePhantoms[key] = cur
	} else {
		delete(treePhantoms, key)
	}
	treeMu.Unlock()
	if !same {
		shown := paths
		if len(shown) > 10 {
			shown = shown[:10]
		}
		observe.Logger(ctx).Warn("shared working tree: tracked differences that a hard reset cannot remove are left in place "+
			"(line-ending conversion or case-only path collisions in the content); the index matches HEAD",
			"home_branch", home, "paths", shown, "count", len(paths))
	}
	return paths
}

// settlePendingTakeBacks completes, or refuses, the take-backs a failed
// ResetBranch left on this tree (pendingTakeBack). The caller holds the
// tree and ctx carries its ticket (ResetBranch re-enters). Per branch:
//
//   - still at the refused merge: the compare-and-swap is retried; a
//     failure keeps the record and is returned (its class survives, so a
//     ref-lock race stays transient);
//   - moved on but still containing the refused merge (another process,
//     an operator's git): refused, permanently — the merge must be taken
//     back by hand before anything builds on the branch;
//   - no longer containing it, or gone: the record is dropped.
//
// It returns the refused merges it took back, and the branch it failed on.
func (c *CLIClient) settlePendingTakeBacks(ctx context.Context, op string) (took []string, branch string, err error) {
	for b, p := range pendingTakeBacksOf(c.repoPath) {
		tip, ok, err := lookupBranch(ctx, c.repoPath, op, b)
		if err != nil {
			return took, b, err
		}
		if !ok {
			clearPendingTakeBack(c.repoPath, b, p.expected)
			continue
		}
		if tip == p.expected {
			if err := c.ResetBranch(ctx, b, p.to, p.expected); err != nil {
				return took, b, fmt.Errorf("take back the refused merge %s on %s: %w", p.expected, b, err)
			}
			observe.Logger(ctx).Warn("shared working tree: a refused merge a failed take-back had left was taken back",
				"branch", b, "merge_sha", p.expected, "restored_to", p.to)
			took = append(took, p.expected)
			continue
		}
		in, err := c.IsAncestor(ctx, p.expected, tip)
		if err != nil {
			return took, b, err
		}
		if in {
			return took, b, &GitError{Kind: ErrKindPermanent, Op: op,
				Message: fmt.Sprintf("refs/heads/%s still contains the refused merge %s (it moved on to %s outside this server); "+
					"reset it so that it no longer contains that merge", b, p.expected, tip)}
		}
		clearPendingTakeBack(c.repoPath, b, p.expected)
	}
	return took, "", nil
}

// resetTreeOverRefusedSwitch brings the index and working tree of a
// home-branch tree to `to` after `read-tree -m -u <expected> <to>`
// refused (switchErr), on a branch ResetBranch already moved to `to` with
// HEAD attached to it (review round 2 of INIT-023 TASK-012). It is the
// restore's own repair, applied to the take-back: an untracked or ignored
// file standing where the reset writes refuses it and nothing changes
// (untrackedObstruction over `HEAD`, now `to`); the tracked state — the
// refused merge's content in the index, and whatever the working tree
// holds over it — is quarantined (`git stash create`, an unreferenced
// commit, logged at Warn); then `read-tree --reset -u <to>`. A failure is
// returned with switchErr, and ResetBranch moves the branch forward again.
func (c *CLIClient) resetTreeOverRefusedSwitch(ctx context.Context, op, branch, to string, switchErr error) error {
	if p, err := c.untrackedObstruction(ctx, op, "HEAD"); err != nil {
		return fmt.Errorf("%w (and the obstruction check failed: %v)", switchErr, err)
	} else if p != "" {
		return &GitError{Kind: ErrKindPermanent, Op: op,
			Message: fmt.Sprintf("the working tree cannot follow the take-back: an untracked file is in the way of a tracked path it would rewrite (%q); "+
				"move it out of the working tree", p),
			Err: switchErr}
	}
	sha, err := c.quarantineTracked(ctx, op)
	if err != nil {
		return fmt.Errorf("%w (and the quarantine failed: %v)", switchErr, err)
	}
	if sha != "" {
		observe.Logger(ctx).Warn("shared working tree: tracked changes quarantined before a refused merge was taken back",
			"branch", branch, "restore_to", to, "quarantine_sha", sha)
	}
	if _, _, err := c.runWith(ctx, op, runOpts{}, "read-tree", "--reset", "-u", "--end-of-options", to); err != nil {
		return fmt.Errorf("%w (and the reset to %s failed: %v)", switchErr, to, err)
	}
	return nil
}

// untrackedObstruction returns a path where an untracked or ignored file
// stands in the way of a command that rewrites tracked paths, or "".
// revs name the paths it rewrites: `git diff --no-renames --name-only
// <revs>` — "HEAD" for `git reset --hard` (every tracked path whose
// working tree or index differs from HEAD), "HEAD", "<branch>" for a
// forced switch (every path that differs between the two trees). Such a
// command deletes an untracked file at that path, an untracked file
// standing where one of its parent directories must be (a tracked
// directory removed and replaced by a file), and untracked content under
// the path (a tracked file replaced by a directory). Untracked entries
// come from `ls-files --others --directory` (ignored files included, an
// untracked directory as one "dir/" entry). An untracked directory that
// contains a path the command would recreate is reported too,
// conservatively.
func (c *CLIClient) untrackedObstruction(ctx context.Context, op string, revs ...string) (string, error) {
	args := append([]string{"diff", "--no-renames", "--name-only", "-z"}, revs...)
	changed, _, err := c.runWith(ctx, op, runOpts{}, append(args, "--")...)
	if err != nil {
		return "", err
	}
	if changed == "" {
		return "", nil
	}
	others, _, err := c.runWith(ctx, op, runOpts{}, "ls-files", "-z", "--others", "--directory")
	if err != nil {
		return "", err
	}
	if others == "" {
		return "", nil
	}
	untracked := map[string]bool{}      // untracked entry path (no trailing slash)
	underUntracked := map[string]bool{} // every proper parent of an untracked entry
	for _, u := range strings.Split(others, "\x00") {
		u = strings.TrimSuffix(u, "/")
		if u == "" {
			continue
		}
		untracked[u] = true
		for i := strings.IndexByte(u, '/'); i >= 0; i = nextSlash(u, i) {
			underUntracked[u[:i]] = true
		}
	}
	for _, p := range strings.Split(changed, "\x00") {
		if p == "" {
			continue
		}
		if untracked[p] || underUntracked[p] {
			return p, nil
		}
		for i := strings.IndexByte(p, '/'); i >= 0; i = nextSlash(p, i) {
			if untracked[p[:i]] {
				return p[:i], nil
			}
		}
	}
	return "", nil
}

// nextSlash returns the index of the next '/' in s after i, or -1.
func nextSlash(s string, i int) int {
	j := strings.IndexByte(s[i+1:], '/')
	if j < 0 {
		return -1
	}
	return i + 1 + j
}
