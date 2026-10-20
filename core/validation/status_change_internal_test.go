package validation

import "testing"

// Every default blocking rule is a registered rule (INIT-023 TASK-017 AC 4):
// an id that matches no rule would silently promote nothing.
func TestDefaultBlockingStatusConflicts_Registered(t *testing.T) {
	ids := map[string]bool{}
	for _, r := range NewEngine(nil).rules {
		ids[r.ID()] = true
	}
	for _, id := range DefaultBlockingStatusConflicts {
		if !ids[id] {
			t.Errorf("default blocking rule %s is not registered", id)
		}
	}
}
