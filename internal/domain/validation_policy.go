package domain

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/bszymi/spine/internal/yamlsafe"
	"gopkg.in/yaml.v3"
)

// ValidationPolicyCanonicalPathPrefix is the canonical directory under
// which validation policy YAML documents live. ADR-014 / artifact-schema
// §5.9 commit Spine to this location. Callers that resolve a policy
// path SHOULD compare against this constant rather than the literal
// string so a future relocation is a single edit.
const ValidationPolicyCanonicalPathPrefix = "/governance/validation-policies/"

// validationPolicyDocumentTopLevelKeys enumerates the YAML keys that
// ParseValidationPolicyDocument accepts at the document root. The set
// is intentionally narrow; any other key is rejected so a typo cannot
// silently degrade into "no policies loaded" — the same defense-in-depth
// pattern the repository catalog parser uses for entry-level keys.
var validationPolicyDocumentTopLevelKeys = map[string]struct{}{
	"schema_version": {},
	"policies":       {},
	"generated_at":   {},
}

// ValidationPolicySchemaVersion identifies the on-disk validation
// policy document schema. Stored in every committed policy document so
// readers reject or migrate older shapes when the schema evolves. The
// version is bumped only when a non-additive change is made — adding a
// new optional field is not a version bump.
const ValidationPolicySchemaVersion = "1"

// ValidationPolicyStatus is the lifecycle status of a single
// validation policy. Policies are versioned in Git; the status field
// records intent so consumers can decide whether to enforce, warn, or
// skip a policy that is being phased in or retired.
type ValidationPolicyStatus string

const (
	// ValidationPolicyStatusDraft — authored but not yet enforced.
	// Validation rules MAY skip draft policies entirely; reporting
	// surfaces SHOULD show them so authors can iterate.
	ValidationPolicyStatusDraft ValidationPolicyStatus = "draft"

	// ValidationPolicyStatusActive — currently enforced. The default
	// state for a published policy.
	ValidationPolicyStatusActive ValidationPolicyStatus = "active"

	// ValidationPolicyStatusDeprecated — still enforced, but slated for
	// removal. Operators should migrate away.
	ValidationPolicyStatusDeprecated ValidationPolicyStatus = "deprecated"

	// ValidationPolicyStatusSuperseded — replaced by a newer policy
	// (typically referenced via ADR linkage). MAY be skipped during
	// validation; readers should treat it as historical.
	ValidationPolicyStatusSuperseded ValidationPolicyStatus = "superseded"
)

// ValidValidationPolicyStatuses returns every recognized policy status.
func ValidValidationPolicyStatuses() []ValidationPolicyStatus {
	return []ValidationPolicyStatus{
		ValidationPolicyStatusDraft,
		ValidationPolicyStatusActive,
		ValidationPolicyStatusDeprecated,
		ValidationPolicyStatusSuperseded,
	}
}

// PolicyCheckKind discriminates what a policy check actually does.
// EPIC-006 wants the boundary to support both local commands first and
// external CI integrations later (TASK-003); this enum is the wire-side
// distinction so the same policy schema can describe either.
type PolicyCheckKind string

const (
	// PolicyCheckKindCommand — the runner executes a deterministic
	// shell command in a cloned repository working tree. The result row
	// is produced by the runner.
	PolicyCheckKindCommand PolicyCheckKind = "command"

	// PolicyCheckKindExternal — the result row is produced by an
	// external system (CI, human reviewer, security scanner). The
	// policy declares the check identifier; whoever fills the row is
	// out of scope for this schema.
	PolicyCheckKindExternal PolicyCheckKind = "external"
)

// ValidPolicyCheckKinds returns every recognized check kind.
func ValidPolicyCheckKinds() []PolicyCheckKind {
	return []PolicyCheckKind{PolicyCheckKindCommand, PolicyCheckKindExternal}
}

// PolicyCheckInterpretation describes whether a check's verdict can be
// reproduced byte-for-byte from inputs.
//
// AC #4 of TASK-002 requires that AI-assisted interpretation is
// explicitly non-blocking unless converted into a deterministic policy.
// This enum is the schema-level distinction; Validate refuses to let an
// advisory check declare blocking severity, satisfying that AC at the
// type system rather than at runtime.
type PolicyCheckInterpretation string

