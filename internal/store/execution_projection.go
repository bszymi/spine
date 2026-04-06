package store

import (
	"encoding/json"
	"time"
)

// ExecutionProjection represents a combined view of artifact and execution state
// for a task, optimized for operational queries (task discovery, dashboard views).
type ExecutionProjection struct {
	TaskPath          string    `json:"task_path"`
	TaskID            string    `json:"task_id"`
	Title             string    `json:"title"`
	Status            string    `json:"status"`
	RequiredSkills    []string  `json:"required_skills,omitempty"`
	AllowedActorTypes []string  `json:"allowed_actor_types,omitempty"`
	Blocked           bool      `json:"blocked"`
	BlockedBy         []string  `json:"blocked_by,omitempty"`
	AssignedActorID   string    `json:"assigned_actor_id,omitempty"`
	AssignmentStatus  string    `json:"assignment_status"` // unassigned, assigned, in_progress
	RunID             string    `json:"run_id,omitempty"`
	WorkflowStep      string    `json:"workflow_step,omitempty"`
	LastUpdated       time.Time `json:"last_updated"`
}

// ExecutionProjectionQuery defines parameters for querying execution projections.
type ExecutionProjectionQuery struct {
	Blocked          *bool  // nil = all, true = blocked only, false = not blocked only
	AssignmentStatus string // empty = all, "unassigned", "assigned", "in_progress"
	AssignedActorID  string // empty = all
	Limit            int
}

// MarshalSkills encodes a string slice as JSON for database storage.
func MarshalSkills(skills []string) []byte {
	if len(skills) == 0 {
		return []byte("[]")
	}
	b, _ := json.Marshal(skills)
	return b
}

// UnmarshalSkills decodes a JSON string slice from database storage.
func UnmarshalSkills(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var result []string
	_ = json.Unmarshal(data, &result)
	return result
}
