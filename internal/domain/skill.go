package domain

import "time"

// SkillStatus represents the lifecycle status of a skill.
type SkillStatus string

const (
	SkillStatusActive     SkillStatus = "active"
	SkillStatusDeprecated SkillStatus = "deprecated"
)

// ValidSkillStatuses returns all valid skill statuses.
func ValidSkillStatuses() []SkillStatus {
	return []SkillStatus{SkillStatusActive, SkillStatusDeprecated}
}

// Skill represents a workspace-scoped capability that can be assigned to actors
// and required by workflow steps. Skills formalize the capability matching system:
// instead of opaque capability strings, skills are first-class entities with
// metadata, lifecycle, and category.
type Skill struct {
	SkillID     string      `json:"skill_id" yaml:"skill_id"`
	Name        string      `json:"name" yaml:"name"`
	Description string      `json:"description" yaml:"description"`
	Category    string      `json:"category" yaml:"category"`
	Status      SkillStatus `json:"status" yaml:"status"`
	CreatedAt   time.Time   `json:"created_at" yaml:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at" yaml:"updated_at"`
}
