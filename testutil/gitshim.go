package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GitRecorder records the argv of every `git` process a test starts
// through PATH lookup, then runs the real git with it. Tests use it to
// assert the exact argv a git client builds, and that a refused call
// starts no git process at all (INIT-023 TASK-002 ACs 5, 6, 18).
type GitRecorder struct {
	log string
}

// gitRecorderScript is the PATH wrapper. Each invocation appends its
// arguments separated by 0x1f and terminated by 0x1e (bytes no git
// argument the tests build contains), then execs the real git. When
// SPINE_GIT_RECORDER_FAKE_SYMREF is set, `git symbolic-ref ...` prints
// that value instead of running git, so a test can break a
// post-condition that real git would satisfy.
const gitRecorderScript = `#!/bin/sh
if [ -n "$SPINE_GIT_RECORDER_LOG" ]; then
  for a in "$@"; do printf '%s\037' "$a"; done >> "$SPINE_GIT_RECORDER_LOG"
  printf '\036' >> "$SPINE_GIT_RECORDER_LOG"
fi
if [ "$1" = symbolic-ref ] && [ -n "$SPINE_GIT_RECORDER_FAKE_SYMREF" ]; then
  echo "$SPINE_GIT_RECORDER_FAKE_SYMREF"
  exit 0
fi
exec "$SPINE_GIT_RECORDER_REAL" "$@"
`

// InstallGitRecorder puts the recording wrapper first on PATH for the
// rest of the test (t.Setenv, so the test must not be parallel). It
// skips the test when git is not installed. Setup commands the test runs
// through `git` after this call are recorded too: call Reset right
// before the call under test.
func InstallGitRecorder(t *testing.T) *GitRecorder {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	if realGit, err = filepath.Abs(realGit); err != nil {
		t.Fatalf("resolve git path: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(gitRecorderScript), 0o755); err != nil { //nolint:gosec // G306: the wrapper must be executable
		t.Fatalf("write git recorder: %v", err)
	}
	r := &GitRecorder{log: filepath.Join(dir, "argv.log")}
	t.Setenv("SPINE_GIT_RECORDER_REAL", realGit)
	t.Setenv("SPINE_GIT_RECORDER_LOG", r.log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return r
}

// Reset forgets every recorded invocation.
func (r *GitRecorder) Reset(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(r.log, nil, 0o600); err != nil {
		t.Fatalf("reset git recorder: %v", err)
	}
}

// Calls returns the argv (without "git") of every invocation since the
// last Reset, in order.
func (r *GitRecorder) Calls(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(r.log)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read git recorder: %v", err)
	}
	var calls [][]string
	for _, rec := range strings.Split(string(data), "\x1e") {
		if rec == "" {
			continue
		}
		args := strings.Split(rec, "\x1f")
		if n := len(args); n > 0 && args[n-1] == "" {
			args = args[:n-1]
		}
		calls = append(calls, args)
	}
	return calls
}

// HermeticGit isolates git from the host's configuration for the rest of
// the test: no system or global config, a fixed identity. Tests that
// build repositories with the real git call it first, so a host's
// commit.gpgsign, init.defaultBranch or missing identity cannot change
// the outcome.
func HermeticGit(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Spine Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@spine.local")
	t.Setenv("GIT_COMMITTER_NAME", "Spine Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@spine.local")
}

// NoGitIdentity takes every git identity away for the rest of the test:
// git then has none, as on a GitHub runner (its user has an empty GECOS
// name) and in Spine's standalone image (`useradd spine`). No system or
// global config is read (HOME and XDG_CONFIG_HOME are empty directories,
// GIT_CONFIG_GLOBAL is the null device), GIT_AUTHOR_*, GIT_COMMITTER_* and
// EMAIL are unset, and user.useConfigOnly=true (through GIT_CONFIG_COUNT)
// stops git from guessing one from the passwd entry and the host name — on
// macOS the GECOS name and a .local host name would otherwise pass, so a
// test that needs an identity it was not given passed locally and failed
// only on CI. A plain `git commit` is then refused "Author identity
// unknown" on every host. A repository's own user.name/user.email, and
// identity variables a command is given explicitly, still apply. Tests
// that use it must not be parallel (t.Setenv).
func NoGitIdentity(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.useConfigOnly")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	for _, k := range []string{"GIT_CONFIG_PARAMETERS", "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "") // restored at cleanup
		_ = os.Unsetenv(k)
	}
}
