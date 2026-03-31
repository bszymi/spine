package artifact_test

import (
	"context"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/testutil"
)

func newTestService(t *testing.T) (*artifact.Service, *git.CLIClient, string, *queue.MemoryQueue) {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	q := queue.NewMemoryQueue(100)
	router := event.NewQueueRouter(q)
	svc := artifact.NewService(client, router, repo)
	return svc, client, repo, q
}

func addBareRemote(t *testing.T, repoDir string) string {
	t.Helper()
	bare := t.TempDir()
	cmd := exec.CommandContext(context.Background(), "git", "init", "--bare")
	cmd.Dir = bare
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	cmd = exec.CommandContext(context.Background(), "git", "remote", "add", "origin", bare)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	// Push initial commit so remote has main
	cmd = exec.CommandContext(context.Background(), "git", "push", "-u", "origin", "main")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initial push: %v\n%s", err, out)
	}
	return bare
}

func testCtx() context.Context {
	ctx := context.Background()
	ctx = observe.WithTraceID(ctx, "test-trace")
	ctx = observe.WithActorID(ctx, "test-actor")
	return ctx
}

const governanceContent = `---
type: Governance
title: Test Document
status: Living Document
version: "0.1"
---

# Test Document

This is a test governance document.
`

func TestCreateAndRead(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := testCtx()

	// Create
	result, err := svc.Create(ctx, "governance/test.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if result.Artifact.Type != domain.ArtifactTypeGovernance {
		t.Errorf("expected Governance, got %s", result.Artifact.Type)
	}
	if result.Artifact.Title != "Test Document" {
		t.Errorf("expected 'Test Document', got %s", result.Artifact.Title)
	}
	if result.CommitSHA == "" {
		t.Error("expected non-empty CommitSHA")
	}

	// Read back
	read, err := svc.Read(ctx, "governance/test.md", "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if read.Title != "Test Document" {
		t.Errorf("expected 'Test Document', got %s", read.Title)
	}
}

func TestCreateWithTrailers(t *testing.T) {
	svc, client, _, _ := newTestService(t)
	ctx := testCtx()

	_, err := svc.Create(ctx, "governance/traced.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify commit has trailers
	commits, err := client.Log(ctx, git.LogOpts{Limit: 1})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) == 0 {
		t.Fatal("expected at least one commit")
	}

	c := commits[0]
	if c.Trailers["Trace-ID"] != "test-trace" {
		t.Errorf("expected Trace-ID=test-trace, got %q", c.Trailers["Trace-ID"])
	}
	if c.Trailers["Actor-ID"] != "test-actor" {
		t.Errorf("expected Actor-ID=test-actor, got %q", c.Trailers["Actor-ID"])
	}
	if c.Trailers["Operation"] != "artifact.create" {
		t.Errorf("expected Operation=artifact.create, got %q", c.Trailers["Operation"])
	}
}

