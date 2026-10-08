package engine

// INIT-023 TASK-002, audit F10 regression tests through the real
// governed merge (MergeRunBranch) with a real *git.CLIClient, derived
// from the audit PoCs gap0-ref-namespace-shadow and verify-r2-0 phase 2,
// inverted. The attacker plants refs with a plain `git push` into the
// engine repository, so these tests hold whether or not push admission
// (TASK-016) refuses such refs. Repositories are hermetic
// (testutil.HermeticGit): the audit PoC depended on the host's git
// identity and failed in a clean container for that reason alone.

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/testutil"
)

const (
	rsTaskReviewed = "---\nid: TASK-001\ntype: Task\ntitle: t\nstatus: Pending\n---\n# reviewed body\n"
	rsTaskAttacker = "---\nid: TASK-001\ntype: Task\ntitle: t\nstatus: Pending\n---\n# ATTACKER BODY\n"
	rsAttackerText = "UNREVIEWED ATTACKER CONTENT\n"
)

// rsEnv is the standalone deployment shape: the engine's CLIClient works
// on a repository that pushers can write refs into, with a bare origin
// the governed merge pushes to, and an attacker clone of origin.
type rsEnv struct {
	root, origin, repo, attacker string
}

// rsGit runs a setup git command and returns its trimmed stdout (stderr
// warnings such as "refname is ambiguous" are kept out of the value).
func rsGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v\n%s%s", args, err, out, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func rsIsAncestor(dir, a, b string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", a, b)
	cmd.Dir = dir
	return cmd.Run() == nil
}

func newRSEnv(t *testing.T) *rsEnv {
	t.Helper()
	testutil.HermeticGit(t)
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	root := t.TempDir()
	e := &rsEnv{
		root:     root,
		origin:   filepath.Join(root, "origin.git"),
		repo:     filepath.Join(root, "engine"),
		attacker: filepath.Join(root, "attacker"),
	}
	rsGit(t, root, "init", "-q", "--bare", "-b", "main", e.origin)
	rsGit(t, root, "init", "-q", "-b", "main", e.repo)
	rsGit(t, e.repo, "remote", "add", "origin", e.origin)
	testutil.WriteFile(t, e.repo, "tasks/t.md", rsTaskReviewed)
	testutil.WriteFile(t, e.repo, "f.txt", "legit\n")
	rsGit(t, e.repo, "add", ".")
	rsGit(t, e.repo, "commit", "-qm", "base")
	rsGit(t, e.repo, "push", "-q", "origin", "refs/heads/main:refs/heads/main")
	rsGit(t, root, "clone", "-q", e.origin, e.attacker)
	return e
}

// runBranch cuts a run branch from main in the engine repository with
// one reviewed commit writing files, pushes it to origin and returns
// its tip.
func (e *rsEnv) runBranch(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	mainTip := rsGit(t, e.repo, "show-ref", "--verify", "--hash", "refs/heads/main")
	rsGit(t, e.repo, "switch", "-q", "-c", name, mainTip)
	for p, c := range files {
		testutil.WriteFile(t, e.repo, p, c)
	}
	rsGit(t, e.repo, "add", ".")
	rsGit(t, e.repo, "commit", "-qm", "reviewed work on "+name)
	tip := rsGit(t, e.repo, "rev-parse", "HEAD")
	rsGit(t, e.repo, "push", "-q", "origin", "refs/heads/"+name+":refs/heads/"+name)
	rsGit(t, e.repo, "switch", "-q", "main")
	return tip
}

// plantAttacker commits unreviewed content in the attacker clone, on
// top of origin's main, and pushes it into the ENGINE repository as ref
// (the git smart-HTTP endpoint serves that repository in standalone).
func (e *rsEnv) plantAttacker(t *testing.T, ref string) string {
	t.Helper()
	rsGit(t, e.attacker, "fetch", "-q", "origin")
	rsGit(t, e.attacker, "switch", "-q", "--detach", "origin/main")
	testutil.WriteFile(t, e.attacker, "f.txt", rsAttackerText)
	testutil.WriteFile(t, e.attacker, "tasks/t.md", rsTaskAttacker)
	rsGit(t, e.attacker, "add", ".")
	rsGit(t, e.attacker, "commit", "-qm", "attacker")
	sha := rsGit(t, e.attacker, "rev-parse", "HEAD")
	rsGit(t, e.attacker, "push", "-q", e.repo, sha+":"+ref)
	return sha
}

