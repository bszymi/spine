package git_test

// INIT-024 TASK-004 (carried from SMP INIT-027 EPIC-004 TASK-010's
// review): in a non-bare working tree `git diff <a> <b>` honours
// `submodule.<name>.ignore` from the checked-out .gitmodules and
// `diff.ignoreSubmodules` from any config, so `ignore = all` made a gitlink
// change at a governed path invisible to every Diff caller — the
// governance guard, the projection, the status cascade. Diff passes
// --ignore-submodules=none, so a gitlink change is always "modified".

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

const (
	gitlinkPath = "governed/sub"
	gitlinkOld  = "1111111111111111111111111111111111111111"
	gitlinkNew  = "2222222222222222222222222222222222222222"
)

// gitlinkIgnoreRepo commits a gitlink at gitlinkPath and then moves it,
// in a non-bare tree whose checked-out .gitmodules carries gitmodules
// (and whose repository config carries config, "key=value" pairs).
func gitlinkIgnoreRepo(t *testing.T, gitmodules string, config ...string) (dir, c1, c2 string) {
	t.Helper()
	dir = t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte(gitmodules), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "a.md", []byte("a\n"))
	t3git(t, dir, nil, "add", ".gitmodules", "a.md")
	t3git(t, dir, nil, "update-index", "--add", "--cacheinfo", "160000,"+gitlinkOld+","+gitlinkPath)
	t3git(t, dir, nil, "commit", "-qm", "c1")
	c1 = t3git(t, dir, nil, "rev-parse", "HEAD")
	t3git(t, dir, nil, "update-index", "--cacheinfo", "160000,"+gitlinkNew+","+gitlinkPath)
	t3git(t, dir, nil, "commit", "-qm", "c2")
	c2 = t3git(t, dir, nil, "rev-parse", "HEAD")
	for _, kv := range config {
		k, v, _ := strings.Cut(kv, "=")
		t3git(t, dir, nil, "config", k, v)
	}
	return dir, c1, c2
}

var gitlinkIgnoreCases = []struct {
	name       string
	gitmodules string
	config     []string
}{
	{"submodule.<name>.ignore = all in .gitmodules",
		"[submodule \"sub\"]\n\tpath = " + gitlinkPath + "\n\turl = ./sub\n\tignore = all\n", nil},
	{"diff.ignoreSubmodules = all in the repository config",
		"[submodule \"sub\"]\n\tpath = " + gitlinkPath + "\n\turl = ./sub\n", []string{"diff.ignoreSubmodules=all"}},
}

func TestDiff_SubmoduleIgnoreSettingsHideNoGitlinkChange(t *testing.T) {
	for _, tc := range gitlinkIgnoreCases {
		t.Run(tc.name, func(t *testing.T) {
			dir, c1, c2 := gitlinkIgnoreRepo(t, tc.gitmodules, tc.config...)
			// The fixture's premise: plain `git diff` hides the change here.
			plain := exec.Command("git", "diff", "--name-status", "-z", c1, c2, "--")
			plain.Dir = dir
			plain.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
			if out, err := plain.Output(); err != nil || len(out) != 0 {
				t.Fatalf("fixture: plain git diff printed %q (%v); want nothing (the setting hides the gitlink)", out, err)
			}
			diffs, err := git.NewCLIClient(dir).Diff(context.Background(), c1, c2)
			if err != nil {
				t.Fatal(err)
			}
			if len(diffs) != 1 || diffs[0] != (git.FileDiff{Path: gitlinkPath, Status: "modified"}) {
				t.Fatalf("Diff = %+v, want the gitlink at %s modified", diffs, gitlinkPath)
			}
		})
	}
}
