// Package projection adapts the runtime branch_protection_rules table
// (populated by internal/projection) into a branchprotect.RuleSource.
//
// Policy callers hold a branchprotect.Policy backed by one of these
// adapters; the hot path reads from the runtime table, so evaluation
// never touches Git. See ADR-009 §1 and EPIC-002 TASK-003.
package projection

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/branchprotect/config"
	"github.com/bszymi/spine/internal/store"
)

// ListReader is the minimal Store surface the adapter needs. Keeping
// it narrow lets unit tests use a tiny fake without mocking the rest of
// the Store interface.
type ListReader interface {
	ListBranchProtectionRules(ctx context.Context) ([]store.BranchProtectionRuleProjection, error)
}

// Compile-time assurance the Store interface already includes the read
// surface we depend on. If the interface drifts, this line breaks the
// build before the adapter silently starts returning nil rules.
var _ ListReader = store.Store(nil)

// RuleSource reads rules from the runtime table. Construct one per
// workspace; the enclosing workspace.ServiceSet owns the lifetime.
type RuleSource struct {
	reader ListReader
}

// New returns a RuleSource that reads through r. A nil r panics — the
// adapter is not useful without a store, and silently degrading to
// "no rules" (which the policy would interpret as bootstrap defaults)
// would hide a misconfiguration.
func New(r ListReader) *RuleSource {
	if r == nil {
		panic("branchprotect/projection: nil ListReader")
	}
	return &RuleSource{reader: r}
}

// Rules implements branchprotect.RuleSource. It translates the JSONB
// protections column into typed RuleKinds. An error during row scanning
// or JSON unmarshal aborts evaluation; the branchprotect policy then
// fails closed (Deny + non-nil error) for the evaluation that triggered
// the load.
//
// An empty table is returned as a non-nil, zero-length slice — meaning
// "this workspace explicitly has no protection rules," the config
// author's `rules: []` choice. The adapter never returns (nil, nil):
// the bootstrap-defaults fallback lives in the projection handler
// (which writes bootstrap rows to the table when the config file is
// absent) and in the 018 migration seed. That way the projection table
// is the single authority for effective rules; the policy's
// (nil, nil) path is only a safety net for rogue adapters.
func (s *RuleSource) Rules(ctx context.Context) ([]config.Rule, error) {
	rows, err := s.reader.ListBranchProtectionRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("branchprotect/projection: list rules: %w", err)
	}
	out := make([]config.Rule, 0, len(rows))
	for _, row := range rows {
		var protections []string
		if err := json.Unmarshal(row.Protections, &protections); err != nil {
			return nil, fmt.Errorf("branchprotect/projection: decode protections for %q: %w", row.BranchPattern, err)
		}
		kinds := make([]config.RuleKind, 0, len(protections))
		for _, p := range protections {
			kinds = append(kinds, config.RuleKind(p))
		}
		out = append(out, config.Rule{Branch: row.BranchPattern, Protections: kinds})
	}
	return out, nil
}

// compile-time check: RuleSource satisfies branchprotect.RuleSource.
var _ branchprotect.RuleSource = (*RuleSource)(nil)
