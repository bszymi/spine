package workspace

import (
	"os"
	"path/filepath"
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