const (
	// PolicyCheckInterpretationDeterministic — the check produces the
	// same verdict on the same inputs. Blocking severity is permitted.
	PolicyCheckInterpretationDeterministic PolicyCheckInterpretation = "deterministic"

	// PolicyCheckInterpretationAdvisory — the verdict is interpretive
	// (e.g. an LLM review, a heuristic). MUST be paired with warning
	// severity. Surfaces as a result row but cannot block publish.
	PolicyCheckInterpretationAdvisory PolicyCheckInterpretation = "advisory"
)

// ValidPolicyCheckInterpretations returns every recognized
// interpretation kind.
func ValidPolicyCheckInterpretations() []PolicyCheckInterpretation {
	return []PolicyCheckInterpretation{
		PolicyCheckInterpretationDeterministic,
		PolicyCheckInterpretationAdvisory,
	}
}

// PolicySeverity controls whether a failed required check blocks
// publication. Both severities still produce evidence rows; only the
// gate behavior differs.
type PolicySeverity string

const (
	// PolicySeverityBlocking — a non-success terminal result blocks
	// publication. The default severity for deterministic compliance
	// checks (lint, tests, schema validators).
	PolicySeverityBlocking PolicySeverity = "blocking"

	// PolicySeverityWarning — a non-success terminal result is visible
	// in evidence and dashboards, but does not block publication.
	PolicySeverityWarning PolicySeverity = "warning"
)

// ValidPolicySeverities returns every recognized severity.
func ValidPolicySeverities() []PolicySeverity {
	return []PolicySeverity{
		PolicySeverityBlocking,
		PolicySeverityWarning,
	}
}

// PolicySelector identifies which runs a policy applies to. A policy
// applies to a run-and-repository pair when:
//
//   - the repository ID matches RepositoryIDs OR the repository's role
//     matches RepositoryRoles, AND
//   - PathPatterns is empty OR at least one changed path matches at
//     least one pattern.
//
// At least one of RepositoryIDs / RepositoryRoles must be set so a
// policy is never silently global by accident.
type PolicySelector struct {
	// RepositoryIDs are explicit catalog IDs (the same IDs declared in
	// /.spine/repositories.yaml). Empty means "match by role only".
	RepositoryIDs []string `json:"repository_ids,omitempty" yaml:"repository_ids,omitempty"`

	// RepositoryRoles are role labels (currently "spine" for the
	// primary repo and "code" for code repos, matching the catalog
	// `kind` field). Empty means "match by ID only".
	RepositoryRoles []string `json:"repository_roles,omitempty" yaml:"repository_roles,omitempty"`

	// PathPatterns are glob expressions evaluated against the run's
	// changed-path summary. Empty means "always applies regardless of
	// changed paths". Patterns use slash-separated POSIX globs.
	PathPatterns []string `json:"path_patterns,omitempty" yaml:"path_patterns,omitempty"`
}