func TestCreateDuplicate(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := testCtx()

	_, err := svc.Create(ctx, "governance/dup.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Try to create again
	_, err = svc.Create(ctx, "governance/dup.md", governanceContent)
	if err == nil {
		t.Fatal("expected error for duplicate creation")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected SpineError, got %T", err)
	}
	if spineErr.Code != domain.ErrAlreadyExists {
		t.Errorf("expected already_exists, got %s", spineErr.Code)
	}
}

func TestCreateInvalidContent(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := testCtx()

	_, err := svc.Create(ctx, "governance/bad.md", "# No front matter")
	if err == nil {
		t.Fatal("expected error for invalid content")
	}
}

func TestCreateFailsValidation(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := testCtx()

	// Missing title
	content := "---\ntype: Governance\nstatus: Living Document\n---\n# Test\n"
	_, err := svc.Create(ctx, "governance/no-title.md", content)
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestReadNotFound(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := testCtx()

	_, err := svc.Read(ctx, "nonexistent.md", "")
	if err == nil {
		t.Fatal("expected error for nonexistent artifact")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected SpineError, got %T", err)
	}
	if spineErr.Code != domain.ErrNotFound {
		t.Errorf("expected not_found, got %s", spineErr.Code)
	}
}

func TestReadAtRef(t *testing.T) {
	svc, client, _, _ := newTestService(t)
	ctx := testCtx()

	_, err := svc.Create(ctx, "governance/versioned.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	firstCommit, _ := client.Head(ctx)

	// Update the artifact
	updatedContent := strings.Replace(governanceContent, "Test Document", "Updated Document", 1)
	_, err = svc.Update(ctx, "governance/versioned.md", updatedContent)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Read at first commit — should get original
	old, err := svc.Read(ctx, "governance/versioned.md", firstCommit)
	if err != nil {
		t.Fatalf("Read at ref: %v", err)
	}
	if old.Title != "Test Document" {
		t.Errorf("expected 'Test Document' at old ref, got %s", old.Title)
	}

	// Read at HEAD — should get updated
	current, err := svc.Read(ctx, "governance/versioned.md", "")
	if err != nil {
		t.Fatalf("Read at HEAD: %v", err)
	}
	if current.Title != "Updated Document" {
		t.Errorf("expected 'Updated Document' at HEAD, got %s", current.Title)
	}
}

func TestUpdate(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := testCtx()

	_, err := svc.Create(ctx, "governance/update-me.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updatedContent := strings.Replace(governanceContent, "Test Document", "Updated Title", 1)
	result, err := svc.Update(ctx, "governance/update-me.md", updatedContent)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if result.Artifact.Title != "Updated Title" {
		t.Errorf("expected 'Updated Title', got %s", result.Artifact.Title)
	}
}

func TestUpdateNotFound(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := testCtx()

	_, err := svc.Update(ctx, "governance/nonexistent.md", governanceContent)
	if err == nil {
		t.Fatal("expected error for update on nonexistent artifact")
	}
}

func TestUpdateFailsValidation(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := testCtx()

	_, err := svc.Create(ctx, "governance/val.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update with invalid content (missing title)
	_, err = svc.Update(ctx, "governance/val.md", "---\ntype: Governance\nstatus: Living Document\n---\n# No title\n")
	if err == nil {
		t.Fatal("expected validation error on update")
	}
}

func TestList(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := testCtx()

	_, err := svc.Create(ctx, "governance/a.md", governanceContent)
	if err != nil {
		t.Fatalf("Create a: %v", err)
	}

	_, err = svc.Create(ctx, "governance/b.md", strings.Replace(governanceContent, "Test Document", "Second Doc", 1))
	if err != nil {
		t.Fatalf("Create b: %v", err)
	}

	artifacts, err := svc.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(artifacts) != 2 {
		t.Errorf("expected 2 artifacts, got %d", len(artifacts))
	}
}

func TestCreateEmitsEvent(t *testing.T) {
	svc, _, _, q := newTestService(t)
	ctx, cancel := context.WithTimeout(testCtx(), 2*time.Second)
	defer cancel()

	var eventCount atomic.Int32
	q.Subscribe(ctx, "event_delivery", func(ctx context.Context, entry queue.Entry) error {
		eventCount.Add(1)
		return nil
	})
	go q.Start(ctx)
	defer q.Stop()

	_, err := svc.Create(ctx, "governance/evented.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	if eventCount.Load() < 1 {
		t.Error("expected at least 1 event emitted on create")
	}
}

func TestListMixedContent(t *testing.T) {
	svc, _, repo, _ := newTestService(t)
	ctx := testCtx()

	// Create an artifact
	_, err := svc.Create(ctx, "governance/test.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Add a non-artifact .md file directly
	testutil.WriteFile(t, repo, "README.md", "# Just a readme\n")
	testutil.GitAdd(t, repo, "README.md", "add readme")

	// Add a non-.md file
	testutil.WriteFile(t, repo, "config.yaml", "key: value\n")
	testutil.GitAdd(t, repo, "config.yaml", "add config")

	artifacts, err := svc.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Should only return the governance artifact, not README or config
	if len(artifacts) != 1 {
		t.Errorf("expected 1 artifact, got %d", len(artifacts))
	}
}

func TestCreateWithNilEvents(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	// Service with nil event router
	svc := artifact.NewService(client, nil, repo)
	ctx := testCtx()

	result, err := svc.Create(ctx, "governance/no-events.md", governanceContent)
	if err != nil {
		t.Fatalf("Create with nil events: %v", err)
	}
	if result.Artifact.Title != "Test Document" {
		t.Errorf("expected title, got %s", result.Artifact.Title)
	}
}

func TestCreatePathTraversal(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := testCtx()

	_, err := svc.Create(ctx, "../../../etc/passwd", governanceContent)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestCreateAbsolutePath(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := testCtx()

	_, err := svc.Create(ctx, "/etc/passwd", governanceContent)
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestUpdatePathTraversal(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := testCtx()

	_, err := svc.Update(ctx, "../../../etc/passwd", governanceContent)
	if err == nil {
		t.Fatal("expected error for path traversal on update")
	}
}

func TestUpdateEmitsEvent(t *testing.T) {
	svc, _, _, q := newTestService(t)
	ctx, cancel := context.WithTimeout(testCtx(), 2*time.Second)
	defer cancel()

	_, err := svc.Create(ctx, "governance/evt-update.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var eventCount atomic.Int32
	q.Subscribe(ctx, "event_delivery", func(ctx context.Context, entry queue.Entry) error {
		eventCount.Add(1)
		return nil
	})
	go q.Start(ctx)
	defer q.Stop()

	updatedContent := strings.Replace(governanceContent, "Test Document", "Updated", 1)
	_, err = svc.Update(ctx, "governance/evt-update.md", updatedContent)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	if eventCount.Load() < 1 {
		t.Error("expected at least 1 event emitted on update")
	}
}

func TestCreateAutoPushesToRemote(t *testing.T) {
	svc, _, repo, _ := newTestService(t)
	bare := addBareRemote(t, repo)
	ctx := testCtx()

	_, err := svc.Create(ctx, "governance/pushed.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify the commit was pushed to the bare remote
	cmd := exec.CommandContext(context.Background(), "git", "log", "--oneline", "-1", "main")
	cmd.Dir = bare
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log on bare: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Create Governance") {
		t.Errorf("expected pushed commit on remote, got: %s", out)
	}
}

func TestUpdateAutoPushesToRemote(t *testing.T) {
	svc, _, repo, _ := newTestService(t)
	bare := addBareRemote(t, repo)
	ctx := testCtx()

	_, err := svc.Create(ctx, "governance/push-update.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updatedContent := strings.Replace(governanceContent, "Test Document", "Updated Doc", 1)
	_, err = svc.Update(ctx, "governance/push-update.md", updatedContent)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Verify the update commit was pushed
	cmd := exec.CommandContext(context.Background(), "git", "log", "--oneline", "-1", "main")
	cmd.Dir = bare
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log on bare: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Update Governance") {
		t.Errorf("expected update commit on remote, got: %s", out)
	}
}

func TestAutoPushDisabledByEnv(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	svc, _, repo, _ := newTestService(t)
	bare := addBareRemote(t, repo)
	ctx := testCtx()

	_, err := svc.Create(ctx, "governance/no-push.md", governanceContent)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify the commit was NOT pushed (remote should only have initial commit)
	cmd := exec.CommandContext(context.Background(), "git", "log", "--oneline", "main")
	cmd.Dir = bare
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log on bare: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "Create Governance") {
		t.Error("expected commit NOT to be pushed when SPINE_GIT_AUTO_PUSH=false")
	}
}
