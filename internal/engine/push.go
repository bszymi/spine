package engine

import (
	"os"
	"strings"
)

// autoPushEnabled returns true unless SPINE_GIT_AUTO_PUSH is set to "false".
func autoPushEnabled() bool {
	return !strings.EqualFold(os.Getenv("SPINE_GIT_AUTO_PUSH"), "false")
}
