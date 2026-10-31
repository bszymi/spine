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
//  1. One writer per branch. EnterBranch claims (repository, branch)
//     before the lookup, the measure and the temporary directory, and
//     holds the claim until the scope's Cleanup has removed the worktree.
//     The repository is identified by its git common directory's (device,
//     inode), so every spelling of one repository — a symbolic link, a
//     case variant on case-insensitive storage, a subdirectory, a linked
//     worktree — is one key. A second entry for a claimed branch is
//     REFUSED at once (ErrBranchInUse), not queued: git refused it before
//     this change too, only after the write, and an embedder's error
//     handling keeps seeing an untyped, non-retryable refusal of the same
//     kind. Waiting instead would turn today's refusal into a
//     serialisation the callers never asked for, and would hang a caller
//     that re-enters a branch it already holds.
//  2. Bounded in-flight checkout weight. With CheckoutLimits.InflightMaxBytes
//     set, a process-wide gate admits a checkout only while the weights
//     of every admitted, not yet cleaned-up branch scope stay within the
//     ceiling. A tree's weight (InflightWeight) is its measured bytes plus
//     InflightEntryWeight per entry, so a tree of many empty files or
//     directories — which costs inodes and blocks, not blob bytes — is
//     weighed too. The gate is fair across repositories (in shared mode,
//     across workspaces):
//     - each repository has its own FIFO queue, and the queues are served
//       round-robin, so one repository's waiters never block another
//       repository's entry that fits in the remaining room;
//     - one repository holds at most half the ceiling; a single tree
//       heavier than half is admitted only while its repository holds
//       nothing, so one workspace at its maximum leaves the other half to
//       everyone else.
//     An entry waits under the caller's context, bounded by the mutate
//     deadline when one is set; a wait that ends first is the transient
//     ErrCheckoutsInFlight refusal. A tree heavier than the ceiling on its
//     own could never be admitted and is refused at once with
//     ErrTreeTooLarge (TreeLimitInflightBytes). The weight and the claim
//     are returned only after the worktree and its directory are gone.
//
// Both are in-process: they bound what one process writes, not what
// several replicas sharing a disk write together (KNOWN-LIMITATIONS).

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
// the in-flight checkout ceiling (CheckoutLimits.InflightMaxBytes) left no
// room for its tree, or its repository's share of the ceiling was taken,
// until its context ended (or its mutate deadline passed) — or because its
// branch kept moving while it was being admitted (ErrBranchMoved, which
// matches too). It is returned inside *GitError{Kind: ErrKindTransient},
// so IsRetryable is true; errors.As reaches the *InflightWaitError.
// Nothing is left on disk.
var ErrCheckoutsInFlight = errors.New("git: the in-flight checkout ceiling is full")

// CheckoutsInFlightMessage is the GitError.Message of an
// ErrCheckoutsInFlight refusal for want of room.
const CheckoutsInFlightMessage = "other checkouts in flight fill the checkout ceiling; retry later"

// ErrBranchMoved matches (errors.Is) an EnterBranch with an in-flight
// ceiling refused because its branch moved between the measure that
// weighed its tree and the checkout, and kept moving (codex review round
// 3): the tree checked out must be the tree weighed, or the ceiling would
// account for less than is on disk. It also matches ErrCheckoutsInFlight;
// the *GitError's Message is BranchMovedMessage. Retryable.
var ErrBranchMoved = errors.New("git: the branch moved while its checkout was being admitted")

// BranchMovedMessage is the GitError.Message of an ErrBranchMoved refusal.
const BranchMovedMessage = "the branch moved while its checkout was being admitted; retry"

// branchMovedAttempts is how often EnterBranch re-measures and re-admits a
// branch that moved while it waited before it refuses with ErrBranchMoved.
const branchMovedAttempts = 3

func branchMoved(op string, weight, ceiling int64) error {
	return &GitError{Kind: ErrKindTransient, Op: op, Message: BranchMovedMessage,
		Err: &InflightWaitError{Weight: weight, Ceiling: ceiling, Err: ErrBranchMoved}}
}

