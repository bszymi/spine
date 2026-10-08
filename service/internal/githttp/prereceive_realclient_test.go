package githttp

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/core/domain"
)

// TestPreReceive_RealGitClientPushesUnchanged drives the gate with the
// real git client (not hand-built bodies), so the INIT-023 TASK-001
// parser change is proven against what git send-pack actually emits:
// capabilities on line 1 only, no trailing LF, push-options in their
// own section. It passes before and after the fix.
//
//   - contributor: a multi-ref push (including a spine/run/* branch
//     whose current step the contributor holds, nulHandler; INIT-023
//     TASK-016), a push with
//     -o ci.skip and a delete of an unprotected branch all apply; a
//     direct write to protected main is still denied by its rule, and
//     -o spine.override=true does not change that;
//   - operator: -o spine.override=true on main still applies with
//     exactly one branch_protection.override event.
func TestPreReceive_RealGitClientPushesUnchanged(t *testing.T) {
	repo, _, _, _ := nulRepo(t, "", "old-branch")
	h := nulHandler(t, repo, config.Rule{Branch: "main",
		Protections: []config.RuleKind{config.KindNoDelete, config.KindNoDirectWrite}})
	em := &nulEmitter{}

	// One server, one path prefix per role: the prefix picks the actor
	// the gateway would have authenticated and is stripped before the
	// handler sees the git path.
	mux := http.NewServeMux()
	for _, role := range []domain.ActorRole{domain.RoleContributor, domain.RoleOperator} {
		actor := &domain.Actor{ActorID: "actor-" + string(role), Role: role}
		prefix := "/" + string(role)
		mux.Handle(prefix+"/", http.StripPrefix(prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithRepoPath(r.Context(), repo)
			ctx = WithEvents(ctx, em)
			ctx = domain.WithActor(ctx, actor)
			h.ServeHTTP(w, r.WithContext(ctx))
		})))
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	contributorURL := srv.URL + "/contributor/"
	operatorURL := srv.URL + "/operator/"

	work := t.TempDir()
	clone := exec.Command("git", "clone", "-q", contributorURL, work)
	clone.Env = nulGitEnv(t)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	nulGit(t, work, nil, "commit", "-q", "--allow-empty", "-m", "work")
	push := func(url string, args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", work, "push", "--porcelain", url}, args...)...)
		cmd.Env = nulGitEnv(t)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := push(contributorURL, "HEAD:refs/heads/feature-a", "HEAD:refs/heads/spine/run/task-001-abcd1234", "HEAD:refs/heads/feature-b"); err != nil {
		t.Fatalf("multi-ref push refused: %v\n%s", err, out)
	}
	if out, err := push(contributorURL, "-o", "ci.skip", "HEAD:refs/heads/feature-c"); err != nil {
		t.Fatalf("push with push-options refused: %v\n%s", err, out)
	}
	if out, err := push(contributorURL, ":refs/heads/old-branch"); err != nil {
		t.Fatalf("delete of unprotected branch refused: %v\n%s", err, out)
	}
	head := nulGit(t, work, nil, "rev-parse", "HEAD")
	for _, ref := range []string{"refs/heads/feature-a", "refs/heads/feature-b", "refs/heads/feature-c", "refs/heads/spine/run/task-001-abcd1234"} {
		if got := nulGit(t, repo, nil, "rev-parse", ref); got != head {
			t.Errorf("%s = %s, want %s", ref, got, head)
		}
	}
	if nulRefExists(t, repo, "refs/heads/old-branch") {
		t.Error("old-branch not deleted")
	}

	mainBefore := nulGit(t, repo, nil, "rev-parse", "refs/heads/main")
	if out, err := push(contributorURL, "HEAD:refs/heads/main"); err == nil || !strings.Contains(out, `rule "no-direct-write"`) {
		t.Fatalf("direct write to protected main not denied by its rule: err=%v\n%s", err, out)
	}
	if out, err := push(contributorURL, "-o", "spine.override=true", "HEAD:refs/heads/main"); err == nil || !strings.Contains(out, "lacks operator role") {
		t.Fatalf("contributor override not refused: err=%v\n%s", err, out)
	}
	if got := nulGit(t, repo, nil, "rev-parse", "refs/heads/main"); got != mainBefore {
		t.Fatalf("main moved on a denied push: %s -> %s", mainBefore, got)
	}
	if n := em.count(); n != 0 {
		t.Fatalf("events before the operator override = %d, want 0", n)
	}

	if out, err := push(operatorURL, "-o", "spine.override=true", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("operator override push refused: %v\n%s", err, out)
	}
	if got := nulGit(t, repo, nil, "rev-parse", "refs/heads/main"); got != head {
		t.Errorf("operator override did not land: main = %s, want %s", got, head)
	}
	if n := em.count(); n != 1 {
		t.Errorf("override events = %d, want exactly 1", n)
	}
}

