package delivery

import (
	"testing"

	"github.com/bszymi/spine/core/domain"
)

// INIT-023 TASK-011 AC 22: the projection's rejection event is
// subscribable — allEventTypes() is the list the delivery subscriber
// registers for, so an event missing from it is never delivered.
func TestAllEventTypes_IncludesProjectionWorkflowRejected(t *testing.T) {
	seen := map[domain.EventType]int{}
	for _, et := range allEventTypes() {
		seen[et]++
	}
	if seen[domain.EventProjectionWorkflowRejected] != 1 {
		t.Fatalf("allEventTypes() must list %q exactly once", domain.EventProjectionWorkflowRejected)
	}
	if domain.EventProjectionWorkflowRejected != "projection_workflow_rejected" {
		t.Fatalf("wire name drifted: %q", domain.EventProjectionWorkflowRejected)
	}
	for et, n := range seen {
		if n != 1 {
			t.Errorf("event type %q listed %d times", et, n)
		}
	}
}

// INIT-023 TASK-003 AC 12: the projection's skipped-file event is
// subscribable, operational, and keeps its wire name.
func TestAllEventTypes_IncludesProjectionFileSkipped(t *testing.T) {
	n := 0
	for _, et := range allEventTypes() {
		if et == domain.EventProjectionFileSkipped {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("allEventTypes() lists %q %d times, want once", domain.EventProjectionFileSkipped, n)
	}
	if domain.EventProjectionFileSkipped != "projection_file_skipped" {
		t.Fatalf("wire name drifted: %q", domain.EventProjectionFileSkipped)
	}
	if isDomainEvent(string(domain.EventProjectionFileSkipped)) {
		t.Fatal("projection_file_skipped is operational, not a domain event")
	}
}
