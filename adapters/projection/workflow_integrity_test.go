package projection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
)

// INIT-023 TASK-011, audit P3-13: the projection stores a workflow only
// when strict Parse accepts it and its JSON reads back to exactly the
// parsed definition; a rejected file keeps its last verified row,
// quarantined, and the sync advances. These tests are the inverted audit
// PoCs (TestPOC_ProjectionWorkflowFoldedKeyDifferential /
// …FoldedOutcomeOverride).

const wfPath = "workflows/task-default.yaml"

// newIntegrityFixture commits body1 as workflows/task-default.yaml,
// FullRebuilds a fresh store and returns everything for the next step.
func newIntegrityFixture(t *testing.T, body1 string) (*scratchRepo, allRowsStore, *recordingRouter, *Service, string) {
	t.Helper()
	repo := newScratchRepo(t)
	repo.write(wfPath, body1)
	c1 := repo.commit("c1")
	st := allRowsStore{newMapStore()}
	router := &recordingRouter{}
	svc := NewService(repo.git, st, router, 0)
	if err := svc.FullRebuild(context.Background()); err != nil {
		t.Fatalf("FullRebuild: %v", err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c1 {
		t.Fatalf("after rebuild: %+v", got)
	}
	if r := st.row(wfPath); r == nil || r.SourceCommit != c1 || r.Status != "Active" {
		t.Fatalf("after rebuild row = %+v", r)
	}
	router.reset()
	return repo, st, router, svc, c1
}

func assertKeptQuarantined(t *testing.T, st allRowsStore, c1 string) *store.WorkflowProjection {
	t.Helper()
	row := st.row(wfPath)
	if row == nil {
		t.Fatal("the verified row must be kept")
	}
	if row.SourceCommit != c1 {
		t.Fatalf("kept row source_commit = %s, want %s (pinned runs must keep loading it)", row.SourceCommit, c1)
	}
	if row.Status != string(domain.WorkflowStatusQuarantined) {
		t.Fatalf("kept row status = %q, want Quarantined", row.Status)
	}
	active, err := st.ListActiveWorkflowProjections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range active {
		if a.WorkflowPath == wfPath {
			t.Fatal("a quarantined row must not be returned as Active")
		}
	}
	got, err := st.GetWorkflowProjection(context.Background(), wfPath)
	if err != nil || got.SourceCommit != c1 {
		t.Fatalf("path-keyed load for a pinned run must still return the c1 row: %+v, %v", got, err)
	}
	return row
}

func assertOneRejection(t *testing.T, router *recordingRouter, check, action, keptCommit string, quarantined bool) map[string]any {
	t.Helper()
	rej := router.rejections(t)
	if len(rej) != 1 {
		t.Fatalf("expected exactly one projection_workflow_rejected event, got %d: %+v", len(rej), rej)
	}
	p := rej[0]
	if p["workflow_path"] != wfPath || p["check"] != check || p["action"] != action ||
		p["kept_source_commit"] != keptCommit || p["quarantined"] != quarantined {
		t.Fatalf("rejection payload = %+v, want check=%s action=%s kept=%s quarantined=%v", p, check, action, keptCommit, quarantined)
	}
	ev := router.ofType(domain.EventProjectionWorkflowRejected)[0]
	if ev.ArtifactPath != wfPath || !strings.HasPrefix(ev.EventID, "wfrej-") {
		t.Fatalf("event envelope = %+v", ev)
	}
	return p
}

// AC 5 (inverted TestPOC_ProjectionWorkflowFoldedKeyDifferential).
func TestIncrementalSync_FoldedKeyWorkflowIsRejectedAndVerifiedRowKept(t *testing.T) {
	repo, st, router, svc, c1 := newIntegrityFixture(t, reviewedWorkflow)

	repo.write(wfPath, pocFoldWorkflow)
	c2 := repo.commit("c2")
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatalf("IncrementalSync: %v", err)
	}

	row := assertKeptQuarantined(t, st, c1)
	if wf := decodeDef(t, row.Definition); !hasStep(wf, "review") {
		t.Fatalf("kept definition lost the review step: %v", stepIDs(wf))
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("a rejection must advance the sync: %+v", got)
	}
	assertOneRejection(t, router, "parse", "kept", c1, true)
}

// AC 5, second payload (inverted …FoldedOutcomeOverride).
func TestIncrementalSync_FoldedOutcomeOverrideIsRejectedAndVerifiedRowKept(t *testing.T) {
	repo, st, router, svc, c1 := newIntegrityFixture(t, validateGateWorkflow)

	repo.write(wfPath, pocFoldOutcome)
	c2 := repo.commit("c2")
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatalf("IncrementalSync: %v", err)
	}

	row := assertKeptQuarantined(t, st, c1)
	wf := decodeDef(t, row.Definition)
	if got := wf.Steps[0].Outcomes[1].NextStep; got != "validate" {
		t.Fatalf("failed outcome routes to %q, want validate", got)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("sync state: %+v", got)
	}
	assertOneRejection(t, router, "parse", "kept", c1, true)
}

