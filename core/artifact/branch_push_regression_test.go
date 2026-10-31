package artifact_test

// INIT-024 TASK-011 review round 1 (P4-3): a branch-scoped write pushed to
// origin inside its write scope, so the branch's claim (and, with a
// ceiling, its in-flight checkout weight) was held across a network push
// with no deadline of its own. It now reads the branch inside the scope,
// releases the scope, then pushes. The test uses only API that existed at
// e92b45c4, where it fails (the push ran while the branch was claimed).

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/testutil"
)

// pushProbe is the service's git client with a hook run as each push
// starts.
type pushProbe struct {
	git.GitClient
	onPush func(ref string)
}

func (p *pushProbe) Push(ctx context.Context, remote, ref string) error {
	p.onPush(ref)
	return p.GitClient.Push(ctx, remote, ref)
}

func TestCreate_PushStartsAfterTheBranchScopeIsReleased(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "")
	repo := testutil.NewTempRepo(t)
	bare := addBareRemote(t, repo)
	run := "spine/run/push-1a2b3c4d"
	claimGit(t, repo, nil, "branch", run, "main")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	var mu sync.Mutex
	var pushed []string
	var held []string
	probe := &pushProbe{GitClient: git.NewCLIClient(repo), onPush: func(ref string) {
		mu.Lock()
		defer mu.Unlock()
		pushed = append(pushed, ref)
		// The write scope must be gone: its worktree removed and the
		// branch free for the next writer.
		entries, _ := os.ReadDir(tmp)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "spine-wt-parent-") {
				held = append(held, "worktree "+e.Name()+" still on disk")
			}
		}
		if s, err := git.EnterBranch(context.Background(), repo, run, nil); err != nil {
			held = append(held, "branch still claimed: "+err.Error())
		} else {
			s.Cleanup()
		}
	}}
	svc := artifact.NewService(probe, &captureEvents{}, repo)
	svc.WithPolicy(branchprotect.New(branchprotect.StaticRules([]config.Rule{})))

	ctx := artifact.WithWriteContext(operatorCtx(), artifact.WriteContext{Branch: run})
	res, err := svc.Create(ctx, "governance/pushed.md", governanceContent)
	if err != nil {
		t.Fatalf("Create on the run branch: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(pushed) != 1 || pushed[0] != run {
		t.Fatalf("pushes = %q, want one push of %s", pushed, run)
	}
	for _, h := range held {
		t.Errorf("the push started while the write scope was held: %s", h)
	}
	// The push itself is unchanged: the commit is on the remote's branch.
	if got := claimGit(t, bare, nil, "rev-parse", "refs/heads/"+run); got != res.CommitSHA {
		t.Errorf("remote %s = %s, want the write's commit %s", run, got, res.CommitSHA)
	}
}
