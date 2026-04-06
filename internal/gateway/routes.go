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
		// Unauthenticated
		r.Get("/system/health", s.handleHealth)
		r.Get("/system/metrics", s.handleMetrics)

		// Workspace management — operator token auth, workspace-exempt
		r.Group(func(r chi.Router) {
			r.Use(operatorTokenMiddleware)
			r.Post("/workspaces", s.handleWorkspaceCreate)
			r.Get("/workspaces", s.handleWorkspaceList)
			r.Get("/workspaces/{workspace_id}", s.handleWorkspaceGet)
			r.Post("/workspaces/{workspace_id}/deactivate", s.handleWorkspaceDeactivate)
		})

		// Authenticated + workspace-scoped routes
		r.Group(func(r chi.Router) {
			r.Use(s.authMiddleware)
			r.Use(s.workspaceMiddleware)

			// System (operator)
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

			// Divergence
			r.Post("/runs/{run_id}/divergences/{divergence_id}/branches", s.handleCreateBranch)
			r.Post("/runs/{run_id}/divergences/{divergence_id}/close-window", s.handleCloseWindow)

			// Steps
			r.Post("/steps/{assignment_id}/submit", s.handleStepSubmit)

			// Assignments
			r.Get("/assignments", s.handleListAssignments)

			// Tasks — wildcard routing for slash-containing paths
			r.HandleFunc("/tasks/*", s.handleTaskWildcard)

			// Discussions
			r.Post("/discussions", s.handleDiscussionCreate)
			r.Get("/discussions", s.handleDiscussionList)
			r.Get("/discussions/{thread_id}", s.handleDiscussionGet)
			r.Post("/discussions/{thread_id}/comments", s.handleDiscussionComment)
			r.Post("/discussions/{thread_id}/resolve", s.handleDiscussionResolve)
			r.Post("/discussions/{thread_id}/reopen", s.handleDiscussionReopen)

			// Execution
			r.Get("/execution/candidates", s.handleExecutionCandidates)
			r.Post("/execution/claim", s.handleExecutionClaim)

			// Query
			r.Get("/query/discussions", s.handleQueryDiscussions)
			r.Get("/query/artifacts", s.handleQueryArtifacts)
			r.Get("/query/graph", s.handleQueryGraph)
			r.Get("/query/history", s.handleQueryHistory)
			r.Get("/query/runs", s.handleQueryRuns)

			// Tokens (admin)
			r.Post("/tokens", s.handleTokenCreate)
			r.Delete("/tokens/{token_id}", s.handleTokenRevoke)
			r.Get("/tokens", s.handleTokenList)
		})
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