// AC 6: FullRebuild applies the same rule across its delete-all window.
func TestFullRebuild_KeepsVerifiedRowOfRejectedFileQuarantined(t *testing.T) {
	repo, st, router, svc, c1 := newIntegrityFixture(t, reviewedWorkflow)
	repo.write(wfPath, pocFoldWorkflow)
	c2 := repo.commit("c2")
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	router.reset()

	if err := svc.FullRebuild(context.Background()); err != nil {
		t.Fatalf("FullRebuild: %v", err)
	}
	row := assertKeptQuarantined(t, st, c1)
	if !hasStep(decodeDef(t, row.Definition), "review") {
		t.Fatal("restored row must be the c1 definition")
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("sync state: %+v", got)
	}
	assertOneRejection(t, router, "parse", "kept", c1, true)
}

func TestFullRebuild_RejectedFileWithNoPreviousRowIsNotProjected(t *testing.T) {
	repo := newScratchRepo(t)
	repo.write(wfPath, pocFoldWorkflow)
	c2 := repo.commit("c2")
	st := allRowsStore{newMapStore()}
	router := &recordingRouter{}
	svc := NewService(repo.git, st, router, 0)

	if err := svc.FullRebuild(context.Background()); err != nil {
		t.Fatalf("FullRebuild: %v", err)
	}
	if st.row(wfPath) != nil {
		t.Fatal("a rejected file must not be projected")
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("sync state: %+v", got)
	}
	assertOneRejection(t, router, "parse", "not_projected", "", false)
}

// AC 7: an unverifiable stored row (the 61994fe encoding of the smuggled
// file) is removed when the file changes to another rejected body.
func TestIncrementalSync_UnverifiableRowIsRemovedOnRejection(t *testing.T) {
	for _, swept := range []bool{true, false} {
		t.Run(fmt.Sprintf("instance_already_swept=%v", swept), func(t *testing.T) {
			repo := newScratchRepo(t)
			repo.write(wfPath, pocFoldWorkflow)
			c1 := repo.commit("c1")
			st := allRowsStore{newMapStore()}
			st.putRow(legacyRow(t, wfPath, pocFoldWorkflow, c1))
			st.setState(c1, "idle")
			router := &recordingRouter{}
			svc := NewService(repo.git, st, router, 0)
			if swept {
				// Isolate the diff path from the upgrade sweep.
				svc.workflowsVerified.Store(true)
			}

			repo.write(wfPath, strings.Replace(pocFoldWorkflow, `version: "8.0"`, `version: "8.1"`, 1))
			c2 := repo.commit("c2")
			if err := svc.IncrementalSync(context.Background()); err != nil {
				t.Fatalf("IncrementalSync: %v", err)
			}
			if st.row(wfPath) != nil {
				t.Fatal("the unverifiable row must be deleted")
			}
			if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
				t.Fatalf("sync state: %+v", got)
			}
			rej := router.rejections(t)
			if len(rej) == 0 || rej[0]["action"] != "removed" || rej[0]["check"] != "parse" {
				t.Fatalf("expected a removed/parse rejection first, got %+v", rej)
			}
		})
	}
}

