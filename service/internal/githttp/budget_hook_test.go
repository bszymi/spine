package githttp

// INIT-024 TASK-003 (review finding N-1): the checkout-budget
// pre-receive hook, driven through Handler.ServeHTTP with the real git
// client. The hook script execs this test binary, whose package init
// runs PreReceiveHookMain exactly as `spine git-hook pre-receive` does.
// Fixtures are a handful of tiny objects: the "bomb" is 50 entries of
// one 4 KiB blob (200 KiB when checked out), refused by a 64 KiB budget.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// hookTestBudget refuses the 200 KiB bomb on bytes and admits a commit of
// a few small files.
var hookTestBudget = git.TreeBudget{MaxEntries: 1000, MaxTotalBytes: 64 << 10, MaxPathBytesTotal: 1 << 20}

// budgetHandler is nulHandler with the small checkout budget. Its hook
// execs this test binary, which runs the hook from the package init
// (HookProcessEnv) exactly as the spine binary does.
func budgetHandler(t *testing.T, repo string) *Handler {
	t.Helper()
	h := nulHandler(t, repo)
	h.budgetHook.budget = hookTestBudget
	return h
}

// budgetServer serves h as a contributor (who may push feature branches).
func budgetServer(t *testing.T, h *Handler, repo string) string {
	t.Helper()
	actor := &domain.Actor{ActorID: "actor-contributor", Role: domain.RoleContributor}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithRepoPath(r.Context(), repo)
		ctx = WithEvents(ctx, &nulEmitter{})
		ctx = domain.WithActor(ctx, actor)
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}

// budgetClone clones url and returns the work tree.
func budgetClone(t *testing.T, url string) string {
	t.Helper()
	work := t.TempDir()
	clone := exec.Command("git", "clone", "-q", url, work)
	clone.Env = nulGitEnv(t)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	return work
}

// pushBomb builds the review PoC's amplifying commit in work (one 4 KiB
// blob named by 50 entries) on top of HEAD and returns its id.
func pushBomb(t *testing.T, work string) string {
	t.Helper()
	blob := nulGit(t, work, bytes.Repeat([]byte("x"), 4<<10), "hash-object", "-w", "--stdin")
	var mk strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&mk, "100644 blob %s\tf%04d.md\n", blob, i)
	}
	inner := nulGit(t, work, []byte(mk.String()), "mktree")
	root := nulGit(t, work, []byte("040000 tree "+inner+"\tdocs\n"), "mktree")
	return nulGit(t, work, nil, "commit-tree", root, "-p", "HEAD", "-m", "bomb")
}

