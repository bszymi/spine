package projection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/core/yamlsafe"
)

// Shared fixtures for the INIT-023 TASK-011 projection tests: an
// in-memory store covering the projection surface, a recording event
// router, a real-git scratch repository and the audit payloads.

// ── map store ──

// mapStore is an in-memory store.Store for the projection surface. Any
// method outside it panics through the embedded nil interface, which
// keeps accidental dependencies visible. It does NOT implement the
// optional all-rows capability; allRowsStore adds it.
type mapStore struct {
	store.Store

	mu        sync.Mutex
	state     *store.SyncState
	artifacts map[string]*store.ArtifactProjection
	links     map[string][]store.ArtifactLink
	execs     map[string]*store.ExecutionProjection
	workflows map[string]*store.WorkflowProjection
	bpRules   []store.BranchProtectionRuleProjection
	bpCommit  string

	// counters
	workflowUpserts map[string]int
	bpUpserts       int
	listActiveCalls int
	listAllCalls    int

	// error injection
	deleteWorkflowErr map[string]error
	upsertWorkflowErr map[string]error
}

func newMapStore() *mapStore {
	return &mapStore{
		artifacts:         map[string]*store.ArtifactProjection{},
		links:             map[string][]store.ArtifactLink{},
		execs:             map[string]*store.ExecutionProjection{},
		workflows:         map[string]*store.WorkflowProjection{},
		workflowUpserts:   map[string]int{},
		deleteWorkflowErr: map[string]error{},
		upsertWorkflowErr: map[string]error{},
	}
}

func (s *mapStore) GetSyncState(context.Context) (*store.SyncState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return nil, nil
	}
	cp := *s.state
	return &cp, nil
}

func (s *mapStore) UpdateSyncState(_ context.Context, st *store.SyncState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *st
	s.state = &cp
	return nil
}

func (s *mapStore) DeleteAllProjections(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artifacts = map[string]*store.ArtifactProjection{}
	s.links = map[string][]store.ArtifactLink{}
	s.execs = map[string]*store.ExecutionProjection{}
	s.workflows = map[string]*store.WorkflowProjection{}
	return nil
}

func (s *mapStore) UpsertArtifactProjection(_ context.Context, p *store.ArtifactProjection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	s.artifacts[p.ArtifactPath] = &cp
	return nil
}

func (s *mapStore) GetArtifactProjection(_ context.Context, path string) (*store.ArtifactProjection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.artifacts[path]; ok {
		cp := *p
		return &cp, nil
	}
	return nil, domain.NewError(domain.ErrNotFound, "artifact not found")
}

// DeleteArtifactProjection mirrors PostgresStore: it deletes the
// artifact row only (execution projections are not touched).
func (s *mapStore) DeleteArtifactProjection(_ context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.artifacts, path)
	return nil
}

func (s *mapStore) UpsertArtifactLinks(_ context.Context, source string, links []store.ArtifactLink, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]store.ArtifactLink(nil), links...)
	s.links[source] = cp
	return nil
}

func (s *mapStore) DeleteArtifactLinks(_ context.Context, source string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.links, source)
	return nil
}

func (s *mapStore) UpsertExecutionProjection(_ context.Context, p *store.ExecutionProjection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	s.execs[p.TaskPath] = &cp
	return nil
}

func (s *mapStore) UpsertWorkflowProjection(_ context.Context, p *store.WorkflowProjection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.upsertWorkflowErr[p.WorkflowPath]; err != nil {
		return err
	}
	cp := *p
	s.workflows[p.WorkflowPath] = &cp
	s.workflowUpserts[p.WorkflowPath]++
	return nil
}

func (s *mapStore) GetWorkflowProjection(_ context.Context, path string) (*store.WorkflowProjection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.workflows[path]; ok {
		cp := *p
		return &cp, nil
	}
	return nil, domain.NewError(domain.ErrNotFound, "workflow not found")
}

func (s *mapStore) DeleteWorkflowProjection(_ context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.deleteWorkflowErr[path]; err != nil {
		return err
	}
	delete(s.workflows, path)
	return nil
}