// AC 8: workflow.Validate is advisory unless strict validation is on.
func TestProjection_ValidateIsAdvisoryUnlessStrict(t *testing.T) {
	draft := strings.Replace(reviewedWorkflow, "status: Active", "status: Draft", 1)
	draft = strings.Replace(draft, "description: Human-reviewed task workflow\n", "", 1) // also fails Validate (description)
	if res := workflow.Validate(mustParse(t, draft)); res.Status != "failed" {
		t.Fatalf("precondition: the fixture must fail Validate, got %+v", res)
	}

	t.Run("advisory", func(t *testing.T) {
		logs := captureLogs(t)
		repo := newScratchRepo(t)
		repo.write(wfPath, draft)
		repo.commit("c1")
		st := allRowsStore{newMapStore()}
		svc := NewService(repo.git, st, &recordingRouter{}, 0)
		if err := svc.FullRebuild(context.Background()); err != nil {
			t.Fatal(err)
		}
		if r := st.row(wfPath); r == nil || r.Status != "Draft" {
			t.Fatalf("a Validate failure must still be projected, got %+v", r)
		}
		if len(logs.lines("level=INFO", "projection validation is advisory", wfPath)) != 1 {
			t.Fatalf("expected one advisory Info line, got:\n%s", logs.String())
		}
	})

	t.Run("strict", func(t *testing.T) {
		repo := newScratchRepo(t)
		repo.write(wfPath, draft)
		repo.commit("c1")
		st := allRowsStore{newMapStore()}
		router := &recordingRouter{}
		svc := NewService(repo.git, st, router, 0)
		svc.WithStrictWorkflowValidation(true)
		if err := svc.FullRebuild(context.Background()); err != nil {
			t.Fatal(err)
		}
		if st.row(wfPath) != nil || st.upserts(wfPath) != 0 {
			t.Fatal("strict validation must not upsert the row")
		}
		assertOneRejection(t, router, "validate", "not_projected", "", false)
		if st.syncState().Status != "idle" {
			t.Fatalf("a validate rejection is not a sync error: %+v", st.syncState())
		}
	})
}

func mustParse(t *testing.T, body string) *domain.WorkflowDefinition {
	t.Helper()
	wf, err := workflow.Parse(wfPath, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return wf
}

// failReadGit fails ReadFile for one path at one ref with a non-git error.
type failReadGit struct {
	git.GitClient
	path, ref string
	err       error
}

func (g *failReadGit) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
	if path == g.path && (g.ref == "" || ref == g.ref) {
		return nil, g.err
	}
	return g.GitClient.ReadFile(ctx, ref, path)
}

// AC 9: a rejection never sets error; a ReadFile failure on a changed
// workflow does, and holds last_synced_commit.
func TestIncrementalSync_WorkflowReadFailureIsSyncErrorRejectionIsNot(t *testing.T) {
	repo, st, _, _, c1 := newIntegrityFixture(t, reviewedWorkflow)
	repo.write(wfPath, strings.Replace(reviewedWorkflow, `version: "1.0"`, `version: "1.1"`, 1))
	c2 := repo.commit("c2")

	g := &failReadGit{GitClient: repo.git, path: wfPath, ref: c2, err: errSimulated}
	svc := NewService(g, st, nil, 0)
	svc.workflowsVerified.Store(true) // isolate from the sweep
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatalf("a counted sync error is reported through sync_state, got %v", err)
	}
	if got := st.syncState(); got.Status != "error" || got.LastSyncedCommit != c1 {
		t.Fatalf("a workflow read failure must hold the sync: %+v", got)
	}
	if r := st.row(wfPath); r == nil || r.SourceCommit != c1 || r.Status != "Active" {
		t.Fatalf("the row must be untouched on an infrastructure error: %+v", r)
	}

	// Healthy retry projects the change.
	svc2 := NewService(repo.git, st, nil, 0)
	if err := svc2.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("retry: %+v", got)
	}
	if r := st.row(wfPath); r.Version != "1.1" || r.SourceCommit != c2 {
		t.Fatalf("retry row: %+v", r)
	}
}

// AC 11: nested files under workflows/ are not workflow definitions.
func TestProjection_NestedWorkflowFilesAreNotWorkflows(t *testing.T) {
	logs := captureLogs(t)
	repo := newScratchRepo(t)
	repo.write(wfPath, reviewedWorkflow)
	repo.write("workflows/steps/validate.yaml", stepProfile)
	repo.commit("c1")
	st := allRowsStore{newMapStore()}
	router := &recordingRouter{}
	svc := NewService(repo.git, st, router, 0)
	if err := svc.FullRebuild(context.Background()); err != nil {
		t.Fatal(err)
	}

	repo.write(wfPath, strings.Replace(reviewedWorkflow, `version: "1.0"`, `version: "1.1"`, 1))
	repo.write("workflows/steps/validate.yaml", stepProfile+"  - name: go-test\n    run: go test ./...\n")
	c2 := repo.commit("c2")
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(st.workflows) != 1 || st.row(wfPath) == nil || st.row(wfPath).Version != "1.1" {
		t.Fatalf("expected only the top-level row, got %v", st.workflows)
	}
	if rej := router.rejections(t); len(rej) != 0 {
		t.Fatalf("a nested file must not be reported as a rejection: %+v", rej)
	}
	if len(logs.lines("workflow file rejected")) != 0 {
		t.Fatalf("no rejection Warn expected:\n%s", logs.String())
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("sync state: %+v", got)
	}
}

