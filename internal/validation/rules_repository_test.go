package validation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
)

const taskRepoPath = "initiatives/test/epics/e1/tasks/t1.md"

func multiRepoCatalog(t *testing.T) *repository.Catalog {
	t.Helper()
	cat, err := repository.ParseCatalog([]byte(`
- id: spine
  kind: spine
  name: Acme Spine
  default_branch: main
- id: payments-service
  kind: code
  name: Payments Service
  default_branch: main
- id: api-gateway
  kind: code
  name: API Gateway
  default_branch: develop
`), repository.PrimarySpec{})
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	return cat
}

func snapshotForCatalog(cat *repository.Catalog) validation.CatalogSnapshot {
	return func(_ context.Context) (*repository.Catalog, string, error) {
		return cat, "deadbeef", nil
	}
}

func primaryOnlyCatalog(t *testing.T) *repository.Catalog {
	t.Helper()
	cat, err := repository.ParseCatalog(nil, repository.PrimarySpec{Name: "Acme", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	return cat
}

func addTaskWithRepos(fs *fakeStore, repos []string) {
	fs.artifacts[taskRepoPath] = &store.ArtifactProjection{
		ArtifactPath: taskRepoPath,
		ArtifactType: string(domain.ArtifactTypeTask),
		Status:       string(domain.StatusPending),
		Repositories: repos,
	}
}

func ruleErrors(result domain.ValidationResult, ruleID string) []domain.ValidationError {
	var out []domain.ValidationError
	for _, e := range result.Errors {
		if e.RuleID == ruleID {
			out = append(out, e)
		}
	}
	return out
}

// ── RE-001 (existence) ──

func TestRE001_MissingRepositoriesIsValid(t *testing.T) {
	fs := newFakeStore()
	addTaskWithRepos(fs, nil)

	e := validation.NewEngine(fs, validation.WithCatalogSnapshot(snapshotForCatalog(multiRepoCatalog(t))))
	result := e.Validate(context.Background(), taskRepoPath)
	if hasRuleError(result, "RE-001") {
		t.Errorf("missing repositories should not fail RE-001; got %+v", result.Errors)
	}
}

func TestRE001_PrimaryOnlyValidatesInSingleRepoWorkspace(t *testing.T) {
	// Single-repo workspace with no /.spine/repositories.yaml committed:
	// ParseCatalog synthesises a primary-only catalog from empty bytes,
	// so an explicit `repositories: [spine]` resolves cleanly.
	fs := newFakeStore()
	addTaskWithRepos(fs, []string{"spine"})

	e := validation.NewEngine(fs, validation.WithCatalogSnapshot(snapshotForCatalog(primaryOnlyCatalog(t))))
	result := e.Validate(context.Background(), taskRepoPath)
	if hasRuleError(result, "RE-001") {
		t.Errorf("explicit [spine] in single-repo workspace must validate; got %+v", result.Errors)
	}
}

func TestRE001_UnknownInSingleRepoWorkspace(t *testing.T) {
	fs := newFakeStore()
	addTaskWithRepos(fs, []string{"payments-service"})

	e := validation.NewEngine(fs, validation.WithCatalogSnapshot(snapshotForCatalog(primaryOnlyCatalog(t))))
	result := e.Validate(context.Background(), taskRepoPath)
	errs := ruleErrors(result, "RE-001")
	if len(errs) != 1 {
		t.Fatalf("expected 1 RE-001 error, got %d: %+v", len(errs), result.Errors)
	}
	if !strings.Contains(errs[0].Message, "payments-service") || !strings.Contains(errs[0].Message, taskRepoPath) {
		t.Errorf("error must name task path and repo id, got %q", errs[0].Message)
	}
}

func TestRE001_NotRegisteredWithoutSnapshot(t *testing.T) {
	// Production callers that haven't yet wired a CatalogSnapshot should
	// get no RE-001 enforcement at all rather than a "single-repo
	// workspace" assumption that would silently reject any
	// multi-repo Task. RE-002/RE-003 still run.
	fs := newFakeStore()
	addTaskWithRepos(fs, []string{"payments-service"})

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), taskRepoPath)
	if hasRuleError(result, "RE-001") {
		t.Errorf("RE-001 must not fire when no catalog snapshot is wired; got %+v", result.Errors)
	}
}

func TestRE001_KnownCatalogEntryValidates(t *testing.T) {
	fs := newFakeStore()
	addTaskWithRepos(fs, []string{"payments-service", "api-gateway"})

	e := validation.NewEngine(fs, validation.WithCatalogSnapshot(snapshotForCatalog(multiRepoCatalog(t))))
	result := e.Validate(context.Background(), taskRepoPath)
	if hasRuleError(result, "RE-001") {
		t.Errorf("known catalog entries must validate; got %+v", result.Errors)
	}
}

func TestRE001_UnknownCatalogEntry(t *testing.T) {
	fs := newFakeStore()
	addTaskWithRepos(fs, []string{"payments-service", "billing-service"})

	e := validation.NewEngine(fs, validation.WithCatalogSnapshot(snapshotForCatalog(multiRepoCatalog(t))))
	result := e.Validate(context.Background(), taskRepoPath)
	errs := ruleErrors(result, "RE-001")
	if len(errs) != 1 {
		t.Fatalf("expected 1 RE-001 error for billing-service, got %d: %+v", len(errs), result.Errors)
	}
	if !strings.Contains(errs[0].Message, "billing-service") {
		t.Errorf("error must name billing-service, got %q", errs[0].Message)
	}
}

