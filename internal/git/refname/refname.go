// Package refname centralizes Git branch/ref-name validation used before
// handing a value to `git worktree add` or related commands.
//
// Callers in internal/artifact and internal/workflow share this helper so
// the security-sensitive rules live in one place. The checks mirror a
// subset of `git check-ref-format` plus a few extras that matter for
// shell/CLI safety, and return typed domain errors so gateway behavior
// stays consistent across services.
package refname

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/bszymi/spine/internal/domain"
)

// Validate enforces branch-name hygiene before passing the value to git.
// Rules:
//   - no empty string
//   - no leading "-" (git would parse it as a flag)
//   - no ".." (refname-component traversal, also triggers git refusal)
//   - no control characters or whitespace
//   - no ASCII characters git explicitly forbids in refnames: \ ~ ^ : ? * [
//   - cannot end in ".lock" or "/"
//   - cannot contain "@{" (reflog syntax)
//
// Returns a *domain.SpineError with ErrInvalidParams on violation so the
// gateway surfaces an invalid-params response rather than internal_error.
func Validate(name string) error {
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
