package gateway_test

// Regression tests for INIT-023 TASK-013 (audit F6 and P3-33). They are
// derived from the 2026-10-06 audit PoCs poc/skeptic-skills,
// poc/standalone-service/poc_skill_selfgrant_test.go,
// poc/engine-authz/poc_skill_selfgrant_test.go and
// poc/crypto-secrets-auth/operator_mints_admin_test.go, with their
// assertions inverted: against 61994fe a contributor could grant
// itself any skill, strip a reviewer's skill, mint, rename or deprecate
// catalogue entries, and an operator could register admin actors.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
)

// ── helpers ──

// auditBuffer is a goroutine-safe sink for the global slog handler.
// The handlers log from the server's goroutines while the test reads.
type auditBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *auditBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *auditBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// auditLine returns the first [AUDIT] record for operation, or "".
func (b *auditBuffer) auditLine(operation string) string {
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.Contains(line, "[AUDIT]") && strings.Contains(line, "audit_operation="+operation+" ") {
			return line
		}
	}
	return ""
}

func captureAudit(t *testing.T) *auditBuffer {
	t.Helper()
	buf := &auditBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// errorDetail reads the first error's detail map from a JSON error
// envelope and closes the body. A response with no detail yields an
// empty map.
func errorDetail(t *testing.T, resp *http.Response) map[string]string {
	t.Helper()
	defer resp.Body.Close()
	var env struct {
		Errors []struct {
			Code   string            `json:"code"`
			Detail map[string]string `json:"detail"`
		} `json:"errors"`
	}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &env)
	if len(env.Errors) == 0 || env.Errors[0].Detail == nil {
		return map[string]string{}
	}
	return env.Errors[0].Detail
}

func eligible(t *testing.T, ss *skillStore, actorID, skill string) bool {
	t.Helper()
	r, err := actor.NewService(ss).ValidateSkillEligibility(context.Background(), actorID, []string{skill})
	if err != nil {
		t.Fatal(err)
	}
	return r.Eligible
}

