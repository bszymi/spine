package artifact

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bszymi/spine/internal/domain"
)

// Valid link types per artifact-schema.md §4.1.
var validLinkTypes = map[domain.LinkType]bool{
	"parent":         true,
	"contains":       true,
	"blocks":         true,
	"blocked_by":     true,
	"supersedes":     true,
	"superseded_by":  true,
	"follow_up_to":   true,
	"follow_up_from": true,
	"related_to":     true,
}

// ID format patterns per naming-conventions.md §2.
var idPatterns = map[domain.ArtifactType]*regexp.Regexp{
	domain.ArtifactTypeInitiative: regexp.MustCompile(`^INIT-\d{3}$`),
	domain.ArtifactTypeEpic:       regexp.MustCompile(`^EPIC-\d{3}$`),
	domain.ArtifactTypeTask:       regexp.MustCompile(`^TASK-\d{3}$`),
	domain.ArtifactTypeADR:        regexp.MustCompile(`^ADR-\d{3,4}$`),
}

// Validate checks an artifact against the schema rules defined in
// artifact-schema.md §2-6. Returns a ValidationResult with errors and warnings.
func Validate(a *domain.Artifact) domain.ValidationResult {
	var errors []domain.ValidationError
	var warnings []domain.ValidationError

	// Required fields (all types)
	if a.Type == "" {
		errors = append(errors, fieldError(a.Path, "type", "required field missing"))
	}
	if a.Title == "" {
		errors = append(errors, fieldError(a.Path, "title", "required field missing"))
	}
	if a.Status == "" {
		errors = append(errors, fieldError(a.Path, "status", "required field missing"))
	}

	// Validate artifact type is known
	if !isValidArtifactType(a.Type) {
		errors = append(errors, fieldError(a.Path, "type", fmt.Sprintf("unknown artifact type: %s", a.Type)))
		return result(errors, warnings)
	}

	// Status validation per type (artifact-schema.md §6)
	if a.Status != "" {
		validStatuses := domain.ValidStatusesForType(a.Type)
		if !containsStatus(validStatuses, a.Status) {
			errors = append(errors, fieldError(a.Path, "status",
				fmt.Sprintf("invalid status %q for %s", a.Status, a.Type)))
		}
	}

	// Type-specific required fields (artifact-schema.md §5)
	errors = append(errors, validateTypeSpecificFields(a)...)

	// ID format validation (naming-conventions.md §2)
	if a.ID != "" {
		if pattern, ok := idPatterns[a.Type]; ok {
			if !pattern.MatchString(a.ID) {
				errors = append(errors, fieldError(a.Path, "id",
					fmt.Sprintf("invalid ID format %q for %s (expected pattern: %s)", a.ID, a.Type, pattern.String())))
			}
		}
	}

	// Link validation (artifact-schema.md §3-4)
	for i, link := range a.Links {
		field := fmt.Sprintf("links[%d]", i)

		// Link type validation (§4.1)
		if !validLinkTypes[link.Type] {
			errors = append(errors, fieldError(a.Path, field+".type",
				fmt.Sprintf("unknown link type: %s", link.Type)))
		}

		// Target must be non-empty
		if link.Target == "" {
			errors = append(errors, fieldError(a.Path, field+".target", "link target is required"))
		}

		// Target must start with / (canonical path format per §3.2)
		if link.Target != "" && !strings.HasPrefix(link.Target, "/") {
			errors = append(errors, fieldError(a.Path, field+".target",
				fmt.Sprintf("link target must be a canonical path starting with /: %s", link.Target)))
		}
	}

	return result(errors, warnings)
}

// ValidateField validates a single field of an artifact.
// Returns nil if valid, or a ValidationError if invalid.
func ValidateField(a *domain.Artifact, field string) *domain.ValidationError {
	switch field {
	case "type":
		if a.Type == "" {
			e := fieldError(a.Path, "type", "required field missing")
			return &e
		}
		if !isValidArtifactType(a.Type) {
			e := fieldError(a.Path, "type", fmt.Sprintf("unknown artifact type: %s", a.Type))
			return &e
		}
	case "status":
		if a.Status == "" {
			e := fieldError(a.Path, "status", "required field missing")
			return &e
		}
		validStatuses := domain.ValidStatusesForType(a.Type)
		if !containsStatus(validStatuses, a.Status) {
			e := fieldError(a.Path, "status", fmt.Sprintf("invalid status %q for %s", a.Status, a.Type))
			return &e
		}
	case "id":
		// Check if ID is required for this type
		switch a.Type {
		case domain.ArtifactTypeInitiative, domain.ArtifactTypeEpic, domain.ArtifactTypeTask, domain.ArtifactTypeADR:
			if a.ID == "" {
				e := fieldError(a.Path, "id", fmt.Sprintf("required for %s", a.Type))
				return &e
			}
		}
		if pattern, ok := idPatterns[a.Type]; ok && a.ID != "" {
			if !pattern.MatchString(a.ID) {
				e := fieldError(a.Path, "id", fmt.Sprintf("invalid ID format %q", a.ID))
				return &e
			}
		}
	case "title":
		if a.Title == "" {
			e := fieldError(a.Path, "title", "required field missing")
			return &e
		}
	}
	return nil
}

