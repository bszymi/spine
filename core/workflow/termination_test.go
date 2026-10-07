package workflow

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// INIT-023 TASK-011 (codex review P1): projection runs workflow.Validate
// on every committed workflow file, so termination analysis must be
// bounded. canReachEnd used to enumerate simple paths (exponential on a
// chain of two-outcome steps); it is now a linear graph search.

// referenceCanReachEnd is the pre-fix implementation, kept verbatim as
// the oracle for the equivalence test below.
func referenceCanReachEnd(stepID string, stepMap map[string]*domain.StepDefinition, divMap map[string]*domain.DivergenceDefinition, convMap map[string]*domain.ConvergenceDefinition, visited map[string]bool) bool {
	if stepID == "end" {
		return true
	}
	if visited[stepID] {
		return false
	}
	visited[stepID] = true
	step, ok := stepMap[stepID]
	if !ok {
		return false
	}
	cp := func(m map[string]bool) map[string]bool {
		c := make(map[string]bool, len(m))
		for k, v := range m {
			c[k] = v
		}
		return c
	}
	for j := range step.Outcomes {
		if referenceCanReachEnd(step.Outcomes[j].NextStep, stepMap, divMap, convMap, cp(visited)) {
			return true
		}
	}
	if step.Diverge != "" {
		if div, ok := divMap[step.Diverge]; ok {
			for _, branch := range div.Branches {
				if branch.StartStep != "" {
					if referenceCanReachEnd(branch.StartStep, stepMap, divMap, convMap, cp(visited)) {
						return true
					}
				}
			}
		}
	}
	if step.Converge != "" {
		if conv, ok := convMap[step.Converge]; ok {
			if conv.EvaluationStep != "" {
				if referenceCanReachEnd(conv.EvaluationStep, stepMap, divMap, convMap, cp(visited)) {
					return true
				}
			}
		}
	}
	return false
}

func randomWorkflowGraph(r *rand.Rand) *domain.WorkflowDefinition {
	n := 1 + r.Intn(7)
	target := func() string {
		switch k := r.Intn(n + 3); {
		case k < n:
			return fmt.Sprintf("s%d", k)
		case k == n:
			return "end"
		case k == n+1:
			return "missing"
		default:
			return ""
		}
	}
	wf := &domain.WorkflowDefinition{EntryStep: target()}
	for i := 0; i < 2; i++ {
		div := domain.DivergenceDefinition{ID: fmt.Sprintf("d%d", i)}
		for b := 0; b < r.Intn(3); b++ {
			div.Branches = append(div.Branches, domain.BranchDefinition{ID: fmt.Sprintf("b%d", b), StartStep: target()})
		}
		wf.DivergencePoints = append(wf.DivergencePoints, div)
		wf.ConvergencePoints = append(wf.ConvergencePoints, domain.ConvergenceDefinition{ID: fmt.Sprintf("c%d", i), EvaluationStep: target()})
	}
	for i := 0; i < n; i++ {
		st := domain.StepDefinition{ID: fmt.Sprintf("s%d", i)}
		for o := 0; o < r.Intn(3); o++ {
			st.Outcomes = append(st.Outcomes, domain.OutcomeDefinition{ID: fmt.Sprintf("o%d", o), NextStep: target()})
		}
		if r.Intn(3) == 0 {
			st.Diverge = fmt.Sprintf("d%d", r.Intn(3)) // d2 does not exist
		}
		if r.Intn(3) == 0 {
			st.Converge = fmt.Sprintf("c%d", r.Intn(3)) // c2 does not exist
		}
		wf.Steps = append(wf.Steps, st)
	}
	return wf
}

func graphMaps(wf *domain.WorkflowDefinition) (map[string]*domain.StepDefinition, map[string]*domain.DivergenceDefinition, map[string]*domain.ConvergenceDefinition) {
	stepMap := map[string]*domain.StepDefinition{}
	for i := range wf.Steps {
		stepMap[wf.Steps[i].ID] = &wf.Steps[i]
	}
	divMap := map[string]*domain.DivergenceDefinition{}
	for i := range wf.DivergencePoints {
		divMap[wf.DivergencePoints[i].ID] = &wf.DivergencePoints[i]
	}
	convMap := map[string]*domain.ConvergenceDefinition{}
	for i := range wf.ConvergencePoints {
		convMap[wf.ConvergencePoints[i].ID] = &wf.ConvergencePoints[i]
	}
	return stepMap, divMap, convMap
}

func TestCanReachEnd_MatchesReferenceOnRandomGraphs(t *testing.T) {
	r := rand.New(rand.NewSource(20261006))
	reachable := 0
	for i := 0; i < 20000; i++ {
		wf := randomWorkflowGraph(r)
		sm, dm, cm := graphMaps(wf)
		want := referenceCanReachEnd(wf.EntryStep, sm, dm, cm, map[string]bool{})
		got := canReachEnd(wf.EntryStep, sm, dm, cm)
		if got != want {
			t.Fatalf("graph %d: canReachEnd = %v, reference = %v: %+v", i, got, want, wf)
		}
		if got {
			reachable++
		}
	}
	if reachable == 0 || reachable == 20000 {
		t.Fatalf("degenerate corpus: %d of 20000 reachable", reachable)
	}
}

// The exponential shape: a chain where every step has two outcomes to the
// next step and the last step loops on itself, so "end" is unreachable
// and the old search explored 2^n paths.
func TestValidate_TerminationAnalysisIsBounded(t *testing.T) {
	const n = 400
	wf := &domain.WorkflowDefinition{
		ID: "chain", Name: "chain", Version: "1", Status: domain.WorkflowStatusActive,
		Description: "d", AppliesTo: []string{"Task"}, EntryStep: "s0",
	}
	for i := 0; i < n; i++ {
		next := fmt.Sprintf("s%d", i+1)
		if i == n-1 {
			next = fmt.Sprintf("s%d", i)
		}
		wf.Steps = append(wf.Steps, domain.StepDefinition{
			ID: fmt.Sprintf("s%d", i), Name: "s", Type: domain.StepTypeManual,
			Outcomes: []domain.OutcomeDefinition{{ID: "a", Name: "a", NextStep: next}, {ID: "b", Name: "b", NextStep: next}},
		})
	}
	start := time.Now()
	res := Validate(wf)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Validate took %v on a %d-step chain", elapsed, n)
	}
	found := false
	for _, e := range res.Errors {
		if e.Message == "workflow may not terminate: not all paths reach 'end'" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the termination error, got %+v", res.Errors)
	}
}
