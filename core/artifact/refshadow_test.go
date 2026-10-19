package artifact_test

// INIT-023 TASK-002 tests for the artifact service under the git client's
// ref grammar: List resolves its ref once (DC-7, AC 18), and the
// write_context auto-push lands on the branch even when a same-named tag
// exists (AC 9).

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/testutil"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func taskDoc(n int) string {
	return fmt.Sprintf("---\nid: TASK-%03d\ntype: Task\ntitle: Task %d\nstatus: Pending\nepic: /initiatives/init-001/epics/epic-001/epic.md\ninitiative: /initiatives/init-001/initiative.md\n---\n# Task %d\n", n, n, n)
}

// TestList_ResolvesTheRefOnce is AC 18: List(ctx, <branch>) runs exactly
// one exact branch lookup and N blob reads — each after its entry probe
// (`ls-tree -l` of the path at that one object id; INIT-023 wave 4
// follow-up, which replaced TASK-003's `cat-file -s` probe and the `git
// show` read) and its object-type check (`cat-file -t <oid>`, review of
// that follow-up) — and returns the same artifacts as listing the branch
// checked out (the pre-change behaviour).
func TestList_ResolvesTheRefOnce(t *testing.T) {
	testutil.HermeticGit(t)
	repo := testutil.NewTempRepo(t)
	branch := "spine/plan/init-001-list-0a1b2c3d"
	gitOut(t, repo, "switch", "-q", "-c", branch)
	const n = 5
	for i := 1; i <= n; i++ {
		testutil.WriteFile(t, repo, fmt.Sprintf("tasks/task-%03d.md", i), taskDoc(i))
	}
	testutil.WriteFile(t, repo, "notes.txt", "not markdown\n")
	gitOut(t, repo, "add", ".")
	gitOut(t, repo, "commit", "-qm", "five tasks")
	tip := gitOut(t, repo, "rev-parse", "HEAD")
	// A same-named tag on main must not change what List sees.
	gitOut(t, repo, "tag", branch, "main")

	client := git.NewCLIClient(repo)
	svc := artifact.NewService(client, event.NewQueueRouter(queue.NewMemoryQueue(10)), repo)
	svc.WithPolicy(branchprotect.NewPermissive())

	rec := testutil.InstallGitRecorder(t)
	rec.Reset(t)
	got, err := svc.List(context.Background(), branch)
	if err != nil {
		t.Fatalf("List(%s): %v", branch, err)
	}
	calls := rec.Calls(t)

	lookups, shows, probes, types, other := 0, 0, 0, 0, 0
	for _, c := range calls {
		switch c[0] {
		case "--literal-pathspecs":
			probes++
			if want := []string{"--literal-pathspecs", "ls-tree", "-l", "-z", "--full-tree", "--end-of-options", tip}; len(c) != len(want)+1 ||
				strings.Join(c[:len(want)], " ") != strings.Join(want, " ") || !strings.HasPrefix(c[len(want)], "tasks/") {
				t.Errorf("probe argv %q, want %q <path>", c, want)
			}
		case "cat-file":
			if len(c) == 3 && c[1] == "-t" && git.IsFullObjectName(c[2]) {
				types++
				continue
			}
			shows++
			if len(c) != 3 || c[1] != "blob" || !git.IsFullObjectName(c[2]) {
				t.Errorf("blob read argv %q, want cat-file blob <oid>", c)
			}
		case "show-ref":
			lookups++
			if want := []string{"show-ref", "--verify", "--hash", "refs/heads/" + branch}; strings.Join(c, " ") != strings.Join(want, " ") {
				t.Errorf("lookup argv %q, want %q", c, want)
			}
		case "ls-tree":
			if c[len(c)-1] != tip {
				t.Errorf("ls-tree argv %q, want the resolved id %s", c, tip)
			}
		default:
			other++
			t.Errorf("unexpected git call %q", c)
		}
	}
	if lookups != 1 || shows != n || probes != n || types != n {
		t.Fatalf("List ran %d lookups, %d probes, %d object-type checks and %d blob reads (calls %q), want 1, %d, %d and %d",
			lookups, probes, types, shows, calls, n, n, n)
	}

	// Same answer as listing the checked-out branch through HEAD.
	want, err := svc.List(context.Background(), "HEAD")
	if err != nil {
		t.Fatalf("List(HEAD): %v", err)
	}
	if g, w := artifactKeys(got), artifactKeys(want); strings.Join(g, ",") != strings.Join(w, ",") || len(g) != n {
		t.Fatalf("List(branch) = %v, want %v (%d artifacts)", g, w, n)
	}
}

func artifactKeys(as []*domain.Artifact) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.ID+"@"+a.Path)
	}
	sort.Strings(out)
	return out
}

// TestCreate_AutoPushWithShadowTag is AC 9's artifact auto-push case: a
// Create with write_context on a branch that also has a same-named tag
// commits on the branch (the worktree attaches to refs/heads/<n>) and
// pushes that commit to the remote's refs/heads/<n>. At 61994fe
// CurrentBranch returned "heads/<n>" in this situation.
func TestCreate_AutoPushWithShadowTag(t *testing.T) {
	testutil.HermeticGit(t)
	svc, _, repo, _ := newTestService(t)
	bare := addBareRemote(t, repo)
	branch := "spine/run/task-001-shadow-0a1b2c3d"
	gitOut(t, repo, "branch", branch)
	gitOut(t, repo, "push", "-q", "origin", "refs/heads/"+branch+":refs/heads/"+branch)
	gitOut(t, repo, "tag", branch, "main")

	ctx := artifact.WithWriteContext(testCtx(), artifact.WriteContext{Branch: branch})
	res, err := svc.Create(ctx, "governance/shadow.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := gitOut(t, bare, "rev-parse", "refs/heads/"+branch); got != res.CommitSHA {
		t.Fatalf("remote refs/heads/%s = %s, want the new commit %s", branch, got, res.CommitSHA)
	}
	if got := gitOut(t, repo, "rev-parse", "refs/tags/"+branch); got == res.CommitSHA {
		t.Fatal("the commit landed on the tag")
	}
	if out := gitOut(t, bare, "for-each-ref", "refs/heads/heads"); out != "" {
		t.Fatalf("a heads/<n> branch was pushed: %s", out)
	}
}
