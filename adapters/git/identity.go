package git

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Spine's own git identity (INIT-024 TASK-010, GitHub CI after the push).
// The quarantine commit (treelock.go) is recorded as it, always; every
// other commit or merge the client makes falls back to it only where git
// itself cannot tell who is committing (identityFallbackEnv).
//
// Why a fallback is needed at all: `git commit`, `git merge` and `git
// commit-tree` refuse to run ("Committer identity unknown") when neither
// the environment (GIT_COMMITTER_NAME/EMAIL), nor any git config (system,
// global, the repository's own, -c) names the committer, and git cannot
// guess one — a user with an empty GECOS name, as in Spine's standalone
// image (`useradd spine`) and on GitHub's runners, or a host name with no
// domain. The client's commits pass --author (the actor) but never a
// committer, and its merges pass neither, so a repository Spine did not
// provision itself — the authoritative repository an operator mounts, a
// workspace cloned from an existing Spine repository (Provision sets a
// repo-local identity only on the repositories it initialises), every
// code-repository clone the gitpool makes — could not be written in such
// a deployment: artifact and workflow writes failed 500 git_error, and
// governed merges into code repositories failed.
//
// Why only a fallback: an identity the deployment configured is kept
// exactly. SMP's embedded scopes set a repo-local committer before they
// write (spinecore configureCommitterIdentity), and any deployment whose
// git resolves an identity today makes byte-identical commits: no fallback
// variable is added, so the command, its argv and its environment are
// unchanged. Only a command git would have refused gets the fallback.
//
// The values are treelock.go's (the quarantine commit's, since INIT-023
// TASK-012): the server is the committer of what it writes on an actor's
// behalf, and that actor stays the author wherever the caller names one.
const (
	serverIdentityName  = "Spine"
	serverIdentityEmail = "spine@spine.local"
)

// identityRoles selects the identities a commit-creating command needs
// from its environment or config.
type identityRoles uint8

const (
	// committerRole: every commit, merge commit and commit-tree.
	committerRole identityRoles = 1 << iota
	// authorRole: a commit with no explicit author (no --author, no
	// GIT_AUTHOR_NAME/EMAIL from the caller) and every merge commit.
	authorRole
)

// rolesFor returns the identities a commit needs given its explicit
// author: the committer always, the author too unless the caller names
// one completely (the client passes --author only when both fields are
// set).
func rolesFor(author Author) identityRoles {
	if author.Name != "" && author.Email != "" {
		return committerRole
	}
	return committerRole | authorRole
}

// identityRole is one git identity: the `git var` variable that resolves
// it and the environment variables that name it.
type identityRole struct {
	role     identityRoles
	identVar string
	nameVar  string
	emailVar string
}

var identityRoleTable = []identityRole{
	{committerRole, "GIT_COMMITTER_IDENT", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"},
	{authorRole, "GIT_AUTHOR_IDENT", "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL"},
}

// identityFallbackEnv returns the environment entries a commit-creating
// git command run in dir, with the inherited environment plus extra (the
// command's own additions, appended in the same order), needs so that it
// is never refused for want of an identity: for each requested role git
// cannot resolve, GIT_<ROLE>_NAME=Spine and GIT_<ROLE>_EMAIL=
// spine@spine.local. It returns nil — the command runs exactly as before
// — whenever git resolves every requested role.
//
// A role is resolved when the environment the command will see names it
// completely (both GIT_<ROLE>_NAME and GIT_<ROLE>_EMAIL non-empty: git
// takes them over any config, so no process is needed), or when `git var
// GIT_<ROLE>_IDENT` succeeds in dir with that environment — the same
// strict resolution `git commit` and `git merge` apply (environment, then
// committer.*/author.*, then user.* from every config scope, then git's
// guess from the passwd entry and host name, refused when the name is
// empty or the guessed address has no domain, and when user.useConfigOnly
// forbids guessing). Only git's own identity refusal counts as
// unresolved: a probe that fails for any other reason (a cancelled
// context, a broken repository) adds nothing, and the command then fails
// or succeeds on its own, as before.
//
// When git resolves part of a role (a configured name but no address),
// the fallback names both fields: the commit is attributed to one
// coherent identity rather than to half of each.
func identityFallbackEnv(ctx context.Context, dir string, extra []string, roles identityRoles) []string {
	var out []string
	for _, r := range identityRoleTable {
		if roles&r.role == 0 {
			continue
		}
		if envNamesIdentity(extra, r.nameVar, r.emailVar) || identityResolves(ctx, dir, extra, r.identVar) {
			continue
		}
		out = append(out, r.nameVar+"="+serverIdentityName, r.emailVar+"="+serverIdentityEmail)
	}
	return out
}

// envNamesIdentity reports whether the environment a command will see —
// the inherited one, then extra, a later entry winning as in os/exec —
// sets both nameVar and emailVar to non-empty values.
func envNamesIdentity(extra []string, nameVar, emailVar string) bool {
	name, email := os.Getenv(nameVar), os.Getenv(emailVar)
	for _, kv := range extra {
		if v, ok := strings.CutPrefix(kv, nameVar+"="); ok {
			name = v
		} else if v, ok := strings.CutPrefix(kv, emailVar+"="); ok {
			email = v
		}
	}
	return name != "" && email != ""
}

// identityResolves runs `git var <identVar>` in dir with the command's
// environment and reports false only when git refuses the identity
// (identityRefusal); the C locale keeps git's message English. git var
// reads config and prints one line; it writes nothing.
func identityResolves(ctx context.Context, dir string, extra []string, identVar string) bool {
	cmd := newGitCmd(ctx, "var", identVar)
	cmd.Dir = dir
	env := append(os.Environ(), extra...)
	env = append(env, "LC_ALL=C", "LANGUAGE=")
	cmd.Env = env
	cmd.Stdout = io.Discard
	stderr := &stderrSink{}
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	return !errors.As(err, &exitErr) || exitErr.ExitCode() != 128 || !identityRefusal(stderr.String())
}

// identityRefusal reports whether git's stderr is its refusal of an
// unresolvable identity (ident.c, every supported git): the "Please tell
// me who you are" hint it prints before a refused guess, and the fatal
// lines themselves — an empty name (the GitHub runner's: "empty ident
// name (for <runner@…>) not allowed"), a guessed name or address it will
// not use ("unable to auto-detect …"), guessing forbidden by
// user.useConfigOnly ("no name was given and auto-detection is
// disabled"), and a configured name with nothing but punctuation.
func identityRefusal(stderr string) bool {
	return containsAny(stderr,
		"Please tell me who you are",
		"empty ident name",
		"unable to auto-detect",
		"auto-detection is disabled",
		"name consists only of disallowed characters")
}
