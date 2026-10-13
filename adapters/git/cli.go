package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/bszymi/spine/core/worktreefs"
)

// CLIClient implements GitClient by shelling out to the git CLI.
//
// # Ref grammar (INIT-023 TASK-002)
//
// Every ref-like parameter belongs to exactly one class, and its argv is
// built only by the helpers in refgrammar.go:
//
//	Class   Parameters                                Accepted                       Reaches git as
//	Branch  Checkout branch; MergeOpts.Target          <n> or refs/heads/<n>, where   <n> for switch/branch/worktree;
//	        (non-empty); CreateBranch name;            <n> passes refname.Validate    refs/heads/<n> as a push
//	        DeleteBranch name; Push ref; PushBranch    and is not a full object id    destination
//	        branch; DeleteRemoteBranch branch;
//	        EnterBranch branch
//	Commit  MergeOpts.Source; CreateBranch base        a full object id; HEAD (base   the object id or HEAD; a branch
//	                                                   only); otherwise Branch        as its exact object id; as a
//	                                                                                  CreateBranch base, with ^{commit}
//	Rev     ReadFile/ListFiles/RefSHA/TreeEntryType    non-empty and ValidateRef      branch-first: an existing branch
//	        ref; Diff from/to; MergeBase a/b;                                         as its exact object id, anything
//	        LogOpts.Since                                                             else unchanged (see resolveRev)
//	Remote  remote of Push, PushBranch,                ^[A-Za-z0-9][A-Za-z0-9._-]*    unchanged
//	        DeleteRemoteBranch                         (/[A-Za-z0-9._-]+)*, no ".."
//
// Everything else is refused, before any git process starts, with a
// *GitError{Kind: ErrKindPermanent, Err: ErrInvalidRef}: HEAD, "@" and
// any refs/* other than refs/heads/<n> as a branch; tags, remote refs
// and rev expressions as a commit (an abbreviated id is a valid branch
// NAME there, so it is looked up as a branch and is ErrKindNotFound
// unless such a branch exists — never read as an object); "", a leading
// "-", ~ ^ : \ "..", space and control bytes as a rev; URLs, ext:: and
// options as a remote.
// Each method checks ALL of its arguments first, then resolves (two
// phases), so an invalid second argument never follows a lookup of the
// first.
//
// A branch is looked up exactly (`show-ref --verify --hash
// refs/heads/<n>`, no DWIM), and its object id, not its name, reaches
// git: a pushed refs/tags/<n>, refs/<n> or refs/tags/refs/heads/<n> can
// therefore never stand in for the branch, even when the branch is
// missing (then the result is ErrKindNotFound).
//
// Argv rules (<oid> is the looked-up object id, [eoo] is
// --end-of-options, which ends option parsing without turning revisions
// into pathspecs):
//
//	Clone               clone --no-tags -- <url> <path>
//	Checkout            switch --no-guess [eoo] <n>      (never detaches onto a tag)
//	Merge               [switch --no-guess [eoo] <target>], merge [--ff-only | --no-ff [-m <msg>]] [eoo] <oid>
//	Merge (abort)       merge --abort                     (Strategy "abort" or Source "--abort", nothing else)
//	CreateBranch        branch [eoo] <n> <HEAD | oid>^{commit}   (git branch re-DWIMs a bare start point)
//	DeleteBranch        branch -D [eoo] <n>
//	Diff                diff --name-status [eoo] <from'> <to'> --
//	Log                 log --format=... [-nN] [eoo] [<since'>..HEAD] -- [<path>]
//	ReadFile            show [eoo] <ref'>:<path> --
//	ListFiles           ls-tree -r --name-only [eoo] <ref'>
//	TreeEntryType       --literal-pathspecs ls-tree -z --full-tree [eoo] <ref'> <path>   (no "--": after eoo it is a pathspec)
//	RefSHA              rev-parse --verify [eoo] <ref'>   (no call at all for an existing branch)
//	MergeBase           merge-base [eoo] <a'> <b'>
//	Push, PushBranch    push [eoo] <remote> <oid>^{commit}:refs/heads/<n>
//	DeleteRemoteBranch  push --delete [eoo] <remote> refs/heads/<n>
//
// The trailing "--" on diff, show and log stops a "rev" that is really a
// path from being read as a pathspec. A push source "<oid>^{commit}" can
// never match a ref name ("^" is illegal in refnames), so neither a
// local refs/tags/refs/heads/<n> nor a refs/tags/<oid> can make the
// source ambiguous or substitute it; git writes no upstream tracking for
// such a source. `git branch` is the one command that re-resolves an
// already-parsed start point by DWIM (a bare <oid> loses to a ref named
// refs/tags/<oid>, a bare HEAD dies "ambiguous" next to refs/tags/HEAD),
// so CreateBranch passes "<oid>^{commit}" / "HEAD^{commit}" for the same
// reason. A doubly-qualified ref on the REMOTE
// (refs/heads/refs/heads/<n> and siblings) still breaks every client's
// push to <n> ("dst refspec ... matches more than one"); no argv can
// avoid that, and push admission refuses to create or update such names
// (core/refadmit rule 0b; KNOWN-LIMITATIONS.md, "Git ref arguments").
//
// The client requires git >= MinGitVersion (2.30); hosts call
// CheckGitVersion at startup.
type CLIClient struct {
	repoPath         string
	credentialHelper string   // path to external credential helper script
	pushEnv          []string // extra env vars for push operations (e.g., SMP_WORKSPACE_ID=xxx)
	pushToken        string   // token for HTTPS push auth (SPINE_GIT_PUSH_TOKEN)
	pushUsername     string   // username for token auth (default: x-access-token)
	askpassPath      string   // path to temporary GIT_ASKPASS script (created from pushToken)
	noAmbientAuth    bool     // WithoutAmbientCredentials: clone and push consult no host credential source
}

