package workflow

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/bszymi/spine/internal/domain"
)

// validateGitRefName enforces branch-name hygiene before passing the value
// to `git worktree add`. Rules mirror the subset of `git check-ref-format`
// that matters for shell/CLI safety. See internal/artifact/refname.go for
// the full rationale — this is a duplicate of that helper; the two live in
// separate packages until extraction to internal/git/refname is warranted.
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
