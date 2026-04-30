package validation_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
)

// ── Fake Store ──

type fakeStore struct {
	store.Store
	artifacts map[string]*store.ArtifactProjection
	links     map[string][]store.ArtifactLink // keyed by source path
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		artifacts: make(map[string]*store.ArtifactProjection),
		links:     make(map[string][]store.ArtifactLink),
	}
}

func (f *fakeStore) GetArtifactProjection(_ context.Context, path string) (*store.ArtifactProjection, error) {
	proj, ok := f.artifacts[path]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "not found")
	}
	return proj, nil
}

func (f *fakeStore) QueryArtifacts(_ context.Context, query store.ArtifactQuery) (*store.ArtifactQueryResult, error) {
	var items []store.ArtifactProjection
	for _, proj := range f.artifacts {
		if query.Type != "" && proj.ArtifactType != query.Type {
			continue
		}
		items = append(items, *proj)
	}
	return &store.ArtifactQueryResult{Items: items}, nil
}

func (f *fakeStore) QueryArtifactLinks(_ context.Context, sourcePath string) ([]store.ArtifactLink, error) {
	return f.links[sourcePath], nil
}

func (f *fakeStore) QueryArtifactLinksByTarget(_ context.Context, targetPath string) ([]store.ArtifactLink, error) {
	var result []store.ArtifactLink
	for _, links := range f.links {
		for _, l := range links {
			if l.TargetPath == targetPath {
				result = append(result, l)
			}
		}
	}
	return result, nil
}

// ── Helpers ──

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func addArtifact(fs *fakeStore, path, artType, status string, meta map[string]string, links []domain.Link) {
	fs.artifacts[path] = &store.ArtifactProjection{
		ArtifactPath: path,
		ArtifactType: artType,
		Status:       status,
		Metadata:     mustJSON(meta),
		Links:        mustJSON(links),
	}
	// Also register links in the links map
	for _, link := range links {
		fs.links[path] = append(fs.links[path], store.ArtifactLink{
			SourcePath: path,
			TargetPath: link.Target,
			LinkType:   string(link.Type),
		})
	}
}

func hasRuleError(result domain.ValidationResult, ruleID string) bool {
	for _, e := range result.Errors {
		if e.RuleID == ruleID {
			return true
		}
	}
	return false
}

func hasRuleWarning(result domain.ValidationResult, ruleID string) bool {
	for _, w := range result.Warnings {
		if w.RuleID == ruleID {
			return true
		}
	}
	return false
}

// ── SI Tests ──

func TestSI001_ParentMissing(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Pending",
		map[string]string{"epic": "/initiatives/test/epics/missing.md", "initiative": "/initiatives/test/initiative.md"}, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleError(result, "SI-001") {
		t.Error("expected SI-001 error for missing parent")
	}
}

func TestSI001_ParentExists(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/epics/e1.md", "Epic", "In Progress",
		map[string]string{"initiative": "/initiatives/test/initiative.md"}, nil)
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Pending",
		map[string]string{"epic": "/initiatives/test/epics/e1.md", "initiative": "/initiatives/test/initiative.md"}, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if hasRuleError(result, "SI-001") {
		t.Error("unexpected SI-001 error when parent exists")
	}
}

func TestSI002_WrongParentType(t *testing.T) {
	fs := newFakeStore()
	// Task points to an Initiative instead of an Epic
	addArtifact(fs, "initiatives/test/initiative.md", "Initiative", "In Progress", nil, nil)
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Pending",
		map[string]string{"epic": "/initiatives/test/initiative.md", "initiative": "/initiatives/test/initiative.md"}, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleError(result, "SI-002") {
		t.Error("expected SI-002 error for wrong parent type")
	}
}

func TestSI003_TerminalParent(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/epics/e1.md", "Epic", "Completed",
		map[string]string{"initiative": "/initiatives/test/initiative.md"}, nil)
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "In Progress",
		map[string]string{"epic": "/initiatives/test/epics/e1.md", "initiative": "/initiatives/test/initiative.md"}, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleWarning(result, "SI-003") {
		t.Error("expected SI-003 warning for terminal parent")
	}
}

func TestSI004_InitiativeMismatch(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/epics/e1.md", "Epic", "In Progress",
		map[string]string{"initiative": "/initiatives/other/initiative.md"}, nil)
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Pending",
		map[string]string{"epic": "/initiatives/test/epics/e1.md", "initiative": "/initiatives/test/initiative.md"}, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleError(result, "SI-004") {
		t.Error("expected SI-004 error for initiative mismatch")
	}
}

