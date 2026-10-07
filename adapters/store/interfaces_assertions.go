package store

import "github.com/bszymi/spine/core/refadmit"

// Compile-time assertions that *PostgresStore satisfies every
// role-specific interface in interfaces.go. Adding a method to a role
// without adding the matching method on PostgresStore produces a
// build error here rather than at every consumer call site.
//
// These supplement the union assertion `var _ Store = ...` and are
// kept in their own file so the role partition is greppable in one
// place.
var (
	_ SystemStore             = (*PostgresStore)(nil)
	_ RunStore                = (*PostgresStore)(nil)
	_ BranchStore             = (*PostgresStore)(nil)
	_ ArtifactStore           = (*PostgresStore)(nil)
	_ WorkflowProjectionStore = (*PostgresStore)(nil)
	_ SyncStateStore          = (*PostgresStore)(nil)
	_ AuthStore               = (*PostgresStore)(nil)
	_ AssignmentStore         = (*PostgresStore)(nil)
	_ SkillStore              = (*PostgresStore)(nil)
	_ RepositoryStore         = (*PostgresStore)(nil)
	_ BranchProtectionStore   = (*PostgresStore)(nil)
	_ DiscussionStore         = (*PostgresStore)(nil)
	_ DeliveryStore           = (*PostgresStore)(nil)
	_ SubscriptionStore       = (*PostgresStore)(nil)
	_ Store                   = (*PostgresStore)(nil)
)

// The git gateways' ref-class push admission reads runs, executions
// and divergence branches through refadmit.RunReader (INIT-023
// TASK-016). Both the concrete store and the union satisfy it with no
// store-interface change, which is what lets `spine serve` hand either
// to the admission as is.
var (
	_ refadmit.RunReader = (*PostgresStore)(nil)
	_ refadmit.RunReader = Store(nil)
)
