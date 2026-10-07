package projection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
)

// Workflow rejection checks (WorkflowRejection.Check).
const (
	checkParse        = "parse"          // strict workflow.Parse refused the file (includes yamlsafe bounds)
	checkRoundTrip    = "round_trip"     // Parse accepted it, but the stored JSON would read back differently
	checkValidate     = "validate"       // workflow.Validate failed and strict validation is on
	checkReadBounds   = "read_bounds"    // the blob is refused by a content-size bound on read
	checkNotAFile     = "not_a_file"     // the commit's tree holds no file (blob) at the path (a gitlink, a directory); checked before every read
	checkScope        = "scope"          // a stored row whose path is not a workflow location
	checkAbsentAtHead = "absent_at_head" // a stored row whose file no longer exists at head
)

// What happened to the stored row for a rejected file.
const (
	actionKept         = "kept"          // a verified previous row is kept, quarantined
	actionNotProjected = "not_projected" // there was no row to keep
	actionRemoved      = "removed"       // the previous row could not be verified and was deleted
)

// maxLoggedErrorBytes caps the rejection text in logs and events. YAML
// error text echoes attacker-chosen keys.
const maxLoggedErrorBytes = 512

// WorkflowRejection reports a workflow file the projection refused
// because of its content (INIT-023 TASK-011). It is never counted as a
// sync error: a file that never changes would otherwise keep the sync in
// `error` forever, and in SMP that stops the governed document index for
// the whole workspace. The durable signals are the Warn log line
// "projection: workflow file rejected" and the
// projection_workflow_rejected event.
type WorkflowRejection struct {
	Path   string // repository-relative workflow path
	Commit string // commit the file was read at
	// Check is one of "parse", "round_trip", "validate", "read_bounds",
	// "not_a_file", "scope" or "absent_at_head".
	Check string
	Err   error
}

func (r *WorkflowRejection) Error() string {
	return fmt.Sprintf("workflow %s rejected at %s (%s): %v", r.Path, shortSHA(r.Commit), r.Check, r.Err)
}

func (r *WorkflowRejection) Unwrap() error { return r.Err }

// WithStrictWorkflowValidation makes a workflow that fails
// workflow.Validate a "validate" rejection instead of an advisory Info
// log. Off by default and off in both repositories: 10 of the 15
// workflow files Spine and SMP ship fail Validate today, so turning it
// on first needs a corpus repair (INIT-023 owner question 7). Strict
// Parse and the round-trip check apply regardless of this flag.
func (s *Service) WithStrictWorkflowValidation(strict bool) {
	s.strictWorkflowValidation = strict
}

// isContentReadError is the single classifier for read errors that mean
// "this blob's content is refused" rather than "git failed". For a
// workflow such a read becomes a read_bounds rejection — the sync
// advances — instead of an infrastructure error that would wedge the
// sync in `error` on a file that never changes; for a changed artifact it
// is a counted read_skipped; for the branch-protection config it is held
// like a malformed config (readFileEntry and classifyReadFailure consult
// it).
// Nothing qualifies yet; the bounded-read work (INIT-023 TASK-003)
// registers its oversize-blob sentinel here.
func isContentReadError(err error) bool {
	_ = err
	return false
}

