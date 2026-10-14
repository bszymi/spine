package projection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
)

// allWorkflowRowsLister is an optional store capability: list every
// projected workflow row regardless of status. *store.PostgresStore
// implements it. It is asserted, not added to store.Store, so embedders
// and test stubs that implement the full interface are unaffected
// (INIT-023: no interface breaks for SMP).
type allWorkflowRowsLister interface {
	ListWorkflowProjections(ctx context.Context) ([]store.WorkflowProjection, error)
}

// syncBaselineSetter is an optional store capability: move the sync
// baseline (last_synced_commit) and leave status and error_detail alone.
// *store.PostgresStore implements it; asserted like allWorkflowRowsLister
// so that no store.Store implementation has to change. See
// resetSyncBaseline.
type syncBaselineSetter interface {
	SetSyncBaseline(ctx context.Context, commit string) error
}

// resetSyncBaseline re-records head as the commit the next sync diffs
// from, after a sweep wrote rows judged at head (IncrementalSync's
// up-to-date path). This sync read idle@head before resolving head and
// sweeping; another instance may have written sync_state since. Through
// syncBaselineSetter only last_synced_commit moves, so a status written
// meanwhile is kept (INIT-023 codex 6.1 catch-up): "rebuilding" (a reader
// such as SMP's idle gate must not see idle over a half-written
// projection), "error" (the next tick must still retry, forcing the
// branch-protection re-projection) and "syncing"; and a state with no
// baseline (a partial wipe) is left to rebuild. A store without the
// capability gets the plain idle@head write the path had before.
func (s *Service) resetSyncBaseline(ctx context.Context, head string) error {
	if setter, ok := s.store.(syncBaselineSetter); ok {
		return setter.SetSyncBaseline(ctx, head)
	}
	return s.store.UpdateSyncState(ctx, &store.SyncState{
		LastSyncedCommit: head,
		Status:           "idle",
	})
}

// *store.PostgresStore has both optional capabilities: a rename on either
// side fails the build here instead of silently taking the fallback.
var (
	_ allWorkflowRowsLister = (*store.PostgresStore)(nil)
	_ syncBaselineSetter    = (*store.PostgresStore)(nil)
)

// ensureWorkflowsVerified runs the workflow-row sweep once per Service
// instance, single-flight. Every IncrementalSync that finds the flag
// unset waits for the mutex, so the event-handler and sync-loop syncs
// cannot interleave sweep writes with their own (a slower sweep at an
// older head could otherwise re-upsert a stale row after a concurrent
// sync projected the newer one). A failed sweep leaves the flag unset
// and the next sync retries it. from is the store's last_synced_commit
// (see reconcileWorkflowRows). changed reports whether this call's sweep
// wrote any row (deleted, replaced or quarantined one), whether or not it
// then failed.
func (s *Service) ensureWorkflowsVerified(ctx context.Context, head, from string) (changed bool, err error) {
	if s.workflowsVerified.Load() {
		return false, nil
	}
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	if s.workflowsVerified.Load() {
		return false, nil
	}
	changed, err = s.reconcileWorkflowRows(ctx, head, from)
	if err != nil {
		return changed, err
	}
	s.workflowsVerified.Store(true)
	return changed, nil
}