func (e *rsEnv) orchestrator(run *domain.Run) *Orchestrator {
	return &Orchestrator{
		store:    &mockRunStore{runs: map[string]*domain.Run{run.RunID: run}},
		git:      git.NewCLIClient(e.repo),
		events:   &mockEventEmitter{},
		wfLoader: &stubWorkflowLoader{},
		policy:   branchprotect.New(branchprotect.StaticRules(nil)),
	}
}

// TestRefShadow_MergeRunBranchIgnoresShadowRef is AC 1: a tag or ref
// named after the victim's run branch no longer makes the governed merge
// publish the attacker's commit. At 61994fe the run completed with the
// tag's content on main and the reviewed tip nowhere on main.
func TestRefShadow_MergeRunBranchIgnoresShadowRef(t *testing.T) {
	for _, prefix := range []string{"refs/tags/", "refs/"} {
		t.Run(prefix+"<run-branch>", func(t *testing.T) {
			e := newRSEnv(t)
			branch := "spine/run/task-001-victim-0a1b2c3d"
			reviewed := e.runBranch(t, branch, map[string]string{"f.txt": "reviewed change\n"})
			attacker := e.plantAttacker(t, prefix+branch)

			run := committingRun("run-0a1b2c3d", branch)
			if err := e.orchestrator(run).MergeRunBranch(context.Background(), run.RunID); err != nil {
				t.Fatalf("MergeRunBranch: %v", err)
			}
			if run.Status != domain.RunStatusCompleted {
				t.Fatalf("run status = %s, want completed", run.Status)
			}
			if got := rsGit(t, e.origin, "show", "refs/heads/main:f.txt"); got != "reviewed change" {
				t.Fatalf("origin main:f.txt = %q, want the reviewed content", got)
			}
			if !rsIsAncestor(e.origin, reviewed, "refs/heads/main") {
				t.Fatal("the reviewed run-branch tip is not on origin main")
			}
			if rsIsAncestor(e.origin, attacker, "refs/heads/main") {
				t.Fatal("the attacker commit reached origin main")
			}
			if got := rsGit(t, e.origin, "rev-parse", "refs/heads/main^2"); got != reviewed {
				t.Fatalf("merge commit second parent = %s, want the reviewed tip %s", got, reviewed)
			}
		})
	}
}

// TestRefShadow_MergeRunBranchMissingBranchShadow is AC 1's DC-2 case:
// with the run branch deleted and refs/tags/refs/heads/<run> planted (a
// qualified name git DWIM-resolves when the branch is missing), neither
// the adapter nor the governed merge picks up the attacker commit.
func TestRefShadow_MergeRunBranchMissingBranchShadow(t *testing.T) {
	e := newRSEnv(t)
	branch := "spine/run/task-001-victim-0a1b2c3d"
	e.runBranch(t, branch, map[string]string{"f.txt": "reviewed change\n"})
	rsGit(t, e.repo, "branch", "-q", "-D", branch)
	attacker := e.plantAttacker(t, "refs/tags/refs/heads/"+branch)
	before := rsGit(t, e.origin, "rev-parse", "refs/heads/main")

	_, err := git.NewCLIClient(e.repo).Merge(context.Background(), git.MergeOpts{
		Source: branch, Target: "main", Strategy: "merge-commit", Message: "Merge run",
	})
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindNotFound || ge.IsRetryable() {
		t.Fatalf("CLIClient.Merge(missing run branch) = %v, want a non-retryable ErrKindNotFound", err)
	}

	run := committingRun("run-0a1b2c3d", branch)
	_ = e.orchestrator(run).MergeRunBranch(context.Background(), run.RunID)
	if rsIsAncestor(e.origin, attacker, "refs/heads/main") || rsIsAncestor(e.repo, attacker, "refs/heads/main") {
		t.Fatal("the attacker commit reached main through a missing run branch")
	}
	if got := rsGit(t, e.origin, "rev-parse", "refs/heads/main"); got != before {
		t.Fatalf("origin main moved to %s", got)
	}
	if run.Status == domain.RunStatusCompleted {
		t.Fatal("a run whose branch is missing completed")
	}
}

