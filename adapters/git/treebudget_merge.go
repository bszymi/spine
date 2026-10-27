package git

// The checkout budget of a merge (INIT-024 TASK-003, review rounds 1-2).
//
// A merge writes neither of its sides: it leaves the RESULT tree in the
// working tree — base, plus what the target added, plus what the source
// added. Measuring each side admitted a result of up to twice every limit,
// and two merges admitted from one base walked main over the budget. So
// the merge's result is computed first — `git merge-tree --write-tree
// <target-tip> <source>`, merge-ort, the machinery `git merge` runs (git
// >= 2.38) — and that tree is measured on every limit before `git merge`
// runs. merge-tree writes the result's new objects (trees, and conflict-
// marker blobs) into the object store; it touches no ref, index or
// working tree.
//
//   - Order (review round 2). merge-tree reads the merge attributes (a
//     `merge=` driver, `binary`) of the working tree it runs in, so it
//     runs AFTER the switch to the target — in exactly the state `git
//     merge` will see — and on the neutral attributes
//     (NeutralCheckoutAttributes), which pin conflict markers to 7
//     characters; computed before the switch, a target that set its own
//     `conflict-marker-size` wrote far more than the result measured.
//     The target's own tree is measured before the switch when the
//     switch is a checkout (HEAD on another branch), as Checkout would
//     measure it; on the target already checked out only the result is.
//     A result equal to the target's tree (an up-to-date replay) writes
//     nothing and is not measured, so a repair merge that brings an
//     over-budget main back under the budget is admitted. The source is
//     never measured alone (on a fast-forward it IS the result).
//   - Allocation. merge-tree and the measured `git merge` run under
//     GIT_ALLOC_LIMIT = max(MaxTotalBytes, the read allocation limit):
//     no single allocation a merge within the budget needs is larger
//     than the bytes the budget admits, and a merge that would need one
//     is refused (ErrTreeTooLarge) before or while git computes it. (The
//     16 MiB read limit alone would refuse a legitimate merge of any
//     packed file over 16 MiB, and `spine serve` has no setting for it.)
//   - Serialisation. Everything after the target lookup runs inside the
//     working-tree hold the merge already takes (lockTree; WithWorkingTree
//     for a governed sequence), and the tip is read again after the switch
//     and again after the result's measure, right before `git merge`: a
//     merge that would not run on the measured tip is refused
//     (transient). Every in-process writer of the tree takes the same
//     hold, so a second merge waits for the first and measures on top of
//     its result. A writer outside the process (an operator's git, a push
//     to the checked-out branch, which git refuses by default) between
//     that check and `git merge` is the residual KNOWN-LIMITATIONS names.
//
// With no budget on the client none of this runs (no process starts).

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// MinBudgetGitVersion is the oldest git the checkout budget's merge
// measure supports: `git merge-tree --write-tree` arrived in git 2.38. A
// budgeted merge on an older git is refused (fail closed); `spine serve`,
// which always has a budget, refuses to start on one.
const MinBudgetGitVersion = "2.38"

const minBudgetGitMajor, minBudgetGitMinor = 2, 38

// CheckBudgetGitVersion returns an error when git is older than
// MinBudgetGitVersion (or missing). Hosts that turn the checkout budget on
// call it at startup.
func CheckBudgetGitVersion(ctx context.Context) error {
	out, err := newGitCmd(ctx, "version").Output()
	if err != nil {
		return errors.New("git version check: cannot run git: " + err.Error())
	}
	return checkBudgetGitVersionOutput(strings.TrimSpace(string(out)))
}

// checkBudgetGitVersionOutput is CheckBudgetGitVersion's parser.
func checkBudgetGitVersionOutput(line string) error {
	m := gitVersionPattern.FindStringSubmatch(line)
	if m == nil {
		return errors.New("git version check: unrecognised output " + line)
	}
	major, minor := atoiOr(m[1]), atoiOr(m[2])
	if major < minBudgetGitMajor || (major == minBudgetGitMajor && minor < minBudgetGitMinor) {
		return errors.New("git version check: " + line +
			" is older than " + MinBudgetGitVersion + ", which the checkout budget needs to measure a merge's result (git merge-tree --write-tree)")
	}
	return nil
}

