package gateway

import (
	"net/http"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
	"github.com/go-chi/chi/v5"
)

type createSkillRequest struct {
	SkillID     string `json:"skill_id,omitempty"`
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

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}

	req, ok := decodeBody[createSkillRequest](w, r)
	if !ok {
		return
	}
	if req.Name == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "name required"))
		return
	}

	skillID := req.SkillID
	if skillID == "" {
		id, err := generateID("skill")
		if err != nil {
			WriteError(w, domain.NewError(domain.ErrInternal, "failed to generate skill_id"))
			return
		}
		skillID = id
	}

	skill := &domain.Skill{
		SkillID:     skillID,
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

	st, ok := s.needStore(w, r)
	if !ok {
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

	st, ok := s.needStore(w, r)
	if !ok {
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

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}

	skillID := chi.URLParam(r, "skill_id")
	skill, err := st.GetSkill(r.Context(), skillID)
	if err != nil {
		WriteError(w, err)
		return
	}

	req, ok := decodeBody[updateSkillRequest](w, r)
	if !ok {
		return
	}

	// Rename guard (INIT-023 TASK-013, audit F6). Step eligibility
	// matches required_skills by skill *name*, so a rename moves
	// eligibility exactly as deprecation does: renaming away from a
	// required name empties that step's eligible set, and renaming onto
	// one makes every holder of this skill eligible. Like deprecation,
	// a rename that touches a name an active workflow references needs
	// an explicit ?force=true, and is audited. Like deprecation, only
	// active workflow projections are scanned.
	renamedFrom := ""
	if req.Name != nil && *req.Name != skill.Name {
		var refs []workflow.SkillReference
		for _, name := range []string{skill.Name, *req.Name} {
			rs, err := workflow.FindWorkflowsReferencingSkill(r.Context(), name, st)
			if err != nil {
				WriteError(w, err)
				return
			}
			refs = appendSkillReferences(refs, rs)
		}
		if len(refs) > 0 && r.URL.Query().Get("force") != "true" {
			WriteJSON(w, http.StatusConflict, map[string]any{
				"status":    "error",
				"message":   "renaming this skill changes step eligibility in active workflows",
				"workflows": refs,
				"hint":      "use ?force=true to rename anyway",
			})
			return
		}
		renamedFrom = skill.Name
	}

	skill, ok = rereadSkillForWrite(w, r, st, skill)
	if !ok {
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
	if renamedFrom != "" {
		observe.AuditLog(r.Context(), "skill_renamed",
			"skill_id", skill.SkillID, "from", renamedFrom, "to", skill.Name)
	}

	WriteJSON(w, http.StatusOK, skill)
}

// rereadSkillForWrite re-reads a skill right before PATCH or deprecate
// writes it, and returns the fresh row to apply the request to
// (INIT-023 TASK-013 review round 1).
//
// UpdateSkill writes every column, so writing back the row read before
// the reference scans would undo whatever a concurrent admin request
// committed in between: a forced deprecation silently reversed by a
// description edit (PATCH has no status field, but it would write the
// stale status back), or a rename reverted by a deprecation while the
// skill_renamed audit line says it happened. The fresh row supplies
// every column the request does not change. A name that changed since
// the first read means the rename guard or the deprecation's reference
// check decided on a name the skill no longer has, so the request is
// refused with 409 conflict and nothing is written; the client retries.
//
// This narrows the window to the back-to-back read and write; closing
// it fully needs a conditional UPDATE in the store (a recorded
// follow-up).
func rereadSkillForWrite(w http.ResponseWriter, r *http.Request, st store.SkillStore, read *domain.Skill) (*domain.Skill, bool) {
	fresh, err := st.GetSkill(r.Context(), read.SkillID)
	if err != nil {
		WriteError(w, err)
		return nil, false
	}
	if fresh.Name != read.Name {
		WriteError(w, domain.NewError(domain.ErrConflict,
			"skill was renamed by a concurrent request; retry"))
		return nil, false
	}
	return fresh, true
}

// appendSkillReferences appends refs to dst, skipping workflows already
// listed (a workflow that names both the old and the new skill name
// appears once in the 409 body).
func appendSkillReferences(dst, refs []workflow.SkillReference) []workflow.SkillReference {
	for _, ref := range refs {
		seen := false
		for _, have := range dst {
			if have == ref {
				seen = true
				break
			}
		}
		if !seen {
			dst = append(dst, ref)
		}
	}
	return dst
}

func (s *Server) handleSkillDeprecate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "skill.deprecate") {
		return
	}

	st, ok := s.needStore(w, r)
	if !ok {
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

	skill, ok = rereadSkillForWrite(w, r, st, skill)
	if !ok {
		return
	}
	skill.Status = domain.SkillStatusDeprecated
	if err := st.UpdateSkill(r.Context(), skill); err != nil {
		WriteError(w, err)
		return
	}
	// Deprecation drops the skill from every holder's eligible set
	// (claim-time matching counts active skills only), so it is
	// audited, and a forced deprecation says how many active workflows
	// it affected.
	observe.AuditLog(r.Context(), "skill_deprecated",
		"skill_id", skill.SkillID, "skill_name", skill.Name,
		"forced", len(refs) > 0, "referencing_workflows", len(refs))

	resp := map[string]any{"skill": skill}
	if len(refs) > 0 {
		resp["warnings"] = refs
	}
	WriteJSON(w, http.StatusOK, resp)
}