// PolicyCheck is one declared check within a validation policy. The
// policy file is the contract; runners and external producers fill the
// matching CheckResult rows in execution evidence (see
// internal/domain/execution_evidence.go).
type PolicyCheck struct {
	// CheckID is the policy-defined identifier of the check. It is the
	// key matched against ExecutionEvidence.CheckResults[*].CheckID.
	// MUST be unique within the policy.
	CheckID string `json:"check_id" yaml:"check_id"`

	// Name is a human-readable label used in UI and reporting.
	// Optional; consumers fall back to CheckID when empty.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Description is the only multi-line free-form field on the policy
	// schema. Single-line fields reject newlines explicitly; this one
	// is left untouched so authors can document intent at length.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Kind discriminates between local commands and external check
	// references.
	Kind PolicyCheckKind `json:"kind" yaml:"kind"`

	// Command is the shell command executed by the runner when
	// Kind=command. MUST be empty when Kind=external — Validate
	// enforces. Single-line for shell-injection / log-bleed defense.
	Command string `json:"command,omitempty" yaml:"command,omitempty"`

	// Interpretation governs whether the check's verdict is
	// reproducible. Advisory checks cannot be blocking — the schema
	// realizes AC #4 at the type system layer.
	Interpretation PolicyCheckInterpretation `json:"interpretation" yaml:"interpretation"`

	// Severity governs whether a failed terminal result blocks publish.
	Severity PolicySeverity `json:"severity" yaml:"severity"`

	// TimeoutSeconds is the maximum runtime budget when Kind=command.
	// Zero means "no policy-declared timeout"; runner-side defaults
	// still apply. Negative values are rejected. Stored as integer
	// seconds for YAML/JSON round-trip determinism — a Go time.Duration
	// would marshal as nanoseconds and lose readability.
	TimeoutSeconds int64 `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
}

// ValidationPolicy is one deterministic enforcement recipe. ADRs link
// to it through typed links (EPIC-006 AC #2). The runtime expansion
// produces ExecutionEvidence rows whose CheckIDs match this policy's
// PolicyCheck.CheckID values.
type ValidationPolicy struct {
	// PolicyID is the document-scoped unique identifier of this policy.
	// Stable across versions of the same policy intent — bump Version
	// (and optionally re-issue with a new PolicyID via supersedes
	// relations) when the meaning changes.
	PolicyID string `json:"policy_id" yaml:"policy_id"`

	// Version is an opaque version label for the policy intent (e.g.
	// "1", "1.2", "2026-04"). Consumers MUST treat it as opaque; Spine
	// does not impose a semantic-version comparison.
	Version string `json:"version" yaml:"version"`

	// Title is a single-line human-readable label.
	Title string `json:"title" yaml:"title"`

	// Description is the multi-line free-form field on the policy.
	// Embedded newlines are permitted; everything else single-line.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Status is the lifecycle state of the policy itself.
	Status ValidationPolicyStatus `json:"status" yaml:"status"`

	// ADRPaths is the list of ADR canonical paths that authorize this
	// policy. AC #2 of EPIC-006 is realized by requiring at least one
	// ADR path on every policy — every policy can be traced back to a
	// governance decision.
	ADRPaths []string `json:"adr_paths" yaml:"adr_paths"`

	// Selector identifies which run-and-repository pairs the policy
	// applies to.
	Selector PolicySelector `json:"selector" yaml:"selector"`

	// Checks is the list of required checks declared by the policy.
	// MUST be non-empty — a policy with no checks asserts nothing.
	Checks []PolicyCheck `json:"checks" yaml:"checks"`
}

// ValidationPolicyDocument is the wrapper committed to Git. One file
// MAY hold multiple policies (see ValidationPolicyRef.PolicyID in the
// execution evidence schema, which exists precisely so one path can
// host several policies). SchemaVersion is at the document level so
// readers can reject unknown shapes without parsing further.
type ValidationPolicyDocument struct {
	SchemaVersion string `json:"schema_version" yaml:"schema_version"`

	// Policies is the list of validation policies in the document. MUST
	// contain at least one entry; PolicyIDs MUST be unique within the
	// document.
	Policies []ValidationPolicy `json:"policies" yaml:"policies"`

	// GeneratedAt is the canonicalization timestamp. Stored at document
	// level (not per policy) because the document is the unit of write.
	// Normalized to UTC by Canonicalize so cross-timezone producers
	// emit byte-identical output.
	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`
}

// PolicyByID returns the policy with the given ID, or false if not
// present. Lookup is linear because policy documents are small (a
// handful of policies at most); a map would just bloat the struct.
func (d ValidationPolicyDocument) PolicyByID(id string) (ValidationPolicy, bool) {
	for _, p := range d.Policies {
		if p.PolicyID == id {
			return p, true
		}
	}
	return ValidationPolicy{}, false
}

// CheckByID returns the policy check with the given ID, or false if
// not present.
func (p ValidationPolicy) CheckByID(id string) (PolicyCheck, bool) {
	for _, c := range p.Checks {
		if c.CheckID == id {
			return c, true
		}
	}
	return PolicyCheck{}, false
}

// IsBlocking reports whether a failed terminal result on this check
// should block publication.
func (c PolicyCheck) IsBlocking() bool {
	return c.Severity == PolicySeverityBlocking
}

// MatchesRepository reports whether this policy's selector applies to
// a repository identified by id and role. The match rule is OR across
// IDs and roles — explicit ID listing or role listing both qualify.
func (s PolicySelector) MatchesRepository(repositoryID, role string) bool {
	for _, id := range s.RepositoryIDs {
		if id == repositoryID {
			return true
		}
	}
	for _, r := range s.RepositoryRoles {
		if r == role {
			return true
		}
	}
	return false
}

