package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const configFileName = ".spine.yaml"

// SpineConfig holds Spine repository configuration read from .spine.yaml.
type SpineConfig struct {
	ArtifactsDir string `yaml:"artifacts_dir"`
}

// Load reads .spine.yaml from the given repository root.
// Returns default config if the file does not exist.
func Load(repoPath string) (*SpineConfig, error) {
	path := filepath.Join(repoPath, configFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaults(), nil
		}
		return nil, err
	}

	var cfg SpineConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	cfg.ArtifactsDir = normalizePath(cfg.ArtifactsDir)
	return &cfg, nil
}

// defaults returns the default configuration when .spine.yaml is absent.
func defaults() *SpineConfig {
	return &SpineConfig{ArtifactsDir: "/"}
}

// normalizePath cleans up the artifacts_dir value.
// Empty, ".", and "/" all mean "repo root". Non-root paths have
// trailing slashes stripped and leading "./" removed.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)

	if p == "" || p == "." || p == "./" || p == "/" {
		return "/"
	}

	p = strings.TrimPrefix(p, "./")
	p = strings.TrimSuffix(p, "/")
	return p
}

// ResolvePath joins the artifacts directory with a relative artifact path.
// When ArtifactsDir is "/" (repo root), paths are returned as-is.
func (c *SpineConfig) ResolvePath(artifactPath string) string {
	artifactPath = strings.TrimPrefix(artifactPath, "/")
	if c.ArtifactsDir == "/" {
		return artifactPath
	}
	return filepath.Join(c.ArtifactsDir, artifactPath)
}
