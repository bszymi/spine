package projection

import (
	"context"
	"errors"
	"fmt"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/observe"
)

// Tree-entry descriptions for a read failure the projection has proved
// deterministic (logged, and named in a not_a_file rejection).
const (
	entryGitlink        = "gitlink"         // mode 160000: no content in this repository
	entryTree           = "tree"            // a directory
	entryNone           = "none"            // the commit's tree has no entry at that exact path
	entryContentRefused = "content_refused" // isContentReadError: the blob exists but its content is refused
)

// classifyReadFailure decides whether a failed ReadFile of path at ref is
// a property of the COMMIT — so retrying can never succeed and holding
// the sync on it would wedge it until someone changes the file — or of
// git or the host, which must hold the sync (status error,
// last_synced_commit unchanged) until a retry succeeds.
//
// The deterministic class is proved POSITIVELY, never inferred from what
// is left over (INIT-023 TASK-011 review round 1):
//
//   - git's stderr is not consulted. `git show <sha>:<path>` echoes the
//     committed path, which the author chooses, and the git client's Kind
//     comes from stderr substrings ("lock", "timeout", "Could not
//     resolve", …). A gitlink named docs/blocked.md was classified
//     transient — a plantable wedge — and a git killed by a signal, or
//     one that could not be spawned, has empty stderr and was classified
//     permanent — a silent skip that advanced the sync past a change it
//     never applied.
//   - Instead the commit's tree is asked what is at that path
//     (git.TreeEntryTyper, which reads tree objects only). No regular
//     file there — a gitlink, a directory, or no entry at that exact
//     path (a quoted or normalised path the diff produced) — is
//     deterministic: there is no content to read at that commit, ever.
//     A blob there means the content exists and git failed to read it
//     (a lock, a killed or unspawnable process, a missing object):
//     infrastructure. Pushes cannot plant a missing blob — receive-pack's
//     connectivity check refuses it; gitlinks are the one unreadable
//     entry a push can carry.
//   - isContentReadError (a blob whose content is refused by a size bound,
//     registered by the bounded-read work) is deterministic too.
//   - Anything that cannot be proved — a cancelled context, a git client
//     without the TreeEntryTyper capability, a failure to read the tree —
//     is infrastructure. Holding the sync on an unproved failure is
//     recoverable; skipping it silently is not.
//
// It returns the tree-entry description and a nil error for a
// deterministic failure, or a non-nil infrastructure error.
func (s *Service) classifyReadFailure(ctx context.Context, ref, path string, readErr error) (string, error) {
	if readErr == nil {
		readErr = errors.New("read failed with no error detail")
	}
	if ctx.Err() != nil {
		return "", readErr
	}
	if isContentReadError(readErr) {
		return entryContentRefused, nil
	}
	typer, ok := s.git.(git.TreeEntryTyper)
	if !ok {
		return "", readErr
	}
	typ, err := typer.TreeEntryType(ctx, ref, path)
	if err != nil {
		return "", errors.Join(readErr, fmt.Errorf("type the tree entry: %w", err))
	}
	switch typ {
	case git.TreeEntryCommit:
		return entryGitlink, nil
	case git.TreeEntryTree:
		return entryTree, nil
	case "":
		return entryNone, nil
	default:
		// A blob: the content exists, so the read failure is git's or
		// the host's.
		return "", readErr
	}
}

// classifyReadErrors sorts the artifact read failures discovery skipped
// past (INIT-023 TASK-011 §3, review round 1). A failure classifyReadFailure
// proves deterministic keeps the historical skip, now logged at Warn and
// counted (distinct paths) as read_skipped. The first failure it cannot
// prove deterministic is returned, and the caller holds the sync on it.
func (s *Service) classifyReadErrors(ctx context.Context, readErrors []artifact.DiscoveryError) (int, error) {
	skipped := make(map[string]struct{})
	for i := range readErrors {
		re := &readErrors[i]
		readErr := re.Err
		if readErr == nil && re.Message != "" {
			readErr = errors.New(re.Message)
		}
		entry, err := s.classifyReadFailure(ctx, re.Ref, re.Path, readErr)
		if err != nil {
			return 0, fmt.Errorf("read %s at %s: %w", re.Path, shortSHA(re.Ref), err)
		}
		observe.Logger(ctx).Warn("projection: skipped unreadable changed file",
			"path", re.Path,
			"ref", shortSHA(re.Ref),
			"entry", entry,
			"error", sanitizeForLog(re.Message),
		)
		skipped[re.Path] = struct{}{}
	}
	return len(skipped), nil
}
