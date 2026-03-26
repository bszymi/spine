package assert

import (
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

// ErrorCode asserts that an error is a SpineError with the expected error code.
func ErrorCode(t *testing.T, err error, expected domain.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Errorf("expected error with code %s, got nil", expected)
		return
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) {
		t.Errorf("expected SpineError with code %s, got %T: %v", expected, err, err)
		return
	}
	if spineErr.Code != expected {
		t.Errorf("error code: got %q, want %q (message: %s)", spineErr.Code, expected, spineErr.Message)
	}
}

// ErrorContains asserts that an error message contains the given substring.
func ErrorContains(t *testing.T, err error, substring string) {
	t.Helper()
	if err == nil {
		t.Errorf("expected error containing %q, got nil", substring)
		return
	}
	if !strings.Contains(err.Error(), substring) {
		t.Errorf("error %q does not contain %q", err.Error(), substring)
	}
}

// NoError asserts that an error is nil.
func NoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

// OperationForbidden asserts that an error indicates a forbidden operation.
func OperationForbidden(t *testing.T, err error) {
	t.Helper()
	ErrorCode(t, err, domain.ErrForbidden)
}

// OperationUnauthorized asserts that an error indicates an unauthorized operation.
func OperationUnauthorized(t *testing.T, err error) {
	t.Helper()
	ErrorCode(t, err, domain.ErrUnauthorized)
}

// ValidationFailed asserts that an error indicates a validation failure.
func ValidationFailed(t *testing.T, err error) {
	t.Helper()
	ErrorCode(t, err, domain.ErrValidationFailed)
}