// CLIOption configures a CLIClient.
type CLIOption func(*CLIClient)

// WithCredentialHelper configures the Git client to use an external credential
// helper for push authentication. The helper path must point to an existing,
// executable file. An empty path is a no-op (credential helper not configured).
func WithCredentialHelper(path string) CLIOption {
	return func(c *CLIClient) {
		c.credentialHelper = path
	}
}

// WithPushEnv adds environment variables to push operations.
// Each entry should be in KEY=VALUE format.
func WithPushEnv(env ...string) CLIOption {
	return func(c *CLIClient) {
		c.pushEnv = append(c.pushEnv, env...)
	}
}

// WithPushToken configures token-based HTTPS push authentication.
// The token is injected into the remote URL per-push (in-memory only).
// Only applies to HTTPS remotes; SSH remotes are unaffected.
func WithPushToken(token, username string) CLIOption {
	return func(c *CLIClient) {
		c.pushToken = token
		c.pushUsername = username
	}
}

// WithoutCredentialHelper clears any previously-configured credential
// helper on the client. Use this when layering a per-binding
// WithPushToken on top of process-level base options that include a
// SPINE_GIT_CREDENTIAL_HELPER: NewCLIClient creates the GIT_ASKPASS
// helper only when the credential helper is empty, so a per-binding
// token would otherwise lose to the workspace-level helper. Apply this
// before WithPushToken so the askpass-creation check at the end of
// NewCLIClient sees credentialHelper == "".
func WithoutCredentialHelper() CLIOption {
	return func(c *CLIClient) {
		c.credentialHelper = ""
	}
}

// WithoutPushToken clears a push token configured by an earlier option —
// typically the process-wide SPINE_GIT_PUSH_TOKEN carried in base
// options (PushAuthOpts) — so NewCLIClient creates no GIT_ASKPASS helper
// and the client offers no token to any host. It mirrors
// WithoutCredentialHelper. Additive (INIT-023 TASK-015): gitpool applies
// both to a code repository that has no per-binding credential, so the
// deployment's credentials never reach whatever host a binding's
// clone_url names. No existing option or call path changes.
func WithoutPushToken() CLIOption {
	return func(c *CLIClient) {
		c.pushToken = ""
		c.pushUsername = ""
	}
}

// WithoutAmbientCredentials makes clone and push authenticate with
// nothing but the client's own options (WithPushToken,
// WithCredentialHelper). Additive and opt-in (INIT-023 TASK-015, codex
// review): without it, a client with no token or helper of its own still
// lets git consult the host's ambient credential sources — a
// credential.helper in the global, system or repo-local git config (for
// example the osxkeychain helper in Apple git's system config), and an
// askpass program inherited through GIT_ASKPASS, core.askPass or
// SSH_ASKPASS — and send what they return to whatever host the clone URL
// or the remote names.
//
// With it, a client that has no askpass token and no credential helper
// runs clone and push with `-c credential.helper=` (an empty value resets
// every configured helper, URL-scoped ones included), GIT_ASKPASS and
// SSH_ASKPASS set empty (an empty GIT_ASKPASS also stops git from falling
// back to core.askPass) and GIT_TERMINAL_PROMPT=0. A client that does
// carry a token or helper is unchanged: its own credential is used as
// before. Not covered: curl's ~/.netrc lookup, which git enables for
// HTTP (KNOWN-LIMITATIONS.md).
func WithoutAmbientCredentials() CLIOption {
	return func(c *CLIClient) {
		c.noAmbientAuth = true
	}
}

// ambientAuthDisabled reports whether clone and push must run with every
// host credential source switched off: WithoutAmbientCredentials was set
// and the client has no credential of its own.
func (c *CLIClient) ambientAuthDisabled() bool {
	return c.noAmbientAuth && c.askpassPath == "" && c.credentialHelper == ""
}

// noAmbientAuthEnv is appended to the environment of a clone or push when
// ambientAuthDisabled; os/exec keeps the last value of a duplicated key.
var noAmbientAuthEnv = []string{"GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_TERMINAL_PROMPT=0"}

// NewCLIClient creates a new Git client for the repository at the given path.
func NewCLIClient(repoPath string, opts ...CLIOption) *CLIClient {
	c := &CLIClient{repoPath: repoPath}
	for _, opt := range opts {
		opt(c)
	}
	// When a push token is configured (and no credential helper takes priority),
	// create a GIT_ASKPASS script that reads credentials from environment variables.
	// This keeps the token out of argv and off disk in the script itself.
	if c.pushToken != "" && c.credentialHelper == "" {
		if path, err := createAskpassScript(); err == nil {
			c.askpassPath = path
		}
	}
	return c
}

