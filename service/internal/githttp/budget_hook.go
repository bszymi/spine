package githttp

// The checkout-budget pre-receive hook (INIT-024 TASK-003, review
// finding N-1).
//
// A push of a few KB can carry a tree that checks out to terabytes: git
// stores a repeated object once, a checkout writes every occurrence.
// receive.maxInputSize bounds the pack, not what it unpacks to. Every
// later checkout or merge of the pushed branch by Spine's own git client
// is held to the checkout budget (adapters/git treebudget.go); this hook
// keeps an over-budget tree from landing at all, so no path that checks
// a branch out — Spine's, or an operator's — ever meets one.
//
// It is a real pre-receive hook, not more in-process parsing of the
// request body: git runs it after the pack is unpacked (into the
// quarantine directory, which the hook's git commands see through the
// GIT_OBJECT_DIRECTORY / GIT_ALTERNATE_OBJECT_DIRECTORIES environment git
// gives it) and before any ref moves, with exactly the (old, new, ref)
// triples git will apply — no pack shape or command ordering the
// in-process parser does not model can skip it. A non-zero exit refuses
// the whole push and git discards the quarantined objects.
//
//   - The hook is a small shell script that execs this binary's hidden
//     `spine git-hook pre-receive` subcommand (PreReceiveHookMain) with
//     the budget and a deadline as flags. installBudgetHook writes it to
//     <git-common-dir>/hooks/pre-receive on every receive-pack POST
//     (atomically; a no-op when it is already current), and refuses to
//     replace a pre-receive hook Spine did not write.
//   - The receive-pack CGI runs with core.hooksPath pinned to that
//     directory (GIT_CONFIG_PARAMETERS, command-line precedence), so a
//     core.hooksPath in the repository's own config cannot point git at
//     a directory without the hook. Other hooks an operator keeps under
//     a custom core.hooksPath therefore do not run for pushes served
//     here (docs/integration-guide.md).
//   - Every pushed tip that is not a delete is measured (each object
//     once). Over the budget, or not measurable at all — a missing
//     object, a tag of a blob, a git failure, the deadline — the push is
//     refused (fail closed) with a "remote:" line naming the limit and
//     its SPINE_GIT_TREE_* variable.
//
// The hook is on whenever receive-pack is (INIT-024 owner question 3):
// there is no switch to turn it off, only larger limits.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/observe"
)

// budgetHookMarker is the line that identifies a pre-receive hook Spine
// wrote. A hook without it is never overwritten.
const budgetHookMarker = "# spine: checkout-budget pre-receive hook (INIT-024 TASK-003)"

// HookProcessEnv is set (to "pre-receive") by the hook script for the
// process it execs. The script execs the executable of the process that
// installed it (os.Executable()) — `spine` in production, but in a test
// any binary that serves pushes through this package. A test binary
// handed "git-hook pre-receive" would run its whole test suite instead,
// inside git, recursively; so this package's init runs the hook itself
// whenever the variable is set, before main or TestMain, and exits. The
// `spine git-hook pre-receive` subcommand is the same function for a
// manual run.
const HookProcessEnv = "SPINE_INTERNAL_GIT_HOOK"

func init() {
	if os.Getenv(HookProcessEnv) == "" {
		return
	}
	os.Exit(runHookProcess(os.Args[1:], os.Stdin, os.Stderr))
}

// runHookProcess is the init entry: argv after the executable must be
// "git-hook pre-receive <flags>", the script's own line. Anything else
// with the variable set is a misconfiguration and refuses (exit 2).
func runHookProcess(args []string, stdin io.Reader, stderr io.Writer) int {
	_ = os.Unsetenv(HookProcessEnv)
	if os.Getenv("GIT_DIR") == "" || len(args) < 2 || args[0] != "git-hook" || args[1] != "pre-receive" {
		_, _ = fmt.Fprintf(stderr, "spine: push refused: %s is set but this is not the checkout-budget pre-receive hook\n", HookProcessEnv)
		return 2
	}
	if err := PreReceiveHookMain(context.Background(), args[2:], stdin, stderr); err != nil {
		return 1
	}
	return 0
}