// AC 27: no stealth downgrade through a kept row. c1 is a valid
// review-less workflow; c2 shows a review step but is rejected (an
// unknown key). New runs must not bind the review-less c1 version while
// main displays the reviewed one.
func TestIncrementalSync_KeptRowIsNotBindable(t *testing.T) {
	repo, st, router, svc, c1 := newIntegrityFixture(t, reviewlessWorkflow)

	repo.write(wfPath, reviewedWorkflow+"x: 1\n")
	c2 := repo.commit("c2")
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}

	row := assertKeptQuarantined(t, st, c1)
	if hasStep(decodeDef(t, row.Definition), "review") {
		t.Fatal("the kept row must be c1's definition")
	}
	provider := workflow.NewProjectionProviderFromListFn(func(ctx context.Context) ([]workflow.WorkflowProjection, error) {
		rows, err := st.ListActiveWorkflowProjections(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]workflow.WorkflowProjection, len(rows))
		for i := range rows {
			out[i] = workflow.WorkflowProjection{
				WorkflowPath: rows[i].WorkflowPath, WorkflowID: rows[i].WorkflowID, Name: rows[i].Name,
				Version: rows[i].Version, Status: rows[i].Status, AppliesTo: rows[i].AppliesTo,
				Definition: rows[i].Definition, SourceCommit: rows[i].SourceCommit,
			}
		}
		return out, nil
	})
	_, err := workflow.ResolveBinding(context.Background(), provider, nil, "Task", "")
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrWorkflowNotFound {
		t.Fatalf("binding must fail closed (workflow_not_found) while main holds a rejected body, got %v", err)
	}
	assertOneRejection(t, router, "parse", "kept", c1, true)
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("sync state: %+v", got)
	}

	// c3 fixes the file: the row becomes c3's definition, Active again.
	repo.write(wfPath, reviewedWorkflow)
	c3 := repo.commit("c3")
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixed := st.row(wfPath)
	if fixed.Status != "Active" || fixed.SourceCommit != c3 || !hasStep(decodeDef(t, fixed.Definition), "review") {
		t.Fatalf("fixed row = %+v", fixed)
	}
	if b, err := workflow.ResolveBinding(context.Background(), provider, nil, "Task", ""); err != nil || b.CommitSHA != c3 {
		t.Fatalf("binding after the fix: %+v, %v", b, err)
	}
}

// ── AC 10: the sweep of stored workflow rows ──

// sweepFixture commits files at head, seeds rows and an idle state AT
// head (the early-return path), and returns a fresh instance.
func sweepFixture(t *testing.T, files map[string]string, rows func(head string) []store.WorkflowProjection, capability bool) (*mapStore, *recordingRouter, *Service, string) {
	t.Helper()
	repo := newScratchRepo(t)
	repo.write("README.md", "# r\n")
	for p, b := range files {
		repo.write(p, b)
	}
	head := repo.commit("head")
	ms := newMapStore()
	for _, r := range rows(head) {
		ms.putRow(r)
	}
	ms.setState(head, "idle")
	router := &recordingRouter{}
	var st store.Store = ms
	if capability {
		st = allRowsStore{ms}
	}
	return ms, router, NewService(repo.git, st, router, 0), head
}

