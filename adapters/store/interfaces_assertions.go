package store

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