func (s *mapStore) ListActiveWorkflowProjections(context.Context) ([]store.WorkflowProjection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listActiveCalls++
	var out []store.WorkflowProjection
	for _, p := range s.workflows {
		if p.Status == string(domain.WorkflowStatusActive) {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkflowPath < out[j].WorkflowPath })
	return out, nil
}

func (s *mapStore) UpsertBranchProtectionRules(_ context.Context, rules []store.BranchProtectionRuleProjection, commit string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bpRules = append([]store.BranchProtectionRuleProjection(nil), rules...)
	s.bpCommit = commit
	s.bpUpserts++
	return nil
}

// row returns a copy of a workflow row, or nil.
func (s *mapStore) row(path string) *store.WorkflowProjection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.workflows[path]; ok {
		cp := *p
		return &cp
	}
	return nil
}

func (s *mapStore) putRow(p store.WorkflowProjection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := p
	s.workflows[p.WorkflowPath] = &cp
}

func (s *mapStore) setState(commit, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = &store.SyncState{LastSyncedCommit: commit, Status: status}
}

func (s *mapStore) syncState() store.SyncState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return store.SyncState{}
	}
	return *s.state
}

func (s *mapStore) upserts(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workflowUpserts[path]
}

// clone deep-copies the store's projection state (not its counters).
func (s *mapStore) clone() *mapStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := newMapStore()
	if s.state != nil {
		st := *s.state
		c.state = &st
	}
	for k, v := range s.artifacts {
		cp := *v
		c.artifacts[k] = &cp
	}
	for k, v := range s.links {
		c.links[k] = append([]store.ArtifactLink(nil), v...)
	}
	for k, v := range s.execs {
		cp := *v
		c.execs[k] = &cp
	}
	for k, v := range s.workflows {
		cp := *v
		c.workflows[k] = &cp
	}
	c.bpRules = append([]store.BranchProtectionRuleProjection(nil), s.bpRules...)
	c.bpCommit = s.bpCommit
	return c
}

// allRowsStore adds the optional all-rows capability the sweep asserts.
type allRowsStore struct{ *mapStore }

func (s allRowsStore) ListWorkflowProjections(context.Context) ([]store.WorkflowProjection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listAllCalls++
	out := make([]store.WorkflowProjection, 0, len(s.workflows))
	for _, p := range s.workflows {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkflowPath < out[j].WorkflowPath })
	return out, nil
}

// ── event recorder ──

type recordingRouter struct {
	mu     sync.Mutex
	events []domain.Event
}

var _ event.EventRouter = (*recordingRouter)(nil)

func (r *recordingRouter) Emit(_ context.Context, e domain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recordingRouter) Subscribe(context.Context, domain.EventType, event.EventHandler) error {
	return nil
}

func (r *recordingRouter) ofType(t domain.EventType) []domain.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Event
	for i := range r.events {
		if r.events[i].Type == t {
			out = append(out, r.events[i])
		}
	}
	return out
}

func (r *recordingRouter) rejections(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	events := r.ofType(domain.EventProjectionWorkflowRejected)
	for i := range events {
		var p map[string]any
		if err := json.Unmarshal(events[i].Payload, &p); err != nil {
			t.Fatalf("rejection payload: %v", err)
		}
		out = append(out, p)
	}
	return out
}

func (r *recordingRouter) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// ── git scratch repository ──

type scratchRepo struct {
	t   *testing.T
	dir string
	git *git.CLIClient
}

func newScratchRepo(t *testing.T) *scratchRepo {
	t.Helper()
	r := &scratchRepo{t: t, dir: t.TempDir()}
	r.run("init", "-q", "-b", "main")
	r.git = git.NewCLIClient(r.dir)
	return r
}

func (r *scratchRepo) run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *scratchRepo) write(rel, body string) {
	r.t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	// rel is a test-authored literal under t.TempDir().
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil { //nolint:gosec // G703: test fixture path, not external input
		r.t.Fatal(err)
	}
}

