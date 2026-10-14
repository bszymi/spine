package workflow_test

// INIT-023 TASK-017 AC 2: the compatibility gate. Every commit.status
// effect of both shipped corpora — Spine's workflows/*.yaml, parsed here,
// and SMP's, pinned as rows with their file:line (Spine CI does not check
// SMP out; SMP TASK-011 re-runs the gate against its live files) — passes
// ValidateCommitStatuses and the merge rule its row names.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
)

// mergeCheck is one merge-time transition a row's effect produces: "O1"
// (the run's existing artifact: Allows(typ, from, to)) or "O2" (an
// artifact the run creates, authored from: IsInitial(typ, from) and
// IsState(typ, to)).
type mergeCheck struct {
	rule     string
	typ      domain.ArtifactType
	from, to domain.ArtifactStatus
}

type corpusRow struct {
	corpus    string // "spine" or "smp"
	file      string // workflows/<file> (spine) or SMP file:line
	appliesTo []string
	statuses  []string // every commit.status outcome, in file order
	checks    []mergeCheck
}

const (
	sDraft   = domain.StatusDraft
	sPending = domain.StatusPending
	sInProg  = domain.StatusInProgress
	sDone    = domain.StatusCompleted
	sProp    = domain.StatusProposed
	sAcc     = domain.StatusAccepted
	sDep     = domain.StatusDeprecated
	sLiving  = domain.StatusLivingDocument
)

func o1(t domain.ArtifactType, from, to domain.ArtifactStatus) mergeCheck {
	return mergeCheck{"O1", t, from, to}
}

func o2(t domain.ArtifactType, authored, to domain.ArtifactStatus) mergeCheck {
	return mergeCheck{"O2", t, authored, to}
}

func each(rule string, types []domain.ArtifactType, from, to domain.ArtifactStatus) []mergeCheck {
	var out []mergeCheck
	for _, t := range types {
		out = append(out, mergeCheck{rule, t, from, to})
	}
	return out
}

var (
	planned  = []domain.ArtifactType{domain.ArtifactTypeInitiative, domain.ArtifactTypeEpic, domain.ArtifactTypeTask}
	document = []domain.ArtifactType{domain.ArtifactTypeGovernance, domain.ArtifactTypeArchitecture, domain.ArtifactTypeProduct}
	adr      = domain.ArtifactTypeADR
	epic     = domain.ArtifactTypeEpic
	task     = domain.ArtifactTypeTask
)

// corpusRows is the TASK-017 §7 table.
var corpusRows = []corpusRow{
	{"spine", "adr-creation.yaml", []string{"ADR"}, []string{"Accepted", "Deprecated"},
		[]mergeCheck{o2(adr, sProp, sAcc), o2(adr, sProp, sDep)}},
	{"spine", "adr.yaml", []string{"ADR"}, []string{"Accepted", "Deprecated"},
		[]mergeCheck{o1(adr, sProp, sAcc), o1(adr, sProp, sDep)}},
	{"spine", "artifact-creation.yaml", []string{"Initiative", "Epic", "Task"}, []string{"Pending"},
		each("O2", planned, sDraft, sPending)},
	{"spine", "document-creation.yaml", []string{"Governance", "Architecture", "Product"}, []string{"Living Document"},
		each("O2", document, sLiving, sLiving)},
	{"spine", "epic-lifecycle.yaml", []string{"Epic"}, []string{"In Progress", "Completed"},
		[]mergeCheck{o1(epic, sPending, sInProg), o1(epic, sPending, sDone), o1(epic, sInProg, sDone)}},
	{"spine", "task-default.yaml", []string{"Task"}, []string{"Completed"},
		[]mergeCheck{o1(task, sPending, sDone)}},
	{"spine", "task-spike.yaml", []string{"Task"}, []string{"Completed", "Completed"},
		[]mergeCheck{o1(task, sPending, sDone)}},
	{"spine", "workflow-lifecycle.yaml", []string{"Workflow"}, nil, nil}, // merge only; TaskPath .yaml skipped

	{"smp", "workflows/adr-creation.yaml:70-82", []string{"ADR"}, []string{"Accepted", "Deprecated"},
		[]mergeCheck{o2(adr, sProp, sAcc), o2(adr, sProp, sDep)}},
	// CreateEntry authors status: Draft (artifact_entry_helpers.go:160).
	{"smp", "workflows/artifact-creation.yaml:108-112", []string{"Initiative", "Epic", "Task"}, []string{"Pending"},
		each("O2", planned, sDraft, sPending)},
	// Authored Living Document (artifact_entry_helpers.go:288).
	{"smp", "workflows/document-creation.yaml:74-78", []string{"Governance", "Architecture", "Product"}, []string{"Living Document"},
		each("O2", document, sLiving, sLiving)},
	{"smp", "workflows/epic-lifecycle.yaml:30-34,66-70", []string{"Epic"}, []string{"In Progress", "Completed"},
		[]mergeCheck{o1(epic, sPending, sInProg), o1(epic, sPending, sDone), o1(epic, sInProg, sDone)}},
	// The merge applies execute.completed's In Progress; publish.published's
	// Completed is advanced after the merge and never applied (§7 note).
	{"smp", "workflows/task-default.yaml:41-45,155-159", []string{"Task"}, []string{"In Progress", "Completed"},
		[]mergeCheck{o1(task, sPending, sInProg)}},
	{"smp", "workflows/task-spike.yaml:30-34,78-82", []string{"Task"}, []string{"Completed", "Completed"},
		[]mergeCheck{o1(task, sPending, sDone)}},
}

