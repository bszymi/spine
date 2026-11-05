package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bszymi/spine/testutil"
)

// TestIdentityRefusal: git's refusals of an unresolvable identity, as the
// supported gits print them, count; any other failure does not.
func TestIdentityRefusal(t *testing.T) {
	const hint = "Committer identity unknown\n\n*** Please tell me who you are.\n\nRun\n\n" +
		"  git config --global user.email \"you@example.com\"\n  git config --global user.name \"Your Name\"\n\n" +
		"to set your account's default identity.\nOmit --global to set the identity only in this repository.\n\n"
	for _, tc := range []struct {
		name   string
		stderr string
		want   bool
	}{
		{"GitHub runner: empty GECOS name", hint + "fatal: empty ident name (for <runner@runnervm.internal.cloudapp.net>) not allowed", true},
		{"container user with no passwd entry (git 2.39)", hint + "fatal: unable to auto-detect email address (got 'unknown@7e85fd182d6d.(none)')", true},
		{"user.useConfigOnly", hint + "fatal: no email was given and auto-detection is disabled", true},
		{"a configured empty name (no hint)", "fatal: empty ident name (for <ops@example.test>) not allowed", true},
		{"a configured name of punctuation only", "fatal: name consists only of disallowed characters: <>", true},
		{"not a repository", "fatal: not a git repository (or any of the parent directories): .git", false},
		{"a broken config", "fatal: bad config line 1 in file .git/config", false},
		{"nothing", "", false},
	} {
		if got := identityRefusal(tc.stderr); got != tc.want {
			t.Errorf("%s: identityRefusal = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIdentityFallbackEnv_OnlyGitsRefusalFallsBack: the fallback names
// exactly the roles git refuses, and a probe that fails for any other
// reason adds nothing (the command then fails or succeeds on its own).
func TestIdentityFallbackEnv_OnlyGitsRefusalFallsBack(t *testing.T) {
	testutil.NoGitIdentity(t)
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	ctx := context.Background()
	both := committerRole | authorRole
	committer := []string{"GIT_COMMITTER_NAME=Spine", "GIT_COMMITTER_EMAIL=spine@spine.local"}
	author := []string{"GIT_AUTHOR_NAME=Spine", "GIT_AUTHOR_EMAIL=spine@spine.local"}

	if got, want := identityFallbackEnv(ctx, dir, nil, both), append(slices.Clone(committer), author...); !slices.Equal(got, want) {
		t.Errorf("no identity: %q, want %q", got, want)
	}
	if got := identityFallbackEnv(ctx, dir, nil, committerRole); !slices.Equal(got, committer) {
		t.Errorf("committer only: %q, want %q", got, committer)
	}
	// The command's own author variables resolve the author role.
	explicit := []string{"GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=alice@example.test"}
	if got := identityFallbackEnv(ctx, dir, explicit, both); !slices.Equal(got, committer) {
		t.Errorf("explicit author: %q, want only %q", got, committer)
	}
	// A configured name without an address is not an identity: the
	// fallback names both fields.
	if out, err := exec.Command("git", "-C", dir, "config", "user.name", "Ops").CombinedOutput(); err != nil {
		t.Fatalf("git config: %v %s", err, out)
	}
	if got := identityFallbackEnv(ctx, dir, nil, committerRole); !slices.Equal(got, committer) {
		t.Errorf("name without an address: %q, want %q", got, committer)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got := identityFallbackEnv(cancelled, dir, nil, both); got != nil {
		t.Errorf("cancelled probe: %q, want nil", got)
	}
	broken := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", broken).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(broken, ".git", "config"), []byte("[core\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := identityFallbackEnv(ctx, broken, nil, both); got != nil {
		t.Errorf("broken config: %q, want nil", got)
	}
}