func (r *scratchRepo) remove(rel string) {
	r.t.Helper()
	r.run("rm", "-q", "--", rel)
}

func (r *scratchRepo) mv(from, to string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(r.dir, to)), 0o755); err != nil {
		r.t.Fatal(err)
	}
	r.run("mv", "--", from, to)
}

// commit stages everything and returns the new commit SHA.
func (r *scratchRepo) commit(msg string) string {
	r.t.Helper()
	r.run("add", "-A")
	r.run("commit", "-q", "--allow-empty", "-m", msg)
	return r.run("rev-parse", "HEAD")
}

// ── workflow fixtures ──

// reviewedWorkflow is a valid task-default with a human review step.
const reviewedWorkflow = `id: task-default
name: Default Task Workflow
version: "1.0"
status: Active
description: Human-reviewed task workflow
applies_to: [Task]
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    outcomes:
      - {id: completed, name: Done, next_step: review}
  - id: review
    name: Human Code Review
    type: review
    outcomes:
      - {id: accepted, name: Accepted, next_step: end}
`

// reviewlessWorkflow is a valid task-default WITHOUT a review step.
const reviewlessWorkflow = `id: task-default
name: Default Task Workflow
version: "0.9"
status: Active
description: Task workflow
applies_to: [Task]
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    outcomes:
      - {id: completed, name: Done, next_step: end}
`

// pocFoldWorkflow / pocFoldOutcome are the audit payloads, verbatim.
const pocFoldWorkflow = `id: task-default
name: Default Task Workflow
version: "8.0"
status: Active
description: Human-reviewed task workflow
applies_to: [Task]
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    execution: {mode: hybrid, eligible_actor_types: [ai_agent], required_skills: [implementation]}
    outcomes:
      - {id: completed, name: Done, next_step: review}
  - id: review
    name: Human Code Review
    type: review
    execution: {mode: human_only, eligible_actor_types: [human], required_skills: [review]}
    outcomes:
      - {id: accepted, name: Accepted, next_step: publish}
  - id: publish
    name: Publish
    type: internal
    execution: {mode: spine_only, handler: merge}
    retry: {limit: 1}
    outcomes:
      - {id: merged, name: Merged, next_step: end}
ſteps:
  - id: execute
    name: Execute Task
    type: manual
    execution: {mode: hybrid, eligible_actor_types: [ai_agent], required_skills: [implementation]}
    outcomes:
      - {id: completed, name: Done, next_step: publish}
  - id: publish
    name: Publish
    type: internal
    execution: {mode: spine_only, handler: merge}
    retry: {limit: 1}
    outcomes:
      - {id: merged, name: Merged, next_step: end}
`

const pocFoldOutcome = `id: task-default
name: Default Task Workflow
version: "8.0"
status: Active
description: d
applies_to: [Task]
entry_step: validate
steps:
  - id: validate
    name: Validate Build and Tests
    type: automated
    execution: {mode: automated_only, eligible_actor_types: [automated_system]}
    retry: {limit: 2}
    outcomes:
      - id: passed
        name: Passed
        next_step: publish
      - id: failed
        name: Failed
        next_step: validate
        next_ſtep: publish
  - id: publish
    name: Publish
    type: internal
    execution: {mode: spine_only, handler: merge}
    retry: {limit: 1}
    outcomes:
      - {id: merged, name: Merged, next_step: end}
`

// validateGateWorkflow is pocFoldOutcome's benign reading: a valid file
// whose failed outcome routes back to validate.
const validateGateWorkflow = `id: task-default
name: Default Task Workflow
version: "7.0"
status: Active
description: d
applies_to: [Task]
entry_step: validate
steps:
  - id: validate
    name: Validate Build and Tests
    type: automated
    execution: {mode: automated_only, eligible_actor_types: [automated_system]}
    retry: {limit: 2}
    outcomes:
      - id: passed
        name: Passed
        next_step: publish
      - id: failed
        name: Failed
        next_step: validate
  - id: publish
    name: Publish
    type: internal
    execution: {mode: spine_only, handler: merge}
    retry: {limit: 1}
    outcomes:
      - {id: merged, name: Merged, next_step: end}
`

