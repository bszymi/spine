//go:build integration

package store_test

// INIT-023 TASK-006 AC 22: the governance-path guard's storage, on
// PostgreSQL. MergeCommitMeta merges keys atomically, and
// MergeCommitMetaIfStatus only in the given run status; and
// ListGovernanceAuthorizations returns honoured override pushes (in the
// shape both Spine's delivery subscriber and SMP's emitter write: the
// whole domain.Event JSON, branch "main" or "refs/heads/main") and
// consumed approvals, and nothing else.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

func govSHA(c byte) string { return strings.Repeat(string(c), 40) }

func writeOverride(t *testing.T, s *store.PostgresStore, id, actor string, at time.Time, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	evt, err := json.Marshal(domain.Event{
		EventID: id, Type: domain.EventBranchProtectionOverride, Timestamp: at, ActorID: actor, Payload: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteEventLog(context.Background(), &store.EventLogEntry{
		EventID: id, EventType: string(domain.EventBranchProtectionOverride), Payload: evt, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
}

func gitOverride(branch, op, oldSHA, newSHA string) map[string]any {
	commit := any(newSHA)
	if newSHA == "" {
		commit = nil
	}
	return map[string]any{
		"branch": branch, "rule_kinds": []string{"no-direct-write"}, "operation": op, "commit_sha": commit,
		"pre_receive_ref": map[string]any{"old_sha": oldSHA, "new_sha": newSHA, "ref": branch},
	}
}

func TestGovernanceStore_Integration(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	defer s.CleanupTestData(ctx, t)
	cleanupEvents := func() {
		if err := s.ExecRaw(ctx, `DELETE FROM runtime.event_log WHERE event_id LIKE 'gov-it-%'`); err != nil {
			t.Fatal(err)
		}
	}
	cleanupEvents()
	defer cleanupEvents()

	t0 := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	// Included: the git push path (refs/heads/main), and the same shape
	// with the short branch name.
	writeOverride(t, s, "gov-it-git", "op-1", t0, gitOverride("refs/heads/main", "direct_write", govSHA('1'), govSHA('2')))
	writeOverride(t, s, "gov-it-short", "op-2", t0.Add(time.Minute), gitOverride("main", "direct_write", govSHA('3'), govSHA('4')))
	// Excluded: a delete, another branch, the API-path shape (no
	// pre_receive_ref), another event type with the same payload.
	writeOverride(t, s, "gov-it-delete", "op-1", t0.Add(2*time.Minute), gitOverride("refs/heads/main", "delete", govSHA('5'), ""))
	writeOverride(t, s, "gov-it-other", "op-1", t0.Add(3*time.Minute), gitOverride("refs/heads/release", "direct_write", govSHA('6'), govSHA('7')))
	writeOverride(t, s, "gov-it-api", "op-1", t0.Add(4*time.Minute), map[string]any{
		"branch": "main", "rule_kinds": []string{"no-direct-write"}, "operation": "direct_write", "commit_sha": govSHA('8')})
	other, _ := json.Marshal(domain.Event{EventID: "gov-it-type", Type: domain.EventRunCompleted, Timestamp: t0,
		Payload: mustJSON(t, gitOverride("refs/heads/main", "direct_write", govSHA('9'), govSHA('a')))})
	if err := s.WriteEventLog(ctx, &store.EventLogEntry{EventID: "gov-it-type", EventType: string(domain.EventRunCompleted), Payload: other, CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}

	mkRun := func(id string, meta map[string]string) {
		t.Helper()
		if err := s.CreateRun(ctx, &domain.Run{RunID: id, TaskPath: "tasks/t.md", WorkflowPath: "workflows/w.yaml",
			WorkflowID: "w", WorkflowVersion: "v", Status: domain.RunStatusCompleted, TraceID: "trace-" + id, CreatedAt: t0}); err != nil {
			t.Fatal(err)
		}
		if meta != nil {
			if err := s.SetCommitMeta(ctx, id, meta); err != nil {
				t.Fatal(err)
			}
		}
	}
	approval := func(sha, mergeSHA, baseSHA, at string) map[string]string {
		m := map[string]string{
			domain.CommitMetaGovernanceApprovalSHA:        sha,
			domain.CommitMetaGovernanceApprovalActorID:    "op-9",
			domain.CommitMetaGovernanceApprovalActorRole:  "operator",
			domain.CommitMetaGovernanceApprovalApprovedAt: at,
			"status": "Completed",
		}
		if mergeSHA != "" {
			m[domain.CommitMetaGovernanceApprovalMergeSHA] = mergeSHA
		}
		if baseSHA != "" {
			m[domain.CommitMetaGovernanceApprovalBaseSHA] = baseSHA
		}
		return m
	}
	// Consumed approval: no repository_merge_outcomes row is needed.
	mkRun("run-gov-consumed", approval(govSHA('b'), govSHA('c'), govSHA('d'), "2026-10-14T11:00:00Z"))
	// Excluded: never consumed, tombstoned by a reject, no base.
	mkRun("run-gov-unconsumed", approval(govSHA('e'), "", "", "2026-10-14T11:01:00Z"))
	mkRun("run-gov-tombstone", approval("", govSHA('f'), govSHA('1'), ""))
	mkRun("run-gov-nobase", approval(govSHA('2'), govSHA('3'), "", "2026-10-14T11:02:00Z"))
	mkRun("run-gov-plain", map[string]string{"status": "Completed"})
	mkRun("run-gov-null", nil)

	recs, err := s.ListGovernanceAuthorizations(ctx, "main", 50)
	if err != nil {
		t.Fatalf("ListGovernanceAuthorizations: %v", err)
	}
	got := map[string]store.GovernanceAuthorization{}
	for _, r := range recs {
		key := r.EventID
		if r.Source == store.GovernanceSourceApprovedMerge {
			key = r.RunID
		}
		got[key] = r
	}
	if len(recs) != 3 {
		t.Fatalf("records = %+v, want exactly gov-it-git, gov-it-short and run-gov-consumed", recs)
	}
	if r := got["gov-it-git"]; r.Source != store.GovernanceSourceOverrideEvent || r.CommitSHA != govSHA('2') || r.BaseSHA != govSHA('1') || r.ActorID != "op-1" {
		t.Fatalf("git override record = %+v", r)
	}
	if r := got["gov-it-short"]; r.CommitSHA != govSHA('4') || r.BaseSHA != govSHA('3') || r.ActorID != "op-2" {
		t.Fatalf("short-branch override record = %+v", r)
	}
	if r := got["run-gov-consumed"]; r.Source != store.GovernanceSourceApprovedMerge || r.CommitSHA != govSHA('c') ||
		r.BaseSHA != govSHA('d') || r.ActorID != "op-9" || !r.RecordedAt.Equal(time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("approval record = %+v", r)
	}
	if !recs[0].RecordedAt.After(recs[len(recs)-1].RecordedAt) {
		t.Fatalf("records not newest first: %+v", recs)
	}
	// The limit applies per source.
	one, err := s.ListGovernanceAuthorizations(ctx, "main", 1)
	if err != nil || len(one) != 2 {
		t.Fatalf("limit 1 per source = %+v, %v; want one of each source", one, err)
	}
	if none, err := s.ListGovernanceAuthorizations(ctx, "release", 50); err != nil || len(none) != 2 {
		// release: its override plus the consumed approval (approvals
		// carry no branch; the caller's reachability check decides).
		t.Fatalf("release records = %+v, %v", none, err)
	}

	// MergeCommitMeta keeps existing keys, merges into a NULL column, and
	// reports an unknown run.
	if err := s.MergeCommitMeta(ctx, "run-gov-consumed", map[string]string{domain.CommitMetaGovernanceApprovalSHA: ""}); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetRun(ctx, "run-gov-consumed")
	if err != nil {
		t.Fatal(err)
	}
	if r.CommitMeta["status"] != "Completed" || r.CommitMeta[domain.CommitMetaGovernanceApprovalMergeSHA] != govSHA('c') ||
		r.CommitMeta[domain.CommitMetaGovernanceApprovalSHA] != "" {
		t.Fatalf("after MergeCommitMeta commit_meta = %v", r.CommitMeta)
	}
	if after, err := s.ListGovernanceAuthorizations(ctx, "main", 50); err != nil || len(after) != 2 {
		t.Fatalf("a tombstoned approval is still listed: %+v, %v", after, err)
	}
	if err := s.MergeCommitMeta(ctx, "run-gov-null", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetRun(ctx, "run-gov-null"); err != nil || r.CommitMeta["k"] != "v" || len(r.CommitMeta) != 1 {
		t.Fatalf("merge into NULL: %+v, %v", r, err)
	}
	err = s.MergeCommitMeta(ctx, "run-gov-missing", map[string]string{"k": "v"})
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrNotFound {
		t.Fatalf("unknown run: %v, want not_found", err)
	}

	// MergeCommitMetaIfStatus (review round 1): applied only in the given
	// status, keeping every other key; a wrong status or an unknown run
	// writes nothing and reports false.
	if ok, err := s.MergeCommitMetaIfStatus(ctx, "run-gov-consumed", domain.RunStatusCommitting,
		map[string]string{domain.CommitMetaGovernanceApprovalSHA: govSHA('d')}); err != nil || ok {
		t.Fatalf("wrong status: applied=%v err=%v; want not applied", ok, err)
	}
	if r, err := s.GetRun(ctx, "run-gov-consumed"); err != nil || r.CommitMeta[domain.CommitMetaGovernanceApprovalSHA] != "" {
		t.Fatalf("a refused conditional write changed commit_meta: %v %v", r.CommitMeta, err)
	}
	if ok, err := s.MergeCommitMetaIfStatus(ctx, "run-gov-consumed", domain.RunStatusCompleted,
		map[string]string{domain.CommitMetaGovernanceApprovalSHA: govSHA('d')}); err != nil || !ok {
		t.Fatalf("matching status: applied=%v err=%v; want applied", ok, err)
	}
	if r, err := s.GetRun(ctx, "run-gov-consumed"); err != nil || r.CommitMeta[domain.CommitMetaGovernanceApprovalSHA] != govSHA('d') ||
		r.CommitMeta["status"] != "Completed" || r.CommitMeta[domain.CommitMetaGovernanceApprovalMergeSHA] != govSHA('c') {
		t.Fatalf("after the conditional write commit_meta = %v, %v", r.CommitMeta, err)
	}
	if ok, err := s.MergeCommitMetaIfStatus(ctx, "run-gov-missing", domain.RunStatusCompleted, map[string]string{"k": "v"}); err != nil || ok {
		t.Fatalf("unknown run: applied=%v err=%v; want not applied, no error", ok, err)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
