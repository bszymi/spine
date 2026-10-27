package engine

import "github.com/bszymi/spine/adapters/git"

// The standalone operator, the Spine CLI client, has the output-evidence
// counter (INIT-024 TASK-004, review finding N-12).
var _ ChangedPathCounter = (*git.CLIClient)(nil)
