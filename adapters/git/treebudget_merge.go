package git

// The checkout budget of a merge (INIT-024 TASK-003, review round 1).
//
// A merge writes neither of its sides: it leaves the RESULT tree in the
// working tree — base, plus what the target added, plus what the source
// added. Measuring each side against the budget admitted a result of up
// to twice every limit, and two merges admitted from one base walked main
// over the budget, after which every checkout, merge, worktree and
// restore of main was refused and no merge could repair it (the over-
// budget target was itself measured). So the merge's result is computed
// first — `git merge-tree --write-tree <target-tip> <source>`, merge-ort,
// the machinery `git merge` itself runs (git >= 2.38) — and that tree is
// measured on every limit (entries, bytes times the expansion of its own
// .gitattributes, path bytes), before anything is switched, merged or
// moved. merge-tree writes the result's new objects (trees, and blobs with
// conflict markers for a conflicted merge, whose markers are measured
// too) into the object store; it touches no ref, index or working tree.
//
//   - Only what is written is measured. The target is measured on its own
//     only when the switch to it is a checkout (HEAD is on another branch,
//     as Checkout would measure it); on the target already checked out,
//     only the result is. A result equal to the target's own tree (an
//     "already up to date" replay) writes nothing and is not measured. So
//     a repair merge that brings an over-budget main back under the
//     budget is admitted, and a replay on it is too.
//   - The source is not measured on its own: a merge never writes the
//     source tree as such, and when it is what the merge leaves (a fast
//     forward) it IS the result.
//   - Ordering. The result is computed inside the working-tree hold the
//     merge already takes (lockTree; WithWorkingTree for a governed
//     sequence), from the target tip read inside it, and after the switch
//     the tip is read again: a merge that would not be computed on the
//     measured tip is refused (transient) instead of merged. Every
//     in-process writer of the tree takes the same hold, so a second
//     merge waits for the first and measures on top of its result. A
//     writer outside the process (an operator's git, a push to the
//     checked-out branch, which git refuses by default) is the residual
//     KNOWN-LIMITATIONS names.
//
// With no budget on the client none of this runs (no process starts).

import (
	"context"
	"errors"
	"os/exec"
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

// checkMergeBudget is the checkout budget of a merge of source (an object
// id) into target (short; "" merges into the checked-out branch). It
// returns the target tip the result was measured on — "" when the client
// has no budget (then nothing ran) — which the caller must find again at
// the merge (sameMergeBase).
func (c *CLIClient) checkMergeBudget(ctx context.Context, targetValue, target, source string) (string, error) {
	const op = "merge"
	l := c.CheckoutLimits()
	if l.Budget == nil {
		return "", nil
	}
	var ours string
	if target != "" {
		oid, err := c.resolveBranchOID(ctx, op, "target", targetValue, target)
		if err != nil {
			return "", err
		}
		ours = oid
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
	} else {
		head, err := c.headCommit(ctx, op)
		if err != nil {
			return "", err
		}
		ours = head
	}
	result, oursTree, err := c.mergeResultTree(ctx, ours, source)
	if err != nil {
		return "", err
	}
	if result == oursTree {
		return ours, nil // nothing to write
	}
	if err := l.Budget.checkRevs(ctx, c, op, result); err != nil {
		return "", err
	}
	return ours, nil
}

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
	out, errText, err := c.runWith(ctx, op, runOpts{},
		"merge-tree", "--write-tree", "--no-messages", "--name-only", "--end-of-options", ours, theirs)
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
