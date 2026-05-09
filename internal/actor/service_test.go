package actor_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/domain"
)

// TestRegister_ValidActorIDMatrix exercises the validActorID regex
// (service.go:14 — `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`) end-to-end via
// Service.Register. The regex is the only client-controlled string ID
// boundary in the actor surface — a regression that loosens it (e.g.,
// allowing leading punctuation, whitespace, or oversize IDs) would let
// malformed IDs land in the store and propagate through every selection
// path. Existing actor_test.go covers only the empty-ID branch; this
// matrix pins the regex itself.
func TestRegister_ValidActorIDMatrix(t *testing.T) {
	maxLen := strings.Repeat("a", 128) // 1 leading char + 127 tail
	overLen := strings.Repeat("a", 129)

	cases := []struct {
		name    string
		actorID string
		ok      bool
	}{
		// — accepted —
		{"alnum-lower", "alice", true},
		{"alnum-mixed", "Alice42", true},
		{"with-hyphen", "alice-bob", true},
		{"with-underscore", "alice_bob", true},
		{"with-dot", "alice.bob", true},
		{"single-char", "a", true},
		{"single-digit", "7", true},
		{"max-length-128", maxLen, true},
		{"all-punct-tail", "a-_.", true},

		// — rejected: leading non-alnum (regex anchors `[a-zA-Z0-9]` at start) —
		{"leading-hyphen", "-alice", false},
		{"leading-underscore", "_alice", false},
		{"leading-dot", ".alice", false},
		{"leading-space", " alice", false},

		// — rejected: trailing whitespace/newline (regex disallows space) —
		{"trailing-space", "alice ", false},
		{"trailing-newline", "alice\n", false},

		// — rejected: disallowed characters in body —
		{"contains-space", "alice bob", false},
		{"contains-slash", "alice/bob", false},
		{"contains-colon", "alice:bob", false},
		{"contains-at", "alice@bob", false},
		{"contains-percent", "alice%20", false},
		{"contains-quote", "alice'bob", false},
		{"contains-unicode", "aliceβ", false},

		// — rejected: length bounds —
		{"empty", "", false}, // hits the empty-string branch before regex
		{"over-length-129", overLen, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeStore()
			svc := actor.NewService(fs)
			err := svc.Register(context.Background(), &domain.Actor{
				ActorID: tc.actorID,
				Type:    domain.ActorTypeHuman,
				Role:    domain.RoleContributor,
			})
			if tc.ok && err != nil {
				t.Fatalf("expected accept for %q, got %v", tc.actorID, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected reject for %q, got nil error", tc.actorID)
			}
			if tc.ok {
				if _, ok := fs.actors[tc.actorID]; !ok {
					t.Errorf("expected actor %q to be persisted", tc.actorID)
				}
			} else {
				if _, ok := fs.actors[tc.actorID]; ok {
					t.Errorf("rejected actor %q must not be persisted", tc.actorID)
				}
			}
		})
	}
}

// TestRegister_RejectErrorIsInvalidParams pins the error code so a
// regression that loosens the failure mode (e.g., switches to a generic
// internal error) is caught — the gateway maps ErrInvalidParams to 400.
func TestRegister_RejectErrorIsInvalidParams(t *testing.T) {
	svc := actor.NewService(newFakeStore())
	err := svc.Register(context.Background(), &domain.Actor{
		ActorID: "-bad",
		Type:    domain.ActorTypeHuman,
		Role:    domain.RoleContributor,
	})
	if err == nil {
		t.Fatal("expected reject for leading hyphen")
	}
	derr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected *domain.SpineError, got %T: %v", err, err)
	}
	if derr.Code != domain.ErrInvalidParams {
		t.Errorf("expected ErrInvalidParams, got %s", derr.Code)
	}
}

// TestRegister_StatusForcedActive pins that Register overrides any
// caller-provided Status with Active — protects against a regression that
// would let callers pre-create suspended/deactivated actors.
func TestRegister_StatusForcedActive(t *testing.T) {
	fs := newFakeStore()
	svc := actor.NewService(fs)
	a := &domain.Actor{
		ActorID: "a1",
		Type:    domain.ActorTypeHuman,
		Role:    domain.RoleContributor,
		Status:  domain.ActorStatusSuspended, // attempted override
	}
	if err := svc.Register(context.Background(), a); err != nil {
		t.Fatalf("register: %v", err)
	}
	if a.Status != domain.ActorStatusActive {
		t.Errorf("expected Active, got %s", a.Status)
	}
	if fs.actors["a1"].Status != domain.ActorStatusActive {
		t.Errorf("persisted status should be Active, got %s", fs.actors["a1"].Status)
	}
}