// reconcileWorkflowRows re-checks every stored workflow row against
// head (INIT-023 TASK-011 §6). Without it, the fix would leave in place
// every row projected before it — including a smuggled review-less
// workflow — until that file next changed or a FullRebuild re-stamped
// every source_commit (which strands SMP's pinned runs). It also repairs
// rows a dropped Diff error (P3-14) left behind.
//
// It visits ALL rows, not only Active ones: in-flight runs load their
// pinned workflow by path or workflow_id with no status filter, so a
// smuggled row later set Deprecated would otherwise keep steering them.
// Stores without the all-rows capability fall back to Active rows.
//
// For each row:
//   - not a workflow location (nested, out of scope): deleted. An Active
//     one is reported (scope / removed); a non-Active one — SMP's
//     empty-status workflows/steps/* step-profile rows, which nothing
//     reads — is logged at Info only;
//   - file absent at head: deleted. When the file still existed at from
//     (the last synced commit, head being ahead of it), the deletion is
//     an ordinary change inside the window this sync's Diff is about to
//     apply — the row is deleted exactly as the diff would, with an Info
//     line, not reported as a rejection. Otherwise it is a missed
//     deletion and is reported (absent_at_head / removed);
//   - file present and accepted: untouched when equal to the stored row
//     (no re-stamp of source_commit, so pinned runs stay valid),
//     otherwise replaced with head's row;
//   - file present and rejected (including a not_a_file entry such as a
//     gitlink): the row is kept quarantined when it verifies, else
//     deleted (removed).
//
// Any git or store error is returned; the caller counts it as a sync
// error and retries the sweep on the next tick. changed reports whether
// any row was written (deleted, replaced or quarantined) before it
// returned: the caller must then move sync_state (see IncrementalSync),
// because head was resolved before the rows were listed, and a row another
// instance projected at a newer head in between is judged at this older
// one.
func (s *Service) reconcileWorkflowRows(ctx context.Context, head, from string) (changed bool, err error) {
	log := observe.Logger(ctx)

	var rows []store.WorkflowProjection
	if lister, ok := s.store.(allWorkflowRowsLister); ok {
		rows, err = lister.ListWorkflowProjections(ctx)
	} else {
		rows, err = s.store.ListActiveWorkflowProjections(ctx)
	}
	if err != nil {
		return false, fmt.Errorf("list workflow rows: %w", err)
	}
	if len(rows) == 0 {
		return false, nil
	}

	files, err := s.git.ListFiles(ctx, head, "")
	if err != nil {
		return false, fmt.Errorf("list files at %s: %w", shortSHA(head), err)
	}
	atHead := make(map[string]struct{}, len(files))
	for _, f := range files {
		atHead[f] = struct{}{}
	}

	// inWindow reports whether a path absent at head still existed at
	// from, i.e. it was deleted (or renamed away) by a commit this sync's
	// Diff covers. The listing at from is taken lazily, once. If it
	// cannot be taken (from pruned, for example) the answer is "no", so
	// the row is reported — the louder of the two outcomes; the delete
	// happens either way.
	var atFrom map[string]struct{}
	inWindow := func(p string) bool {
		if from == "" || from == head {
			return false
		}
		if atFrom == nil {
			atFrom = map[string]struct{}{}
			fromFiles, err := s.git.ListFiles(ctx, from, "")
			if err != nil {
				log.Debug("projection: list files at the last synced commit", "from", shortSHA(from), "error", sanitizeForLog(err.Error()))
			}
			for _, f := range fromFiles {
				atFrom[f] = struct{}{}
			}
		}
		_, ok := atFrom[p]
		return ok
	}

	for i := range rows {
		row := &rows[i]
		active := row.Status == string(domain.WorkflowStatusActive)

		if !s.scope.isWorkflow(row.WorkflowPath) {
			if err := s.store.DeleteWorkflowProjection(ctx, row.WorkflowPath); err != nil {
				return changed, fmt.Errorf("delete out-of-scope workflow row %s: %w", row.WorkflowPath, err)
			}
			changed = true
			if active {
				s.reportWorkflowRejection(ctx, &WorkflowRejection{
					Path: row.WorkflowPath, Commit: head, Check: checkScope,
					Err: errors.New("stored row is not at a workflow definition location (a direct child of workflows/)"),
				}, actionRemoved, nil)
			} else {
				log.Info("projection: removed non-active workflow row outside the workflow definition locations",
					"workflow_path", row.WorkflowPath, "status", row.Status)
			}
			continue
		}

		if _, ok := atHead[row.WorkflowPath]; !ok {
			if err := s.store.DeleteWorkflowProjection(ctx, row.WorkflowPath); err != nil {
				return changed, fmt.Errorf("delete workflow row absent at head %s: %w", row.WorkflowPath, err)
			}
			changed = true
			if inWindow(row.WorkflowPath) {
				log.Info("projection: removed workflow row whose file was deleted after the last synced commit",
					"workflow_path", row.WorkflowPath, "from", shortSHA(from), "head", shortSHA(head))
				continue
			}
			s.reportWorkflowRejection(ctx, &WorkflowRejection{
				Path: row.WorkflowPath, Commit: head, Check: checkAbsentAtHead,
				Err: errors.New("stored row has no file at the sync head"),
			}, actionRemoved, nil)
			continue
		}

		cand, rej, err := s.buildWorkflowRow(ctx, row.WorkflowPath, head)
		if err != nil {
			return changed, err
		}
		if rej == nil {
			if sameWorkflowRow(cand, row) {
				continue
			}
			if err := s.store.UpsertWorkflowProjection(ctx, cand); err != nil {
				return changed, fmt.Errorf("upsert workflow %s: %w", row.WorkflowPath, err)
			}
			changed = true
			continue
		}
		if workflow.VerifyProjectionDefinition(row.Definition) == nil {
			if row.Status != string(domain.WorkflowStatusQuarantined) {
				changed = true // quarantineWorkflowRow writes only then
			}
			if err := s.quarantineWorkflowRow(ctx, row); err != nil {
				return changed, err
			}
			s.reportWorkflowRejection(ctx, rej, actionKept, row)
			continue
		}
		if err := s.store.DeleteWorkflowProjection(ctx, row.WorkflowPath); err != nil {
			return changed, fmt.Errorf("delete unverifiable workflow row %s: %w", row.WorkflowPath, err)
		}
		changed = true
		s.reportWorkflowRejection(ctx, rej, actionRemoved, nil)
	}
	return changed, nil
}

