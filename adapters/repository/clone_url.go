package repository

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/bszymi/spine/core/domain"
)

// scpLikeGit matches git's SCP-like remote syntax ("user@host:path").
// It is not a URL but is a long-standing valid clone target, so the
// validator accepts it as a parallel form alongside the URL schemes
// in allowedSchemes. Hosts containing IPv6 brackets, colons, or
// userinfo passwords are deliberately not matched — those callers
// must use the explicit ssh:// URL form.
var scpLikeGit = regexp.MustCompile(`^[A-Za-z0-9_.+-]+@[A-Za-z0-9.-]+:\S+$`)

// allowedSchemes is the explicit allowlist of URL schemes accepted by
// ValidateCloneURL. Anything outside this set is rejected, including:
//   - http (cleartext password / token leakage)
//   - git (cleartext, no authentication)
//   - file (clone-from-local-FS exfiltrates whatever path the operator
//     names — see TASK-001/TASK-002 in INIT-022)
//   - ext (smart-protocol ext:: lets the URL specify an arbitrary
//     command for git to exec)
//   - any scheme starting with "-" (flag injection through git CLI)
//
// Convergence: internal/git.ValidateCloneURL uses the same allowlist
// so the workspace-clone path and the code-repo-binding path agree on
// what a "safe" clone URL is.
var allowedSchemes = map[string]struct{}{
	"https":   {},
	"ssh":     {},
	"git+ssh": {},
}

// ValidateCloneURL rejects empty, malformed, or unsafe clone URLs.
// Accepted forms:
//   - https://host/path
//   - ssh://[user@]host/path
//   - git+ssh://[user@]host/path
//   - SCP-like git@host:path (e.g. git@github.com:acme/repo.git)
//
// Any embedded credentials (https://user:token@host/...) are tolerated
// by the validator but must be redacted by RedactCloneURL before the
// URL is echoed in API responses or events.
func ValidateCloneURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return domain.NewError(domain.ErrInvalidParams, "clone_url is required")
	}
	// Reject any padding: Register/Update persist the raw string and
	// downstream redaction goes through RedactCloneURL → url.Parse,
	// which fails (Scheme="") on a leading space and echoes the raw
	// URL — leaking any embedded user:token. Forcing the validator
	// and the storage layer to agree on the byte-exact value closes
	// that round-trip leak.
	if raw != strings.TrimSpace(raw) {
		return domain.NewError(domain.ErrInvalidParams,
			"clone_url must not contain leading or trailing whitespace")
	}
	// Leading-dash check runs before url.Parse: a value like
	// "--upload-pack=cmd https://..." would otherwise sneak past the
	// scheme switch when git later interprets the leading token as a
	// CLI flag.
	if strings.HasPrefix(raw, "-") {
		return domain.NewError(domain.ErrInvalidParams,
			"clone_url must not start with '-' (flag injection)")
	}
	// ext:: is git's "smart" external-helper protocol; it lets the URL
	// itself specify an arbitrary command for git to exec. Reject
	// before url.Parse so the case-insensitive form is caught even
	// when the trailing argument contains spaces that would make
	// url.Parse error out generically.
	if strings.HasPrefix(strings.ToLower(raw), "ext::") {
		return domain.NewError(domain.ErrInvalidParams,
			"clone_url scheme ext:: is not allowed (arbitrary command execution)")
	}
	if scpLikeGit.MatchString(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return domain.NewError(domain.ErrInvalidParams, "clone_url is not a valid URL")
	}
	if u.Scheme == "" {
		return domain.NewError(domain.ErrInvalidParams,
			"clone_url must include a scheme (https, ssh, git+ssh) or use git@host:path syntax")
	}
	if _, ok := allowedSchemes[strings.ToLower(u.Scheme)]; !ok {
		return domain.NewError(domain.ErrInvalidParams,
			"clone_url scheme "+u.Scheme+" is not supported; allowed: https, ssh, git+ssh, or git@host:path")
	}
	if u.Host == "" {
		return domain.NewError(domain.ErrInvalidParams, "clone_url is missing a host")
	}
	return nil
}

// RedactCloneURL returns the clone URL with any embedded credential
// stripped, for echoing through the API or an event:
//
//   - https: all userinfo is removed. GitHub, GitLab and Bitbucket accept
//     a bare token in the username position (https://<token>@host/...),
//     which the old password-only rule echoed to every Reader of
//     GET /repositories (INIT-023 TASK-015). The cost is that a
//     non-secret username (Azure DevOps https://org@dev.azure.com/...)
//     is not shown either; the stored value is untouched.
//   - other schemes: the userinfo is removed only when it carries a
//     password. A bare user of an ssh URL (ssh://git@host/...,
//     git+ssh://git@host/...) is the SSH login, not a credential, and
//     is kept; so is the SCP-like form (user@host:path).
//   - a value url.Parse refuses (only reachable for a row stored before
//     ValidateCloneURL existed) loses everything before the last '@' of
//     its authority, so a parse failure never echoes a credential.
//
// Operators often embed deploy tokens directly in HTTPS clone URLs, so
// any code that surfaces such a URL must run it through this first.
func RedactCloneURL(raw string) string {
	if scpLikeGit.MatchString(raw) {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return redactUnparsedUserinfo(raw)
	}
	if u.User == nil {
		return raw
	}
	_, hasPassword := u.User.Password()
	if !hasPassword && !strings.EqualFold(u.Scheme, "https") {
		return raw
	}
	u.User = nil
	return u.String()
}

// redactUnparsedUserinfo strips the userinfo of a "scheme://authority"
// string url.Parse could not parse: everything between "://" and the
// last '@' before the path, query or fragment. A string without "://" is
// returned unchanged.
func redactUnparsedUserinfo(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return raw
	}
	rest := raw[i+3:]
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
		return raw[:i+3] + rest[at+1:]
	}
	return raw
}