// Budget limit environment variables of `spine serve`, named in the
// hook's refusal so an operator knows which knob a refusal is about.
const (
	EnvTreeMaxEntries   = "SPINE_GIT_TREE_MAX_ENTRIES"
	EnvTreeMaxBytes     = "SPINE_GIT_TREE_MAX_BYTES"
	EnvTreeMaxPathBytes = "SPINE_GIT_TREE_MAX_PATH_BYTES"
)

// EnvForTreeLimit maps a crossed limit to its `spine serve` variable.
func EnvForTreeLimit(l git.TreeLimit) string {
	switch l {
	case git.TreeLimitEntries:
		return EnvTreeMaxEntries
	case git.TreeLimitTotalBytes:
		return EnvTreeMaxBytes
	case git.TreeLimitPathBytes:
		return EnvTreeMaxPathBytes
	}
	return "SPINE_GIT_TREE_*"
}

const (
	// maxHookScriptBytes bounds the read of an existing hook file.
	maxHookScriptBytes = 64 << 10
	// maxHookInputBytes bounds the hook's stdin. The in-process gate
	// already caps the command section at defaultMaxPushCommandBytes
	// (1 MiB); the slack covers the line format difference.
	maxHookInputBytes = 4 << 20
	// maxHookLineBytes bounds one "<old> <new> <ref>" line.
	maxHookLineBytes = 64 << 10
	// defaultHookTimeout bounds the whole hook when no --timeout is given.
	defaultHookTimeout = 60 * time.Second
)

// budgetHook is what the pre-receive script runs.
type budgetHook struct {
	// argv is the command the script execs before its flags; NewHandler
	// sets it to [<this executable>, "git-hook", "pre-receive"]. Tests
	// replace it with a command that re-enters the test binary.
	argv    []string
	budget  git.TreeBudget // effective
	timeout time.Duration
}

// newBudgetHook builds the hook of a receive-pack-enabled handler.
func newBudgetHook(budget git.TreeBudget, timeout time.Duration) (budgetHook, error) {
	exe, err := os.Executable()
	if err != nil {
		return budgetHook{}, fmt.Errorf("checkout-budget pre-receive hook: cannot locate this executable: %w", err)
	}
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}
	return budgetHook{argv: []string{exe, "git-hook", "pre-receive"}, budget: budget.Effective(), timeout: timeout}, nil
}

// script renders the hook. Every word is single-quoted for sh; a word
// holding a NUL or a newline is refused (no sh quoting can carry a NUL,
// and the hook must be exactly one exec line).
func (b budgetHook) script() (string, error) {
	if len(b.argv) == 0 {
		return "", errors.New("checkout-budget pre-receive hook: no command")
	}
	words := append(append([]string{}, b.argv...),
		"--max-entries="+strconv.FormatInt(b.budget.MaxEntries, 10),
		"--max-total-bytes="+strconv.FormatInt(b.budget.MaxTotalBytes, 10),
		"--max-path-bytes="+strconv.FormatInt(b.budget.MaxPathBytesTotal, 10),
		"--timeout="+b.timeout.String())
	quoted := make([]string, len(words))
	for i, w := range words {
		if strings.ContainsAny(w, "\x00\n\r") {
			return "", fmt.Errorf("checkout-budget pre-receive hook: command word %d holds a NUL or newline", i)
		}
		quoted[i] = shQuote(w)
	}
	return "#!/bin/sh\n" + budgetHookMarker + "\n" +
		"# Written by the Spine git HTTP endpoint on every push; local edits are replaced.\n" +
		HookProcessEnv + "=pre-receive\n" +
		"export " + HookProcessEnv + "\n" +
		"exec " + strings.Join(quoted, " ") + "\n", nil
}

