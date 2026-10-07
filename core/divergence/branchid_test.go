package divergence_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/divergence"
	"github.com/bszymi/spine/core/domain"
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
		"a..b", "a.", "a.lock", "a~1", "a^", "a:b", "a?", "a*", "a[", `a\b`, "a@{1}", "é",
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