func TestSI005_PathMismatch(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "wrong/path/task.md", "Task", "Pending", nil, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "wrong/path/task.md")
	if !hasRuleError(result, "SI-005") {
		t.Error("expected SI-005 error for path mismatch")
	}
}

func TestSI005_PathCorrect(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/task.md", "Task", "Pending", nil, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/task.md")
	if hasRuleError(result, "SI-005") {
		t.Error("unexpected SI-005 error for correct path")
	}
}

// ── LC Tests ──

func TestLC001_MissingReciprocalLink(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Pending", nil,
		[]domain.Link{{Type: "parent", Target: "/initiatives/test/epics/e1.md"}})
	addArtifact(fs, "initiatives/test/epics/e1.md", "Epic", "In Progress", nil, nil) // no contains back-link

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleWarning(result, "LC-001") {
		t.Error("expected LC-001 warning for missing reciprocal link")
	}
}

func TestLC004_BrokenLinkTarget(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Pending", nil,
		[]domain.Link{{Type: "related_to", Target: "/nonexistent.md"}})

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleError(result, "LC-004") {
		t.Error("expected LC-004 error for broken link target")
	}
}

func TestLC005_NonCanonicalPath(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Pending", nil,
		[]domain.Link{{Type: "related_to", Target: "relative/path.md"}})

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleError(result, "LC-005") {
		t.Error("expected LC-005 error for non-canonical path")
	}
}

// LC-004 with a wired GovernedFileResolver: a link to a pure-YAML
// governed artifact (validation policy, repository catalog) is not
// flagged as dangling when the resolver claims it. Anchors AC #4 of
// EPIC-006 TASK-007 — "ADRs can declare typed links to validation
// policies and validation catches dangling links".
func TestLC004_GovernedFileResolver_AcceptsKnownPolicyTarget(t *testing.T) {
	const policyTarget = "/governance/validation-policies/api-contract.yaml"
	fs := newFakeStore()
	addArtifact(fs, "architecture/adr/ADR-099-test.md", "ADR", "Accepted", nil,
		[]domain.Link{{Type: "related_to", Target: policyTarget}})

	resolver := func(_ context.Context, target string) bool {
		return target == policyTarget
	}
	e := validation.NewEngine(fs, validation.WithGovernedFileResolver(resolver))
	result := e.Validate(context.Background(), "architecture/adr/ADR-099-test.md")
	if hasRuleError(result, "LC-004") {
		t.Errorf("LC-004 should not flag a target the resolver claims; got %+v", result.Errors)
	}
}

func TestLC004_GovernedFileResolver_StillFlagsUnknownTarget(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "architecture/adr/ADR-099-test.md", "ADR", "Accepted", nil,
		[]domain.Link{{Type: "related_to", Target: "/governance/validation-policies/typo.yaml"}})

	// Resolver only accepts api-contract.yaml; the link points elsewhere.
	resolver := func(_ context.Context, target string) bool {
		return target == "/governance/validation-policies/api-contract.yaml"
	}
	e := validation.NewEngine(fs, validation.WithGovernedFileResolver(resolver))
	result := e.Validate(context.Background(), "architecture/adr/ADR-099-test.md")
	if !hasRuleError(result, "LC-004") {
		t.Error("LC-004 must still flag a target neither the projection nor the resolver knows about")
	}
}

// Default behavior (no resolver wired) is preserved: every
// non-projection target is dangling, just as before TASK-007.
func TestLC004_NoResolver_DefaultsToProjectionOnly(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "architecture/adr/ADR-099-test.md", "ADR", "Accepted", nil,
		[]domain.Link{{Type: "related_to", Target: "/governance/validation-policies/api-contract.yaml"}})

	e := validation.NewEngine(fs) // no WithGovernedFileResolver
	result := e.Validate(context.Background(), "architecture/adr/ADR-099-test.md")
	if !hasRuleError(result, "LC-004") {
		t.Error("LC-004 must flag non-projection link targets when no resolver is wired (preserves pre-TASK-007 behavior)")
	}
}

// NoopGovernedFileResolver is the explicit production placeholder while
// the policy registry wiring (TASK-004) is pending. It MUST behave as if
// no resolver were wired.
func TestNoopGovernedFileResolver_NeverResolves(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "architecture/adr/ADR-099-test.md", "ADR", "Accepted", nil,
		[]domain.Link{{Type: "related_to", Target: "/governance/validation-policies/api-contract.yaml"}})

	e := validation.NewEngine(fs,
		validation.WithGovernedFileResolver(validation.NoopGovernedFileResolver()))
	result := e.Validate(context.Background(), "architecture/adr/ADR-099-test.md")
	if !hasRuleError(result, "LC-004") {
		t.Error("NoopGovernedFileResolver must preserve LC-004's projection-only behavior")
	}
}

