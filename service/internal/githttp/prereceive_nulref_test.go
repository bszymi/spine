package githttp

// Regression tests for the 2026-10-06 audit finding F1 (P1), INIT-023
// TASK-001. A NUL placed after the ref on the 2nd+ receive-pack command
// line was applied by git as the literal ref (git ends every command's
// ref at its first NUL) while the gate evaluated "refs/heads/<b>\x00",
// matched no rule and allowed it — a contributor could advance or
// delete a literal-protected branch, with no override event.
//
// Ported from the audit PoCs (verify-r1-0/nulref_verify_test.go and
// governance/nul_ref_bypass_test.go) and inverted: every attack must now
// be refused as a malformed push, nothing may move, and no override
// event may be emitted. They drive the real git-http-backend CGI through
// Handler.ServeHTTP, so a gate decision is checked against what git
// actually does with the bytes.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/core/domain"
)

// nulGitEnv returns an environment for test-side git commands that
// ignores the host's global and system git config (a developer's
// commit.gpgsign or init.defaultObjectFormat=sha256 would otherwise
// change what these tests build) and never prompts.
func nulGitEnv(t *testing.T) []string {
	t.Helper()
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
}

// nulGit runs git in dir and returns trimmed stdout, failing the test on
// error.
func nulGit(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = nulGitEnv(t)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// nulRefExists reports whether ref resolves in repo.
func nulRefExists(t *testing.T, repo, ref string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", "-q", ref)
	cmd.Env = nulGitEnv(t)
	return cmd.Run() == nil
}

// nulEmitter counts emitted events. Guarded by a mutex: in the
// real-client test the handler runs on an httptest server goroutine and
// the only synchronisation with the test goroutine is an external git
// process, which the race detector cannot see.
type nulEmitter struct {
	mu     sync.Mutex
	events []domain.Event
}

func (e *nulEmitter) Emit(_ context.Context, ev domain.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	return nil
}

func (e *nulEmitter) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.events)
}

// nulRepo builds a bare repo whose main (HEAD) and the given extra
// branches point at one base commit, plus an unreferenced attacker
// commit, and returns (repo, base, evil, pack(evil)). The attacker
// commit carries the governance-takeover shape from the audit: an
// unreviewed EVIL.md and a .spine/branch-protection.yaml with
// `rules: []`.
func nulRepo(t *testing.T, objectFormat string, branches ...string) (repo, base, evil string, pack []byte) {
	t.Helper()
	src := t.TempDir()
	initArgs := []string{"init", "-q", "-b", "main"}
	if objectFormat != "" {
		initArgs = append(initArgs, "--object-format="+objectFormat)
	}
	nulGit(t, src, nil, initArgs...)
	nulGit(t, src, nil, "commit", "-q", "--allow-empty", "-m", "base")
	for _, b := range branches {
		nulGit(t, src, nil, "branch", b)
	}
	repo = t.TempDir()
	clone := exec.Command("git", "clone", "-q", "--bare", src, repo)
	clone.Env = nulGitEnv(t)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	base = nulGit(t, repo, nil, "rev-parse", "refs/heads/main")
	evilBlob := nulGit(t, repo, []byte("pwned\n"), "hash-object", "-w", "--stdin")
	rulesBlob := nulGit(t, repo, []byte("rules: []\n"), "hash-object", "-w", "--stdin")
	spineTree := nulGit(t, repo, []byte("100644 blob "+rulesBlob+"\tbranch-protection.yaml\n"), "mktree")
	tree := nulGit(t, repo, []byte("040000 tree "+spineTree+"\t.spine\n100644 blob "+evilBlob+"\tEVIL.md\n"), "mktree")
	evil = nulGit(t, repo, nil, "commit-tree", tree, "-p", base, "-m", "unreviewed")
	cmd := exec.Command("git", "-C", repo, "pack-objects", "--stdout", "-q", "--revs")
	cmd.Env = nulGitEnv(t)
	cmd.Stdin = strings.NewReader(evil + "\n")
	var err error
	if pack, err = cmd.Output(); err != nil {
		t.Fatalf("pack-objects: %v", err)
	}
	return repo, base, evil, pack
}

