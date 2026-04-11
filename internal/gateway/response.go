package gateway

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/bszymi/spine/internal/domain"
)

// ErrorResponse is the standard JSON error envelope per api-operations.md §4.
type ErrorResponse struct {
	Status string        `json:"status"`
	Errors []ErrorDetail `json:"errors"`
}

// ErrorDetail represents a single error in the response.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
}

// WriteJSON marshals v as JSON and writes it with the given HTTP status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Best effort — headers already sent
		http.Error(w, `{"status":"error","errors":[{"code":"internal_error","message":"response encoding failed"}]}`, http.StatusInternalServerError)
	}
}

// WriteError writes a structured error response derived from the given error.
// If err is a *domain.SpineError, its code is mapped to an HTTP status.
// Otherwise, a generic 500 internal_error is returned.
func WriteError(w http.ResponseWriter, err error) {
	var spineErr *domain.SpineError
	if errors.As(err, &spineErr) {
		WriteJSON(w, httpStatusForCode(spineErr.Code), ErrorResponse{
			Status: "error",
			Errors: []ErrorDetail{
				{
					Code:    string(spineErr.Code),
					Message: spineErr.Message,
					Detail:  spineErr.Detail,
				},
			},
		})
		return
	}

	WriteJSON(w, http.StatusInternalServerError, ErrorResponse{
		Status: "error",
		Errors: []ErrorDetail{
			{
				Code:    string(domain.ErrInternal),
				Message: "internal error",
			},
		},
	})
}

// WriteNotImplemented writes a 501 response for stub endpoints.
func WriteNotImplemented(w http.ResponseWriter) {
	WriteJSON(w, http.StatusNotImplemented, ErrorResponse{
		Status: "error",
		Errors: []ErrorDetail{
			{
				Code:    "not_implemented",
				Message: "not yet implemented",
			},
		},
	})
}

// httpStatusForCode maps a domain ErrorCode to an HTTP status code.
func httpStatusForCode(code domain.ErrorCode) int {
	switch code {
	case domain.ErrNotFound, domain.ErrWorkflowNotFound:
		return http.StatusNotFound
	case domain.ErrAlreadyExists, domain.ErrConflict:
		return http.StatusConflict
	case domain.ErrValidationFailed:
		return http.StatusUnprocessableEntity
	case domain.ErrUnauthorized:
		return http.StatusUnauthorized
	case domain.ErrForbidden:
		return http.StatusForbidden
	case domain.ErrPrecondition:
		return http.StatusPreconditionFailed
	case domain.ErrInvalidParams:
		return http.StatusBadRequest
	case domain.ErrRateLimited:
		return http.StatusTooManyRequests
	case domain.ErrUnavailable:
		return http.StatusServiceUnavailable
	case domain.ErrInternal, domain.ErrGit:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
