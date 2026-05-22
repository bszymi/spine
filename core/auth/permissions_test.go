package auth_test

import (
	"errors"
	"testing"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
)

// TestAuthorize_RejectsSuspendedActor pins the defence-in-depth status
// check on permissions.go's Authorize. Suspended actors with a role
// that *would* satisfy the operation must still be denied — otherwise
// any caller that constructs an *Actor and calls Authorize directly
// (or a regression that drops the ValidateToken status check) would
// silently bypass account suspension. This is the AC's "removing the
// actor-status check causes the suspended case to fail" test.
func TestAuthorize_RejectsSuspendedActor(t *testing.T) {
	actor := &domain.Actor{
		ActorID: "actor-1",
		Role:    domain.RoleAdmin, // would pass any role gate
		Status:  domain.ActorStatusSuspended,
	}
	err := auth.Authorize(actor, "artifact.create")
	if err == nil {
		t.Fatal("expected reject for suspended actor")
	}
	derr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected *domain.SpineError, got %T: %v", err, err)
	}
	if derr.Code != domain.ErrForbidden {
		t.Errorf("expected ErrForbidden, got %s", derr.Code)
	}
}

// TestAuthorize_RejectsDeactivatedActor mirrors the suspended case for
// the terminal lifecycle state. Deactivated is one-way (actor.go
// updateStatus rejects reactivation from deactivated), so a successful
// Authorize on a deactivated actor would re-arm a tombstoned account.
func TestAuthorize_RejectsDeactivatedActor(t *testing.T) {
	actor := &domain.Actor{
		ActorID: "actor-1",
		Role:    domain.RoleAdmin,
		Status:  domain.ActorStatusDeactivated,
	}
	err := auth.Authorize(actor, "artifact.create")
	if err == nil {
		t.Fatal("expected reject for deactivated actor")
	}
	derr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected *domain.SpineError, got %T: %v", err, err)
	}
	if derr.Code != domain.ErrForbidden {
		t.Errorf("expected ErrForbidden, got %s", derr.Code)
	}
}

// TestAuthorize_RejectsZeroStatus pins that an unset Status string
// (zero value "") is treated as non-active. Defence against a caller
// that builds an Actor without populating Status — without this, the
// zero value would skip the role check and bypass authorization.
func TestAuthorize_RejectsZeroStatus(t *testing.T) {
	actor := &domain.Actor{
		ActorID: "actor-1",
		Role:    domain.RoleAdmin,
		// Status intentionally unset — zero value is "".
	}
	err := auth.Authorize(actor, "artifact.create")
	if err == nil {
		t.Fatal("expected reject for zero-value status")
	}
}

// TestAuthorize_StatusCheckPrecedesRoleCheck pins ordering: a
// suspended Reader hitting an Admin-only operation should fail with a
// status-class reason (actor_status detail), not the
// insufficient-permissions detail. A regression that swaps the
// checks would mask suspension behind a role error.
func TestAuthorize_StatusCheckPrecedesRoleCheck(t *testing.T) {
	actor := &domain.Actor{
		ActorID: "actor-1",
		Role:    domain.RoleReader, // would fail role gate too
		Status:  domain.ActorStatusSuspended,
	}
	err := auth.Authorize(actor, "system.validate") // admin-only
	if err == nil {
		t.Fatal("expected reject")
	}
	var derr *domain.SpineError
	if !errors.As(err, &derr) {
		t.Fatalf("expected *domain.SpineError, got %T: %v", err, err)
	}
	detail, ok := derr.Detail.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string detail, got %T: %v", derr.Detail, derr.Detail)
	}
	if detail["actor_status"] != string(domain.ActorStatusSuspended) {
		t.Errorf("expected status-class error detail, got %v", detail)
	}
	if _, hasRoleDetail := detail["required_role"]; hasRoleDetail {
		t.Errorf("expected status check to short-circuit before role check, got role detail %v", detail)
	}
}

