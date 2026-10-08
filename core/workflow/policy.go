package workflow

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// checkBranchProtection is the ADR-009 guard on the workflow write path
// (INIT-023 TASK-014, audit F7). Before this, workflow.Service held only a
// git client and a repo path and never consulted branch protection, so the
// bootstrap `main: [no-delete, no-direct-write]` rule did not apply to
// workflow files: a reviewer's PUT /workflows/{id} committed straight to
// main. The gateway now routes reviewers to a lifecycle planning run; this
// check is the service-level backstop, so a future handler that forgets the
// dispatch is refused here instead of silently committing.
//
// Rules, in order:
//
//  1. No policy → nil (I4: the pre-TASK-014 behaviour, which SMP relies on —
//     it builds the service with no policy and writes only through
//     actor-less bypass contexts).
//  2. Target: the WriteContext branch when set, else s.defaultRef.
//  3. Actor: domain.ActorFromContext, else a zero-role actor carrying only
//     observe.ActorID (the same fallback artifact.Service uses).
//  4. Bypass (WithBypass): below operator → ErrForbidden, an actor-less
//     bypass included (I2). On the authoritative branch an operator bypass
//     skips rule evaluation — it is the ADR-008 §5 sanctioned direct
//     commit, audited by its Workflow-Bypass trailer, not an ADR-009
//     override (Decision 4: one audit mechanism per write). A bypass that
//     also carries a WriteContext branch falls through to rule 5; the
//     gateway never builds one.
//  5. policy.Evaluate as OpDirectWrite. An evaluation error is ErrInternal
//     (500, logged; fail closed); a deny is ErrForbidden (403) with the
//     artifact service's detail shape {branch, reasons[]}, so a client
//     parses one deny format. Override is never requested: workflow
//     endpoints refuse write_context.override (no override event or
//     trailer is emitted on this surface).
//
// It runs before enterBranch, so a refused write creates no worktree and
// touches no file.
func (s *Service) checkBranchProtection(ctx context.Context, operation string) error {
	if s.policy == nil {
		return nil
	}

	branch := s.defaultRef
	if wc := GetWriteContext(ctx); wc != nil && wc.Branch != "" {
		branch = wc.Branch
	}
	actor := actorForRequest(ctx)

	if IsBypass(ctx) {
		if !actor.Role.HasAtLeast(domain.RoleOperator) {
			observe.Logger(ctx).Warn("workflow bypass refused: operator role required",
				"operation", operation,
				"branch", branch,
				"role", string(actor.Role),
			)
			return domain.NewError(domain.ErrForbidden,
				"workflow bypass requires operator role (ADR-008 §5)")
		}
		if branch == s.defaultRef {
			return nil
		}
	}

	req := branchprotect.Request{
		Branch:  branch,
		Kind:    branchprotect.OpDirectWrite,
		Actor:   actor,
		RunID:   observe.RunID(ctx),
		TraceID: observe.TraceID(ctx),
	}
	decision, reasons, evalErr := s.policy.Evaluate(ctx, req)
	if evalErr != nil {
		observe.Logger(ctx).Error("branch-protection evaluation failed",
			"operation", operation,
			"branch", branch,
			"error", evalErr.Error(),
		)
		return domain.NewError(domain.ErrInternal,
			fmt.Sprintf("branch-protection evaluation failed: %v", evalErr))
	}
	if decision == branchprotect.DecisionDeny {
		observe.Logger(ctx).Info("branch-protection denied workflow write",
			"operation", operation,
			"branch", branch,
			"reasons", reasonCodes(reasons),
		)
		return domain.NewErrorWithDetail(domain.ErrForbidden,
			firstDenyMessage(reasons, branch),
			map[string]any{
				"branch":  branch,
				"reasons": reasonsToDetail(reasons),
			})
	}
	return nil
}

// actorForRequest resolves the authenticated actor for a
// branchprotect.Request: domain.ActorFromContext, else a zero-role actor
// identified only by observe.ActorID (artifact.Service's fallback). A
// zero-role actor never passes the bypass check.
func actorForRequest(ctx context.Context) domain.Actor {
	if a := domain.ActorFromContext(ctx); a != nil {
		return *a
	}
	return domain.Actor{ActorID: observe.ActorID(ctx)}
}

// The three helpers below render a deny exactly as core/artifact does
// (its helpers are unexported), so both services answer the same shape.

func reasonCodes(reasons []branchprotect.Reason) []string {
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = string(r.Code)
	}
	return out
}

func reasonsToDetail(reasons []branchprotect.Reason) []map[string]string {
	out := make([]map[string]string, len(reasons))
	for i, r := range reasons {
		entry := map[string]string{
			"code":    string(r.Code),
			"message": r.Message,
		}
		if r.RuleKind != "" {
			entry["rule_kind"] = string(r.RuleKind)
		}
		out[i] = entry
	}
	return out
}

func firstDenyMessage(reasons []branchprotect.Reason, branch string) string {
	if len(reasons) == 0 {
		return fmt.Sprintf("branch %q is protected", branch)
	}
	return reasons[0].Message
}
