package engine

// INIT-023 TASK-005 AC 6 and the pure parts of AC 5 / DC-9: reserved
// commit_meta keys, the merge of a completion into commit_meta, and the
// bound_by provenance check.

import (
	"fmt"
	"reflect"
	"testing"
)

func TestStripReserved(t *testing.T) {
	in := map[string]string{
		"status":                 "Completed",
		"spine.bound_sha":        "attacker",
		"Spine.Bound_By":         "x",
		"SPINE.repo.a.bound_sha": "y",
		"spinex":                 "kept: no dot",
		"my.spine.key":           "kept: not a prefix",
	}
	snapshot := fmt.Sprint(in)
	got, dropped := stripReserved(in)
	want := map[string]string{"status": "Completed", "spinex": "kept: no dot", "my.spine.key": "kept: not a prefix"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stripReserved = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(dropped, []string{"SPINE.repo.a.bound_sha", "Spine.Bound_By", "spine.bound_sha"}) {
		t.Fatalf("dropped = %v", dropped)
	}
	if fmt.Sprint(in) != snapshot {
		t.Fatal("stripReserved modified its input")
	}
	if got, dropped := stripReserved(nil); got != nil || dropped != nil {
		t.Fatalf("stripReserved(nil) = %v, %v", got, dropped)
	}
}

func TestMergeCommitMeta(t *testing.T) {
	const h1, h2 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	current := map[string]string{
		"status":                  "In Progress",
		"stale":                   "from an earlier outcome",
		CommitMetaBoundSHA:        h1,
		CommitMetaBoundBy:         "step:execute:run-1-execute-1",
		"spine.head.execute":      h1,
		RepoBoundSHAKey("code-a"): h1,
	}
	hb := &headBinding{head: h2, by: "step:review:run-1-review-1", stepID: "review",
		repoHeads: map[string]string{"code-a": h2}, replaceRepoKeys: true}
	cases := []struct {
		name          string
		current       map[string]string
		outcome       map[string]string
		hasCommit, on bool
		hb            *headBinding
		want          map[string]string
		write         bool
	}{
		{name: "binding off, commit: exactly the stripped outcome", current: current,
			outcome: map[string]string{"status": "Completed", "spine.bound_sha": "evil"}, hasCommit: true,
			want: map[string]string{"status": "Completed"}, write: true},
		{name: "binding off, no commit: no write", current: current, want: nil, write: false},
		{name: "binding off ignores hb", current: current, hb: hb, want: nil, write: false},
		{name: "binding on, completion without commit keeps non-reserved keys and updates reserved",
			current: current, on: true, hb: hb,
			want: map[string]string{
				"status": "In Progress", "stale": "from an earlier outcome",
				CommitMetaBoundSHA: h2, CommitMetaBoundBy: "step:review:run-1-review-1",
				"spine.head.execute": h1, "spine.head.review": h2,
				RepoBoundSHAKey("code-a"): h2,
			}, write: true},
		{name: "binding on, commit replaces non-reserved keys (stale dropped), reserved kept",
			current: current, on: true, hasCommit: true,
			outcome: map[string]string{"status": "Completed", "Spine.Bound_SHA": "evil"},
			want: map[string]string{
				"status":           "Completed",
				CommitMetaBoundSHA: h1, CommitMetaBoundBy: "step:execute:run-1-execute-1",
				"spine.head.execute": h1, RepoBoundSHAKey("code-a"): h1,
			}, write: true},
		{name: "binding on, no commit, no capture: no write", current: current, on: true, want: nil, write: false},
		{name: "binding on, step capture replaces stale repo keys",
			current: map[string]string{RepoBoundSHAKey("gone"): h1}, on: true,
			hb:    &headBinding{head: h2, by: "step:s:run-1-s-1", stepID: "s", replaceRepoKeys: true},
			want:  map[string]string{CommitMetaBoundSHA: h2, CommitMetaBoundBy: "step:s:run-1-s-1", "spine.head.s": h2},
			write: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			curSnap, outSnap := fmt.Sprint(tc.current), fmt.Sprint(tc.outcome)
			curPtr := reflect.ValueOf(tc.current).Pointer()
			got, write := mergeCommitMeta(tc.current, tc.outcome, tc.hasCommit, tc.on, tc.hb)
			if write != tc.write || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mergeCommitMeta = %v, %v; want %v, %v", got, write, tc.want, tc.write)
			}
			if fmt.Sprint(tc.current) != curSnap || fmt.Sprint(tc.outcome) != outSnap {
				t.Fatal("mergeCommitMeta modified an input map")
			}
			if got != nil && tc.current != nil && reflect.ValueOf(got).Pointer() == curPtr {
				t.Fatal("mergeCommitMeta returned the current map itself")
			}
		})
	}
}