// TestAuthorize_StatusCheckPrecedesUnknownOp pins that an inactive
// actor cannot probe the operation registry — a suspended actor
// hitting an unknown op should be rejected because of their status,
// not because the op is unknown. Otherwise an attacker with a
// suspended valid token could enumerate the registry.
func TestAuthorize_StatusCheckPrecedesUnknownOp(t *testing.T) {
	actor := &domain.Actor{
		ActorID: "actor-1",
		Role:    domain.RoleAdmin,
		Status:  domain.ActorStatusSuspended,
	}
	err := auth.Authorize(actor, "nonexistent.operation")
	if err == nil {
		t.Fatal("expected reject")
	}
	var derr *domain.SpineError
	if !errors.As(err, &derr) {
		t.Fatalf("expected *domain.SpineError, got %T: %v", err, err)
	}
	detail, ok := derr.Detail.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string detail (status-class), got %T: %v", derr.Detail, derr.Detail)
	}
	if detail["actor_status"] != string(domain.ActorStatusSuspended) {
		t.Errorf("expected status-class error detail to short-circuit before unknown-op branch, got %v", detail)
	}
}

// TestAuthorize_ActiveActorWrongCapability is the AC's regression bait
// for the role gate. Active status + valid op + insufficient role
// must still be denied — a regression that removed the role check
// (e.g. while reorganising for the status check) would let a Reader
// run admin operations.
func TestAuthorize_ActiveActorWrongCapability(t *testing.T) {
	actor := &domain.Actor{
		ActorID: "actor-1",
		Role:    domain.RoleReader,
		Status:  domain.ActorStatusActive,
	}
	err := auth.Authorize(actor, "artifact.create") // requires Contributor
	if err == nil {
		t.Fatal("expected reject for Reader on artifact.create")
	}
	var derr *domain.SpineError
	if !errors.As(err, &derr) {
		t.Fatalf("expected *domain.SpineError, got %T: %v", err, err)
	}
	if derr.Code != domain.ErrForbidden {
		t.Errorf("expected ErrForbidden, got %s", derr.Code)
	}
	detail, ok := derr.Detail.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string detail, got %T: %v", derr.Detail, derr.Detail)
	}
	if detail["required_role"] != string(domain.RoleContributor) {
		t.Errorf("expected required_role=contributor in detail, got %v", detail)
	}
}

// TestAuthorize_ActiveActorRightCapability pins the happy path
// alongside the rejection cases so a regression that flipped the
// rejection sense (e.g. inverted HasAtLeast) is caught here too.
func TestAuthorize_ActiveActorRightCapability(t *testing.T) {
	actor := &domain.Actor{
		ActorID: "actor-1",
		Role:    domain.RoleContributor,
		Status:  domain.ActorStatusActive,
	}
	if err := auth.Authorize(actor, "artifact.create"); err != nil {
		t.Errorf("expected accept, got %v", err)
	}
}

// TestAuthorize_RoleMatrix exercises every defined ActorRole against
// at least one capability that should and should not be granted.
// Per the AC: "each role exercised against at least one capability
// that should and should not be granted." Acts as a smoke matrix
// over operationRoles in addition to TestAuthorizeAllOperations'
// hand-picked cases.
func TestAuthorize_RoleMatrix(t *testing.T) {
	cases := []struct {
		role     domain.ActorRole
		grant    auth.Operation
		denyOp   auth.Operation
		denyRole domain.ActorRole // the role required for denyOp (for assertion clarity)
	}{
		{domain.RoleReader, "artifact.read", "artifact.create", domain.RoleContributor},
		{domain.RoleContributor, "artifact.create", "task.accept", domain.RoleReviewer},
		{domain.RoleReviewer, "task.accept", "run.cancel", domain.RoleOperator},
		{domain.RoleOperator, "run.cancel", "token.create", domain.RoleAdmin},
		{domain.RoleAdmin, "token.create", "" /* no op denies admin */, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			actor := &domain.Actor{
				ActorID: "actor-1",
				Role:    tc.role,
				Status:  domain.ActorStatusActive,
			}
			if err := auth.Authorize(actor, tc.grant); err != nil {
				t.Errorf("role=%s should be granted %s, got %v", tc.role, tc.grant, err)
			}
			if tc.denyOp == "" {
				return // admin has no deny case
			}
			if err := auth.Authorize(actor, tc.denyOp); err == nil {
				t.Errorf("role=%s should be denied %s (requires %s), got nil", tc.role, tc.denyOp, tc.denyRole)
			}
		})
	}
}
