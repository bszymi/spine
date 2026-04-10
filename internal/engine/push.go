package engine

import (
	"os"
	"strings"
)

// autoPushEnabled returns true unless push is explicitly disabled.
// Checks SPINE_GIT_PUSH_ENABLED (preferred) and SPINE_GIT_AUTO_PUSH (legacy).
func autoPushEnabled() bool {
	if v := os.Getenv("SPINE_GIT_PUSH_ENABLED"); v != "" {
		return !strings.EqualFold(v, "false")
	}
	return !strings.EqualFold(os.Getenv("SPINE_GIT_AUTO_PUSH"), "false")
}
