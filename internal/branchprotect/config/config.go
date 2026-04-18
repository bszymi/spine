// Package config parses /.spine/branch-protection.yaml into typed rules.
//
// The file format is specified in /architecture/branch-protection-config-format.md;
// the policy decisions that consume these rules are in ADR-009.
//
// This package is intentionally narrow: it knows about file shape and pattern
// matching, nothing else. Policy evaluation (when a rule denies) lives in the
// parent internal/branchprotect package (EPIC-002 TASK-002); projection into
// a runtime table lives in the projection layer (EPIC-002 TASK-003).
package config

// SupportedVersion is the only schema version this parser accepts.
const SupportedVersion = 1

// RuleKind is a protection the config can impose on a branch.
type RuleKind string

const (
	// KindNoDelete blocks any operation that removes a ref.
	KindNoDelete RuleKind = "no-delete"
	// KindNoDirectWrite blocks any advance of a ref that is not a
	// Spine-governed merge (see ADR-009 §2).
	KindNoDirectWrite RuleKind = "no-direct-write"
)

// IsKnown reports whether k is a RuleKind the v1 parser accepts. v1 closes
// the set of rule kinds; unknown values in a config file are rejected at
// parse time.
func (k RuleKind) IsKnown() bool {
	switch k {
	case KindNoDelete, KindNoDirectWrite:
		return true
	}
	return false
}

// Rule is one branch-protection entry. Branch may be a literal name or a
// glob pattern (see path.Match); Protections is a non-empty list of kinds
// to apply when the pattern matches.
type Rule struct {
	Branch      string     `json:"branch" yaml:"branch"`
	Protections []RuleKind `json:"protections" yaml:"protections"`
}

// Config is the parsed /.spine/branch-protection.yaml file.
type Config struct {
	Version int    `json:"version" yaml:"version"`
	Rules   []Rule `json:"rules" yaml:"rules"`
}