func atoiOr(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// errMergeTargetMoved is the refusal when the target moved between the
// result's measure and the merge.
func errMergeTargetMoved() *GitError {
	return &GitError{Kind: ErrKindTransient, Op: "merge",
		Message: "the merge target moved after the checkout budget measured the merge's result; retry"}
}

// checkMergeTarget is the pre-switch half of a merge's budget: it
// returns the target tip the merge must run on — target's object id, or
// HEAD's commit when target is "" (merge into the checked-out branch) —
// and measures the target's tree when the switch to it is a checkout. ""
// when the client has no budget (then nothing ran).
func (c *CLIClient) checkMergeTarget(ctx context.Context, targetValue, target string) (string, error) {
	const op = "merge"
	l := c.CheckoutLimits()
	if l.Budget == nil {
		return "", nil
	}
	if target == "" {
		return c.headCommit(ctx, op)
	}
	ours, err := c.resolveBranchOID(ctx, op, "target", targetValue, target)
	if err != nil {
		return "", err
	}
	onTarget, err := c.headIsBranch(ctx, op, headsPrefix+target)
	if err != nil {
		return "", err
	}
	if !onTarget {
		// The switch is a checkout of the target's whole tree.
		if err := l.Budget.checkRevs(ctx, c, op, ours); err != nil {
			return "", err
		}
	}
	return ours, nil
}

// checkMergeResult is the post-switch half: the result of merging source
// into ours, computed in the switched working tree, measured unless it is
// ours' own tree. ours "" (no budget): nothing runs.
func (c *CLIClient) checkMergeResult(ctx context.Context, ours, source string) error {
	if ours == "" {
		return nil
	}
	l := c.CheckoutLimits()
	if l.Budget == nil {
		return nil
	}
	result, oursTree, err := c.mergeResultTree(ctx, ours, source)
	if err != nil {
		return err
	}
	if c.mergeResultHook != nil {
		c.mergeResultHook(result)
	}
	if result == oursTree {
		return nil // nothing to write
	}
	return l.Budget.checkRevs(ctx, c, "merge", result)
}

// mergeAllocLimit is the GIT_ALLOC_LIMIT of a budgeted merge: the larger
// of the byte budget and the read allocation limit.
func (c *CLIClient) mergeAllocLimit() int64 {
	l := c.CheckoutLimits()
	if l.Budget == nil {
		return 0
	}
	return max(l.Budget.Effective().MaxTotalBytes, c.gitAllocLimit())
}

// mergeAllocRefusal maps git's refusal of an allocation over the merge's
// GIT_ALLOC_LIMIT to the budget refusal (nil when errText is not one).
func mergeAllocRefusal(op, errText string, limit int64) error {
	if limit <= 0 || !isGitAllocRefusal(errText, limit) {
		return nil
	}
	seen := limit + 1
	if m := gitAllocRequest.FindStringSubmatch(errText); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			seen = n
		}
	}
	return &GitError{Kind: ErrKindPermanent, Op: op, Message: MergeAllocRefusedMessage,
		Err: &TreeTooLargeError{Limit: TreeLimitTotalBytes, Max: limit, Seen: seen}}
}

// gitAllocRequest captures the size git asked for in its alloc refusal.
var gitAllocRequest = regexp.MustCompile(`(?m)^(?:fatal|error): attempting to allocate ([0-9]+) over limit [0-9]+\r?$`)

// MergeAllocRefusedMessage is the GitError.Message when a budgeted merge
// needed one allocation larger than its GIT_ALLOC_LIMIT (matches
// ErrTreeTooLarge).
const MergeAllocRefusedMessage = "the merge needs an allocation larger than the checkout budget admits"

// sameMergeBase is the post-switch check of a budgeted merge: HEAD must
// still be the tip the result was measured on. ours "" (no budget):
// nothing runs.
func (c *CLIClient) sameMergeBase(ctx context.Context, ours string) error {
	if ours == "" {
		return nil
	}
	head, err := c.headCommit(ctx, "merge")
	if err != nil {
		return err
	}
	if head != ours {
		return errMergeTargetMoved()
	}
	return nil
}

// headCommit is HEAD's commit id.
func (c *CLIClient) headCommit(ctx context.Context, op string) (string, error) {
	out, _, err := c.runWith(ctx, op, runOpts{}, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(out)
	if !IsFullObjectName(head) {
		return "", &GitError{Kind: ErrKindPermanent, Op: op, Message: "HEAD names no commit"}
	}
	return head, nil
}

// mergeResultTree computes the tree `git merge` of theirs into ours
// leaves, and ours' own tree. A conflicted merge still has a result tree
// (git merge-tree exits 1 and prints it first): the conflict markers are
// what a conflicted `git merge` writes.
func (c *CLIClient) mergeResultTree(ctx context.Context, ours, theirs string) (result, oursTree string, err error) {
	const op = "merge"
	limit := c.mergeAllocLimit()
	out, errText, err := c.runWith(ctx, op, runOpts{env: []string{"GIT_ALLOC_LIMIT=" + strconv.FormatInt(limit, 10)}},
		"merge-tree", "--write-tree", "--no-messages", "--name-only", "--end-of-options", ours, theirs)
	if refused := mergeAllocRefusal(op, errText, limit); refused != nil {
		return "", "", refused
	}
	first, _, _ := strings.Cut(out, "\n")
	first = strings.TrimSpace(first)
	if err != nil {
		var exitErr *exec.ExitError
		conflicted := errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && IsFullObjectName(first)
		if !conflicted {
			if strings.Contains(errText, "write-tree") && (strings.Contains(errText, "unknown option") || strings.Contains(errText, "usage:")) {
				return "", "", &GitError{Kind: ErrKindPermanent, Op: op,
					Message: "git " + MinBudgetGitVersion + " or newer is required to measure a merge's result against the checkout budget", Err: err}
			}
			return "", "", err
		}
	}
	if !IsFullObjectName(first) {
		return "", "", &GitError{Kind: ErrKindPermanent, Op: op, Message: "git merge-tree printed no result tree"}
	}
	treeOut, _, err := c.runWith(ctx, op, runOpts{}, "rev-parse", "--verify", "--end-of-options", ours+"^{tree}")
	if err != nil {
		return "", "", err
	}
	return first, strings.TrimSpace(treeOut), nil
}