// MatchesAnyPath reports whether at least one of the changed paths in
// the given summary matches at least one of the selector's
// PathPatterns. An empty PathPatterns list means "always matches" —
// patterns gate applicability, they do not over-restrict it.
//
// When the summary is Truncated, the visible Paths slice is by
// definition incomplete (the producer dropped entries to fit a size
// budget; see ChangedPathsSummary doc and execution-evidence schema
// §4.2). A "no visible match" result on a truncated summary is
// ambiguous: the matching file may have been dropped. To preserve
// blocking semantics for path-gated policies — silently skipping a
// required policy on a large diff would defeat AC #4 of EPIC-006 —
// MatchesAnyPath returns true conservatively whenever the summary is
// truncated. The cost is at most a redundant check execution; the
// alternative is missing required evidence on real diffs.
//
// Patterns use Go's path.Match semantics (POSIX-style globs with `*`,
// `?`, `[...]`). Patterns that are themselves invalid are treated as
// non-matching for safety; Validate rejects bad patterns at write
// time.
func (s PolicySelector) MatchesAnyPath(summary ChangedPathsSummary) bool {
	if len(s.PathPatterns) == 0 {
		return true
	}
	for _, pattern := range s.PathPatterns {
		for _, p := range summary.Paths {
			if matched, err := path.Match(pattern, p); err == nil && matched {
				return true
			}
		}
	}
	if summary.Truncated {
		return true
	}
	return false
}

// Canonicalize sorts every keyed slice and normalizes timestamps to
// UTC so two semantically-identical documents marshal byte-identically
// to JSON or YAML. AC #3 ("Policy execution is deterministic") starts
// with deterministic on-disk representation.
//
// Ordering rules:
//   - Document.Policies — by PolicyID
//   - Policy.ADRPaths — lexicographic
//   - Policy.Checks — by CheckID
//   - Selector.RepositoryIDs / RepositoryRoles / PathPatterns —
//     lexicographic
//
// Timestamp rules:
//   - Document.GeneratedAt → UTC.
func (d *ValidationPolicyDocument) Canonicalize() {
	d.GeneratedAt = d.GeneratedAt.UTC()
	sort.Slice(d.Policies, func(i, j int) bool {
		return d.Policies[i].PolicyID < d.Policies[j].PolicyID
	})
	for i := range d.Policies {
		d.Policies[i].canonicalize()
	}
}

func (p *ValidationPolicy) canonicalize() {
	sort.Strings(p.ADRPaths)
	sort.Strings(p.Selector.RepositoryIDs)
	sort.Strings(p.Selector.RepositoryRoles)
	sort.Strings(p.Selector.PathPatterns)
	sort.Slice(p.Checks, func(i, j int) bool {
		return p.Checks[i].CheckID < p.Checks[j].CheckID
	})
}

// Validate enforces the schema invariants storage and downstream
// consumers rely on. A malformed policy is rejected at write time so
// readers (validation service, dashboards, audit tooling) can trust
// every committed document.
func (d ValidationPolicyDocument) Validate() error {
	if d.SchemaVersion == "" {
		return NewError(ErrInvalidParams, "validation_policy: schema_version is required")
	}
	if d.SchemaVersion != ValidationPolicySchemaVersion {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy: unsupported schema_version %q (this build supports %q)",
				d.SchemaVersion, ValidationPolicySchemaVersion))
	}
	if d.GeneratedAt.IsZero() {
		return NewError(ErrInvalidParams, "validation_policy: generated_at is required")
	}
	if len(d.Policies) == 0 {
		return NewError(ErrInvalidParams, "validation_policy: at least one policy is required")
	}

	seen := make(map[string]struct{}, len(d.Policies))
	// checkIDOrigin records which policy first declared a given check_id
	// so the document-wide collision error can name both offenders.
	// Document-wide uniqueness is required because ExecutionEvidence.
	// RequiredChecks and CheckResults are keyed by check_id alone — two
	// policies in the same document with the same check_id would collapse
	// onto a single evidence row and a verdict for one policy could
	// silently satisfy or fail the other.
	checkIDOrigin := make(map[string]string)
	for i, p := range d.Policies {
		if err := p.Validate(); err != nil {
			return err
		}
		if _, dup := seen[p.PolicyID]; dup {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy: policies[%d] has duplicate policy_id %q", i, p.PolicyID))
		}
		seen[p.PolicyID] = struct{}{}
		for _, c := range p.Checks {
			if other, dup := checkIDOrigin[c.CheckID]; dup {
				return NewError(ErrInvalidParams,
					fmt.Sprintf("validation_policy: check_id %q is declared by both policies %q and %q in the same document — check_ids must be unique document-wide because evidence rows are keyed only by check_id",
						c.CheckID, other, p.PolicyID))
			}
			checkIDOrigin[c.CheckID] = p.PolicyID
		}
	}
	return nil
}

