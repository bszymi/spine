package assert

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// FileExists asserts that a file exists in the test repository.
func FileExists(t *testing.T, repo *harness.TestRepo, path string) {
	t.Helper()
	fullPath := filepath.Join(repo.Dir, path)
	if _, err := os.Stat(fullPath); err != nil {
		t.Errorf("expected file %s to exist: %v", path, err)
	}
}

// FileContains asserts that a file in the test repository contains the given substring.
func FileContains(t *testing.T, repo *harness.TestRepo, path, substring string) {
	t.Helper()
	fullPath := filepath.Join(repo.Dir, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), substring) {
		t.Errorf("file %s does not contain %q", path, substring)
	}
}

// ArtifactProjectionExists asserts that an artifact projection exists in the database.
func ArtifactProjectionExists(t *testing.T, db *harness.TestDB, ctx context.Context, path string) {
	t.Helper()
	_, err := db.Store.GetArtifactProjection(ctx, path)
	if err != nil {
		t.Errorf("expected projection for %s to exist: %v", path, err)
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
	default:
		t.Fatalf("unsupported projection field: %s", field)
	}

	if got != expected {
		t.Errorf("projection %s.%s: got %q, want %q", path, field, got, expected)
	}
}
