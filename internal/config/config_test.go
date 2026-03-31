package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_FileNotExists(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ArtifactsDir != "/" {
		t.Errorf("expected default /, got %q", cfg.ArtifactsDir)
	}
}

func TestLoad_ValidFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".spine.yaml"), []byte("artifacts_dir: spine/\n"), 0o644)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ArtifactsDir != "spine" {
		t.Errorf("expected spine, got %q", cfg.ArtifactsDir)
	}
}

func TestLoad_EmptyArtifactsDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".spine.yaml"), []byte("artifacts_dir: \"\"\n"), 0o644)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ArtifactsDir != "/" {
		t.Errorf("expected /, got %q", cfg.ArtifactsDir)
	}
}

func TestLoad_RootSlash(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".spine.yaml"), []byte("artifacts_dir: /\n"), 0o644)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ArtifactsDir != "/" {
		t.Errorf("expected /, got %q", cfg.ArtifactsDir)
	}
}

func TestLoad_DotPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".spine.yaml"), []byte("artifacts_dir: .\n"), 0o644)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ArtifactsDir != "/" {
		t.Errorf("expected / for dot, got %q", cfg.ArtifactsDir)
	}
}

func TestLoad_DotSlashPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".spine.yaml"), []byte("artifacts_dir: ./spine/\n"), 0o644)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ArtifactsDir != "spine" {
		t.Errorf("expected spine, got %q", cfg.ArtifactsDir)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".spine.yaml"), []byte("{{invalid yaml"), 0o644)

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "/"},
		{".", "/"},
		{"./", "/"},
		{"/", "/"},
		{"spine/", "spine"},
		{"spine", "spine"},
		{"./spine/", "spine"},
		{"./spine", "spine"},
		{"  spine/  ", "spine"},
		{"docs/spine", "docs/spine"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizePath(tt.input)
			if got != tt.want {
				t.Errorf("normalizePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	tests := []struct {
		artifactsDir string
		path         string
		want         string
	}{
		{"/", "governance/charter.md", "governance/charter.md"},
		{"/", "/governance/charter.md", "governance/charter.md"},
		{"spine", "governance/charter.md", "spine/governance/charter.md"},
		{"spine", "/governance/charter.md", "spine/governance/charter.md"},
		{"docs/spine", "governance/charter.md", "docs/spine/governance/charter.md"},
	}

	for _, tt := range tests {
		t.Run(tt.artifactsDir+":"+tt.path, func(t *testing.T) {
			cfg := &SpineConfig{ArtifactsDir: tt.artifactsDir}
			got := cfg.ResolvePath(tt.path)
			if got != tt.want {
				t.Errorf("ResolvePath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