// Validate enforces invariants on a single policy. Called by the
// document-level Validate; safe to call directly when a producer is
// constructing a single policy in memory.
func (p ValidationPolicy) Validate() error {
	if p.PolicyID == "" {
		return NewError(ErrInvalidParams, "validation_policy: policy_id is required")
	}
	if strings.ContainsAny(p.PolicyID, "\n\r") {
		return NewError(ErrInvalidParams, "validation_policy: policy_id must not contain newlines")
	}
	if p.Version == "" {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: version is required", p.PolicyID))
	}
	if strings.ContainsAny(p.Version, "\n\r") {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: version must not contain newlines", p.PolicyID))
	}
	if p.Title == "" {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: title is required", p.PolicyID))
	}
	if strings.ContainsAny(p.Title, "\n\r") {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: title must not contain newlines", p.PolicyID))
	}
	if !isValidValidationPolicyStatus(p.Status) {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: invalid status %q", p.PolicyID, p.Status))
	}

	if len(p.ADRPaths) == 0 {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: at least one adr_path is required", p.PolicyID))
	}
	adrSeen := make(map[string]struct{}, len(p.ADRPaths))
	for _, a := range p.ADRPaths {
		if a == "" {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: adr_paths entries must not be empty", p.PolicyID))
		}
		if strings.ContainsAny(a, "\n\r") {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: adr_paths entries must not contain newlines", p.PolicyID))
		}
		// Canonical artifact reference per artifact-schema §3.2: every
		// entry MUST be a repository-relative path starting with "/".
		// Without this guard a policy could declare a relative path
		// (e.g. "architecture/adr/ADR-014.md") that the validation
		// service cannot reconcile against the ADR's reverse link,
		// silently weakening AC #2 (ADR traceability).
		if !strings.HasPrefix(a, "/") {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: adr_paths entry %q must be a canonical path starting with %q",
					p.PolicyID, a, "/"))
		}
		if _, dup := adrSeen[a]; dup {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: adr_paths contains duplicate entry %q", p.PolicyID, a))
		}
		adrSeen[a] = struct{}{}
	}

	if err := p.Selector.validate(p.PolicyID); err != nil {
		return err
	}

	if len(p.Checks) == 0 {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: at least one check is required", p.PolicyID))
	}
	checkSeen := make(map[string]struct{}, len(p.Checks))
	for i, c := range p.Checks {
		if err := c.validate(p.PolicyID, i); err != nil {
			return err
		}
		if _, dup := checkSeen[c.CheckID]; dup {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: checks contains duplicate check_id %q", p.PolicyID, c.CheckID))
		}
		checkSeen[c.CheckID] = struct{}{}
	}

	return nil
}

func (s PolicySelector) validate(policyID string) error {
	if len(s.RepositoryIDs) == 0 && len(s.RepositoryRoles) == 0 {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: selector must declare at least one repository_id or repository_role",
				policyID))
	}
	idSeen := make(map[string]struct{}, len(s.RepositoryIDs))
	for _, id := range s.RepositoryIDs {
		if id == "" {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: selector.repository_ids entries must not be empty", policyID))
		}
		if strings.ContainsAny(id, "\n\r") {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: selector.repository_ids entries must not contain newlines",
					policyID))
		}
		if _, dup := idSeen[id]; dup {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: selector.repository_ids contains duplicate %q", policyID, id))
		}
		idSeen[id] = struct{}{}
	}
	roleSeen := make(map[string]struct{}, len(s.RepositoryRoles))
	for _, r := range s.RepositoryRoles {
		if r == "" {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: selector.repository_roles entries must not be empty", policyID))
		}
		if strings.ContainsAny(r, "\n\r") {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: selector.repository_roles entries must not contain newlines",
					policyID))
		}
		if _, dup := roleSeen[r]; dup {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: selector.repository_roles contains duplicate %q",
					policyID, r))
		}
		roleSeen[r] = struct{}{}
	}
	patSeen := make(map[string]struct{}, len(s.PathPatterns))
	for _, pattern := range s.PathPatterns {
		if pattern == "" {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: selector.path_patterns entries must not be empty", policyID))
		}
		if strings.IndexFunc(pattern, unicode.IsSpace) >= 0 {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: selector.path_patterns entries must not contain whitespace",
					policyID))
		}
		// path.Match(pattern, "") returns no error when the syntax fault
		// sits past the prefix that fails the literal compare — e.g.
		// "cmd/[invalid" mismatches at 'c' vs '\0' before the matcher
		// reaches the unclosed bracket. Matching the pattern against
		// itself forces the matcher past every literal segment, surfacing
		// late syntax errors that the empty-string probe misses.
		for _, probe := range []string{"", pattern} {
			if _, err := path.Match(pattern, probe); err != nil {
				return NewError(ErrInvalidParams,
					fmt.Sprintf("validation_policy[%q]: selector.path_patterns entry %q is not a valid glob: %s",
						policyID, pattern, err))
			}
		}
		if _, dup := patSeen[pattern]; dup {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: selector.path_patterns contains duplicate %q",
					policyID, pattern))
		}
		patSeen[pattern] = struct{}{}
	}
	return nil
}

