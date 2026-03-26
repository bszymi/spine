package observe

import "context"

// AuditLog emits a structured audit entry for governance-significant operations.
// Per Observability §7: acceptance, rejection, convergence decisions, and
// status transitions must produce auditable records.
func AuditLog(ctx context.Context, operation string, fields ...any) {
	log := Logger(ctx)
	args := append([]any{"audit_operation", operation}, fields...)
	log.Info("[AUDIT]", args...)
}