func validateTypeSpecificFields(a *domain.Artifact) []domain.ValidationError {
	var errors []domain.ValidationError

	switch a.Type {
	case domain.ArtifactTypeInitiative:
		if a.ID == "" {
			errors = append(errors, fieldError(a.Path, "id", "required for Initiative"))
		}
		if a.Metadata["created"] == "" {
			errors = append(errors, fieldError(a.Path, "created", "required for Initiative"))
		} else if !isValidDate(a.Metadata["created"]) {
			errors = append(errors, fieldError(a.Path, "created", fmt.Sprintf("invalid date format %q (expected YYYY-MM-DD)", a.Metadata["created"])))
		}
	case domain.ArtifactTypeEpic:
		if a.ID == "" {
			errors = append(errors, fieldError(a.Path, "id", "required for Epic"))
		}
		if a.Metadata["initiative"] == "" {
			errors = append(errors, fieldError(a.Path, "initiative", "required for Epic"))
		} else if !strings.HasPrefix(a.Metadata["initiative"], "/") {
			errors = append(errors, fieldError(a.Path, "initiative", "must be a canonical path starting with /"))
		}
	case domain.ArtifactTypeTask:
		if a.ID == "" {
			errors = append(errors, fieldError(a.Path, "id", "required for Task"))
		}
		if a.Metadata["epic"] == "" {
			errors = append(errors, fieldError(a.Path, "epic", "required for Task"))
		} else if !strings.HasPrefix(a.Metadata["epic"], "/") {
			errors = append(errors, fieldError(a.Path, "epic", "must be a canonical path starting with /"))
		}
		if a.Metadata["initiative"] == "" {
			errors = append(errors, fieldError(a.Path, "initiative", "required for Task"))
		} else if !strings.HasPrefix(a.Metadata["initiative"], "/") {
			errors = append(errors, fieldError(a.Path, "initiative", "must be a canonical path starting with /"))
		}
	case domain.ArtifactTypeADR:
		if a.ID == "" {
			errors = append(errors, fieldError(a.Path, "id", "required for ADR"))
		}
		if a.Metadata["date"] == "" {
			errors = append(errors, fieldError(a.Path, "date", "required for ADR"))
		} else if !isValidDate(a.Metadata["date"]) {
			errors = append(errors, fieldError(a.Path, "date", fmt.Sprintf("invalid date format %q (expected YYYY-MM-DD)", a.Metadata["date"])))
		}
		if a.Metadata["decision_makers"] == "" {
			errors = append(errors, fieldError(a.Path, "decision_makers", "required for ADR"))
		}
	}

	return errors
}

func fieldError(path, field, message string) domain.ValidationError {
	return domain.ValidationError{
		ArtifactPath: path,
		Field:        field,
		Severity:     "error",
		Message:      message,
	}
}

func result(errors, warnings []domain.ValidationError) domain.ValidationResult {
	status := "passed"
	if len(errors) > 0 {
		status = "failed"
	} else if len(warnings) > 0 {
		status = "warnings"
	}
	return domain.ValidationResult{
		Status:   status,
		Errors:   errors,
		Warnings: warnings,
	}
}

// datePattern validates ISO-8601 date format (YYYY-MM-DD).
var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func isValidDate(s string) bool {
	return datePattern.MatchString(s)
}

func isValidArtifactType(t domain.ArtifactType) bool {
	for _, valid := range domain.ValidArtifactTypes() {
		if t == valid {
			return true
		}
	}
	return false
}

func containsStatus(statuses []domain.ArtifactStatus, s domain.ArtifactStatus) bool {
	for _, valid := range statuses {
		if s == valid {
			return true
		}
	}
	return false
}
