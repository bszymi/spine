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
//   - contributor: a multi-ref push (spine/run/* included), a push with
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
