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
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/bszymi/spine/core/domain"
)

// Validate enforces branch-name hygiene before passing the value to git.
// name is a short branch name ("main", "spine/run/x"), never a
// qualified ref. Rules:
//   - no empty string
//   - no leading "-" (git would parse it as a flag)
//   - no ".." (refname-component traversal, also triggers git refusal)
//   - no control characters or whitespace
//   - no ASCII characters git explicitly forbids in refnames: \ ~ ^ : ? * [
//   - cannot end in ".lock" or "/"
//   - cannot contain "@{" (reflog syntax)
//   - not "HEAD" or "@", which git reads as the current commit
//   - does not start with "refs/": a short name never names a
//     qualified ref, so "refs/heads/x" here would become the
//     doubly-qualified refs/heads/refs/heads/x
//   - "refs/heads/" + name passes ValidateQualified, git's own refname
//     grammar, which adds: no component starting with "." or ending in
//     ".lock", no empty component ("//", a leading "/"), no trailing "."
//
// The first rules (and the Unicode control/space refusal, which is
// stricter than git) are the pre-INIT-023 set and keep their messages;
// the last three converge the branch grammar on the one refname grammar
// (INIT-023 TASK-002 §3). The result is never looser than git: every
// name accepted here is one `git check-ref-format --branch` accepts.
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
	if name == "HEAD" || name == "@" {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("branch name %q is reserved: git reads it as the current commit", name))
	}
	if strings.HasPrefix(name, "refs/") {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("branch name %q must be a short name, not a ref under refs/", name))
	}
	if err := ValidateQualified("refs/heads/" + name); err != nil {
		var se *domain.SpineError
		if errors.As(err, &se) {
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("branch name %q is not a valid git branch name: %s", name, se.Message))
		}
		return err
	}
	return nil
}
