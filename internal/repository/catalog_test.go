package repository_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/repository"
)

func TestParseCatalogEmptySynthesizesPrimary(t *testing.T) {
	spec := repository.PrimarySpec{
		Name:          "Acme Spine",
		DefaultBranch: "main",
		LocalPath:     "/var/spine/workspaces/acme/repos/spine",
	}

	for _, name := range []string{"nil", "empty", "whitespace"} {
		t.Run(name, func(t *testing.T) {
			var data []byte
			switch name {
			case "empty":
				data = []byte{}
			case "whitespace":
				data = []byte("   \n")
			}
			cat, err := repository.ParseCatalog(data, spec)
			if err != nil {
				t.Fatalf("ParseCatalog: %v", err)
			}
			primary := cat.Primary()
			if primary.ID != repository.PrimaryRepositoryID {
				t.Errorf("primary id: got %q, want %q", primary.ID, repository.PrimaryRepositoryID)
			}
			if primary.Kind != repository.KindSpine {
				t.Errorf("primary kind: got %q, want %q", primary.Kind, repository.KindSpine)
			}
			if primary.Name != spec.Name {
				t.Errorf("primary name: got %q, want %q", primary.Name, spec.Name)
			}
			if primary.DefaultBranch != spec.DefaultBranch {
				t.Errorf("primary default_branch: got %q, want %q", primary.DefaultBranch, spec.DefaultBranch)
			}
			if got := cat.List(); len(got) != 1 {
				t.Errorf("expected 1 entry for primary-only catalog, got %d", len(got))
			}
		})
	}
}

func TestParseCatalogValidMultiRepo(t *testing.T) {
	data := []byte(`
- id: spine
  kind: spine
  name: Acme Spine
  default_branch: main
  description: Governance and architecture.

- id: payments-service
  kind: code
  name: Payments Service
  default_branch: main
  role: service

- id: api-gateway
  kind: code
  name: API Gateway
  default_branch: develop
`)
	cat, err := repository.ParseCatalog(data, repository.PrimarySpec{Name: "fallback", DefaultBranch: "fallback"})
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}

	listed := cat.List()
	if len(listed) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(listed))
	}
	if listed[0].ID != "spine" {
		t.Errorf("primary not pinned first: got %q", listed[0].ID)
	}
	wantOrder := []string{"spine", "api-gateway", "payments-service"}
	for i, want := range wantOrder {
		if listed[i].ID != want {
			t.Errorf("List()[%d] = %q, want %q", i, listed[i].ID, want)
		}
	}

	if _, ok := cat.Get("nonexistent"); ok {
		t.Errorf("Get(unknown) returned ok=true")
	}
	got, ok := cat.Get("api-gateway")
	if !ok {
		t.Fatalf("Get(api-gateway) not found")
	}
	if got.DefaultBranch != "develop" {
		t.Errorf("api-gateway default_branch: got %q, want %q", got.DefaultBranch, "develop")
	}
}

