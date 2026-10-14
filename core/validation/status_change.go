package validation

import (
	"context"
	"errors"
	"sync"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

// ValidateStatusChange evaluates the status-conflict (SC-*) rules against
// the projected artifact as if its status were newStatus, with the
// blocking promotion applied (INIT-023 TASK-017, the merge-time SC gate).
// The governed merge calls it for an owned status transition before the
// new status reaches the authoritative branch: validation reads the
// projection of main, where a parent is never Completed before its own
// merge, so promotion alone (Validate) would only change reporting
// (Decision D8).
//
// It copies the projection row, sets Status, and runs only the rules
// classified status_conflict; children's statuses come from the projection
// (exact for the merge, which refuses non-owned status edits). An artifact
// not in the projection → passed, nil (nothing to judge yet). A store
// failure → a non-nil error, never a silent pass: the projection read
// itself, and every store read a blocking rule makes (the rules swallow
// their own query errors, so a failed child listing would otherwise look
// like "no open children"). A nil engine → passed.
func (e *Engine) ValidateStatusChange(ctx context.Context, artifactPath string, newStatus domain.ArtifactStatus) (domain.ValidationResult, error) {
	passed := domain.ValidationResult{Status: "passed"}
	if e == nil {
		return passed, nil
	}
	proj, err := e.store.GetArtifactProjection(ctx, artifactPath)
	if err != nil {
		if isStoreNotFound(err) {
			return passed, nil
		}
		return domain.ValidationResult{}, err
	}
	cp := *proj
	cp.Status = string(newStatus)

	var errs, warnings []domain.ValidationError
	for _, rule := range e.rules {
		if rule.Classification() != domain.ViolationStatusConflict {
			continue
		}
		st := store.Store(e.store)
		var rec *errRecordingStore
		if e.blocking[rule.ID()] {
			rec = &errRecordingStore{Store: e.store}
			st = rec
		}
		results := rule.Evaluate(ctx, &cp, st)
		if rec != nil {
			if err := rec.firstErr(); err != nil {
				return domain.ValidationResult{}, err
			}
		}
		for i := range results {
			results[i].ArtifactPath = artifactPath
			results[i].Classification = rule.Classification()
			e.promote(&results[i])
			if results[i].Severity == "warning" {
				warnings = append(warnings, results[i])
			} else {
				errs = append(errs, results[i])
			}
		}
	}
	status := "passed"
	if len(errs) > 0 {
		status = "failed"
	} else if len(warnings) > 0 {
		status = "warnings"
	}
	return domain.ValidationResult{Status: status, Errors: errs, Warnings: warnings}, nil
}

func isStoreNotFound(err error) bool {
	var se *domain.SpineError
	return errors.As(err, &se) && se.Code == domain.ErrNotFound
}

// errRecordingStore records the first store failure a rule's reads hit
// (a not-found projection — a dangling link target — is not a failure).
type errRecordingStore struct {
	store.Store
	mu  sync.Mutex
	err error
}

func (s *errRecordingStore) record(err error) {
	if err == nil || isStoreNotFound(err) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

func (s *errRecordingStore) firstErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *errRecordingStore) QueryArtifacts(ctx context.Context, q store.ArtifactQuery) (*store.ArtifactQueryResult, error) {
	res, err := s.Store.QueryArtifacts(ctx, q)
	s.record(err)
	return res, err
}

func (s *errRecordingStore) GetArtifactProjection(ctx context.Context, path string) (*store.ArtifactProjection, error) {
	res, err := s.Store.GetArtifactProjection(ctx, path)
	s.record(err)
	return res, err
}
