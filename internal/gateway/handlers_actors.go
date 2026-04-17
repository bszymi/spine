package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/domain"
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

	WriteJSON(w, http.StatusCreated, a)
}
