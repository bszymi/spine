package domain

import "testing"

func TestStepOutcomeConstants(t *testing.T) {
	// Verify step outcomes are distinct from task acceptance outcomes.
	stepOutcomes := []StepOutcome{
		StepOutcomeAccepted,
		StepOutcomeNeedsRework,
		StepOutcomeFailed,
	}

	taskOutcomes := []TaskAcceptance{
		AcceptanceApproved,
		AcceptanceRejectedWithFollowup,
		AcceptanceRejectedClosed,
	}

	// No overlap between step and task vocabularies.
	stepSet := make(map[string]bool)
	for _, so := range stepOutcomes {
		stepSet[string(so)] = true
	}
	for _, ta := range taskOutcomes {
		if stepSet[string(ta)] {
			t.Errorf("overlap between step outcome and task acceptance: %s", ta)
		}
	}
}

func TestMaxReworkCycles(t *testing.T) {
	if MaxReworkCycles < 1 {
		t.Error("MaxReworkCycles must be at least 1")
	}
	if MaxReworkCycles > 100 {
		t.Error("MaxReworkCycles seems too high")
	}
}
