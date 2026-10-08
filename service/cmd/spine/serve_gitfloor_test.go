package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestServe_RefusesGitBelowFloor is INIT-023 TASK-002 AC 13: `spine
// serve` exits with an error when git is older than 2.30, before it
// loads push auth (LoadPushAuthFromEnv scrubs SPINE_GIT_PUSH_TOKEN from
// the environment, so a token still present proves it never ran) and
// before any database or workspace setup (none is configured here; the
// version error is the first one returned).
func TestServe_RefusesGitBelowFloor(t *testing.T) {
	dir := t.TempDir()
	shim := "#!/bin/sh\necho 'git version 2.29.2'\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o755); err != nil { //nolint:gosec // G306: test shim must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SPINE_GIT_PUSH_TOKEN", "tok-floor-test")
	t.Setenv("SPINE_DEV_MODE", "")
	t.Setenv("SPINE_DATABASE_URL", "")

	cmd := serveCmd()
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("spine serve started with git 2.29.2")
	}
	if !strings.Contains(err.Error(), "older than the minimum supported git 2.30") {
		t.Fatalf("spine serve error = %v, want the git version floor", err)
	}
	if got := os.Getenv("SPINE_GIT_PUSH_TOKEN"); got != "tok-floor-test" {
		t.Fatalf("SPINE_GIT_PUSH_TOKEN = %q: push auth was loaded before the version check", got)
	}
}