func checkAccepted(l *domain.StatusLifecycle, c mergeCheck) bool {
	switch c.rule {
	case "O1":
		return l.Allows(c.typ, c.from, c.to)
	case "O2":
		return l.IsInitial(c.typ, c.from) && l.IsState(c.typ, c.to)
	}
	return false
}

// commitStatuses lists every outcome's commit.status, in file order.
func commitStatuses(wf *domain.WorkflowDefinition) []string {
	var out []string
	for _, s := range wf.Steps {
		for _, o := range s.Outcomes {
			if st := o.Commit["status"]; st != "" {
				out = append(out, st)
			}
		}
	}
	return out
}

// synthetic builds a workflow whose outcomes carry exactly the row's
// commit.status effects (an SMP row's file is not available here).
func synthetic(row corpusRow) *domain.WorkflowDefinition {
	wf := &domain.WorkflowDefinition{ID: "synthetic", AppliesTo: row.appliesTo,
		Steps: []domain.StepDefinition{{ID: "s"}}}
	for i, st := range row.statuses {
		wf.Steps[0].Outcomes = append(wf.Steps[0].Outcomes,
			domain.OutcomeDefinition{ID: fmt.Sprintf("o%d", i), Commit: map[string]string{"status": st}})
	}
	return wf
}

func TestCommitStatusCompat_SpineCorpus(t *testing.T) {
	l := domain.DefaultStatusLifecycle()
	files, err := filepath.Glob(filepath.Join("..", "..", "workflows", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]corpusRow{}
	for _, r := range corpusRows {
		if r.corpus == "spine" {
			rows[r.file] = r
		}
	}
	seen, matched := 0, 0
	var names []string
	for _, f := range files {
		name := filepath.Base(f)
		names = append(names, name)
		content, err := os.ReadFile(f) //nolint:gosec // G304: test reads the repository's own corpus
		if err != nil {
			t.Fatal(err)
		}
		wf, err := workflow.Parse("workflows/"+name, content)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if errs := workflow.ValidateCommitStatuses(wf, l); len(errs) != 0 {
			t.Errorf("%s: ValidateCommitStatuses = %v, want none", name, errs)
		}
		got := commitStatuses(wf)
		seen += len(got)
		row, ok := rows[name]
		if !ok {
			t.Errorf("%s: shipped workflow has no §7 row", name)
			continue
		}
		if strings.Join(got, "|") != strings.Join(row.statuses, "|") {
			t.Errorf("%s: commit.status outcomes = %v, the §7 row says %v", name, got, row.statuses)
		}
		if strings.Join(wf.AppliesTo, ",") != strings.Join(row.appliesTo, ",") {
			t.Errorf("%s: applies_to = %v, the §7 row says %v", name, wf.AppliesTo, row.appliesTo)
		}
		matched++
	}
	sort.Strings(names)
	if seen != 11 {
		t.Fatalf("saw %d commit.status outcomes in %v, want 11 (an empty glob must not pass)", seen, names)
	}
	if matched != len(rows) {
		t.Fatalf("matched %d of the %d Spine rows", matched, len(rows))
	}
}

func TestCommitStatusCompat_RowsPassTheMergeRules(t *testing.T) {
	l := domain.DefaultStatusLifecycle()
	smpOutcomes := 0
	for _, row := range corpusRows {
		if row.corpus == "smp" {
			smpOutcomes += len(row.statuses)
			if errs := workflow.ValidateCommitStatuses(synthetic(row), l); len(errs) != 0 {
				t.Errorf("SMP %s: ValidateCommitStatuses = %v", row.file, errs)
			}
		}
		for _, c := range row.checks {
			if !checkAccepted(l, c) {
				t.Errorf("%s %s: %s %s %s -> %s refused", row.corpus, row.file, c.rule, c.typ, c.from, c.to)
			}
		}
	}
	if smpOutcomes != 10 {
		t.Fatalf("SMP rows carry %d commit.status outcomes, want 10", smpOutcomes)
	}
}

// Deleting Task Pending -> In Progress from the table fails the SMP
// task-default row: the gate really depends on that edge.
func TestCommitStatusCompat_SMPTaskDefaultNeedsPendingToInProgress(t *testing.T) {
	l := domain.DefaultStatusLifecycle().WithoutTransition(domain.ArtifactTypeTask, sPending, sInProg)
	for _, row := range corpusRows {
		if row.corpus != "smp" || !strings.HasPrefix(row.file, "workflows/task-default.yaml") {
			continue
		}
		for _, c := range row.checks {
			if checkAccepted(l, c) {
				t.Fatalf("without Task Pending -> In Progress the SMP task-default row still passes (%+v)", c)
			}
		}
		return
	}
	t.Fatal("no SMP task-default row")
}