// InflightWaitError is the typed cause of an ErrCheckoutsInFlight
// refusal: the tree's weight, the ceiling it waited under, and the error
// of the context that ended the wait.
type InflightWaitError struct {
	Weight  int64
	Ceiling int64
	Err     error
}

func (e *InflightWaitError) Error() string {
	return fmt.Sprintf("a checkout weighing %d bytes waited for room under the %d-byte in-flight ceiling until %v", e.Weight, e.Ceiling, e.Err)
}

// Unwrap exposes ErrCheckoutsInFlight and the context's error.
func (e *InflightWaitError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrCheckoutsInFlight}
	}
	return []error{ErrCheckoutsInFlight, e.Err}
}

// --- the weight ---

// InflightEntryWeight is what each entry of a measured tree (a file, a
// symbolic link, a directory, a gitlink's empty directory) adds to the
// tree's in-flight weight: one 4 KiB filesystem block, standing for the
// inode, the directory entry and the index entry a checkout creates for
// it, which the blob bytes do not count. A tree of many empty files
// therefore weighs about 4 KiB per file instead of nothing.
const InflightEntryWeight int64 = 4096

// InflightWeight is the weight the in-flight checkout ceiling counts for
// a measured tree: TotalBytes plus InflightEntryWeight per entry
// (saturating at math.MaxInt64). An embedder holding its own checkouts to
// an aggregate bound can weigh them the same way.
func InflightWeight(st TreeStats) int64 {
	return saturatingWeight(st.TotalBytes, st.Entries)
}

// MaxInflightWeight is the heaviest weight a tree within b can have:
// MaxTotalBytes plus InflightEntryWeight per allowed entry (b's unset
// limits mean their defaults). A ceiling below it refuses some tree the
// budget admits; `spine serve`'s default ceiling is derived from it.
func (b TreeBudget) MaxInflightWeight() int64 {
	e := b.Effective()
	return saturatingWeight(e.MaxTotalBytes, e.MaxEntries)
}

func saturatingWeight(bytes, entries int64) int64 {
	if bytes < 0 {
		bytes = 0
	}
	if entries <= 0 {
		return bytes
	}
	if entries > (math.MaxInt64-bytes)/InflightEntryWeight {
		return math.MaxInt64
	}
	return bytes + entries*InflightEntryWeight
}

// --- one writer per branch ---

type branchClaimKey struct{ repo, ref string }

var (
	branchClaimsMu sync.Mutex
	branchClaims   = map[branchClaimKey]struct{}{}
)