// sameWorkflowRow reports whether a candidate row built at head carries
// the same content as a stored row: identical typed columns, and
// applies_to / definition equal as decoded JSON (JSONB normalises key
// order and whitespace, so byte comparison would see spurious changes).
// source_commit is deliberately not compared — an unchanged row keeps
// the commit runs are pinned to.
func sameWorkflowRow(a, b *store.WorkflowProjection) bool {
	if a.WorkflowPath != b.WorkflowPath || a.WorkflowID != b.WorkflowID || a.Name != b.Name ||
		a.Version != b.Version || a.Status != b.Status {
		return false
	}
	return sameJSON(a.AppliesTo, b.AppliesTo) && sameJSON(a.Definition, b.Definition)
}

func sameJSON(a, b []byte) bool {
	va, err := decodeExactJSON(a)
	if err != nil {
		return false
	}
	vb, err := decodeExactJSON(b)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// decodeExactJSON is json.Unmarshal into `any` (one value, nothing after
// it) with integers kept exact: an integer literal that fits an int64
// becomes an int64, any other number the float64 json.Unmarshal would
// give. With plain float64s two definitions differing only in an integer
// above 2^53 compared equal, so the sweep left a stale row in place
// (INIT-023 codex 6.1 catch-up; core/workflow's VerifyProjectionDefinition
// reads stored definitions the same way).
func decodeExactJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid data after the top-level value")
	}
	return exactJSONNumbers(v)
}

func exactJSONNumbers(v any) (any, error) {
	switch t := v.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(t.String(), 10, 64); err == nil {
			return i, nil
		}
		f, err := t.Float64()
		if err != nil {
			return nil, err
		}
		return f, nil
	case map[string]any:
		for k, child := range t {
			n, err := exactJSONNumbers(child)
			if err != nil {
				return nil, err
			}
			t[k] = n
		}
	case []any:
		for i, child := range t {
			n, err := exactJSONNumbers(child)
			if err != nil {
				return nil, err
			}
			t[i] = n
		}
	}
	return v, nil
}
