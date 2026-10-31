package git

// Aggregate checkout bound (INIT-024 TASK-011; the TASK-010 cross-task
// review's X-1, next to N-1).
//
// The checkout budget (treebudget.go) bounds ONE tree. A branch-scoped
// write (EnterBranch with a branch) puts a whole tree into a private
// directory under os.TempDir(), which every workspace on the host
// shares, and nothing bounded how many of those trees were on that disk
// at once:
//
//   - concurrent entries for the SAME branch each measured, created their
//     directory and wrote the tree before git's own "already checked out"
//     guard refused all but one of them at `switch`, so refused writes
//     materialised the tree too. That guard also races: several linked
//     worktrees could attach to one branch, and the ref compare-and-swap
//     at commit turned the race into failed writes;
//   - concurrent entries for DIFFERENT branches each got the full budget.
//
// Two in-process controls close that, both taken before anything is
// written:
//
//  1. One writer per branch. EnterBranch claims (git common directory,
//     branch) before the lookup, the measure and the temporary directory,
//     and holds the claim until the scope's Cleanup has removed the
//     worktree. A second entry for a claimed branch is REFUSED at once
//     (ErrBranchInUse), not queued: git refused it before this change
//     too, only after the write, and an embedder's error handling keeps
//     seeing an untyped, non-retryable refusal of the same kind. Waiting
//     instead would turn today's refusal into a serialisation the
//     callers never asked for, and would hang a caller that re-enters a
//     branch it already holds.
//  2. Bounded in-flight checkout bytes. With CheckoutLimits.InflightMaxBytes
//     set, a process-wide weighted gate admits a checkout only while the
//     measured trees (TreeStats.TotalBytes) of every admitted, not yet
//     cleaned-up branch scope stay within the ceiling. Admission is FIFO
//     (a large tree is not starved by a stream of small ones) and waits
//     under the caller's context, bounded by the mutate deadline when one
//     is set; a wait that ends first is the transient ErrCheckoutsInFlight
//     refusal. A tree larger than the ceiling on its own could never be
//     admitted and is refused at once with ErrTreeTooLarge
//     (TreeLimitInflightBytes). The weight is returned only after the
//     worktree and its directory are gone.
//
// Both are in-process: they bound what one process writes, not what
// several replicas sharing a disk write together (KNOWN-LIMITATIONS).

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrBranchInUse matches (errors.Is) an EnterBranch refused because
// another write scope of this process holds the same branch of the same
// repository. Nothing was created or written. It is returned wrapped in a
// plain error, not a *GitError, so callers classify it exactly as the
// "already checked out" refusal git returned for the same case before
// INIT-024 TASK-011 (the artifact and workflow services: git_error); the
// write can be retried once the other scope's Cleanup ran.
var ErrBranchInUse = errors.New("git: the branch is checked out by another write scope of this process")

// ErrCheckoutsInFlight matches (errors.Is) an EnterBranch refused because
// the in-flight checkout ceiling (CheckoutLimits.InflightMaxBytes) stayed
// full until its context ended (or its mutate deadline passed). It is
// returned inside *GitError{Kind: ErrKindTransient, Message:
// CheckoutsInFlightMessage}, so IsRetryable is true; errors.As reaches
// the *InflightWaitError. Nothing was created or written.
var ErrCheckoutsInFlight = errors.New("git: the in-flight checkout ceiling is full")

// CheckoutsInFlightMessage is the GitError.Message of an
// ErrCheckoutsInFlight refusal.
const CheckoutsInFlightMessage = "other checkouts in flight fill the checkout ceiling; retry later"

// InflightWaitError is the typed cause of an ErrCheckoutsInFlight
// refusal: the tree's measured bytes, the ceiling it waited under, and
// the error of the context that ended the wait.
type InflightWaitError struct {
	Weight  int64
	Ceiling int64
	Err     error
}

func (e *InflightWaitError) Error() string {
	return fmt.Sprintf("a %d-byte checkout waited for room under the %d-byte in-flight ceiling until %v", e.Weight, e.Ceiling, e.Err)
}

// Unwrap exposes ErrCheckoutsInFlight and the context's error.
func (e *InflightWaitError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrCheckoutsInFlight}
	}
	return []error{ErrCheckoutsInFlight, e.Err}
}

// --- one writer per branch ---

type branchClaimKey struct{ repo, ref string }

var (
	branchClaimsMu sync.Mutex
	branchClaims   = map[branchClaimKey]struct{}{}
)

// claimBranch claims ref (refs/heads/<n>) of the repository at repoDir
// for one write scope. It never waits: ok is false when the key is held.
// release is idempotent.
func claimBranch(repoDir, ref string) (release func(), ok bool) {
	key := branchClaimKey{repo: repoClaimKey(repoDir), ref: ref}
	branchClaimsMu.Lock()
	defer branchClaimsMu.Unlock()
	if _, held := branchClaims[key]; held {
		return nil, false
	}
	branchClaims[key] = struct{}{}
	var once sync.Once
	return func() {
		once.Do(func() {
			branchClaimsMu.Lock()
			delete(branchClaims, key)
			branchClaimsMu.Unlock()
		})
	}, true
}

