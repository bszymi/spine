package domain

import "sort"

// Declared artifact-status lifecycle (INIT-023 TASK-017, audit P3-21).
//
// Runs, steps, divergences and branches have explicit state machines
// (core/workflow/*_machine.go); an artifact's status had only an
// allowed-values list (ValidStatusesForType) and governance prose
// (governance/task-lifecycle.md §4). Nothing said which status may follow
// which, so a governed merge published whatever status a run branch
// carried. StatusLifecycle is that missing machine, declared in code
// (Decision D1: a repository-held table could be rewritten by the very
// run branch it judges).
//
// The table is seeded so that every commit.status effect in Spine's and
// SMP's shipped workflow corpora is accepted (TASK-017 §7). Every terminal
// status — Completed, Cancelled, Rejected, Abandoned, Superseded,
// Deprecated — is final: it has no outbound edge. Task "In Progress" is
// declared although ValidStatusesForType(Task) does not list it, because
// SMP's task-default commits it (owner question 3); States(t) is always a
// superset of ValidStatusesForType(t), so no status that validates today
// becomes undeclared.

// StatusLifecycle declares, per Markdown artifact type, the statuses an
// artifact may be created with and the status transitions a governed change
// may make. Immutable; safe for concurrent use. A nil *StatusLifecycle is
// "no enforcement" wherever it is accepted as an option, and every method
// is nil-safe (a nil lifecycle declares nothing).
type StatusLifecycle struct {
	types map[ArtifactType]typeLifecycle
}

type typeLifecycle struct {
	states  map[ArtifactStatus]bool
	initial map[ArtifactStatus]bool
	edges   map[ArtifactStatus]map[ArtifactStatus]bool
}

// StatusTransition is one declared edge of the lifecycle.
type StatusTransition struct {
	From, To ArtifactStatus
}

// lifecycleSpec is the human-readable form the default table is built from.
type lifecycleSpec struct {
	initial []ArtifactStatus
	edges   []StatusTransition
}

func edgesFrom(from ArtifactStatus, to ...ArtifactStatus) []StatusTransition {
	out := make([]StatusTransition, 0, len(to))
	for _, t := range to {
		out = append(out, StatusTransition{From: from, To: t})
	}
	return out
}

