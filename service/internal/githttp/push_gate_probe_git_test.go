package githttp

// INIT-024 TASK-005 review round 1: the probe directory is invisible to
// git (P4-10), and the test-only probe override refuses outside a test
// binary (P4-5). These reach internals added in that round.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestStorageProbe_InvisibleToGit (review round 1, P4-10): the probe runs
// in refs/heads — where the protected branches' loose ref files live —
// and while its files are there `git for-each-ref`, `git pack-refs --all`
// (and the packed-refs it writes), `git ls-remote` and a clone of the
// repository see none of them; afterwards nothing is left. A reftable
// repository (git >= 2.45; skipped on older git) has a placeholder file
// at refs/heads, so the probe runs in refs/ there. The git-floor job runs
// this on git 2.39.
func TestStorageProbe_InvisibleToGit(t *testing.T) {
	t.Cleanup(func() { probeDirHookForTest = nil })
	check := func(t *testing.T, repo, wantParent string) {
		t.Helper()
		ran := false
		probeDirHookForTest = func(dir string) {
			ran = true
			if filepath.Dir(dir) != wantParent {
				t.Errorf("probe directory %s, want it under %s", dir, wantParent)
			}
			if ents, err := os.ReadDir(dir); err != nil || len(ents) == 0 {
				t.Errorf("probe files not in place: %v %v", ents, err)
			}
			for _, args := range [][]string{
				{"for-each-ref", "--format=%(refname)"},
				{"ls-remote", repo},
			} {
				if out := nulGit(t, repo, nil, args...); strings.Contains(out, "probe") {
					t.Errorf("git %v shows the probe: %s", args, out)
				}
			}
			nulGit(t, repo, nil, "pack-refs", "--all")
			if b, err := os.ReadFile(filepath.Join(repo, "packed-refs")); err == nil && strings.Contains(string(b), "probe") {
				t.Errorf("packed-refs holds the probe:\n%s", b)
			}
			clone := filepath.Join(t.TempDir(), "c")
			cmd := exec.Command("git", "clone", "-q", "--mirror", repo, clone)
			cmd.Env = nulGitEnv(t)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("clone during the probe: %v %s", err, out)
			} else if out := nulGit(t, clone, nil, "for-each-ref", "--format=%(refname)"); strings.Contains(out, "probe") {
				t.Errorf("the clone has the probe: %s", out)
			}
		}
		if _, err := probeRefStorage(context.Background(), repo); err != nil {
			t.Fatal(err)
		}
		if !ran {
			t.Fatal("the probe hook did not run")
		}
		ents, _ := os.ReadDir(wantParent)
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".spine-storage-probe-") {
				t.Errorf("probe directory left behind: %s", e.Name())
			}
		}
		if out := nulGit(t, repo, nil, "for-each-ref", "--format=%(refname)"); !strings.Contains(out, "refs/heads/main") {
			t.Errorf("refs after the probe: %s", out)
		}
	}

	t.Run("files backend", func(t *testing.T) {
		repo, base, _, _ := nulRepo(t, "")
		nulGit(t, repo, nil, "update-ref", "refs/heads/loose", base) // a loose ref beside the probe
		check(t, repo, filepath.Join(repo, "refs", "heads"))
	})
	t.Run("reftable", func(t *testing.T) {
		repo := t.TempDir()
		cmd := exec.Command("git", "init", "-q", "--bare", "--ref-format=reftable", "-b", "main", repo)
		cmd.Env = nulGitEnv(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("this git has no reftable format: %v %s", err, out)
		}
		work := t.TempDir()
		nulGit(t, work, nil, "init", "-q", "-b", "main")
		nulGit(t, work, nil, "commit", "-q", "--allow-empty", "-m", "base")
		nulGit(t, work, nil, "push", "-q", repo, "main")
		check(t, repo, filepath.Join(repo, "refs"))
	})
}

// TestFakeCaseSensitiveStorage_RefusedOutsideATestBinary (review round 1,
// P4-5): the test-only override of the case-sensitivity probe refuses,
// and changes nothing, in a process that is not a test binary. The check
// goes through isTestBinary (testing.Testing) so it can be tested in this
// binary.
func TestFakeCaseSensitiveStorage_RefusedOutsideATestBinary(t *testing.T) {
	prev := isTestBinary
	isTestBinary = func() bool { return false }
	t.Cleanup(func() { isTestBinary = prev })
	before := storageProbeOverride.Load()
	faked, err := FakeCaseSensitiveStorageOnFoldingHostForTests()
	if faked || !errors.Is(err, errNotATestBinary) {
		t.Fatalf("outside a test binary: faked=%v err=%v, want errNotATestBinary", faked, err)
	}
	if storageProbeOverride.Load() != before {
		t.Fatal("the refused call changed the probe")
	}
}