// TestPreReceive_ShallowClonePushStillRefused pins the other place the
// gate is stricter than git, one INIT-023 TASK-001 did not introduce: a
// push from a shallow clone. git send-pack sends a `shallow <oid>`
// pkt-line ahead of the commands whenever the pushing repository is
// shallow, and git receive-pack applies the push when the pushed
// commits connect to history it already has. The gate's parser takes
// that line for a command, finds two fields and refuses the whole push
// as `command 1: malformed ref update` — exactly as it did before this
// task (Design §5 lists shallow among the shapes already refused; owner
// question 2 keeps it, and docs/git-push-guide.md §5 names it as a
// cause). The CHANGELOG and isCapabilityList's doc comment state it
// beside the one exception this task did add.
//
// The same shallow clone pushes the same commit to plain git (a bare
// copy of the repository, over the local transport) and through
// Handler.ServeHTTP with the real git client. If git ever stops
// applying such a push, or the gate starts admitting it (TASK-016 may
// widen it), this test fails and those statements must change with it.
func TestPreReceive_ShallowClonePushStillRefused(t *testing.T) {
	repo, base, _, _ := nulRepo(t, "")
	// A second commit on main, so a depth-1 clone has a shallow
	// boundary (a root commit has no parents to cut off).
	tip := nulGit(t, repo, nil, "commit-tree", base+"^{tree}", "-p", base, "-m", "second")
	nulGit(t, repo, nil, "update-ref", "refs/heads/main", tip, base)
	gitRepo := t.TempDir()
	clone := exec.Command("git", "clone", "-q", "--bare", repo, gitRepo)
	clone.Env = nulGitEnv(t)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}

	work := t.TempDir()
	// --depth needs a transport; a plain path would hard-link a full
	// local clone instead.
	shallow := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+repo, work)
	shallow.Env = nulGitEnv(t)
	if out, err := shallow.CombinedOutput(); err != nil {
		t.Fatalf("shallow clone: %v %s", err, out)
	}
	if got := nulGit(t, work, nil, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Fatalf("clone is not shallow (%s); the test would prove nothing", got)
	}
	nulGit(t, work, nil, "commit", "-q", "--allow-empty", "-m", "work")
	head := nulGit(t, work, nil, "rev-parse", "HEAD")
	push := func(url string) (string, error) {
		cmd := exec.Command("git", "-C", work, "push", "--porcelain", url, "HEAD:refs/heads/feature")
		cmd.Env = nulGitEnv(t)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := push(gitRepo); err != nil {
		t.Fatalf("plain git refused the shallow-clone push (the statement is no longer needed?): %v\n%s", err, out)
	}
	if got := nulGit(t, gitRepo, nil, "rev-parse", "refs/heads/feature"); got != head {
		t.Fatalf("plain git: feature = %s, want %s", got, head)
	}

	h := nulHandler(t, repo)
	em := &nulEmitter{}
	actor := &domain.Actor{ActorID: "actor-contributor", Role: domain.RoleContributor}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithRepoPath(r.Context(), repo)
		ctx = WithEvents(ctx, em)
		ctx = domain.WithActor(ctx, actor)
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer srv.Close()

	out, err := push(srv.URL + "/")
	if err == nil {
		t.Fatalf("shallow-clone push was admitted through the gate:\n%s", out)
	}
	if want := `branch-protection: malformed push: command 1: malformed ref update "shallow `; !strings.Contains(out, want) {
		t.Errorf("expected %q in the refusal, got:\n%s", want, out)
	}
	if nulRefExists(t, repo, "refs/heads/feature") {
		t.Error("feature was created through the gate")
	}
	if got := nulGit(t, repo, nil, "rev-parse", "refs/heads/main"); got != tip {
		t.Errorf("main moved: %s -> %s", tip, got)
	}
	if n := em.count(); n != 0 {
		t.Errorf("events emitted = %d, want 0", n)
	}
}
