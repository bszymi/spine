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
//     instead of failing it.
//  3. A taken sequence is never abandoned half-way: WithWorkingTree runs
//     the entry restore, fn and the exit restore on a context detached
//     from the caller's cancellation and bounded by treeSequenceTimeout
//     (owner question 6). Only the lock wait honours the caller's context.
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
// classification survives errors.As.
type TreeRestoreError struct {
	Branch string
	Err    error
}

func (e *TreeRestoreError) Error() string {
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
}

// Repaired reports whether the restore changed anything.
func (r TreeRestoreReport) Repaired() bool {
	return r.AbortedMerge || r.QuarantineSHA != "" || r.SwitchedHEAD
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
)

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
// When this call TAKES the tree:
//
//   - fn, and the restores around it, run on a context detached from the
//     caller's cancellation and bounded by treeSequenceTimeout (it keeps
//     the caller's values: actor, trace, logger fields). Only the wait for
//     the tree honours the caller's ctx (*TreeWaitError). A sequence the
//     server has started is therefore finished, never left half-way by a
//     client disconnect; one that hits the bound is stopped with SIGTERM
//     and healed by the next restore;
//   - on a tree with a home branch, the tree is restored before fn (a
//     failure returns the *TreeRestoreError and fn does not run) and after
//     fn (a failure is logged at Error; fn's result is returned).
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
	seq, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), treeSequenceTimeout)
	defer cancel()
	home := c.treeHome()
	if home != "" {
		if _, err := c.restoreHeld(seq, home); err != nil {
			return err
		}
	}
	fnErr := fn(seq)
	if home != "" {
		// A fresh bound: fn may have spent the sequence's.
		if _, err := c.restoreHeld(h.ctx, home); err != nil {
			observe.Logger(ctx).Error("shared working tree not restored after a sequence; the next sequence retries the restore",
				"home_branch", home, "error", err)
		}
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
//     commit, the restore fails and nothing is reset.
//  4. HEAD is switched to the home branch with the Checkout argv (`switch
//     --no-guess`), which never detaches onto a same-named tag.
//  5. HEAD must now be refs/heads/<home> and the tracked status clean.
//
// Untracked files are never removed. One that blocks step 4 fails the
// restore, and an operator resolves it.
func (c *CLIClient) restoreHeld(ctx context.Context, home string) (TreeRestoreReport, error) {
	const op = "restore_tree"
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), treeRestoreTimeout)
	defer cancel()
	var rep TreeRestoreReport
	fail := func(err error) (TreeRestoreReport, error) {
		return rep, &TreeRestoreError{Branch: home, Err: err}
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
	tracked := func() (string, error) {
		out, _, err := c.runWith(ctx, op, runOpts{}, "status", "--porcelain=v1", "-z", "--untracked-files=no")
		return out, err
	}

	if rep.PreviousRef, err = headRef(); err != nil {
		return fail(err)
	}
	if _, _, err := c.runWith(ctx, op, runOpts{}, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		rep.AbortedMerge = true
		if _, _, abortErr := c.runWith(ctx, op, runOpts{}, "merge", "--abort"); abortErr != nil {
			if _, _, err := c.runWith(ctx, op, runOpts{}, "reset", "--hard", "-q"); err != nil {
				return fail(err)
			}
		}
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return fail(err)
		}
	}

	status, err := tracked()
	if err != nil {
		return fail(err)
	}
	if status != "" {
		// A fixed identity: the quarantine commit is the server's, and
		// stash create must work on a host with no user.name configured.
		env := []string{
			"GIT_AUTHOR_NAME=Spine", "GIT_AUTHOR_EMAIL=spine@spine.local",
			"GIT_COMMITTER_NAME=Spine", "GIT_COMMITTER_EMAIL=spine@spine.local",
		}
		out, _, err := c.runWith(ctx, op, runOpts{env: env},
			"stash", "create", "spine: quarantined working-tree state")
		if err != nil {
			return fail(err)
		}
		sha := strings.TrimSpace(out)
		if !IsCanonicalObjectName(sha) {
			return fail(&GitError{Kind: ErrKindPermanent, Op: op,
				Message: "tracked changes found but git stash create produced no commit; nothing was reset"})
		}
		rep.QuarantineSHA = sha
		if _, _, err := c.runWith(ctx, op, runOpts{}, "reset", "--hard", "-q"); err != nil {
			return fail(err)
		}
	}

	if rep.PreviousRef != full {
		if _, _, err := c.runWith(ctx, op, runOpts{}, "switch", "--no-guess", "--end-of-options", short); err != nil {
			return fail(err)
		}
		rep.SwitchedHEAD = true
	}

	if rep.Repaired() {
		ref, err := headRef()
		if err != nil {
			return fail(err)
		}
		if ref != full {
			return fail(&GitError{Kind: ErrKindPermanent, Op: op,
				Message: fmt.Sprintf("HEAD is %q after the restore, not %s", ref, full)})
		}
		left, err := tracked()
		if err != nil {
			return fail(err)
		}
		if left != "" {
			return fail(&GitError{Kind: ErrKindPermanent, Op: op,
				Message: "tracked changes remain after the restore"})
		}
		observe.Logger(ctx).Warn("shared working tree restored",
			"home_branch", short, "previous_ref", rep.PreviousRef,
			"aborted_merge", rep.AbortedMerge, "quarantine_sha", rep.QuarantineSHA,
			"switched_head", rep.SwitchedHEAD)
		observe.AuditLog(ctx, "working_tree_restored",
			"home_branch", short, "previous_ref", rep.PreviousRef,
			"aborted_merge", rep.AbortedMerge, "quarantine_sha", rep.QuarantineSHA)
	}
	return rep, nil
}