// nulHandler returns a push-enabled handler for repo. With no rules it
// uses the ADR-009 bootstrap defaults (main: no-delete +
// no-direct-write).
func nulHandler(t *testing.T, repo string, rules ...config.Rule) *Handler {
	t.Helper()
	var src branchprotect.RuleSource = branchprotect.StaticRules(nil)
	if len(rules) > 0 {
		src = branchprotect.StaticRules(rules)
	}
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
		Policy:             branchprotect.New(src),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// nulPush posts a hand-built receive-pack body as the given role and
// returns the response body.
func nulPush(t *testing.T, h *Handler, repo string, role domain.ActorRole, em *nulEmitter, body []byte) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/git-receive-pack", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	ctx := WithRepoPath(req.Context(), repo)
	ctx = WithEvents(ctx, em)
	ctx = domain.WithActor(ctx, &domain.Actor{ActorID: "actor-" + string(role), Role: role})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// nulBody frames the command lines, a flush, an optional push-options
// block (options != nil), and the pack.
func nulBody(lines []string, options []string, pack []byte) []byte {
	var b []byte
	for _, l := range lines {
		b = append(b, pktLine(l)...)
	}
	b = append(b, flushPkt...)
	if options != nil {
		for _, o := range options {
			b = append(b, pktLine(o+"\n")...)
		}
		b = append(b, flushPkt...)
	}
	return append(b, pack...)
}

// assertMalformedRefusal checks a refusal is the parser's all-or-nothing
// malformed-push denial and echoes no raw NUL back to the client.
func assertMalformedRefusal(t *testing.T, label, resp, want string) {
	t.Helper()
	if !strings.Contains(resp, "branch-protection: malformed push: "+want) {
		t.Errorf("%s: expected malformed-push rejection containing %q, got %q", label, want, resp)
	}
	if strings.Contains(resp, "pre-receive hook declined") {
		t.Errorf("%s: malformed push must carry no per-ref ng lines: %q", label, resp)
	}
	if strings.IndexByte(resp, 0) >= 0 {
		t.Errorf("%s: response echoes a raw NUL: %q", label, resp)
	}
}

// nulRoles are the pushers the inverted PoCs run as: a contributor (the
// audit's attacker) and an operator pushing WITHOUT -o spine.override,
// for whom the bypass was an unaudited override.
var nulRoles = []domain.ActorRole{domain.RoleContributor, domain.RoleOperator}

// TestPreReceive_NulAfterRefOnLaterLine_DirectWriteRefused is the
// inverted verify-r1-0 / governance PoC: main stays put (so the
// `rules: []` takeover never lands), no override event is emitted, and
// the line-1 decoy is not created either — the whole push is refused.
func TestPreReceive_NulAfterRefOnLaterLine_DirectWriteRefused(t *testing.T) {
	variants := []struct {
		name, decoy, line2Suffix string
	}{
		{"nul", "refs/heads/feature-x", "\x00\n"},
		{"nul+caps", "refs/heads/feature-x", "\x00report-status\n"},
		{"nul+junk", "refs/heads/feature-x", "\x00garbage\n"},
		{"spine-run decoy", "refs/heads/spine/run/decoy", "\x00\n"},
	}
	for _, role := range nulRoles {
		t.Run(string(role), func(t *testing.T) {
			repo, base, evil, pack := nulRepo(t, "")
			h := nulHandler(t, repo)
			em := &nulEmitter{}
			z := zeroSHA

			// Control: the honest line-2 update is denied by its rule.
			ctrl := nulPush(t, h, repo, role, em, nulBody([]string{
				z + " " + evil + " refs/heads/feature-x\x00report-status delete-refs\n",
				base + " " + evil + " refs/heads/main\n",
			}, nil, pack))
			if !strings.Contains(ctrl, `rule "no-direct-write"`) {
				t.Fatalf("control push not denied by no-direct-write: %q", ctrl)
			}

			for _, v := range variants {
				resp := nulPush(t, h, repo, role, em, nulBody([]string{
					z + " " + evil + " " + v.decoy + "\x00report-status delete-refs\n",
					base + " " + evil + " refs/heads/main" + v.line2Suffix,
				}, nil, pack))
				assertMalformedRefusal(t, v.name, resp, `command 2: invalid ref name: ref "refs/heads/main\x00`)
				if strings.Contains(resp, "ok refs/heads/main") {
					t.Errorf("%s: git reported main updated: %q", v.name, resp)
				}
				if got := nulGit(t, repo, nil, "rev-parse", "refs/heads/main"); got != base {
					t.Fatalf("%s: BYPASS — main moved %s -> %s", v.name, base[:10], got[:10])
				}
				if nulRefExists(t, repo, v.decoy) {
					t.Errorf("%s: decoy %s was created; the whole push must be refused", v.name, v.decoy)
				}
			}
			if n := em.count(); n != 0 {
				t.Errorf("override events emitted = %d, want 0", n)
			}
		})
	}
}

// TestPreReceive_NulAfterRefOnLaterLine_DeleteRefused is the inverted
// delete variant: literal no-delete branches that are not HEAD (so git's
// receive.denyDeleteCurrent does not save them) must survive, as must
// the line-1 decoy delete. release/* is a wildcard rule — never
// bypassable, because `*` matches the NUL — and is kept for parity with
// SMP's suite.
func TestPreReceive_NulAfterRefOnLaterLine_DeleteRefused(t *testing.T) {
	for _, role := range nulRoles {
		t.Run(string(role), func(t *testing.T) {
			repo, _, _, _ := nulRepo(t, "", "develop", "release/1", "feature-y")
			h := nulHandler(t, repo,
				config.Rule{Branch: "main", Protections: []config.RuleKind{config.KindNoDelete, config.KindNoDirectWrite}},
				config.Rule{Branch: "develop", Protections: []config.RuleKind{config.KindNoDelete}},
				config.Rule{Branch: "release/*", Protections: []config.RuleKind{config.KindNoDelete}},
			)
			em := &nulEmitter{}
			dev := nulGit(t, repo, nil, "rev-parse", "refs/heads/develop")
			z := zeroSHA

			ctrl := nulPush(t, h, repo, role, em, nulBody([]string{
				dev + " " + z + " refs/heads/feature-y\x00report-status delete-refs\n",
				dev + " " + z + " refs/heads/develop\n",
			}, nil, nil))
			if !strings.Contains(ctrl, `rule "no-delete"`) {
				t.Fatalf("control delete not denied by no-delete: %q", ctrl)
			}

			for _, target := range []string{"refs/heads/develop", "refs/heads/release/1"} {
				resp := nulPush(t, h, repo, role, em, nulBody([]string{
					dev + " " + z + " refs/heads/feature-y\x00report-status delete-refs\n",
					dev + " " + z + " " + target + "\x00\n",
				}, nil, nil))
				assertMalformedRefusal(t, target, resp, "command 2: invalid ref name")
			}
			for _, ref := range []string{"refs/heads/develop", "refs/heads/release/1", "refs/heads/feature-y", "refs/heads/main"} {
				if !nulRefExists(t, repo, ref) {
					t.Errorf("BYPASS — %s was deleted; the whole push must be refused", ref)
				}
			}
			if n := em.count(); n != 0 {
				t.Errorf("override events emitted = %d, want 0", n)
			}
		})
	}
}

// TestPreReceive_WellFormedMultiRefPushStillApplies proves the fix does
// not change well-formed pushes: a two-command push (caps on line 1
// only) to unprotected branches is admitted and applied by git. It
// passes before and after the fix.
func TestPreReceive_WellFormedMultiRefPushStillApplies(t *testing.T) {
	repo, _, evil, pack := nulRepo(t, "")
	h := nulHandler(t, repo)
	em := &nulEmitter{}
	z := zeroSHA
	resp := nulPush(t, h, repo, domain.RoleContributor, em, nulBody([]string{
		z + " " + evil + " refs/heads/feature-a\x00report-status side-band-64k agent=git/2.54.0\n",
		z + " " + evil + " refs/heads/spine/run/task-001-abcd1234\n",
	}, nil, pack))
	if strings.Contains(resp, "pre-receive hook declined") || strings.Contains(resp, "malformed push") {
		t.Fatalf("well-formed push was refused: %q", resp)
	}
	for _, ref := range []string{"refs/heads/feature-a", "refs/heads/spine/run/task-001-abcd1234"} {
		if got := nulGit(t, repo, nil, "rev-parse", ref); got != evil {
			t.Errorf("%s = %s, want %s", ref, got, evil)
		}
	}
	if n := em.count(); n != 0 {
		t.Errorf("events emitted = %d, want 0", n)
	}
}

// TestPreReceive_SHA256NullOIDDeleteRefusedByNoDelete: in a SHA-256
// repository the null new-oid is 64 zeros. Before INIT-023 TASK-001
// IsDelete knew only the 40-zero form, so the gate evaluated the delete
// as a direct write and a no-delete-only rule let it through.
//
// Must PASS, not SKIP, in CI: every git since 2.29 supports
// --object-format=sha256, so a skip there would hide a regression. The
// skip is allowed only off-CI (GitHub Actions sets CI=true).
func TestPreReceive_SHA256NullOIDDeleteRefusedByNoDelete(t *testing.T) {
	probe := exec.Command("git", "init", "-q", "--object-format=sha256", t.TempDir())
	probe.Env = nulGitEnv(t)
	if out, err := probe.CombinedOutput(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("git on this CI runner cannot create a SHA-256 repository: %v %s", err, out)
		}
		t.Skipf("git without SHA-256 object-format support: %v %s", err, out)
	}
	repo, _, _, _ := nulRepo(t, "sha256", "develop", "feature-y")
	h := nulHandler(t, repo, config.Rule{Branch: "develop", Protections: []config.RuleKind{config.KindNoDelete}})
	em := &nulEmitter{}
	dev := nulGit(t, repo, nil, "rev-parse", "refs/heads/develop")
	if len(dev) != 64 {
		t.Fatalf("not a sha256 repo: %s", dev)
	}
	z := strings.Repeat("0", 64)

	resp := nulPush(t, h, repo, domain.RoleContributor, em, nulBody([]string{
		dev + " " + z + " refs/heads/develop\x00report-status delete-refs object-format=sha256\n",
	}, nil, nil))
	if !nulRefExists(t, repo, "refs/heads/develop") {
		t.Fatal("BYPASS — no-delete branch develop deleted in a SHA-256 repo")
	}
	if !strings.Contains(resp, `rule "no-delete"`) {
		t.Errorf("expected no-delete denial, got %q", resp)
	}

	// Control: the same 64-zero delete of an unprotected branch lands,
	// so the body above is one git accepts and the denial is the rule's.
	resp = nulPush(t, h, repo, domain.RoleContributor, em, nulBody([]string{
		dev + " " + z + " refs/heads/feature-y\x00report-status delete-refs object-format=sha256\n",
	}, nil, nil))
	if nulRefExists(t, repo, "refs/heads/feature-y") {
		t.Errorf("unprotected SHA-256 delete did not apply: %q", resp)
	}
	if n := em.count(); n != 0 {
		t.Errorf("events emitted = %d, want 0", n)
	}
}