// createAskpassScript writes a minimal GIT_ASKPASS helper to a temp file.
// The script reads the actual credentials from environment variables set by
// the run() method, so the token never appears in the script or in argv.
func createAskpassScript() (string, error) {
	f, err := os.CreateTemp("", "spine-askpass-*.sh")
	if err != nil {
		return "", err
	}
	// The script echoes the username or password depending on what Git asks for.
	// Git passes "Username for ..." or "Password for ..." as the first argument.
	script := `#!/bin/sh
case "$1" in
  Username*) echo "$SPINE_GIT_PUSH_USERNAME_INTERNAL" ;;
  Password*) echo "$SPINE_GIT_PUSH_TOKEN_INTERNAL" ;;
esac
`
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	f.Close()
	if err := os.Chmod(f.Name(), 0o700); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Close cleans up temporary resources created by the CLIClient,
// such as the GIT_ASKPASS script.
func (c *CLIClient) Close() {
	if c.askpassPath != "" {
		os.Remove(c.askpassPath)
		c.askpassPath = ""
	}
}

// ConfigureCredentialHelper sets credential.helper in the repo-local git config.
// Must be called after the repository exists on disk (after Clone or init).
// No-op if no credential helper is configured on the client.
func (c *CLIClient) ConfigureCredentialHelper(ctx context.Context) error {
	if c.credentialHelper == "" {
		return nil
	}
	_, err := c.run(ctx, "config", "config", "--local", "credential.helper", c.credentialHelper)
	return err
}

// allowedCredentialHelpers enumerates the short git credential helper
// names Spine accepts. Anything else — including absolute paths,
// relative paths, or freeform shell strings — is rejected outright.
// Git treats credential.helper values as "run a program," so without
// an allowlist a misconfigured env var is a latent RCE surface.
var allowedCredentialHelpers = map[string]struct{}{
	"cache":       {},
	"store":       {},
	"osxkeychain": {},
	"manager":     {},
	"pass":        {},
}

// ValidateCredentialHelper enforces that the configured credential
// helper is one of the known-safe short names in allowedCredentialHelpers.
// Returns nil if no helper is configured. Returning an error here
// causes spine serve to refuse startup, surfacing misconfiguration
// instead of letting git inherit a potentially dangerous value.
func ValidateCredentialHelper(name string) error {
	if name == "" {
		return nil
	}
	if _, ok := allowedCredentialHelpers[name]; !ok {
		return fmt.Errorf("credential helper %q is not in the allowlist; set SPINE_GIT_CREDENTIAL_HELPER to one of cache, store, osxkeychain, manager, pass", name)
	}
	return nil
}

// Package-level cache of push-auth configuration resolved once at
// startup. Shared mode builds a CLIClient per workspace on first use;
// reading the token from os.Environ() at that point would defeat the
// scrubbing done in LoadPushAuthFromEnv, so subsequent callers read
// from this cache instead.
var (
	authMu         sync.Mutex
	authLoaded     bool
	cachedAuthOpts []CLIOption
)

// LoadPushAuthFromEnv reads SPINE_GIT_CREDENTIAL_HELPER,
// SPINE_GIT_PUSH_TOKEN, and SPINE_GIT_PUSH_USERNAME, caches the
// resulting CLIOptions for the rest of the process, and scrubs the
// token/username variables from os.Environ() so they are no longer
// visible to child processes, core dumps, or /proc/<pid>/environ.
// A configured credential helper always wins: when both a helper and
// a token are set, the token is ignored and the user is warned.
// Returns (warnings, err); warnings should be logged by the caller.
// On validation failure the cache is left empty so the caller can
// decide whether to start without push auth.
func LoadPushAuthFromEnv() (warnings []string, err error) {
	authMu.Lock()
	defer authMu.Unlock()
	if authLoaded {
		return nil, nil
	}

	helper := os.Getenv("SPINE_GIT_CREDENTIAL_HELPER")
	token := os.Getenv("SPINE_GIT_PUSH_TOKEN")
	username := os.Getenv("SPINE_GIT_PUSH_USERNAME")

	var opts []CLIOption

	if helper != "" {
		if err := ValidateCredentialHelper(helper); err != nil {
			return warnings, err
		}
		opts = append(opts, WithCredentialHelper(helper))
		if token != "" {
			warnings = append(warnings, "SPINE_GIT_CREDENTIAL_HELPER and SPINE_GIT_PUSH_TOKEN are both set; credential helper wins and the token is ignored — unset SPINE_GIT_PUSH_TOKEN to silence this warning")
			_ = os.Unsetenv("SPINE_GIT_PUSH_TOKEN")
			_ = os.Unsetenv("SPINE_GIT_PUSH_USERNAME")
		}
	} else if token != "" {
		opts = append(opts, WithPushToken(token, username))
		// Copy the value into the in-memory CLIOption and remove it
		// from the process environment. GIT_ASKPASS reads from a
		// distinct *_INTERNAL var set at push time, not from the
		// public SPINE_GIT_PUSH_TOKEN, so the scrub is safe.
		_ = os.Unsetenv("SPINE_GIT_PUSH_TOKEN")
		_ = os.Unsetenv("SPINE_GIT_PUSH_USERNAME")
	}

	cachedAuthOpts = opts
	authLoaded = true
	return warnings, nil
}

// PushAuthOpts returns the CLIOptions resolved by LoadPushAuthFromEnv.
// Returns nil before LoadPushAuthFromEnv has been called or when
// neither a helper nor a token was configured.
func PushAuthOpts() []CLIOption {
	authMu.Lock()
	defer authMu.Unlock()
	if len(cachedAuthOpts) == 0 {
		return nil
	}
	out := make([]CLIOption, len(cachedAuthOpts))
	copy(out, cachedAuthOpts)
	return out
}

// resetPushAuthForTest clears the cache so tests can exercise
// LoadPushAuthFromEnv multiple times with different environments.
func resetPushAuthForTest() {
	authMu.Lock()
	defer authMu.Unlock()
	authLoaded = false
	cachedAuthOpts = nil
}

// Clone clones a remote repository to a local path.
// Unlike other operations, Clone does not run inside repoPath since the
// target directory may not exist yet.
//
// When the client was constructed with WithPushToken, Clone wires the
// same GIT_ASKPASS-based auth path used by Push. The token is fed to
// git via env-var-backed askpass (never argv, never .git/config), and
// GIT_TERMINAL_PROMPT=0 ensures git fails fast instead of prompting on
// a missing or wrong credential.
//
// When the client was constructed with WithCredentialHelper, Clone
// passes the helper inline via `git -c credential.helper=<name> clone`.
// The `-c` is positioned BEFORE the clone subcommand so the override
// applies for the duration of this command only and is NOT persisted
// to the cloned repo's local config. If we used `git clone -c ...` the
// value would be written into .git/config; a later binding update that
// adds a per-binding token would then lose to the still-persisted
// helper because git consults repo-local config before any in-memory
// override. Command-scoped -c keeps the on-disk state clean so
// rotation paths can take effect.
//
// askpass and credential-helper paths are mutually exclusive at
// construction time (NewCLIClient only creates the askpass when no
// helper is configured), so Clone applies whichever was wired.
func (c *CLIClient) Clone(ctx context.Context, url, path string) error {
	var args []string
	switch {
	case c.askpassPath != "":
		// Per-binding token path. An empty `credential.helper=` value
		// passed via `git -c` disables every helper resolution for
		// the duration of the command — global, system, and any
		// pre-existing local config — so the resolved token reaches
		// git via GIT_ASKPASS instead of being masked by stale
		// credentials a host happens to have configured. The empty
		// value is git's documented "no helper" override.
		args = append(args, "-c", "credential.helper=")
	case c.credentialHelper != "":
		// Helper-only path. The helper hasn't been written to the
		// new repo's local config yet (clone is creating that repo),
		// so we pass it inline. Command-scoped via `git -c` rather
		// than `git clone -c` so the value is not persisted —
		// otherwise a later rotation to a per-binding token would
		// lose to the still-on-disk helper.
		args = append(args, "-c", "credential.helper="+c.credentialHelper)
	case c.ambientAuthDisabled():
		// No credential at all (WithoutAmbientCredentials): the empty
		// value disables every configured helper for this command.
		args = append(args, "-c", "credential.helper=")
	}
	// `--` ends option parsing so a poisoned URL like `--upload-pack=cmd`
	// cannot be re-interpreted as a flag. ValidateCloneURL already
	// rejects `-`-prefixed URLs at the entry points (workspace/code-repo
	// provisioning) and `gitpool.cliCloner.Clone` re-validates as a
	// second line of defense, but the sentinel is the cheapest fix
	// directly inside the shellout.
	//
	// --no-tags (INIT-023 TASK-002, owner question 1): the clone imports
	// no upstream tag names into the local namespace and persists
	// remote.origin.tagOpt=--no-tags, so later fetches do not either.
	// Branch arguments never resolve through tags anyway (exact lookup);
	// this keeps upstream-controlled names out of rev resolution too.
	args = append(args, "clone", "--no-tags", "--", url, path)
	cmd := exec.CommandContext(ctx, "git", args...)
	// Build the clone environment when the client carries any auth
	// state — askpass token, credential-helper env (e.g.
	// SMP_WORKSPACE_ID for per-workspace helpers), or both. Skipping
	// the env in the helper-only branch would drop WithPushEnv
	// entries and leave the helper invoking without its required
	// context, so any non-empty pushEnv triggers env injection.
	if c.askpassPath != "" || len(c.pushEnv) > 0 || c.ambientAuthDisabled() {
		env := os.Environ()
		if len(c.pushEnv) > 0 {
			env = append(env, c.pushEnv...)
		}
		if c.ambientAuthDisabled() {
			env = append(env, noAmbientAuthEnv...)
		}
		if c.askpassPath != "" {
			username := c.pushUsername
			if username == "" {
				username = "x-access-token"
			}
			env = append(env,
				"GIT_ASKPASS="+c.askpassPath,
				"GIT_TERMINAL_PROMPT=0",
				"SPINE_GIT_PUSH_TOKEN_INTERNAL="+c.pushToken,
				"SPINE_GIT_PUSH_USERNAME_INTERNAL="+username,
			)
		}
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		gitErr := classifyGitError("clone", strings.TrimSpace(stderr.String()))
		gitErr.Err = err
		return gitErr
	}
	return nil
}

// Checkout switches to the given branch (Branch class).
//
// It runs `git switch --no-guess --end-of-options <n>`, not checkout:
// before git 2.44 `checkout --end-of-options` treats the terminator as a
// pathspec, `checkout <n>` detaches onto a same-named tag when the branch
// is missing, and checkout --guess would create a branch from a
// remote-tracking ref. switch only ever attaches to an existing local
// branch ("a branch is expected, got tag ..."), carries staged changes
// like checkout, and refuses to run during an in-progress merge.
func (c *CLIClient) Checkout(ctx context.Context, branch string) error {
	short, _, err := checkBranch("checkout", "branch", branch)
	if err != nil {
		return err
	}
	return c.switchTo(ctx, short)
}

// switchTo runs the Checkout argv for an already-validated short name.
func (c *CLIClient) switchTo(ctx context.Context, short string) error {
	_, err := c.run(ctx, "checkout", "switch", "--no-guess", "--end-of-options", short)
	return err
}

// Commit creates a new commit with the staged changes.
// Commit messages include structured trailers per Git Integration §5.1.
func (c *CLIClient) Commit(ctx context.Context, opts CommitOpts) (CommitResult, error) {
	msg := buildCommitMessage(opts)

	args := []string{"commit", "-m", msg}
	if opts.AllowEmpty {
		args = append(args, "--allow-empty")
	}
	if opts.Author.Name != "" && opts.Author.Email != "" {
		args = append(args, "--author", fmt.Sprintf("%s <%s>", opts.Author.Name, opts.Author.Email))
	}

	if _, err := c.run(ctx, "commit", args...); err != nil {
		return CommitResult{}, err
	}

	sha, err := c.run(ctx, "rev-parse", "rev-parse", "HEAD")
	if err != nil {
		return CommitResult{}, err
	}

	return CommitResult{SHA: strings.TrimSpace(sha)}, nil
}

// Merge merges a source branch into the current branch (or target branch).
//
// Strategy "abort" or Source "--abort" is the in-band abort sentinel the
// engine (and SMP's operator) rely on: it runs exactly `git merge
// --abort` — no checkout, no grammar check, no other argv.
//
// Otherwise Source is Commit class (a full object id or a branch) and a
// non-empty Target is Branch class; both are checked before any git
// process. A branch source is looked up exactly and merged BY OBJECT ID,
// so a same-named tag or ref, or a refs/tags/refs/heads/<n> planted for a
// deleted branch, can never be merged in its place: a missing source
// branch is ErrKindNotFound and no `git merge` runs. The lookup happens
// before the switch to Target, so a missing source changes nothing.
func (c *CLIClient) Merge(ctx context.Context, opts MergeOpts) (MergeResult, error) {
	if opts.Strategy == "abort" || opts.Source == "--abort" {
		if _, err := c.run(ctx, "merge", "merge", "--abort"); err != nil {
			return MergeResult{}, err
		}
		return MergeResult{}, nil
	}

	// Phase 1: every argument, before any git process.
	if err := checkCommit("merge", "source", opts.Source, false); err != nil {
		return MergeResult{}, err
	}
	var target string
	if opts.Target != "" {
		short, _, err := checkBranch("merge", "target", opts.Target)
		if err != nil {
			return MergeResult{}, err
		}
		target = short
	}

	// Phase 2: resolve the source to an object id, then switch.
	source, err := c.resolveCommit(ctx, "merge", "source", opts.Source)
	if err != nil {
		return MergeResult{}, err
	}
	if target != "" {
		if err := c.switchTo(ctx, target); err != nil {
			return MergeResult{}, err
		}
	}

	args := []string{"merge"}
	switch opts.Strategy {
	case "fast-forward":
		args = append(args, "--ff-only")
	case "merge-commit":
		args = append(args, "--no-ff")
		if opts.Message != "" {
			msg := opts.Message
			if len(opts.Trailers) > 0 {
				msg += "\n"
				for k, v := range opts.Trailers {
					msg += fmt.Sprintf("\n%s: %s", k, v)
				}
			}
			args = append(args, "-m", msg)
		}
	}
	args = append(args, "--end-of-options", source)

	output, err := c.run(ctx, "merge", args...)
	if err != nil {
		return MergeResult{}, err
	}

	sha, err := c.run(ctx, "rev-parse", "rev-parse", "HEAD")
	if err != nil {
		return MergeResult{}, err
	}

	ff := strings.Contains(output, "Fast-forward")
	return MergeResult{SHA: strings.TrimSpace(sha), FastForward: ff}, nil
}

// CreateBranch creates a new branch from the specified base. name is
// Branch class; base is Commit class plus "HEAD". A branch base is
// looked up exactly and the branch is cut from that object id, so a
// same-named tag never becomes the base and a missing base branch is
// ErrKindNotFound rather than a DWIM guess (a code-repo default branch
// with no local branch is refused, as `git branch <run> <d>` already
// refused it with a Permanent "not a valid object name"; it is never
// taken from origin/<d>).
//
// The start point reaches git as "<oid>^{commit}" or "HEAD^{commit}",
// never bare (INIT-023 TASK-002 review round 1). Unlike every other
// command the client runs, `git branch` re-resolves its start point by
// DWIM after parsing it: branch.c dwim_branch_start passes the string to
// dwim_ref and adopts the object of the first ref it finds. So a bare
// <oid> was replaced by a pushed refs/<oid>, refs/tags/<oid>,
// refs/heads/<oid> or refs/remotes/<oid> (the F10 shadow, renamed after
// the base's object id), and a bare "HEAD" died with "ambiguous object
// name: 'HEAD'" while any refs/HEAD, refs/tags/HEAD, refs/heads/HEAD or
// refs/remotes/HEAD existed — one pushed ref blocked every run start.
// "^" is illegal in refnames, so dwim_ref matches nothing; the revision
// parser still reads HEAD from $GIT_DIR/HEAD first (rule 1, a warning
// at most) and a full hex id as the object itself. git writes no
// upstream tracking for such a start point (the engine never relied on
// it: autoSetupMerge's default tracks remote-tracking bases only). An
// unborn HEAD still fails with "not a valid object name", Permanent, as
// before.
func (c *CLIClient) CreateBranch(ctx context.Context, name, base string) error {
	short, _, err := checkBranch("branch", "name", name)
	if err != nil {
		return err
	}
	if err := checkCommit("branch", "base", base, true); err != nil {
		return err
	}
	from, err := c.resolveCommit(ctx, "branch", "base", base)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, "branch", "branch", "--end-of-options", short, from+"^{commit}")
	return err
}