// stepProfile is the shape of SMP's runner step profiles under
// workflows/steps/ (not a workflow definition; fails strict Parse).
const stepProfile = `name: validate
environment:
  type: docker
  image_slot: validate-base
timeout: "10m"
steps:
  - name: go-build
    run: go build ./...
`

const taskPath = "initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001.md"

func taskDoc(status, title string) string {
	return "---\nid: TASK-001\ntype: Task\ntitle: " + title + "\nstatus: " + status +
		"\nepic: /initiatives/INIT-001/epics/EPIC-001/epic.md\ninitiative: /initiatives/INIT-001/initiative.md\n---\n\n# body\n"
}

// legacyRow builds the row Spine 61994fe's projectWorkflow would have
// stored for a body: yamlsafe.Decode → any → json.Marshal for the
// definition, a non-strict decode for the typed columns. It is how a
// smuggled row looks in a store written before the fix.
func legacyRow(t *testing.T, path, body, commit string) store.WorkflowProjection {
	t.Helper()
	node, err := yamlsafe.Decode([]byte(body))
	if err != nil {
		t.Fatalf("legacy decode: %v", err)
	}
	var typed struct {
		ID        string   `yaml:"id"`
		Name      string   `yaml:"name"`
		Version   string   `yaml:"version"`
		Status    string   `yaml:"status"`
		AppliesTo []string `yaml:"applies_to"`
	}
	if err := node.Decode(&typed); err != nil {
		t.Fatalf("legacy typed decode: %v", err)
	}
	var raw any
	if err := node.Decode(&raw); err != nil {
		t.Fatalf("legacy raw decode: %v", err)
	}
	def, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("legacy marshal: %v", err)
	}
	applies, _ := json.Marshal(typed.AppliesTo)
	return store.WorkflowProjection{
		WorkflowPath: path, WorkflowID: typed.ID, Name: typed.Name, Version: typed.Version,
		Status: typed.Status, AppliesTo: applies, Definition: def, SourceCommit: commit,
	}
}

// acceptedRow builds the row the fixed projection stores for a body.
func acceptedRow(t *testing.T, path, body, commit string) store.WorkflowProjection {
	t.Helper()
	wf, def, err := workflow.ProjectionDefinition(path, []byte(body))
	if err != nil {
		t.Fatalf("ProjectionDefinition(%s): %v", path, err)
	}
	applies, _ := json.Marshal(wf.AppliesTo)
	return store.WorkflowProjection{
		WorkflowPath: path, WorkflowID: wf.ID, Name: wf.Name, Version: wf.Version,
		Status: string(wf.Status), AppliesTo: applies, Definition: def, SourceCommit: commit,
	}
}

func decodeDef(t *testing.T, def []byte) *domain.WorkflowDefinition {
	t.Helper()
	wf, err := workflow.UnmarshalProjectionDefinition(def)
	if err != nil {
		t.Fatalf("decode definition: %v", err)
	}
	return wf
}

func stepIDs(wf *domain.WorkflowDefinition) []string {
	out := make([]string, 0, len(wf.Steps))
	for i := range wf.Steps {
		out = append(out, wf.Steps[i].ID)
	}
	return out
}

func hasStep(wf *domain.WorkflowDefinition, id string) bool {
	for i := range wf.Steps {
		if wf.Steps[i].ID == id {
			return true
		}
	}
	return false
}

// ── log capture ──

// captureLogs redirects slog's default logger for the test. Tests that
// use it must not run in parallel.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// lines returns the captured log lines containing every needle.
func (b *lockedBuffer) lines(needles ...string) []string {
	var out []string
	for _, l := range strings.Split(b.String(), "\n") {
		ok := l != ""
		for _, n := range needles {
			if !strings.Contains(l, n) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, l)
		}
	}
	return out
}

var errSimulated = errors.New("simulated failure")