// claimBranch claims ref (refs/heads/<n>) of the repository identified by
// repoKey (repoClaimKey) for one write scope. It never waits: ok is false
// when the key is held. release is idempotent.
func claimBranch(repoKey, ref string) (release func(), ok bool) {
	key := branchClaimKey{repo: repoKey, ref: ref}
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

// repoClaimKey identifies the repository repoDir belongs to: its git
// common directory — what git's own "already checked out" guard is scoped
// to, shared by every worktree of the repository — by (device, inode)
// where the platform reports them, so every spelling of the directory (a
// symbolic link, a case variant on case-insensitive storage, "..") is one
// key; else by its symlink-free path. The same key is the repository's
// share of the in-flight gate.
//
// The common directory is found without running git (the claim adds no
// process to EnterBranch), the way git discovers it: from repoDir upward,
// the first directory with a .git (a directory, or a file naming the git
// directory of a linked worktree, whose commondir file names the common
// one), or that is itself a bare repository. When none is found the key is
// repoDir's own (device, inode), which still serialises every entry made
// through that directory — git's own guard covers the rest, as before. A
// wrong key can only weaken the claim to git's own guard, never refuse an
// entry another repository's scope holds: two repositories never share a
// common directory.
//
// Only git-administrative entries are read (a .git, a commondir, a bare
// directory's HEAD and objects), and a file is opened only when what it
// names is a regular file — no FIFO or device is ever opened — and read to
// at most 4 KiB (core/worktreefs scan allowlist).
func repoClaimKey(repoDir string) string {
	root := canonicalTreeKey(repoDir)
	target := root
	if common := commonDirOf(root); common != "" {
		target = common
	}
	if id, ok := fileIdentity(target); ok {
		return id
	}
	return canonicalTreeKey(target)
}

// commonDirOf returns the git common directory of the repository that
// contains dir (searched upward like git's discovery), or "".
func commonDirOf(dir string) string {
	for {
		if gitDir := gitDirOf(dir); gitDir != "" {
			common := gitDir
			if raw, ok := readSmallFile(filepath.Join(gitDir, "commondir")); ok {
				if c := firstLine(raw); c != "" {
					if !filepath.IsAbs(c) {
						c = filepath.Join(gitDir, c)
					}
					common = c
				}
			}
			return common
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// gitDirOf returns the git directory of a repository whose top is root,
// or "" when root has none of the layouts repoClaimKey reads.
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
			p, found := strings.CutPrefix(firstLine(raw), "gitdir:")
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
// one short line), and only when what path names is a regular file: a
// FIFO or device is reported as unreadable and never opened.
func readSmallFile(path string) (string, bool) {
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
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

// --- the release of a branch scope ---

// scopeReleaseHook, when set (tests only: SetScopeReleaseHookForTest), is
// called with the scope's repoDir, private parent directory and worktree
// path ("" for those not created yet) immediately before a branch scope
// returns its weight and its claim, so a test can assert that the
// worktree is gone by then.
var scopeReleaseHook atomic.Pointer[func(repoDir, parent, worktreeDir string)]

// --- bounded in-flight checkout weight ---

// inflightGate is the process's admission control for branch scopes: a
// weighted semaphore whose capacity is given per acquisition (each
// caller's ceiling; `spine serve` installs one for the process), with one
// FIFO queue per repository, the queues served round-robin, and a cap of
// half the ceiling per repository.
type inflightGate struct {
	mu    sync.Mutex
	held  int64                 // summed weight of every admitted checkout
	repos map[string]*repoShare // repositories holding weight or waiting
	turns list.List             // of *repoShare with waiters, in round-robin order
}

// repoShare is one repository's part of the gate.
type repoShare struct {
	key     string
	held    int64
	waiters list.List     // of *inflightWaiter, in arrival order
	turn    *list.Element // in inflightGate.turns while it has waiters
}

type inflightWaiter struct {
	weight, ceiling int64
	ready           chan struct{} // closed when admitted
}

// checkoutsInFlight is the process's gate: every branch scope entered
// with a ceiling is weighed in it.
var checkoutsInFlight = inflightGate{repos: map[string]*repoShare{}}

// fits reports whether weight may be admitted for s under ceiling now:
// room under the ceiling, and either nothing held by the repository or
// the repository staying within half the ceiling.
func (g *inflightGate) fits(s *repoShare, weight, ceiling int64) bool {
	if g.held+weight > ceiling {
		return false
	}
	return s.held == 0 || s.held+weight <= ceiling/2
}

func (g *inflightGate) shareLocked(key string) *repoShare {
	s := g.repos[key]
	if s == nil {
		s = &repoShare{key: key}
		g.repos[key] = s
	}
	return s
}

// dropIfIdleLocked forgets a repository that holds nothing and waits for
// nothing, so the map stays bounded by the repositories in flight.
func (g *inflightGate) dropIfIdleLocked(s *repoShare) {
	if s.held == 0 && s.waiters.Len() == 0 && g.repos[s.key] == s {
		delete(g.repos, s.key)
	}
}

// acquire admits weight for the repository key under ceiling, waiting in
// the repository's arrival order until it fits or ctx ends. A done ctx
// never acquires (checked before the wait and when the wait ends: Go's
// select picks at random among ready cases). The caller has checked
// weight <= ceiling.
func (g *inflightGate) acquire(ctx context.Context, key string, weight, ceiling int64) (*repoShare, error) {
	g.mu.Lock()
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return nil, err
	}
	s := g.shareLocked(key)
	// Only this repository's own queue can make an arrival wait for an
	// earlier one: another repository's waiters never do.
	if s.waiters.Len() == 0 && g.fits(s, weight, ceiling) {
		g.held += weight
		s.held += weight
		g.mu.Unlock()
		return s, nil
	}
	w := &inflightWaiter{weight: weight, ceiling: ceiling, ready: make(chan struct{})}
	elem := s.waiters.PushBack(w)
	if s.turn == nil {
		s.turn = g.turns.PushBack(s)
	}
	g.mu.Unlock()

	select {
	case <-w.ready:
		if err := ctx.Err(); err != nil {
			g.release(s, weight)
			return nil, err
		}
		return s, nil
	case <-ctx.Done():
		g.mu.Lock()
		select {
		case <-w.ready:
			// Admitted while the context ended: give the weight back.
			g.held -= weight
			s.held -= weight
		default:
			s.waiters.Remove(elem)
			if s.waiters.Len() == 0 && s.turn != nil {
				g.turns.Remove(s.turn)
				s.turn = nil
			}
		}
		// Either way a head may fit now (this waiter blocked its
		// repository's queue, or its weight came back).
		g.admitLocked()
		g.dropIfIdleLocked(s)
		g.mu.Unlock()
		return nil, ctx.Err()
	}
}

// release returns weight held by s and admits the waiters that now fit.
func (g *inflightGate) release(s *repoShare, weight int64) {
	g.mu.Lock()
	g.held -= weight
	s.held -= weight
	g.admitLocked()
	g.dropIfIdleLocked(s)
	g.mu.Unlock()
}

// admitLocked serves the repositories with waiters round-robin. Each pass
// offers every such repository's head one turn, in the order of the turn
// list. A repository that is served goes behind every other — it had its
// turn; one whose head does not fit KEEPS its place (codex review round 3:
// rotating it too restored the original order after every pass, so one
// repository's backlog could take each freed slot before another
// repository waiting for the same room). So the room that frees next goes
// first to the repository that has waited longest since it was last
// served. A head that does not fit blocks only its own repository's
// queue. Passes repeat while one admits something.
func (g *inflightGate) admitLocked() {
	for {
		admitted := false
		pass := make([]*list.Element, 0, g.turns.Len())
		for e := g.turns.Front(); e != nil; e = e.Next() {
			pass = append(pass, e)
		}
		for _, e := range pass {
			s := e.Value.(*repoShare)
			head := s.waiters.Front()
			w := head.Value.(*inflightWaiter)
			if !g.fits(s, w.weight, w.ceiling) {
				continue
			}
			g.held += w.weight
			s.held += w.weight
			s.waiters.Remove(head)
			close(w.ready)
			admitted = true
			if s.waiters.Len() == 0 {
				g.turns.Remove(e)
				s.turn = nil
			} else {
				g.turns.MoveToBack(e)
			}
		}
		if !admitted {
			return
		}
	}
}

// admitCheckout weighs a branch scope's tree (weight, InflightWeight of
// its measure) for the repository key in the process gate under
// limits.InflightMaxBytes (> 0; the caller checks). The wait is bounded
// by the caller's context and, when set, the mutate deadline. op names
// the refused operation. The returned release is idempotent.
func admitCheckout(ctx context.Context, op, repoKey string, weight int64, limits CheckoutLimits) (func(), error) {
	ceiling := limits.InflightMaxBytes
	if weight > ceiling {
		return nil, treeTooLarge(op, &TreeTooLargeError{Limit: TreeLimitInflightBytes, Max: ceiling, Seen: weight})
	}
	wctx, cancel := withMutateDeadline(ctx, limits.MutateTimeout)
	defer cancel()
	s, err := checkoutsInFlight.acquire(wctx, repoKey, weight, ceiling)
	if err != nil {
		return nil, &GitError{Kind: ErrKindTransient, Op: op, Message: CheckoutsInFlightMessage,
			Err: &InflightWaitError{Weight: weight, Ceiling: ceiling, Err: err}}
	}
	var once sync.Once
	return func() { once.Do(func() { checkoutsInFlight.release(s, weight) }) }, nil
}