// DeleteBranch deletes a local branch (Branch class).
func (c *CLIClient) DeleteBranch(ctx context.Context, name string) error {
	short, _, err := checkBranch("branch", "name", name)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, "branch", "branch", "-D", "--end-of-options", short)
	return err
}

// Diff returns the file differences between two refs (both Rev class).
//
// Both refs are validated before any git process, so a poisoned ref like
// `--upload-pack=cmd` cannot be re-interpreted as a `git diff` flag. The
// refs follow --end-of-options, which ends option parsing WITHOUT
// turning them into pathspecs (a bare `--` before them would, INIT-022
// TASK-009), and a trailing `--` stops a "rev" that is really a path
// from being silently read as a pathspec (`diff <sha> f` used to return
// an empty diff; now it is "bad revision", ErrKindNotFound).
func (c *CLIClient) Diff(ctx context.Context, from, to string) ([]FileDiff, error) {
	if err := checkRev("diff", "from", from); err != nil {
		return nil, err
	}
	if err := checkRev("diff", "to", to); err != nil {
		return nil, err
	}
	fromRev, err := c.resolveRev(ctx, "diff", "from", from)
	if err != nil {
		return nil, err
	}
	toRev, err := c.resolveRev(ctx, "diff", "to", to)
	if err != nil {
		return nil, err
	}
	output, err := c.run(ctx, "diff", "diff", "--name-status", "--end-of-options", fromRev, toRev, "--")
	if err != nil {
		return nil, err
	}

	var diffs []FileDiff
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}

		statusCode := parts[0]
		var fd FileDiff

		switch {
		case statusCode == "A":
			fd = FileDiff{Path: parts[1], Status: "added"}
		case statusCode == "M":
			fd = FileDiff{Path: parts[1], Status: "modified"}
		case statusCode == "D":
			fd = FileDiff{Path: parts[1], Status: "deleted"}
		case strings.HasPrefix(statusCode, "R"):
			// Rename: R<score>\t<old>\t<new>
			fd = FileDiff{Status: "renamed"}
			if len(parts) >= 3 {
				fd.OldPath = parts[1]
				fd.Path = parts[2]
			} else {
				fd.Path = parts[1]
			}
		default:
			fd = FileDiff{Path: parts[1], Status: "modified"}
		}

		diffs = append(diffs, fd)
	}
	return diffs, nil
}

