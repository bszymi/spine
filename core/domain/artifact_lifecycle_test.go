package domain_test

// INIT-023 TASK-017 AC 1: the declared artifact-status lifecycle table.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
)

type tr = domain.StatusTransition

func edges(from domain.ArtifactStatus, to ...domain.ArtifactStatus) []tr {
	var out []tr
	for _, t := range to {
		out = append(out, tr{From: from, To: t})
	}
	return out
}

func cat(gs ...[]tr) []tr {
	var out []tr
	for _, g := range gs {
		out = append(out, g...)
	}
	return out
}

const (
	draft   = domain.StatusDraft
	pending = domain.StatusPending
	inProg  = domain.StatusInProgress
	done    = domain.StatusCompleted
	superd  = domain.StatusSuperseded
	cancel  = domain.StatusCancelled
	reject  = domain.StatusRejected
	abandon = domain.StatusAbandoned
	propose = domain.StatusProposed
	accept  = domain.StatusAccepted
	deprec  = domain.StatusDeprecated
	living  = domain.StatusLivingDocument
	found   = domain.StatusFoundational
	stable  = domain.StatusStable
	untest  = domain.StatusUntested
	support = domain.StatusSupported
	contra  = domain.StatusContradicted
)

// golden is the §1 table of TASK-017, written out edge by edge in sorted
// order (From, then To). It is also the table in
// architecture/engine-state-machine.md "Artifact status lifecycle".
var golden = map[domain.ArtifactType]struct {
	initial []domain.ArtifactStatus
	edges   []tr
}{
	domain.ArtifactTypeInitiative: {
		initial: []domain.ArtifactStatus{draft, pending},
		edges: cat(
			edges(draft, pending, superd),
			edges(inProg, done, superd),
			edges(pending, done, inProg, superd),
		),
	},
	domain.ArtifactTypeEpic: {
		initial: []domain.ArtifactStatus{draft, pending},
		edges: cat(
			edges(draft, pending, superd),
			edges(inProg, done, superd),
			edges(pending, done, inProg, superd),
		),
	},
	domain.ArtifactTypeTask: {
		initial: []domain.ArtifactStatus{draft, pending},
		edges: cat(
			edges(draft, cancel, pending),
			edges(inProg, abandon, cancel, done, reject, superd),
			edges(pending, abandon, cancel, done, inProg, reject, superd),
		),
	},
	domain.ArtifactTypeADR: {
		initial: []domain.ArtifactStatus{propose},
		edges: cat(
			edges(accept, deprec, superd),
			edges(propose, accept, deprec, superd),
		),
	},
	// ADR-017 §2.
	domain.ArtifactTypeAssumption: {
		initial: []domain.ArtifactStatus{propose, untest},
		edges: cat(
			edges(contra, superd, support, untest),
			edges(propose, contra, reject, support, untest),
			edges(support, contra, superd, untest),
			edges(untest, contra, superd, support),
		),
	},
	domain.ArtifactTypeGovernance: {
		initial: []domain.ArtifactStatus{found, living},
		edges: cat(
			edges(found, living, superd),
			edges(living, found, superd),
		),
	},
	domain.ArtifactTypeArchitecture: {
		initial: []domain.ArtifactStatus{living, stable},
		edges: cat(
			edges(living, stable, superd),
			edges(stable, living, superd),
		),
	},
	domain.ArtifactTypeProduct: {
		initial: []domain.ArtifactStatus{living, stable},
		edges: cat(
			edges(living, stable, superd),
			edges(stable, living, superd),
		),
	},
}

func TestDefaultStatusLifecycle_Golden(t *testing.T) {
	l := domain.DefaultStatusLifecycle()
	if got := len(l.Types()); got != len(golden) {
		t.Fatalf("Types() = %v, want the %d golden types", l.Types(), len(golden))
	}
	for typ, want := range golden {
		if got := l.Initial(typ); !reflect.DeepEqual(got, want.initial) {
			t.Errorf("%s: Initial = %v, want %v", typ, got, want.initial)
		}
		if got := l.Transitions(typ); !reflect.DeepEqual(got, want.edges) {
			t.Errorf("%s: Transitions =\n  %v\nwant\n  %v", typ, got, want.edges)
		}
	}
}

