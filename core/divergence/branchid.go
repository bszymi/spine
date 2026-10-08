package divergence

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bszymi/spine/core/domain"
)

// exploratoryBranchIDPattern is the workflow ID grammar
// (core/workflow/service.go idPattern): lower-case ASCII letters and
// digits, then letters, digits, '_', '-' and '.'. It admits no '/', no
// space, no control byte and nothing git's refname grammar treats
// specially except '.', which ValidateExploratoryBranchID constrains
// further.
var exploratoryBranchIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-.]*$`)

// maxExploratoryBranchIDLen bounds a client-chosen branch ID. It keeps
// the git ref component (spine/<run>/<divergence>/<branch-id>) well
// under the filesystem's 255-byte name limit and the 1024-byte bound
// the push admission puts on reserved branch names (core/refadmit,
// INIT-023 TASK-016 DC-4).
const maxExploratoryBranchIDLen = 128

// ValidateExploratoryBranchID checks a client-supplied exploratory
// branch ID before it is embedded in a git ref name and a store key
// (INIT-023 TASK-016 owner question 6). It returns ErrInvalidParams
// (HTTP 400) unless id matches the workflow ID grammar, is at most 128
// bytes, is a valid git ref component (no "..", no trailing "." and no
// ".lock" suffix), and is not "refs".
//
// "refs" would make the branch's ref spine/<run>/<divergence>/refs, a
// name with a non-leading "refs" component, which push admission's rule
// 0b (core/refadmit) refuses for every principal, operators included:
// the engine would create a branch nobody can ever push (INIT-023 wave 2
// cross-task review). "HEAD", the other shadowing component, cannot match
// the lower-case grammar, and the run and divergence components the
// engine builds ("<run-id>-div-…") never equal either name.
//
// CreateExploratoryBranch itself does not call it, so embedders that
// call that method keep their current behaviour; the standalone
// gateway validates the request body with it. Without the check a
// control byte reached branchprotect.Policy.Evaluate and surfaced as an
// HTTP 500 evaluation failure (INIT-023 TASK-001 cross-task review),
// and a name git refuses failed as a 500 git error.
func ValidateExploratoryBranchID(id string) error {
	switch {
	case len(id) > maxExploratoryBranchIDLen:
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("branch_id is longer than %d bytes", maxExploratoryBranchIDLen))
	case !exploratoryBranchIDPattern.MatchString(id):
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("invalid branch_id %q (must match %s)", id, exploratoryBranchIDPattern.String()))
	case strings.Contains(id, ".."), strings.HasSuffix(id, "."), strings.HasSuffix(id, ".lock"):
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("invalid branch_id %q (not a valid git ref component)", id))
	case id == "refs":
		return domain.NewError(domain.ErrInvalidParams,
			`invalid branch_id "refs" (a "refs" component after the first makes a ref name push admission refuses)`)
	}
	return nil
}
