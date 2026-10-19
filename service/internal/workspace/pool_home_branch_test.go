package workspace

import (
	"context"
	"os/exec"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/engine"
)

// INIT-023 TASK-012 AC 19: a workspace's primary client is the shared
// working tree with the home branch, and the options it is built from
// leave the code-repository clients' base untouched.
func TestBuildServiceSet_PrimaryClientHasHomeBranch(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "c"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	ss, err := buildServiceSet(context.Background(), Config{ID: "ws-home", RepoPath: repo}, nil, nil, "", nil, PoolPolicy{}, "")
	if err != nil {
		t.Fatalf("buildServiceSet: %v", err)
	}
	defer ss.close("shutdown")
	if got := ss.GitClient.HomeBranch(); got != engine.AuthoritativeBranch {
		t.Fatalf("primary client home branch = %q, want %q", got, engine.AuthoritativeBranch)
	}
	if pc, ok := ss.GitPool.PrimaryClient().(*git.CLIClient); !ok || pc.HomeBranch() != engine.AuthoritativeBranch {
		t.Fatal("GitPool.PrimaryClient is not the home-branch client")
	}

	base := make([]git.CLIOption, 1, 8)
	base[0] = git.WithPushEnv("X=1")
	opts := PrimaryClientOptions(base)
	if len(opts) != 2 || len(base) != 1 || cap(base) != 8 {
		t.Fatalf("PrimaryClientOptions: len %d (base %d)", len(opts), len(base))
	}
	if extra := base[:cap(base)][1]; extra != nil {
		t.Fatal("PrimaryClientOptions wrote into the base slice's spare capacity (code-repo clients would inherit the home branch)")
	}
	if c := git.NewCLIClient(t.TempDir(), base...); c.HomeBranch() != "" {
		t.Fatalf("a client built from the base options got home branch %q", c.HomeBranch())
	}
}
