package gateway

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleActorSkillAssign(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "skill.update") {
		return
	}

	st, ok := s.needStore(w, r)
	if !ok {
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

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}

	actorID := chi.URLParam(r, "actor_id")
	skillID := chi.URLParam(r, "skill_id")

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

	st, ok := s.needStore(w, r)
	if !ok {
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
