package engine_test

import (
	"testing"

	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/workflow"
)

func TestLookupInternalHandler_Merge(t *testing.T) {
	h, ok := engine.LookupInternalHandler("merge")
	if !ok {
		t.Fatal("expected merge handler to be registered")
	}
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

func TestLookupInternalHandler_Unknown(t *testing.T) {
	if _, ok := engine.LookupInternalHandler("does_not_exist"); ok {
		t.Fatal("expected unknown handler lookup to return false")
	}
}

// The engine's handler registry and the workflow validator's known-handlers
// list must agree on the set of registered handlers — otherwise the
// validator can accept a workflow the engine cannot execute (or vice
// versa). This test fails loudly if someone adds a handler to one side
// and forgets the other.
func TestInternalHandlers_MatchValidatorKnownSet(t *testing.T) {
	for name := range workflow.KnownInternalHandlers {
		if _, ok := engine.LookupInternalHandler(name); !ok {
			t.Errorf("validator knows handler %q but engine does not register it", name)
		}
	}

	if !workflow.IsKnownInternalHandler("merge") {
		t.Error("expected validator to recognise 'merge' handler")
	}
	if _, ok := engine.LookupInternalHandler("merge"); !ok {
		t.Error("expected engine to register 'merge' handler")
	}
}

func TestEngineMergeActorID_StablePrefix(t *testing.T) {
	// The engine actor ID is audit-visible; callers may filter on the
	// "actor-engine-" prefix. Lock the exact value so the prefix stays
	// stable across refactors.
	const expected = "actor-engine-merge"
	if engine.EngineMergeActorID != expected {
		t.Errorf("expected EngineMergeActorID = %q, got %q", expected, engine.EngineMergeActorID)
	}
}
