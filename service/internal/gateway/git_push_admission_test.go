package gateway_test

// Ref-class push admission through the real standalone gateway (INIT-023
// TASK-016 AC 8-12): gateway.NewServer with bearer auth, githttp with the
// git-http-backend CGI, real git clients. Before TASK-016 the pre-receive
// gate skipped refs/heads/spine/* entirely, so any authenticated actor
// (a reader included) could create, advance, force-push or delete any
// run, planning or divergence branch, and the governed merge published
// whatever the run branch then pointed at (audit F2, push half).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/cli"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/bszymi/spine/service/internal/workspace"
)

const (
	admRunID     = "run-0a1b2c3d"
	admRunBranch = "spine/run/task-001-victim-0a1b2c3d"
	admDivID     = admRunID + "-div-d"
	admDivBranch = "spine/" + admRunID + "/" + admDivID + "/b1"
)

type admResolver struct{ cfg workspace.Config }

func (r admResolver) Resolve(_ context.Context, id string) (*workspace.Config, error) {
	if id != "" && id != r.cfg.ID {
		return nil, workspace.ErrWorkspaceNotFound
	}
	c := r.cfg
	return &c, nil
}

func (r admResolver) List(context.Context) ([]workspace.Config, error) {
	return []workspace.Config{r.cfg}, nil
}

// admRuns is a mutex-guarded refadmit.RunReader: the gateway reads it
// on its server goroutine while the test goroutine changes run state
// between pushes, with only an external git process in between.
type admRuns struct {
	mu       sync.Mutex
	runs     map[string]*domain.Run
	execs    map[string][]domain.StepExecution
	branches map[string]*domain.Branch
}

func (f *admRuns) GetRun(_ context.Context, id string) (*domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.runs[id]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, domain.NewError(domain.ErrNotFound, "run not found")
}

func (f *admRuns) ListStepExecutionsByRun(_ context.Context, id string) ([]domain.StepExecution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.StepExecution(nil), f.execs[id]...), nil
}

func (f *admRuns) GetBranch(_ context.Context, id string) (*domain.Branch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.branches[id]; ok {
		cp := *b
		return &cp, nil
	}
	return nil, domain.NewError(domain.ErrNotFound, "branch not found")
}

func (f *admRuns) setRunStatus(st domain.RunStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[admRunID].Status = st
}

// admStepDefs resolves every step as a manual (unfrozen) step, standing
// in for the orchestrator's pinned-workflow lookup.
type admStepDefs struct{}

func (admStepDefs) LookupStepDef(_ context.Context, _, stepID string) (*domain.StepDefinition, *domain.Run) {
	return &domain.StepDefinition{ID: stepID, Type: domain.StepTypeManual}, nil
}

// admGitEnv is a hermetic environment for test-side git: no host or
// system config, no prompt, a fixed identity.
func admGitEnv(home string) []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "HOME="+home,
		"GIT_AUTHOR_NAME=x", "GIT_AUTHOR_EMAIL=x@example.invalid",
		"GIT_COMMITTER_NAME=x", "GIT_COMMITTER_EMAIL=x@example.invalid")
}

func admGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = admGitEnv(dir)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func admMust(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := admGit(t, dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func admRefSHA(t *testing.T, repo, ref string) string {
	t.Helper()
	out, err := admGit(t, repo, "rev-parse", "--verify", "-q", "refs/heads/"+ref)
	if err != nil {
		return ""
	}
	return out
}

// admServerRepo builds the served repository: main and the reviewed
// branches (all at one commit holding task.md = "reviewed content").
func admServerRepo(t *testing.T, branches ...string) (repo, reviewed string) {
	t.Helper()
	repo = t.TempDir()
	admMust(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "task.md"), []byte("reviewed content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	admMust(t, repo, "add", ".")
	admMust(t, repo, "commit", "-q", "-m", "init")
	for _, b := range branches {
		admMust(t, repo, "branch", b)
	}
	return repo, admMust(t, repo, "rev-parse", "HEAD")
}

// admTokens registers one actor per role entry and mints a token each.
func admTokens(t *testing.T, fs *fakeStore, actors map[string]domain.ActorRole) (*auth.Service, map[string]string) {
	t.Helper()
	for id, role := range actors {
		fs.actors[id] = &domain.Actor{ActorID: id, Type: domain.ActorTypeAIAgent, Role: role, Status: domain.ActorStatusActive}
	}
	authSvc := auth.NewService(fs)
	tokens := map[string]string{}
	for id := range actors {
		tok, _, err := authSvc.CreateToken(context.Background(), id, "t", nil)
		if err != nil {
			t.Fatal(err)
		}
		tokens[id] = tok
	}
	return authSvc, tokens
}

// admPusher is a clone of the served repository with one unreviewed
// commit on top, pushing as a chosen principal.
type admPusher struct {
	t      *testing.T
	url    string
	dir    string
	tokens map[string]string
}

func newAdmPusher(t *testing.T, url string, tokens map[string]string, cloneAs string) *admPusher {
	t.Helper()
	parent := t.TempDir()
	dir := filepath.Join(parent, "c")
	if out, err := admGit(t, parent, "-c", "http.extraHeader=Authorization: Bearer "+tokens[cloneAs], "clone", "-q", url, dir); err != nil {
		t.Fatalf("clone as %s: %v\n%s", cloneAs, err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte("UNREVIEWED attacker content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	admMust(t, dir, "commit", "-q", "-am", "inject")
	return &admPusher{t: t, url: url, dir: dir, tokens: tokens}
}

func (p *admPusher) push(as string, args ...string) (string, error) {
	p.t.Helper()
	out, err := admGit(p.t, p.dir, append([]string{"-c", "http.extraHeader=Authorization: Bearer " + p.tokens[as], "push"}, args...)...)
	p.t.Logf("%s push %v: err=%v\n%s", as, args, err, out)
	return out, err
}

func (p *admPusher) head() string { return admMust(p.t, p.dir, "rev-parse", "HEAD") }

// TestGitPush_ReaderCannotPushForeignRunBranch is the audit PoC
// (poc_git_push_runbranch_test.go) with its assertions inverted. It
// keeps the PoC's setup: a reader token, the bootstrap rule set
// (StaticRules(nil): main is no-delete + no-direct-write), no push
// resolver, and the literal branch spine/run/victim-run. At 61994fe
// both the push and the force-push landed, and the governed merge would
// then have published the unreviewed content.
//
// On its own this test does not prove the binding: a reader is refused
// by the role floor before any store read. TestGitPush_BoundAndOperatorPushes
// is the F2 regression with a contributor and a store wired.
func TestGitPush_ReaderCannotPushForeignRunBranch(t *testing.T) {
	srvRepo, reviewed := admServerRepo(t, "spine/run/victim-run")

	fs := newFakeStore()
	authSvc, tokens := admTokens(t, fs, map[string]domain.ActorRole{"reader-1": domain.RoleReader})
	gh, err := githttp.NewHandler(githttp.Config{
		ReceivePackEnabled: true,
		Policy:             branchprotect.New(branchprotect.StaticRules(nil)),
	})
	if err != nil {
		t.Skipf("git-http-backend unavailable: %v", err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store: fs, Auth: authSvc, GitHTTP: gh,
		WorkspaceResolver: admResolver{cfg: workspace.Config{ID: "ws1", RepoPath: srvRepo, Status: workspace.StatusActive}},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	p := newAdmPusher(t, ts.URL+"/git/ws1", tokens, "reader-1")
	admMust(t, p.dir, "checkout", "-q", "-b", "local", "origin/spine/run/victim-run")
	if err := os.WriteFile(filepath.Join(p.dir, "task.md"), []byte("UNREVIEWED attacker content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	admMust(t, p.dir, "commit", "-q", "-am", "inject")

	if out, err := p.push("reader-1", "origin", "HEAD:refs/heads/spine/run/victim-run"); err == nil || !strings.Contains(out, "ref-admission:") {
		t.Errorf("reader push to a run branch: err=%v, want a ref-admission refusal\n%s", err, out)
	}
	admMust(t, p.dir, "commit", "-q", "--amend", "-m", "rewritten")
	if out, err := p.push("reader-1", "-f", "origin", "HEAD:refs/heads/spine/run/victim-run"); err == nil || !strings.Contains(out, "ref-admission:") {
		t.Errorf("reader force-push to a run branch: err=%v, want a ref-admission refusal\n%s", err, out)
	}
	if out, err := p.push("reader-1", "origin", "HEAD:refs/heads/main"); err == nil {
		t.Errorf("control: reader push to main was accepted\n%s", out)
	}

	if got := admRefSHA(t, srvRepo, "spine/run/victim-run"); got != reviewed {
		t.Fatalf("run branch moved: %s, want the reviewed %s", got, reviewed)
	}
	if got := admMust(t, srvRepo, "show", "spine/run/victim-run:task.md"); got != "reviewed content" {
		t.Fatalf("run branch content = %q, want the reviewed content", got)
	}
}

// admGateway serves a repository holding main, the fixture run branch
// and a divergence branch, with a push resolver that returns the
// bootstrap policy, the fixture run reader and a step-definition
// lookup. alice holds the run's current step, dave holds divergence
// branch b1's current step, bob is an unbound contributor.
type admGateway struct {
	url      string
	repo     string
	reviewed string
	runs     *admRuns
	tokens   map[string]string
}

func newAdmGateway(t *testing.T) *admGateway {
	t.Helper()
	repo, reviewed := admServerRepo(t, admRunBranch, admDivBranch)
	runs := &admRuns{
		runs: map[string]*domain.Run{admRunID: {
			RunID: admRunID, Status: domain.RunStatusActive, CurrentStepID: "execute", BranchName: admRunBranch,
		}},
		execs: map[string][]domain.StepExecution{admRunID: {
			{ExecutionID: "e1", RunID: admRunID, StepID: "execute", ActorID: "alice", Status: domain.StepStatusInProgress, RepositoryID: domain.PrimaryRepositoryID},
			{ExecutionID: "x1", RunID: admRunID, StepID: "explore", BranchID: admDivID + "-b1", ActorID: "dave", Status: domain.StepStatusAssigned, RepositoryID: domain.PrimaryRepositoryID},
		}},
		branches: map[string]*domain.Branch{admDivID + "-b1": {
			BranchID: admDivID + "-b1", RunID: admRunID, DivergenceID: admDivID, Status: domain.BranchStatusInProgress, CurrentStepID: "explore",
		}},
	}
	fs := newFakeStore()
	authSvc, tokens := admTokens(t, fs, map[string]domain.ActorRole{
		"alice": domain.RoleContributor, "bob": domain.RoleContributor, "dave": domain.RoleContributor,
		"op": domain.RoleOperator,
	})
	policy := branchprotect.New(branchprotect.StaticRules(nil))
	gh, err := githttp.NewHandler(githttp.Config{ReceivePackEnabled: true, Policy: policy})
	if err != nil {
		t.Skipf("git-http-backend unavailable: %v", err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store: fs, Auth: authSvc, GitHTTP: gh,
		WorkspaceResolver: admResolver{cfg: workspace.Config{ID: "ws1", RepoPath: repo, Status: workspace.StatusActive}},
		GitPushResolver: func(context.Context, string) (gateway.GitPushResources, func(), error) {
			return gateway.GitPushResources{Policy: policy, Runs: runs, StepDefs: admStepDefs{}}, func() {}, nil
		},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &admGateway{url: ts.URL + "/git/ws1", repo: repo, reviewed: reviewed, runs: runs, tokens: tokens}
}

// TestGitPush_BoundAndOperatorPushes is the F2 regression proper (AC
// 9): a contributor with a store wired. Only the actor holding the
// run's current step may move the run branch; nobody below operator
// may delete it; the merge window (committing) closes it.
func TestGitPush_BoundAndOperatorPushes(t *testing.T) {
	g := newAdmGateway(t)
	p := newAdmPusher(t, g.url, g.tokens, "op")
	dst := "HEAD:refs/heads/" + admRunBranch

	// bob is not bound: plain and force push are both refused.
	if out, err := p.push("bob", "origin", dst); err == nil || !strings.Contains(out, "ref-admission: "+admRunBranch+" is a run branch") {
		t.Errorf("unbound contributor push: err=%v, want the run-branch refusal\n%s", err, out)
	}
	if out, err := p.push("bob", "-f", "origin", dst); err == nil || !strings.Contains(out, "ref-admission:") {
		t.Errorf("unbound contributor force-push: err=%v, want a refusal\n%s", err, out)
	}
	if got := admRefSHA(t, g.repo, admRunBranch); got != g.reviewed {
		t.Fatalf("run branch moved after bob's pushes: %s, want %s", got, g.reviewed)
	}

	// alice holds the current step.
	if out, err := p.push("alice", "origin", dst); err != nil {
		t.Fatalf("bound contributor push refused: %v\n%s", err, out)
	}
	if got := admRefSHA(t, g.repo, admRunBranch); got != p.head() {
		t.Fatalf("run branch = %s, want alice's %s", got, p.head())
	}
	if out, err := p.push("alice", "origin", "--delete", admRunBranch); err == nil || !strings.Contains(out, "ref-admission: deleting "+admRunBranch+" requires the operator role") {
		t.Errorf("bound contributor delete: err=%v, want the delete refusal\n%s", err, out)
	}
	if admRefSHA(t, g.repo, admRunBranch) == "" {
		t.Fatal("run branch deleted by a contributor")
	}

	// The merge window: once the run is committing, alice is refused.
	g.runs.setRunStatus(domain.RunStatusCommitting)
	admMust(t, p.dir, "commit", "-q", "--allow-empty", "-m", "late")
	if out, err := p.push("alice", "origin", dst); err == nil || !strings.Contains(out, "ref-admission:") {
		t.Errorf("push during committing: err=%v, want a refusal\n%s", err, out)
	}
	g.runs.setRunStatus(domain.RunStatusActive)

	// The operator (unbound) may force-push and delete.
	if out, err := p.push("op", "-f", "origin", dst); err != nil {
		t.Fatalf("operator push refused: %v\n%s", err, out)
	}
	if out, err := p.push("op", "origin", "--delete", admRunBranch); err != nil {
		t.Fatalf("operator delete refused: %v\n%s", err, out)
	}
	if got := admRefSHA(t, g.repo, admRunBranch); got != "" {
		t.Errorf("run branch still exists after the operator delete: %s", got)
	}
}

// TestGitPush_WorkspaceWithoutStoreDoesNotFallBack: when the per-push
// resolver has no run store for the workspace (a shared-mode workspace
// with no database yet), run branches are operator-only for that push.
// The handler's own fallback reader — here one that would bind alice —
// must not be consulted, since in shared mode it belongs to another
// workspace.
func TestGitPush_WorkspaceWithoutStoreDoesNotFallBack(t *testing.T) {
	repo, _ := admServerRepo(t, admRunBranch)
	fallback := &admRuns{
		runs: map[string]*domain.Run{admRunID: {
			RunID: admRunID, Status: domain.RunStatusActive, CurrentStepID: "execute", BranchName: admRunBranch,
		}},
		execs: map[string][]domain.StepExecution{admRunID: {
			{ExecutionID: "e1", RunID: admRunID, StepID: "execute", ActorID: "alice", Status: domain.StepStatusInProgress},
		}},
	}
	fs := newFakeStore()
	authSvc, tokens := admTokens(t, fs, map[string]domain.ActorRole{"alice": domain.RoleContributor, "op": domain.RoleOperator})
	policy := branchprotect.New(branchprotect.StaticRules(nil))
	gh, err := githttp.NewHandler(githttp.Config{
		ReceivePackEnabled: true, Policy: policy, RunRefs: fallback, StepDefs: admStepDefs{},
	})
	if err != nil {
		t.Skipf("git-http-backend unavailable: %v", err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store: fs, Auth: authSvc, GitHTTP: gh,
		WorkspaceResolver: admResolver{cfg: workspace.Config{ID: "ws1", RepoPath: repo, Status: workspace.StatusActive}},
		GitPushResolver: func(context.Context, string) (gateway.GitPushResources, func(), error) {
			return gateway.GitPushResources{Policy: branchprotect.NewPermissive()}, func() {}, nil
		},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	p := newAdmPusher(t, ts.URL+"/git/ws1", tokens, "op")
	out, err := p.push("alice", "origin", "HEAD:refs/heads/"+admRunBranch)
	if err == nil || !strings.Contains(out, "ref-admission: run-branch admission is not configured on this server") {
		t.Errorf("push with no workspace store: err=%v, want the operator-only refusal\n%s", err, out)
	}
	if out, err := p.push("op", "origin", "HEAD:refs/heads/"+admRunBranch); err != nil {
		t.Errorf("operator push refused: %v\n%s", err, out)
	}
}

// admCodeRepos is a gitpool.Resolver serving one code repository.
type admCodeRepos struct{ id, path string }

func (r admCodeRepos) Lookup(_ context.Context, id string) (*repository.Repository, error) {
	if id == r.id {
		return &repository.Repository{ID: id, LocalPath: r.path}, nil
	}
	return nil, domain.NewErrorWithCause(domain.ErrNotFound,
		fmt.Sprintf("repository %q not found in catalog", id), repository.ErrRepositoryNotFound)
}

func (r admCodeRepos) ListActive(context.Context) ([]repository.Repository, error) { return nil, nil }

// TestGitPush_RepositoryScopedBinding pins §3 item 2 (DC-7): the
// gateway hands the URL's repository segment to the admission, so a run
// branch is pushable only in the repository the bound execution
// targets. carol holds the run's current step `build` on code
// repository "payments". Review round 1: no gateway test pushed to a
// code-repository URL, so dropping githttp.WithRepositoryID in
// handleGit (every push then evaluated as a primary-repository push)
// went unnoticed. Mutation check: that no-op fails this test.
func TestGitPush_RepositoryScopedBinding(t *testing.T) {
	primary, primaryReviewed := admServerRepo(t, admRunBranch)
	payments, paymentsReviewed := admServerRepo(t, admRunBranch)
	runs := &admRuns{
		runs: map[string]*domain.Run{admRunID: {
			RunID: admRunID, Status: domain.RunStatusActive, CurrentStepID: "build", BranchName: admRunBranch,
			RepositoryBranches: map[string]string{"payments": admRunBranch},
		}},
		execs: map[string][]domain.StepExecution{admRunID: {
			{ExecutionID: "b1", RunID: admRunID, StepID: "build", ActorID: "carol", Status: domain.StepStatusInProgress, RepositoryID: "payments"},
		}},
	}
	fs := newFakeStore()
	authSvc, tokens := admTokens(t, fs, map[string]domain.ActorRole{"carol": domain.RoleContributor, "op": domain.RoleOperator})
	policy := branchprotect.New(branchprotect.StaticRules(nil))
	gh, err := githttp.NewHandler(githttp.Config{ReceivePackEnabled: true, Policy: policy})
	if err != nil {
		t.Skipf("git-http-backend unavailable: %v", err)
	}
	pool, err := gitpool.New(git.NewCLIClient(primary), admCodeRepos{id: "payments", path: payments}, gitpool.NewCLIClientFactory())
	if err != nil {
		t.Fatalf("gitpool.New: %v", err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store: fs, Auth: authSvc, GitHTTP: gh, GitPool: pool,
		WorkspaceResolver: admResolver{cfg: workspace.Config{ID: "ws1", RepoPath: primary, Status: workspace.StatusActive}},
		GitPushResolver: func(context.Context, string) (gateway.GitPushResources, func(), error) {
			return gateway.GitPushResources{Policy: policy, Runs: runs, StepDefs: admStepDefs{}}, func() {}, nil
		},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	base := ts.URL + "/git/ws1"
	dst := "HEAD:refs/heads/" + admRunBranch

	p := newAdmPusher(t, base+"/payments", tokens, "op")
	refused := func(url string) {
		t.Helper()
		if out, err := p.push("carol", url, dst); err == nil || !strings.Contains(out, "ref-admission: "+admRunBranch+" is a run branch") {
			t.Errorf("carol push to %s: err=%v, want the run-branch refusal\n%s", url, err, out)
		}
	}

	// The step targets payments: admitted there, refused in the
	// primary under both of its URL spellings.
	if out, err := p.push("carol", base+"/payments", dst); err != nil {
		t.Fatalf("carol push to payments refused: %v\n%s", err, out)
	}
	if got := admRefSHA(t, payments, admRunBranch); got != p.head() {
		t.Fatalf("payments run branch = %s, want carol's %s (reviewed %s)", got, p.head(), paymentsReviewed)
	}
	refused(base)
	refused(base + "/spine")
	if got := admRefSHA(t, primary, admRunBranch); got != primaryReviewed {
		t.Fatalf("primary run branch moved to %s, want %s", got, primaryReviewed)
	}

	// Retarget the step at the primary: "/git/ws1/spine" is the primary
	// (DC-7), and payments is now refused.
	runs.mu.Lock()
	runs.execs[admRunID][0].RepositoryID = domain.PrimaryRepositoryID
	runs.mu.Unlock()
	admMust(t, p.dir, "commit", "-q", "--allow-empty", "-m", "primary")
	if out, err := p.push("carol", base+"/spine", dst); err != nil {
		t.Fatalf("carol push to the primary via /spine refused: %v\n%s", err, out)
	}
	if got := admRefSHA(t, primary, admRunBranch); got != p.head() {
		t.Fatalf("primary run branch = %s, want carol's %s", got, p.head())
	}
	admMust(t, p.dir, "commit", "-q", "--allow-empty", "-m", "late")
	refused(base + "/payments")
}

// TestGitPush_DivergenceRefAdmission is AC 11: the deterministic
// divergence ref (the F8 vector) admits only the actor holding that
// branch's current step, or an operator.
func TestGitPush_DivergenceRefAdmission(t *testing.T) {
	g := newAdmGateway(t)
	p := newAdmPusher(t, g.url, g.tokens, "op")
	dst := "HEAD:refs/heads/" + admDivBranch

	if out, err := p.push("alice", "origin", dst); err == nil || !strings.Contains(out, "ref-admission: "+admDivBranch+" is a divergence branch") {
		t.Errorf("run-level actor push to the divergence ref: err=%v, want the divergence refusal\n%s", err, out)
	}
	if got := admRefSHA(t, g.repo, admDivBranch); got != g.reviewed {
		t.Fatalf("divergence ref moved: %s", got)
	}
	if out, err := p.push("dave", "origin", dst); err != nil {
		t.Fatalf("branch-bound actor refused: %v\n%s", err, out)
	}
	if got := admRefSHA(t, g.repo, admDivBranch); got != p.head() {
		t.Fatalf("divergence ref = %s, want dave's %s", got, p.head())
	}
	admMust(t, p.dir, "commit", "-q", "--allow-empty", "-m", "op")
	if out, err := p.push("op", "origin", dst); err != nil {
		t.Fatalf("operator refused: %v\n%s", err, out)
	}
}

// TestGitPush_InitAndReservedNamespace is AC 12: spine/init is written
// by `spine init-repo` with an operator bearer only, the rest of the
// spine namespace is operator-only, and ordinary branches keep their
// branch-protection behaviour.
func TestGitPush_InitAndReservedNamespace(t *testing.T) {
	g := newAdmGateway(t)

	// cli.InitRepo runs git with the process environment.
	home := t.TempDir()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0", "HOME": home,
		"GIT_AUTHOR_NAME": "x", "GIT_AUTHOR_EMAIL": "x@example.invalid",
		"GIT_COMMITTER_NAME": "x", "GIT_COMMITTER_EMAIL": "x@example.invalid",
		"SPINE_GIT_AUTO_PUSH": "",
	} {
		t.Setenv(k, v)
	}
	// initRepoAs runs cli.InitRepo in a clone authenticated as who and
	// returns what it wrote to stderr (InitRepo writes its push note to
	// os.Stderr; this test is not parallel, so swapping it is safe).
	initRepoAs := func(who string) string {
		t.Helper()
		parent := t.TempDir()
		dir := filepath.Join(parent, "c")
		admMust(t, parent, "clone", "-q", "-c", "http.extraHeader=Authorization: Bearer "+g.tokens[who], g.url, dir)
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		stderr := make(chan string, 1)
		go func() {
			b, _ := io.ReadAll(r)
			stderr <- string(b)
		}()
		orig := os.Stderr
		os.Stderr = w
		initErr := func() error {
			defer func() { os.Stderr = orig; w.Close() }()
			return cli.InitRepo(dir)
		}()
		out := <-stderr
		if initErr != nil {
			t.Fatalf("InitRepo as %s: %v\n%s", who, initErr, out)
		}
		return out
	}
	// The contributor's push of spine/init is refused, and init-repo
	// says why instead of "remote may not be configured" (review round
	// 1: before, it exited 0 with only that misleading note).
	out := initRepoAs("alice")
	if got := admRefSHA(t, g.repo, "spine/init"); got != "" {
		t.Fatalf("contributor init-repo created spine/init (%s)", got)
	}
	if !strings.Contains(out, "ref-admission: spine/init is reserved for 'spine init-repo'; operator role required") ||
		!strings.Contains(out, "operator token") || strings.Contains(out, "remote may not be configured") {
		t.Errorf("contributor init-repo stderr does not name the refusal and its remedy:\n%s", out)
	}
	if out := initRepoAs("op"); strings.Contains(out, "Note:") {
		t.Errorf("operator init-repo printed a push note:\n%s", out)
	}
	if admRefSHA(t, g.repo, "spine/init") == "" {
		t.Fatal("operator init-repo did not create spine/init")
	}

	p := newAdmPusher(t, g.url, g.tokens, "op")
	for _, ref := range []string{"spine", "spine/other"} {
		out, err := p.push("alice", "origin", "HEAD:refs/heads/"+ref)
		if err == nil || !strings.Contains(out, "ref-admission: "+ref+" is in the reserved spine namespace; operator role required") {
			t.Errorf("contributor push to %s: err=%v, want the reserved-namespace refusal\n%s", ref, err, out)
		}
	}
	if out, err := p.push("alice", "origin", "HEAD:refs/heads/feature/x"); err != nil {
		t.Errorf("contributor push to an ordinary branch refused: %v\n%s", err, out)
	}
	if out, err := p.push("alice", "origin", "HEAD:refs/heads/main"); err == nil || !strings.Contains(out, `branch-protection: rule "no-direct-write"`) {
		t.Errorf("contributor push to main: err=%v, want the no-direct-write refusal\n%s", err, out)
	}
}

// caseInsensitiveDir reports whether dir's filesystem folds case.
func caseInsensitiveDir(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "case-probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, "CASE-PROBE"))
	return err == nil
}

func pktLine(s string) []byte { return []byte(fmt.Sprintf("%04x%s", len(s)+4, s)) }

// TestGitPush_CaseFoldVariantOfReservedRefRefused (AC 12, DC-1): on a
// case-insensitive server filesystem "SPINE/…" and "ſpine/…" (U+017F)
// name the same files as "spine/…", and git refuses only a colliding
// create. An update carrying the run branch's current object id would
// land on the real run branch. Both spellings classify as reserved, so
// even the bound actor is refused. Runs on case-insensitive hosts
// (macOS dev); on Linux CI it skips and core/refadmit's TestClassify
// rows carry the guarantee.
func TestGitPush_CaseFoldVariantOfReservedRefRefused(t *testing.T) {
	g := newAdmGateway(t)
	if !caseInsensitiveDir(t, g.repo) {
		t.Skip("server repository is on a case-sensitive filesystem")
	}

	// A new commit on top of the run branch, and a pack holding it. The
	// stock client sends a zero old id for an unadvertised name, so the
	// command section is built by hand with the fixture's current id.
	tree := admMust(t, g.repo, "rev-parse", g.reviewed+"^{tree}")
	next := admMust(t, g.repo, "commit-tree", tree, "-p", g.reviewed, "-m", "case-fold update")
	pack := exec.Command("git", "pack-objects", "--stdout", "-q", "--revs")
	pack.Dir = g.repo
	pack.Env = admGitEnv(g.repo)
	pack.Stdin = strings.NewReader(next + "\n^" + g.reviewed + "\n")
	packBytes, err := pack.Output()
	if err != nil {
		t.Fatalf("pack-objects: %v", err)
	}

	for _, who := range []string{"bob", "alice"} {
		for _, variant := range []string{
			"SPINE/run/task-001-victim-0a1b2c3d",
			"ſpine/run/task-001-victim-0a1b2c3d",
		} {
			var body bytes.Buffer
			body.Write(pktLine(g.reviewed + " " + next + " refs/heads/" + variant + "\x00report-status side-band-64k\n"))
			body.WriteString("0000")
			body.Write(packBytes)
			req, _ := http.NewRequest(http.MethodPost, g.url+"/git-receive-pack", &body)
			req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
			req.Header.Set("Authorization", "Bearer "+g.tokens[who])
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			_, _ = out.ReadFrom(resp.Body)
			resp.Body.Close()
			if !strings.Contains(out.String(), "ref-admission: "+variant+" is in the reserved spine namespace") {
				t.Errorf("%s update of %q: want the reserved-namespace refusal, got %q", who, variant, out.String())
			}
			if got := admRefSHA(t, g.repo, admRunBranch); got != g.reviewed {
				t.Fatalf("%s update of %q moved the run branch to %s", who, variant, got)
			}
		}
	}
}
