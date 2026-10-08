package refadmit

import (
	"strings"
	"testing"
)

// TestClassify pins the admission class of every ref shape TASK-016
// AC 2 names, plus the design-check rows (DC-1 to DC-4) and the
// shadowing flag (TASK-002 D12). Mutation checks recorded in the task's
// Implementation Notes: replacing foldsToNamespace with a plain
// strings.ToLower comparison fails the U+017F row; removing the
// control-byte loop fails the three invalid rows; dropping the
// maxReservedShort bound fails the DC-4 row.
func TestClassify(t *testing.T) {
	long := "refs/heads/spine/run/" + strings.Repeat("a", 1020) + "-0a1b2c3d"
	cases := []struct {
		ref       string
		class     Class
		runID     string
		divPath   string
		short     string
		shadowing bool
	}{
		// AC 2 table.
		{ref: "refs/heads/main", class: ClassBranch, short: "main"},
		{ref: "refs/heads/feature/spine/run/x", class: ClassBranch, short: "feature/spine/run/x"},
		{ref: "refs/heads/spinefoo", class: ClassBranch, short: "spinefoo"},
		{ref: "refs/heads/spine", class: ClassReserved, short: "spine"},
		{ref: "refs/heads/SPINE/run/x-0a1b2c3d", class: ClassReserved, short: "SPINE/run/x-0a1b2c3d"},
		{ref: "refs/heads/Spine/init", class: ClassReserved},
		{ref: "refs/heads/spine/run/", class: ClassReserved},
		{ref: "refs/heads/spine/run/abc", class: ClassReserved},
		{ref: "refs/heads/spine/run/a/b-0a1b2c3d", class: ClassReserved},
		{ref: "refs/heads/spine/other", class: ClassReserved},
		{ref: "refs/heads/spine/init/x", class: ClassReserved},
		{ref: "refs/heads/spine/run/task-001-x-0a1b2c3d", class: ClassRunBranch, runID: "run-0a1b2c3d", short: "spine/run/task-001-x-0a1b2c3d"},
		{ref: "refs/heads/spine/plan/task-001-x-0a1b2c3d", class: ClassRunBranch, runID: "run-0a1b2c3d"},
		{ref: "refs/heads/spine/init", class: ClassInitBranch, short: "spine/init"},
		{ref: "refs/heads/spine/run-0a1b2c3d/run-0a1b2c3d-div-d/b1", class: ClassDivergenceBranch, runID: "run-0a1b2c3d", divPath: "run-0a1b2c3d-div-d/b1"},
		{ref: "refs/tags/spine/run/task-001-x-0a1b2c3d", class: ClassNonBranch, short: "refs/tags/spine/run/task-001-x-0a1b2c3d"},
		{ref: "refs/tags/main", class: ClassNonBranch, short: "refs/tags/main"},
		{ref: "refs/notes/commits", class: ClassNonBranch},
		{ref: "refs/spine/run/x", class: ClassNonBranch},
		{ref: "refs/remotes/origin/main", class: ClassNonBranch},
		{ref: "refs/HEADS/main", class: ClassNonBranch},
		{ref: "HEAD", class: ClassNonBranch, shadowing: true},

		// Design-check rows (DC-1 to DC-4, D/F blockers).
		{ref: "refs/heads/ſpine/run/task-001-x-0a1b2c3d", class: ClassReserved},
		{ref: "refs/heads/Spine/run/task-001-x-0a1b2c3d", class: ClassReserved},
		{ref: "refs/heads/spİne/run/task-001-x-0a1b2c3d", class: ClassReserved}, // ToLower-only alias
		{ref: "refs/heads/spine\x00", class: ClassInvalid},
		{ref: "refs/heads/spine/run/task-001-x-0a1b2c3d\x00", class: ClassInvalid},
		{ref: "refs/heads/main\x7f", class: ClassInvalid},
		{ref: "refs/heads/spine/init/x/y", class: ClassReserved},
		{ref: "refs/heads/spine/run", class: ClassReserved},
		{ref: "refs/heads/spine/plan", class: ClassReserved},
		{ref: "refs/heads/spine/run-0a1b2c3d", class: ClassReserved},
		{ref: long, class: ClassReserved},

		// Further shapes the rules imply.
		{ref: "refs/heads/spine/run/-0a1b2c3d", class: ClassRunBranch, runID: "run-0a1b2c3d"}, // empty id and slug
		{ref: "refs/heads/spine/run/x-", class: ClassReserved},
		{ref: "refs/heads/spine/run-0a1b2c3d/run-0a1b2c3d-div-d", class: ClassReserved},
		{ref: "refs/heads/spine/run-0a1b2c3d/run-0a1b2c3d-div-d/b/1", class: ClassDivergenceBranch, runID: "run-0a1b2c3d", divPath: "run-0a1b2c3d-div-d/b/1"},
		{ref: "refs/heads/spine/plan/x/y/z", class: ClassReserved},
		{ref: "refs/heads/ſpine", class: ClassReserved},

		// Shadowing flag (TASK-002 D12): a non-leading "refs" or any
		// "HEAD" component, exact case.
		{ref: "refs/heads/refs/heads/main", class: ClassBranch, shadowing: true},
		{ref: "refs/tags/refs/heads/main", class: ClassNonBranch, shadowing: true},
		{ref: "refs/refs/heads/main", class: ClassNonBranch, shadowing: true},
		{ref: "refs/remotes/refs/heads/main", class: ClassNonBranch, shadowing: true},
		{ref: "refs/heads/HEAD", class: ClassBranch, shadowing: true},
		{ref: "refs/tags/HEAD", class: ClassNonBranch, shadowing: true},
		{ref: "refs/heads/spine/refs/x", class: ClassReserved, shadowing: true},
		{ref: "refs/heads/feature/HEAD/x", class: ClassBranch, shadowing: true},
		{ref: "refs/heads/feature/refs-x", class: ClassBranch},
		{ref: "refs/heads/head", class: ClassBranch},
		{ref: "refs/heads/x/Refs", class: ClassBranch},
		{ref: "refs/heads/HEADS", class: ClassBranch},
	}
	for _, tc := range cases {
		got := Classify(tc.ref)
		if got.Class != tc.class {
			t.Errorf("Classify(%q).Class = %s, want %s", tc.ref, got.Class, tc.class)
		}
		if got.RunID != tc.runID {
			t.Errorf("Classify(%q).RunID = %q, want %q", tc.ref, got.RunID, tc.runID)
		}
		if got.DivergencePath != tc.divPath {
			t.Errorf("Classify(%q).DivergencePath = %q, want %q", tc.ref, got.DivergencePath, tc.divPath)
		}
		if tc.short != "" && got.Short != tc.short {
			t.Errorf("Classify(%q).Short = %q, want %q", tc.ref, got.Short, tc.short)
		}
		if got.Shadowing != tc.shadowing {
			t.Errorf("Classify(%q).Shadowing = %v, want %v", tc.ref, got.Shadowing, tc.shadowing)
		}
		if got.Full != tc.ref {
			t.Errorf("Classify(%q).Full = %q", tc.ref, got.Full)
		}
		if got.Class == ClassInvalid && got.Short != "" {
			t.Errorf("Classify(%q): an invalid ref must carry no Short to echo, got %q", tc.ref, got.Short)
		}
	}
}

// TestClassify_ReservedLengthBoundary pins DC-4's bound at exactly
// 1024 bytes of branch name: one byte more turns a run-branch shape
// into ClassReserved.
func TestClassify_ReservedLengthBoundary(t *testing.T) {
	suffix := "-0a1b2c3d"
	at := "spine/run/" + strings.Repeat("a", maxReservedShort-len("spine/run/")-len(suffix)) + suffix
	if len(at) != maxReservedShort {
		t.Fatalf("fixture length %d, want %d", len(at), maxReservedShort)
	}
	if c := Classify("refs/heads/" + at); c.Class != ClassRunBranch {
		t.Errorf("%d-byte run branch: class %s, want run_branch", len(at), c.Class)
	}
	over := "spine/run/a" + at[len("spine/run/"):]
	if c := Classify("refs/heads/" + over); c.Class != ClassReserved {
		t.Errorf("%d-byte run branch: class %s, want reserved", len(over), c.Class)
	}
}
