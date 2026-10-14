package projection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
)

// Refused files (INIT-023 TASK-003, audit F4 and P3-9). Discovery
// declines to project a file that is over the git client's per-blob read
// limit, or that sits at a path Spine refuses to store
// (artifact.UnsafePath). Each refusal is reported — never silently
// dropped — and never counted as a sync error: the file cannot become
// projectable at that commit, so holding the sync on it would only let
// one push freeze the workspace's projection. Before TASK-003 such a file
// either took the process down (an unbounded read) or vanished without a
// trace (a C-quoted path that no longer ended in ".md").
//
// An oversized WORKFLOW file is not reported here: it reaches the
// workflow row builder, which reports it as a read_bounds rejection
// (projection_workflow_rejected) through isContentReadError. An
// unsafe-named workflow file is reported here, because it must never
// reach the store (its path is the row key).

// refusedDisplayPath is the form of a refused path that goes into logs
// and events: the path itself when it is safe, strconv.Quote of it
// otherwise — one line, valid UTF-8, every control and bidi character
// escaped, and still exact enough to find the file.
func refusedDisplayPath(p string) string {
	if artifact.UnsafePath(p) {
		return strconv.Quote(p)
	}
	return p
}

// keepRefusal reports whether a refusal DiscoverAll made is one this
// projection would otherwise have projected: every .md candidate, but
// only those workflow candidates the scope treats as definitions
// (direct children of a workflows/ directory). Nested workflows/** files
// are never projected, refused or not, so they are not reported.
func (s *Service) keepRefusal(r artifact.RefusedFile) bool {
	return strings.HasSuffix(r.Path, ".md") || s.scope.isWorkflow(r.Path)
}

// recordRefused reports every refusal at head: one Warn line, one
// spine_projection_files_skipped_total increment and — with an event
// router — one projection_file_skipped event with a deterministic id
// (projskip-<head[:12]>-<sha256(reason NUL path)[:12]>), so a rebuild or
// a retried sync at the same commit re-emits the same id and the delivery
// queue de-duplicates it.
func (s *Service) recordRefused(ctx context.Context, head string, refused []artifact.RefusedFile) {
	log := observe.Logger(ctx)
	for _, r := range refused {
		display := refusedDisplayPath(r.Path)
		var size, limit any // null for unsafe_path
		var gitAlloc any    // null unless git refused an allocation
		if r.Reason == artifact.RefusedOversized {
			size, limit = r.Size, r.Limit
			if r.GitAllocLimit > 0 {
				// The file may be under limit_bytes: git could not
				// allocate something larger it needed to produce it (a
				// delta base, a gitlink's patch). Saying which limit
				// refused it keeps a 3 MB file from reading as "over
				// 4 MiB" (review round 1).
				gitAlloc = r.GitAllocLimit
			}
		}
		log.Warn("projection skipped file",
			"path", display,
			"reason", string(r.Reason),
			"size_bytes", size,
			"limit_bytes", limit,
			"git_alloc_limit_bytes", gitAlloc,
			"commit", shortSHA(head),
		)
		observe.GlobalMetrics.ProjectionFilesSkipped.Inc()
		if s.events == nil {
			continue
		}
		payload, err := json.Marshal(map[string]any{
			"path":                  display,
			"reason":                string(r.Reason),
			"size_bytes":            size,
			"limit_bytes":           limit,
			"git_alloc_limit_bytes": gitAlloc,
			"commit":                head,
		})
		if err != nil {
			log.Warn("projection: marshal skipped-file event", "error", err)
			continue
		}
		sum := sha256.Sum256([]byte(string(r.Reason) + "\x00" + r.Path))
		head12 := head
		if len(head12) > 12 {
			head12 = head12[:12]
		}
		event.EmitLogged(ctx, s.events, domain.Event{
			EventID:      "projskip-" + head12 + "-" + hex.EncodeToString(sum[:])[:12],
			Type:         domain.EventProjectionFileSkipped,
			ArtifactPath: display,
			Payload:      payload,
		})
	}
}