// Log returns commit history. A non-empty Since is Rev class; Path
// always follows the `--`.
func (c *CLIClient) Log(ctx context.Context, opts LogOpts) ([]CommitInfo, error) {
	if opts.Since != "" {
		if err := checkRev("log", "since", opts.Since); err != nil {
			return nil, err
		}
	}
	args := []string{"log", "--format=%H%n%an%n%ae%n%aI%n%B%n---END---"}

	if opts.Limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", opts.Limit))
	}
	args = append(args, "--end-of-options")
	if opts.Since != "" {
		since, err := c.resolveRev(ctx, "log", "since", opts.Since)
		if err != nil {
			return nil, err
		}
		args = append(args, fmt.Sprintf("%s..HEAD", since))
	}
	args = append(args, "--")
	if opts.Path != "" {
		args = append(args, opts.Path)
	}

	output, err := c.run(ctx, "log", args...)
	if err != nil {
		return nil, err
	}

	return parseLogOutput(output), nil
}

// ReadFile reads a file at a specific Git ref (Rev class). An empty ref
// is refused: `git show :<path>` would read the index, which no caller
// means.
func (c *CLIClient) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
	if err := checkRev("read_file", "ref", ref); err != nil {
		return nil, err
	}
	rev, err := c.resolveRev(ctx, "read_file", "ref", ref)
	if err != nil {
		return nil, err
	}
	output, err := c.run(ctx, "read_file", "show", "--end-of-options", fmt.Sprintf("%s:%s", rev, path), "--")
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

