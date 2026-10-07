package engine

import "github.com/bszymi/spine/adapters/store"

// The step-entry policy's optional ports are type-asserted at run time, so
// pin at compile time that Spine's own store provides every one of them
// (standalone `spine serve` and SMP both pass *store.PostgresStore as the
// assignment and blocking store).
var (
	_ assignmentHistory         = (*store.PostgresStore)(nil)
	_ activeAssignmentCanceller = (*store.PostgresStore)(nil)
	_ entryActorLoader          = (*store.PostgresStore)(nil)
	_ entrySkillLister          = (*store.PostgresStore)(nil)
)
