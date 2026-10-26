package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
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
//	Diff                diff --name-status -z [eoo] <from'> <to'> --
//	Log                 log --format=... [-nN] [eoo] [<since'>..HEAD] -- [<path>]
//	ReadFile            --literal-pathspecs ls-tree -l -z --full-tree [eoo] <ref'> <path>, then cat-file -t <oid>, cat-file blob <oid>
//	                    (rev-parse --verify --quiet [eoo] <ref'>^{tree} only after a failed ls-tree)
//	ListFiles           ls-tree -r --name-only -z [eoo] <ref'>
//	TreeEntryType       --literal-pathspecs ls-tree -z --full-tree [eoo] <ref'> <path>   (no "--": after eoo it is a pathspec)
//	RefSHA              rev-parse --verify [eoo] <ref'>   (no call at all for an existing branch)
//	MergeBase           merge-base [eoo] <a'> <b'>
//	Push, PushBranch    push [eoo] <remote> <oid>^{commit}:refs/heads/<n>
//	DeleteRemoteBranch  push --delete [eoo] <remote> refs/heads/<n>
//
// The trailing "--" on diff and log stops a "rev" that is really a
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
//
// # Read bounds (INIT-023 TASK-003)
//
// No method buffers more than its limit (limits.go): ReadFile refuses a
// blob over the per-blob limit before reading it (BlobTooLargeError,
// ErrBlobTooLarge) and reads blobs only, refusing a directory or a
// gitlink unread, and a file entry that records a tag, a tree or a commit
// (NotBlobError, ErrNotBlob; wave 4 follow-up); every
// other read command fails past the per-command stdout limit
// (OutputTooLargeError, ErrOutputTooLarge), and mutating
// commands drain their stdout instead of failing on its size. git's own
// memory on read commands is bounded per allocation (GIT_ALLOC_LIMIT)
// and its delta-base cache is pinned, which leaves about three times the
// alloc limit per read (a delta-chain step: base, result, cache), not
// one. Diff and ListFiles read NUL-delimited output, so every path is
// returned raw — never C-quoted, never whitespace-trimmed.
//
// # Shared working tree (INIT-023 TASK-012)
//
// Every stateful method (Checkout, Commit, Merge, CreateBranch,
// DeleteBranch, WriteAndStageFile, MergeBound, ResetBranch) holds the
// tree's process-wide lock for its duration; a multi-step sequence holds it
// once through WithWorkingTree (WorkingTreeGuard), and an in-place
// EnterBranch scope until its Cleanup. Merge refuses an empty Target. On a
// tree registered with WithHomeBranch, a taken sequence first restores a
// clean checkout of the home branch, and the read methods resolve "HEAD"
// to refs/heads/<home>. Every git child is stopped with SIGTERM, not
// SIGKILL, when its context ends (gitcmd.go). See treelock.go.
type CLIClient struct {
	repoPath         string
	credentialHelper string   // path to external credential helper script
	pushEnv          []string // extra env vars for push operations (e.g., SMP_WORKSPACE_ID=xxx)
	pushToken        string   // token for HTTPS push auth (SPINE_GIT_PUSH_TOKEN)
	pushUsername     string   // username for token auth (default: x-access-token)
	askpassPath      string   // path to temporary GIT_ASKPASS script (created from pushToken)
	noAmbientAuth    bool     // WithoutAmbientCredentials: clone and push consult no host credential source

	// Read bounds (limits.go). Zero means the default, so even a
	// CLIClient not built by NewCLIClient is bounded.
	maxBlobBytes     int64 // one ReadFile (WithMaxBlobBytes)
	maxOutputBytes   int64 // stdout of every other command (WithMaxOutputBytes)
	maxGitAllocBytes int64 // GIT_ALLOC_LIMIT on read commands (WithMaxGitAllocBytes)
	// blobSize, when set, replaces the size ReadFile's entry probe
	// reported for a blob (it is given the blob's object id). It is a
	// field only so a test can make the probe lie and exercise the read's
	// own cap (the second line of defence).
	blobSize func(ctx context.Context, oid string) (int64, error)

	// Shared working tree (INIT-023 TASK-012, treelock.go): the
	// WithHomeBranch value (validated and registered for the tree by
	// NewCLIClient), and the refusal when it was not a valid branch name.
	homeBranch string
	homeErr    error

	// Checkout limits (INIT-024 TASK-003, treebudget.go): the client's
	// own WithTreeBudget (nil: the process default, if any) and
	// WithMutateTimeout (0: likewise). Read through CheckoutLimits() at
	// every call. measureHook is a test hook (SetMeasureHookForTest).
	treeBudget    *TreeBudget
	mutateTimeout time.Duration
	measureHook   func(MeasureTrace)
	// measureEnv, when set, is appended to the environment of the
	// measure's git-config read (filterDrivers): a budgeted Clone's
	// checkout client passes the clone's own environment.
	measureEnv []string
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
// SSH_ASKPASS, and for ssh:// and SCP-style remotes the process's SSH
// identity (ssh-agent, default and configured key files) — and send
// what they return to whatever host the clone URL or the remote names.
//
// With it, a client that has no askpass token and no credential helper
// runs clone and push without the host's git configuration and without
// the inherited credential environment:
//
//   - GIT_CONFIG_NOSYSTEM=1 and GIT_CONFIG_GLOBAL set to the null device
//     (git >= 2.32): no system or global config is read, so no helper,
//     askpass program, http.extraHeader (URL-scoped ones included),
//     sshCommand or URL rewrite configured there applies;
//     GIT_CONFIG_PARAMETERS and GIT_CONFIG_COUNT are cleared, so no
//     config injected through the environment applies either;
//   - `-c credential.helper=` and `-c http.extraHeader=` reset the
//     generic keys on every supported git version, including 2.30-2.31,
//     which do not know GIT_CONFIG_GLOBAL;
//   - GIT_ASKPASS and SSH_ASKPASS empty (an empty GIT_ASKPASS also stops
//     the core.askPass fallback), GIT_TERMINAL_PROMPT=0, SSH_AUTH_SOCK
//     empty, and GIT_SSH_COMMAND=noAmbientSSHCommand (which overrides
//     GIT_SSH and core.sshCommand).
//
// Because no system or global config is read, the host's
// non-credential git settings do not apply to such a clone or push
// either: a CA bundle or sslVerify, http.proxy, url.<base>.insteadOf,
// safe.directory. Their environment forms (GIT_SSL_CAINFO,
// GIT_SSL_NO_VERIFY, HTTPS_PROXY, ...) are left alone and still apply;
// safe.directory and insteadOf have none (KNOWN-LIMITATIONS.md). The
// client's other operations (commit, merge, log) are unaffected.
//
// A client that does carry a token or helper is unchanged: its own
// credential is used as before. Not covered (KNOWN-LIMITATIONS.md):
// curl's ~/.netrc lookup, which git enables for HTTP; config in the
// repository's own .git/config (a reused clone), which only someone with
// filesystem access can write; and, on git 2.30-2.31, a URL-scoped
// http.extraHeader in the global config.
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

// noAmbientSSHCommand is the ssh git runs for a client with ambient
// credentials disabled: `-F none` reads no ssh_config (no IdentityFile,
// ControlMaster/ControlPath, ProxyCommand or GSSAPI setting from the
// host), IdentityAgent=none ignores any agent, PubkeyAuthentication=no
// offers no key or certificate, password and keyboard-interactive
// authentication are off, and BatchMode=yes never prompts. Such a client
// therefore cannot authenticate over SSH at all; the gitpool has no
// per-binding SSH credential, so an SSH code repository with no
// credentials_ref can only be reached anonymously.
const noAmbientSSHCommand = "ssh -F none -o BatchMode=yes -o IdentityAgent=none -o PubkeyAuthentication=no" +
	" -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o ControlPath=none"

// noAmbientAuthEnv is appended to the environment of a clone or push when
// ambientAuthDisabled; os/exec keeps the last value of a duplicated key.
var noAmbientAuthEnv = []string{
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_CONFIG_GLOBAL=" + os.DevNull,
	"GIT_CONFIG_PARAMETERS=",
	"GIT_CONFIG_COUNT=0",
	"GIT_ASKPASS=",
	"SSH_ASKPASS=",
	"GIT_TERMINAL_PROMPT=0",
	"SSH_AUTH_SOCK=",
	"GIT_SSH_COMMAND=" + noAmbientSSHCommand,
}

// noAmbientAuthArgs are the command-scoped resets placed before the
// subcommand of a clone or push when ambientAuthDisabled. An empty value
// clears every value of the key accumulated from config files.
var noAmbientAuthArgs = []string{"-c", "credential.helper=", "-c", "http.extraHeader="}

// NewCLIClient creates a new Git client for the repository at the given path.
//
// Every client gets the read bounds (DefaultMaxBlobBytes,
// DefaultMaxOutputBytes, GIT_ALLOC_LIMIT = 4 x the per-blob limit) unless
// WithMaxBlobBytes / WithMaxOutputBytes / WithMaxGitAllocBytes say
// otherwise.
func NewCLIClient(repoPath string, opts ...CLIOption) *CLIClient {
	c := &CLIClient{
		repoPath:       repoPath,
		maxBlobBytes:   DefaultMaxBlobBytes,
		maxOutputBytes: DefaultMaxOutputBytes,
	}
	for _, opt := range opts {
		opt(c)
	}
	// Derived after every option, so WithMaxBlobBytes moves the default
	// alloc limit with it and a too-small explicit value is raised.
	c.maxGitAllocBytes = deriveGitAllocLimit(c.maxGitAllocBytes, c.blobLimit())
	c.setupHomeBranch()
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
		// No credential at all (WithoutAmbientCredentials): reset every
		// configured helper and extra header for this command.
		args = append(args, noAmbientAuthArgs...)
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
	//
	// With a checkout budget or a mutate deadline (INIT-024 TASK-003,
	// adversarial review; codex round 1) the clone does not check out: a
	// code repository's default branch is whatever its remote holds, and
	// `git clone` would write it before anything could measure it, inside
	// a command whose time is the network's. The clone runs with
	// --no-checkout; its HEAD tree is then measured (with a budget) and
	// written by a separate, deadlined `reset --hard` (checkoutClone)
	// that runs with exactly the clone's -c arguments and environment, so
	// a client without ambient credentials keeps the host's git config —
	// and any smudge filter it configures — out of the checkout too.
	limits := c.CheckoutLimits()
	split := limits.Budget != nil || limits.MutateTimeout > 0
	cfgArgs := append([]string{}, args...)
	args = append(args, "clone", "--no-tags")
	if split {
		args = append(args, "--no-checkout")
	}
	args = append(args, "--", url, path)
	_, statErr := os.Lstat(path)
	created := errors.Is(statErr, fs.ErrNotExist)
	cmd := newGitCmd(ctx, args...)
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
	// stderr is bounded like every other command's (INIT-023 TASK-003):
	// the remote controls the sideband ("remote: …") lines git relays,
	// and an unbounded buffer would let it grow the heap at will.
	stderr := &stderrSink{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		gitErr := classifyGitError("clone", strings.TrimSpace(stderr.String()))
		gitErr.Err = err
		return gitErr
	}
	if split {
		return c.checkoutClone(ctx, path, created, limits, cfgArgs, cmd.Env)
	}
	return nil
}

// checkoutClone writes the HEAD tree of a clone made with --no-checkout,
// after measuring it against limits' budget when there is one (INIT-024
// TASK-003). An empty repository (unborn HEAD) has nothing to write. Over
// the budget, or on any failure, the clone is removed (see discard) so no
// half-made clone is later taken for a usable one; the error is returned.
// The write is `git <cfgArgs> reset --hard` with the clone's own
// environment (env; nil inherits, as the clone did), under the mutate
// deadline.
func (c *CLIClient) checkoutClone(ctx context.Context, path string, created bool, limits CheckoutLimits, cfgArgs, env []string) error {
	cc := &CLIClient{repoPath: path, maxBlobBytes: c.maxBlobBytes, maxOutputBytes: c.maxOutputBytes,
		maxGitAllocBytes: c.maxGitAllocBytes, treeBudget: limits.Budget, mutateTimeout: limits.MutateTimeout,
		measureEnv: env}
	// A refused or failed clone is removed: all of it when Clone created
	// the directory; otherwise everything in it — git clones only into an
	// empty directory, so all of it is the clone's, including whatever a
	// stopped or failed `reset --hard` had already written (codex round
	// 2) — so the directory is empty again and a retry can clone into it.
	// RemoveAll never follows a link inside what it removes.
	discard := func(err error) error {
		if created {
			_ = os.RemoveAll(path)
			return err
		}
		if ents, rerr := os.ReadDir(path); rerr == nil {
			for _, e := range ents {
				_ = os.RemoveAll(filepath.Join(path, e.Name()))
			}
		}
		return err
	}
	out, _, err := cc.runWith(ctx, "clone", runOpts{}, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && strings.TrimSpace(out) == "" {
			return nil // unborn HEAD: an empty repository
		}
		return discard(err)
	}
	head := strings.TrimSpace(out)
	if err := cc.checkTrees(ctx, "clone", head); err != nil {
		return discard(err)
	}
	rctx, cancel := withMutateDeadline(ctx, limits.MutateTimeout)
	defer cancel()
	reset := newGitCmd(rctx, append(append([]string{}, cfgArgs...), "reset", "--hard", "-q")...)
	reset.Dir = path
	reset.Env = env
	stderr := &stderrSink{}
	reset.Stderr = stderr
	if err := reset.Run(); err != nil {
		if mutateDeadlineHit(ctx, rctx, limits.MutateTimeout) {
			return discard(mutateTimeout("clone", limits.MutateTimeout, err))
		}
		gitErr := classifyGitError("clone", strings.TrimSpace(stderr.String()))
		gitErr.Err = err
		return discard(gitErr)
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
//
// It takes the client's working tree for its duration (INIT-023 TASK-012,
// treelock.go): re-entrant inside WithWorkingTree, it never restores the
// tree — a sequence owns HEAD.
//
// With a checkout budget (INIT-024 TASK-003, treebudget.go) the branch's
// tree is measured first and one over the budget is refused
// (ErrTreeTooLarge) before `git switch` runs; without one nothing extra
// runs.
func (c *CLIClient) Checkout(ctx context.Context, branch string) error {
	short, _, err := checkBranch("checkout", "branch", branch)
	if err != nil {
		return err
	}
	h, err := c.lockTree(ctx)
	if err != nil {
		return err
	}
	defer h.release()
	if err := c.checkBranchTree(h.ctx, "checkout", "branch", branch, short); err != nil {
		return err
	}
	return c.switchTo(h.ctx, short)
}

// checkBranchTree is the checkout budget of a switch to branch short: the
// branch's tree, looked up exactly, measured (checkTrees). No budget: nil,
// and no git process — not even the lookup.
func (c *CLIClient) checkBranchTree(ctx context.Context, op, what, value, short string) error {
	if c.CheckoutLimits().Budget == nil {
		return nil
	}
	oid, err := c.resolveBranchOID(ctx, op, what, value, short)
	if err != nil {
		return err
	}
	return c.checkTrees(ctx, op, oid)
}

// switchTo runs the Checkout argv for an already-validated short name.
func (c *CLIClient) switchTo(ctx context.Context, short string) error {
	_, err := c.run(ctx, "checkout", "switch", "--no-guess", "--end-of-options", short)
	return err
}

// Commit creates a new commit with the staged changes.
// Commit messages include structured trailers per Git Integration §5.1.
// It takes the working tree like Checkout; the commit's own `rev-parse
// HEAD` reads the real HEAD (the commit just made), never the home branch.
func (c *CLIClient) Commit(ctx context.Context, opts CommitOpts) (CommitResult, error) {
	h, err := c.lockTree(ctx)
	if err != nil {
		return CommitResult{}, err
	}
	defer h.release()
	ctx = h.ctx
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

// Merge merges a source branch into opts.Target.
//
// Strategy "abort" or Source "--abort" is the in-band abort sentinel the
// engine (and SMP's operator) rely on: it runs exactly `git merge
// --abort` — no checkout, no grammar check, no other argv. An abort that
// also names a Target, or a Strategy "abort" whose Source is neither empty
// nor "--abort", is refused before git runs (INIT-023 TASK-012).
//
// Every other call needs a non-empty Target (INIT-023 TASK-012, audit F8):
// an empty Target used to mean "merge into whatever is checked out", and
// in standalone's shared tree that is main between sequences, so
// convergence (which sent no Target) merged unreviewed divergence-branch
// content straight into main before the run's review. It is refused with a
// permanent *GitError ("merge target required") before any git process
// starts, after Source's grammar check.
//
// Otherwise Source is Commit class (a full object id or a branch) and
// Target is Branch class; both are checked before any git process. A
// branch source is looked up exactly and merged BY OBJECT ID, so a
// same-named tag or ref, or a refs/tags/refs/heads/<n> planted for a
// deleted branch, can never be merged in its place: a missing source
// branch is ErrKindNotFound and no `git merge` runs. The lookup happens
// before the switch to Target, so a missing source changes nothing.
//
// A non-empty SourceSHA (INIT-023 TASK-005) replaces Source: the commit
// is verified (VerifyCommit's check) before any switch, exactly that
// object id is merged, Source is never resolved, and BaseSHA reports the
// target tip the merge was computed on.
//
// The whole call (lookup, switch, merge) holds the working tree.
//
// With a checkout budget (INIT-024 TASK-003) the merge's RESULT tree is
// computed (`git merge-tree --write-tree`) on the target tip and measured
// after the lookup and before the switch, and the target's own tree when
// the switch to it is a checkout (treebudget_merge.go); over the budget
// it is refused (ErrTreeTooLarge) with nothing switched, merged or moved.
// The abort path measures nothing: it restores the state the merge
// started from.
func (c *CLIClient) Merge(ctx context.Context, opts MergeOpts) (MergeResult, error) {
	if opts.Strategy == "abort" || opts.Source == "--abort" {
		if opts.Target != "" {
			return MergeResult{}, &GitError{Kind: ErrKindPermanent, Op: "merge",
				Message: "merge abort takes no target"}
		}
		if opts.Strategy == "abort" && opts.Source != "" && opts.Source != "--abort" {
			return MergeResult{}, &GitError{Kind: ErrKindPermanent, Op: "merge",
				Message: "merge abort takes no source"}
		}
		h, err := c.lockTree(ctx)
		if err != nil {
			return MergeResult{}, err
		}
		defer h.release()
		if _, err := c.run(h.ctx, "merge", "merge", "--abort"); err != nil {
			return MergeResult{}, err
		}
		return MergeResult{}, nil
	}
	if opts.SourceSHA != "" {
		if opts.Target == "" {
			return MergeResult{}, errMergeTargetRequired()
		}
		return c.mergeSHA(ctx, opts)
	}

	// Phase 1: every argument, before any git process.
	if err := checkCommit("merge", "source", opts.Source, false); err != nil {
		return MergeResult{}, err
	}
	if opts.Target == "" {
		return MergeResult{}, errMergeTargetRequired()
	}
	target, _, err := checkBranch("merge", "target", opts.Target)
	if err != nil {
		return MergeResult{}, err
	}

	h, err := c.lockTree(ctx)
	if err != nil {
		return MergeResult{}, err
	}
	defer h.release()
	ctx = h.ctx
	// Phase 2: resolve the source to an object id, then switch.
	source, err := c.resolveCommit(ctx, "merge", "source", opts.Source)
	if err != nil {
		return MergeResult{}, err
	}
	ours, err := c.checkMergeBudget(ctx, opts.Target, target, source)
	if err != nil {
		return MergeResult{}, err
	}
	if err := c.switchTo(ctx, target); err != nil {
		return MergeResult{}, err
	}
	if err := c.sameMergeBase(ctx, ours); err != nil {
		return MergeResult{}, err
	}
	return c.runMerge(ctx, opts, source)
}

// errMergeTargetRequired is Merge's refusal of an implicit target.
func errMergeTargetRequired() *GitError {
	return &GitError{Kind: ErrKindPermanent, Op: "merge", Message: "merge target required"}
}

// runMerge runs `git merge` of the already-resolved source object id
// into the current branch and reports the resulting HEAD.
func (c *CLIClient) runMerge(ctx context.Context, opts MergeOpts, source string) (MergeResult, error) {
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
//
// It holds the working tree (INIT-023 TASK-012). When base is "HEAD" and
// this call TAKES a tree that has a home branch, the tree is restored
// first, so a run or divergence branch is never cut from a foreign run
// branch a poisoned tree was left on; a failed restore is the
// *TreeRestoreError and no branch is created. Inside a sequence (re-entry)
// nothing is restored: the sequence's entry restore already did.
func (c *CLIClient) CreateBranch(ctx context.Context, name, base string) error {
	short, _, err := checkBranch("branch", "name", name)
	if err != nil {
		return err
	}
	if err := checkCommit("branch", "base", base, true); err != nil {
		return err
	}
	h, err := c.lockTree(ctx)
	if err != nil {
		return err
	}
	defer h.release()
	ctx = h.ctx
	if h.fresh && base == "HEAD" {
		if home := c.treeHome(); home != "" {
			if _, err := c.restoreHeld(ctx, home); err != nil {
				return err
			}
		}
	}
	from, err := c.resolveCommit(ctx, "branch", "base", base)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, "branch", "branch", "--end-of-options", short, from+"^{commit}")
	return err
}

// DeleteBranch deletes a local branch (Branch class).
// It holds the working tree like Checkout.
func (c *CLIClient) DeleteBranch(ctx context.Context, name string) error {
	short, _, err := checkBranch("branch", "name", name)
	if err != nil {
		return err
	}
	h, err := c.lockTree(ctx)
	if err != nil {
		return err
	}
	defer h.release()
	_, err = c.run(h.ctx, "branch", "branch", "-D", "--end-of-options", short)
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
//
// The output is NUL-delimited (`-z`, INIT-023 TASK-003, audit P3-9), so
// every path is returned raw. Without it git C-quoted any path holding a
// byte >= 0x80, a control character, `"` or `\` ("r\303\251sum\303\251"),
// and the quoted string no longer ended in ".md": a governed artifact in a
// non-ASCII directory (artifact.Slugify keeps every Unicode letter) was
// never projected, never status-cascaded, and NextID allocated its ID a
// second time. Rename detection still follows diff.renames (porcelain
// `git diff`). The parser is strict (parseNameStatusZ): a malformed or
// truncated stream is an error, never a partial list.
func (c *CLIClient) Diff(ctx context.Context, from, to string) ([]FileDiff, error) {
	if err := checkRev("diff", "from", from); err != nil {
		return nil, err
	}
	if err := checkRev("diff", "to", to); err != nil {
		return nil, err
	}
	fromRev, err := c.resolveRev(ctx, "diff", "from", c.homeRev(from))
	if err != nil {
		return nil, err
	}
	toRev, err := c.resolveRev(ctx, "diff", "to", c.homeRev(to))
	if err != nil {
		return nil, err
	}
	output, err := c.run(ctx, "diff", "diff", "--name-status", "-z", "--end-of-options", fromRev, toRev, "--")
	if err != nil {
		return nil, err
	}
	diffs, err := parseNameStatusZ(output)
	if err != nil {
		return nil, &GitError{Kind: ErrKindPermanent, Op: "diff", Message: "unparseable diff output", Err: err}
	}
	return diffs, nil
}

// errMalformedDiff is parseNameStatusZ's error. It never echoes a path:
// paths are committed, attacker-chosen text.
var errMalformedDiff = errors.New("malformed `git diff --name-status -z` output")

// parseNameStatusZ parses `git diff --name-status -z` output: records of
// "<status>\0<path>\0", or "<status>\0<src>\0<dst>\0" for a status
// starting with R (rename) or C (copy). Mapping:
//
//	A        added
//	D        deleted
//	M, T     modified (T: type change)
//	R<score> renamed, OldPath = src, Path = dst
//	C<score> added at dst (the copy source is unchanged; before -z this
//	         fell into the default case and reported the SOURCE as
//	         modified — only reachable with diff.renames=copies)
//	other    modified at the single path, as before
//
// A truncated or malformed stream (no terminating NUL, an empty status
// or path, a missing second path) returns an error and no records: one
// mis-split would shift every later record onto the wrong path. Empty
// output is (nil, nil).
func parseNameStatusZ(out string) ([]FileDiff, error) {
	if out == "" {
		return nil, nil
	}
	if !strings.HasSuffix(out, "\x00") {
		return nil, fmt.Errorf("%w: unterminated record", errMalformedDiff)
	}
	// Scanned field by field, not split: the paths are substrings of the
	// one output buffer, and no intermediate slice of every field is
	// built. out ends in NUL, so every field is NUL-terminated and rest is
	// empty exactly when the stream is exhausted.
	rest := out
	field := func() (string, bool) {
		if rest == "" {
			return "", false
		}
		f, tail, _ := strings.Cut(rest, "\x00")
		rest = tail
		return f, true
	}
	var diffs []FileDiff
	for rest != "" {
		status, _ := field()
		if !validDiffStatus(status) {
			return nil, fmt.Errorf("%w: unexpected status field at record %d", errMalformedDiff, len(diffs)+1)
		}
		paths := 1
		if status[0] == 'R' || status[0] == 'C' {
			paths = 2
		}
		p1, ok := field()
		p2 := ""
		if ok && paths == 2 {
			p2, ok = field()
		}
		if !ok {
			return nil, fmt.Errorf("%w: record %d is missing a path", errMalformedDiff, len(diffs)+1)
		}
		if p1 == "" || (paths == 2 && p2 == "") {
			return nil, fmt.Errorf("%w: record %d has an empty path", errMalformedDiff, len(diffs)+1)
		}
		var fd FileDiff
		switch status[0] {
		case 'A':
			fd = FileDiff{Path: p1, Status: "added"}
		case 'D':
			fd = FileDiff{Path: p1, Status: "deleted"}
		case 'R':
			fd = FileDiff{OldPath: p1, Path: p2, Status: "renamed"}
		case 'C':
			fd = FileDiff{Path: p2, Status: "added"}
		default: // M, T and anything else git may print for one path
			fd = FileDiff{Path: p1, Status: "modified"}
		}
		diffs = append(diffs, fd)
	}
	return diffs, nil
}

// validDiffStatus accepts git's name-status field: one upper-case letter
// optionally followed by a decimal score ("R100", "C075").
func validDiffStatus(s string) bool {
	if s == "" || s[0] < 'A' || s[0] > 'Z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Log returns commit history. A non-empty Since is Rev class; Path
// always follows the `--`.
//
// The history ends at HEAD — on a tree with a home branch (INIT-023
// TASK-012 §2.9) at refs/heads/<home>, looked up exactly: both the
// implicit default revision and the `<since>..HEAD` range name the home
// branch's object id, so a history query issued while a sequence has a
// run branch checked out still reads the home branch.
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
	tip := "HEAD"
	if home := c.treeHome(); home != "" {
		oid, err := c.resolveBranchOID(ctx, "log", "HEAD", headsPrefix+home, home)
		if err != nil {
			return nil, err
		}
		tip = oid
	}
	if opts.Since != "" {
		since, err := c.resolveRev(ctx, "log", "since", c.homeRev(opts.Since))
		if err != nil {
			return nil, err
		}
		args = append(args, fmt.Sprintf("%s..%s", since, tip))
	} else if tip != "HEAD" {
		args = append(args, tip)
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
//
// It reads blobs only (INIT-023 wave 4 follow-up). Before it, the
// content came from `git show [eoo] <rev>:<path> --`, which renders
// whatever the revision parser makes of the spec: a directory as a
// listing; a gitlink whose commit is in the repository as that commit
// with its whole patch — SMP measured 272 MiB in the git child at
// ae513dca, which neither the output cap nor GIT_ALLOC_LIMIT stops (a
// diff queue is millions of small allocations); and a path holding ".."
// or "^" as a range or a revision with its parents, so a missing
// "d..main" read as `git log` of `<rev>:d..main`, and even an existing
// file of that name was rendered as the range. The path never reaches a
// revision expression now:
//
//  1. `git --literal-pathspecs ls-tree -l -z --full-tree [eoo] <rev> <path>`
//     types the entry at exactly that path (the record whose path is
//     byte-equal to it; git normalises the pathspec) and reports its
//     object id and, for a file entry, the recorded object's size.
//     ls-tree reads only tree objects and the object's header, so a
//     gitlink whose commit is absent is still typed and an oversized
//     blob is never inflated. The type comes from the entry's MODE, so a
//     gitlink recording the id of a blob in this repository is still a
//     gitlink.
//  2. No entry: a miss (below). A directory or a gitlink: a
//     *GitError{Permanent, "read_file", NotBlobMessage} wrapping a
//     *NotBlobError, matching ErrNotBlob — refused before any object is
//     read.
//  3. A file entry (a blob mode) records an id, and nothing makes that
//     id a blob's: `git cat-file -t <oid>` reads the object's own type
//     from its header (adversarial review of the wave 4 follow-up). A
//     tag, a tree or a commit is the same not-a-file refusal, its
//     NotBlobError carrying that type and FileEntry. Without this check
//     the read below peeled a tag to its target blob — content no diff
//     shows, since git diff shows the tag object's own text — and failed
//     on a tree or a commit with a plain Permanent "bad file" that a
//     caller could not tell from a git failure.
//  4. A blob over the per-blob limit: a *GitError{Permanent,
//     "read_file", "blob exceeds read limit"} wrapping a
//     *BlobTooLargeError with the probed size, matching ErrBlobTooLarge.
//  5. `git cat-file blob <oid>` reads the probed object — by id, so a
//     ref that moves after the probe cannot change what is read (an
//     object id names one object, so the type step 3 read is the type
//     of what is read here) — into one buffer pre-sized to the probed
//     size and capped at the limit. It is byte-identical to `git show`
//     of a blob (no textconv or filter runs). Overrunning the cap is the
//     size refusal with Size -1.
//
// Every command runs with GIT_ALLOC_LIMIT, so a small blob stored as a
// delta on a huge base is refused by git before it allocates the large
// object; that is also Size -1, with GitAllocLimit set so the message
// names the real cause (the file itself may be small).
//
// The path is the one `git show` looked up (entryLookupPath): a "./" or
// "../" prefix is relative to the repository root ("./task.md" reads
// task.md, "./" is the root tree; climbing out of the repository is git's
// Permanent "outside repository" error), and a trailing "/" names only a
// directory.
//
// A miss keeps the error it has had since INIT-023 TASK-002 — git's
// "bad revision '<rev>:<path>'", ErrKindNotFound, Op "read_file" — now
// built here instead of read from a `git show` that would first try the
// spec as a range. It is the answer when the revision's tree has no
// entry at exactly that path; when the path can name no tree entry at
// all ("", a leading "/", an empty, "." or ".." component inside it, a
// NUL byte — git took those literally and found nothing); and when the
// revision names no tree, which `git rev-parse --verify --quiet
// <rev>^{tree}` proves by its exit status (1) after ls-tree failed —
// never git's stderr. Any other ls-tree or cat-file failure is classified
// from git's stderr as before (a missing blob in a corrupt repository is
// an error, not a miss; its stderr names only the object id).
//
// A 0-byte blob returns a non-nil, empty slice.
func (c *CLIClient) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
	if err := checkRev("read_file", "ref", ref); err != nil {
		return nil, err
	}
	rev, err := c.resolveRev(ctx, "read_file", "ref", c.homeRev(ref))
	if err != nil {
		return nil, err
	}
	limit := c.blobLimit()

	ent, err := c.readEntry(ctx, rev, path)
	if err != nil {
		if errors.Is(err, ErrOutputTooLarge) {
			return nil, readOverrun(ref, path, limit, err)
		}
		return nil, err
	}
	switch ent.typ {
	case "":
		return nil, readMiss(rev + ":" + path)
	case TreeEntryTree, TreeEntryCommit:
		return nil, &GitError{Kind: ErrKindPermanent, Op: "read_file", Message: NotBlobMessage,
			Err: &NotBlobError{Ref: ref, Path: path, Type: ent.typ}}
	}

	// A file entry: its mode says blob, which does not make the object it
	// records one (step 3).
	objType, err := c.objectType(ctx, ent.oid)
	if err != nil {
		if errors.Is(err, ErrOutputTooLarge) {
			return nil, readOverrun(ref, path, limit, err)
		}
		return nil, err
	}
	if objType != TreeEntryBlob {
		return nil, &GitError{Kind: ErrKindPermanent, Op: "read_file", Message: NotBlobMessage,
			Err: &NotBlobError{Ref: ref, Path: path, Type: objType, FileEntry: true}}
	}

	size := ent.size
	if c.blobSize != nil {
		if size, err = c.blobSize(ctx, ent.oid); err != nil {
			return nil, err
		}
	}
	var sizeHint int64
	switch {
	case size > limit:
		return nil, blobTooLarge(ref, path, size, limit)
	case size > 0:
		sizeHint = size
	}

	out, err := c.runBounded(ctx, "read_file", limit, sizeHint, "cat-file", "blob", ent.oid)
	if err != nil {
		if errors.Is(err, ErrOutputTooLarge) {
			return nil, readOverrun(ref, path, limit, err)
		}
		return nil, err
	}
	if out == nil {
		out = []byte{}
	}
	return out, nil
}

// maxObjectTypeBytes bounds `git cat-file -t` output: one type word.
const maxObjectTypeBytes = 64

// objectType is ReadFile's step 3: the type of the object oid names, as
// `git cat-file -t <oid>` prints it — read from the object's header, never
// its content, and never dereferenced (a tag is "tag", not its target's
// type). oid is a full object name from ls-tree, so it cannot be an option
// or a revision expression. Anything but one of the four object types is
// an error.
func (c *CLIClient) objectType(ctx context.Context, oid string) (string, error) {
	out, err := c.runBounded(ctx, "read_file", maxObjectTypeBytes, 0, "cat-file", "-t", oid)
	if err != nil {
		return "", err
	}
	switch typ := strings.TrimSuffix(string(out), "\n"); typ {
	case TreeEntryBlob, TreeEntryTree, TreeEntryCommit, ObjectTag:
		return typ, nil
	default:
		return "", &GitError{Kind: ErrKindPermanent, Op: "read_file",
			Message: fmt.Sprintf("unexpected object type %q", truncateRecord(typ))}
	}
}

// blobEntry is ReadFile's probe result: the tree entry at exactly the
// requested path.
type blobEntry struct {
	typ  string // TreeEntryBlob, TreeEntryTree, TreeEntryCommit, or "" for no entry
	oid  string // the entry's object id
	size int64  // the blob's size; -1 when ls-tree reported none
}

// readEntry types the entry path names in rev's tree (ReadFile step 1).
// The path is first resolved the way `git show <rev>:<path>` resolved it
// (entryLookupPath): a "./" or "../" prefix is relative to the
// repository root, and a trailing "/" names only a directory. A path no
// tree entry can have, and a revision that names no tree, are "no entry"
// (typ ""). The ls-tree output is one record for any path readEntryPath
// accepts; its cap leaves room for a second and fails the read past it
// (errors.Is ErrOutputTooLarge — a tree carrying duplicate entries is
// refused, not guessed at).
func (c *CLIClient) readEntry(ctx context.Context, rev, path string) (blobEntry, error) {
	lookup, dirOnly, root, err := c.entryLookupPath(path)
	if err != nil {
		return blobEntry{}, err
	}
	if root { // "./": the revision's root tree
		ok, err := c.revNamesTree(ctx, rev)
		if err != nil || !ok {
			return blobEntry{}, err
		}
		return blobEntry{typ: TreeEntryTree}, nil
	}
	if !readEntryPath(lookup) {
		return blobEntry{}, nil
	}
	limit := 2 * (int64(len(lookup)) + readEntryRecordBytes)
	out, err := c.runBounded(ctx, "read_file", limit, 0,
		"--literal-pathspecs", "ls-tree", "-l", "-z", "--full-tree", "--end-of-options", rev, lookup)
	if err != nil {
		if !errors.Is(err, ErrOutputTooLarge) && ctx.Err() == nil {
			if ok, rerr := c.revNamesTree(ctx, rev); rerr == nil && !ok {
				return blobEntry{}, nil
			}
		}
		return blobEntry{}, err
	}
	ent, err := parseBlobEntry(string(out), lookup)
	if err != nil || (dirOnly && ent.typ != TreeEntryTree) {
		return blobEntry{}, err
	}
	return ent, nil
}

// entryLookupPath is the tree path `git show <rev>:<path>` looked up for
// path, and whether only a directory matches it (a trailing "/": git
// listed "d/" as the directory d and found no "d/x.md/").
//
// A path that starts with "./" or "../" is relative to git's working
// directory, the repository root (git's resolve_relative_path): "."
// components and repeated slashes are dropped and ".." pops a component,
// so "./task.md" still reads task.md (codex review, wave 4 follow-up); a
// path that climbs out of the repository is git's own refusal, a
// Permanent "outside repository" error, as before. "./" alone is the root
// tree (lookup ""). Any other path was taken literally, so it is returned
// unchanged apart from its trailing slashes; readEntryPath then refuses
// the shapes no entry can have ("" and "/" among them).
func (c *CLIClient) entryLookupPath(path string) (lookup string, dirOnly, root bool, err error) {
	dirOnly = len(path) > 1 && strings.HasSuffix(path, "/")
	if !strings.HasPrefix(path, "./") && !strings.HasPrefix(path, "../") {
		return strings.TrimRight(path, "/"), dirOnly, false, nil
	}
	clean := pathpkg.Clean(path)
	switch {
	case clean == ".." || strings.HasPrefix(clean, "../"):
		return "", false, false, &GitError{Kind: ErrKindPermanent, Op: "read_file",
			Message: fmt.Sprintf("fatal: '%s' is outside repository at '%s'", path, c.repoPath)}
	case clean == ".":
		return "", true, true, nil
	}
	return clean, dirOnly, false, nil
}

// readEntryRecordBytes bounds an `ls-tree -l -z` record apart from its
// path: mode, type, a SHA-256 object id, the padded size and separators.
const readEntryRecordBytes = 128

// readEntryPath reports whether path can name a tree entry: non-empty, no
// NUL, and every "/"-separated component non-empty and neither "." nor
// "..". A tree entry's name can be none of those, and git took such a
// path literally (`<rev>:d/./x.md` and `<rev>:d//x.md` were misses), so
// it names no file.
func readEntryPath(path string) bool {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return false
	}
	for _, comp := range strings.Split(path, "/") {
		if comp == "" || comp == "." || comp == ".." {
			return false
		}
	}
	return true
}

// revNamesTree reports whether rev names a tree-ish:
// `git rev-parse --verify --quiet [eoo] <rev>^{tree}` exits 0 (yes) or 1,
// its "not a valid object name" answer (no; a name that resolves to a blob
// also prints why on stderr — the status decides, never the text). rev
// passed checkRev, so it holds no "^" or ":" of its own. Any other outcome
// — git died (128), could not run or was signalled — is an error.
func (c *CLIClient) revNamesTree(ctx context.Context, rev string) (bool, error) {
	_, err := c.runBounded(ctx, "read_file", maxProbeBytes+64, 0,
		"rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{tree}")
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// parseBlobEntry reads `ls-tree -l -z` records ("<mode> SP <type> SP
// <object> SP+ <size> TAB <path> NUL") and returns the entry whose path
// equals want, or a zero blobEntry when there is none. A record that does
// not have the documented shape is an error, never "no entry".
func parseBlobEntry(output, want string) (blobEntry, error) {
	for _, rec := range strings.Split(output, "\x00") {
		if rec == "" {
			continue
		}
		meta, p, ok := strings.Cut(rec, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 || !IsFullObjectName(fields[2]) {
			return blobEntry{}, &GitError{Kind: ErrKindPermanent, Op: "read_file",
				Message: fmt.Sprintf("unexpected ls-tree record %q", truncateRecord(rec))}
		}
		switch fields[1] {
		case TreeEntryBlob, TreeEntryTree, TreeEntryCommit:
		default:
			return blobEntry{}, &GitError{Kind: ErrKindPermanent, Op: "read_file",
				Message: fmt.Sprintf("unexpected tree entry type %q", truncateRecord(fields[1]))}
		}
		if p != want {
			continue
		}
		size := int64(-1)
		if n, err := strconv.ParseInt(fields[3], 10, 64); err == nil && n >= 0 {
			size = n
		}
		return blobEntry{typ: fields[1], oid: fields[2], size: size}, nil
	}
	return blobEntry{}, nil
}

// readMiss is ReadFile's answer for a miss: the *GitError the
// `git show [eoo] <spec> --` read returned for every miss since INIT-023
// TASK-002 — git's own text, classified ErrKindNotFound — built without
// running a command that would parse spec as a revision expression.
func readMiss(spec string) *GitError {
	return &GitError{Kind: ErrKindNotFound, Op: "read_file", Message: "fatal: bad revision '" + spec + "'"}
}

// blobTooLarge builds ReadFile's size refusal.
func blobTooLarge(ref, path string, size, limit int64) *GitError {
	return &GitError{
		Kind:    ErrKindPermanent,
		Op:      "read_file",
		Message: "blob exceeds read limit",
		Err:     &BlobTooLargeError{Ref: ref, Path: path, Size: size, Limit: limit},
	}
}

// readOverrun maps an output refusal of the probe or the read (cause
// matches ErrOutputTooLarge) to ReadFile's size refusal, Size -1. When
// git refused an allocation over GIT_ALLOC_LIMIT, the refusal carries
// that limit and says so: the content may be well under the per-blob
// limit (a small blob stored as a delta on a large base), and calling it
// "over the read limit" would point the operator at the wrong cause.
func readOverrun(ref, path string, limit int64, cause error) *GitError {
	tl := &BlobTooLargeError{Ref: ref, Path: path, Size: -1, Limit: limit}
	msg := "blob exceeds read limit"
	var ot *OutputTooLargeError
	if errors.As(cause, &ot) && ot.GitAlloc {
		tl.GitAllocLimit = ot.Limit
		msg = "blob read exceeds git allocation limit"
	}
	return &GitError{Kind: ErrKindPermanent, Op: "read_file", Message: msg, Err: tl}
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
//
// The write and the stage hold the working tree (INIT-023 TASK-012), taken
// before the confined write.
func (c *CLIClient) WriteAndStageFile(ctx context.Context, path, content string) error {
	rel, err := worktreefs.Clean(path)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	h, err := c.lockTree(ctx)
	if err != nil {
		return err
	}
	defer h.release()
	ctx = h.ctx
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
//
// The listing is NUL-delimited (`ls-tree -z`, INIT-023 TASK-003, audit
// P3-9): every path is returned raw. Before, git C-quoted non-ASCII and
// control-character paths (so discovery and NextID could not see them)
// and strings.TrimSpace mangled a leading or trailing space in the first
// or last path. Only the empty element after the final NUL is dropped;
// output that does not end in NUL is a truncated listing and an error.
func (c *CLIClient) ListFiles(ctx context.Context, ref, pattern string) ([]string, error) {
	if err := checkRev("list_files", "ref", ref); err != nil {
		return nil, err
	}
	rev, err := c.resolveRev(ctx, "list_files", "ref", c.homeRev(ref))
	if err != nil {
		return nil, err
	}
	output, err := c.run(ctx, "list_files", "ls-tree", "-r", "--name-only", "-z", "--end-of-options", rev)
	if err != nil {
		return nil, err
	}
	if output == "" {
		return nil, nil
	}
	if !strings.HasSuffix(output, "\x00") {
		return nil, &GitError{Kind: ErrKindPermanent, Op: "list_files", Message: "unterminated `git ls-tree -z` output"}
	}

	// Scanned, not split: the paths are substrings of the one output
	// buffer, and no intermediate slice of every field is built.
	var files []string
	for rest := output; rest != ""; {
		var p string
		p, rest, _ = strings.Cut(rest, "\x00")
		if p == "" {
			return nil, &GitError{Kind: ErrKindPermanent, Op: "list_files", Message: "empty path in `git ls-tree -z` output"}
		}
		if pattern == "" || matchSimpleGlob(pattern, p) {
			files = append(files, p)
		}
	}
	return files, nil
}

// Head returns the current HEAD commit SHA. On a tree with a home branch
// (INIT-023 TASK-012 §2.9) it is the object id of refs/heads/<home>,
// looked up exactly (a missing home branch is ErrKindNotFound), whatever a
// sequence has checked out at the moment.
func (c *CLIClient) Head(ctx context.Context) (string, error) {
	if home := c.treeHome(); home != "" {
		return c.resolveBranchOID(ctx, "head", "HEAD", headsPrefix+home, home)
	}
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
	rev, err := c.resolveRev(ctx, "ref-sha", "ref", c.homeRev(ref))
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
	revA, err := c.resolveRev(ctx, "merge-base", "a", c.homeRev(a))
	if err != nil {
		return "", err
	}
	revB, err := c.resolveRev(ctx, "merge-base", "b", c.homeRev(b))
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

// opClass decides how a command's output is bounded (INIT-023 TASK-003).
type opClass int

const (
	// opRead: stdout fails past its limit, and git runs with
	// GIT_ALLOC_LIMIT, a pinned delta-base cache and the C locale. Every
	// command that only reads the repository.
	opRead opClass = iota
	// opDiff: stdout fails past its limit, and git runs with the pinned
	// delta-base cache and the C locale but no GIT_ALLOC_LIMIT — rename
	// detection loads both blobs of a candidate pair, and a legitimate
	// large rename already on main must not fail Diff (which would wedge
	// IncrementalSync). Also the class of any op name not listed below:
	// output is never silently truncated.
	opDiff
	// opMutate: stdout is drained (head kept, rest discarded, never an
	// error) and no GIT_ALLOC_LIMIT. git performs the side effect before
	// it prints — `git merge --ff-only` killed by SIGPIPE after 4 KiB of
	// diffstat had already moved main — and checkout/merge materialise
	// legitimate large files.
	opMutate
)

// opClasses maps every op name the client passes to run/runBounded.
var opClasses = map[string]opClass{
	"read_file":  opRead,
	"list_files": opRead,
	"log":        opRead,
	"head":       opRead,
	"ref-sha":    opRead,
	"rev-parse":  opRead,
	"merge-base": opRead,
	"tree_entry": opRead,
	// measure_tree: the checkout budget's .gitattributes read (its
	// streamed listing builds the same environment, commandEnv).
	"measure_tree": opRead,
	"diff":         opDiff,
	"checkout":     opMutate,
	"merge":        opMutate,
	"commit":       opMutate,
	"add":          opMutate,
	"branch":       opMutate,
	"config":       opMutate,
	"push":         opMutate,
}

func classOf(op string) opClass {
	if c, ok := opClasses[op]; ok {
		return c
	}
	return opDiff
}

// run executes a git command and returns stdout. On error, classifies
// the failure. stdout is bounded by the client's per-command limit
// (runSink) and held once: the returned string is the sink's own buffer,
// not a copy of it, so a parser's substrings share it too.
func (c *CLIClient) run(ctx context.Context, op string, args ...string) (string, error) {
	sink, err := c.runSink(ctx, op, c.outputLimit(), 0, true, args...)
	if err != nil {
		return "", err
	}
	return sink.String(), nil
}

// runBounded is run for a caller that needs the stdout bytes (ReadFile):
// sizeHint pre-sizes the buffer when the caller knows the expected size.
func (c *CLIClient) runBounded(ctx context.Context, op string, limit, sizeHint int64, args ...string) ([]byte, error) {
	sink, err := c.runSink(ctx, op, limit, sizeHint, false, args...)
	if err != nil {
		return nil, err
	}
	return sink.Bytes(), nil
}

// runSink executes a git command in the repository and returns the sink
// holding its stdout, never more than limit bytes of it (text: kept as a
// string, see stdoutSink). See opClass for what each command class gets.
//
// A read command whose stdout overruns limit, or that git aborts on
// GIT_ALLOC_LIMIT, returns
// *GitError{Permanent, op, "output exceeds read limit", *OutputTooLargeError}
// BEFORE stderr is classified: git's own text for an alloc refusal
// ("packed object … is corrupt") would otherwise be reported as
// repository corruption. Stderr keeps its head and tail (stderrSink).
func (c *CLIClient) runSink(ctx context.Context, op string, limit, sizeHint int64, text bool, args ...string) (*stdoutSink, error) {
	class := classOf(op)

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
		// configured helper or extra header applies (the same rule as
		// Clone).
		args = append(append([]string{}, noAmbientAuthArgs...), args...)
	}
	// The mutate deadline (INIT-024 TASK-003, WithMutateTimeout): every
	// local mutating command — not push, whose time is the network's —
	// runs under it, so a checkout or merge the tree budget admitted still
	// cannot run unbounded. Zero (the library default): ctx alone.
	parent := ctx
	var deadline time.Duration
	if class == opMutate && op != "push" {
		deadline = c.CheckoutLimits().MutateTimeout
	}
	ctx, cancelDeadline := withMutateDeadline(ctx, deadline)
	defer cancelDeadline()

	cmd := newGitCmd(ctx, args...)
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

	// Read and diff commands inherit the parent environment, as before,
	// plus (a later duplicate wins in os/exec, so an inherited value
	// cannot loosen any of these):
	//
	//   - the C locale. git translates the "fatal: " / "error: " prefix of
	//     its alloc refusal, which isGitAllocRefusal must recognise, and
	//     the English stderr is also what classifyGitError's patterns
	//     expect (lookupBranch forces it for the same reason): a
	//     translated "bad revision" would be a permanent error instead of
	//     not-found. Their stdout does not depend on the locale: paths are
	//     raw (-z), blobs are bytes, dates are ISO, status letters fixed.
	//   - a pinned delta-base cache (deltaBaseCacheEnv), which bounds what
	//     GIT_ALLOC_LIMIT cannot: the bases git caches while it rebuilds
	//     a delta chain. It refuses nothing, so diff gets it too.
	//   - read commands only: GIT_ALLOC_LIMIT. diff is exempt (opDiff).
	allocLimit := c.gitAllocLimit()
	if class == opRead || class == opDiff {
		cmd.Env = c.commandEnv(class)
	}

	stdout := newStdoutSink(limit, sizeHint, class != opMutate, text)
	stderr := &stderrSink{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	errText := stderr.String()
	if err != nil && mutateDeadlineHit(parent, ctx, deadline) {
		return nil, mutateTimeout(op, deadline, err)
	}

	// Size refusals first. The alloc refusal is honoured even when git
	// exited 0: after a gentle allocation failure its output may be
	// incomplete.
	if class == opRead && isGitAllocRefusal(errText, allocLimit) {
		return nil, &GitError{Kind: ErrKindPermanent, Op: op, Message: "output exceeds read limit",
			Err: &OutputTooLargeError{Op: op, Limit: allocLimit, GitAlloc: true}}
	}
	if stdout.overflow && stdout.failing {
		return nil, &GitError{Kind: ErrKindPermanent, Op: op, Message: "output exceeds read limit",
			Err: &OutputTooLargeError{Op: op, Limit: limit}}
	}
	if err != nil {
		gitErr := classifyGitError(op, strings.TrimSpace(errText))
		gitErr.Err = err
		return nil, gitErr
	}
	return stdout, nil
}

// commandEnv is the environment of a read (opRead) or diff (opDiff)
// command: the inherited environment, the pinned delta-base cache, the C
// locale, and for reads GIT_ALLOC_LIMIT (see runSink). The checkout
// budget's streamed listing (treebudget.go) runs with exactly the read
// environment.
func (c *CLIClient) commandEnv(class opClass) []string {
	env := append(os.Environ(),
		deltaBaseCacheEnv(os.Getenv("GIT_CONFIG_PARAMETERS"), c.blobLimit()),
		"LC_ALL=C", "LANGUAGE=")
	if class == opRead {
		env = append(env, "GIT_ALLOC_LIMIT="+strconv.FormatInt(c.gitAllocLimit(), 10))
	}
	return env
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
		// Write any remaining trailers not in the standard set, sorted so
		// the message (and so CommitFilesOnto's commit id) is
		// deterministic (INIT-023 TASK-005 DC-2).
		extra := make([]string, 0, len(opts.Trailers))
		for k := range opts.Trailers {
			if k == "Trace-ID" || k == "Actor-ID" || k == "Run-ID" || k == "Operation" {
				continue
			}
			extra = append(extra, k)
		}
		sort.Strings(extra)
		for _, k := range extra {
			b.WriteString("\n")
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(opts.Trailers[k])
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
