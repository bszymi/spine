package auth_test

// Exhaustive tables for the INIT-023 TASK-013 identity-authority
// helpers (audit F6 and P3-33). Both helpers are exported from
// core/auth, so they are public authorization primitives: each must
// fail closed on its own, whatever its caller checked first.

import (
	"errors"
	"testing"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
)

var allRoles = []domain.ActorRole{
	domain.RoleReader,
	domain.RoleContributor,
	domain.RoleReviewer,
	domain.RoleOperator,
	domain.RoleAdmin,
}

// forbiddenDetail asserts err is an ErrForbidden SpineError and returns
// its map[string]string detail (nil when the error carries none).
func forbiddenDetail(t *testing.T, err error) map[string]string {
	t.Helper()
	var se *domain.SpineError
	if !errors.As(err, &se) {
		t.Fatalf("error %v is not a SpineError", err)
	}
	if se.Code != domain.ErrForbidden {
		t.Fatalf("error code %q, want %q", se.Code, domain.ErrForbidden)
	}
	d, _ := se.Detail.(map[string]string)
	return d
}

func TestAuthorizeActorSkillChange_Matrix(t *testing.T) {
	for _, op := range []auth.Operation{"actor.add_skill", "actor.remove_skill"} {
		for _, role := range allRoles {
			caller := &domain.Actor{ActorID: "caller-1", Role: role, Status: domain.ActorStatusActive}
			for _, target := range []string{"caller-1", "someone-else"} {
				self := target == caller.ActorID
				name := string(op) + "/" + string(role) + "/self=" + map[bool]string{true: "yes", false: "no"}[self]
				t.Run(name, func(t *testing.T) {
					err := auth.AuthorizeActorSkillChange(caller, op, target)
					if role == domain.RoleAdmin {
						// Admins sit at the top of the hierarchy and may
						// change any actor's skills, their own included
						// (Decision 3; step-level SoD is TASK-007's).
						if err != nil {
							t.Fatalf("admin refused: %v", err)
						}
						return
					}
					if err == nil {
						t.Fatalf("role %s allowed to change skills (self=%v)", role, self)
					}
					d := forbiddenDetail(t, err)
					if self {
						if d["reason"] != auth.ReasonSelfSkillChange {
							t.Fatalf("self change by %s: reason %q, want %q (detail %v)", role, d["reason"], auth.ReasonSelfSkillChange, d)
						}
						if d["operation"] != string(op) || d["actor_role"] != string(role) {
							t.Fatalf("self change detail %v lacks operation/actor_role", d)
						}
						return
					}
					// Another actor: a plain insufficient-role refusal
					// from the role map, with no self reason.
					if d["reason"] != "" {
						t.Fatalf("grant to another actor carries reason %q; want a plain insufficient-role refusal", d["reason"])
					}
					if d["required_role"] != string(domain.RoleAdmin) {
						t.Fatalf("grant to another actor: required_role %q, want admin (detail %v)", d["required_role"], d)
					}
				})
			}
		}
	}
}

// A non-active caller gets Authorize's status-class refusal, never the
// self reason: the status check precedes the role check everywhere
// (TestAuthorize_StatusCheckPrecedesRoleCheck), and this helper keeps
// that ordering.
func TestAuthorizeActorSkillChange_InactiveCaller(t *testing.T) {
	for _, status := range []domain.ActorStatus{domain.ActorStatusSuspended, domain.ActorStatusDeactivated, ""} {
		for _, role := range []domain.ActorRole{domain.RoleContributor, domain.RoleAdmin} {
			t.Run(string(role)+"/"+string(status), func(t *testing.T) {
				caller := &domain.Actor{ActorID: "caller-1", Role: role, Status: status}
				err := auth.AuthorizeActorSkillChange(caller, "actor.add_skill", "caller-1")
				if err == nil {
					t.Fatalf("%s %s caller allowed", status, role)
				}
				d := forbiddenDetail(t, err)
				if d["reason"] != "" {
					t.Fatalf("inactive caller got reason %q; want the status-class detail", d["reason"])
				}
				if v, ok := d["actor_status"]; !ok || v != string(status) {
					t.Fatalf("detail %v lacks actor_status=%q", d, status)
				}
			})
		}
	}
}

// noPanic runs f and turns a panic into a test failure, so a nil
// caller is pinned as "refused", not "crashes the request goroutine".
func noPanic(t *testing.T, f func() error) (err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked on a nil caller: %v", r)
		}
	}()
	return f()
}

func TestAuthorizeActorSkillChange_NilCallerFailsClosed(t *testing.T) {
	err := noPanic(t, func() error { return auth.AuthorizeActorSkillChange(nil, "actor.add_skill", "x") })
	if err == nil {
		t.Fatal("nil caller allowed")
	}
	forbiddenDetail(t, err)
}