// WriteAndStageFile writes content to a file in the working tree and stages it.
//
// This is the governed-merge status cascade's sink (engine.applyCommitStatus
// reads each blob with `git show`, then checks the run branch out and writes
// here). The write goes through worktreefs (INIT-023 TASK-004, audit F5):
// path must be a clean repo-relative path ("..", absolute paths and every
// git-dir spelling are refused lexically), and a symbolic link anywhere in
// it — for example one pushed to the run branch between the read and the
// checkout — is refused before any filesystem change instead of being
// followed out of the repository. Refusals unwrap to worktreefs.ErrUnsafePath.
// An existing file is replaced by rename (its mode is kept); a new one is
// created O_EXCL with mode 0644.
func (c *CLIClient) WriteAndStageFile(ctx context.Context, path, content string) error {
	rel, err := worktreefs.Clean(path)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tree, err := worktreefs.Open(c.repoPath)
	if err != nil {
		return fmt.Errorf("open worktree: %w", err)
	}
	defer func() { _ = tree.Close() }()
	if err := tree.WriteFile(rel, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// --literal-pathspecs (go-coding-guidelines §4.2): the path is a file
	// name, never a pathspec pattern.
	if _, err := c.run(ctx, "add", "--literal-pathspecs", "add", "--", rel); err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	return nil
}

// ListFiles lists files matching a pattern at a specific Git ref (Rev
// class).
func (c *CLIClient) ListFiles(ctx context.Context, ref, pattern string) ([]string, error) {
	if err := checkRev("list_files", "ref", ref); err != nil {
		return nil, err
	}
	rev, err := c.resolveRev(ctx, "list_files", "ref", ref)
	if err != nil {
		return nil, err
	}
	output, err := c.run(ctx, "list_files", "ls-tree", "-r", "--name-only", "--end-of-options", rev)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		if pattern == "" || matchSimpleGlob(pattern, line) {
			files = append(files, line)
		}
	}
	return files, nil
}

