package git

import (
	"fmt"
	"net/url"
	"strings"
)

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

// ValidateCloneURL checks that a git URL uses a safe scheme.
// Blocks ext:: (arbitrary command execution), file:// (local filesystem access),
// and other dangerous schemes.
func ValidateCloneURL(gitURL string) error {
	if gitURL == "" {
		return fmt.Errorf("git URL is empty")
	}
	lower := strings.ToLower(gitURL)
	if strings.HasPrefix(lower, "ext::") {
		return fmt.Errorf("refusing dangerous git URL scheme ext::")
	}
	if strings.HasPrefix(lower, "file://") {
		return fmt.Errorf("refusing file:// git URL scheme")
	}
	if strings.HasPrefix(lower, "-") {
		return fmt.Errorf("refusing git URL starting with '-'")
	}
	// Allow https://, ssh://, git@host:path (SCP-like SSH)
	return nil
}