// shQuote single-quotes s for sh.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// gitSQQuote quotes s the way git's sq_quote_buf does for
// GIT_CONFIG_PARAMETERS: single quotes, with ' and ! each closed out and
// backslash-escaped.
func gitSQQuote(s string) string {
	r := strings.NewReplacer("'", `'\''`, "!", `'\!'`)
	return "'" + r.Replace(s) + "'"
}

// hooksPathEnv pins core.hooksPath for the receive-pack CGI. The CGI
// environment is built from scratch (net/http/cgi), so there is no
// inherited GIT_CONFIG_PARAMETERS to keep.
func hooksPathEnv(dir string) string {
	return "GIT_CONFIG_PARAMETERS=" + gitSQQuote("core.hooksPath="+dir)
}

// errForeignHook is the refusal to replace a hook Spine did not write.
var errForeignHook = errors.New("an existing pre-receive hook was not written by Spine; it is never replaced — move it aside to enable pushes")

// installBudgetHook makes <git-common-dir>/hooks/pre-receive the current
// budget hook and returns the hooks directory (for hooksPathEnv). It runs
// on every receive-pack POST: a hook removed or edited since is put back
// before the push reaches git. It never replaces a hook that lacks the
// marker, and never follows a symbolic link at the hook's path.
func (h *Handler) installBudgetHook(ctx context.Context, repoPath string) (string, error) {
	want, err := h.budgetHook.script()
	if err != nil {
		return "", err
	}
	// G702: repoPath is server-resolved from the workspace registry.
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-common-dir") //nolint:gosec // G204: fixed argv
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("locate the git directory: %w", err)
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(repoPath, common)
	}
	common, err = filepath.Abs(common)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(common, "hooks")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	h.warnOverriddenHooks(ctx, repoPath, dir)
	// The hook's measure needs the served repository to carry the neutral
	// checkout attributes (it refuses without them), and so does every
	// checkout of what it admits. A work tree still waiting for its
	// renormalisation does not stop the push — the measure needs only the
	// line — and is retried by the next push; the tree's own operations
	// report it. A check that fails before the line (HEAD over the budget,
	// a torn HEAD: TASK-003 integration A phase 4) leaves no line, so the
	// push is refused (fail closed) until a call succeeds.
	if err := git.NewCLIClient(repoPath).EnsureNeutralWorkTree(ctx); err != nil {
		if !errors.Is(err, git.ErrRenormalisePending) {
			return "", fmt.Errorf("neutral checkout attributes: %w", err)
		}
		observe.Logger(ctx).Warn("served repository's work tree not yet brought to its stored bytes; the push goes on and the next push retries",
			"repo_path", repoPath, "error", err)
	}
	path := filepath.Join(dir, "pre-receive")
	switch fi, err := os.Lstat(path); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", err
	case !fi.Mode().IsRegular():
		return "", fmt.Errorf("%s: %w", path, errForeignHook)
	default:
		have, err := readHead(path, maxHookScriptBytes)
		if err != nil {
			return "", err
		}
		if !bytes.Contains(have, []byte(budgetHookMarker)) {
			return "", fmt.Errorf("%s: %w", path, errForeignHook)
		}
		if string(have) == want && fi.Mode().Perm()&0o100 != 0 {
			return dir, nil
		}
	}
	tmp, err := os.CreateTemp(dir, ".pre-receive.spine-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op after the rename
	if _, err := tmp.WriteString(want); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o700); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return dir, nil
}