// TestRefShadow_ShadowedMainDoesNotBlockGovernedMerges is AC 2: with a
// refs/tags/main (the original F10 denial of service: every push failed
// "src refspec main matches more than one", classified permanent) or a
// doubly-qualified local shadow of main (DC-1) in the engine repository,
// two independent runs both complete and origin main advances twice.
// The refs/tags/<local main oid> case of DC-1 needs the shadow planted
// between the merge and the push: TestRefShadow_ObjectIDShadowOfMainAtPushTime
// (and adapters/git TestRefShadow_PushIgnoresLocalShadows).
func TestRefShadow_ShadowedMainDoesNotBlockGovernedMerges(t *testing.T) {
	for _, shadow := range []string{"refs/tags/main", "refs/tags/refs/heads/main", "refs/refs/heads/main", "refs/heads/refs/heads/main"} {
		t.Run(shadow, func(t *testing.T) {
			e := newRSEnv(t)
			attacker := e.plantAttacker(t, shadow)
			b1 := "spine/run/task-001-first-0a1b2c3d"
			b2 := "spine/run/task-002-second-1a2b3c4d"
			e.runBranch(t, b1, map[string]string{"one.txt": "one\n"})
			e.runBranch(t, b2, map[string]string{"two.txt": "two\n"})

			prev := rsGit(t, e.origin, "rev-parse", "refs/heads/main")
			for i, b := range []string{b1, b2} {
				run := committingRun("run-"+[]string{"0a1b2c3d", "1a2b3c4d"}[i], b)
				if err := e.orchestrator(run).MergeRunBranch(context.Background(), run.RunID); err != nil {
					t.Fatalf("MergeRunBranch(%s): %v", b, err)
				}
				if run.Status != domain.RunStatusCompleted {
					t.Fatalf("run %s status = %s, want completed", b, run.Status)
				}
				cur := rsGit(t, e.origin, "rev-parse", "refs/heads/main")
				if cur == prev || !rsIsAncestor(e.origin, prev, cur) {
					t.Fatalf("origin main did not advance for %s (%s -> %s)", b, prev, cur)
				}
				prev = cur
			}
			if rsIsAncestor(e.origin, attacker, "refs/heads/main") {
				t.Fatal("the attacker commit reached origin main")
			}
			testutil.WriteFile(t, e.repo, "three.txt", "three\n")
			rsGit(t, e.repo, "add", "three.txt")
			rsGit(t, e.repo, "commit", "-qm", "local advance")
			if err := git.NewCLIClient(e.repo).Push(context.Background(), "origin", "main"); err != nil {
				t.Fatalf("CLIClient.Push(origin, main) with %s: %v", shadow, err)
			}
			if got, want := rsGit(t, e.origin, "rev-parse", "refs/heads/main"), rsGit(t, e.repo, "rev-parse", "refs/heads/main"); got != want {
				t.Fatalf("origin main = %s, want local main %s", got, want)
			}
		})
	}
}

// TestRefShadow_StatusCascadeReadsTheBranch is AC 3: the commit-status
// cascade reads the run branch, not a same-named tag. Qualifying only
// the merge source and the push is not enough: at 61994fe the cascade
// read tasks/t.md from the shadow tag, rewrote its status, committed it
// onto the real branch, and the merge carried the attacker's body to
// main.
func TestRefShadow_StatusCascadeReadsTheBranch(t *testing.T) {
	e := newRSEnv(t)
	branch := "spine/run/task-001-victim-0a1b2c3d"
	e.runBranch(t, branch, map[string]string{
		"tasks/t.md": strings.Replace(rsTaskReviewed, "# reviewed body", "# reviewed body, edited on the run", 1),
	})
	e.plantAttacker(t, "refs/tags/"+branch)

	run := committingRun("run-0a1b2c3d", branch)
	run.CommitMeta = map[string]string{"status": "Completed"}
	if err := e.orchestrator(run).MergeRunBranch(context.Background(), run.RunID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}
	got := rsGit(t, e.origin, "show", "refs/heads/main:tasks/t.md")
	if strings.Contains(got, "ATTACKER") {
		t.Fatalf("the shadow tag's body reached main through the status cascade:\n%s", got)
	}
	if !strings.Contains(got, "# reviewed body, edited on the run") || !strings.Contains(got, "status: Completed") {
		t.Fatalf("main:tasks/t.md = %q, want the reviewed body with status: Completed", got)
	}
}

