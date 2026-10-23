package git

// Checkout and merge amplification budget (INIT-024 TASK-003, review
// finding N-1).
//
// git stores an object once however many tree entries name it, but a
// checkout writes every occurrence. One 4 MiB blob named by 1000 entries
// of each of 1000 trees is a pack of a few KB that checks out to 10^6
// files and about 4 TB, and the disk it lands on (os.TempDir() for
// EnterBranch's worktrees, the workspace tree for the shared checkout) is
// shared by every workspace on the host — if /tmp is a tmpfs, the
// expansion is memory instead. INIT-023 TASK-003 bounded what Spine's git
// commands print (limits.go); nothing bounded what they write.
//
// A client that carries a TreeBudget (WithTreeBudget, or the process
// default SetDefaultCheckoutLimits installs — `spine serve` does) now
// measures the tree a command is about to write, BEFORE the command:
//
//   - EnterBranch (before `git worktree add`, so a refusal creates no
//     temporary directory), Checkout, Merge (target and source),
//     MergeBound / Merge with SourceSHA (source and target), ResetBranch's
//     `read-tree -m -u` and its `read-tree --reset -u` fallback, and the
//     shared tree's restore when it switches back to the home branch;
//   - a tree over the budget is refused with a permanent *GitError
//     wrapping *TreeTooLargeError (errors.Is(err, ErrTreeTooLarge)), and
//     the command never runs: nothing is written and no ref moves.
//
// The measure (TreeBudget.Measure) reads object HEADERS, never object
// contents: `git ls-tree -r -t -l -z` prints every entry with its blob
// size, which git takes from the object header (for a delta, from the
// delta's result-size header), so measuring an over-budget tree costs a
// listing, not an inflation. The listing is streamed through a parser
// that keeps only running totals and the record it is in the middle of
// (bounded by the path budget), and the moment a total crosses its limit
// the parser stops reading and git is stopped: an over-budget tree costs
// about as much as its first MaxEntries entries. It runs as a read
// command (GIT_ALLOC_LIMIT, the pinned delta-base cache, the C locale).
//
// What a checkout writes is not only the blob sizes git stores:
// attributes in the tree's own .gitattributes rewrite content on the way
// out (adversarial review of this task). `ident` turns every 4-byte
// "$Id$" into "$Id: <object id> $" (47 bytes with SHA-1, 71 with
// SHA-256), `text`/`eol=crlf` doubles a file of newlines, and
// `working-tree-encoding` re-encodes UTF-8 (UTF-32 is 4x, UTF-7 up to 5x
// per character). Measured on git 2.54: a 50,000-byte blob of "$Id$\n"
// under `* ident text eol=crlf` checked out to 490,000 bytes. The measure
// therefore reads the tree's .gitattributes files (at most
// maxAttributeFiles of them, each at most maxAttributeFileBytes; more, a
// larger one, or a symbolic link named .gitattributes is taken as the
// worst case) and multiplies the byte total by the worst-case factor of
// the attribute classes they mention (attributeExpansion) before the
// total-bytes limit is applied. Repositories without such attributes —
// Spine and SMP have none that expand — are measured at face value.
//
// What is not measured (KNOWN-LIMITATIONS.md, "Checkout budget"):
// clean/smudge filter DRIVERS (`filter=<name>` runs only a driver the
// host's git config defines, such as git-lfs; Spine configures none),
// the host's own core.autocrlf (operator config, at most 2x), and the
// interval between the measure and the command: a branch that moves in
// between is checked out at its new tip. In standalone the pre-receive
// hook (service/internal/githttp) holds every pushed tip to the same
// budget, so a pushed branch cannot move to an over-budget tree there.
//
// Mutating commands also get a wall-clock deadline (WithMutateTimeout;
// `spine serve`: 2 minutes), so a checkout the budget somehow admits
// cannot run unbounded. On expiry the command is stopped with SIGTERM
// (newGitCmd), git's own handler removes its lock files, and the call
// returns a permanent *GitError wrapping *MutateTimeoutError.
//
// Both are opt-in for the library (INIT-024 rule 2): a client built with
// neither option, in a process that never called
// SetDefaultCheckoutLimits, measures nothing, starts no extra git process
// and runs every command under the caller's context alone — byte for
// byte the behaviour before this file. Embedders (SMP) that run their own
// checkouts can hold them to the same budget with TreeBudget.Check or
// MeasureTree.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ErrTreeTooLarge is what every checkout-budget refusal matches with
// errors.Is: the tree a checkout, merge or restore was about to write
// exceeds the client's TreeBudget. A retry cannot shrink the tree. It is
// distinct from ErrBlobTooLarge and ErrOutputTooLarge (read bounds).
var ErrTreeTooLarge = errors.New("git: tree exceeds the checkout budget")

