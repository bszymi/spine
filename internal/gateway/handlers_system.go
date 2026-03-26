package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// handleHealth returns system health status (unauthenticated).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "healthy"
	components := map[string]string{}

	if s.store != nil {
		if err := s.store.Ping(r.Context()); err != nil {
			status = "unhealthy"
			components["database"] = "unhealthy"
		} else {
			components["database"] = "healthy"
		}
	} else {
		status = "unhealthy"
		components["database"] = "not_configured"
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"status":     status,
		"components": components,
	})
}

func (s *Server) handleSystemRebuild(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "system.rebuild") {
		return
	}

	if s.projSync == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "projection service not configured"))
		return
	}

	if err := s.projSync.FullRebuild(r.Context()); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"status":   "completed",
		"trace_id": observe.TraceID(r.Context()),
	})
}

func (s *Server) handleSystemRebuildStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "system.rebuild") {
		return
	}

	if s.store == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	state, err := s.store.GetSyncState(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}

	if state == nil {
		WriteJSON(w, http.StatusOK, map[string]any{"status": "no_sync_state"})
		return
	}

	WriteJSON(w, http.StatusOK, state)
}

func (s *Server) handleSystemValidate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "system.validate") {
		return
	}

	// Use validation engine if available (cross-artifact rules)
	if s.validator != nil {
		results := s.validator.ValidateAll(r.Context())
		if results == nil {
			WriteError(w, domain.NewError(domain.ErrInternal, "validation query failed"))
			return
		}

		var issues []map[string]any
		for i := range results {
			if results[i].Status != "passed" {
				// Determine artifact path from errors or warnings
				artPath := ""
				if len(results[i].Errors) > 0 {
					artPath = results[i].Errors[0].ArtifactPath
				} else if len(results[i].Warnings) > 0 {
					artPath = results[i].Warnings[0].ArtifactPath
				}
				issues = append(issues, map[string]any{
					"path":   artPath,
					"result": results[i],
				})
			}
		}

		// Also run schema validation if artifact service is available
		if s.artifacts != nil {
			if artifacts, err := s.artifacts.List(r.Context(), ""); err == nil {
				for _, a := range artifacts {
					schemaResult := artifact.Validate(a)
					if schemaResult.Status != "passed" {
						issues = append(issues, map[string]any{
							"path":   a.Path,
							"result": schemaResult,
						})
					}
				}
			}
		}

		// Emit validation event.
		if s.store != nil {
			evtType := domain.EventValidationPassed
			if len(issues) > 0 {
				evtType = domain.EventValidationFailed
			}
			payload, _ := json.Marshal(map[string]any{
				"total_artifacts": len(results),
				"issues_count":    len(issues),
			})
			if s.events != nil {
				if err := s.events.Emit(r.Context(), domain.Event{
					EventID:   fmt.Sprintf("validate-%s", observe.TraceID(r.Context())),
					Type:      evtType,
					Timestamp: time.Now(),
					Payload:   payload,
				}); err != nil {
					observe.Logger(r.Context()).Warn("failed to emit validation event", "error", err)
				}
			}
		}

		WriteJSON(w, http.StatusOK, map[string]any{
			"total_artifacts": len(results),
			"issues":          issues,
			"trace_id":        observe.TraceID(r.Context()),
		})
		return
	}

	// Fallback to schema-only validation
	if s.artifacts == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "validation not configured"))
		return
	}

	artifacts, err := s.artifacts.List(r.Context(), "")
	if err != nil {
		WriteError(w, err)
		return
	}

	var issues []map[string]any
	for _, a := range artifacts {
		result := artifact.Validate(a)
		if result.Status != "passed" {
			issues = append(issues, map[string]any{
				"path":   a.Path,
				"result": result,
			})
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"total_artifacts": len(artifacts),
		"issues":          issues,
		"trace_id":        observe.TraceID(r.Context()),
	})
}