// buildWorkflowRow reads a workflow file at commit and turns it into a
// projection row. It returns exactly one of: a row to upsert, a content
// rejection, or an infrastructure error (a git read failure).
//
// The file is read with readFileEntry, which types the commit's tree
// entry BEFORE reading (review round 2). An entry that is not a file is a
// not_a_file rejection whether or not `git show` could print something
// for it: a gitlink whose recorded id names a blob in this repository
// reads successfully, and projecting that content would run a body no
// diff shows; a dangling gitlink reads as `fatal: bad object` forever —
// as an infrastructure error it held the sync in error on every entry
// point, and after a restart or a rebuild the pre-diff sweep froze the
// whole projection on it. Refused content is a read_bounds rejection.
// Every other failure is infrastructure.
//
// The row's typed columns come from the strictly parsed definition and
// its definition column from workflow.ProjectionDefinition, which is
// byte-for-byte the historical encoder for every accepted file — SMP
// reads that JSONB raw, so its shape must not change.
func (s *Service) buildWorkflowRow(ctx context.Context, wfPath, commitSHA string) (*store.WorkflowProjection, *WorkflowRejection, error) {
	content, unreadable, err := s.readFileEntry(ctx, commitSHA, wfPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read workflow %s: %w", wfPath, err)
	}
	if unreadable != nil {
		check := checkNotAFile
		if unreadable.entry == entryContentRefused {
			check = checkReadBounds
		}
		return nil, &WorkflowRejection{Path: wfPath, Commit: commitSHA, Check: check, Err: unreadable.cause()}, nil
	}

	wf, definition, err := workflow.ProjectionDefinition(wfPath, content)
	if err != nil {
		check := checkParse
		if errors.Is(err, workflow.ErrProjectionRoundTrip) {
			check = checkRoundTrip
		}
		return nil, &WorkflowRejection{Path: wfPath, Commit: commitSHA, Check: check, Err: err}, nil
	}

	// workflow.Validate is advisory at projection (INIT-023 owner
	// question 7): content checks are enforced at create/update. Info,
	// not Warn — shipped files trip it on every rebuild.
	if result := workflow.Validate(wf); result.Status == "failed" {
		first := ""
		if len(result.Errors) > 0 {
			first = result.Errors[0].Message
		}
		if s.strictWorkflowValidation {
			return nil, &WorkflowRejection{
				Path: wfPath, Commit: commitSHA, Check: checkValidate,
				Err: fmt.Errorf("workflow.Validate failed with %d error(s); first: %s", len(result.Errors), first),
			}, nil
		}
		observe.Logger(ctx).Info("workflow fails workflow.Validate; projected (projection validation is advisory)",
			"workflow_path", wfPath,
			"commit", shortSHA(commitSHA),
			"errors", len(result.Errors),
			"first_error", sanitizeForLog(first),
		)
	}

	appliesTo, err := json.Marshal(wf.AppliesTo)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal workflow applies_to %s: %w", wfPath, err)
	}
	return &store.WorkflowProjection{
		WorkflowPath: wfPath,
		WorkflowID:   wf.ID,
		Name:         wf.Name,
		Version:      wf.Version,
		Status:       string(wf.Status),
		AppliesTo:    appliesTo,
		Definition:   definition,
		SourceCommit: commitSHA,
	}, nil, nil
}

// projectWorkflow reads, verifies and upserts one workflow file. A
// content rejection is returned as a *WorkflowRejection error; callers
// that must tell the two apart use buildWorkflowRow.
func (s *Service) projectWorkflow(ctx context.Context, wfPath, commitSHA string) error {
	row, rej, err := s.buildWorkflowRow(ctx, wfPath, commitSHA)
	if err != nil {
		return err
	}
	if rej != nil {
		return rej
	}
	return s.store.UpsertWorkflowProjection(ctx, row)
}

// syncWorkflowFile projects one added, modified or renamed-to workflow
// path during IncrementalSync. It returns rejected=true for a content
// rejection (already reported, never an error) and an error only for an
// infrastructure failure the caller counts into the sync errors.
func (s *Service) syncWorkflowFile(ctx context.Context, wfPath, head string) (rejected bool, err error) {
	row, rej, err := s.buildWorkflowRow(ctx, wfPath, head)
	if err != nil {
		return false, err
	}
	if rej == nil {
		if err := s.store.UpsertWorkflowProjection(ctx, row); err != nil {
			return false, fmt.Errorf("upsert workflow %s: %w", wfPath, err)
		}
		return false, nil
	}
	return true, s.applyRejection(ctx, rej)
}

// applyRejection decides what happens to the stored row of a rejected
// file, applies it and reports the rejection:
//   - no row: not_projected;
//   - a row that passes workflow.VerifyProjectionDefinition: kept, and
//     QUARANTINED (owner question 4) — its definition, typed columns and
//     source_commit stay, so runs pinned to that commit keep loading it,
//     but its status column becomes "Quarantined", so no new run binds
//     it. Otherwise `main` could show a reviewed workflow while new runs
//     bind an older, review-less one kept from before the rejected edit;
//   - a row that cannot be verified (a smuggled pre-upgrade row): removed.
//
// Store errors are infrastructure errors.
func (s *Service) applyRejection(ctx context.Context, rej *WorkflowRejection) error {
	prev, err := s.getWorkflowRow(ctx, rej.Path)
	if err != nil {
		return err
	}
	switch {
	case prev == nil:
		s.reportWorkflowRejection(ctx, rej, actionNotProjected, nil)
	case workflow.VerifyProjectionDefinition(prev.Definition) == nil:
		if err := s.quarantineWorkflowRow(ctx, prev); err != nil {
			return err
		}
		s.reportWorkflowRejection(ctx, rej, actionKept, prev)
	default:
		if err := s.store.DeleteWorkflowProjection(ctx, rej.Path); err != nil {
			return fmt.Errorf("delete unverifiable workflow row %s: %w", rej.Path, err)
		}
		s.reportWorkflowRejection(ctx, rej, actionRemoved, nil)
	}
	return nil
}

