package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/yamlsafe"
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

// TestLoad_OversizedFile asserts that .spine.yaml above the yamlsafe
// cap is refused with a bounded error before we try to decode it. A
// hostile repo must not be able to stall config loading with a
// multi-megabyte file.
func TestLoad_OversizedFile(t *testing.T) {
	dir := t.TempDir()
	// Prefix with a valid scalar so the file parses as YAML below the cap.
	payload := append([]byte("artifacts_dir: spine\n# "), bytes.Repeat([]byte("a"), yamlsafe.MaxBytes+10)...)
	if err := os.WriteFile(filepath.Join(dir, ".spine.yaml"), payload, 0o644); err != nil {
		t.Fatalf("write oversized file: %v", err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for oversized .spine.yaml")
	}
	if !strings.Contains(err.Error(), "byte cap") {
		t.Errorf("expected byte-cap error, got %v", err)
	}
}

// TestLoad_AliasBombRejected asserts yamlsafe's alias guard catches
// billion-laughs-shaped input before we materialise it.
func TestLoad_AliasBombRejected(t *testing.T) {
	dir := t.TempDir()
	// Build an alias-heavy doc that exceeds MaxAliases.
	var sb strings.Builder
	sb.WriteString("artifacts_dir: &a spine\nrefs:\n")
	for i := 0; i < yamlsafe.MaxAliases+5; i++ {
		sb.WriteString("  - *a\n")
	}
	if err := os.WriteFile(filepath.Join(dir, ".spine.yaml"), []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write alias bomb: %v", err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for alias-heavy .spine.yaml")
	}
	if !strings.Contains(err.Error(), "alias") {
		t.Errorf("expected alias-count error, got %v", err)
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
