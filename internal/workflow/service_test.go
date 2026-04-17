package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/testutil"
	"github.com/bszymi/spine/internal/workflow"
)

const minimalYAML = `id: {ID}
name: Test Workflow
version: "{V}"
status: Active
description: test
applies_to:
  - Task
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    outcomes:
      - id: done
        name: Done
        next_step: end
`

func newSvc(t *testing.T) (*workflow.Service, string) {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	svc := workflow.NewService(client, repo)
	return svc, repo
}

func ctxWithActor() context.Context {
	return observe.WithActorID(context.Background(), "test-actor")
}

func TestService_Create_Success(t *testing.T) {
	svc, repo := newSvc(t)
	body := fmtYAML("task-default", "1.0")

	res, err := svc.Create(ctxWithActor(), "task-default", body)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.Workflow.ID != "task-default" {
		t.Errorf("id: %q", res.Workflow.ID)
	}
	if res.CommitSHA == "" {
		t.Error("empty commit_sha")
	}
	abs := filepath.Join(repo, "workflows/task-default.yaml")
	if _, err := os.Stat(abs); err != nil {
		t.Errorf("file not written: %v", err)
	}
}

func TestService_Create_InvalidID(t *testing.T) {
	svc, _ := newSvc(t)
	_, err := svc.Create(ctxWithActor(), "../evil", "body")
	if err == nil {
		t.Fatal("expected error")
	}
	se, ok := err.(*domain.SpineError)
	if !ok || se.Code != domain.ErrInvalidParams {
		t.Errorf("expected ErrInvalidParams, got %v", err)
	}
}

func TestService_Create_IDMismatch(t *testing.T) {
	svc, _ := newSvc(t)
	// id in body is "other" but path id is "task-default"
	body := fmtYAML("other", "1.0")
	_, err := svc.Create(ctxWithActor(), "task-default", body)
	if err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestService_Create_ValidationFailed(t *testing.T) {
	svc, _ := newSvc(t)
	// Missing required fields.
	body := "id: task-default\nname: test\n"
	_, err := svc.Create(ctxWithActor(), "task-default", body)
	if err == nil {
		t.Fatal("expected validation failure")
	}
	se, ok := err.(*domain.SpineError)
	if !ok || se.Code != domain.ErrValidationFailed {
		t.Errorf("expected ErrValidationFailed, got %v", err)
	}
}

func TestService_Create_Duplicate(t *testing.T) {
	svc, _ := newSvc(t)
	body := fmtYAML("task-default", "1.0")

	if _, err := svc.Create(ctxWithActor(), "task-default", body); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := svc.Create(ctxWithActor(), "task-default", body)
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	se, ok := err.(*domain.SpineError)
	if !ok || se.Code != domain.ErrAlreadyExists {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestService_Update_Success(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Create(ctxWithActor(), "task-default", fmtYAML("task-default", "1.0")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, err := svc.Update(ctxWithActor(), "task-default", fmtYAML("task-default", "1.1"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.Workflow.Version != "1.1" {
		t.Errorf("version: %q", res.Workflow.Version)
	}
}

func TestService_Update_RequiresVersionBump(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Create(ctxWithActor(), "task-default", fmtYAML("task-default", "1.0")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := svc.Update(ctxWithActor(), "task-default", fmtYAML("task-default", "1.0"))
	if err == nil {
		t.Fatal("expected version-bump error")
	}
	if !strings.Contains(err.Error(), "version bump") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestService_Update_NotFound(t *testing.T) {
	svc, _ := newSvc(t)
	_, err := svc.Update(ctxWithActor(), "missing", fmtYAML("missing", "1.0"))
	if err == nil {
		t.Fatal("expected not-found")
	}
	se, ok := err.(*domain.SpineError)
	if !ok || se.Code != domain.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestService_Read_Success(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Create(ctxWithActor(), "task-default", fmtYAML("task-default", "1.0")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, err := svc.Read(ctxWithActor(), "task-default", "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.Workflow.ID != "task-default" {
		t.Errorf("id: %q", res.Workflow.ID)
	}
	if res.Body == "" {
		t.Error("empty body")
	}
	if res.SourceCommit == "" {
		t.Error("empty source_commit")
	}
}

func TestService_Read_NotFound(t *testing.T) {
	svc, _ := newSvc(t)
	_, err := svc.Read(ctxWithActor(), "missing", "")
	if err == nil {
		t.Fatal("expected not-found")
	}
	se, ok := err.(*domain.SpineError)
	if !ok || se.Code != domain.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestService_List_Filters(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Create(ctxWithActor(), "first", fmtYAML("first", "1.0")); err != nil {
		t.Fatalf("Create first: %v", err)
	}
	if _, err := svc.Create(ctxWithActor(), "second", fmtYAML("second", "1.0")); err != nil {
		t.Fatalf("Create second: %v", err)
	}

	all, err := svc.List(ctxWithActor(), workflow.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("want 2 results, got %d", len(all))
	}

	filtered, err := svc.List(ctxWithActor(), workflow.ListOptions{AppliesTo: "Task"})
	if err != nil {
		t.Fatalf("List AppliesTo: %v", err)
	}
	if len(filtered) != 2 {
		t.Errorf("want 2 filtered by applies_to=Task, got %d", len(filtered))
	}

	none, err := svc.List(ctxWithActor(), workflow.ListOptions{AppliesTo: "Epic"})
	if err != nil {
		t.Fatalf("List AppliesTo Epic: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("want 0 filtered by applies_to=Epic, got %d", len(none))
	}
}

func TestService_ValidateBody_Passed(t *testing.T) {
	svc, _ := newSvc(t)
	result := svc.ValidateBody(ctxWithActor(), "task-default", fmtYAML("task-default", "1.0"))
	if result.Status != "passed" {
		t.Errorf("status: %s, errors: %+v", result.Status, result.Errors)
	}
}

func TestService_ValidateBody_ParseError(t *testing.T) {
	svc, _ := newSvc(t)
	result := svc.ValidateBody(ctxWithActor(), "task-default", ":::not yaml:::")
	if result.Status != "failed" {
		t.Errorf("status: %s", result.Status)
	}
}

func fmtYAML(id, version string) string {
	out := strings.ReplaceAll(minimalYAML, "{ID}", id)
	out = strings.ReplaceAll(out, "{V}", version)
	return out
}