// TestPreReceive_Line1CapabilityListRefused pins that the gate and git
// read the same capability set from line 1 (parity with SMP INIT-027
// EPIC-004 TASK-001 §1 step 3a). git reads the list as a C string — it
// ends at a second NUL — so before the guard:
//
//   - (a) "report-status\x00 push-options": the gate saw push-options,
//     read the options block, and honoured an operator's
//     spine.override=true with one branch_protection.override event;
//     git saw no push-options, read the options block as pack data and
//     applied nothing — an audit event for a push that never landed.
//   - (b) "report-status push-options\x00x": git saw push-options and
//     the gate did not; the push applied with options the gate never
//     read.
//
// Both are now refused as a malformed capability list, nothing moves,
// and no event is emitted.
func TestPreReceive_Line1CapabilityListRefused(t *testing.T) {
	repo, base, evil, pack := nulRepo(t, "")
	h := nulHandler(t, repo)
	em := &nulEmitter{}
	z := zeroSHA

	respA := nulPush(t, h, repo, domain.RoleOperator, em, nulBody([]string{
		base + " " + evil + " refs/heads/main\x00report-status\x00 push-options\n",
	}, []string{"spine.override=true"}, pack))
	assertMalformedRefusal(t, "hidden push-options", respA, `command 1: malformed capability list "report-status\x00 push-options"`)
	if got := nulGit(t, repo, nil, "rev-parse", "refs/heads/main"); got != base {
		t.Fatalf("main moved %s -> %s", base[:10], got[:10])
	}

	respB := nulPush(t, h, repo, domain.RoleContributor, em, nulBody([]string{
		z + " " + evil + " refs/heads/feature\x00report-status push-options\x00x\n",
	}, []string{"ci.skip"}, pack))
	assertMalformedRefusal(t, "revealed push-options", respB, "command 1: malformed capability list")
	if nulRefExists(t, repo, "refs/heads/feature") {
		t.Error("feature was created by a push whose capability list the gate did not read as git does")
	}
	if n := em.count(); n != 0 {
		t.Errorf("override events emitted = %d, want 0", n)
	}
}
