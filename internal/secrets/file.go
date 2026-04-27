package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileConfig configures the file-mounted SecretClient. Root must point
// to a directory laid out as:
//
//	{Root}/workspaces/{workspace_id}/{purpose}.json
//
// Each JSON file must contain a top-level JSON string holding the
// secret value, e.g. `"postgres://user:pass@host:5432/db"`. JSON
// objects are not supported on purpose: the dev/test path matches the
// AWS Secrets Manager string-only shape so prod-only behaviour cannot
// hide behind a richer dev schema.
type FileConfig struct {
	Root string
}

// FileClient implements SecretClient against a directory of JSON
// files. It is the dev / test path; production deployments use the
// AWS provider (see ADR-010).
type FileClient struct {
	cfg FileConfig
}

// Compile-time assertion: FileClient implements SecretClient.
var _ SecretClient = (*FileClient)(nil)

// NewFileClient builds a FileClient. The Root must exist and be a
// directory at construction time so misconfiguration fails fast.
func NewFileClient(cfg FileConfig) (*FileClient, error) {
	if cfg.Root == "" {
		return nil, errors.New("file secret client: root is required")
	}
	info, err := os.Stat(cfg.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file secret client: root %q does not exist", cfg.Root)
		}
		return nil, fmt.Errorf("file secret client: stat root %q: %w", cfg.Root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("file secret client: root %q is not a directory", cfg.Root)
	}
	return &FileClient{cfg: cfg}, nil
}

// Get reads the file backing ref. The file's content is parsed as a
// top-level JSON string; the decoded value is the secret. The file's
// modification time (UnixNano) is returned as the VersionID so that
// callers can detect updates.
func (c *FileClient) Get(_ context.Context, ref SecretRef) (SecretValue, VersionID, error) {
	path, err := c.refPath(ref)
	if err != nil {
		return SecretValue{}, "", err
	}

	info, statErr := os.Stat(path)
	switch {
	case errors.Is(statErr, fs.ErrNotExist):
		// Distinguish "secret missing" from "mount missing": if the
		// configured Root has gone away, the store is down. Otherwise
		// the workspace/purpose simply isn't seeded.
		if !c.rootReachable() {
			return SecretValue{}, "", fmt.Errorf("%w: root %q unreachable", ErrSecretStoreDown, c.cfg.Root)
		}
		return SecretValue{}, "", fmt.Errorf("%w: %s", ErrSecretNotFound, ref)
	case errors.Is(statErr, fs.ErrPermission):
		return SecretValue{}, "", fmt.Errorf("%w: %s", ErrAccessDenied, ref)
	case statErr != nil:
		return SecretValue{}, "", fmt.Errorf("%w: %s: %v", ErrSecretStoreDown, ref, statErr)
	}

	data, readErr := os.ReadFile(path) //nolint:gosec // path is constructed from a validated ref under c.cfg.Root.
	switch {
	case errors.Is(readErr, fs.ErrNotExist):
		// File disappeared between Stat and ReadFile (e.g. concurrent
		// rotation). Re-check the mount before classifying.
		if !c.rootReachable() {
			return SecretValue{}, "", fmt.Errorf("%w: root %q unreachable", ErrSecretStoreDown, c.cfg.Root)
		}
		return SecretValue{}, "", fmt.Errorf("%w: %s", ErrSecretNotFound, ref)
	case errors.Is(readErr, fs.ErrPermission):
		return SecretValue{}, "", fmt.Errorf("%w: %s", ErrAccessDenied, ref)
	case readErr != nil:
		return SecretValue{}, "", fmt.Errorf("%w: %s: %v", ErrSecretStoreDown, ref, readErr)
	}

	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		// File found but malformed. Surface as an InvalidRef-flavoured
		// error so callers (and tests) can distinguish from missing.
		// We deliberately do NOT include the file bytes in the error,
		// since they may contain a secret-shaped payload.
		return SecretValue{}, "", fmt.Errorf("file secret client: %s: malformed JSON (expected top-level string): %w", ref, err)
	}

	vid := VersionID(fmt.Sprintf("%d", info.ModTime().UnixNano()))
	return NewSecretValue([]byte(value)), vid, nil
}

// Invalidate is a no-op; the file provider has no remote cache.
func (c *FileClient) Invalidate(_ context.Context, ref SecretRef) error {
	if _, _, err := ParseRef(ref); err != nil {
		return err
	}
	return nil
}

// rootReachable reports whether the configured Root is still a
// directory on disk. Used to disambiguate ENOENT-on-secret from
// ENOENT-on-mount.
func (c *FileClient) rootReachable() bool {
	info, err := os.Stat(c.cfg.Root)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// refPath maps a validated SecretRef to its file path under Root.
func (c *FileClient) refPath(ref SecretRef) (string, error) {
	ws, purpose, err := ParseRef(ref)
	if err != nil {
		return "", err
	}
	// ParseRef already rejects "/" in segments, so Join will not
	// escape the Root directory.
	return filepath.Join(c.cfg.Root, "workspaces", ws, purpose+".json"), nil
}