// TreeBudget bounds what writing one tree to disk may cost. A field <= 0
// means its default (DefaultTreeMaxEntries, DefaultTreeMaxTotalBytes,
// DefaultTreeMaxPathBytesTotal): the zero TreeBudget is the default
// budget, never "unbounded". Whether a client applies a budget at all is
// a separate choice (WithTreeBudget, SetDefaultCheckoutLimits).
type TreeBudget struct {
	// MaxEntries caps the entries of the tree: files, symbolic links,
	// gitlinks and directories (`ls-tree -r -t` records).
	MaxEntries int64
	// MaxTotalBytes caps the summed blob sizes, after the checkout-time
	// expansion the tree's .gitattributes can apply.
	MaxTotalBytes int64
	// MaxPathBytesTotal caps the summed bytes of every entry's full path.
	MaxPathBytesTotal int64
}

// DefaultTreeBudget is the budget with every limit at its default.
func DefaultTreeBudget() TreeBudget { return TreeBudget{}.Effective() }

// Effective returns b with every unset (<= 0) limit replaced by its
// default.
func (b TreeBudget) Effective() TreeBudget {
	if b.MaxEntries <= 0 {
		b.MaxEntries = DefaultTreeMaxEntries
	}
	if b.MaxTotalBytes <= 0 {
		b.MaxTotalBytes = DefaultTreeMaxTotalBytes
	}
	if b.MaxPathBytesTotal <= 0 {
		b.MaxPathBytesTotal = DefaultTreeMaxPathBytesTotal
	}
	return b
}

// TreeStats is what a measure counted. When the measure stopped at a
// budget crossing (Complete false) every total is a lower bound.
type TreeStats struct {
	Entries  int64 // every record: Blobs + Trees + Gitlinks
	Blobs    int64 // regular files and symbolic links (blob entries)
	Trees    int64 // directories
	Gitlinks int64 // submodule entries (mode 160000; written as an empty directory)
	// TotalBytes is the summed blob sizes as git stores them, before any
	// checkout-time expansion.
	TotalBytes int64
	// PathBytes is the summed byte length of every entry's full path.
	PathBytes int64
	// AttributeFiles counts the entries named .gitattributes (any case).
	AttributeFiles int64
	// Expansion is the worst-case factor the tree's .gitattributes can
	// multiply TotalBytes by when it is checked out (1 when they mention
	// no expanding attribute, or there are none). The total-bytes limit
	// is applied to TotalBytes x Expansion.
	Expansion float64
	// Complete is true when the whole listing was read.
	Complete bool

	// attrClasses is the union of the attribute classes the tree's
	// .gitattributes mention (attrCRLF, attrIdent, attrEncoding), for a
	// merge that applies one side's attributes to the other side's files.
	attrClasses attrClass
	// hexLen is the object-id length the listing printed (40 or 64).
	hexLen int
}

// TreeLimit names the TreeBudget limit a measure crossed.
type TreeLimit string

const (
	TreeLimitEntries    TreeLimit = "entries"
	TreeLimitTotalBytes TreeLimit = "total_bytes"
	TreeLimitPathBytes  TreeLimit = "path_bytes"
)