func TestAuthorizeRoleGrant_Matrix(t *testing.T) {
	for _, callerRole := range allRoles {
		for _, granted := range allRoles {
			t.Run(string(callerRole)+"->"+string(granted), func(t *testing.T) {
				caller := &domain.Actor{ActorID: "caller-1", Role: callerRole, Status: domain.ActorStatusActive}
				err := auth.AuthorizeRoleGrant(caller, granted)
				// Admin may grant any role; anyone else only roles
				// strictly below its own (Decision 4: an operator
				// cannot mint a peer operator).
				allowed := callerRole == domain.RoleAdmin || granted.RoleLevel() < callerRole.RoleLevel()
				if allowed {
					if err != nil {
						t.Fatalf("%s granting %s refused: %v", callerRole, granted, err)
					}
					return
				}
				if err == nil {
					t.Fatalf("%s allowed to grant %s", callerRole, granted)
				}
				d := forbiddenDetail(t, err)
				if d["reason"] != auth.ReasonRoleCeiling {
					t.Fatalf("reason %q, want %q (detail %v)", d["reason"], auth.ReasonRoleCeiling, d)
				}
				if d["granted_role"] != string(granted) || d["actor_role"] != string(callerRole) {
					t.Fatalf("detail %v lacks granted_role/actor_role", d)
				}
			})
		}
	}
}

// An unknown or empty granted role has RoleLevel() == 0, which an
// unguarded "granted >= caller" comparison treats as below every role.
// The helper refuses it for every caller, admin included, rather than
// relying on its caller to have validated the role first.
func TestAuthorizeRoleGrant_UnknownRoleRefusedForEveryCaller(t *testing.T) {
	for _, callerRole := range allRoles {
		for _, granted := range []domain.ActorRole{"", "superuser", "Admin"} {
			t.Run(string(callerRole)+"->"+string(granted), func(t *testing.T) {
				caller := &domain.Actor{ActorID: "caller-1", Role: callerRole, Status: domain.ActorStatusActive}
				err := auth.AuthorizeRoleGrant(caller, granted)
				if err == nil {
					t.Fatalf("%s allowed to grant unknown role %q", callerRole, granted)
				}
				if d := forbiddenDetail(t, err); d["reason"] != auth.ReasonRoleCeiling {
					t.Fatalf("reason %q, want %q", d["reason"], auth.ReasonRoleCeiling)
				}
			})
		}
	}
}

// A non-active caller is refused with the same status-class detail as
// Authorize, checked first — a suspended admin must not keep minting
// actors through this primitive.
func TestAuthorizeRoleGrant_InactiveCaller(t *testing.T) {
	cases := []struct {
		role   domain.ActorRole
		status domain.ActorStatus
	}{
		{domain.RoleAdmin, domain.ActorStatusSuspended},
		{domain.RoleOperator, domain.ActorStatusDeactivated},
		{domain.RoleAdmin, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.role)+"/"+string(tc.status), func(t *testing.T) {
			caller := &domain.Actor{ActorID: "caller-1", Role: tc.role, Status: tc.status}
			err := auth.AuthorizeRoleGrant(caller, domain.RoleReader)
			if err == nil {
				t.Fatalf("%s %s caller allowed", tc.status, tc.role)
			}
			d := forbiddenDetail(t, err)
			if v, ok := d["actor_status"]; !ok || v != string(tc.status) {
				t.Fatalf("detail %v lacks actor_status=%q", d, tc.status)
			}
			if d["reason"] != "" {
				t.Fatalf("inactive caller got reason %q; want the status-class detail", d["reason"])
			}
		})
	}
}

func TestAuthorizeRoleGrant_NilCallerFailsClosed(t *testing.T) {
	err := noPanic(t, func() error { return auth.AuthorizeRoleGrant(nil, domain.RoleReader) })
	if err == nil {
		t.Fatal("nil caller allowed")
	}
	forbiddenDetail(t, err)
}

// The skill catalogue and actor-skill grants are admin-only (F6,
// Design §1). Pinned per operation so a revert of any single entry to
// contributor turns this red.
func TestSkillAndGrantOperationsAreAdminOnly(t *testing.T) {
	for _, op := range []auth.Operation{"skill.create", "skill.update", "skill.deprecate", "actor.add_skill", "actor.remove_skill"} {
		role, ok := auth.RequiredRole(op)
		if !ok {
			t.Errorf("%s: unknown operation", op)
			continue
		}
		if role != domain.RoleAdmin {
			t.Errorf("%s requires %s, want admin", op, role)
		}
	}
	if role, _ := auth.RequiredRole("skill.read"); role != domain.RoleReader {
		t.Errorf("skill.read requires %s, want reader (unchanged)", role)
	}
	if role, _ := auth.RequiredRole("actor.create"); role != domain.RoleOperator {
		t.Errorf("actor.create requires %s, want operator (owner question 1 default)", role)
	}
}
