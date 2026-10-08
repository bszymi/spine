package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
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

	projSync, ok := s.needProjSync(w, r)
	if !ok {
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
	s.rebuilds.Store(rebuildRegistryKey(r.Context(), rebuildID), state)

	// G118: the rebuild deliberately detaches from the request
	// context. It must survive the client hanging up, and its
	// duration is unbounded relative to the HTTP request that kicked
	// it off. Status polling is via the rebuildID returned below.
	go func() { //nolint:gosec // G118: detached-by-design goroutine
		if err := projSync.FullRebuild(context.Background()); err != nil {
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
	val, ok := s.rebuilds.Load(rebuildRegistryKey(r.Context(), rebuildID))
	if !ok {
		WriteError(w, domain.NewError(domain.ErrNotFound, "rebuild not found"))
		return
	}

	state := val.(*rebuildState)
	WriteJSON(w, http.StatusOK, state.snapshot())
}

// rebuildRegistryKey scopes the process-global rebuild registry to the
// request's workspace (INIT-023 TASK-015, audit P3-29). The rebuild ID is
// "rb-" plus the first eight characters of the trace ID, which a client
// sets with X-Trace-Id, so in shared mode a bare-ID key let one tenant
// read another tenant's rebuild status, or replace it, by choosing the
// same trace prefix. The wire format of rebuild_id is unchanged. A
// request with no resolved workspace (no resolver wired) keys on the bare
// ID. Shared-mode workspace IDs pass workspace.ValidateID, which admits no
// NUL, so two composite keys cannot collide.
func rebuildRegistryKey(ctx context.Context, rebuildID string) string {
	if cfg := WorkspaceConfigFromContext(ctx); cfg != nil && cfg.ID != "" {
		return cfg.ID + "\x00" + rebuildID
	}
	return rebuildID
}

// handleMetrics serves Prometheus metrics to an operator of the request's
// workspace. In shared multi-workspace mode (a ServicePool is wired) the
// full export is a cross-tenant disclosure: its spine_workspace_pool_*
// series name every workspace in the process, and its process-global
// counters and histograms aggregate every tenant's runs and steps. There
// the response is only the caller's own workspace series
// (observe.ExportPrometheusWorkspace); the full export moves to
// GET /api/v1/platform/metrics behind SPINE_OPERATOR_TOKEN. INIT-023
// TASK-015, audit P3-29; owner question 3 default, pending the owner.
//
// The mode test is "a ServicePool is wired", not "a workspace config is
// present": file mode resolves a workspace config too, and a
// single-tenant scrape keeps the full export, unchanged.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "system.metrics") {
		return
	}
	if s.servicePool == nil {
		writePrometheus(w, observe.ExportPrometheus())
		return
	}
	cfg := WorkspaceConfigFromContext(r.Context())
	if cfg == nil || cfg.ID == "" {
		// Unreachable past authMiddleware in shared mode; fail closed
		// rather than fall back to the full export.
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "X-Workspace-ID header is required"))
		return
	}
	writePrometheus(w, observe.ExportPrometheusWorkspace(cfg.ID))
}

// handlePlatformMetrics serves the full process-wide export — every
// workspace's series and the process-global ones — to the platform
// operator. It is mounted in the operatorTokenMiddleware group, so it
// authenticates with SPINE_OPERATOR_TOKEN and answers 503 when that is
// unset. Shared-mode deployments scrape this one target.
func (s *Server) handlePlatformMetrics(w http.ResponseWriter, _ *http.Request) {
	writePrometheus(w, observe.ExportPrometheus())
}

func writePrometheus(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
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
				event.EmitLogged(r.Context(), s.events, domain.Event{
					EventID: fmt.Sprintf("validate-%s", observe.TraceID(r.Context())),
					Type:    evtType,
					Payload: payload,
				})
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

	// Fallback to schema-only validation. Without the artifact service
	// there is nothing to list, so respond consistently with other
	// routes rather than with a validate-specific message.
	artSvc, ok := s.needArtifacts(w, r)
	if !ok {
		return
	}
	artifacts, err := artSvc.List(r.Context(), "")
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
