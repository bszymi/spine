package store_test

import (
	"testing"

	"github.com/bszymi/spine/adapters/store"
)

// ADR-017 §5.
func TestLinkTargetForms(t *testing.T) {
	for _, in := range []string{"assumptions/ASM-001-x.md", "/assumptions/ASM-001-x.md"} {
		bare, canonical := store.LinkTargetForms(in)
		if bare != "assumptions/ASM-001-x.md" || canonical != "/assumptions/ASM-001-x.md" {
			t.Errorf("LinkTargetForms(%q) = %q, %q", in, bare, canonical)
		}
	}
	// Exactly one leading slash is the canonical form; nothing else is rewritten.
	if bare, canonical := store.LinkTargetForms("//a.md"); bare != "/a.md" || canonical != "//a.md" {
		t.Errorf("LinkTargetForms(//a.md) = %q, %q", bare, canonical)
	}
}
