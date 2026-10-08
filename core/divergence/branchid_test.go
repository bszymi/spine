package divergence_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/divergence"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/refadmit"
)

// TestValidateExploratoryBranchID pins the grammar a client-chosen
// exploratory branch ID must match before it becomes a git ref
// component (INIT-023 TASK-016 owner question 6): the workflow ID
// grammar, at most 128 bytes, and a valid ref component.
func TestValidateExploratoryBranchID(t *testing.T) {
	for _, ok := range []string{"b1", "variant-1", "a.b", "a_b", "0", "x" + strings.Repeat("a", 127)} {
		if err := divergence.ValidateExploratoryBranchID(ok); err != nil {
			t.Errorf("%q: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "a\x00", "a\x01", "a\nb", "a\x1f", "a\x7f", "A", "Variant", "a/b", "a b", "-a", "_a", ".a",
		"a..b", "a.", "a.lock", "a~1", "a^", "a:b", "a?", "a*", "a[", `a\b`, "a@{1}", "é", "refs", "HEAD",
		strings.Repeat("a", 129),
	} {
		err := divergence.ValidateExploratoryBranchID(bad)
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrInvalidParams {
			t.Errorf("%q: got %v, want ErrInvalidParams", bad, err)
		}
		if err != nil && strings.ContainsAny(err.Error(), "\x00\x01\n\x1f\x7f") {
			t.Errorf("%q: error text carries a raw control byte: %q", bad, err.Error())
		}
	}
}

// INIT-023 wave 2 cross-task review: every branch ID the validator admits
// must give a divergence ref push admission can admit. "refs" was
// accepted, and spine/<run>/<divergence>/refs is a shadowing name that
// rule 0b refuses for every principal, so the engine created a branch
// nobody could push. Mutation: dropping the "refs" case fails both loops.
func TestValidateExploratoryBranchID_AgreesWithPushAdmission(t *testing.T) {
	const prefix = "refs/heads/spine/run-0a1b2c3d/run-0a1b2c3d-div-explore/"
	for _, id := range []string{"b1", "variant-1", "a.b", "a_b", "0", "ref", "refs-1", "refs.x", "xrefs", "head", "heads"} {
		if err := divergence.ValidateExploratoryBranchID(id); err != nil {
			t.Fatalf("%q: unexpected error %v", id, err)
		}
		c := refadmit.Classify(prefix + id)
		if c.Class != refadmit.ClassDivergenceBranch || c.Shadowing {
			t.Errorf("%q is admitted but its ref classifies as %s (shadowing=%v)", id, c.Class, c.Shadowing)
		}
	}
	for _, id := range []string{"refs", "HEAD"} {
		if !refadmit.Classify(prefix + id).Shadowing {
			t.Fatalf("precondition: %q must be a shadowing component", id)
		}
		if err := divergence.ValidateExploratoryBranchID(id); err == nil {
			t.Errorf("%q gives a ref push admission refuses for everyone; it must be refused at creation", id)
		}
	}
}
