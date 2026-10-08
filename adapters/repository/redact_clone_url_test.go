package repository_test

// INIT-023 TASK-015 (the audit's latent gitpool item, companion leak):
// ported from the audit PoC (poc/crypto-secrets-auth/
// redact_cloneurl_test.go) with the assertion inverted (AC 14).
// RedactCloneURL stripped only a userinfo password, so a token in the
// username position — which GitHub, GitLab and Bitbucket accept, and
// which ValidateCloneURL accepts — was echoed by GET /api/v1/repositories.

import (
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/repository"
)

func TestRedactCloneURL_TokenInUsernameIsRedacted(t *testing.T) {
	const tok = "ghp_0123456789abcdefABCDEF0123456789abcd"
	for _, raw := range []string{
		"https://" + tok + "@github.com/acme/private.git",
		"https://" + tok + ":@github.com/acme/private.git",
		"https://" + tok + ":x-oauth-basic@github.com/acme/private.git",
		"https://x-access-token:" + tok + "@github.com/acme/private.git",
		"HTTPS://" + tok + "@github.com/acme/private.git",
	} {
		if err := repository.ValidateCloneURL(raw); err != nil {
			t.Fatalf("validator rejected %q: %v", raw, err)
		}
		red := repository.RedactCloneURL(raw)
		if strings.Contains(red, tok) {
			t.Errorf("%q -> %q still carries the token", raw, red)
		}
		if !strings.HasSuffix(red, "github.com/acme/private.git") {
			t.Errorf("%q -> %q lost the repository location", raw, red)
		}
	}
}

// Guard (passes before and after): SSH login names are not credentials,
// and a URL without userinfo is unchanged.
func TestRedactCloneURL_SSHLoginKept(t *testing.T) {
	for raw, want := range map[string]string{
		"ssh://git@github.com/acme/repo.git":     "ssh://git@github.com/acme/repo.git",
		"git+ssh://git@github.com/acme/repo.git": "git+ssh://git@github.com/acme/repo.git",
		"git@github.com:acme/repo.git":           "git@github.com:acme/repo.git",
		"https://github.com/acme/repo.git":       "https://github.com/acme/repo.git",
		"ssh://git:secret@github.com/acme/r.git": "ssh://github.com/acme/r.git",
	} {
		if got := repository.RedactCloneURL(raw); got != want {
			t.Errorf("%q -> %q, want %q", raw, got, want)
		}
	}
}

// A stored value url.Parse refuses never echoes its userinfo.
func TestRedactCloneURL_UnparsableValueRedacted(t *testing.T) {
	for raw, want := range map[string]string{
		"https://tok@github.com/a\x7fb.git":  "https://github.com/a\x7fb.git",
		" https://user:tok@github.com/a.git": " https://github.com/a.git",
		"https://user:tok@[::1/a.git":        "https://[::1/a.git",
		"not a url at all":                   "not a url at all",
	} {
		if got := repository.RedactCloneURL(raw); got != want {
			t.Errorf("%q -> %q, want %q", raw, got, want)
		}
	}
}