func TestSweep_UpgradeCases(t *testing.T) {
	const older = "1111111111111111111111111111111111111111"

	t.Run("a_smuggled_active_row_with_smuggled_file_removed", func(t *testing.T) {
		ms, router, svc, _ := sweepFixture(t, map[string]string{wfPath: pocFoldWorkflow},
			func(string) []store.WorkflowProjection {
				return []store.WorkflowProjection{legacyRow(t, wfPath, pocFoldWorkflow, older)}
			}, true)
		if err := svc.IncrementalSync(context.Background()); err != nil {
			t.Fatal(err)
		}
		if ms.row(wfPath) != nil {
			t.Fatal("smuggled row must be removed")
		}
		assertOneRejection(t, router, "parse", "removed", "", false)
	})

	t.Run("b_smuggled_row_valid_file_replaced", func(t *testing.T) {
		ms, router, svc, head := sweepFixture(t, map[string]string{wfPath: reviewedWorkflow},
			func(string) []store.WorkflowProjection {
				return []store.WorkflowProjection{legacyRow(t, wfPath, pocFoldWorkflow, older)}
			}, true)
		if err := svc.IncrementalSync(context.Background()); err != nil {
			t.Fatal(err)
		}
		r := ms.row(wfPath)
		if r == nil || r.SourceCommit != head || !sameJSON(r.Definition, acceptedRow(t, wfPath, reviewedWorkflow, head).Definition) {
			t.Fatalf("row must be replaced by head's row, got %+v", r)
		}
		if rej := router.rejections(t); len(rej) != 0 {
			t.Fatalf("no rejection expected: %+v", rej)
		}
	})

	t.Run("c_active_row_absent_at_head_removed", func(t *testing.T) {
		ms, router, svc, _ := sweepFixture(t, map[string]string{},
			func(string) []store.WorkflowProjection {
				return []store.WorkflowProjection{acceptedRow(t, wfPath, reviewedWorkflow, older)}
			}, true)
		if err := svc.IncrementalSync(context.Background()); err != nil {
			t.Fatal(err)
		}
		if ms.row(wfPath) != nil {
			t.Fatal("row absent at head must be removed")
		}
		assertOneRejection(t, router, "absent_at_head", "removed", "", false)
	})

	t.Run("d_nested_rows_removed", func(t *testing.T) {
		logs := captureLogs(t)
		const nestedActive = "workflows/steps/x.yaml"
		const nestedEmpty = "workflows/steps/validate.yaml"
		ms, router, svc, _ := sweepFixture(t, map[string]string{nestedActive: reviewedWorkflow, nestedEmpty: stepProfile},
			func(string) []store.WorkflowProjection {
				active := acceptedRow(t, nestedActive, reviewedWorkflow, older)
				// SMP's shape: an empty-status row from the step profile.
				empty := store.WorkflowProjection{WorkflowPath: nestedEmpty, Name: "validate", AppliesTo: []byte("null"),
					Definition: []byte(`{"name":"validate","steps":[{"name":"go-build","run":"go build ./..."}]}`), SourceCommit: older}
				return []store.WorkflowProjection{active, empty}
			}, true)
		if err := svc.IncrementalSync(context.Background()); err != nil {
			t.Fatal(err)
		}
		if ms.row(nestedActive) != nil || ms.row(nestedEmpty) != nil {
			t.Fatal("nested rows must be removed")
		}
		rej := router.rejections(t)
		if len(rej) != 1 || rej[0]["workflow_path"] != nestedActive || rej[0]["check"] != "scope" || rej[0]["action"] != "removed" {
			t.Fatalf("expected exactly one scope rejection for the Active nested row, got %+v", rej)
		}
		if len(logs.lines("level=WARN", nestedEmpty)) != 0 {
			t.Fatalf("the non-Active nested row must not Warn:\n%s", logs.String())
		}
		if len(logs.lines("level=INFO", "removed non-active workflow row", nestedEmpty)) != 1 {
			t.Fatalf("expected one Info line for the non-Active nested row:\n%s", logs.String())
		}
	})

	t.Run("e_equal_row_untouched", func(t *testing.T) {
		ms, router, svc, _ := sweepFixture(t, map[string]string{wfPath: reviewedWorkflow},
			func(string) []store.WorkflowProjection {
				return []store.WorkflowProjection{acceptedRow(t, wfPath, reviewedWorkflow, older)}
			}, true)
		if err := svc.IncrementalSync(context.Background()); err != nil {
			t.Fatal(err)
		}
		if ms.upserts(wfPath) != 0 {
			t.Fatalf("an equal row must not be upserted (re-stamping strands pinned runs), got %d upserts", ms.upserts(wfPath))
		}
		if r := ms.row(wfPath); r.SourceCommit != older {
			t.Fatalf("source_commit changed to %s", r.SourceCommit)
		}
		if rej := router.rejections(t); len(rej) != 0 {
			t.Fatalf("no rejection expected: %+v", rej)
		}
	})

	t.Run("f_second_sync_does_not_sweep_again", func(t *testing.T) {
		ms, _, svc, _ := sweepFixture(t, map[string]string{wfPath: reviewedWorkflow},
			func(string) []store.WorkflowProjection {
				return []store.WorkflowProjection{acceptedRow(t, wfPath, reviewedWorkflow, older)}
			}, false)
		if err := svc.IncrementalSync(context.Background()); err != nil {
			t.Fatal(err)
		}
		if ms.listActiveCalls != 1 {
			t.Fatalf("first sync: %d listing calls, want 1", ms.listActiveCalls)
		}
		if err := svc.IncrementalSync(context.Background()); err != nil {
			t.Fatal(err)
		}
		if ms.listActiveCalls != 1 {
			t.Fatalf("second sync on the same instance swept again (%d listing calls)", ms.listActiveCalls)
		}
	})

	t.Run("g_deprecated_smuggled_row", func(t *testing.T) {
		deprecated := func(string) []store.WorkflowProjection {
			r := legacyRow(t, wfPath, pocFoldWorkflow, older)
			r.Status = "Deprecated"
			return []store.WorkflowProjection{r}
		}
		t.Run("with_all_rows_capability_removed", func(t *testing.T) {
			ms, router, svc, _ := sweepFixture(t, map[string]string{wfPath: pocFoldWorkflow}, deprecated, true)
			if err := svc.IncrementalSync(context.Background()); err != nil {
				t.Fatal(err)
			}
			if ms.row(wfPath) != nil {
				t.Fatal("a Deprecated smuggled row steers pinned runs and must be removed")
			}
			if ms.listAllCalls != 1 {
				t.Fatalf("expected the all-rows listing, got %d calls", ms.listAllCalls)
			}
			assertOneRejection(t, router, "parse", "removed", "", false)
		})
		t.Run("without_capability_survives_documented", func(t *testing.T) {
			// Fallback for stores without ListWorkflowProjections: only
			// Active rows are swept, so this row survives until its file
			// changes or a FullRebuild. *store.PostgresStore (Spine and
			// SMP) implements the capability.
			ms, _, svc, _ := sweepFixture(t, map[string]string{wfPath: pocFoldWorkflow}, deprecated, false)
			if err := svc.IncrementalSync(context.Background()); err != nil {
				t.Fatal(err)
			}
			if ms.row(wfPath) == nil {
				t.Fatal("documented fallback: the non-Active row is not visible to an Active-only sweep")
			}
		})
	})

	t.Run("h_concurrent_first_syncs_sweep_once", func(t *testing.T) {
		ms, _, svc, _ := sweepFixture(t, map[string]string{wfPath: reviewedWorkflow},
			func(string) []store.WorkflowProjection {
				return []store.WorkflowProjection{acceptedRow(t, wfPath, reviewedWorkflow, older)}
			}, true)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs[i] = svc.IncrementalSync(context.Background())
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		ms.mu.Lock()
		calls := ms.listAllCalls + ms.listActiveCalls
		ms.mu.Unlock()
		if calls != 1 {
			t.Fatalf("expected exactly one listing call across concurrent syncs, got %d", calls)
		}
	})

	t.Run("i_valid_row_with_rejected_file_quarantined", func(t *testing.T) {
		ms, router, svc, _ := sweepFixture(t, map[string]string{wfPath: pocFoldWorkflow},
			func(string) []store.WorkflowProjection {
				return []store.WorkflowProjection{acceptedRow(t, wfPath, reviewedWorkflow, older)}
			}, true)
		if err := svc.IncrementalSync(context.Background()); err != nil {
			t.Fatal(err)
		}
		r := ms.row(wfPath)
		if r == nil || r.Status != "Quarantined" || r.SourceCommit != older {
			t.Fatalf("expected the row quarantined with its source_commit, got %+v", r)
		}
		assertOneRejection(t, router, "parse", "kept", older, true)
	})
}

// A sweep failure holds the sync and is retried by the next tick.
func TestSweep_FailureMarksErrorAndRetries(t *testing.T) {
	ms, _, svc, head := sweepFixture(t, map[string]string{wfPath: pocFoldWorkflow},
		func(string) []store.WorkflowProjection {
			return []store.WorkflowProjection{legacyRow(t, wfPath, pocFoldWorkflow, "1111111111111111111111111111111111111111")}
		}, true)
	ms.deleteWorkflowErr[wfPath] = errSimulated
	err := svc.IncrementalSync(context.Background())
	if err == nil || !errors.Is(err, errSimulated) {
		t.Fatalf("expected the sweep error, got %v", err)
	}
	if got := ms.syncState(); got.Status != "error" || got.LastSyncedCommit != head || !strings.Contains(got.ErrorDetail, "reconcile workflow rows") {
		t.Fatalf("sync state: %+v", got)
	}
	delete(ms.deleteWorkflowErr, wfPath)
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ms.row(wfPath) != nil || ms.syncState().Status != "idle" {
		t.Fatalf("retry must complete the sweep: row=%+v state=%+v", ms.row(wfPath), ms.syncState())
	}
}
