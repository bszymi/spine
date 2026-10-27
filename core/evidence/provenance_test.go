package evidence_test

// INIT-024 TASK-007, review finding N-17: run.status showed evidence read
// from the author-writable run branch as fact ("checks passed"). Every
// present repository now records the ref it was read from and whether that
// is the authoritative branch; anything else is unverified.

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/evidence"
)

func TestRunSummary_BranchEvidenceMarkedUnverified(t *testing.T) {
	const branch = "spine/run/run-1-abc"

	type want struct {
		present  bool
		verified bool
		source   string
	}
	cases := []struct {
		name       string
		status     domain.RunStatus
		branchName string
		primary    string // Querier.WithPrimaryBranch; "" = default main
		files      map[string]bool
		want       want
	}{
		{
			name: "active run, evidence on the run branch only", status: domain.RunStatusActive, branchName: branch,
			files: map[string]bool{branch: true},
			want:  want{present: true, verified: false, source: branch},
		},
		{
			name: "active run, evidence on both: the run branch copy is shown, unverified", status: domain.RunStatusActive, branchName: branch,
			files: map[string]bool{branch: true, "main": true},
			want:  want{present: true, verified: false, source: branch},
		},
		{
			name: "active run, evidence already merged to main", status: domain.RunStatusActive, branchName: branch,
			files: map[string]bool{"main": true},
			want:  want{present: true, verified: true, source: "main"},
		},
		{
			name: "completed run, evidence on main", status: domain.RunStatusCompleted, branchName: branch,
			files: map[string]bool{"main": true, branch: true},
			want:  want{present: true, verified: true, source: "main"},
		},
		{
			name: "failed run, evidence only on the preserved run branch", status: domain.RunStatusFailed, branchName: branch,
			files: map[string]bool{branch: true},
			want:  want{present: true, verified: false, source: branch},
		},
		{
			name: "non-default authoritative branch", status: domain.RunStatusActive, branchName: branch, primary: "trunk",
			files: map[string]bool{"trunk": true, "main": true},
			want:  want{present: true, verified: true, source: "trunk"},
		},
		{
			name: "a run branch named like the authoritative branch is never verified", status: domain.RunStatusActive, branchName: "main",
			files: map[string]bool{"main": true},
			want:  want{present: true, verified: false, source: "main"},
		},
		{
			name: "no evidence anywhere", status: domain.RunStatusActive, branchName: branch,
			files: map[string]bool{},
			want:  want{present: false, verified: false, source: ""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{}
			for ref := range tc.files {
				files[ref+"\t"+evidence.EvidencePath("run-1", "spine")] = fixtureEvidence(t, "run-1", "spine", nil)
			}
			q := evidence.NewQuerier(newFakeReaderWithFiles(files))
			if tc.primary != "" {
				q = q.WithPrimaryBranch(tc.primary)
			}
			run := &domain.Run{RunID: "run-1", Status: tc.status, BranchName: tc.branchName, AffectedRepositories: []string{"spine"}}
			got, err := q.SummarizeForRun(context.Background(), run)
			if err != nil || got == nil {
				t.Fatalf("SummarizeForRun: %v, %v", got, err)
			}
			r := got.Repositories[0]
			if r.Present != tc.want.present || r.Verified != tc.want.verified || r.SourceRef != tc.want.source {
				t.Fatalf("present=%v verified=%v source_ref=%q, want present=%v verified=%v source_ref=%q",
					r.Present, r.Verified, r.SourceRef, tc.want.present, tc.want.verified, tc.want.source)
			}
			var wantUnverified []string
			if tc.want.present && !tc.want.verified {
				wantUnverified = []string{"spine"}
			}
			if !reflect.DeepEqual(got.UnverifiedRepositories, wantUnverified) {
				t.Errorf("unverified_repositories = %v, want %v", got.UnverifiedRepositories, wantUnverified)
			}
			// The marker is explicit on the wire: "verified": false is
			// serialized, never omitted.
			raw, _ := json.Marshal(r)
			if !strings.Contains(string(raw), `"verified":`+map[bool]string{true: "true", false: "false"}[tc.want.verified]) {
				t.Errorf("JSON lacks an explicit verified field: %s", raw)
			}
		})
	}
}

// A direct BuildSummary caller gets the default authority: only the named
// authoritative branch is verified, never the symbolic HEAD.
func TestBuildSummary_ProvenanceUsesDefaultAuthority(t *testing.T) {
	files := map[string][]byte{
		keyFor("HEAD", "run-1", "api"):   fixtureEvidence(t, "run-1", "api", nil),
		keyFor("main", "run-1", "spine"): fixtureEvidence(t, "run-1", "spine", nil),
	}
	run := &domain.Run{RunID: "run-1", Status: domain.RunStatusCompleted, AffectedRepositories: []string{"spine", "api"}}
	got, err := evidence.BuildSummary(context.Background(), newFakeReaderWithFiles(files), []string{"HEAD", "main"}, run)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]evidence.RepositorySummary{}
	for _, r := range got.Repositories {
		by[r.RepositoryID] = r
	}
	if r := by["api"]; r.Verified || r.SourceRef != "HEAD" {
		t.Errorf("api read from HEAD: verified=%v source_ref=%q, want unverified from HEAD", r.Verified, r.SourceRef)
	}
	if r := by["spine"]; !r.Verified || r.SourceRef != "main" {
		t.Errorf("spine read from main: verified=%v source_ref=%q, want verified from main", r.Verified, r.SourceRef)
	}
	if !reflect.DeepEqual(got.UnverifiedRepositories, []string{"api"}) {
		t.Errorf("unverified_repositories = %v, want [api]", got.UnverifiedRepositories)
	}
	// The headline still rolls up every record (passed): the list is what
	// says part of it is the author's claim.
	if got.Status != evidence.RunEvidencePassed {
		t.Errorf("status = %s, want passed", got.Status)
	}
}