// warnOverriddenHooks logs, once per repository and handler lifetime, a
// Warn when the hooks directory git would use for this repository — a
// core.hooksPath in its own, the global or the system config — is not
// the one pushes served here are pinned to, naming the hooks there that
// will therefore not run for them (review round 1). Pushes still go
// through the budget hook; this only makes the override visible.
func (h *Handler) warnOverriddenHooks(ctx context.Context, repoPath, pinned string) {
	h.mu.Lock()
	if h.hooksWarned == nil {
		h.hooksWarned = map[string]bool{}
	}
	done := h.hooksWarned[repoPath]
	h.hooksWarned[repoPath] = true
	h.mu.Unlock()
	if done {
		return
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-path", "hooks") //nolint:gosec // G204: fixed argv
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=")
	out, err := cmd.Output()
	if err != nil {
		return
	}
	effective := strings.TrimSpace(string(out))
	if !filepath.IsAbs(effective) {
		effective = filepath.Join(repoPath, effective)
	}
	if a, err := filepath.Abs(effective); err == nil {
		effective = a
	}
	if filepath.Clean(effective) == filepath.Clean(pinned) {
		return
	}
	var hooks []string
	if ents, err := os.ReadDir(effective); err == nil {
		for _, e := range ents {
			if e.IsDir() || strings.HasSuffix(e.Name(), ".sample") {
				continue
			}
			if fi, err := e.Info(); err == nil && fi.Mode().Perm()&0o111 != 0 {
				hooks = append(hooks, e.Name())
			}
		}
	}
	observe.Logger(ctx).Warn("core.hooksPath points pushes served by Spine away from this repository's hooks directory; they are pinned to the checkout-budget hook's directory, so the hooks in core.hooksPath do not run for them",
		"repo_path", repoPath, "core_hooks_path", effective, "pinned_hooks_dir", pinned, "hooks_not_run", hooks)
}

// readHead reads at most limit bytes of path.
func readHead(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the repository's own hook path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit))
}

// errHookRefused is PreReceiveHookMain's refusal (exit status 1).
var errHookRefused = errors.New("push refused by the checkout-budget pre-receive hook")

// PreReceiveHookMain is `spine git-hook pre-receive`: it reads the
// "<old> <new> <ref>" lines git gives a pre-receive hook on stdin and
// measures the tree of every new tip against the budget its flags name
// (--max-entries, --max-total-bytes, --max-path-bytes; unset or <= 0:
// the defaults) within --timeout. It must run where git runs hooks
// ($GIT_DIR, with git's environment). It returns nil when every tip is
// within the budget; otherwise it writes one line per refusal to stderr
// (git relays it as "remote: ...") and returns an error, and the caller
// exits non-zero. Anything it cannot read or measure is a refusal.
func PreReceiveHookMain(ctx context.Context, args []string, stdin io.Reader, stderr io.Writer) error {
	refuse := func(format string, a ...any) error {
		_, _ = fmt.Fprintf(stderr, "spine: push refused: "+format+"\n", a...)
		return errHookRefused
	}
	flags := flag.NewFlagSet("pre-receive", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	maxEntries := flags.Int64("max-entries", 0, "")
	maxBytes := flags.Int64("max-total-bytes", 0, "")
	maxPath := flags.Int64("max-path-bytes", 0, "")
	timeout := flags.Duration("timeout", defaultHookTimeout, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return refuse("the checkout-budget hook was installed with invalid arguments (%v); refusing every push until the server rewrites it", err)
	}
	if *timeout <= 0 {
		*timeout = defaultHookTimeout
	}
	budget := git.TreeBudget{MaxEntries: *maxEntries, MaxTotalBytes: *maxBytes, MaxPathBytesTotal: *maxPath}.Effective()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	// git runs pre-receive in $GIT_DIR with GIT_DIR set; the quarantined
	// objects are visible through the environment the client inherits.
	c := git.NewCLIClient(".")
	in := &io.LimitedReader{R: stdin, N: maxHookInputBytes + 1}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 4096), maxHookLineBytes)
	type tip struct{ oid, ref string }
	var tips []tip
	seen := map[string]bool{}
	n := 0
	for sc.Scan() {
		n++
		f := strings.Split(sc.Text(), " ")
		if len(f) != 3 || !git.IsFullObjectName(f[0]) || !git.IsFullObjectName(f[1]) || f[2] == "" {
			return refuse("ref update %d is malformed", n)
		}
		newOID, ref := f[1], f[2]
		if strings.Trim(newOID, "0") == "" || seen[newOID] {
			continue // a delete writes nothing; a repeated tip is typed once
		}
		seen[newOID] = true
		tips = append(tips, tip{newOID, ref})
	}
	if in.N <= 0 {
		return refuse("the ref updates exceed %d bytes", maxHookInputBytes)
	}
	if err := sc.Err(); err != nil {
		return refuse("cannot read the ref updates: %v", err)
	}
	if len(tips) == 0 {
		return nil
	}
	oids := make([]string, len(tips))
	for i, t := range tips {
		oids[i] = t.oid
	}
	trees, err := tipTrees(ctx, oids)
	if err != nil {
		return refuse("the pushed objects could not be typed (%v); refused (fail closed)", err)
	}
	// Only a tip that is (or peels to) a commit is ever checked out; a
	// blob or tree tip (a tag of a GPG key, say) is admitted unmeasured
	// (review round 1). Tips that share a tree — many tags of one commit,
	// a branch and its tag — are measured once.
	measured := map[string]bool{}
	for i, t := range tips {
		tree := trees[i]
		if tree == "" || measured[tree] {
			continue
		}
		if measureHookForTest != nil {
			measureHookForTest(tree)
		}
		_, err := budget.Measure(ctx, c, tree)
		var tl *git.TreeTooLargeError
		switch {
		case errors.As(err, &tl):
			return refuse("%q: %s (%s); raise %s above %d to admit it, or reduce the tree",
				t.ref, git.TreeTooLargeMessage, tl.Error(), EnvForTreeLimit(tl.Limit), tl.Seen)
		case err != nil:
			var ge *git.GitError
			if errors.As(err, &ge) && ge.Message == git.TreeObjectTooLargeMessage {
				return refuse("%q: %s", t.ref, git.TreeObjectTooLargeMessage)
			}
			return refuse("%q: the pushed tree could not be measured against the checkout budget within %s; refused (fail closed)", t.ref, *timeout)
		}
		measured[tree] = true
	}
	return nil
}

