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

// RedactCloneURL returns the clone URL with any embedded password
// stripped. A bare user component without a password is left intact —
// e.g. ssh://git@host/... uses "git" as the SSH login, not a
// credential. The SCP-like form (user@host:path) is also left intact
// for the same reason. Operators often embed deploy tokens directly
// in HTTPS clone URLs as user:token@host, so any code that surfaces
// such a URL through the API or event log should run it through this
// first.
func RedactCloneURL(raw string) string {
	if scpLikeGit.MatchString(raw) {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return raw
	}
	u.User = nil
	return u.String()
}
