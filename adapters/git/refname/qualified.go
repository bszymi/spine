package refname

import (
	"fmt"
	"strings"

	"github.com/bszymi/spine/core/domain"
)

// ValidateQualified enforces git's refname grammar on a fully-qualified
// ref ("refs/heads/main", "refs/tags/v1") the way the pre-receive gate
// needs it: the name must start with "refs/" and the whole name must
// pass `git check-ref-format --allow-onelevel` (check_refname_format
// with REFNAME_ALLOW_ONELEVEL). It exists so the gate refuses, before
// any policy decision, every ref that git could read differently from
// the gate — above all a NUL, which git treats as the end of the ref
// (INIT-023 TASK-001, audit F1).
//
// Where this differs from `git receive-pack`, it is only ever looser,
// and only on refs receive-pack refuses itself ("funny refname"), so a
// ref accepted here is never one git would apply under another name:
//   - receive-pack checks the remainder after "refs/", not the whole
//     name, so it also refuses "refs/@" (remainder "@");
//   - receive-pack allows a one-level remainder ("refs/foo") only for a
//     delete, and refuses to create or update one.
//
// The check is byte-wise, so invalid UTF-8 cannot hide a forbidden
// byte. Bytes >= 0x80 are accepted, as git accepts them; so is a
// leading "-" inside a component ("refs/heads/-x"), which git accepts
// and which the mandatory "refs/" prefix keeps from ever reading as a
// command-line flag.
//
// Rejected (each one is a ref git would refuse as a "funny refname"):
//   - missing "refs/" prefix
//   - any ASCII control byte (0x00-0x1f, 0x7f), space, or one of ~ ^ : ? * [ \
//   - ".." or "@{" anywhere
//   - an empty component ("//", trailing "/", bare "refs/")
//   - a component that starts with "." or ends with ".lock"
//   - a trailing "."
//
// Returns a *domain.SpineError with ErrInvalidParams, like Validate.
// Messages quote the ref with %q so a control byte never reaches a log
// line or a git client's side-band output raw.
//
// Validate and the adapters/git ValidateRef helper are deliberately
// untouched here; converging them onto this grammar is INIT-023
// TASK-002 ("one ref grammar"). SMP carries a temporary unexported copy
// of this function (INIT-027 EPIC-004 TASK-001) whose rules and test
// corpus must stay identical to this one until SMP calls it directly.
func ValidateQualified(ref string) error {
	if !strings.HasPrefix(ref, "refs/") {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("ref %q must start with \"refs/\"", ref))
	}
	for i := 0; i < len(ref); i++ {
		b := ref[i]
		if b < 0x20 || b == 0x7f {
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("ref %q contains control byte 0x%02x", ref, b))
		}
		switch b {
		case ' ', '~', '^', ':', '?', '*', '[', '\\':
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("ref %q contains forbidden character %q", ref, b))
		}
	}
	if strings.Contains(ref, "..") {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("ref %q must not contain '..'", ref))
	}
	if strings.Contains(ref, "@{") {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("ref %q must not contain '@{'", ref))
	}
	if strings.HasSuffix(ref, ".") {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("ref %q must not end with '.'", ref))
	}
	for _, c := range strings.Split(ref[len("refs/"):], "/") {
		switch {
		case c == "":
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("ref %q has an empty path component", ref))
		case strings.HasPrefix(c, "."):
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("ref %q has a component starting with '.'", ref))
		case strings.HasSuffix(c, ".lock"):
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("ref %q has a component ending with '.lock'", ref))
		}
	}
	return nil
}
