package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/domain"
	"github.com/go-chi/chi/v5"
)

func (s *Server) handleActorSkillAssign(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "skill.update") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	actorID := chi.URLParam(r, "actor_id")
	skillID := chi.URLParam(r, "skill_id")

	// Verify skill exists before assigning.
	skill, err := st.GetSkill(r.Context(), skillID)
	if err != nil {
		WriteError(w, err)
		return
	}

	if err := st.AddSkillToActor(r.Context(), actorID, skillID); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, skill)
}

func (s *Server) handleActorSkillRemove(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "skill.update") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	actorID := chi.URLParam(r, "actor_id")
	skillID := chi.URLParam(r, "skill_id")

	// Verify assignment exists before removing.
	skills, err := st.ListActorSkills(r.Context(), actorID)
	if err != nil {
		WriteError(w, err)
		return
	}
	found := false
	for _, sk := range skills {
		if sk.SkillID == skillID {
			found = true
			break
		}
	}
	if !found {
		WriteError(w, domain.NewError(domain.ErrNotFound, "actor-skill assignment not found"))
		return
	}

	if err := st.RemoveSkillFromActor(r.Context(), actorID, skillID); err != nil {
		WriteError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleActorSkillList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "skill.read") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	actorID := chi.URLParam(r, "actor_id")
	skills, err := st.ListActorSkills(r.Context(), actorID)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"items": skills})
}