// measureHookForTest, when set, is called before each tree the hook
// measures.
var measureHookForTest func(tree string)

// tipTrees types every pushed tip in one `git cat-file --batch-check`
// and returns, per tip, the tree to measure: the commit's tree for a
// commit or a tag that peels to one, "" for a tip that is (or peels to)
// a blob or a tree. A missing or unparsable object is an error.
func tipTrees(ctx context.Context, oids []string) ([]string, error) {
	var in strings.Builder
	for _, oid := range oids {
		// The tip's own type, what it peels to, and that commit's tree.
		fmt.Fprintf(&in, "%s\n%s^{}\n%s^{tree}\n", oid, oid, oid)
	}
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch-check") //nolint:gosec // G204: fixed argv; the input is validated object ids
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=")
	cmd.Stdin = strings.NewReader(in.String())
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 3*len(oids) {
		return nil, fmt.Errorf("cat-file printed %d lines for %d queries", len(lines), 3*len(oids))
	}
	field := func(line string) (oid, typ string, ok bool) {
		f := strings.Fields(line)
		if len(f) == 3 && git.IsFullObjectName(f[0]) {
			return f[0], f[1], true
		}
		return "", "", false
	}
	out := make([]string, len(oids))
	for i := range oids {
		if _, _, ok := field(lines[3*i]); !ok {
			return nil, fmt.Errorf("pushed object %d is missing", i+1)
		}
		_, peeled, ok := field(lines[3*i+1])
		if !ok {
			return nil, fmt.Errorf("pushed object %d does not peel", i+1)
		}
		switch peeled {
		case git.TreeEntryCommit:
			tree, typ, ok := field(lines[3*i+2])
			if !ok || typ != git.TreeEntryTree {
				return nil, fmt.Errorf("pushed commit %d has no tree", i+1)
			}
			out[i] = tree
		case git.TreeEntryBlob, git.TreeEntryTree:
			// Nothing Spine checks out.
		default:
			return nil, fmt.Errorf("pushed object %d peels to a %q", i+1, peeled)
		}
	}
	return out, nil
}