// LC-004 must not hand non-canonical (no leading-slash) paths to the
// resolver — the resolver contract relies on canonical inputs. LC-005
// flags non-canonical targets separately, so LC-004's job here is to
// emit the standard dangling-link error and skip the resolver.
func TestLC004_GovernedFileResolver_NotCalledForNonCanonicalTarget(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "architecture/adr/ADR-099-test.md", "ADR", "Accepted", nil,
		[]domain.Link{{Type: "related_to", Target: "governance/validation-policies/api-contract.yaml"}})

	resolverCalls := 0
	resolver := func(_ context.Context, _ string) bool {
		resolverCalls++
		return true // would otherwise mask the failure
	}
	e := validation.NewEngine(fs, validation.WithGovernedFileResolver(resolver))
	result := e.Validate(context.Background(), "architecture/adr/ADR-099-test.md")
	if !hasRuleError(result, "LC-004") {
		t.Error("LC-004 must still flag a non-canonical link target as dangling, even when a resolver would claim it")
	}
	if resolverCalls != 0 {
		t.Errorf("resolver must not be consulted for non-canonical paths; got %d calls", resolverCalls)
	}
}

// Projection lookup wins over the resolver: when the target IS in the
// projection, the resolver is not even consulted. Guards against an
// over-eager resolver masking projection truth.
func TestLC004_GovernedFileResolver_ProjectionTakesPrecedence(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "architecture/adr/ADR-099-test.md", "ADR", "Accepted", nil,
		[]domain.Link{{Type: "related_to", Target: "/architecture/some-doc.md"}})
	addArtifact(fs, "architecture/some-doc.md", "Architecture", "Living Document", nil, nil)

	resolverCalls := 0
	resolver := func(_ context.Context, _ string) bool {
		resolverCalls++
		return false
	}
	e := validation.NewEngine(fs, validation.WithGovernedFileResolver(resolver))
	result := e.Validate(context.Background(), "architecture/adr/ADR-099-test.md")
	if hasRuleError(result, "LC-004") {
		t.Error("LC-004 must trust the projection over the resolver")
	}
	if resolverCalls != 0 {
		t.Errorf("resolver must not be consulted when the projection has the target; got %d calls", resolverCalls)
	}
}

// ── SC Tests ──

func TestSC001_CompletedTaskNoAcceptance(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Completed", nil, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleWarning(result, "SC-001") {
		t.Error("expected SC-001 warning for missing acceptance")
	}
}

func TestSC001_CompletedTaskWithAcceptance(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Completed",
		map[string]string{"acceptance": "Approved"}, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if hasRuleWarning(result, "SC-001") {
		t.Error("unexpected SC-001 warning when acceptance is set")
	}
}

func TestSC002_CompletedEpicWithActiveChild(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/epics/e1.md", "Epic", "Completed",
		map[string]string{"initiative": "/initiatives/test/initiative.md"}, nil)
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "In Progress",
		map[string]string{"epic": "/initiatives/test/epics/e1.md"}, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/epics/e1.md")
	if !hasRuleWarning(result, "SC-002") {
		t.Error("expected SC-002 warning for active child of completed epic")
	}
}

func TestSC004_SupersededNoLink(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Superseded", nil, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleWarning(result, "SC-004") {
		t.Error("expected SC-004 warning for superseded without link")
	}
}

func TestSC005_BlockedByActiveTask(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/blocker.md", "Task", "In Progress", nil, nil)
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "In Progress", nil,
		[]domain.Link{{Type: "blocked_by", Target: "/initiatives/test/tasks/blocker.md"}})

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleWarning(result, "SC-005") {
		t.Error("expected SC-005 warning for blocked by active task")
	}
}

// ── SA Tests ──

func TestSA001_EmptyTaskContent(t *testing.T) {
	fs := newFakeStore()
	fs.artifacts["initiatives/test/tasks/t1.md"] = &store.ArtifactProjection{
		ArtifactPath: "initiatives/test/tasks/t1.md",
		ArtifactType: "Task", Status: "Pending", Content: "",
	}

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleWarning(result, "SA-001") {
		t.Error("expected SA-001 warning for empty content")
	}
}

func TestSA002_ADRNoRelatedLinks(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "architecture/adr/ADR-001.md", "ADR", "Proposed", nil, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "architecture/adr/ADR-001.md")
	if !hasRuleWarning(result, "SA-002") {
		t.Error("expected SA-002 warning for ADR without related links")
	}
}