// shadowBeforePush plants refs/tags/<object id of local main> on an
// attacker commit at the last moment before the governed merge pushes
// main: the merged object id does not exist before the merge, so this is
// the only way to put the DC-1 "raw object id as push source" shadow in
// front of the real governed push.
type shadowBeforePush struct {
	*git.CLIClient
	t       *testing.T
	repo    string
	evil    string
	planted []string
}

func (s *shadowBeforePush) Push(ctx context.Context, remote, ref string) error {
	oid := rsGit(s.t, s.repo, "show-ref", "--verify", "--hash", "refs/heads/"+ref)
	rsGit(s.t, s.repo, "update-ref", "refs/tags/"+oid, s.evil)
	s.planted = append(s.planted, oid)
	return s.CLIClient.Push(ctx, remote, ref)
}

// TestRefShadow_ObjectIDShadowOfMainAtPushTime is AC 2's fourth DC-1
// case through the governed merge (review round 1; it was pinned only at
// the adapter level): with refs/tags/<merged main oid> on an attacker
// commit built on origin main, a raw "<oid>:refs/heads/main" push source
// would send the attacker commit as a fast-forward. The run completes,
// origin main is the merge the engine made, and the attacker commit is
// not on it.
func TestRefShadow_ObjectIDShadowOfMainAtPushTime(t *testing.T) {
	e := newRSEnv(t)
	attacker := e.plantAttacker(t, "refs/attacker/parking")
	branch := "spine/run/task-001-victim-0a1b2c3d"
	reviewed := e.runBranch(t, branch, map[string]string{"f.txt": "reviewed change\n"})

	run := committingRun("run-0a1b2c3d", branch)
	orch := e.orchestrator(run)
	sp := &shadowBeforePush{CLIClient: git.NewCLIClient(e.repo), t: t, repo: e.repo, evil: attacker}
	orch.git = sp
	if err := orch.MergeRunBranch(context.Background(), run.RunID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}
	if run.Status != domain.RunStatusCompleted {
		t.Fatalf("run status = %s, want completed", run.Status)
	}
	if len(sp.planted) == 0 {
		t.Fatal("the governed merge never pushed main; the shadow was not exercised")
	}
	originMain := rsGit(t, e.origin, "rev-parse", "refs/heads/main")
	if want := sp.planted[len(sp.planted)-1]; originMain != want {
		t.Fatalf("origin main = %s, want the merged local main %s (attacker %s)", originMain, want, attacker)
	}
	if rsIsAncestor(e.origin, attacker, "refs/heads/main") {
		t.Fatal("the attacker commit reached origin main")
	}
	if !rsIsAncestor(e.origin, reviewed, "refs/heads/main") {
		t.Fatal("the reviewed run-branch tip is not on origin main")
	}
}

// realRepoClients serves a real *git.CLIClient per code repository.
type realRepoClients map[string]git.GitClient

func (r realRepoClients) Client(_ context.Context, repoID string) (git.GitClient, error) {
	c, ok := r[repoID]
	if !ok {
		return nil, errors.New("test misconfigured: no client for " + repoID)
	}
	return c, nil
}