func gitPush(t *testing.T, work, url string, refspecs ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", work, "push", "--porcelain", url}, refspecs...)...)
	cmd.Env = nulGitEnv(t)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestPrereceive_RefusesOverBudgetTreePush: with receive-pack on, the
// amplifying push is refused by the pre-receive hook with a "remote:"
// line naming the budget and its variable, the ref does not move and the
// pushed objects are not in the repository. A push within the budget
// lands. At a560c210 the amplifying push landed.
func TestPrereceive_RefusesOverBudgetTreePush(t *testing.T) {
	repo, _, _, _ := nulRepo(t, "")
	h := budgetHandler(t, repo)
	url := budgetServer(t, h, repo)
	work := budgetClone(t, url)

	bomb := pushBomb(t, work)
	out, err := gitPush(t, work, url, bomb+":refs/heads/feature-bomb")
	if err == nil {
		t.Fatalf("the amplifying push landed:\n%s", out)
	}
	t.Logf("refused push:\n%s", out)
	for _, want := range []string{"remote: spine: push refused", git.TreeTooLargeMessage, "MaxTotalBytes", EnvTreeMaxBytes, "pre-receive hook declined"} {
		if !strings.Contains(out, want) {
			t.Errorf("push output lacks %q:\n%s", want, out)
		}
	}
	if nulRefExists(t, repo, "refs/heads/feature-bomb") {
		t.Fatal("feature-bomb was created")
	}
	if objectExists(t, repo, bomb) {
		t.Fatal("the refused push's commit is in the repository (quarantine not discarded)")
	}

	// Within the budget: a normal commit lands through the same hook.
	testFile := filepath.Join(work, "ok.md")
	if err := os.WriteFile(testFile, []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nulGit(t, work, nil, "add", "ok.md")
	nulGit(t, work, nil, "commit", "-q", "-m", "ok")
	if out, err := gitPush(t, work, url, "HEAD:refs/heads/feature-ok"); err != nil {
		t.Fatalf("a push within the budget was refused: %v\n%s", err, out)
	}
	if got, want := nulGit(t, repo, nil, "rev-parse", "refs/heads/feature-ok"), nulGit(t, work, nil, "rev-parse", "HEAD"); got != want {
		t.Fatalf("feature-ok = %s, want %s", got, want)
	}
	hook, err := os.ReadFile(filepath.Join(repo, "hooks", "pre-receive"))
	if err != nil || !bytes.Contains(hook, []byte(budgetHookMarker)) {
		t.Fatalf("installed hook: %v\n%s", err, hook)
	}
	// The served repository carries the neutral checkout attributes.
	if ok, err := git.HasNeutralCheckoutAttributes(context.Background(), repo); !ok || err != nil {
		t.Fatalf("served repository lacks the neutral checkout attributes: %v %v", ok, err)
	}
}

// TestPrereceive_BudgetHookCannotBeRedirected: a core.hooksPath in the
// repository's config does not take the hook away (the CGI pins it), a
// deleted or edited hook is rewritten on the next push, and a
// pre-receive hook Spine did not write is never replaced — the push is
// refused instead (fail closed).
func TestPrereceive_BudgetHookCannotBeRedirected(t *testing.T) {
	repo, _, _, _ := nulRepo(t, "")
	h := budgetHandler(t, repo)
	url := budgetServer(t, h, repo)
	work := budgetClone(t, url)
	empty := t.TempDir()
	nulGit(t, repo, nil, "config", "core.hooksPath", empty)

	bomb := pushBomb(t, work)
	if out, err := gitPush(t, work, url, bomb+":refs/heads/b1"); err == nil || !strings.Contains(out, "remote: spine: push refused") {
		t.Fatalf("core.hooksPath in the repository config bypassed the hook: %v\n%s", err, out)
	}

	hookPath := filepath.Join(repo, "hooks", "pre-receive")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n"+budgetHookMarker+"\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := gitPush(t, work, url, bomb+":refs/heads/b2"); err == nil || !strings.Contains(out, "remote: spine: push refused") {
		t.Fatalf("an edited Spine hook was not rewritten: %v\n%s", err, out)
	}
	if err := os.Remove(hookPath); err != nil {
		t.Fatal(err)
	}
	if out, err := gitPush(t, work, url, bomb+":refs/heads/b3"); err == nil || !strings.Contains(out, "remote: spine: push refused") {
		t.Fatalf("a deleted hook was not reinstalled: %v\n%s", err, out)
	}

	foreign := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(hookPath, foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := gitPush(t, work, url, "HEAD:refs/heads/b4"); err == nil {
		t.Fatalf("a push went through with a foreign pre-receive hook in place:\n%s", out)
	}
	if got, _ := os.ReadFile(hookPath); !bytes.Equal(got, foreign) {
		t.Fatal("a foreign pre-receive hook was replaced")
	}
	for _, ref := range []string{"b1", "b2", "b3", "b4"} {
		if nulRefExists(t, repo, "refs/heads/"+ref) {
			t.Errorf("%s was created", ref)
		}
	}
}

// TestBudgetHook_ScriptQuoting: the script is one exec line whose words
// are single-quoted; a word holding a newline is refused.
func TestBudgetHook_ScriptQuoting(t *testing.T) {
	b := budgetHook{argv: []string{"/opt/it's here/spine", "git-hook", "pre-receive"}, budget: git.DefaultTreeBudget(), timeout: 30e9}
	s, err := b.script()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, `exec '/opt/it'\''s here/spine' 'git-hook' 'pre-receive' '--max-entries=200000'`) || !strings.Contains(s, "'--timeout=30s'") ||
		!strings.Contains(s, "\nexport "+HookProcessEnv+"\n") {
		t.Fatalf("script:\n%s", s)
	}
	b.argv = []string{"/bin/spine\nrm -rf /"}
	if _, err := b.script(); err == nil {
		t.Fatal("a newline in the command was accepted")
	}
	if got := hooksPathEnv("/r/it's!/hooks"); got != `GIT_CONFIG_PARAMETERS='core.hooksPath=/r/it'\''s'\!'/hooks'` {
		t.Fatalf("hooksPathEnv = %s", got)
	}
}

// TestBudgetHook_ProcessEntryRefusesMisuse: a process started with
// HookProcessEnv but without the script's argv (or outside git) refuses
// instead of running as anything else.
func TestBudgetHook_ProcessEntryRefusesMisuse(t *testing.T) {
	var stderr bytes.Buffer
	t.Setenv("GIT_DIR", "")
	if code := runHookProcess([]string{"git-hook", "pre-receive"}, strings.NewReader(""), &stderr); code != 2 {
		t.Fatalf("outside git: exit %d", code)
	}
	t.Setenv("GIT_DIR", ".")
	if code := runHookProcess([]string{"-test.run=."}, strings.NewReader(""), &stderr); code != 2 {
		t.Fatalf("wrong argv: exit %d", code)
	}
	if !strings.Contains(stderr.String(), HookProcessEnv) {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func objectExists(t *testing.T, repo, oid string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "cat-file", "-e", oid)
	cmd.Env = nulGitEnv(t)
	return cmd.Run() == nil
}

// TestBudgetHook_TagsArePeeledAndTreesMeasuredOnce (review round 1, P3):
// an operator's `git push --tags` carrying a tag of a blob is admitted
// (nothing checks a blob out), and many tags of one commit cost one
// measure of its tree, not one per tag.
func TestBudgetHook_TagsArePeeledAndTreesMeasuredOnce(t *testing.T) {
	repo, _, _, _ := nulRepo(t, "")
	h := budgetHandler(t, repo)
	actor := &domain.Actor{ActorID: "actor-operator", Role: domain.RoleOperator}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithRepoPath(r.Context(), repo)
		ctx = WithEvents(ctx, &nulEmitter{})
		ctx = domain.WithActor(ctx, actor)
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer srv.Close()
	url := srv.URL + "/"
	work := budgetClone(t, url)
	key := nulGit(t, work, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\n"), "hash-object", "-w", "--stdin")
	nulGit(t, work, nil, "tag", "-a", "-m", "key", "gpg-key", key)
	for i := 0; i < 5; i++ {
		nulGit(t, work, nil, "tag", "-a", "-m", "r", fmt.Sprintf("v1.%d", i), "HEAD")
	}
	if out, err := gitPush(t, work, url, "--tags"); err != nil {
		t.Fatalf("--tags push with a blob tag refused: %v\n%s", err, out)
	}
	if !nulRefExists(t, repo, "refs/tags/gpg-key") || !nulRefExists(t, repo, "refs/tags/v1.4") {
		t.Fatal("tags did not land")
	}

	// In-process, so the measures can be counted: five tags of one commit
	// and the branch are one tree.
	var measured []string
	measureHookForTest = func(tree string) { measured = append(measured, tree) }
	t.Cleanup(func() { measureHookForTest = nil })
	head := nulGit(t, repo, nil, "rev-parse", "refs/heads/main")
	var in strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&in, "%s %s refs/tags/v1.%d\n", zeroSHA, nulGit(t, repo, nil, "rev-parse", fmt.Sprintf("refs/tags/v1.%d", i)), i)
	}
	fmt.Fprintf(&in, "%s %s refs/heads/again\n", zeroSHA, head)
	fmt.Fprintf(&in, "%s %s refs/tags/gpg-key\n", zeroSHA, nulGit(t, repo, nil, "rev-parse", "refs/tags/gpg-key"))
	t.Chdir(repo)
	t.Setenv("GIT_DIR", ".")
	var stderr bytes.Buffer
	if err := PreReceiveHookMain(context.Background(), nil, strings.NewReader(in.String()), &stderr); err != nil {
		t.Fatalf("hook: %v %s", err, stderr.String())
	}
	if len(measured) != 1 {
		t.Fatalf("measured %d trees (%v), want 1", len(measured), measured)
	}
}

// TestBudgetHook_WarnsOnceWhenHooksPathIsOverridden (review round 1, P3):
// a core.hooksPath in the repository's config is overridden for pushes
// served here; the first push logs one Warn naming the hooks that will
// not run, later pushes do not repeat it.
func TestBudgetHook_WarnsOnceWhenHooksPathIsOverridden(t *testing.T) {
	repo, _, _, _ := nulRepo(t, "")
	custom := t.TempDir()
	if err := os.WriteFile(filepath.Join(custom, "post-receive"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(custom, "update.sample"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	nulGit(t, repo, nil, "config", "core.hooksPath", custom)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := budgetHandler(t, repo)
	url := budgetServer(t, h, repo)
	work := budgetClone(t, url)
	for i := 0; i < 2; i++ {
		nulGit(t, work, nil, "commit", "-q", "--allow-empty", "-m", fmt.Sprint(i))
		if out, err := gitPush(t, work, url, fmt.Sprintf("HEAD:refs/heads/f%d", i)); err != nil {
			t.Fatalf("push %d: %v\n%s", i, err, out)
		}
	}
	got := logs.String()
	if n := strings.Count(got, "core.hooksPath points pushes"); n != 1 {
		t.Fatalf("warned %d times, want once:\n%s", n, got)
	}
	if !strings.Contains(got, "post-receive") || strings.Contains(got, "update.sample") {
		t.Fatalf("the warning does not name exactly the hooks that will not run:\n%s", got)
	}
}

// servedConvertedTree is a served work tree checked out with conversions
// (`* text eol=crlf`) before the neutral line: f.txt is CRLF on disk, its
// stored bytes are LF; real.txt is there to be edited.
func servedConvertedTree(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	nulGit(t, repo, nil, "init", "-q", "-b", "main")
	for name, content := range map[string]string{".gitattributes": "* text eol=crlf\n", "f.txt": "a\nb\n", "real.txt": "real\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	nulGit(t, repo, nil, "add", "-A")
	nulGit(t, repo, nil, "commit", "-q", "-m", "base")
	for _, f := range []string{"f.txt", "real.txt"} {
		if err := os.Remove(filepath.Join(repo, f)); err != nil {
			t.Fatal(err)
		}
	}
	nulGit(t, repo, nil, "checkout", "--", ".")
	if b, _ := os.ReadFile(filepath.Join(repo, "f.txt")); string(b) != "a\r\nb\r\n" {
		t.Fatalf("fixture: f.txt not converted: %q", b)
	}
	return repo
}

// TestBudgetHook_PendingRenormalisationDoesNotStopThePush (codex review
// round 3; integration A phase 4): a served work tree whose
// renormalisation cannot finish yet does not refuse pushes (the hook's
// measure needs only the line, which is in place): each push warns and
// retries (the install runs per receive-pack request), and once the cause
// is gone the next push brings the tree to its stored bytes and stops
// warning. Since phase 4 only an I/O error or a crash can leave a
// renormalisation pending past the line — here a stale index.lock (an
// exclusive create, which root cannot bypass either, unlike a read-only
// directory) makes the rewrite fail; the status reads around it.
func TestBudgetHook_PendingRenormalisationDoesNotStopThePush(t *testing.T) {
	repo := servedConvertedTree(t)
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{}})
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	lock := filepath.Join(repo, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	h := budgetHandler(t, repo)
	url := budgetServer(t, h, repo)
	work := budgetClone(t, url)
	const warn = "not yet brought to its stored bytes"
	for i := 0; i < 2; i++ {
		nulGit(t, work, nil, "commit", "-q", "--allow-empty", "-m", fmt.Sprint(i))
		if out, err := gitPush(t, work, url, fmt.Sprintf("HEAD:refs/heads/f%d", i)); err != nil {
			t.Fatalf("push %d with the renormalisation pending was refused: %v\n%s", i, err, out)
		}
	}
	if n := strings.Count(logs.String(), warn); n < 2 {
		t.Fatalf("want a warning at each push while pending, got %d:\n%s", n, logs.String())
	}
	if ok, err := git.HasNeutralCheckoutAttributes(context.Background(), repo); !ok || err != nil {
		t.Fatalf("served repository lacks the neutral checkout attributes: %v %v", ok, err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	nulGit(t, work, nil, "commit", "-q", "--allow-empty", "-m", "2")
	if out, err := gitPush(t, work, url, "HEAD:refs/heads/f2"); err != nil {
		t.Fatalf("push once the lock is gone: %v\n%s", err, out)
	}
	if strings.Contains(logs.String(), warn) {
		t.Fatalf("still pending once the lock is gone:\n%s", logs.String())
	}
	if b, err := os.ReadFile(filepath.Join(repo, "f.txt")); err != nil || string(b) != "a\nb\n" {
		t.Fatalf("the retried renormalisation did not bring f.txt to its stored bytes: %q %v", b, err)
	}
}

// TestBudgetHook_HEADOverTheBudgetBeforeTheLineRefusesThePush
// (integration A phase 4): a served repository whose HEAD is over the
// budget when the line would first be added is not given the line (every
// check that can fail runs before it), and the push gate, which needs the
// line, refuses the push; nothing is written to the tree. Raising the
// limit lets the next push add the line, renormalise and land
// (KNOWN-LIMITATIONS, "Checkout Budget").
func TestBudgetHook_HEADOverTheBudgetBeforeTheLineRefusesThePush(t *testing.T) {
	repo := servedConvertedTree(t)
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{MaxEntries: 1}})
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	var logs bytes.Buffer
	var logMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &logMu}, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	h := budgetHandler(t, repo)
	url := budgetServer(t, h, repo)
	work := budgetClone(t, url)
	nulGit(t, work, nil, "commit", "-q", "--allow-empty", "-m", "0")
	if out, err := gitPush(t, work, url, "HEAD:refs/heads/f0"); err == nil {
		t.Fatalf("push over a HEAD past the budget landed:\n%s", out)
	}
	if ok, _ := git.HasNeutralCheckoutAttributes(context.Background(), repo); ok {
		t.Fatal("the refused check wrote the line")
	}
	if _, err := os.Lstat(filepath.Join(repo, ".git", "spine-renormalise-pending")); err == nil {
		t.Fatal("the refused check left a marker")
	}
	// The refusal's log names the variable and the restart (confirming
	// review of integration A phase 4, P4).
	logMu.Lock()
	got := logs.String()
	logMu.Unlock()
	for _, w := range []string{"push refused", "env=SPINE_GIT_TREE_MAX_ENTRIES", "raise SPINE_GIT_TREE_MAX_ENTRIES above", "restart spine serve"} {
		if !strings.Contains(got, w) {
			t.Errorf("the refusal's log lacks %q:\n%s", w, got)
		}
	}
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{}})
	if out, err := gitPush(t, work, url, "HEAD:refs/heads/f0"); err != nil {
		t.Fatalf("push once the limit is raised: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(repo, "f.txt")); err != nil || string(b) != "a\nb\n" {
		t.Fatalf("f.txt after the push = %q %v; want its stored bytes", b, err)
	}
}

// TestBudgetHook_RenormalisationKeepsRealChanges (review round 4, P3):
// the per-push renormalisation of a served tree quarantined and reset
// every tracked change. Now a real edit, a staged file and an untracked
// file survive byte for byte, only the conversion-only file goes to its
// stored bytes, and the tree is no longer marked.
func TestBudgetHook_RenormalisationKeepsRealChanges(t *testing.T) {
	repo := servedConvertedTree(t)
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("real.txt", "real\nuncommitted edit\n")
	write("staged.txt", "staged\n")
	nulGit(t, repo, nil, "add", "staged.txt")
	write("untracked.txt", "mine\r\n")
	h := budgetHandler(t, repo)
	url := budgetServer(t, h, repo)
	work := budgetClone(t, url)
	nulGit(t, work, nil, "commit", "-q", "--allow-empty", "-m", "x")
	if out, err := gitPush(t, work, url, "HEAD:refs/heads/f"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	for name, want := range map[string]string{
		"f.txt": "a\nb\n", "real.txt": "real\nuncommitted edit\n", "staged.txt": "staged\n", "untracked.txt": "mine\r\n",
	} {
		if b, err := os.ReadFile(filepath.Join(repo, name)); err != nil || string(b) != want {
			t.Errorf("%s = %q (%v), want %q", name, b, err, want)
		}
	}
	if staged := nulGit(t, repo, nil, "diff", "--cached", "--name-only"); staged != "staged.txt" {
		t.Errorf("staged = %q, want staged.txt kept staged", staged)
	}
	if _, err := os.Lstat(filepath.Join(repo, ".git", "spine-renormalise-pending")); err == nil {
		t.Error("the served tree is still marked")
	}
}
