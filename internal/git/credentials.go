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
