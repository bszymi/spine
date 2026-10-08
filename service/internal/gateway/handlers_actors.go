package gateway

import (
	"net/http"

	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

type createActorRequest struct {
	ActorID string `json:"actor_id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Role    string `json:"role"`
}

func (s *Server) handleActorCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "actor.create") {
		return
	}

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}

	req, ok := decodeBody[createActorRequest](w, r)
	if !ok {
		return
	}

	if req.Type == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "type required"))
		return
	}
	actorType := domain.ActorType(req.Type)
	switch actorType {
	case domain.ActorTypeHuman, domain.ActorTypeAIAgent, domain.ActorTypeAutomated:
		// valid
	default:
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "type must be one of: human, ai_agent, automated_system"))
		return
	}

	actorRole := domain.RoleContributor
	if req.Role != "" {
		actorRole = domain.ActorRole(req.Role)
		if actorRole.RoleLevel() == 0 {
			WriteError(w, domain.NewError(domain.ErrInvalidParams, "role must be one of: reader, contributor, reviewer, operator, admin"))
			return
		}
	}

	// Role ceiling (INIT-023 TASK-013, audit P3-33). actor.create is an
	// operator operation, but a non-admin may only register roles
	// strictly below its own — an operator registering admin actors
	// would stage an escalation for the day any non-admin token path
	// opens. A nil caller is dev mode, which authorize() already let
	// through.
	if caller := actorFromContext(r.Context()); caller != nil {
		if err := auth.AuthorizeRoleGrant(caller, actorRole); err != nil {
			observe.AuditLog(r.Context(), "actor_create_refused",
				"granted_role", string(actorRole), "reason", errorReason(err))
			WriteError(w, err)
			return
		}
	}

	actorID := req.ActorID
	if actorID == "" {
		id, err := generateID("actor")
		if err != nil {
			WriteError(w, domain.NewError(domain.ErrInternal, "failed to generate actor_id"))
			return
		}
		actorID = id
	}

	a := &domain.Actor{
		ActorID: actorID,
		Type:    actorType,
		Name:    req.Name,
		Role:    actorRole,
	}

	svc := actor.NewService(st)
	if err := svc.Register(r.Context(), a); err != nil {
		WriteError(w, err)
		return
	}
	observe.AuditLog(r.Context(), "actor_created",
		"created_actor_id", a.ActorID, "granted_role", string(a.Role))

	WriteJSON(w, http.StatusCreated, a)
}
