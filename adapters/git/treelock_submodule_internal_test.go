package git

// INIT-024 TASK-004 review round 1 (P4-5): the shared tree's obstruction
// check (untrackedObstruction) lists the paths a forced switch or a hard
// reset rewrites with `git diff`, which in a non-bare tree honours the
// repository's submodule ignore settings. With `ignore = all` a gitlink
// the switch would write was not listed, so an untracked file standing at
// its path was not reported and the switch could remove it. The diff now
// passes --ignore-submodules=none, as Diff does.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUntrackedObstruction_SubmoduleIgnoreSettingsHideNoGitlink(t *testing.T) {
	const gitmodules = "[submodule \"sub\"]\n\tpath = gov/sub\n\turl = ./sub\n"
	for _, tc := range []struct {
		name       string
		gitmodules string
		config     []string
	}{
		{"submodule.<name>.ignore = all", gitmodules + "\tignore = all\n", nil},
		{"diff.ignoreSubmodules = all", gitmodules, []string{"diff.ignoreSubmodules", "all"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			run := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
					"GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			run("init", "-q", "-b", "main")
			if err := os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte(tc.gitmodules), 0o644); err != nil {
				t.Fatal(err)
			}
			run("add", ".gitmodules")
			run("commit", "-qm", "c1")
			run("switch", "-q", "-c", "b")
			run("update-index", "--add", "--cacheinfo", "160000,1111111111111111111111111111111111111111,gov/sub")
			run("commit", "-qm", "gitlink")
			run("switch", "-q", "main")
			if len(tc.config) == 2 {
				run("config", tc.config[0], tc.config[1])
			}
			// An untracked file where the switch to b writes the gitlink.
			if err := os.MkdirAll(filepath.Join(dir, "gov"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "gov", "sub"), []byte("untracked work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := NewCLIClient(dir).untrackedObstruction(context.Background(), "test", "HEAD", "b")
			if err != nil || p == "" {
				t.Fatalf("untrackedObstruction(HEAD, b) = %q, %v; want the untracked file at the gitlink's path reported", p, err)
			}
		})
	}
}
