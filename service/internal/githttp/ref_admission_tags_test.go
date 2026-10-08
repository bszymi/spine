package githttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/core/domain"
)

// The F10 gateway half (audit 2026-10-06, INIT-023 TASK-016 AC 10),
// ported from the audit's push PoC with real git through the real
// handler (git-http-backend CGI behind the pre-receive gate). git
// resolves a bare branch name as refs/<n>, then refs/tags/<n>, then
// refs/heads/<n>, so a pushed tag named after a run branch made the
// victim's next governed merge publish the tag's commit, and a tag
// named `main` made every later `git push origin main` ambiguous (a
// permanent error: every governed merge in the workspace failed).
// Before TASK-016 any authenticated pusher could create both; branch
// protection allows every non-branch ref (ADR-009 §6) and nothing else
// gated them.

const tagVictimRun = "spine/run/task-001-victim-0a1b2c3d"

func tagGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = nulGitEnv(t)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func tagMust(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := tagGit(t, dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

// tagSetup builds origin (bare) <- engine (non-bare: the repo the
// standalone engine and the git endpoint share) holding main and a
// reviewed run branch, plus an attacker clone with an unreviewed
// commit. Returns the engine repo, the attacker clone and the
// attacker's commit.
func tagSetup(t *testing.T) (engine, attacker, evil string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	engine = filepath.Join(root, "engine")
	attacker = filepath.Join(root, "attacker")
	tagMust(t, root, "init", "-q", "--bare", "-b", "main", origin)
	tagMust(t, root, "clone", "-q", origin, engine)
	tagMust(t, engine, "checkout", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(engine, "f.txt"), []byte("legit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tagMust(t, engine, "add", ".")
	tagMust(t, engine, "commit", "-qm", "base")
	tagMust(t, engine, "push", "-q", "origin", "main")
	tagMust(t, engine, "checkout", "-q", "-b", tagVictimRun, "main")
	if err := os.WriteFile(filepath.Join(engine, "reviewed.txt"), []byte("reviewed change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tagMust(t, engine, "add", ".")
	tagMust(t, engine, "commit", "-qm", "reviewed work")
	tagMust(t, engine, "checkout", "-q", "main")
	tagMust(t, root, "clone", "-q", engine, attacker)
	if err := os.WriteFile(filepath.Join(attacker, "f.txt"), []byte("UNREVIEWED ATTACKER CONTENT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tagMust(t, attacker, "commit", "-qam", "attacker")
	return engine, attacker, tagMust(t, attacker, "rev-parse", "HEAD")
}

// tagServer serves repo with main protected (no-direct-write,
// no-delete) and one URL prefix per principal: /anon/ (no actor — the
// gateway itself never forwards an anonymous push since INIT-023
// TASK-015, so this exercises the handler's own defence), /contributor/
// and /operator/. It wires no run reader: none of these refs is a run
// branch.
func tagServer(t *testing.T, repo string) func(principal string) string {
	t.Helper()
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
		Policy: branchprotect.New(branchprotect.StaticRules([]config.Rule{{
			Branch:      "main",
			Protections: []config.RuleKind{config.KindNoDirectWrite, config.KindNoDelete},
		}})),
	})
	if err != nil {
		t.Skipf("git-http-backend unavailable: %v", err)
	}
	actors := map[string]*domain.Actor{
		"anon":        nil,
		"contributor": {ActorID: "c-1", Role: domain.RoleContributor},
		"operator":    {ActorID: "op-1", Role: domain.RoleOperator},
	}
	mux := http.NewServeMux()
	for name, a := range actors {
		a := a
		prefix := "/" + name
		mux.Handle(prefix+"/", http.StripPrefix(prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithRepoPath(r.Context(), repo)
			if a != nil {
				ctx = domain.WithActor(ctx, a)
			}
			h.ServeHTTP(w, r.WithContext(ctx))
		})))
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return func(principal string) string { return srv.URL + "/" + principal + "/" }
}

// TestRefAdmission_ShadowTagsRefused is the inverted F10 PoC: tags,
// notes and other non-branch refs are refused for anonymous and
// contributor pushers and leave nothing on the server, while an
// operator can still push a release tag.
func TestRefAdmission_ShadowTagsRefused(t *testing.T) {
	engine, attacker, evil := tagSetup(t)
	url := tagServer(t, engine)

	// Control: main stays protected by its rule.
	out, err := tagGit(t, attacker, "push", url("contributor"), evil+":refs/heads/main")
	if err == nil || !strings.Contains(out, `rule "no-direct-write"`) {
		t.Fatalf("direct write to main: err=%v, want the no-direct-write refusal\n%s", err, out)
	}

	for _, principal := range []string{"anon", "contributor"} {
		for _, dst := range []string{
			"refs/tags/" + tagVictimRun,
			"refs/tags/main",
			"refs/notes/commits",
			"refs/" + tagVictimRun,
		} {
			out, err := tagGit(t, attacker, "push", url(principal), evil+":"+dst)
			if err == nil {
				t.Errorf("%s push to %s was accepted\n%s", principal, dst, out)
				continue
			}
			if !strings.Contains(out, "ref-admission: "+dst+" is not a branch; only operators may push tags or other non-branch refs") {
				t.Errorf("%s push to %s: want the ref-admission refusal, got\n%s", principal, dst, out)
			}
		}
	}
	if refs := tagMust(t, engine, "for-each-ref", "refs/tags", "refs/notes", "refs/spine"); refs != "" {
		t.Fatalf("non-branch refs landed on the server:\n%s", refs)
	}

	if out, err := tagGit(t, attacker, "push", url("operator"), evil+":refs/tags/v1.0.0"); err != nil {
		t.Fatalf("operator release tag refused: %v\n%s", err, out)
	}
	if got := tagMust(t, engine, "rev-parse", "refs/tags/v1.0.0"); got != evil {
		t.Errorf("refs/tags/v1.0.0 = %s, want %s", got, evil)
	}
}

// TestRefAdmission_ShadowingNamesRefusedForOperators pins the
// INIT-023 TASK-002 D12 rule on the gateway: a ref with a 'refs'
// component after the first, or a 'HEAD' component, makes every client
// push of the branch it shadows fail ("dst refspec refs/heads/main
// matches more than one"), so nobody may create one, operators
// included; deleting one stays possible.
func TestRefAdmission_ShadowingNamesRefusedForOperators(t *testing.T) {
	engine, attacker, evil := tagSetup(t)
	url := tagServer(t, engine)

	for _, dst := range []string{
		"refs/heads/refs/heads/main",
		"refs/tags/refs/heads/main",
		"refs/refs/heads/main",
		"refs/heads/HEAD",
	} {
		out, err := tagGit(t, attacker, "push", url("operator"), evil+":"+dst)
		if err == nil {
			t.Errorf("operator push to %s was accepted\n%s", dst, out)
			continue
		}
		if !strings.Contains(out, "ref-admission: "+dst+" has a 'refs' component after the first or a 'HEAD' component") {
			t.Errorf("operator push to %s: want the shadowing refusal, got\n%s", dst, out)
		}
	}
	if refs := tagMust(t, engine, "for-each-ref", "refs/heads/refs", "refs/tags", "refs/refs", "refs/heads/HEAD"); refs != "" {
		t.Fatalf("shadowing refs landed on the server:\n%s", refs)
	}

	// A shadow created before the upgrade can still be cleaned up.
	tagMust(t, engine, "update-ref", "refs/heads/refs/heads/main", tagMust(t, engine, "rev-parse", "main"))
	if out, err := tagGit(t, attacker, "push", url("contributor"), ":refs/heads/refs/heads/main"); err != nil {
		t.Fatalf("deleting a shadowing branch was refused: %v\n%s", err, out)
	}
	if _, err := tagGit(t, engine, "rev-parse", "--verify", "-q", "refs/heads/refs/heads/main"); err == nil {
		t.Errorf("refs/heads/refs/heads/main still exists after the delete")
	}
}