// TestRefShadow_StartRunCutsFromTheRealBase is review round 1 (both P2
// findings) through StartRun with real CLIClients on the primary and a
// code repository. `git branch <run> HEAD` (primary, and divergence)
// died "ambiguous object name: 'HEAD'" next to any HEAD-named ref, so
// one pushed refs/tags/HEAD failed every run start; `git branch <run>
// <oid>` (code repository, base resolved exactly from default_branch)
// adopted a ref named after <oid>, so the code-repo run branch was cut
// at the attacker commit while RepositoryBaselines recorded the real
// tip. Each shadow is planted in both repositories.
func TestRefShadow_StartRunCutsFromTheRealBase(t *testing.T) {
	shadows := []string{
		"refs/HEAD", "refs/tags/HEAD", "refs/heads/HEAD", "refs/remotes/HEAD",
		"refs/tags/<main oid>", "refs/heads/<main oid>", "refs/<main oid>", "refs/remotes/<main oid>",
	}
	for _, shadow := range shadows {
		t.Run(shadow, func(t *testing.T) {
			e := newRSEnv(t)
			attacker := e.plantAttacker(t, "refs/attacker/parking")

			code := filepath.Join(e.root, "code")
			codeOrigin := filepath.Join(e.root, "code-origin.git")
			rsGit(t, e.root, "init", "-q", "--bare", "-b", "main", codeOrigin)
			rsGit(t, e.root, "init", "-q", "-b", "main", code)
			rsGit(t, code, "remote", "add", "origin", codeOrigin)
			testutil.WriteFile(t, code, "svc.go", "package svc\n")
			rsGit(t, code, "add", ".")
			rsGit(t, code, "commit", "-qm", "code base")
			rsGit(t, code, "push", "-q", "origin", "refs/heads/main:refs/heads/main")
			rsGit(t, code, "switch", "-q", "--orphan", "evil")
			testutil.WriteFile(t, code, "svc.go", "package svc // ATTACKER\n")
			rsGit(t, code, "add", ".")
			rsGit(t, code, "commit", "-qm", "code attacker")
			codeAttacker := rsGit(t, code, "rev-parse", "HEAD")
			rsGit(t, code, "switch", "-q", "main")
			rsGit(t, code, "branch", "-q", "-D", "evil")

			primaryMain := rsGit(t, e.repo, "show-ref", "--verify", "--hash", "refs/heads/main")
			codeMain := rsGit(t, code, "show-ref", "--verify", "--hash", "refs/heads/main")
			rsGit(t, e.repo, "update-ref", strings.ReplaceAll(shadow, "<main oid>", primaryMain), attacker)
			rsGit(t, code, "update-ref", strings.ReplaceAll(shadow, "<main oid>", codeMain), codeAttacker)

			art := taskWithRepos([]string{"code-a"})
			resolver := newRepoResolver(map[string]repoLookup{"code-a": activeRepoLookup("code-a", "main")})
			orch, st := multiRepoOrchestrator(t, art, resolver,
				realRepoClients{"code-a": git.NewCLIClient(code)}, git.NewCLIClient(e.repo))
			res, err := orch.StartRun(context.Background(), art.Path)
			if err != nil {
				t.Fatalf("StartRun with %s: %v", shadow, err)
			}
			if st.createdRun == nil {
				t.Fatal("no run row was persisted")
			}
			b := res.Run.BranchName
			if got := rsGit(t, e.repo, "show-ref", "--verify", "--hash", "refs/heads/"+b); got != primaryMain {
				t.Errorf("primary run branch cut at %s, want HEAD %s (attacker %s)", got, primaryMain, attacker)
			}
			if got := rsGit(t, code, "show-ref", "--verify", "--hash", "refs/heads/"+b); got != codeMain {
				t.Errorf("code-repo run branch cut at %s, want main %s (attacker %s)", got, codeMain, codeAttacker)
			}
			if got := rsGit(t, codeOrigin, "show-ref", "--verify", "--hash", "refs/heads/"+b); got != codeMain {
				t.Errorf("code-repo run branch on origin at %s, want main %s", got, codeMain)
			}
			if got := res.Run.RepositoryBaselines["code-a"]; got != codeMain {
				t.Errorf("RepositoryBaselines[code-a] = %q, want %s", got, codeMain)
			}
			if got := res.Run.RepositoryBaselines[domain.PrimaryRepositoryID]; got != primaryMain {
				t.Errorf("RepositoryBaselines[spine] = %q, want %s", got, primaryMain)
			}
		})
	}
}
