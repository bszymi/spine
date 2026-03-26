package assert

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// ArtifactProjectionExists asserts that an artifact projection exists in the database.
func ArtifactProjectionExists(t *testing.T, db *harness.TestDB, ctx context.Context, path string) {
	t.Helper()
	_, err := db.Store.GetArtifactProjection(ctx, path)
	if err != nil {
		t.Errorf("expected projection for %s to exist: %v", path, err)
	}
}

// ArtifactProjectionNotExists asserts that an artifact projection does not exist.
// Only treats a not_found error as successful absence; other errors (DB unavailable,
// cancelled context) are reported as test failures.
func ArtifactProjectionNotExists(t *testing.T, db *harness.TestDB, ctx context.Context, path string) {
	t.Helper()
	_, err := db.Store.GetArtifactProjection(ctx, path)
	if err == nil {
		t.Errorf("expected projection for %s to not exist, but it does", path)
		return
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrNotFound {
		t.Fatalf("unexpected error checking projection for %s: %v", path, err)
	}
}

// ArtifactProjectionField asserts that a specific field of an artifact projection
// matches the expected value. Supported fields: Title, ArtifactType, Status.
func ArtifactProjectionField(t *testing.T, db *harness.TestDB, ctx context.Context, path, field, expected string) {
	t.Helper()
	proj, err := db.Store.GetArtifactProjection(ctx, path)
	if err != nil {
		t.Fatalf("get projection for %s: %v", path, err)
	}

	var got string
	switch field {
	case "Title":
		got = proj.Title
	case "ArtifactType":
		got = proj.ArtifactType
	case "Status":
		got = proj.Status
	case "ArtifactID":
		got = proj.ArtifactID
	default:
		t.Fatalf("unsupported projection field: %s", field)
	}

	if got != expected {
		t.Errorf("projection %s.%s: got %q, want %q", path, field, got, expected)
	}
}

// ArtifactHasLink asserts that an artifact projection has a link of the given type
// to the given target.
func ArtifactHasLink(t *testing.T, db *harness.TestDB, ctx context.Context, path string, linkType domain.LinkType, target string) {
	t.Helper()
	links, err := db.Store.QueryArtifactLinks(ctx, path)
	if err != nil {
		t.Fatalf("query links for %s: %v", path, err)
	}

	// Normalize target to match both canonical (/path) and relative (path) forms.
	canonicalTarget := target
	if !strings.HasPrefix(canonicalTarget, "/") {
		canonicalTarget = "/" + canonicalTarget
	}
	relativeTarget := strings.TrimPrefix(canonicalTarget, "/")

	for _, link := range links {
		if link.LinkType == string(linkType) && (link.TargetPath == canonicalTarget || link.TargetPath == relativeTarget) {
			return
		}
	}
	t.Errorf("projection %s: expected link type=%s target=%s, not found", path, linkType, target)
}

// ArtifactValidationPasses asserts that the validation engine reports no errors
// for the given artifact path.
func ArtifactValidationPasses(t *testing.T, rt *harness.TestRuntime, ctx context.Context, path string) {
	t.Helper()
	if rt.Validator == nil {
		t.Fatal("ArtifactValidationPasses requires WithValidation() on the runtime")
	}
	result := rt.Validator.Validate(ctx, path)
	if result.Status == "failed" {
		details, _ := json.Marshal(result.Errors)
		t.Errorf("expected validation to pass for %s, got errors: %s", path, details)
	}
}

// ArtifactValidationFails asserts that the validation engine reports at least one
// error for the given artifact path.
func ArtifactValidationFails(t *testing.T, rt *harness.TestRuntime, ctx context.Context, path string) {
	t.Helper()
	if rt.Validator == nil {
		t.Fatal("ArtifactValidationFails requires WithValidation() on the runtime")
	}
	result := rt.Validator.Validate(ctx, path)
	if result.Status != "failed" {
		t.Errorf("expected validation to fail for %s, but it passed", path)
	}
}
