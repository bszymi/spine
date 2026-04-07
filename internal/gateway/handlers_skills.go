package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
	"github.com/go-chi/chi/v5"
)

type createSkillRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
}

type updateSkillRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Category    *string `json:"category,omitempty"`
}

func (s *Server) handleSkillCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "skill.create") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	var req createSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Name == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "name required"))
		return
	}

	skill := &domain.Skill{
		Name:        req.Name,
		Description: req.Description,
		Category:    req.Category,
		Status:      domain.SkillStatusActive,
	}

	if err := st.CreateSkill(r.Context(), skill); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusCreated, skill)
}

func (s *Server) handleSkillList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "skill.read") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	category := r.URL.Query().Get("category")

	var (
		skills []domain.Skill
		err    error
	)
	if category != "" {
		skills, err = st.ListSkillsByCategory(r.Context(), category)
	} else {
		skills, err = st.ListSkills(r.Context())
	}
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"items": skills})
}

func (s *Server) handleSkillGet(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "skill.read") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	skillID := chi.URLParam(r, "skill_id")
	skill, err := st.GetSkill(r.Context(), skillID)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, skill)
}

func (s *Server) handleSkillUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "skill.update") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	skillID := chi.URLParam(r, "skill_id")
	skill, err := st.GetSkill(r.Context(), skillID)
	if err != nil {
		WriteError(w, err)
		return
	}

	var req updateSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}

	if req.Name != nil {
		skill.Name = *req.Name
	}
	if req.Description != nil {
		skill.Description = *req.Description
	}
	if req.Category != nil {
		skill.Category = *req.Category
	}

	if err := st.UpdateSkill(r.Context(), skill); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, skill)
}

func (s *Server) handleSkillDeprecate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "skill.deprecate") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	skillID := chi.URLParam(r, "skill_id")
	skill, err := st.GetSkill(r.Context(), skillID)
	if err != nil {
		WriteError(w, err)
		return
	}

	// Check if skill is referenced by active workflows.
	refs, err := workflow.FindWorkflowsReferencingSkill(r.Context(), skill.Name, st)
	if err != nil {
		WriteError(w, err)
		return
	}

	force := r.URL.Query().Get("force") == "true"
	if len(refs) > 0 && !force {
		WriteJSON(w, http.StatusConflict, map[string]any{
			"status":    "error",
			"message":   "skill is referenced by active workflows",
			"workflows": refs,
			"hint":      "use ?force=true to deprecate anyway",
		})
		return
	}

	skill.Status = domain.SkillStatusDeprecated
	if err := st.UpdateSkill(r.Context(), skill); err != nil {
		WriteError(w, err)
		return
	}

	resp := map[string]any{"skill": skill}
	if len(refs) > 0 {
		resp["warnings"] = refs
	}
	WriteJSON(w, http.StatusOK, resp)
}
