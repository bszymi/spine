package domain

import "fmt"

// ErrorCode represents a Spine API error code.
// Per api-operations.md §4.1.
type ErrorCode string

const (
	ErrNotFound         ErrorCode = "not_found"
	ErrAlreadyExists    ErrorCode = "already_exists"
	ErrValidationFailed ErrorCode = "validation_failed"
	ErrUnauthorized     ErrorCode = "unauthorized"
	ErrForbidden        ErrorCode = "forbidden"
	ErrConflict         ErrorCode = "conflict"
	ErrPrecondition     ErrorCode = "precondition_failed"
	ErrInvalidParams    ErrorCode = "invalid_params"
	ErrInternal         ErrorCode = "internal_error"
	ErrUnavailable      ErrorCode = "service_unavailable"
	ErrRateLimited      ErrorCode = "rate_limited"
	ErrGit              ErrorCode = "git_error"
	ErrWorkflowNotFound ErrorCode = "workflow_not_found"
)

// SpineError is the standard error type for Spine operations.
type SpineError struct {
	Code    ErrorCode `json:"code" yaml:"code"`
	Message string    `json:"message" yaml:"message"`
	Detail  any       `json:"detail,omitempty" yaml:"detail,omitempty"`
}

func (e *SpineError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewError creates a new SpineError.
func NewError(code ErrorCode, message string) *SpineError {
	return &SpineError{Code: code, Message: message}
}

// NewErrorWithDetail creates a new SpineError with structured detail.
func NewErrorWithDetail(code ErrorCode, message string, detail any) *SpineError {
	return &SpineError{Code: code, Message: message, Detail: detail}
}

// ViolationClassification classifies a validation failure for resolution guidance.
// Per validation-service.md §4.
type ViolationClassification string

const (
	ViolationStructuralError   ViolationClassification = "structural_error"
	ViolationLinkInconsistency ViolationClassification = "link_inconsistency"
	ViolationStatusConflict    ViolationClassification = "status_conflict"
	ViolationScopeConflict     ViolationClassification = "scope_conflict"
	ViolationMissingPrereq     ViolationClassification = "missing_prerequisite"
)

// ValidationError represents a single validation failure.
type ValidationError struct {
	RuleID         string                  `json:"rule_id,omitempty" yaml:"rule_id,omitempty"`
	Classification ViolationClassification `json:"classification,omitempty" yaml:"classification,omitempty"`
	ArtifactPath   string                  `json:"artifact_path,omitempty" yaml:"artifact_path,omitempty"`
	Field          string                  `json:"field,omitempty" yaml:"field,omitempty"`
	Severity       string                  `json:"severity" yaml:"severity"` // "error" or "warning"
	Message        string                  `json:"message" yaml:"message"`
}

// ValidationResult represents the outcome of a validation check.
type ValidationResult struct {
	Status   string            `json:"status" yaml:"status"` // "passed", "failed", "warnings"
	Errors   []ValidationError `json:"errors,omitempty" yaml:"errors,omitempty"`
	Warnings []ValidationError `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}