// TreeTooLargeError is the typed cause of a checkout-budget refusal. It
// is returned inside the envelope
// *GitError{Kind: ErrKindPermanent, Op: <the refused operation>, Message: TreeTooLargeMessage},
// so IsRetryable is false; errors.Is(err, ErrTreeTooLarge) and
// errors.As reach it through GitError.Unwrap. The tree-ish is not
// printed: it is caller- or branch-controlled text.
type TreeTooLargeError struct {
	Limit TreeLimit
	// Max is the limit that was crossed.
	Max int64
	// Seen is the running total when the measure stopped — for
	// TreeLimitTotalBytes after the expansion — so a lower bound of the
	// tree's real total.
	Seen int64
	// Expansion is the attribute expansion factor the total-bytes limit
	// was applied with (1 when none). Seen already includes it.
	Expansion float64
}

// TreeTooLargeMessage is the GitError.Message of every ErrTreeTooLarge
// refusal.
const TreeTooLargeMessage = "tree exceeds the checkout budget"

func (e *TreeTooLargeError) Error() string {
	switch e.Limit {
	case TreeLimitEntries:
		return fmt.Sprintf("the tree has more than %d entries (checkout budget MaxEntries)", e.Max)
	case TreeLimitPathBytes:
		return fmt.Sprintf("the tree's paths total more than %d bytes (checkout budget MaxPathBytesTotal)", e.Max)
	case TreeLimitTotalBytes:
		if e.Expansion > 1 {
			return fmt.Sprintf("the tree would write more than %d bytes (checkout budget MaxTotalBytes; its .gitattributes can expand content up to %.4gx on checkout)", e.Max, e.Expansion)
		}
		return fmt.Sprintf("the tree's files total more than %d bytes (checkout budget MaxTotalBytes)", e.Max)
	}
	return fmt.Sprintf("the tree exceeds the checkout budget (%s > %d)", e.Limit, e.Max)
}

// Is makes errors.Is(err, ErrTreeTooLarge) hold.
func (e *TreeTooLargeError) Is(target error) bool { return target == ErrTreeTooLarge }

func treeTooLarge(op string, e *TreeTooLargeError) *GitError {
	return &GitError{Kind: ErrKindPermanent, Op: op, Message: TreeTooLargeMessage, Err: e}
}

// MutateTimeoutError is the typed cause of a mutating command stopped at
// the client's mutate deadline (WithMutateTimeout). It is returned inside
// *GitError{Kind: ErrKindPermanent, Op: op, Message: MutateTimeoutMessage}:
// a command that needed longer than the deadline would need it again.
// errors.Is(err, context.DeadlineExceeded) holds, and errors.As reaches
// the command's own error (the *exec.ExitError of the SIGTERMed git).
type MutateTimeoutError struct {
	Op      string
	Timeout time.Duration
	Err     error
}

// MutateTimeoutMessage is the GitError.Message of a mutate-deadline stop.
const MutateTimeoutMessage = "mutating git command exceeded its deadline"

func (e *MutateTimeoutError) Error() string {
	return fmt.Sprintf("git %s did not finish within the %s mutate deadline and was stopped", e.Op, e.Timeout)
}

// Unwrap exposes context.DeadlineExceeded and the command's own error.
func (e *MutateTimeoutError) Unwrap() []error {
	if e.Err == nil {
		return []error{context.DeadlineExceeded}
	}
	return []error{context.DeadlineExceeded, e.Err}
}

// CheckoutLimits is what a client holds its checkouts and merges to.
// The zero value is none: no budget and no deadline (the library
// default, today's behaviour).
type CheckoutLimits struct {
	// Budget, when non-nil, is measured before every checkout, merge and
	// restore (its unset fields mean their defaults).
	Budget *TreeBudget
	// MutateTimeout, when > 0, is the wall-clock deadline of every local
	// mutating git command.
	MutateTimeout time.Duration
}

func (l CheckoutLimits) clone() CheckoutLimits {
	if l.Budget != nil {
		b := l.Budget.Effective()
		l.Budget = &b
	}
	if l.MutateTimeout < 0 {
		l.MutateTimeout = 0
	}
	return l
}

