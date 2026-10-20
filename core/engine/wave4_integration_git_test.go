package engine_test

// INIT-023 wave 4 integration on real git: every refusal after the merge
// moved the local main — TASK-005's verifyBoundMerge (TASK-012's own
// TestF9_RefusedBoundMergeNeverVisibleToNextHolder), TASK-017's V1 and
// TASK-006's post-merge assertion — is taken back through
// git.CLIClient.ResetBranch (armed, tree-locked) inside TASK-012's merge
// block, before the shared tree is released: an in-place write that
// waited for the tree lands on the base, and origin main never receives
// the refused merge, also when the first take-back attempt fails.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
)

// lateRefusalCLI is the shared client with a refusal injected after
// verifyBoundMerge accepted the merge: for TASK-006 the merge result's
// diff names a governed path the evaluation never saw, for TASK-017 the
// merge result's TaskPath carries a status the lifecycle did not plan.
// While the merge block holds the tree it starts an in-place artifact
// write, which must wait; failTakeBack holds refs/heads/main.lock during
// the first take-back (git's detached auto-gc running pack-refs).
type lateRefusalCLI struct {
	*git.CLIClient
	kind string // "governance" | "lifecycle"
	dir  string

	mu           sync.Mutex
	merge        git.MergeResult
	onMerged     func()
	failTakeBack bool
	takeBackErr  error
	takeBacks    int
}

func (c *lateRefusalCLI) result() git.MergeResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.merge
}

func (c *lateRefusalCLI) MergeBound(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	res, err := c.CLIClient.MergeBound(ctx, opts)
	if err == nil {
		c.mu.Lock()
		first := c.merge.SHA == ""
		c.merge = res
		hook := c.onMerged
		c.mu.Unlock()
		if first && hook != nil {
			hook()
		}
	}
	return res, err
}

func (c *lateRefusalCLI) Diff(ctx context.Context, a, b string) ([]git.FileDiff, error) {
	d, err := c.CLIClient.Diff(ctx, a, b)
	if m := c.result(); err == nil && c.kind == "governance" && m.SHA != "" && a == m.BaseSHA && b == m.SHA {
		d = append(d, git.FileDiff{Path: ".spine/branch-protection.yaml", Status: "modified"})
	}
	return d, err
}

func (c *lateRefusalCLI) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
	if m := c.result(); c.kind == "lifecycle" && m.SHA != "" && ref == m.SHA && path == "tasks/t.md" {
		return []byte(strings.Replace(taskPending, "status: Pending", "status: Cancelled", 1)), nil
	}
	return c.CLIClient.ReadFile(ctx, ref, path)
}

func (c *lateRefusalCLI) ResetBranch(ctx context.Context, branch, to, expected string) error {
	c.mu.Lock()
	c.takeBacks++
	fail := c.failTakeBack && c.takeBacks == 1
	c.mu.Unlock()
	if !fail {
		return c.CLIClient.ResetBranch(ctx, branch, to, expected)
	}
	lock := filepath.Join(c.dir, ".git", "refs", "heads", branch+".lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		return err
	}
	err := c.CLIClient.ResetBranch(ctx, branch, to, expected)
	_ = os.Remove(lock)
	c.mu.Lock()
	c.takeBackErr = err
	c.mu.Unlock()
	return err
}

