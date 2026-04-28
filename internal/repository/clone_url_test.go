package repository_test

import (
	"errors"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/repository"
)

func TestValidateCloneURLAccepts(t *testing.T) {
	valid := []string{
		"https://github.com/acme/svc.git",
		"ssh://git@github.com/acme/svc.git",
		"git://example.com/svc.git",
		"file:///srv/git/svc.git",
		"git@github.com:acme/svc.git",
		"https://deploy:tok@github.com/acme/svc.git",
	}
	for _, u := range valid {
		t.Run(u, func(t *testing.T) {
			if err := repository.ValidateCloneURL(u); err != nil {
				t.Errorf("expected %q to validate, got %v", u, err)
			}
		})
	}
}

func TestValidateCloneURLRejects(t *testing.T) {
	invalid := map[string]string{
		"empty":          "",
		"whitespace":     "   ",
		"http rejected":  "http://example.com/x.git",
		"missing scheme": "example.com/x.git",
		"unsupported":    "ftp://example.com/x.git",
	}
	for name, u := range invalid {
		t.Run(name, func(t *testing.T) {
			err := repository.ValidateCloneURL(u)
			if err == nil {
				t.Fatalf("expected error for %q", u)
			}
			var spineErr *domain.SpineError
			if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrInvalidParams {
				t.Errorf("expected ErrInvalidParams, got %v", err)
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
