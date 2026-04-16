package artifact

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/bszymi/spine/internal/domain"
)

// validateGitRefName enforces branch-name hygiene before we hand the
// value to `git worktree add`. The existing caller generates branches
// from a safe template, so today this is defense-in-depth against a
// future code path that could let a client influence the name. Rules
// follow git's refname rules plus a few extras that matter for shell /
// CLI safety:
//   - no empty string
//   - no leading "-" (git would parse it as a flag)
//   - no ".." (refname-component traversal, also triggers git refusal)
//   - no control characters or whitespace
//   - no ASCII characters git explicitly forbids in refnames: \ ~ ^ : ? * [
//   - cannot end in ".lock" or "/"
//   - cannot contain "@{" (reflog syntax)
//
// The checks deliberately mirror a subset of `git check-ref-format` so
// the helper is pure and testable without invoking git.
func validateGitRefName(name string) error {
	if name == "" {
		return domain.NewError(domain.ErrInvalidParams, "branch name is empty")
	}
	if strings.HasPrefix(name, "-") {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("branch name %q must not start with '-'", name))
	}
	if strings.Contains(name, "..") {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("branch name %q must not contain '..'", name))
	}
	if strings.Contains(name, "@{") {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("branch name %q must not contain '@{'", name))
	}
	if strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, "/") {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("branch name %q has a forbidden suffix", name))
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("branch name %q contains a control or whitespace character", name))
		}
		switch r {
		case '\\', '~', '^', ':', '?', '*', '[':
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("branch name %q contains a forbidden character %q", name, r))
		}
	}
	return nil
}