func TestWave4_LateRefusalsAreTakenBackUnderTheTreeLock(t *testing.T) {
	const doc = "---\ntype: Governance\ntitle: Concurrent\nstatus: Living Document\nversion: \"0.1\"\n---\n# Concurrent\n"
	for _, kind := range []string{"governance", "lifecycle"} {
		for _, failFirst := range []bool{false, true} {
			name := kind + map[bool]string{false: "", true: ", first take-back fails"}[failFirst]
			t.Run(name, func(t *testing.T) {
				t.Setenv("SPINE_GIT_AUTO_PUSH", "") // artifact auto-push follows SPINE_GIT_AUTO_PUSH (on)
				lc := &lateRefusalCLI{kind: kind, failTakeBack: failFirst}
				e := newRetryEnv(t, reviewWorkflow(), true, func(c *git.CLIClient) engine.GitOperator {
					lc.CLIClient = c
					return lc
				}, nil)
				lc.dir = e.dir
				switch kind {
				case "governance":
					if err := e.orch.WithGovernanceGuard(engine.GovernanceGuard{Enabled: true}); err != nil {
						t.Fatalf("WithGovernanceGuard: %v", err)
					}
				case "lifecycle":
					e.orch.WithStatusLifecycle(domain.DefaultStatusLifecycle())
				}
				svc := artifact.NewService(git.NewCLIClient(e.dir), nil, e.dir)
				svc.WithPolicy(branchprotect.NewPermissive())
				writeDone := make(chan error, 1)
				var waited bool
				lc.onMerged = func() {
					go func() {
						_, err := svc.Create(context.Background(), "governance/concurrent.md", doc)
						writeDone <- err
					}()
					select {
					case err := <-writeDone:
						writeDone <- err
					case <-time.After(400 * time.Millisecond):
						waited = true
					}
				}
				e.commitOnRun(map[string]string{"deliverable.md": "work\n"})
				e.submit(0, "completed")
				base := regGit(t, e.dir, "rev-parse", "refs/heads/main")
				e.submit(1, "accepted") // the bound merge runs here and is refused after it moved main

				m := lc.result()
				if m.SHA == "" || m.BaseSHA != base {
					t.Fatalf("merge = %+v, want one computed on %s", m, base)
				}
				if !waited {
					t.Fatal("the in-place write did not wait for the merge block")
				}
				if err := <-writeDone; err != nil {
					t.Fatalf("in-place write: %v", err)
				}
				write := regGit(t, e.dir, "log", "-1", "--format=%H", "refs/heads/main", "--", "governance/concurrent.md")
				if parent := regGit(t, e.dir, "rev-parse", write+"^"); parent != base {
					t.Fatalf("the in-place write's parent is %s, want the base %s (the refused merge is %s)", parent, base, m.SHA)
				}
				if err := exec.Command("git", "-C", e.dir, "merge-base", "--is-ancestor", m.SHA, "refs/heads/main").Run(); err == nil {
					t.Fatal("local main contains the refused merge")
				}
				if err := exec.Command("git", "-C", e.origin, "merge-base", "--is-ancestor", m.SHA, "refs/heads/main").Run(); err == nil {
					t.Fatal("origin main contains the refused merge")
				}
				lc.mu.Lock()
				takeBackErr := lc.takeBackErr
				lc.mu.Unlock()
				if failFirst && takeBackErr == nil {
					t.Fatal("the injected take-back failure did not happen")
				}
				o := e.primary()
				want := map[string]string{"governance": "merge changed governed paths not covered", "lifecycle": "merge_result_mismatch"}[kind]
				if failFirst {
					want = "take-back failed"
					if kind == "governance" {
						want = "could not be taken back"
					}
				}
				if o.Status != domain.RepositoryMergeStatusFailed || !strings.Contains(o.FailureDetail, want) {
					t.Fatalf("primary outcome = %+v, want a failure naming %q", o, want)
				}
				run := e.runNow()
				switch {
				case kind == "governance" && failFirst:
					// TASK-006 keeps its run committing with the block on it;
					// the block's exit restore already finished the take-back
					// (TASK-012), so the run's next attempt clears the block.
					if run.Status != domain.RunStatusCommitting || run.CommitMeta[engine.CommitMetaGovernanceBlockedTarget] != m.SHA+" "+base {
						t.Fatalf("run %s, block %q; want committing with the block recorded", run.Status, run.CommitMeta[engine.CommitMetaGovernanceBlockedTarget])
					}
					_ = e.orch.MergeRunBranch(context.Background(), e.run.RunID)
					if got := e.runNow().CommitMeta[engine.CommitMetaGovernanceBlockedTarget]; got != "" {
						t.Fatalf("block not cleared after the take-back completed: %q", got)
					}
				default:
					if run.Status != domain.RunStatusFailed {
						t.Fatalf("run = %s, want failed (the permanent route of a review-only workflow)", run.Status)
					}
				}
				if st := regGit(t, e.dir, "status", "--porcelain", "--untracked-files=no"); st != "" || regGit(t, e.dir, "symbolic-ref", "HEAD") != "refs/heads/main" {
					t.Fatalf("the shared tree was not left clean on main: %q", st)
				}
			})
		}
	}
}