// addOperator registers op-1 (operator) on ss and returns its token.
func addOperator(t *testing.T, ss *skillStore) string {
	t.Helper()
	ss.actors["op-1"] = &domain.Actor{ActorID: "op-1", Type: domain.ActorTypeHuman, Name: "Operator",
		Role: domain.RoleOperator, Status: domain.ActorStatusActive}
	tok, _, err := auth.NewService(ss).CreateToken(context.Background(), "op-1", "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// ── AC 1 ──

func TestSkillAuthority_ContributorCannotEscalate(t *testing.T) {
	ts, ss, contrib := setupSkillServer(t)
	ss.skills["sk-review"] = &domain.Skill{SkillID: "sk-review", Name: "review", Status: domain.SkillStatusActive}
	ss.actors["reviewer-1"] = &domain.Actor{ActorID: "reviewer-1", Type: domain.ActorTypeHuman,
		Role: domain.RoleReviewer, Status: domain.ActorStatusActive}
	ss.actorSkills["reviewer-1"] = map[string]bool{"sk-review": true}

	if eligible(t, ss, "contributor-1", "review") || !eligible(t, ss, "reviewer-1", "review") {
		t.Fatal("precondition: only reviewer-1 is eligible for review")
	}

	// Self-grant carries its own reason.
	resp := skillRequest(t, "POST", ts.URL+"/api/v1/actors/contributor-1/skills/sk-review", contrib, "")
	if resp.StatusCode != http.StatusForbidden {
		resp.Body.Close()
		t.Fatalf("self-grant review: %d, want 403", resp.StatusCode)
	}
	if r := errorDetail(t, resp)["reason"]; r != auth.ReasonSelfSkillChange {
		t.Fatalf("self-grant reason %q, want %q", r, auth.ReasonSelfSkillChange)
	}

	for _, c := range []struct{ name, method, path, body string }{
		{"mint workflow_review", "POST", "/api/v1/skills", `{"skill_id":"sk-wfr","name":"workflow_review"}`},
		{"grant to a colluding actor", "POST", "/api/v1/actors/reviewer-1/skills/sk-review", ""},
		{"strip the real reviewer", "DELETE", "/api/v1/actors/reviewer-1/skills/sk-review", ""},
		{"forced deprecate", "POST", "/api/v1/skills/sk-review/deprecate?force=true", ""},
		{"rename", "PATCH", "/api/v1/skills/sk-review", `{"name":"review-x"}`},
	} {
		resp := skillRequest(t, c.method, ts.URL+c.path, contrib, c.body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s (%s %s) by contributor: %d, want 403", c.name, c.method, c.path, resp.StatusCode)
		}
	}

	if eligible(t, ss, "contributor-1", "review") {
		t.Error("contributor became eligible for review")
	}
	if !eligible(t, ss, "reviewer-1", "review") {
		t.Error("reviewer-1 lost review eligibility")
	}
	if _, ok := ss.skills["sk-wfr"]; ok {
		t.Error("contributor minted a skill")
	}
	if sk := ss.skills["sk-review"]; sk.Name != "review" || sk.Status != domain.SkillStatusActive || len(ss.skills) != 1 {
		t.Errorf("contributor mutated the skill catalogue: %+v (%d skills)", sk, len(ss.skills))
	}
}

// ── AC 3 ──

func TestSkillAuthority_NonAdminSelfChangeHasDistinctReason(t *testing.T) {
	ss := newSkillStore()
	ss.actors["contributor-1"] = &domain.Actor{ActorID: "contributor-1", Type: domain.ActorTypeHuman,
		Role: domain.RoleContributor, Status: domain.ActorStatusActive}
	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "review", Status: domain.SkillStatusActive}
	op := addOperator(t, ss)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: ss, Auth: auth.NewService(ss)})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	audit := captureAudit(t)

	self := skillRequest(t, "POST", ts.URL+"/api/v1/actors/op-1/skills/sk-1", op, "")
	if self.StatusCode != http.StatusForbidden {
		t.Fatalf("operator self-grant: %d, want 403", self.StatusCode)
	}
	if r := errorDetail(t, self)["reason"]; r != auth.ReasonSelfSkillChange {
		t.Fatalf("operator self-grant reason %q, want %q", r, auth.ReasonSelfSkillChange)
	}
	line := audit.auditLine("actor_skill_change_refused")
	if line == "" {
		t.Fatalf("self-grant refusal not audited; log:\n%s", audit.String())
	}
	for _, want := range []string{"target_actor_id=op-1", "operation=actor.add_skill", "reason=" + auth.ReasonSelfSkillChange, " actor_id=op-1"} {
		if !strings.Contains(line, want) {
			t.Errorf("audit line lacks %q: %s", want, line)
		}
	}

	// Self-removal is refused the same way.
	ss.actorSkills["op-1"] = map[string]bool{"sk-1": true}
	selfDel := skillRequest(t, "DELETE", ts.URL+"/api/v1/actors/op-1/skills/sk-1", op, "")
	if selfDel.StatusCode != http.StatusForbidden || errorDetail(t, selfDel)["reason"] != auth.ReasonSelfSkillChange {
		t.Fatalf("operator self-removal not refused as %s", auth.ReasonSelfSkillChange)
	}
	if !ss.actorSkills["op-1"]["sk-1"] {
		t.Fatal("operator removed its own skill")
	}

	// Another actor: a plain insufficient-role 403 with no reason.
	other := skillRequest(t, "POST", ts.URL+"/api/v1/actors/contributor-1/skills/sk-1", op, "")
	if other.StatusCode != http.StatusForbidden {
		t.Fatalf("operator grant to another actor: %d, want 403", other.StatusCode)
	}
	if d := errorDetail(t, other); d["reason"] != "" || d["required_role"] != string(domain.RoleAdmin) {
		t.Fatalf("operator grant to another actor: detail %v, want a plain insufficient-role refusal", d)
	}

	// Percent-encoded self id: chi routes on the escaped RawPath, so
	// without decoding, "op%2D1" would be compared with "op-1" as a
	// different actor and the self check would be skipped.
	enc := skillRequest(t, "POST", ts.URL+"/api/v1/actors/op%2D1/skills/sk-1", op, "")
	if enc.StatusCode != http.StatusForbidden {
		t.Fatalf("encoded self-grant: %d, want 403", enc.StatusCode)
	}
	if r := errorDetail(t, enc)["reason"]; r != auth.ReasonSelfSkillChange {
		t.Fatalf("encoded self-grant reason %q, want %q", r, auth.ReasonSelfSkillChange)
	}
	if _, ok := ss.actorSkills["op%2D1"]; ok {
		t.Fatal("encoded id reached the store undecoded")
	}
	// The remove route decodes the same way: an encoded self-removal is
	// refused with the self reason, not as a removal from "another"
	// actor, and the operator keeps its skill.
	encDel := skillRequest(t, "DELETE", ts.URL+"/api/v1/actors/op%2D1/skills/sk-1", op, "")
	if encDel.StatusCode != http.StatusForbidden {
		t.Fatalf("encoded self-removal: %d, want 403", encDel.StatusCode)
	}
	if r := errorDetail(t, encDel)["reason"]; r != auth.ReasonSelfSkillChange {
		t.Fatalf("encoded self-removal reason %q, want %q", r, auth.ReasonSelfSkillChange)
	}
	if !ss.actorSkills["op-1"]["sk-1"] {
		t.Fatal("encoded self-removal removed the operator's skill")
	}
	if n := strings.Count(audit.String(), "operation=actor.remove_skill"); n != 2 {
		t.Fatalf("%d actor.remove_skill refusals audited, want 2 (plain and encoded); log:\n%s", n, audit.String())
	}

	// Malformed escape. net/http refuses it before routing (the request
	// line does not parse), so over the wire it can never reach the
	// handler...
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /api/v1/actors/op%%ZZ/skills/sk-1 HTTP/1.1\r\nHost: spine\r\nAuthorization: Bearer %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", op)
	wire, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	wire.Body.Close()
	if wire.StatusCode != http.StatusBadRequest {
		t.Fatalf("wire-level malformed escape: %d, want 400", wire.StatusCode)
	}
	// ...but the handler does not rely on that: a request whose RawPath
	// carries a malformed escape (an in-process caller, or a future
	// front end that hands the raw path through) answers 400
	// invalid_params instead of reaching authorization with an
	// undecodable id.
	req := httptest.NewRequest("POST", "/api/v1/actors/op-1/skills/sk-1", http.NoBody)
	req.URL.Path = "/api/v1/actors/op%ZZ/skills/sk-1"
	req.URL.RawPath = "/api/v1/actors/op%ZZ/skills/sk-1"
	req.Header.Set("Authorization", "Bearer "+op)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed escape in {actor_id}: %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"invalid_params"`) {
		t.Fatalf("malformed escape: body %s, want invalid_params", rec.Body.String())
	}
}

// ── AC 4 ──

func TestSkillAuthority_AdminManagesSkills(t *testing.T) {
	ts, ss, admin := setupSkillServerAdmin(t)
	ss.skills["sk-review"] = &domain.Skill{SkillID: "sk-review", Name: "review", Status: domain.SkillStatusActive}
	audit := captureAudit(t)

	for _, target := range []string{"contributor-1", "admin-1"} {
		resp := skillRequest(t, "POST", ts.URL+"/api/v1/actors/"+target+"/skills/sk-review", admin, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("admin grant to %s: %d, want 200", target, resp.StatusCode)
		}
		if !ss.actorSkills[target]["sk-review"] {
			t.Fatalf("grant to %s not stored", target)
		}
	}
	line := audit.auditLine("actor_skill_added")
	if line == "" {
		t.Fatalf("grant not audited; log:\n%s", audit.String())
	}
	for _, want := range []string{"target_actor_id=contributor-1", "skill_id=sk-review", "skill_name=review", " actor_id=admin-1"} {
		if !strings.Contains(line, want) {
			t.Errorf("audit line lacks %q: %s", want, line)
		}
	}

	// The decoded id reaches the store: an encoded path grants to the
	// actor the client named, not to a literal "contributor%2D1".
	ss.actorSkills = map[string]map[string]bool{}
	resp := skillRequest(t, "POST", ts.URL+"/api/v1/actors/contributor%2D1/skills/sk-review", admin, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !ss.actorSkills["contributor-1"]["sk-review"] {
		t.Fatalf("encoded admin grant: %d, stored %v", resp.StatusCode, ss.actorSkills)
	}

	// Removal through the encoded path reaches the decoded actor too
	// (an undecoded "contributor%2D1" would answer 404).
	resp = skillRequest(t, "DELETE", ts.URL+"/api/v1/actors/contributor%2D1/skills/sk-review", admin, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || ss.actorSkills["contributor-1"]["sk-review"] {
		t.Fatalf("encoded admin removal: %d, stored %v", resp.StatusCode, ss.actorSkills)
	}
	if line := audit.auditLine("actor_skill_removed"); line == "" ||
		!strings.Contains(line, "target_actor_id=contributor-1") || !strings.Contains(line, "skill_id=sk-review") {
		t.Fatalf("removal not audited with the decoded id; log:\n%s", audit.String())
	}

	ss.actorSkills["contributor-1"] = map[string]bool{"sk-review": true}
	resp = skillRequest(t, "DELETE", ts.URL+"/api/v1/actors/contributor-1/skills/sk-review", admin, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("admin removal: %d, want 204", resp.StatusCode)
	}
}

// ── AC 5 ──

func TestSkillAuthority_ReferencedSkillDeprecateAndRenameNeedForceAndAreAudited(t *testing.T) {
	reviewWorkflow := []store.WorkflowProjection{
		{WorkflowID: "task-default", WorkflowPath: "workflows/task-default.yaml", Definition: makeWorkflowDefJSON("execution", "review")},
	}

	t.Run("referenced skill: rename away and deprecate", func(t *testing.T) {
		ts, ss, admin := setupSkillServerAdmin(t)
		ss.skills["sk-review"] = &domain.Skill{SkillID: "sk-review", Name: "review", Status: domain.SkillStatusActive}
		ss.workflowProjections = reviewWorkflow
		audit := captureAudit(t)

		resp := skillRequest(t, "PATCH", ts.URL+"/api/v1/skills/sk-review", admin, `{"name":"review-renamed"}`)
		if resp.StatusCode != http.StatusConflict {
			resp.Body.Close()
			t.Fatalf("rename of a referenced skill without force: %d, want 409", resp.StatusCode)
		}
		body := decodeBody(t, resp)
		resp.Body.Close()
		if wfs, _ := body["workflows"].([]any); len(wfs) != 1 {
			t.Errorf("409 body workflows = %v, want the one referencing workflow", body["workflows"])
		}
		if body["hint"] != "use ?force=true to rename anyway" {
			t.Errorf("409 hint = %v", body["hint"])
		}
		if ss.skills["sk-review"].Name != "review" {
			t.Fatalf("refused rename still renamed the skill to %q", ss.skills["sk-review"].Name)
		}

		resp = skillRequest(t, "POST", ts.URL+"/api/v1/skills/sk-review/deprecate", admin, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("deprecate of a referenced skill without force: %d, want 409", resp.StatusCode)
		}

		resp = skillRequest(t, "POST", ts.URL+"/api/v1/skills/sk-review/deprecate?force=true", admin, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("forced deprecate: %d, want 200", resp.StatusCode)
		}
		line := audit.auditLine("skill_deprecated")
		if line == "" {
			t.Fatalf("forced deprecation not audited; log:\n%s", audit.String())
		}
		for _, want := range []string{"skill_id=sk-review", "skill_name=review", "forced=true", "referencing_workflows=1", " actor_id=admin-1"} {
			if !strings.Contains(line, want) {
				t.Errorf("deprecate audit line lacks %q: %s", want, line)
			}
		}
	})

	// The real "rename to review" lever: auth.skills.name is UNIQUE, so
	// renaming x to review only succeeds in Postgres when no review row
	// exists — the fixture therefore has none; only the workflow names it.
	t.Run("rename onto a referenced name", func(t *testing.T) {
		ts, ss, admin := setupSkillServerAdmin(t)
		ss.skills["sk-x"] = &domain.Skill{SkillID: "sk-x", Name: "x", Status: domain.SkillStatusActive}
		ss.workflowProjections = reviewWorkflow
		audit := captureAudit(t)

		resp := skillRequest(t, "PATCH", ts.URL+"/api/v1/skills/sk-x", admin, `{"name":"review"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("rename x -> review without force: %d, want 409", resp.StatusCode)
		}
		if ss.skills["sk-x"].Name != "x" {
			t.Fatalf("refused rename still renamed the skill to %q", ss.skills["sk-x"].Name)
		}

		resp = skillRequest(t, "PATCH", ts.URL+"/api/v1/skills/sk-x?force=true", admin, `{"name":"review"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || ss.skills["sk-x"].Name != "review" {
			t.Fatalf("forced rename: %d, name %q", resp.StatusCode, ss.skills["sk-x"].Name)
		}
		line := audit.auditLine("skill_renamed")
		if line == "" || !strings.Contains(line, "skill_id=sk-x") || !strings.Contains(line, "from=x") || !strings.Contains(line, "to=review") {
			t.Fatalf("forced rename not audited; log:\n%s", audit.String())
		}
	})

	t.Run("unreferenced rename and same-name update need no force", func(t *testing.T) {
		ts, ss, admin := setupSkillServerAdmin(t)
		ss.skills["sk-go"] = &domain.Skill{SkillID: "sk-go", Name: "go", Status: domain.SkillStatusActive}
		ss.skills["sk-review"] = &domain.Skill{SkillID: "sk-review", Name: "review", Status: domain.SkillStatusActive}
		ss.workflowProjections = reviewWorkflow

		resp := skillRequest(t, "PATCH", ts.URL+"/api/v1/skills/sk-go", admin, `{"name":"golang"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("rename of an unreferenced skill: %d, want 200", resp.StatusCode)
		}
		// Re-sending the current name, or changing only the
		// description, is not a rename.
		resp = skillRequest(t, "PATCH", ts.URL+"/api/v1/skills/sk-review", admin, `{"name":"review","description":"d"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("same-name update of a referenced skill: %d, want 200", resp.StatusCode)
		}
	})
}

// ── AC 6 ──

func TestActorCreate_RoleCeiling(t *testing.T) {
	fs := newFakeStore()
	fs.actors["op-1"] = &domain.Actor{ActorID: "op-1", Type: domain.ActorTypeHuman, Name: "Operator",
		Role: domain.RoleOperator, Status: domain.ActorStatusActive}
	fs.actors["admin-1"] = &domain.Actor{ActorID: "admin-1", Type: domain.ActorTypeHuman, Name: "Admin",
		Role: domain.RoleAdmin, Status: domain.ActorStatusActive}
	authSvc := auth.NewService(fs)
	opTok, _, err := authSvc.CreateToken(context.Background(), "op-1", "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	adminTok, _, err := authSvc.CreateToken(context.Background(), "admin-1", "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	audit := captureAudit(t)

	cases := []struct {
		caller, tok, id, role string
		want                  int
	}{
		{"operator", opTok, "shadow-admin", "admin", http.StatusForbidden},
		{"operator", opTok, "peer-op", "operator", http.StatusForbidden},
		{"operator", opTok, "rev-1", "reviewer", http.StatusCreated},
		{"admin", adminTok, "admin-2", "admin", http.StatusCreated},
	}
	for _, c := range cases {
		t.Run(c.caller+"->"+c.role, func(t *testing.T) {
			body := `{"actor_id":"` + c.id + `","type":"automated_system","name":"x","role":"` + c.role + `"}`
			resp := skillRequest(t, "POST", ts.URL+"/api/v1/actors", c.tok, body)
			if resp.StatusCode != c.want {
				resp.Body.Close()
				t.Fatalf("%s creating role=%s: %d, want %d", c.caller, c.role, resp.StatusCode, c.want)
			}
			_, stored := fs.actors[c.id]
			if c.want == http.StatusForbidden {
				d := errorDetail(t, resp)
				if d["reason"] != auth.ReasonRoleCeiling || d["granted_role"] != c.role {
					t.Fatalf("refusal detail %v, want reason=%s granted_role=%s", d, auth.ReasonRoleCeiling, c.role)
				}
				if stored {
					t.Fatalf("refused actor %s was stored", c.id)
				}
				return
			}
			resp.Body.Close()
			if !stored {
				t.Fatalf("created actor %s not stored", c.id)
			}
		})
	}

	refused := audit.auditLine("actor_create_refused")
	if refused == "" || !strings.Contains(refused, "granted_role=admin") || !strings.Contains(refused, "reason="+auth.ReasonRoleCeiling) {
		t.Errorf("ceiling refusal not audited; log:\n%s", audit.String())
	}
	created := audit.auditLine("actor_created")
	if created == "" || !strings.Contains(created, "created_actor_id=rev-1") || !strings.Contains(created, "granted_role=reviewer") {
		t.Errorf("creation not audited; log:\n%s", audit.String())
	}
}

// ── Review round 1: concurrent skill writes ──

// racingSkillStore returns copies from GetSkill, as Postgres does, and
// runs a concurrent change once, right after the handler's first read:
// another admin request committing between that read and the write.
type racingSkillStore struct {
	*skillStore
	once           sync.Once
	afterFirstRead func(stored *domain.Skill)
}

func (r *racingSkillStore) GetSkill(ctx context.Context, skillID string) (*domain.Skill, error) {
	sk, err := r.skillStore.GetSkill(ctx, skillID)
	if err != nil {
		return nil, err
	}
	read := *sk
	r.once.Do(func() {
		if r.afterFirstRead != nil {
			r.afterFirstRead(sk)
		}
	})
	return &read, nil
}

// UpdateSkill and PATCH write every column, so before the re-read a
// PATCH racing a forced deprecation wrote the stale status back (an
// un-deprecation with no audit line), and a deprecation racing a rename
// reverted the name. The rename guard's reference scans widened that
// window. Now the columns a request does not change come from a re-read
// just before the write, and a name that changed in between is a 409.
func TestSkillMutations_ConcurrentChangeIsNotOverwritten(t *testing.T) {
	setup := func(t *testing.T, change func(*domain.Skill)) (*httptest.Server, *skillStore, string) {
		t.Helper()
		ss := newSkillStore()
		ss.actors["admin-1"] = &domain.Actor{ActorID: "admin-1", Type: domain.ActorTypeHuman, Name: "Admin",
			Role: domain.RoleAdmin, Status: domain.ActorStatusActive}
		ss.skills["sk-x"] = &domain.Skill{SkillID: "sk-x", Name: "x", Description: "old", Status: domain.SkillStatusActive}
		rs := &racingSkillStore{skillStore: ss, afterFirstRead: change}
		tok, _, err := auth.NewService(rs).CreateToken(context.Background(), "admin-1", "t", nil)
		if err != nil {
			t.Fatal(err)
		}
		srv := gateway.NewServer(":0", gateway.ServerConfig{Store: rs, Auth: auth.NewService(rs)})
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)
		return ts, ss, tok
	}
	deprecate := func(sk *domain.Skill) { sk.Status = domain.SkillStatusDeprecated }
	rename := func(sk *domain.Skill) { sk.Name = "y" }

	for _, c := range []struct {
		name       string
		change     func(*domain.Skill)
		method     string
		path       string
		body       string
		wantStatus int
		want       domain.Skill
	}{
		{"description edit keeps a concurrent deprecation", deprecate, "PATCH", "/api/v1/skills/sk-x", `{"description":"new"}`,
			http.StatusOK, domain.Skill{Name: "x", Description: "new", Status: domain.SkillStatusDeprecated}},
		{"rename keeps a concurrent deprecation", deprecate, "PATCH", "/api/v1/skills/sk-x", `{"name":"z"}`,
			http.StatusOK, domain.Skill{Name: "z", Description: "old", Status: domain.SkillStatusDeprecated}},
		{"rename decided on a stale name is refused", rename, "PATCH", "/api/v1/skills/sk-x", `{"name":"z"}`,
			http.StatusConflict, domain.Skill{Name: "y", Description: "old", Status: domain.SkillStatusActive}},
		{"deprecation does not revert a concurrent rename", rename, "POST", "/api/v1/skills/sk-x/deprecate?force=true", "",
			http.StatusConflict, domain.Skill{Name: "y", Description: "old", Status: domain.SkillStatusActive}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ts, ss, admin := setup(t, c.change)
			resp := skillRequest(t, c.method, ts.URL+c.path, admin, c.body)
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("%s %s: %d (%s), want %d", c.method, c.path, resp.StatusCode, b, c.wantStatus)
			}
			if c.wantStatus == http.StatusConflict && !strings.Contains(string(b), `"code":"conflict"`) {
				t.Errorf("409 body %s, want code conflict", b)
			}
			got := ss.skills["sk-x"]
			if got.Name != c.want.Name || got.Description != c.want.Description || got.Status != c.want.Status {
				t.Fatalf("stored skill {name %q, description %q, status %q}, want {%q, %q, %q}",
					got.Name, got.Description, got.Status, c.want.Name, c.want.Description, c.want.Status)
			}
		})
	}
}