func (c PolicyCheck) validate(policyID string, index int) error {
	if c.CheckID == "" {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: checks[%d].check_id is required", policyID, index))
	}
	if strings.ContainsAny(c.CheckID, "\n\r") {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: checks[%d].check_id must not contain newlines", policyID, index))
	}
	if strings.ContainsAny(c.Name, "\n\r") {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: checks[%q].name must not contain newlines", policyID, c.CheckID))
	}
	if !isValidPolicyCheckKind(c.Kind) {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: checks[%q] has invalid kind %q",
				policyID, c.CheckID, c.Kind))
	}
	if !isValidPolicyCheckInterpretation(c.Interpretation) {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: checks[%q] has invalid interpretation %q",
				policyID, c.CheckID, c.Interpretation))
	}
	if !isValidPolicySeverity(c.Severity) {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: checks[%q] has invalid severity %q",
				policyID, c.CheckID, c.Severity))
	}
	// AC #4: AI-assisted (advisory) interpretation is explicitly
	// non-blocking unless converted into a deterministic policy.
	if c.Interpretation == PolicyCheckInterpretationAdvisory && c.Severity == PolicySeverityBlocking {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: checks[%q] has advisory interpretation but blocking severity — advisory checks must be warning",
				policyID, c.CheckID))
	}
	switch c.Kind {
	case PolicyCheckKindCommand:
		// Whitespace-only commands MUST be rejected here even though
		// strings.ContainsAny below would not catch a `   ` value.
		// Most shells treat a blank command as a no-op that exits 0,
		// which would let a blocking check be "satisfied" without
		// running any validation. Trim before checking so leading or
		// trailing whitespace tricks are caught too.
		if strings.TrimSpace(c.Command) == "" {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: checks[%q] has kind=command but command is empty or whitespace-only",
					policyID, c.CheckID))
		}
		if strings.ContainsAny(c.Command, "\n\r") {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: checks[%q].command must not contain newlines",
					policyID, c.CheckID))
		}
	case PolicyCheckKindExternal:
		if c.Command != "" {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("validation_policy[%q]: checks[%q] has kind=external but command is set",
					policyID, c.CheckID))
		}
	}
	if c.TimeoutSeconds < 0 {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("validation_policy[%q]: checks[%q].timeout_seconds must not be negative",
				policyID, c.CheckID))
	}
	return nil
}

// ValidateAcrossDocuments enforces uniqueness invariants that span an
// entire policy SET — currently, no two policies in any of the given
// documents may declare the same check_id, even when those policies
// live in different files. ExecutionEvidence.RequiredChecks /
// AdvisoryChecks / CheckResults are keyed solely by check_id, so a
// collision between two co-applied policies would let one verdict
// silently satisfy or fail both. Document-level Validate is necessary
// but not sufficient; workspace loaders / validation services SHOULD
// also call this helper when loading the full set of governance
// policies.
//
// Each document is also Validate()'d so this single call is enough to
// vet a workspace's full policy set.
func ValidateAcrossDocuments(docs []ValidationPolicyDocument) error {
	checkIDOrigin := make(map[string]struct {
		policyID string
		// docIndex is the index within the input slice; document path
		// is unknown to this layer.
		docIndex int
	})
	for i, d := range docs {
		if err := d.Validate(); err != nil {
			return err
		}
		for _, p := range d.Policies {
			for _, c := range p.Checks {
				if prior, dup := checkIDOrigin[c.CheckID]; dup {
					return NewError(ErrInvalidParams,
						fmt.Sprintf("validation_policy: check_id %q is declared by both policies %q (document index %d) and %q (document index %d) — check_ids must be unique across the entire policy set because evidence rows are keyed only by check_id",
							c.CheckID, prior.policyID, prior.docIndex, p.PolicyID, i))
				}
				checkIDOrigin[c.CheckID] = struct {
					policyID string
					docIndex int
				}{policyID: p.PolicyID, docIndex: i}
			}
		}
	}
	return nil
}