// repoClaimKey is the canonical git common directory of repoDir: what
// git's own "already checked out" guard is scoped to, since every
// worktree of a repository shares it (so a scope entered through a linked
// worktree of the repository and one entered through its main tree
// claim the same key). It is derived without running git — the claim
// adds no process to EnterBranch — from the layouts a repository root
// has: .git a directory; .git a file naming the git directory of a linked
// worktree, whose commondir file names the common one; or a bare
// repository. Anything else keys by the canonical repoDir, which still
// serialises every entry made through that path (git's own guard covers
// the rest, as before).
func repoClaimKey(repoDir string) string {
	root := canonicalTreeKey(repoDir)
	gitDir := gitDirOf(root)
	if gitDir == "" {
		return root
	}
	common := gitDir
	if raw, ok := readSmallFile(filepath.Join(gitDir, "commondir")); ok {
		if c := firstLine(raw); c != "" {
			if !filepath.IsAbs(c) {
				c = filepath.Join(gitDir, c)
			}
			common = c
		}
	}
	return canonicalTreeKey(common)
}

// gitDirOf returns the git directory of the repository rooted at root, or
// "" when root has none of the layouts repoClaimKey reads.
func gitDirOf(root string) string {
	dotgit := filepath.Join(root, ".git")
	if fi, err := os.Stat(dotgit); err == nil {
		switch {
		case fi.IsDir():
			return dotgit
		case fi.Mode().IsRegular():
			raw, ok := readSmallFile(dotgit)
			if !ok {
				return ""
			}
			line := firstLine(raw)
			p, found := strings.CutPrefix(line, "gitdir:")
			p = strings.TrimSpace(p)
			if !found || p == "" {
				return ""
			}
			if !filepath.IsAbs(p) {
				p = filepath.Join(root, p)
			}
			return p
		}
		return ""
	}
	// A bare repository is its own git directory.
	head, errH := os.Stat(filepath.Join(root, "HEAD"))
	objects, errO := os.Stat(filepath.Join(root, "objects"))
	if errH == nil && head.Mode().IsRegular() && errO == nil && objects.IsDir() {
		return root
	}
	return ""
}

// readSmallFile reads at most 4 KiB of path (a .git or commondir file is
// one short line).
func readSmallFile(path string) (string, bool) {
	f, err := os.Open(path) //nolint:gosec // G304: a repository's own .git / commondir file, under an operator-chosen repoDir
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 4<<10))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// --- bounded in-flight checkout bytes ---

// inflightGate is a FIFO weighted semaphore whose capacity is given per
// acquisition (each caller's ceiling; `spine serve` installs one for the
// process). held is the summed weight of the admitted checkouts.
type inflightGate struct {
	mu      sync.Mutex
	held    int64
	waiters list.List // of *inflightWaiter, in arrival order
}

type inflightWaiter struct {
	weight, ceiling int64
	ready           chan struct{} // closed when admitted
}

// checkoutsInFlight is the process's gate: every branch scope entered
// with a ceiling is weighed in it.
var checkoutsInFlight inflightGate

// acquire admits weight under ceiling, waiting in arrival order until it
// fits or ctx ends. A done ctx never acquires (checked before the wait
// and when the wait ends: Go's select picks at random among ready cases).
// The caller has checked weight <= ceiling.
func (g *inflightGate) acquire(ctx context.Context, weight, ceiling int64) error {
	g.mu.Lock()
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return err
	}
	if g.waiters.Len() == 0 && g.held+weight <= ceiling {
		g.held += weight
		g.mu.Unlock()
		return nil
	}
	w := &inflightWaiter{weight: weight, ceiling: ceiling, ready: make(chan struct{})}
	elem := g.waiters.PushBack(w)
	g.mu.Unlock()

	select {
	case <-w.ready:
		if err := ctx.Err(); err != nil {
			g.release(weight)
			return err
		}
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		select {
		case <-w.ready:
			// Admitted while the context ended: give the weight back.
			g.held -= weight
		default:
			g.waiters.Remove(elem)
		}
		// Either way the queue's head may fit now (this waiter blocked it,
		// or its weight came back).
		g.admitLocked()
		g.mu.Unlock()
		return ctx.Err()
	}
}

// release returns weight and admits the waiters that now fit.
func (g *inflightGate) release(weight int64) {
	g.mu.Lock()
	g.held -= weight
	g.admitLocked()
	g.mu.Unlock()
}

// admitLocked admits waiters from the head of the queue while the head
// fits. A head that does not fit blocks the ones behind it (FIFO), so a
// large tree is never starved by smaller ones.
func (g *inflightGate) admitLocked() {
	for {
		front := g.waiters.Front()
		if front == nil {
			return
		}
		w := front.Value.(*inflightWaiter)
		if g.held+w.weight > w.ceiling {
			return
		}
		g.held += w.weight
		g.waiters.Remove(front)
		close(w.ready)
	}
}

// admitCheckout weighs a branch scope's measured tree in the process
// gate under limits.InflightMaxBytes (> 0; the caller checks). The wait
// is bounded by the caller's context and, when set, the mutate deadline.
// op names the refused operation. The returned release is idempotent.
func admitCheckout(ctx context.Context, op string, weight int64, limits CheckoutLimits) (func(), error) {
	ceiling := limits.InflightMaxBytes
	if weight > ceiling {
		return nil, treeTooLarge(op, &TreeTooLargeError{Limit: TreeLimitInflightBytes, Max: ceiling, Seen: weight})
	}
	wctx, cancel := withMutateDeadline(ctx, limits.MutateTimeout)
	defer cancel()
	if err := checkoutsInFlight.acquire(wctx, weight, ceiling); err != nil {
		return nil, &GitError{Kind: ErrKindTransient, Op: op, Message: CheckoutsInFlightMessage,
			Err: &InflightWaitError{Weight: weight, Ceiling: ceiling, Err: err}}
	}
	var once sync.Once
	return func() { once.Do(func() { checkoutsInFlight.release(weight) }) }, nil
}
