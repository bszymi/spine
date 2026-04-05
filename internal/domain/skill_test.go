package domain_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

func TestValidSkillStatuses(t *testing.T) {
	statuses := domain.ValidSkillStatuses()
	if len(statuses) != 2 {
		t.Errorf("expected 2 skill statuses, got %d", len(statuses))
	}

	expected := map[domain.SkillStatus]bool{
		domain.SkillStatusActive:     false,
		domain.SkillStatusDeprecated: false,
	}
	for _, s := range statuses {
		if _, ok := expected[s]; !ok {
			t.Errorf("unexpected skill status: %s", s)
		}
		expected[s] = true
	}
	for s, found := range expected {
		if !found {
			t.Errorf("missing skill status: %s", s)
		}
	}
}