// getWorkflowRow returns the stored row for a path, nil when there is
// none, or an infrastructure error.
func (s *Service) getWorkflowRow(ctx context.Context, wfPath string) (*store.WorkflowProjection, error) {
	prev, err := s.store.GetWorkflowProjection(ctx, wfPath)
	if err != nil {
		var se *domain.SpineError
		if errors.As(err, &se) && se.Code == domain.ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("get workflow row %s: %w", wfPath, err)
	}
	return prev, nil
}

// quarantineWorkflowRow upserts a kept row with only its status column
// changed to Quarantined. Everything else — definition, typed columns,
// source_commit — is written back unchanged. A row that is already
// quarantined is not touched.
func (s *Service) quarantineWorkflowRow(ctx context.Context, row *store.WorkflowProjection) error {
	if row.Status == string(domain.WorkflowStatusQuarantined) {
		return nil
	}
	q := *row
	q.Status = string(domain.WorkflowStatusQuarantined)
	if err := s.store.UpsertWorkflowProjection(ctx, &q); err != nil {
		return fmt.Errorf("quarantine workflow row %s: %w", row.WorkflowPath, err)
	}
	*row = q
	return nil
}

// reportWorkflowRejection logs one Warn line and emits one
// projection_workflow_rejected event (a no-op without an event router,
// which is SMP's configuration). The event id is deterministic over
// (path, commit, action), so the repeat report from a sweep on the next
// instance start de-duplicates.
func (s *Service) reportWorkflowRejection(ctx context.Context, rej *WorkflowRejection, action string, kept *store.WorkflowProjection) {
	keptCommit := ""
	quarantined := false
	if kept != nil {
		keptCommit = kept.SourceCommit
		quarantined = kept.Status == string(domain.WorkflowStatusQuarantined)
	}
	errText := ""
	if rej.Err != nil {
		errText = sanitizeForLog(rej.Err.Error())
	}
	observe.Logger(ctx).Warn("projection: workflow file rejected",
		"workflow_path", rej.Path,
		"commit", rej.Commit,
		"check", rej.Check,
		"action", action,
		"kept_source_commit", keptCommit,
		"quarantined", quarantined,
		"error", errText,
	)
	if s.events == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"workflow_path":      rej.Path,
		"commit":             rej.Commit,
		"check":              rej.Check,
		"action":             action,
		"kept_source_commit": keptCommit,
		"quarantined":        quarantined,
		"error":              errText,
	})
	if err != nil {
		observe.Logger(ctx).Warn("projection: marshal workflow rejection event", "error", err)
		return
	}
	sum := sha256.Sum256([]byte(rej.Path + "\x00" + rej.Commit + "\x00" + action))
	event.EmitLogged(ctx, s.events, domain.Event{
		EventID:      "wfrej-" + hex.EncodeToString(sum[:])[:16],
		Type:         domain.EventProjectionWorkflowRejected,
		ArtifactPath: rej.Path,
		Payload:      payload,
	})
}

// sanitizeForLog renders s as one bounded line: line breaks become
// "; " (yaml.v3 puts the useful "line N: field … not found" detail on
// the second line, so it is joined rather than cut), every other control
// or format character (bidi overrides, U+2028/U+2029) and invalid UTF-8
// becomes U+FFFD, and the result is capped at maxLoggedErrorBytes.
// Rejection text can echo attacker-chosen YAML keys and values.
func sanitizeForLog(s string) string {
	var b strings.Builder
	lastBreak := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\u2028' || r == '\u2029' {
			if !lastBreak && b.Len() > 0 {
				if b.Len()+2 > maxLoggedErrorBytes {
					break
				}
				b.WriteString("; ")
			}
			lastBreak = true
			continue
		}
		lastBreak = false
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			r = utf8.RuneError
		}
		if b.Len()+utf8.RuneLen(r) > maxLoggedErrorBytes {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimRight(b.String(), "; ")
}

// truncateForLog caps an arbitrary string for an error message.
func truncateForLog(s string, n int) string {
	s = sanitizeForLog(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
