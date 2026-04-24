package projection

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/yamlsafe"
)

// bfGit returns content[ref+path] from ReadFile. All other GitClient
// methods embedded on git.GitClient panic, which is what we want — the
// test must not depend on unrelated git behaviour.
type bfGit struct {
	git.GitClient
	content []byte
}

func (g *bfGit) ReadFile(_ context.Context, _, _ string) ([]byte, error) {
	return g.content, nil
}

// bfStore records whether UpsertWorkflowProjection was called. It must
// NOT be called when parsing is refused — a projection write on unbounded
// input would defeat the purpose of the size guard.
type bfStore struct {
	store.Store
	upserted bool
}

func (s *bfStore) UpsertWorkflowProjection(_ context.Context, _ *store.WorkflowProjection) error {
	s.upserted = true
	return nil
}

func TestProjectWorkflow_RejectsOversizedYAML(t *testing.T) {
	oversized := append([]byte("id: wf-x\nname: oversize\n# "), bytes.Repeat([]byte("a"), yamlsafe.MaxBytes+10)...)
	g := &bfGit{content: oversized}
	s := &bfStore{}
	svc := NewService(g, s, nil, 0)

	err := svc.projectWorkflow(context.Background(), "workflows/oversize.yaml", "deadbeef")
	if err == nil {
		t.Fatal("expected error for oversized workflow YAML")
	}
	if !strings.Contains(err.Error(), "byte cap") {
		t.Errorf("expected byte-cap error, got %v", err)
	}
	if s.upserted {
		t.Error("UpsertWorkflowProjection must not be called when size cap rejects input")
	}
}

func TestProjectWorkflow_RejectsAliasBomb(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("id: &a wf-x\nname: alias-bomb\nrefs:\n")
	for i := 0; i < yamlsafe.MaxAliases+5; i++ {
		sb.WriteString("  - *a\n")
	}
	g := &bfGit{content: []byte(sb.String())}
	s := &bfStore{}
	svc := NewService(g, s, nil, 0)

	err := svc.projectWorkflow(context.Background(), "workflows/bomb.yaml", "deadbeef")
	if err == nil {
		t.Fatal("expected error for alias-heavy workflow YAML")
	}
	if !strings.Contains(err.Error(), "alias") {
		t.Errorf("expected alias-count error, got %v", err)
	}
	if s.upserted {
		t.Error("UpsertWorkflowProjection must not be called when alias guard rejects input")
	}
}

func TestProjectWorkflow_RejectsDeepNesting(t *testing.T) {
	// Build a doc nested deeper than MaxDepth.
	var sb strings.Builder
	for i := 0; i < yamlsafe.MaxDepth+5; i++ {
		sb.WriteString(strings.Repeat("  ", i))
		sb.WriteString("k:\n")
	}
	sb.WriteString(strings.Repeat("  ", yamlsafe.MaxDepth+5))
	sb.WriteString("v: 1\n")
	g := &bfGit{content: []byte(sb.String())}
	s := &bfStore{}
	svc := NewService(g, s, nil, 0)

	err := svc.projectWorkflow(context.Background(), "workflows/deep.yaml", "deadbeef")
	if err == nil {
		t.Fatal("expected error for deeply nested workflow YAML")
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Errorf("expected depth error, got %v", err)
	}
	if s.upserted {
		t.Error("UpsertWorkflowProjection must not be called when depth guard rejects input")
	}
}