// over returns l with every limit it leaves unset taken from base. A set
// limit replaces base's; nothing in l can remove one base has.
func (l CheckoutLimits) over(base CheckoutLimits) CheckoutLimits {
	out := base.clone()
	if l.Budget != nil {
		b := l.Budget.Effective()
		out.Budget = &b
	}
	if l.MutateTimeout > 0 {
		out.MutateTimeout = l.MutateTimeout
	}
	return out
}

// processCheckoutLimits is the process default (SetDefaultCheckoutLimits).
var processCheckoutLimits atomic.Pointer[CheckoutLimits]

// SetDefaultCheckoutLimits installs the checkout budget and mutate
// deadline of every CLIClient in this process that sets neither itself,
// including clients built before the call, and of EnterBranch. Each
// limit a client sets (WithTreeBudget, WithMutateTimeout) replaces the
// default one; no option removes it, so a process that sets a budget
// has no client without one. `spine serve` calls it once at startup with
// the SPINE_GIT_TREE_* and SPINE_GIT_MUTATE_TIMEOUT values. A process
// that never calls it — an embedder — keeps today's behaviour.
func SetDefaultCheckoutLimits(l CheckoutLimits) {
	cp := l.clone()
	processCheckoutLimits.Store(&cp)
}

// DefaultCheckoutLimits returns the process default
// (SetDefaultCheckoutLimits), the zero CheckoutLimits when none was set.
func DefaultCheckoutLimits() CheckoutLimits {
	if p := processCheckoutLimits.Load(); p != nil {
		return p.clone()
	}
	return CheckoutLimits{}
}

// WithTreeBudget makes the client measure every tree it is about to
// check out, merge or restore against b (unset fields: their defaults)
// and refuse one over it with ErrTreeTooLarge before any git command
// writes. It replaces the process default budget for this client.
func WithTreeBudget(b TreeBudget) CLIOption {
	return func(c *CLIClient) {
		eff := b.Effective()
		c.treeBudget = &eff
	}
}

// WithMutateTimeout gives every local mutating git command the client
// runs (checkout, merge, commit, add, branch, config; read-tree, reset,
// switch and the other plumbing of the merge binding and the shared
// tree's restore; a clone's checkout, which Clone then runs as its own
// `reset --hard`) a wall-clock deadline of d. Push and the fetch of a
// clone, which depend on a network, are not deadlined by it. d <= 0 keeps
// the default: the process default (SetDefaultCheckoutLimits), else none.
func WithMutateTimeout(d time.Duration) CLIOption {
	return func(c *CLIClient) {
		if d > 0 {
			c.mutateTimeout = d
		}
	}
}

// CheckoutLimits returns the limits the client applies: its own options
// over the process default. Read at every call, so a later
// SetDefaultCheckoutLimits reaches a client built before it.
func (c *CLIClient) CheckoutLimits() CheckoutLimits {
	return CheckoutLimits{Budget: c.treeBudget, MutateTimeout: c.mutateTimeout}.over(DefaultCheckoutLimits())
}

// withMutateDeadline derives the context a mutating command runs under:
// ctx bounded by d when d > 0, ctx itself otherwise.
func withMutateDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// mutateDeadlineHit reports whether the mutate deadline (and not the
// caller's own context) is what ended a command run under run, derived
// from parent by withMutateDeadline.
func mutateDeadlineHit(parent, run context.Context, d time.Duration) bool {
	return d > 0 && parent.Err() == nil && errors.Is(run.Err(), context.DeadlineExceeded)
}

func mutateTimeout(op string, d time.Duration, err error) *GitError {
	return &GitError{Kind: ErrKindPermanent, Op: op, Message: MutateTimeoutMessage,
		Err: &MutateTimeoutError{Op: op, Timeout: d, Err: err}}
}