func TestWithEngineBinding(t *testing.T) {
	const h1, h2 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	current := map[string]string{"status": "Completed", CommitMetaBoundSHA: h1, CommitMetaBoundBy: "step:a:run-1-a-1",
		RepoBoundSHAKey("x"): h1}
	snap := fmt.Sprint(current)
	got := withEngineBinding(current, &headBinding{head: h2, by: "convergence:run-1:d1"})
	want := map[string]string{"status": "Completed", CommitMetaBoundSHA: h2, CommitMetaBoundBy: "convergence:run-1:d1",
		RepoBoundSHAKey("x"): h1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("withEngineBinding = %v, want %v", got, want)
	}
	if fmt.Sprint(current) != snap {
		t.Fatal("withEngineBinding modified its input")
	}
	got = withEngineBinding(current, &headBinding{head: h2, by: "convergence:run-1:d1", replaceRepoKeys: true})
	if _, ok := got[RepoBoundSHAKey("x")]; ok {
		t.Fatal("replaceRepoKeys kept a code repository key")
	}
}

func TestBoundByNamesRun(t *testing.T) {
	cases := []struct {
		by   string
		want bool
	}{
		{"step:review:run-abc12345-review-1", true},
		{"step:execute:run-abc12345-execute-3", true},
		{"convergence:run-abc12345:div-1", true},
		{"merge_recovery:run-abc12345:merge_recovery_retry", true},
		{"", false},
		{"x", false},
		{"step:review:run-deadbeef-review-1", false},  // another run
		{"step:review:run-abc12345", false},           // no execution suffix
		{"step::run-abc12345-review-1", false},        // no step
		{"step:review:run-abc123456-review-1", false}, // a longer run id is not this run
		{"convergence:run-deadbeef:div-1", false},     // another run
		{"convergence:run-abc12345:", false},          // no divergence
		{"merge_recovery:run-abc12345", false},        // no operation
		{"manual:run-abc12345:x", false},              // unknown kind
		{"STEP:review:run-abc12345-review-1", false},  // kinds are exact
	}
	for _, tc := range cases {
		if got := boundByNamesRun(tc.by, "run-abc12345"); got != tc.want {
			t.Errorf("boundByNamesRun(%q) = %v, want %v", tc.by, got, tc.want)
		}
	}
	if boundByNamesRun("step:review:-review-1", "") {
		t.Error("an empty run id must never match")
	}
}

func TestIsAdvancingOutcomeAndGates(t *testing.T) {
	wf := testBindingWorkflow()
	cases := []struct {
		step, outcome string
		want          bool
	}{
		{"execute", "done", true},       // → review (gate)
		{"validate", "passed", true},    // → review
		{"validate", "failed", false},   // → execute (producer)
		{"review", "accepted", true},    // → publish (internal)
		{"review", "rework", false},     // → execute
		{"review", "reject", false},     // end without commit
		{"review", "approve_end", true}, // end with commit
		{"review", "to_unknown", true},  // unknown next step: fail closed
		{"converge", "ok", true},        // → review
	}
	for _, tc := range cases {
		sd := findStepDef(wf, tc.step)
		o := findOutcome(sd, tc.outcome)
		if o == nil {
			t.Fatalf("fixture: %s/%s missing", tc.step, tc.outcome)
		}
		if got := isAdvancingOutcome(wf, o); got != tc.want {
			t.Errorf("isAdvancingOutcome(%s/%s) = %v, want %v", tc.step, tc.outcome, got, tc.want)
		}
	}
	for step, gate := range map[string]bool{"execute": false, "validate": false, "review": true, "converge": true, "publish": false} {
		if got := isGateStep(findStepDef(wf, step)); got != gate {
			t.Errorf("isGateStep(%s) = %v, want %v", step, got, gate)
		}
	}
}
