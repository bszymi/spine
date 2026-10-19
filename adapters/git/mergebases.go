package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// MergeBases returns every best common ancestor of a and b (`git
// merge-base --all`), each a full lowercase hex object id, in git's order
// and without duplicates. It is the optional capability the governance-
// path guard needs (core/engine MergeBaseLister; ADR-016, INIT-023
// TASK-006).
//
// Why not MergeBase: when the two histories cross (a run branch merged
// another run's branch and that branch later landed on main), there are
// several merge bases, and `git merge-base` prints one of them. A diff
// from that one base can miss a path that git's recursive merge — which
// merges against a virtual base built from all of them — changes on the
// target. The guard therefore diffs from every base.
//
// Same argument handling as MergeBase: both revisions are validated before
// any git process and follow --end-of-options, so a poisoned revision
// cannot become a flag, and a short branch name resolves exactly to
// refs/heads/<name> (TASK-002's lookup, never a tag or other shadow).
// Histories with no common ancestor make git exit 1 with no output and no
// diagnostic; that is an empty list and a nil error (INIT-023 TASK-006
// review round 1): the guard refuses such a merge permanently, whereas an
// error means "try again" and would leave the run committing for good.
// Any other failure is returned as the command's error. A line that is
// not a full object id is an error, never skipped: the caller fails
// closed on it.
func (c *CLIClient) MergeBases(ctx context.Context, a, b string) ([]string, error) {
	if err := checkRev("merge-base", "a", a); err != nil {
		return nil, err
	}
	if err := checkRev("merge-base", "b", b); err != nil {
		return nil, err
	}
	revA, err := c.resolveRev(ctx, "merge-base", "a", a)
	if err != nil {
		return nil, err
	}
	revB, err := c.resolveRev(ctx, "merge-base", "b", b)
	if err != nil {
		return nil, err
	}
	output, err := c.run(ctx, "merge-base", "merge-base", "--all", "--end-of-options", revA, revB)
	if err != nil {
		if noMergeBase(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var out []string
	seen := map[string]struct{}{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !IsCanonicalObjectName(line) {
			return nil, &GitError{Kind: ErrKindPermanent, Op: "merge-base",
				Message: fmt.Sprintf("unexpected `git merge-base --all` output line %q", truncateOutput(line))}
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	return out, nil
}

// noMergeBase reports git merge-base's "no common ancestor" answer: exit
// status 1 with nothing on stderr (both revisions were resolved before, so
// a missing revision cannot produce it; git's fatal errors exit 128 and
// say why).
func noMergeBase(err error) bool {
	var gitErr *GitError
	var exitErr *exec.ExitError
	return errors.As(err, &gitErr) && gitErr.Message == "" &&
		errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

// truncateOutput bounds a line of git output quoted in an error.
func truncateOutput(s string) string {
	const n = 80
	if len(s) > n {
		return s[:n]
	}
	return s
}