// MeasureTree measures the tree of treeish (a commit, a tree or an
// annotated tag of one: a full object id, or a Rev-class name resolved
// like ListFiles — a branch exactly, "HEAD" on a home-branch tree as the
// home branch) against the client's budget, or DefaultTreeBudget when
// the client has none, so the measure itself is always bounded. Over the
// budget it returns the partial TreeStats and an ErrTreeTooLarge
// *GitError. It is the SMP-facing primitive: an embedder that runs its
// own checkouts and merges can measure the tree first, with its own
// limits through TreeBudget.Measure / TreeBudget.Check.
func MeasureTree(ctx context.Context, c *CLIClient, treeish string) (TreeStats, error) {
	if c == nil {
		return TreeStats{}, &GitError{Kind: ErrKindPermanent, Op: "measure_tree", Message: "nil client"}
	}
	b := DefaultTreeBudget()
	if l := c.CheckoutLimits(); l.Budget != nil {
		b = *l.Budget
	}
	return b.Measure(ctx, c, treeish)
}

// Check returns nil when the tree of treeish is within b, the
// ErrTreeTooLarge *GitError when it is not, and the measure's own error
// (a missing object, a git failure) otherwise — never nil on a tree it
// could not measure.
func (b TreeBudget) Check(ctx context.Context, c *CLIClient, treeish string) error {
	_, err := b.Measure(ctx, c, treeish)
	return err
}

// Measure measures the tree of treeish (see MeasureTree) against b,
// stopping at the first crossing. A treeish that names no tree-ish (a
// blob, a missing object) is an error, never an empty tree.
func (b TreeBudget) Measure(ctx context.Context, c *CLIClient, treeish string) (TreeStats, error) {
	const op = measureOp
	if c == nil {
		return TreeStats{}, &GitError{Kind: ErrKindPermanent, Op: op, Message: "nil client"}
	}
	if err := checkRev(op, "treeish", treeish); err != nil {
		return TreeStats{}, err
	}
	rev, err := c.resolveRev(ctx, op, "treeish", c.homeRev(treeish))
	if err != nil {
		return TreeStats{}, err
	}
	return c.measureTree(ctx, rev, b.Effective())
}

// checkTrees is the pre-command budget check of every budgeted
// operation: with no budget on the client it returns nil and starts no
// process (today's behaviour). Otherwise each rev (an object id, or a
// name the caller has already resolved) is measured and the first
// refusal or measure failure is returned under op. A merge passes both
// sides: what it writes is at most both trees, and either side's
// attributes reach the other side's files, so the byte limit is applied
// with the expansion of the union of their attribute classes.
func (c *CLIClient) checkTrees(ctx context.Context, op string, revs ...string) error {
	l := c.CheckoutLimits()
	if l.Budget == nil {
		return nil
	}
	return l.Budget.checkRevs(ctx, c, op, revs...)
}

func (b TreeBudget) checkRevs(ctx context.Context, c *CLIClient, op string, revs ...string) error {
	b = b.Effective()
	stats := make([]TreeStats, 0, len(revs))
	var union attrClass
	for _, rev := range revs {
		st, err := c.measureTree(ctx, rev, b)
		if err != nil {
			return relabel(err, op)
		}
		stats = append(stats, st)
		union |= st.attrClasses
	}
	for _, st := range stats {
		f := union.factor(st.hexLen)
		if e := bytesOver(st.TotalBytes, f, b.MaxTotalBytes); e != nil {
			return treeTooLarge(op, e)
		}
	}
	return nil
}

// relabel puts op on a refusal measureTree returned under its own op
// (measureOp), so the caller sees the operation that was refused
// ("checkout", "merge").
func relabel(err error, op string) error {
	var ge *GitError
	if errors.As(err, &ge) && ge.Op == measureOp {
		cp := *ge
		cp.Op = op
		return &cp
	}
	return err
}

// bytesOver returns the total-bytes refusal when total x factor exceeds
// limit, nil otherwise. Saturating: a product past MaxInt64 is over.
func bytesOver(total int64, factor float64, limit int64) *TreeTooLargeError {
	if factor < 1 {
		factor = 1
	}
	exp := float64(total) * factor
	if exp <= float64(limit) {
		return nil
	}
	seen := int64(math.MaxInt64)
	if exp < float64(math.MaxInt64) {
		seen = int64(exp)
	}
	return &TreeTooLargeError{Limit: TreeLimitTotalBytes, Max: limit, Seen: seen, Expansion: factor}
}