func concatEdges(groups ...[]StatusTransition) []StatusTransition {
	var out []StatusTransition
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// defaultLifecycleSpecs is the §1 table of TASK-017. Changing it is a
// governance decision: the golden test in artifact_lifecycle_test.go and
// the table in architecture/engine-state-machine.md must change with it.
func defaultLifecycleSpecs() map[ArtifactType]lifecycleSpec {
	planned := lifecycleSpec{
		initial: []ArtifactStatus{StatusDraft, StatusPending},
		edges: concatEdges(
			edgesFrom(StatusDraft, StatusPending, StatusSuperseded),
			edgesFrom(StatusPending, StatusInProgress, StatusCompleted, StatusSuperseded),
			edgesFrom(StatusInProgress, StatusCompleted, StatusSuperseded),
		),
	}
	task := lifecycleSpec{
		initial: []ArtifactStatus{StatusDraft, StatusPending},
		edges: concatEdges(
			edgesFrom(StatusDraft, StatusPending, StatusCancelled),
			edgesFrom(StatusPending, StatusInProgress, StatusCompleted, StatusCancelled,
				StatusRejected, StatusSuperseded, StatusAbandoned),
			edgesFrom(StatusInProgress, StatusCompleted, StatusCancelled,
				StatusRejected, StatusSuperseded, StatusAbandoned),
		),
	}
	adr := lifecycleSpec{
		initial: []ArtifactStatus{StatusProposed},
		edges: concatEdges(
			edgesFrom(StatusProposed, StatusAccepted, StatusDeprecated, StatusSuperseded),
			edgesFrom(StatusAccepted, StatusDeprecated, StatusSuperseded),
		),
	}
	governance := lifecycleSpec{
		initial: []ArtifactStatus{StatusLivingDocument, StatusFoundational},
		edges: concatEdges(
			edgesFrom(StatusLivingDocument, StatusFoundational, StatusSuperseded),
			edgesFrom(StatusFoundational, StatusLivingDocument, StatusSuperseded),
		),
	}
	document := lifecycleSpec{
		initial: []ArtifactStatus{StatusLivingDocument, StatusStable},
		edges: concatEdges(
			edgesFrom(StatusLivingDocument, StatusStable, StatusSuperseded),
			edgesFrom(StatusStable, StatusLivingDocument, StatusSuperseded),
		),
	}
	return map[ArtifactType]lifecycleSpec{
		ArtifactTypeInitiative:   planned,
		ArtifactTypeEpic:         planned,
		ArtifactTypeTask:         task,
		ArtifactTypeADR:          adr,
		ArtifactTypeGovernance:   governance,
		ArtifactTypeArchitecture: document,
		ArtifactTypeProduct:      document,
	}
}

// defaultStatusLifecycle is built once; StatusLifecycle exposes no
// mutator, so sharing the instance is safe.
var defaultStatusLifecycle = buildStatusLifecycle(defaultLifecycleSpecs())

func buildStatusLifecycle(specs map[ArtifactType]lifecycleSpec) *StatusLifecycle {
	l := &StatusLifecycle{types: make(map[ArtifactType]typeLifecycle, len(specs))}
	for t, spec := range specs {
		tl := typeLifecycle{
			states:  map[ArtifactStatus]bool{},
			initial: map[ArtifactStatus]bool{},
			edges:   map[ArtifactStatus]map[ArtifactStatus]bool{},
		}
		for _, s := range spec.initial {
			tl.states[s] = true
			tl.initial[s] = true
		}
		for _, e := range spec.edges {
			tl.states[e.From] = true
			tl.states[e.To] = true
			if tl.edges[e.From] == nil {
				tl.edges[e.From] = map[ArtifactStatus]bool{}
			}
			tl.edges[e.From][e.To] = true
		}
		l.types[t] = tl
	}
	return l
}

// DefaultStatusLifecycle returns the declared lifecycle table (TASK-017
// §1). The returned value is shared and immutable.
func DefaultStatusLifecycle() *StatusLifecycle { return defaultStatusLifecycle }

// Types returns the artifact types the lifecycle declares, sorted.
func (l *StatusLifecycle) Types() []ArtifactType {
	if l == nil {
		return nil
	}
	out := make([]ArtifactType, 0, len(l.types))
	for t := range l.types {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// States returns the declared statuses of type t, sorted. An unknown type
// has none.
func (l *StatusLifecycle) States(t ArtifactType) []ArtifactStatus {
	if l == nil {
		return nil
	}
	return sortedStatuses(l.types[t].states)
}

// Initial returns the statuses an artifact of type t may be created with,
// sorted.
func (l *StatusLifecycle) Initial(t ArtifactType) []ArtifactStatus {
	if l == nil {
		return nil
	}
	return sortedStatuses(l.types[t].initial)
}

// IsState reports whether s is a declared status of type t.
func (l *StatusLifecycle) IsState(t ArtifactType, s ArtifactStatus) bool {
	if l == nil {
		return false
	}
	return l.types[t].states[s]
}

// IsInitial reports whether an artifact of type t may be created with s.
func (l *StatusLifecycle) IsInitial(t ArtifactType, s ArtifactStatus) bool {
	if l == nil {
		return false
	}
	return l.types[t].initial[s]
}

// Allows reports whether from -> to is a declared edge. Identity (from ==
// to) is allowed when from is a declared state. Unknown types and
// undeclared statuses are never allowed.
func (l *StatusLifecycle) Allows(t ArtifactType, from, to ArtifactStatus) bool {
	if l == nil {
		return false
	}
	tl, ok := l.types[t]
	if !ok || !tl.states[from] || !tl.states[to] {
		return false
	}
	if from == to {
		return true
	}
	return tl.edges[from][to]
}

// Transitions returns every declared edge of type t (identity excluded),
// sorted by From then To — for documentation and golden tests.
func (l *StatusLifecycle) Transitions(t ArtifactType) []StatusTransition {
	if l == nil {
		return nil
	}
	var out []StatusTransition
	for from, tos := range l.types[t].edges {
		for to := range tos {
			out = append(out, StatusTransition{From: from, To: to})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

func sortedStatuses(set map[ArtifactStatus]bool) []ArtifactStatus {
	if len(set) == 0 {
		return nil
	}
	out := make([]ArtifactStatus, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// WithoutTransition returns a copy of l without the edge from -> to of
// type t (l itself is unchanged). It exists for compatibility gates that
// prove a shipped workflow depends on a particular edge (TASK-017 AC 2):
// the gate must fail against the table minus that edge. A nil l returns
// nil.
func (l *StatusLifecycle) WithoutTransition(t ArtifactType, from, to ArtifactStatus) *StatusLifecycle {
	if l == nil {
		return nil
	}
	out := &StatusLifecycle{types: make(map[ArtifactType]typeLifecycle, len(l.types))}
	for typ, tl := range l.types {
		cp := typeLifecycle{
			states:  tl.states,
			initial: tl.initial,
			edges:   make(map[ArtifactStatus]map[ArtifactStatus]bool, len(tl.edges)),
		}
		for f, tos := range tl.edges {
			m := make(map[ArtifactStatus]bool, len(tos))
			for to2 := range tos {
				if typ == t && f == from && to2 == to {
					continue
				}
				m[to2] = true
			}
			cp.edges[f] = m
		}
		out.types[typ] = cp
	}
	return out
}
