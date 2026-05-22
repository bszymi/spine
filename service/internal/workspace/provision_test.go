package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeDBName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ws-main", "spine_ws_ws_main"},
		{"workspace123", "spine_ws_workspace123"},
		{"My Workspace!", "spine_ws_my_workspace_"},
		{"ws.prod.eu-1", "spine_ws_ws_prod_eu_1"},
		{"UPPER", "spine_ws_upper"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeDBName(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeDBName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestReplaceDatabaseInURL(t *testing.T) {
	tests := []struct {
		name     string
		connURL  string
		newDB    string
		expected string
	}{
		{
			name:     "postgres URL",
			connURL:  "postgres://user:pass@localhost:5432/olddb?sslmode=disable",
			newDB:    "newdb",
			expected: "postgres://user:pass@localhost:5432/newdb?sslmode=disable",
		},
		{
			name:     "postgres URL no query",
			connURL:  "postgres://user:pass@localhost:5432/olddb",
			newDB:    "newdb",
			expected: "postgres://user:pass@localhost:5432/newdb",
		},
		{
			name:     "postgresql URL",
			connURL:  "postgresql://spine:spine@host:5432/postgres",
			newDB:    "spine_ws_test",
			expected: "postgresql://spine:spine@host:5432/spine_ws_test",
		},
		{
			name:     "key=value format",
			connURL:  "host=localhost port=5432 dbname=olddb user=spine",
			newDB:    "spine_ws_new",
			expected: "host=localhost port=5432 dbname=spine_ws_new user=spine",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replaceDatabaseInURL(tt.connURL, tt.newDB)
			if got != tt.expected {
				t.Errorf("replaceDatabaseInURL(%q, %q) = %q, want %q", tt.connURL, tt.newDB, got, tt.expected)
			}
		})
	}
}

func TestPgIdentifier(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"spine_ws_test", `"spine_ws_test"`},
		{`name"with"quotes`, `"name""with""quotes"`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := pgIdentifier(tt.input)
			if got != tt.expected {
				t.Errorf("pgIdentifier(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestIsSpineRepo_WithSpineYaml(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".spine.yaml"), []byte("version: 1"), 0644); err != nil {
		t.Fatalf("write .spine.yaml: %v", err)
	}
	if !IsSpineRepo(dir) {
		t.Error("expected IsSpineRepo=true when .spine.yaml exists")
	}
}

func TestIsSpineRepo_WithGovernanceDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "governance"), 0755); err != nil {
		t.Fatalf("mkdir governance: %v", err)
	}
	if !IsSpineRepo(dir) {
		t.Error("expected IsSpineRepo=true when governance/ exists")
	}
}

func TestIsSpineRepo_WithWorkflowsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "workflows"), 0755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if !IsSpineRepo(dir) {
		t.Error("expected IsSpineRepo=true when workflows/ exists")
	}
}

func TestIsSpineRepo_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	if IsSpineRepo(dir) {
		t.Error("expected IsSpineRepo=false for empty directory")
	}
}

func TestProvisionRepo_RejectsInvalidID(t *testing.T) {
	baseDir := t.TempDir()
	p := NewRepoProvisioner(baseDir)

	cases := []string{
		"../escape",
		"a/b",
		"-rm",
		"",
		"ws id with space",
	}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			path, err := p.ProvisionRepo(context.Background(), id, "")
			if err == nil {
				t.Fatalf("ProvisionRepo(%q) = %q, want error", id, path)
			}
		})
	}
}

func TestProvisionRepo_StaysInsideBaseDir(t *testing.T) {
	baseDir := t.TempDir()
	p := NewRepoProvisioner(baseDir)

	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		t.Fatalf("abs base: %v", err)
	}

	// Fresh init with a valid ID.
	repoPath, err := p.ProvisionRepo(context.Background(), "valid-ws_1", "")
	if err != nil {
		t.Fatalf("ProvisionRepo: %v", err)
	}
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		t.Fatalf("abs repo: %v", err)
	}
	rel, err := filepath.Rel(absBase, absRepo)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	if strings.HasPrefix(rel, "..") || rel == "." {
		t.Fatalf("repo path %q escapes base %q (rel=%q)", absRepo, absBase, rel)
	}
	if rel != "valid-ws_1" {
		t.Errorf("expected path directly under base, got rel=%q", rel)
	}
}

func TestReplaceDatabaseInURL_EdgeCases(t *testing.T) {
	// Arbitrary format without dbname= falls through to append.
	got := replaceDatabaseInURL("something=else", "newdb")
	if got != "something=else dbname=newdb" {
		t.Errorf("fallback append: got %q", got)
	}

	// dbname= replacement preserves trailing key=value pairs.
	got = replaceDatabaseInURL("host=localhost dbname=old sslmode=disable", "newdb")
	if got != "host=localhost dbname=newdb sslmode=disable" {
		t.Errorf("dbname= middle replace: got %q", got)
	}

	// postgres:// with no path segment: the helper must add the database
	// path, not overwrite the authority. Regression guard for the
	// pre-refactor behaviour that produced "postgres://newdb".
	got = replaceDatabaseInURL("postgres://host?sslmode=disable", "newdb")
	if got != "postgres://host/newdb?sslmode=disable" {
		t.Errorf("no-path URL: got %q", got)
	}

	got = replaceDatabaseInURL("postgres://host:5432", "newdb")
	if got != "postgres://host:5432/newdb" {
		t.Errorf("no-path URL without query: got %q", got)
	}
}