// measureOp is the op of every git command a measure runs (opRead, see
// opClasses) and of the errors it returns; checkTrees relabels them.
const measureOp = "measure_tree"

// measureTree runs the streamed listing of rev and, when the tree has
// .gitattributes, their scan. b must be effective.
func (c *CLIClient) measureTree(ctx context.Context, rev string, b TreeBudget) (TreeStats, error) {
	const op = measureOp
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sink := &treeListingSink{budget: b, stop: cancel}
	sink.stats.Expansion = 1
	// -r recurses, -t lists each directory too (an inode the checkout
	// creates, and the record that makes a deep tree's path bytes grow
	// quadratically), -l adds each blob's size from its object header,
	// -z keeps paths raw, --full-tree ignores the working directory (the
	// pre-receive hook runs in $GIT_DIR).
	cmd := newGitCmd(runCtx, "ls-tree", "-r", "-t", "-l", "-z", "--full-tree", "--end-of-options", rev)
	cmd.Dir = c.repoPath
	cmd.Env = c.commandEnv(opRead)
	stderr := &stderrSink{}
	cmd.Stdout = sink
	cmd.Stderr = stderr
	runErr := cmd.Run()
	if c.measureHook != nil {
		c.measureHook(MeasureTrace{Delivered: sink.delivered, Stats: sink.stats})
	}
	if sink.over != nil {
		// git was stopped by us (EPIPE or SIGTERM): its exit is ours.
		return sink.stats, treeTooLarge(op, sink.over)
	}
	errText := stderr.String()
	if isGitAllocRefusal(errText, c.gitAllocLimit()) {
		return sink.stats, &GitError{Kind: ErrKindPermanent, Op: op, Message: "output exceeds read limit",
			Err: &OutputTooLargeError{Op: op, Limit: c.gitAllocLimit(), GitAlloc: true}}
	}
	if sink.parseErr != nil {
		return sink.stats, &GitError{Kind: ErrKindPermanent, Op: op, Message: "unparseable `git ls-tree` output", Err: sink.parseErr}
	}
	if runErr != nil {
		gitErr := classifyGitError(op, strings.TrimSpace(errText))
		gitErr.Err = runErr
		return sink.stats, gitErr
	}
	if len(sink.carry) > 0 {
		return sink.stats, &GitError{Kind: ErrKindPermanent, Op: op, Message: "unterminated `git ls-tree -z` output"}
	}
	sink.stats.Complete = true
	if err := c.scanAttributes(ctx, &sink.stats, sink.attrOIDs, sink.attrWorst); err != nil {
		return sink.stats, err
	}
	if e := bytesOver(sink.stats.TotalBytes, sink.stats.Expansion, b.MaxTotalBytes); e != nil {
		return sink.stats, treeTooLarge(op, e)
	}
	return sink.stats, nil
}

// MeasureTrace is what a measure reports to the test hook
// (SetMeasureHookForTest): the listing bytes git handed the parser, and
// the totals when it stopped.
type MeasureTrace struct {
	Delivered int64
	Stats     TreeStats
}

// --- the streamed listing parser ---

const (
	// maxRecordMetaBytes bounds the part of an `ls-tree -l` record
	// before its path: "<mode> <type> <oid> <size>\t" is at most about
	// 6+1+6+1+64+1+20+1 bytes; the slack is generous.
	maxRecordMetaBytes = 256
	// maxAttributeFiles is how many .gitattributes files a measure reads.
	// A tree with more is taken to expand by the worst-case factor: a
	// legitimate repository has a handful, and reading each costs a git
	// process.
	maxAttributeFiles = 64
	// maxAttributeFileBytes is the largest .gitattributes a measure reads
	// (a larger one is the worst case). Real ones are a few hundred bytes.
	maxAttributeFileBytes int64 = 64 << 10
)

