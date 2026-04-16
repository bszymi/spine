package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/go-chi/chi/v5"
)

type rebuildState struct {
	mu                 sync.Mutex
	RebuildID          string     `json:"rebuild_id"`
	Status             string     `json:"status"`
	StartedAt          time.Time  `json:"started_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
	ArtifactsProcessed int        `json:"artifacts_processed,omitempty"`
	ErrorDetail        string     `json:"error_detail,omitempty"`
}

func (rs *rebuildState) complete(status, errDetail string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	now := time.Now()
	rs.Status = status
	rs.CompletedAt = &now
	rs.ErrorDetail = errDetail
}

func (rs *rebuildState) snapshot() rebuildState {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rebuildState{
		RebuildID:          rs.RebuildID,
		Status:             rs.Status,
		StartedAt:          rs.StartedAt,
		CompletedAt:        rs.CompletedAt,
		ArtifactsProcessed: rs.ArtifactsProcessed,
		ErrorDetail:        rs.ErrorDetail,
	}
}

// handleHealth returns system health status (unauthenticated).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "healthy"
	components := map[string]string{}

	if s.storeFrom(r.Context()) != nil {
		if err := s.storeFrom(r.Context()).Ping(r.Context()); err != nil {
			status = "unhealthy"
			components["database"] = "unhealthy"
		} else {
			components["database"] = "healthy"
		}
	} else {
		status = "unhealthy"
		components["database"] = "not_configured"
	}

	resp := map[string]any{
		"status":     status,
		"components": components,
		"env":        s.env,
		"dev_mode":   s.devMode,
	}

	WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSystemRebuild(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "system.rebuild") {
		return
	}

	if s.projSyncFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "projection service not configured"))
		return
	}

	traceID := observe.TraceID(r.Context())
	if len(traceID) > 8 {
		traceID = traceID[:8]
	}
	rebuildID := fmt.Sprintf("rb-%s", traceID)
	state := &rebuildState{
		RebuildID: rebuildID,
		Status:    "in_progress",
		StartedAt: time.Now(),
	}
	s.rebuilds.Store(rebuildID, state)

	// G118: the rebuild deliberately detaches from the request
	// context. It must survive the client hanging up, and its
	// duration is unbounded relative to the HTTP request that kicked
	// it off. Status polling is via the rebuildID returned below.
	go func() { //nolint:gosec // G118: detached-by-design goroutine
		if err := s.projSyncFrom(r.Context()).FullRebuild(context.Background()); err != nil {
			observe.Logger(r.Context()).Error("system rebuild failed", "rebuild_id", rebuildID, "error", err)
			state.complete("failed", "rebuild failed, check server logs for details")
		} else {
			state.complete("completed", "")
		}
	}()

	WriteJSON(w, http.StatusAccepted, map[string]any{
		"status":     "started",
		"rebuild_id": rebuildID,
	})
}

func (s *Server) handleSystemRebuildStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "system.rebuild") {
		return
	}

	rebuildID := chi.URLParam(r, "rebuild_id")
	val, ok := s.rebuilds.Load(rebuildID)
	if !ok {
		WriteError(w, domain.NewError(domain.ErrNotFound, "rebuild not found"))
		return
	}

	state := val.(*rebuildState)
	WriteJSON(w, http.StatusOK, state.snapshot())
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "system.metrics") {
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(observe.ExportPrometheus()))
}

func (s *Server) handleSystemValidate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "system.validate") {
		return
	}

	// Use validation engine if available (cross-artifact rules)
	validator := s.validatorFrom(r.Context())
	if validator != nil {
		results := validator.ValidateAll(r.Context())
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
		if s.artifactsFrom(r.Context()) != nil {
			if artifacts, err := s.artifactsFrom(r.Context()).List(r.Context(), ""); err == nil {
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
		if s.storeFrom(r.Context()) != nil {
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

		passedCount := len(results) - len(issues)
		warningCount := 0
		failedCount := 0
		for _, iss := range issues {
			r, _ := iss["result"].(domain.ValidationResult)
			if r.Status == "failed" {
				failedCount++
			} else {
				warningCount++
			}
		}

		overallStatus := "passed"
		if failedCount > 0 {
			overallStatus = "failed"
		} else if warningCount > 0 {
			overallStatus = "warnings"
		}

		WriteJSON(w, http.StatusOK, map[string]any{
			"status":          overallStatus,
			"total_artifacts": len(results),
			"passed":          passedCount,
			"warnings":        warningCount,
			"failed":          failedCount,
			"results":         results,
		})
		return
	}

	// Fallback to schema-only validation
	if s.artifactsFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "validation not configured"))
		return
	}

	artifacts, err := s.artifactsFrom(r.Context()).List(r.Context(), "")
	if err != nil {
		WriteError(w, err)
		return
	}

	var allResults []domain.ValidationResult
	passedCount := 0
	warningCount := 0
	failedCount := 0
	for _, a := range artifacts {
		result := artifact.Validate(a)
		allResults = append(allResults, result)
		switch result.Status {
		case "passed":
			passedCount++
		case "warnings":
			warningCount++
		default:
			failedCount++
		}
	}

	overallStatus := "passed"
	if failedCount > 0 {
		overallStatus = "failed"
	} else if warningCount > 0 {
		overallStatus = "warnings"
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"status":          overallStatus,
		"total_artifacts": len(artifacts),
		"passed":          passedCount,
		"warnings":        warningCount,
		"failed":          failedCount,
		"results":         allResults,
	})
}
