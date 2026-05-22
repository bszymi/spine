package git

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// schemeURLPrefix matches the "scheme://" head of an absolute URL per
// RFC 3986. The capture group is the scheme. It is used by
// ValidateCloneURL to decide whether a clone target is a URL (subject
// to scheme allowlisting) or a non-URL form (SCP-like remote / local
// path), which are accepted unconditionally because their safety is
// enforced elsewhere — SCP-like remotes through SSH key-based auth,
// local paths through the workspace base-directory containment.
var schemeURLPrefix = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.\-]*)://`)

// RewriteRemoteURL injects token-based authentication into an HTTPS remote URL.
// The token is embedded as the password in the URL (e.g., https://user:token@host/repo.git).
// Returns the original URL unchanged if it is not HTTPS (e.g., SSH).
func RewriteRemoteURL(remoteURL, username, token string) (string, error) {
	if token == "" {
		return remoteURL, nil
	}

	// Only rewrite HTTPS URLs — SSH uses keys, not tokens.
	// Reject plain HTTP to avoid sending credentials over cleartext.
	if strings.HasPrefix(remoteURL, "http://") {
		return "", fmt.Errorf("refusing to add credentials to plain HTTP remote %q: use HTTPS", RedactURL(remoteURL))
	}
	if !strings.HasPrefix(remoteURL, "https://") {
		return remoteURL, nil
	}

	u, err := url.Parse(remoteURL)
	if err != nil {
		return "", fmt.Errorf("parse remote URL: %w", err)
	}

	if username == "" {
		username = "x-access-token"
	}

	u.User = url.UserPassword(username, token)
	return u.String(), nil
}

// RedactURL removes credentials from a URL for safe logging.
func RedactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "***"
	}
	return u.Redacted()
}

// ValidateRef checks that a git ref name is safe to pass as a command argument.
// Rejects refs starting with "-" (flag injection) and refs containing
// control characters or other unsafe patterns.
func ValidateRef(ref string) error {
	if ref == "" {
		return nil
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid git ref: must not start with '-'")
	}
	for _, c := range ref {
		if c < 0x20 || c == 0x7f || c == ' ' || c == '~' || c == '^' || c == ':' || c == '\\' {
			return fmt.Errorf("invalid git ref: contains unsafe character")
		}
	}
	if strings.Contains(ref, "..") {
		return fmt.Errorf("invalid git ref: must not contain '..'")
	}
	return nil
}

// ValidateCloneURL checks that a git URL uses a safe form.
//
// Convergence with internal/repository.ValidateCloneURL (INIT-022
// TASK-002): for any URL with an explicit scheme, both validators
// allow only https / ssh / git+ssh and reject everything else,
// including
//   - ext:: (arbitrary command execution via git's smart ext helper)
//   - file:// (clones operator-named local paths into the workspace
//     tree, where they become readable through the gateway)
//   - git:// (cleartext, no authentication)
//   - http:// (cleartext credential transit)
//   - any other scheme (ftp, rsync, smb, etc.)
//   - any URL starting with "-" (flag injection through the git CLI)
//
// The repository validator is stricter in one respect — it does not
// accept non-URL forms other than SCP-like remotes, because code-repo
// bindings round-trip through API and storage. This function
// additionally tolerates bare local paths (e.g. /tmp/seed-repo)
// because workspace provisioning uses them for local-seed clones in
// tests and bootstrap scripts; the workspace base directory provides
// the containment envelope for that flow.
func ValidateCloneURL(gitURL string) error {
	if gitURL == "" {
		return fmt.Errorf("git URL is empty")
	}
	if strings.HasPrefix(gitURL, "-") {
		return fmt.Errorf("refusing git URL starting with '-'")
	}
	// ext:: is git's smart-protocol external helper, which lets the
	// URL specify an arbitrary command for git to exec. It uses a
	// `scheme::arg` shape (single colon-pair, no `//`), so the URL
	// regex below cannot catch it — handle it explicitly first.
	if strings.HasPrefix(strings.ToLower(gitURL), "ext::") {
		return fmt.Errorf("refusing dangerous ext:: git URL scheme")
	}
	if m := schemeURLPrefix.FindStringSubmatch(gitURL); m != nil {
		switch strings.ToLower(m[1]) {
		case "https", "ssh", "git+ssh":
			return nil
		case "file":
			return fmt.Errorf("refusing file:// git URL scheme")
		case "git":
			return fmt.Errorf("refusing cleartext git:// URL scheme; use https or ssh")
		case "http":
			return fmt.Errorf("refusing cleartext http:// URL scheme; use https or ssh")
		default:
			return fmt.Errorf("refusing git URL scheme %s://; allowed: https, ssh, git+ssh", m[1])
		}
	}
	// No URL scheme — must be an SCP-like remote (user@host:path) or
	// a local filesystem path. Both are accepted; their safety is
	// enforced elsewhere (SSH key auth and workspace containment
	// respectively).
	return nil
}