func isValidValidationPolicyStatus(s ValidationPolicyStatus) bool {
	for _, v := range ValidValidationPolicyStatuses() {
		if s == v {
			return true
		}
	}
	return false
}

func isValidPolicyCheckKind(k PolicyCheckKind) bool {
	for _, v := range ValidPolicyCheckKinds() {
		if k == v {
			return true
		}
	}
	return false
}

func isValidPolicyCheckInterpretation(i PolicyCheckInterpretation) bool {
	for _, v := range ValidPolicyCheckInterpretations() {
		if i == v {
			return true
		}
	}
	return false
}

func isValidPolicySeverity(s PolicySeverity) bool {
	for _, v := range ValidPolicySeverities() {
		if s == v {
			return true
		}
	}
	return false
}

// ParseValidationPolicyDocument decodes data into a fully validated
// ValidationPolicyDocument. The pipeline is:
//
//  1. yamlsafe.Decode bounds the input (size, depth, node count, alias
//     count) so a malformed or hostile file cannot exhaust memory.
//  2. The decoded root is walked to reject any unknown top-level key
//     with a specific "unknown top-level field" message. This catches
//     typos like "polices:" or "generatedAt:" that would otherwise
//     silently parse into a zero-valued document.
//  3. yaml.NewDecoder + KnownFields(true) rejects unknown keys at every
//     nesting level (`policies[*]`, `selector`, `checks[*]`). A typo
//     like `timeout_second:` (missing the "s") in a check would
//     otherwise drop silently to the zero value, and a blocking-policy
//     misconfiguration would slip through committed governance.
//  4. Validate() enforces every schema invariant from
//     §4-§7 of /architecture/validation-policy.md.
//
// Errors are returned as domain.ErrInvalidParams so callers in the
// validation service surface them with the same shape used by the
// repository catalog parser (ADR-013).
func ParseValidationPolicyDocument(data []byte) (*ValidationPolicyDocument, error) {
	root, err := yamlsafe.Decode(data)
	if err != nil {
		return nil, NewError(ErrInvalidParams,
			fmt.Sprintf("validation policy document: %v", err))
	}

	if root == nil || len(root.Content) == 0 {
		return nil, NewError(ErrInvalidParams,
			"validation policy document is empty")
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, NewError(ErrInvalidParams,
			"validation policy document must be a YAML mapping")
	}
	if len(doc.Content)%2 != 0 {
		return nil, NewError(ErrInvalidParams,
			"validation policy document is malformed")
	}
	for i := 0; i < len(doc.Content); i += 2 {
		key := doc.Content[i].Value
		if _, ok := validationPolicyDocumentTopLevelKeys[key]; !ok {
			return nil, NewError(ErrInvalidParams,
				fmt.Sprintf("validation policy document: unknown top-level field %q", key))
		}
	}

	// Strict decode catches unknown keys at every nested level
	// (policies[*], selector, checks[*]). yaml.v3's node-level Decode
	// does not honor KnownFields, so we route through a fresh
	// Decoder. The bounds pass above means this second parse cannot
	// be turned into a memory-exhaustion vector.
	var out ValidationPolicyDocument
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&out); err != nil {
		return nil, NewError(ErrInvalidParams,
			fmt.Sprintf("validation policy document: %v", err))
	}
	// yaml.NewDecoder reads one document at a time. A file with a
	// second document separated by `---` would have everything past
	// the first separator silently dropped, so a typo introducing an
	// accidental document break could hide whole policy entries from
	// validation. Require EOF after the first document.
	var trailing yaml.Node
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, NewError(ErrInvalidParams,
			"validation policy document: only one YAML document per file is allowed; remove trailing `---` and following content")
	}
	if err := out.Validate(); err != nil {
		return nil, NewError(ErrInvalidParams,
			fmt.Sprintf("validation policy document: %v", err))
	}
	return &out, nil
}