func TestDefaultStatusLifecycle_Invariants(t *testing.T) {
	l := domain.DefaultStatusLifecycle()

	t.Run("types", func(t *testing.T) {
		have := map[domain.ArtifactType]bool{}
		for _, typ := range l.Types() {
			have[typ] = true
		}
		for _, typ := range domain.ValidMarkdownArtifactTypes() {
			if !have[typ] {
				t.Errorf("type %s missing", typ)
			}
		}
		if have[domain.ArtifactTypeWorkflow] {
			t.Error("Workflow must not be in the table: workflow definitions carry no status")
		}
	})

	t.Run("states cover ValidStatusesForType", func(t *testing.T) {
		for _, typ := range domain.ValidMarkdownArtifactTypes() {
			for _, s := range domain.ValidStatusesForType(typ) {
				if !l.IsState(typ, s) {
					t.Errorf("%s: %q validates today but is not declared", typ, s)
				}
			}
		}
	})

	t.Run("every state reachable from an initial state", func(t *testing.T) {
		for _, typ := range l.Types() {
			reach := map[domain.ArtifactStatus]bool{}
			var queue []domain.ArtifactStatus
			for _, s := range l.Initial(typ) {
				reach[s] = true
				queue = append(queue, s)
			}
			for len(queue) > 0 {
				s := queue[0]
				queue = queue[1:]
				for _, e := range l.Transitions(typ) {
					if e.From == s && !reach[e.To] {
						reach[e.To] = true
						queue = append(queue, e.To)
					}
				}
			}
			for _, s := range l.States(typ) {
				if !reach[s] {
					t.Errorf("%s: %q is unreachable", typ, s)
				}
			}
		}
	})

	t.Run("terminal statuses are final", func(t *testing.T) {
		final := []domain.ArtifactStatus{done, cancel, reject, abandon, superd, deprec}
		for _, typ := range l.Types() {
			for _, e := range l.Transitions(typ) {
				for _, f := range final {
					if e.From == f {
						t.Errorf("%s: final status %q has an outbound edge to %q", typ, f, e.To)
					}
				}
			}
			for _, f := range final {
				for _, to := range l.States(typ) {
					if to != f && l.Allows(typ, f, to) {
						t.Errorf("%s: Allows(%q -> %q)", typ, f, to)
					}
				}
			}
		}
	})

	t.Run("identity only for declared statuses", func(t *testing.T) {
		for _, typ := range l.Types() {
			for _, s := range l.States(typ) {
				if !l.Allows(typ, s, s) {
					t.Errorf("%s: identity %q refused", typ, s)
				}
			}
			for _, s := range []domain.ArtifactStatus{"Done", "Active", "", "completed", "Draft for Review"} {
				if l.Allows(typ, s, s) {
					t.Errorf("%s: identity of undeclared %q allowed", typ, s)
				}
			}
		}
		if l.Allows(domain.ArtifactTypeADR, done, done) {
			t.Error("ADR: Completed is not an ADR status")
		}
	})

	t.Run("unknown type never allowed", func(t *testing.T) {
		for _, typ := range []domain.ArtifactType{"Taskx", domain.ArtifactTypeWorkflow, ""} {
			if l.Allows(typ, pending, done) || l.Allows(typ, pending, pending) ||
				l.IsState(typ, pending) || l.IsInitial(typ, pending) {
				t.Errorf("unknown type %q is allowed something", typ)
			}
			if l.States(typ) != nil || l.Initial(typ) != nil || l.Transitions(typ) != nil {
				t.Errorf("unknown type %q declares something", typ)
			}
		}
	})

	t.Run("nil lifecycle declares nothing", func(t *testing.T) {
		var nl *domain.StatusLifecycle
		if nl.Allows(domain.ArtifactTypeTask, pending, done) || nl.IsState(domain.ArtifactTypeTask, pending) ||
			nl.IsInitial(domain.ArtifactTypeTask, draft) || nl.Types() != nil || nl.States(domain.ArtifactTypeTask) != nil ||
			nl.Transitions(domain.ArtifactTypeTask) != nil {
			t.Error("a nil lifecycle must declare nothing")
		}
	})
}

