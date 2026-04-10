package engine

import "testing"

func TestAutoPushEnabled(t *testing.T) {
	tests := []struct {
		name     string
		envKey   string
		envVal   string
		expected bool
	}{
		{"default enabled", "", "", true},
		{"SPINE_GIT_PUSH_ENABLED=true", "SPINE_GIT_PUSH_ENABLED", "true", true},
		{"SPINE_GIT_PUSH_ENABLED=false", "SPINE_GIT_PUSH_ENABLED", "false", false},
		{"SPINE_GIT_PUSH_ENABLED=FALSE", "SPINE_GIT_PUSH_ENABLED", "FALSE", false},
		{"SPINE_GIT_AUTO_PUSH=false (legacy)", "SPINE_GIT_AUTO_PUSH", "false", false},
		{"SPINE_GIT_AUTO_PUSH=true (legacy)", "SPINE_GIT_AUTO_PUSH", "true", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear both env vars.
			t.Setenv("SPINE_GIT_PUSH_ENABLED", "")
			t.Setenv("SPINE_GIT_AUTO_PUSH", "")
			if tt.envKey != "" {
				t.Setenv(tt.envKey, tt.envVal)
			}
			got := autoPushEnabled()
			if got != tt.expected {
				t.Errorf("autoPushEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestAutoPushEnabled_PrefersPushEnabled(t *testing.T) {
	// SPINE_GIT_PUSH_ENABLED takes precedence over SPINE_GIT_AUTO_PUSH.
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")
	t.Setenv("SPINE_GIT_AUTO_PUSH", "true")
	if autoPushEnabled() {
		t.Error("SPINE_GIT_PUSH_ENABLED=false should override SPINE_GIT_AUTO_PUSH=true")
	}
}
