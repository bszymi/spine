package repository

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/bszymi/spine/internal/domain"
)

// scpLikeGit matches git's SCP-like remote syntax ("user@host:path").
// It is not a URL but is a long-standing valid clone target, so the
// validator accepts it as a parallel form alongside http(s)/ssh/git URLs.
var scpLikeGit = regexp.MustCompile(`^[A-Za-z0-9_.+-]+@[A-Za-z0-9.-]+:\S+$`)

// ValidateCloneURL rejects empty, malformed, or unsafe clone URLs.
// Accepted forms: https://, http:// (rejected — see below), ssh://,
// git://, file://, and SCP-like git@host:path. Any embedded
// credentials (https://user:token@host/...) are tolerated by the
// validator but must be redacted by RedactCloneURL before the URL is
// echoed in API responses or events.
func ValidateCloneURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return domain.NewError(domain.ErrInvalidParams, "clone_url is required")
	}
	if scpLikeGit.MatchString(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return domain.NewError(domain.ErrInvalidParams, "clone_url is not a valid URL")
	}
	switch u.Scheme {
	case "https", "ssh", "git", "file":
		// supported
	case "http":
		return domain.NewError(domain.ErrInvalidParams,
			"clone_url scheme http is not allowed; use https or ssh")
	case "":
		return domain.NewError(domain.ErrInvalidParams,
			"clone_url must include a scheme (https, ssh, git, file) or use git@host:path syntax")
	default:
		return domain.NewError(domain.ErrInvalidParams,
			"clone_url scheme "+u.Scheme+" is not supported")
	}
	if u.Host == "" && u.Scheme != "file" {
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