// Head returns the current HEAD commit SHA.
func (c *CLIClient) Head(ctx context.Context) (string, error) {
	output, err := c.run(ctx, "head", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

// RefSHA resolves a named ref (branch, tag, or HEAD) to its commit
// SHA (Rev class). An existing branch resolves to its exact object id
// with no further git call; anything else runs `git rev-parse --verify
// --end-of-options <ref>`. Returns an error when the ref is unknown
// (ErrKindNotFound, as before) or ambiguous; callers that treat baseline
// capture as best-effort should swallow the error.
func (c *CLIClient) RefSHA(ctx context.Context, ref string) (string, error) {
	if err := checkRev("ref-sha", "ref", ref); err != nil {
		return "", err
	}
	rev, err := c.resolveRev(ctx, "ref-sha", "ref", ref)
	if err != nil {
		return "", err
	}
	if rev != ref && IsFullObjectName(rev) {
		return rev, nil // the exact branch lookup already produced the id
	}
	output, err := c.run(ctx, "ref-sha", "rev-parse", "--verify", "--end-of-options", rev)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

// MergeBase returns the best common ancestor SHA between two refs.
// Callers use this to scope diffs to "what a branch added since it diverged"
// rather than "what differs from the current tip of the other ref", which
// would also flag files the other ref deleted or modified in the meantime.
//
// Same flag-injection guard as Diff: both refs (Rev class) are validated
// before any git process, and they follow --end-of-options rather than a
// `--` sentinel, because `git merge-base -- <a> <b>` would interpret a/b
// as pathspecs (which merge-base does not accept anyway).
func (c *CLIClient) MergeBase(ctx context.Context, a, b string) (string, error) {
	if err := checkRev("merge-base", "a", a); err != nil {
		return "", err
	}
	if err := checkRev("merge-base", "b", b); err != nil {
		return "", err
	}
	revA, err := c.resolveRev(ctx, "merge-base", "a", a)
	if err != nil {
		return "", err
	}
	revB, err := c.resolveRev(ctx, "merge-base", "b", b)
	if err != nil {
		return "", err
	}
	output, err := c.run(ctx, "merge-base", "merge-base", "--end-of-options", revA, revB)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

// HasCommitWithTrailer checks if a commit with the given trailer value exists.
// Used for idempotent commit detection per Git Integration §5.6.
func (c *CLIClient) HasCommitWithTrailer(ctx context.Context, key, value string) (string, bool, error) {
	output, err := c.run(ctx, "log", "log", "--all", "--format=%H %B---END---")
	if err != nil {
		return "", false, err
	}

	trailer := fmt.Sprintf("%s: %s", key, value)
	for _, entry := range strings.Split(output, "---END---") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, trailer) {
			sha := strings.SplitN(entry, " ", 2)[0]
			return sha, true, nil
		}
	}
	return "", false, nil
}

// Push pushes a local branch to the same-named branch on the specified
// remote. remote is Remote class and ref is Branch class: Push accepts
// branches only (INIT-023 TASK-002, owner question 2); a tag or any
// other refspec is refused with ErrInvalidRef.
//
// The branch is looked up exactly and pushed as
// `push --end-of-options <remote> <oid>^{commit}:refs/heads/<n>`. A
// bare `push origin main` failed ("src refspec main matches more than
// one") once any refs/tags/main existed — a workspace-wide merge denial
// of service — and even `refs/heads/main:refs/heads/main` or a raw
// `<oid>:` source is matched against local refs and can be made
// ambiguous or substituted (refs/tags/refs/heads/main, refs/tags/<oid>).
// "<oid>^{commit}" can never match a ref name. A missing local branch is
// a Permanent *GitError, matching the classification of git's own
// "src refspec ... does not match any".
func (c *CLIClient) Push(ctx context.Context, remote, ref string) error {
	return c.pushBranch(ctx, "ref", remote, ref)
}

// PushBranch pushes a branch to the specified remote; same argv and
// rules as Push. It no longer configures upstream tracking (`-u`): git
// writes branch.<n>.remote/merge only for a ref source, and nothing in
// Spine or SMP reads them (INIT-023 TASK-002, owner question 4).
func (c *CLIClient) PushBranch(ctx context.Context, remote, branch string) error {
	return c.pushBranch(ctx, "branch", remote, branch)
}

func (c *CLIClient) pushBranch(ctx context.Context, what, remote, branch string) error {
	if err := checkRemote("push", remote); err != nil {
		return err
	}
	short, full, err := checkBranch("push", what, branch)
	if err != nil {
		return err
	}
	oid, ok, err := lookupBranch(ctx, c.repoPath, "push", short)
	if err != nil {
		return err
	}
	if !ok {
		return &GitError{Kind: ErrKindPermanent, Op: "push",
			Message: fmt.Sprintf("%s: local branch %q does not exist", what, short)}
	}
	_, err = c.run(ctx, "push", "push", "--end-of-options", remote, oid+"^{commit}:"+full)
	return err
}

// DeleteRemoteBranch deletes a branch on the specified remote (remote is
// Remote class, branch is Branch class):
// `push --delete --end-of-options <remote> refs/heads/<n>`. It has no
// source side; only a doubly-qualified ref on the remote can make the
// destination ambiguous, which push admission must refuse.
func (c *CLIClient) DeleteRemoteBranch(ctx context.Context, remote, branch string) error {
	if err := checkRemote("push", remote); err != nil {
		return err
	}
	_, full, err := checkBranch("push", "branch", branch)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, "push", "push", "--delete", "--end-of-options", remote, full)
	return err
}

// run executes a git command and returns stdout. On error, classifies the failure.
func (c *CLIClient) run(ctx context.Context, op string, args ...string) (string, error) {
	// For push operations, prepend a command-scoped credential.helper
	// override via `git -c` so authentication is wired the same way
	// it was for the lazy clone (Clone applies the symmetric logic).
	//
	//   - askpass token configured  → empty helper to disable any
	//     external system/global/repo-local helper, letting the
	//     GIT_ASKPASS-fed token reach git unmasked.
	//   - credential helper configured (no token) → pass it inline
	//     so the helper engages even when the cloned repo's local
	//     config does not persist it (Clone uses command-scoped -c
	//     so a later token rotation can take effect without a stale
	//     helper on disk).
	switch {
	case op == "push" && c.askpassPath != "":
		args = append([]string{"-c", "credential.helper="}, args...)
	case op == "push" && c.credentialHelper != "":
		args = append([]string{"-c", "credential.helper=" + c.credentialHelper}, args...)
	case op == "push" && c.ambientAuthDisabled():
		// WithoutAmbientCredentials and no credential of its own: no
		// configured helper may answer (the same rule as Clone).
		args = append([]string{"-c", "credential.helper="}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.repoPath

	// For push operations, inject extra environment variables for credential
	// helpers (SMP_WORKSPACE_ID) and token-based auth (GIT_ASKPASS).
	if op == "push" {
		var env []string
		if len(c.pushEnv) > 0 || c.askpassPath != "" || c.ambientAuthDisabled() {
			env = os.Environ()
		}
		if len(c.pushEnv) > 0 {
			env = append(env, c.pushEnv...)
		}
		if c.ambientAuthDisabled() {
			env = append(env, noAmbientAuthEnv...)
		}
		if c.askpassPath != "" {
			username := c.pushUsername
			if username == "" {
				username = "x-access-token"
			}
			env = append(env,
				"GIT_ASKPASS="+c.askpassPath,
				"GIT_TERMINAL_PROMPT=0",
				"SPINE_GIT_PUSH_TOKEN_INTERNAL="+c.pushToken,
				"SPINE_GIT_PUSH_USERNAME_INTERNAL="+username,
			)
		}
		if env != nil {
			cmd.Env = env
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		gitErr := classifyGitError(op, strings.TrimSpace(stderr.String()))
		gitErr.Err = err
		return "", gitErr
	}

	return stdout.String(), nil
}

// buildCommitMessage constructs a commit message with trailers.
func buildCommitMessage(opts CommitOpts) string {
	var b strings.Builder
	b.WriteString(opts.Message)

	if opts.Body != "" {
		b.WriteString("\n\n")
		b.WriteString(opts.Body)
	}

	if len(opts.Trailers) > 0 {
		b.WriteString("\n")
		// Write trailers in deterministic order
		for _, key := range []string{"Trace-ID", "Actor-ID", "Run-ID", "Operation"} {
			if val, ok := opts.Trailers[key]; ok {
				b.WriteString("\n")
				b.WriteString(key)
				b.WriteString(": ")
				b.WriteString(val)
			}
		}
		// Write any remaining trailers not in the standard set
		for k, v := range opts.Trailers {
			if k == "Trace-ID" || k == "Actor-ID" || k == "Run-ID" || k == "Operation" {
				continue
			}
			b.WriteString("\n")
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
		}
	}

	return b.String()
}

// parseLogOutput parses the custom-formatted git log output.
func parseLogOutput(output string) []CommitInfo {
	var commits []CommitInfo

	entries := strings.Split(output, "---END---")
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		lines := strings.SplitN(entry, "\n", 5)
		if len(lines) < 4 {
			continue
		}

		ts, _ := time.Parse(time.RFC3339, strings.TrimSpace(lines[3]))

		ci := CommitInfo{
			SHA:       strings.TrimSpace(lines[0]),
			Author:    Author{Name: strings.TrimSpace(lines[1]), Email: strings.TrimSpace(lines[2])},
			Timestamp: ts,
			Trailers:  make(map[string]string),
		}

		if len(lines) > 4 {
			ci.Message = strings.TrimSpace(lines[4])
			// Extract trailers from message. Whitelist kept in sync with
			// TrailerOrder (branchwrite.go) so trailers the producer side
			// can write are also surfaced through this parser.
			for _, msgLine := range strings.Split(lines[4], "\n") {
				msgLine = strings.TrimSpace(msgLine)
				if idx := strings.Index(msgLine, ": "); idx > 0 {
					key := msgLine[:idx]
					switch key {
					case "Trace-ID", "Actor-ID", "Run-ID", "Operation",
						"Workflow-Bypass", "Branch-Protection-Override":
						ci.Trailers[key] = msgLine[idx+2:]
					}
				}
			}
		}

		commits = append(commits, ci)
	}
	return commits
}

// matchSimpleGlob provides basic glob matching (supports * and ** prefix/suffix).
func matchSimpleGlob(pattern, path string) bool {
	if pattern == "*" || pattern == "" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		return strings.HasSuffix(path, pattern[1:])
	}
	if strings.HasSuffix(pattern, "/*") {
		return strings.HasPrefix(path, pattern[:len(pattern)-1])
	}
	return strings.Contains(path, pattern)
}