// ── PC Tests ──

func TestPC001_BlockedByMissing(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Pending", nil,
		[]domain.Link{{Type: "blocked_by", Target: "/initiatives/test/tasks/missing.md"}})

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleError(result, "PC-001") {
		t.Error("expected PC-001 error for missing blocker")
	}
}

func TestPC002_TaskInProgressParentNotStarted(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/epics/e1.md", "Epic", "Pending",
		map[string]string{"initiative": "/initiatives/test/initiative.md"}, nil)
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "In Progress",
		map[string]string{"epic": "/initiatives/test/epics/e1.md", "initiative": "/initiatives/test/initiative.md"}, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")
	if !hasRuleWarning(result, "PC-002") {
		t.Error("expected PC-002 warning for parent not in progress")
	}
}

func TestPC003_EpicInProgressParentNotStarted(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/initiative.md", "Initiative", "Draft", nil, nil)
	addArtifact(fs, "initiatives/test/epics/e1.md", "Epic", "In Progress",
		map[string]string{"initiative": "/initiatives/test/initiative.md"}, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/epics/e1.md")
	if !hasRuleWarning(result, "PC-003") {
		t.Error("expected PC-003 warning for initiative not in progress")
	}
}

// ── Engine Tests ──

func TestValidateNonExistent(t *testing.T) {
	fs := newFakeStore()
	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "nonexistent.md")
	if result.Status != "failed" {
		t.Errorf("expected failed, got %s", result.Status)
	}
	// Engine errors should also have classification.
	if len(result.Errors) > 0 && result.Errors[0].Classification != domain.ViolationStructuralError {
		t.Errorf("expected structural_error classification on engine error, got %s", result.Errors[0].Classification)
	}
}

func TestValidateCleanArtifact(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/initiative.md", "Initiative", "In Progress", nil, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/initiative.md")
	if result.Status == "failed" {
		t.Errorf("expected passed or warnings, got failed: %v", result.Errors)
	}
}

// ── Violation Classification Tests ──

func TestValidationErrors_HaveClassification(t *testing.T) {
	fs := newFakeStore()
	// SI-001 (structural_error): task with missing parent
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Pending",
		map[string]string{"epic": "/nonexistent/epic.md", "initiative": "/nonexistent/init.md"}, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")

	for _, err := range result.Errors {
		if err.Classification == "" {
			t.Errorf("error %s missing classification", err.RuleID)
		}
	}
}

func TestValidationErrors_StructuralClassification(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Pending",
		map[string]string{"epic": "/nonexistent/epic.md", "initiative": "/nonexistent/init.md"}, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")

	for _, err := range result.Errors {
		if err.RuleID == "SI-001" && err.Classification != domain.ViolationStructuralError {
			t.Errorf("SI-001 expected classification %s, got %s", domain.ViolationStructuralError, err.Classification)
		}
	}
}

func TestValidationErrors_LinkClassification(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Pending", nil,
		[]domain.Link{{Type: "related_to", Target: "/nonexistent.md"}})

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")

	for _, err := range result.Errors {
		if err.RuleID == "LC-004" && err.Classification != domain.ViolationLinkInconsistency {
			t.Errorf("LC-004 expected classification %s, got %s", domain.ViolationLinkInconsistency, err.Classification)
		}
	}
}

func TestValidationErrors_PrereqClassification(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Pending", nil,
		[]domain.Link{{Type: "blocked_by", Target: "/initiatives/test/tasks/missing.md"}})

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")

	for _, err := range result.Errors {
		if err.RuleID == "PC-001" && err.Classification != domain.ViolationMissingPrereq {
			t.Errorf("PC-001 expected classification %s, got %s", domain.ViolationMissingPrereq, err.Classification)
		}
	}
}

func TestValidationWarnings_HaveClassification(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/tasks/t1.md", "Task", "Completed", nil, nil)

	e := validation.NewEngine(fs)
	result := e.Validate(context.Background(), "initiatives/test/tasks/t1.md")

	for _, w := range result.Warnings {
		if w.Classification == "" {
			t.Errorf("warning %s missing classification", w.RuleID)
		}
	}
}

func TestValidateAll(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/test/initiative.md", "Initiative", "In Progress", nil, nil)
	addArtifact(fs, "initiatives/test/epics/e1.md", "Epic", "In Progress",
		map[string]string{"initiative": "/initiatives/test/initiative.md"}, nil)

	e := validation.NewEngine(fs)
	results := e.ValidateAll(context.Background())
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}