// treeListingSink parses `git ls-tree -r -t -l -z` output as git writes
// it. Like stdoutSink it implements only io.Writer: os/exec copies into
// it, and the Write that crosses a limit (or meets a malformed record)
// returns an error, which stops the copy and closes the pipe; stop
// cancels the command's context as well, so git is SIGTERMed at once.
type treeListingSink struct {
	budget    TreeBudget
	stop      context.CancelFunc
	stats     TreeStats
	carry     []byte // the record being read, when it spans writes
	delivered int64
	over      *TreeTooLargeError
	parseErr  error
	attrOIDs  []string
	attrWorst bool // a .gitattributes the scan cannot read: assume the worst
}

var errStopListing = errors.New("git: tree listing stopped")

func (s *treeListingSink) Write(p []byte) (int, error) {
	if s.over != nil || s.parseErr != nil {
		return 0, errStopListing
	}
	n := len(p)
	s.delivered += int64(n)
	for len(p) > 0 {
		i := bytes.IndexByte(p, 0)
		if i < 0 {
			// A record that does not end in this write. Its path alone
			// may not exceed what is left of the path budget, so the
			// carried bytes are bounded by MaxPathBytesTotal.
			if int64(len(s.carry)+len(p)) > s.budget.MaxPathBytesTotal-s.stats.PathBytes+maxRecordMetaBytes {
				s.cross(&TreeTooLargeError{Limit: TreeLimitPathBytes, Max: s.budget.MaxPathBytesTotal,
					Seen: s.stats.PathBytes + int64(len(s.carry)+len(p)) - maxRecordMetaBytes})
				return 0, errStopListing
			}
			s.carry = append(s.carry, p...)
			return n, nil
		}
		rec := p[:i]
		if len(s.carry) > 0 {
			s.carry = append(s.carry, rec...)
			rec = s.carry
		}
		if err := s.record(rec); err != nil {
			return 0, err
		}
		s.carry = s.carry[:0]
		p = p[i+1:]
	}
	return n, nil
}

func (s *treeListingSink) cross(e *TreeTooLargeError) {
	s.over = e
	s.stop()
}

func (s *treeListingSink) malformed(format string, args ...any) error {
	s.parseErr = fmt.Errorf("record %d: "+format, append([]any{s.stats.Entries + 1}, args...)...)
	s.stop()
	return errStopListing
}

// record parses one "<mode> <type> <oid> <size>\t<path>" record and adds
// it to the totals. The path is never echoed (committed, attacker-chosen
// text).
func (s *treeListingSink) record(rec []byte) error {
	tab := bytes.IndexByte(rec, '\t')
	if tab < 0 {
		return s.malformed("no tab")
	}
	meta, p := rec[:tab], rec[tab+1:]
	if len(p) == 0 {
		return s.malformed("empty path")
	}
	f := strings.Fields(string(meta))
	if len(f) != 4 || !IsFullObjectName(f[2]) {
		return s.malformed("unexpected fields")
	}
	mode, typ, oid, size := f[0], f[1], f[2], f[3]
	var blobBytes int64
	switch {
	case typ == TreeEntryBlob && (mode == "100644" || mode == "100755" || mode == "120000"):
		v, err := strconv.ParseInt(size, 10, 64)
		if err != nil || v < 0 {
			// "BAD" (a missing object) or anything else: unmeasurable.
			return s.malformed("blob size %q", size)
		}
		blobBytes = v
		s.stats.Blobs++
		if strings.EqualFold(path.Base(string(p)), ".gitattributes") {
			s.stats.AttributeFiles++
			switch {
			case mode == "120000" || len(s.attrOIDs) >= maxAttributeFiles || v > maxAttributeFileBytes:
				// A symbolic link is read through by older git; too many
				// or too large to read cheaply: the worst case.
				s.attrWorst = true
			default:
				s.attrOIDs = append(s.attrOIDs, oid)
			}
		}
	case typ == TreeEntryTree && mode == "040000" && size == "-":
		s.stats.Trees++
	case typ == TreeEntryCommit && mode == "160000" && size == "-":
		s.stats.Gitlinks++
	default:
		return s.malformed("unexpected mode/type %q %q", mode, typ)
	}
	if s.stats.hexLen == 0 {
		s.stats.hexLen = len(oid)
	}
	s.stats.Entries++
	s.stats.PathBytes = satAdd(s.stats.PathBytes, int64(len(p)))
	s.stats.TotalBytes = satAdd(s.stats.TotalBytes, blobBytes)
	switch {
	case s.stats.Entries > s.budget.MaxEntries:
		s.cross(&TreeTooLargeError{Limit: TreeLimitEntries, Max: s.budget.MaxEntries, Seen: s.stats.Entries})
	case s.stats.PathBytes > s.budget.MaxPathBytesTotal:
		s.cross(&TreeTooLargeError{Limit: TreeLimitPathBytes, Max: s.budget.MaxPathBytesTotal, Seen: s.stats.PathBytes})
	case s.stats.TotalBytes > s.budget.MaxTotalBytes:
		// Face value already over: no attribute can make it smaller.
		s.cross(&TreeTooLargeError{Limit: TreeLimitTotalBytes, Max: s.budget.MaxTotalBytes, Seen: s.stats.TotalBytes, Expansion: 1})
	default:
		return nil
	}
	return errStopListing
}

func satAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// --- checkout-time expansion by .gitattributes ---

// attrClass is a set of attribute classes that rewrite content on
// checkout.
type attrClass uint8

const (
	// attrCRLF: text / eol / crlf — LF becomes CRLF, at most 2x.
	attrCRLF attrClass = 1 << iota
	// attrIdent: ident — "$Id$" (4 bytes) becomes "$Id: <hex> $".
	attrIdent
	// attrEncoding: working-tree-encoding — UTF-8 re-encoded: UTF-32 is
	// 4x, UTF-7 up to 5x for an isolated character; 8x bounds every
	// encoding iconv offers for one input byte.
	attrEncoding

	attrAll = attrCRLF | attrIdent | attrEncoding

	encodingFactor = 8
)

// factor is the worst-case expansion of the classes in a, for object ids
// of hexLen hex digits (40 when unknown).
func (a attrClass) factor(hexLen int) float64 {
	if hexLen <= 0 {
		hexLen = 40
	}
	f := 1.0
	if a&attrCRLF != 0 {
		f *= 2
	}
	if a&attrIdent != 0 {
		// "$Id: " + hex + " $" for every 4-byte "$Id$".
		f *= float64(5+hexLen+2) / 4
	}
	if a&attrEncoding != 0 {
		f *= encodingFactor
	}
	return f
}

// attributeClasses returns the classes a .gitattributes text can turn
// on. The match is a plain substring search, deliberately loose: a
// pattern or a comment that merely contains the word counts, and so does
// a macro defined with it ("[attr]m ident" then "* m"), which no
// token-level parse that skipped macros would see. A false positive only
// tightens the byte limit for that tree.
func attributeClasses(text []byte) attrClass {
	var a attrClass
	if bytes.Contains(text, []byte("text")) || bytes.Contains(text, []byte("eol")) || bytes.Contains(text, []byte("crlf")) {
		a |= attrCRLF
	}
	if bytes.Contains(text, []byte("ident")) {
		a |= attrIdent
	}
	if bytes.Contains(text, []byte("encoding")) {
		a |= attrEncoding
	}
	return a
}

// scanAttributes reads the tree's .gitattributes blobs (bounded) and sets
// st's attribute classes and expansion factor.
func (c *CLIClient) scanAttributes(ctx context.Context, st *TreeStats, oids []string, worst bool) error {
	var classes attrClass
	if worst {
		classes = attrAll
	}
	for _, oid := range oids {
		if classes == attrAll {
			break
		}
		out, err := c.runBounded(ctx, "measure_tree", maxAttributeFileBytes, 0, "cat-file", "blob", oid)
		if err != nil {
			if errors.Is(err, ErrOutputTooLarge) {
				classes = attrAll
				continue
			}
			return err
		}
		classes |= attributeClasses(out)
	}
	st.attrClasses = classes
	st.Expansion = classes.factor(st.hexLen)
	return nil
}