func TestDefaultStatusLifecycle_Prohibited(t *testing.T) {
	l := domain.DefaultStatusLifecycle()
	for _, c := range []struct {
		typ      domain.ArtifactType
		from, to domain.ArtifactStatus
	}{
		{domain.ArtifactTypeTask, done, pending},
		{domain.ArtifactTypeTask, cancel, pending},
		{domain.ArtifactTypeTask, draft, done},
		{domain.ArtifactTypeTask, draft, superd},
		{domain.ArtifactTypeTask, abandon, inProg},
		{domain.ArtifactTypeADR, accept, propose},
		{domain.ArtifactTypeADR, propose, done},
		{domain.ArtifactTypeEpic, done, inProg},
		{domain.ArtifactTypeInitiative, done, pending},
		{domain.ArtifactTypeGovernance, superd, living},
		{domain.ArtifactTypeArchitecture, living, found},
		// ADR-017 §2: nothing returns to Proposed, a dismissed or retired
		// assumption stays so, and Proposed cannot be retired unconfirmed.
		{domain.ArtifactTypeAssumption, untest, propose},
		{domain.ArtifactTypeAssumption, support, propose},
		{domain.ArtifactTypeAssumption, reject, untest},
		{domain.ArtifactTypeAssumption, superd, untest},
		{domain.ArtifactTypeAssumption, propose, superd},
		{domain.ArtifactTypeAssumption, untest, reject},
		{domain.ArtifactTypeAssumption, propose, accept},
	} {
		if l.Allows(c.typ, c.from, c.to) {
			t.Errorf("%s %s -> %s must be prohibited", c.typ, c.from, c.to)
		}
	}
	// The corpus shapes (§7) are allowed.
	for _, c := range []struct {
		typ      domain.ArtifactType
		from, to domain.ArtifactStatus
	}{
		{domain.ArtifactTypeTask, pending, inProg}, // SMP task-default
		{domain.ArtifactTypeTask, pending, done},
		{domain.ArtifactTypeEpic, pending, inProg},
		{domain.ArtifactTypeEpic, inProg, done},
		{domain.ArtifactTypeEpic, pending, done},
		{domain.ArtifactTypeADR, propose, accept},
		{domain.ArtifactTypeADR, propose, deprec},
		{domain.ArtifactTypeInitiative, draft, pending},
		{domain.ArtifactTypeAssumption, propose, untest},
		{domain.ArtifactTypeAssumption, propose, reject},
		{domain.ArtifactTypeAssumption, support, untest},
		{domain.ArtifactTypeAssumption, contra, support},
	} {
		if !l.Allows(c.typ, c.from, c.to) {
			t.Errorf("%s %s -> %s must be allowed", c.typ, c.from, c.to)
		}
	}
}

func TestStatusLifecycle_WithoutTransition(t *testing.T) {
	l := domain.DefaultStatusLifecycle()
	m := l.WithoutTransition(domain.ArtifactTypeTask, pending, inProg)
	if m.Allows(domain.ArtifactTypeTask, pending, inProg) {
		t.Fatal("the removed edge is still allowed")
	}
	if !l.Allows(domain.ArtifactTypeTask, pending, inProg) {
		t.Fatal("WithoutTransition changed the original")
	}
	if !m.Allows(domain.ArtifactTypeTask, pending, done) || !m.Allows(domain.ArtifactTypeEpic, pending, inProg) ||
		!m.IsState(domain.ArtifactTypeTask, inProg) {
		t.Fatal("WithoutTransition removed more than one edge")
	}
	if got, want := len(m.Transitions(domain.ArtifactTypeTask)), len(l.Transitions(domain.ArtifactTypeTask))-1; got != want {
		t.Fatalf("Task edges = %d, want %d", got, want)
	}
}

// TestStatusLifecycleDocTable pins the table in
// architecture/engine-state-machine.md §5A to DefaultStatusLifecycle
// (TASK-017 AC 16): a table edit must change the doc, and the doc cannot
// drift.
func TestStatusLifecycleDocTable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "architecture", "engine-state-machine.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	begin := strings.Index(doc, "<!-- status-lifecycle-table:begin")
	end := strings.Index(doc, "<!-- status-lifecycle-table:end -->")
	if begin < 0 || end < begin {
		t.Fatal("status-lifecycle table markers not found")
	}
	got := map[string][2]string{}
	for _, line := range strings.Split(doc[begin:end], "\n") {
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| Type ") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "| "), " | ")
		if len(cells) != 3 {
			t.Fatalf("malformed row %q", line)
		}
		got[cells[0]] = [2]string{cells[1], cells[2]}
	}
	l := domain.DefaultStatusLifecycle()
	if len(got) != len(l.Types()) {
		t.Fatalf("doc rows %v, want one per type %v", got, l.Types())
	}
	for _, typ := range l.Types() {
		var initial, edges []string
		for _, s := range l.Initial(typ) {
			initial = append(initial, string(s))
		}
		for _, e := range l.Transitions(typ) {
			edges = append(edges, string(e.From)+" → "+string(e.To))
		}
		want := [2]string{strings.Join(initial, ", "), strings.Join(edges, ", ")}
		if got[string(typ)] != want {
			t.Errorf("%s: doc row %q, want %q", typ, got[string(typ)], want)
		}
	}
}
