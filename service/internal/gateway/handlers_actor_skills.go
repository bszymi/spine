package gateway

import (
	"net/http"

	"github.com/bszymi/spine/core/observe"
	"github.com/go-chi/chi/v5"
)

// handleActorSkillAssign grants a skill to an actor. Admin-only
// (actor.add_skill), and a non-admin can never target itself
// (INIT-023 TASK-013, audit F6): skills decide who may claim review and
// approval steps, so a self-grant would let an actor approve its own
// work.
func (s *Server) handleActorSkillAssign(w http.ResponseWriter, r *http.Request) {
	actorID, err := actorIDParam(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	skillID := chi.URLParam(r, "skill_id")

	if !s.authorizeActorSkillChange(w, r, "actor.add_skill", actorID) {
		return
	}

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}

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
	observe.AuditLog(r.Context(), "actor_skill_added",
		"target_actor_id", actorID, "skill_id", skillID, "skill_name", skill.Name)

	WriteJSON(w, http.StatusOK, skill)
}

// handleActorSkillRemove removes a skill from an actor. Admin-only
// (actor.remove_skill), with the same self-change refusal: stripping a
// reviewer's skill is the other half of the F6 lever.
func (s *Server) handleActorSkillRemove(w http.ResponseWriter, r *http.Request) {
	actorID, err := actorIDParam(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	skillID := chi.URLParam(r, "skill_id")

	if !s.authorizeActorSkillChange(w, r, "actor.remove_skill", actorID) {
		return
	}

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}

	if err := st.RemoveSkillFromActor(r.Context(), actorID, skillID); err != nil {
		WriteError(w, err)
		return
	}
	observe.AuditLog(r.Context(), "actor_skill_removed",
		"target_actor_id", actorID, "skill_id", skillID)

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