func TestRE001_PrimaryAlongsideCodeRepos(t *testing.T) {
	fs := newFakeStore()
	addTaskWithRepos(fs, []string{"spine", "payments-service"})

	e := validation.NewEngine(fs, validation.WithCatalogSnapshot(snapshotForCatalog(multiRepoCatalog(t))))
	result := e.Validate(context.Background(), taskRepoPath)
	if hasRuleError(result, "RE-001") {
		t.Errorf("explicit [spine, payments-service] must validate; got %+v", result.Errors)
	}
}

func TestRE001_CatalogSnapshotErrorSurfaces(t *testing.T) {
	fs := newFakeStore()
	addTaskWithRepos(fs, []string{"payments-service"})

	failing := func(_ context.Context) (*repository.Catalog, string, error) {
		return nil, "", errors.New("catalog read failed")
	}
	e := validation.NewEngine(fs, validation.WithCatalogSnapshot(failing))
	result := e.Validate(context.Background(), taskRepoPath)
	if !hasRuleError(result, "RE-001") {
		t.Fatalf("snapshot loader error must surface as RE-001; got %+v", result)
	}
}

func TestRE001_NonTaskIgnored(t *testing.T) {
	fs := newFakeStore()
	// An Epic projection with stray Repositories — RE rules must skip it.
	// Per TASK-001 the parser rejects repositories on non-Task artifacts,
	// but the rule itself should still gate by type so a hand-poked
	// projection can't trip it.
	fs.artifacts["initiatives/test/epics/e1/epic.md"] = &store.ArtifactProjection{
		ArtifactPath: "initiatives/test/epics/e1/epic.md",
		ArtifactType: string(domain.ArtifactTypeEpic),
		Status:       string(domain.StatusInProgress),
		Repositories: []string{"unknown-id"},
	}

	e := validation.NewEngine(fs, validation.WithCatalogSnapshot(snapshotForCatalog(multiRepoCatalog(t))))
	result := e.Validate(context.Background(), "initiatives/test/epics/e1/epic.md")
	if hasRuleError(result, "RE-001") || hasRuleError(result, "RE-002") || hasRuleError(result, "RE-003") {
		t.Errorf("RE-* rules must not fire on non-Task artifacts; got %+v", result.Errors)
	}
}

// ── RE-002 (duplicates) ──

func TestRE002_DuplicateRejected(t *testing.T) {
	fs := newFakeStore()
	addTaskWithRepos(fs, []string{"payments-service", "payments-service"})

	e := validation.NewEngine(fs, validation.WithCatalogSnapshot(snapshotForCatalog(multiRepoCatalog(t))))
	result := e.Validate(context.Background(), taskRepoPath)
	errs := ruleErrors(result, "RE-002")
	if len(errs) != 1 {
		t.Fatalf("expected 1 RE-002 error, got %d: %+v", len(errs), result.Errors)
	}
	if !strings.Contains(errs[0].Message, "payments-service") || !strings.Contains(errs[0].Message, taskRepoPath) {
		t.Errorf("duplicate error must name task path and repo id, got %q", errs[0].Message)
	}
}

func TestRE002_DistinctIDsAccepted(t *testing.T) {
	fs := newFakeStore()
	addTaskWithRepos(fs, []string{"spine", "payments-service", "api-gateway"})

	e := validation.NewEngine(fs, validation.WithCatalogSnapshot(snapshotForCatalog(multiRepoCatalog(t))))
	result := e.Validate(context.Background(), taskRepoPath)
	if hasRuleError(result, "RE-002") {
		t.Errorf("distinct IDs must not trigger RE-002; got %+v", result.Errors)
	}
}

// ── RE-003 (syntax) ──

func TestRE003_BadSyntaxRejected(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"uppercase", "Payments-Service"},
		{"underscore", "payments_service"},
		{"leading hyphen", "-payments"},
		{"trailing hyphen", "payments-"},
		{"double hyphen", "payments--service"},
		{"slash", "payments/service"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeStore()
			addTaskWithRepos(fs, []string{tc.id})

			e := validation.NewEngine(fs, validation.WithCatalogSnapshot(snapshotForCatalog(multiRepoCatalog(t))))
			result := e.Validate(context.Background(), taskRepoPath)
			if !hasRuleError(result, "RE-003") {
				t.Errorf("expected RE-003 error for %q, got %+v", tc.id, result.Errors)
			}
		})
	}
}

func TestRE003_GoodSyntaxAccepted(t *testing.T) {
	fs := newFakeStore()
	// Catalog only declares spine — the IDs below are not in it, so
	// RE-001 will fire, but RE-003 must not.
	addTaskWithRepos(fs, []string{"a", "a-b", "a1-b2-c3"})

	e := validation.NewEngine(fs, validation.WithCatalogSnapshot(snapshotForCatalog(multiRepoCatalog(t))))
	result := e.Validate(context.Background(), taskRepoPath)
	if hasRuleError(result, "RE-003") {
		t.Errorf("well-formed IDs must not trigger RE-003; got %+v", result.Errors)
	}
}

func TestRE_Classification(t *testing.T) {
	fs := newFakeStore()
	addTaskWithRepos(fs, []string{"BAD"})

	e := validation.NewEngine(fs, validation.WithCatalogSnapshot(snapshotForCatalog(multiRepoCatalog(t))))
	result := e.Validate(context.Background(), taskRepoPath)
	for _, err := range result.Errors {
		if strings.HasPrefix(err.RuleID, "RE-") && err.Classification != domain.ViolationStructuralError {
			t.Errorf("%s expected classification %s, got %s", err.RuleID, domain.ViolationStructuralError, err.Classification)
		}
	}
}
