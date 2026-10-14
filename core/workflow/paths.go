package workflow

import (
	"path"
	"strings"
)

// InWorkflowsDir reports whether p, read as a repo-relative path, names the
// workflow definitions directory or anything under it once it is
// canonicalised (INIT-023 TASK-014 §3.5, I9). It is the one test behind every
// "this is a workflow definition path" guard — the gateway's artifact-surface
// refusals, POST /runs and the engine's StartPlanningRun — so a spelling that
// a raw prefix test misses cannot reach workflows/ through a generic writer:
//
//   - dot segments and repeated or leading slashes are resolved
//     (path.Clean): "./workflows/x", "governance/../workflows/x",
//     "workflows//x" and "/workflows/x" all name workflows/x;
//   - the first component is compared with Unicode case folding
//     (strings.EqualFold), because on a case-insensitive filesystem (macOS
//     APFS, Windows, a Docker Desktop bind mount of either) "Workflows/x"
//     or a spelling with U+212A KELVIN SIGN writes into the real
//     workflows/ directory.
//
// It refuses more, never less: "workflows-guide.md" and
// "docs/workflows/x.md" are not workflow paths.
func InWorkflowsDir(p string) bool {
	c := strings.TrimPrefix(path.Clean("/"+p), "/")
	first, _, _ := strings.Cut(c, "/")
	return strings.EqualFold(first, WorkflowsDir)
}
