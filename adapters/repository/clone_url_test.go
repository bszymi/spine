package repository_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/adapters/repository"
)

func TestValidateCloneURLAccepts(t *testing.T) {
	// One positive case per allowed form (incl. SCP-like and an
	// embedded-credential variant that the redactor handles
	// downstream — the validator is a scheme/structure gate, not a
	// secret-leak gate).
	valid := map[string]string{
		"https":              "https://github.com/acme/svc.git",
		"ssh":                "ssh://git@github.com/acme/svc.git",
		"git+ssh":            "git+ssh://git@github.com/acme/svc.git",
		"scp-like":           "git@github.com:acme/svc.git",
		"https with creds":   "https://deploy:tok@github.com/acme/svc.git",
		"ssh no user":        "ssh://github.com/acme/svc.git",
		"https mixed-case":   "HTTPS://github.com/acme/svc.git",
		"ssh non-default-22": "ssh://git@github.com:2222/acme/svc.git",
	}
	for name, u := range valid {
		t.Run(name, func(t *testing.T) {
			if err := repository.ValidateCloneURL(u); err != nil {
				t.Errorf("expected %q to validate, got %v", u, err)
			}
		})
	}
}

func TestValidateCloneURLRejects(t *testing.T) {
	// One negative case per rejection class called out in the
	// acceptance criteria (file, git, http, ext::, leading-dash) plus
	// the structural rejections (empty, missing scheme, unknown
	// scheme, missing host).
	invalid := map[string]struct {
		url     string
		wantSub string
	}{
		"empty":                  {url: "", wantSub: "required"},
		"whitespace":             {url: "   ", wantSub: "required"},
		"file scheme":            {url: "file:///etc/passwd", wantSub: "file"},
		"git scheme":             {url: "git://example.com/repo.git", wantSub: "git"},
		"http rejected":          {url: "http://example.com/x.git", wantSub: "http"},
		"ext command exec":       {url: "ext::ssh foo", wantSub: "ext::"},
		"ext mixed case":         {url: "EXT::ssh foo", wantSub: "ext::"},
		"leading dash injection": {url: "--upload-pack=cmd https://example.com/x.git", wantSub: "-"},
		"missing scheme":         {url: "example.com/x.git", wantSub: "scheme"},
		"unsupported ftp":        {url: "ftp://example.com/x.git", wantSub: "ftp"},
		"unsupported sftp":       {url: "sftp://example.com/x.git", wantSub: "sftp"},
		"https no host":          {url: "https://", wantSub: "host"},
		"scp-like no user":       {url: "github.com:acme/svc.git", wantSub: "scheme"},
		// Padding rejection (codex P1): a padded URL with embedded
		// credentials must NOT validate, because Register/Update
		// persist the raw string and RedactCloneURL fails on it,
		// echoing user:token back to clients.
		"leading space with creds":  {url: " https://deploy:tok@github.com/acme/svc.git", wantSub: "whitespace"},
		"trailing space with creds": {url: "https://deploy:tok@github.com/acme/svc.git ", wantSub: "whitespace"},
		"leading tab":               {url: "\thttps://github.com/acme/svc.git", wantSub: "whitespace"},
	}
	for name, tc := range invalid {
		t.Run(name, func(t *testing.T) {
			err := repository.ValidateCloneURL(tc.url)
			if err == nil {
				t.Fatalf("expected error for %q", tc.url)
			}
			var spineErr *domain.SpineError
			if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrInvalidParams {
				t.Fatalf("expected ErrInvalidParams, got %v", err)
			}
			if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("expected error to mention %q, got %q", tc.wantSub, err.Error())
			}
		})
	}
}

func TestRedactCloneURL(t *testing.T) {
	cases := map[string]string{
		"https://user:pw@github.com/acme/svc.git": "https://github.com/acme/svc.git",
		"https://github.com/acme/svc.git":         "https://github.com/acme/svc.git",
		"git@github.com:acme/svc.git":             "git@github.com:acme/svc.git",
		"ssh://git@example.com/svc.git":           "ssh://git@example.com/svc.git",
	}
	for in, want := range cases {
		got := repository.RedactCloneURL(in)
		if got != want {
			t.Errorf("RedactCloneURL(%q) = %q, want %q", in, got, want)
		}
	}
}
