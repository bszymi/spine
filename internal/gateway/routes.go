package gateway

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// routes creates the chi router with all middleware and route registrations.
func (s *Server) routes() http.Handler {
	r := chi.NewRouter()

	// Global middleware (order matters: recovery outermost, then trace, then logging)
	r.Use(recoveryMiddleware)
	r.Use(traceIDMiddleware)
	r.Use(loggingMiddleware)

	r.Route("/api/v1", func(r chi.Router) {
		// System
		r.Get("/system/health", s.handleHealth)
		r.Post("/system/rebuild", s.handleSystemRebuild)
		r.Get("/system/rebuild/{rebuild_id}", s.handleSystemRebuildStatus)
		r.Post("/system/validate", s.handleSystemValidate)

		// Artifacts — wildcard routing for slash-containing paths
		r.Post("/artifacts", s.handleArtifactCreate)
		r.Get("/artifacts", s.handleArtifactList)
		r.HandleFunc("/artifacts/*", s.handleArtifactWildcard)

		// Runs
		r.Post("/runs", s.handleRunStart)
		r.Get("/runs/{run_id}", s.handleRunStatus)
		r.Post("/runs/{run_id}/cancel", s.handleRunCancel)
		r.Post("/runs/{run_id}/steps/{step_id}/assign", s.handleStepAssign)

		// Steps
		r.Post("/steps/{assignment_id}/submit", s.handleStepSubmit)

		// Tasks — wildcard routing for slash-containing paths
		r.HandleFunc("/tasks/*", s.handleTaskWildcard)

		// Query
		r.Get("/query/artifacts", s.handleQueryArtifacts)
		r.Get("/query/graph", s.handleQueryGraph)
		r.Get("/query/history", s.handleQueryHistory)
		r.Get("/query/runs", s.handleQueryRuns)
	})

	return r
}

// extractArtifactPath extracts the artifact path from the wildcard,
// stripping any known suffix (/validate, /links).
// Returns (artifactPath, suffix).
func extractArtifactPath(r *http.Request) (string, string) {
	raw := chi.URLParam(r, "*")
	for _, suffix := range []string{"/validate", "/links"} {
		if strings.HasSuffix(raw, suffix) {
			return strings.TrimSuffix(raw, suffix), suffix
		}
	}
	return raw, ""
}

// extractTaskAction extracts the task path and action from the wildcard.
// E.g., "initiatives/INIT-001/task.md/accept" → ("initiatives/INIT-001/task.md", "accept")
func extractTaskAction(r *http.Request) (string, string) {
	raw := chi.URLParam(r, "*")
	for _, action := range []string{"/accept", "/reject", "/cancel", "/abandon", "/supersede"} {
		if strings.HasSuffix(raw, action) {
			return strings.TrimSuffix(raw, action), strings.TrimPrefix(action, "/")
		}
	}
	return raw, ""
}