func TestParseCatalogRejectsForbiddenAndUnknownFields(t *testing.T) {
	cases := map[string]string{
		"clone_url forbidden": `
- id: spine
  kind: spine
  name: S
  default_branch: main
- id: code
  kind: code
  name: C
  default_branch: main
  clone_url: https://example.com/x.git
`,
		"credentials forbidden": `
- id: spine
  kind: spine
  name: S
  default_branch: main
  credentials: bad
`,
		"local_path forbidden": `
- id: spine
  kind: spine
  name: S
  default_branch: main
- id: code
  kind: code
  name: C
  default_branch: main
  local_path: /r/c
`,
		"status forbidden": `
- id: spine
  kind: spine
  name: S
  default_branch: main
- id: code
  kind: code
  name: C
  default_branch: main
  status: active
`,
		"unknown field": `
- id: spine
  kind: spine
  name: S
  default_branch: main
- id: code
  kind: code
  name: C
  default_branch: main
  whatever: yes
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := repository.ParseCatalog([]byte(body), repository.PrimarySpec{})
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			assertInvalidParams(t, err)
		})
	}
}

func TestParseCatalogRejectsBadIDs(t *testing.T) {
	cases := map[string]string{
		"uppercase":           "Payments-Service",
		"underscore":          "payments_service",
		"leading hyphen":      "-payments",
		"trailing hyphen":     "payments-",
		"consecutive hyphens": "payments--service",
		"empty":               "",
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			body := strings.ReplaceAll(`
- id: spine
  kind: spine
  name: S
  default_branch: main
- id: __ID__
  kind: code
  name: C
  default_branch: main
`, "__ID__", id)
			_, err := repository.ParseCatalog([]byte(body), repository.PrimarySpec{})
			if err == nil {
				t.Fatalf("expected error for id %q", id)
			}
			assertInvalidParams(t, err)
		})
	}
}

// TestParseCatalogRejectsReservedIDs asserts that catalog IDs which
// would collide with git smart-HTTP path segments are rejected at
// parse time. Without this, `/git/{ws}/{repo_id}/...` parsing would
// be ambiguous for those IDs and the affected repository would be
// unreachable over HTTP.
func TestParseCatalogRejectsReservedIDs(t *testing.T) {
	for _, id := range []string{"info", "objects", "git-upload-pack", "git-receive-pack", "head"} {
		t.Run(id, func(t *testing.T) {
			body := strings.ReplaceAll(`
- id: spine
  kind: spine
  name: S
  default_branch: main
- id: __ID__
  kind: code
  name: C
  default_branch: main
`, "__ID__", id)
			_, err := repository.ParseCatalog([]byte(body), repository.PrimarySpec{})
			if err == nil {
				t.Fatalf("expected error for reserved id %q", id)
			}
			assertInvalidParams(t, err)
			if !strings.Contains(err.Error(), "reserved") {
				t.Errorf("expected 'reserved' in error, got %v", err)
			}
		})
	}
}

func TestParseCatalogIDLengthCap(t *testing.T) {
	long := strings.Repeat("a", repository.MaxIDLength+1)
	body := `
- id: spine
  kind: spine
  name: S
  default_branch: main
- id: ` + long + `
  kind: code
  name: C
  default_branch: main
`
	_, err := repository.ParseCatalog([]byte(body), repository.PrimarySpec{})
	if err == nil {
		t.Fatalf("expected error for over-long id")
	}
	assertInvalidParams(t, err)
}

func TestParseCatalogPrimaryInvariants(t *testing.T) {
	cases := map[string]string{
		"two primaries": `
- id: spine
  kind: spine
  name: S
  default_branch: main
- id: spine-2
  kind: spine
  name: S2
  default_branch: main
`,
		"no primary": `
- id: code-a
  kind: code
  name: A
  default_branch: main
`,
		"primary with wrong id": `
- id: not-spine
  kind: spine
  name: S
  default_branch: main
`,
		"code uses reserved id": `
- id: spine
  kind: spine
  name: S
  default_branch: main
- id: spine
  kind: code
  name: dup
  default_branch: main
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := repository.ParseCatalog([]byte(body), repository.PrimarySpec{})
			if err == nil {
				t.Fatalf("expected error")
			}
			assertInvalidParams(t, err)
		})
	}
}

func TestParseCatalogRejectsDuplicateIDs(t *testing.T) {
	body := `
- id: spine
  kind: spine
  name: S
  default_branch: main
- id: payments
  kind: code
  name: A
  default_branch: main
- id: payments
  kind: code
  name: B
  default_branch: develop
`
	_, err := repository.ParseCatalog([]byte(body), repository.PrimarySpec{})
	if err == nil {
		t.Fatalf("expected duplicate-id error")
	}
	assertInvalidParams(t, err)
}

func TestParseCatalogRejectsMissingRequiredFields(t *testing.T) {
	cases := map[string]string{
		"missing kind": `
- id: spine
  name: S
  default_branch: main
`,
		"missing name": `
- id: spine
  kind: spine
  default_branch: main
`,
		"missing default_branch": `
- id: spine
  kind: spine
  name: S
`,
		"missing id": `
- kind: spine
  name: S
  default_branch: main
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := repository.ParseCatalog([]byte(body), repository.PrimarySpec{})
			if err == nil {
				t.Fatalf("expected error")
			}
			assertInvalidParams(t, err)
		})
	}
}

func TestParseCatalogRejectsNonSequence(t *testing.T) {
	body := `
spine:
  kind: spine
  name: S
  default_branch: main
`
	_, err := repository.ParseCatalog([]byte(body), repository.PrimarySpec{})
	if err == nil {
		t.Fatalf("expected error for mapping at top level")
	}
	assertInvalidParams(t, err)
}

func assertInvalidParams(t *testing.T, err error) {
	t.Helper()
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) {
		t.Fatalf("expected SpineError, got %T: %v", err, err)
	}
	if spineErr.Code != domain.ErrInvalidParams {
		t.Errorf("expected code %q, got %q (msg=%q)", domain.ErrInvalidParams, spineErr.Code, spineErr.Message)
	}
}
