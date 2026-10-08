package projection

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
)

// INIT-023 TASK-011, audit P3-23: FullRebuild and IncrementalSync use
// one path classifier, so artifacts_dir is applied identically and a
// full rebuild equals an incrementally maintained store.

func TestPathScope_Classifier(t *testing.T) {
	root := newPathScope("")
	sub := newPathScope("spine")
	for _, in := range []string{"", ".", "./", "/", "  /  "} {
		if newPathScope(in) != root {
			t.Errorf("newPathScope(%q) must be the root", in)
		}
	}
	for _, in := range []string{"spine", "spine/", "./spine", " ./spine/ "} {
		if newPathScope(in) != sub {
			t.Errorf("newPathScope(%q) = %+v, want spine", in, newPathScope(in))
		}
	}

	keys := []struct {
		scope pathScope
		path  string
		key   string
		ok    bool
	}{
		{root, "initiatives/a.md", "initiatives/a.md", true},
		{root, "templates/task.md", "", false},
		{root, "spine/templates/task.md", "spine/templates/task.md", true},
		{root, "workflows/x.yaml", "", false},
		{sub, "spine/initiatives/a.md", "initiatives/a.md", true},
		{sub, "initiatives/a.md", "", false},
		{sub, "spine/templates/task.md", "", false},
		{sub, "templates/task.md", "", false},
		{sub, "spinex/a.md", "", false},
		{sub, "spine/README.txt", "", false},
	}
	for _, c := range keys {
		key, ok := c.scope.artifactKey(c.path)
		if key != c.key || ok != c.ok {
			t.Errorf("%+v artifactKey(%q) = %q,%v want %q,%v", c.scope, c.path, key, ok, c.key, c.ok)
		}
	}

	wfs := []struct {
		scope pathScope
		path  string
		want  bool
	}{
		{root, "workflows/task-default.yaml", true},
		{root, "workflows/a.yml", true},
		{root, "workflows/steps/validate.yaml", false},
		{root, "workflows/readme.md", false},
		{root, "spine/workflows/x.yaml", false},
		{root, "xworkflows/x.yaml", false},
		{sub, "workflows/rogue.yaml", true}, // owner question 3: root stays canonical
		{sub, "spine/workflows/task.yaml", true},
		{sub, "spine/workflows/steps/x.yaml", false},
		{sub, "other/workflows/x.yaml", false},
	}
	for _, c := range wfs {
		if got := c.scope.isWorkflow(c.path); got != c.want {
			t.Errorf("%+v isWorkflow(%q) = %v", c.scope, c.path, got)
		}
	}

	if got := WorkflowDirs("/"); !reflect.DeepEqual(got, []string{"workflows/"}) {
		t.Errorf("WorkflowDirs(/) = %v", got)
	}
	if got := WorkflowDirs("./spine/"); !reflect.DeepEqual(got, []string{"workflows/", "spine/workflows/"}) {
		t.Errorf("WorkflowDirs(spine) = %v", got)
	}
}

func TestPathScope_ArtifactDiffsConvertsBoundaryRenames(t *testing.T) {
	sub := newPathScope("spine")
	in := []git.FileDiff{
		{Path: "spine/a.md", Status: "modified"},
		{Path: "b.md", Status: "added"},                                          // out of scope
		{Path: "spine/templates/t.md", Status: "added"},                          // template
		{OldPath: "spine/templates/t.md", Path: "spine/x.md", Status: "renamed"}, // into scope
		{OldPath: "spine/y.md", Path: "spine/templates/y.md", Status: "renamed"}, // out of scope
		{OldPath: "z.md", Path: "spine/z.md", Status: "renamed"},                 // into the dir
		{OldPath: "spine/w.md", Path: "w.md", Status: "renamed"},                 // out of the dir
		{OldPath: "spine/v.md", Path: "spine/v2.md", Status: "renamed"},          // inside
		{OldPath: "u.md", Path: "u2.md", Status: "renamed"},                      // outside
		{Path: "spine/workflows/x.yaml", Status: "added"},                        // not an artifact
	}
	want := []git.FileDiff{
		{Path: "spine/a.md", Status: "modified"},
		{Path: "spine/x.md", Status: "added"},
		{Path: "spine/y.md", Status: "deleted"},
		{Path: "spine/z.md", Status: "added"},
		{Path: "spine/w.md", Status: "deleted"},
		{OldPath: "spine/v.md", Path: "spine/v2.md", Status: "renamed"},
	}
	if got := sub.artifactDiffs(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("artifactDiffs =\n %+v\nwant\n %+v", got, want)
	}
}

// AC 19 (inverted TestPOC_IncrementalSyncArtifactsDirKeyCollision).
func TestIncrementalSync_ArtifactsDirScopesArtifactsAndWorkflows(t *testing.T) {
	ctx := context.Background()
	repo := newScratchRepo(t)
	repo.write("spine/"+taskPath, taskDoc("Pending", "Governed task"))
	repo.write("spine/workflows/task.yaml", "id: wf\nname: governed\nversion: '1'\nstatus: Active\napplies_to: [Task]\n")
	repo.commit("c1")

	st := newMapStore()
	svc := NewService(repo.git, st, nil, 0)
	svc.WithArtifactsDir("spine")
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}

	// c2: an out-of-tree file at the colliding key, a governed workflow
	// edit, a root workflow and a template with Task front matter.
	repo.write(taskPath, taskDoc("Completed", "Spoofed by out-of-tree file"))
	repo.write("spine/workflows/task.yaml", "id: wf\nname: governed\nversion: '2'\nstatus: Deprecated\napplies_to: [Task]\n")
	repo.write("workflows/rogue.yaml", "id: rogue\nname: rogue\nversion: '1'\nstatus: Active\napplies_to: [Task]\n")
	repo.write("spine/templates/x.md", taskDoc("Pending", "Template"))
	repo.write("spine/templates/t.md", strings.Replace(taskDoc("Pending", "Moved in"), "TASK-001", "TASK-002", 1))
	repo.commit("c2")
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}

	got := st.artifacts[taskPath]
	if got == nil || got.Status != "Pending" || got.Title != "Governed task" {
		t.Fatalf("the governed row was overwritten by an out-of-tree file: %+v", got)
	}
	if w := st.row("spine/workflows/task.yaml"); w == nil || w.Status != "Deprecated" || w.Version != "2" {
		t.Fatalf("the governed workflow edit must be projected incrementally: %+v", w)
	}
	if _, ok := st.artifacts["templates/x.md"]; ok {
		t.Fatal("templates/ must not be projected")
	}
	// Owner question 3 default: root workflows/ stays a governed
	// workflow location (workflow.Service writes there), so the PoC's
	// third observation holds by design, for both entry points.
	if w := st.row("workflows/rogue.yaml"); w == nil || w.Status != "Active" {
		t.Fatalf("root workflows/rogue.yaml must be projected Active by IncrementalSync: %+v", w)
	}
	rebuilt := newMapStore()
	rsvc := NewService(repo.git, rebuilt, nil, 0)
	rsvc.WithArtifactsDir("spine")
	if err := rsvc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if w := rebuilt.row("workflows/rogue.yaml"); w == nil || w.Status != "Active" {
		t.Fatalf("root workflows/rogue.yaml must be projected Active by FullRebuild: %+v", w)
	}

	// A rename out of templates/ into the governed tree is a create…
	task2 := strings.Replace(taskPath, "TASK-001", "TASK-002", 1)
	repo.mv("spine/templates/t.md", "spine/"+task2)
	repo.commit("c3")
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}
	if a := st.artifacts[task2]; a == nil || a.Title != "Moved in" {
		t.Fatalf("rename out of templates/ must create %s: %+v", task2, a)
	}
	// …and the reverse rename is a delete.
	repo.mv("spine/"+task2, "spine/templates/t.md")
	repo.commit("c4")
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.artifacts[task2]; ok {
		t.Fatalf("rename into templates/ must delete %s", task2)
	}
	if st.syncState().Status != "idle" {
		t.Fatalf("sync state: %+v", st.syncState())
	}
}

// ── AC 20: FullRebuild ≡ IncrementalSync ──

type historyStep struct {
	name  string
	apply func(r *scratchRepo, p string) // p is "" or "spine/"
}

func equivalenceHistory() []historyStep {
	initiative := "initiatives/INIT-001/initiative.md"
	task2 := strings.Replace(taskPath, "TASK-001", "TASK-002", 1)
	return []historyStep{
		{"seed", func(r *scratchRepo, p string) {
			r.write(p+"governance/charter.md", "---\ntype: Governance\ntitle: Charter\nstatus: Foundational\n---\n\n# Charter\n")
			r.write(p+initiative, "---\nid: INIT-001\ntype: Initiative\ntitle: Init\nstatus: Pending\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n\n# I\n")
			r.write(p+taskPath, taskDoc("Pending", "Task one"))
			r.write(p+"templates/task-template.md", taskDoc("Pending", "Template"))
			r.write(p+"templates/t.md", strings.Replace(taskDoc("Pending", "Task two"), "TASK-001", "TASK-002", 1))
			r.write("workflows/task-default.yaml", reviewedWorkflow)
			r.write(BranchProtectionConfigPath, protectMain)
			r.write("outside/notes.md", "---\ntype: Governance\ntitle: Outside\nstatus: Living Document\n---\n\n# N\n")
		}},
		{"modify_and_subdir_workflow", func(r *scratchRepo, p string) {
			r.write(p+taskPath, taskDoc("In Progress", "Task one"))
			r.write(p+"workflows/spine-wf.yaml", strings.Replace(reviewlessWorkflow, "id: task-default", "id: spine-wf", 1))
		}},
		{"rename_out_of_templates", func(r *scratchRepo, p string) {
			r.mv(p+"templates/t.md", p+task2)
		}},
		{"nested_workflows_and_bp_edit", func(r *scratchRepo, p string) {
			r.write("workflows/steps/validate.yaml", stepProfile)
			r.write(p+"workflows/steps/x.yaml", stepProfile)
			r.write(BranchProtectionConfigPath, protectMain+"  - branch: \"release/*\"\n    protections: [no-delete]\n")
		}},
		{"rejected_workflow_and_delete", func(r *scratchRepo, p string) {
			r.write("workflows/task-default.yaml", pocFoldWorkflow)
			r.remove(p + "governance/charter.md")
		}},
		{"renames_out_of_scope", func(r *scratchRepo, p string) {
			r.mv(p+task2, p+"templates/t2.md")
			r.mv(p+initiative, "moved/initiative.md")
		}},
		{"rename_into_scope_fix_workflow_drop_bp", func(r *scratchRepo, p string) {
			r.mv("outside/notes.md", p+"governance/notes.md")
			r.write("workflows/task-default.yaml", strings.Replace(reviewedWorkflow, `version: "1.0"`, `version: "1.2"`, 1))
			r.remove(BranchProtectionConfigPath)
		}},
		{"rejected_new_workflow_and_workflow_rename", func(r *scratchRepo, p string) {
			r.write(p+"workflows/bad.yaml", pocFoldOutcome)
			r.mv(p+"workflows/spine-wf.yaml", p+"workflows/spine-wf2.yaml")
		}},
		{"links_and_template_edit", func(r *scratchRepo, p string) {
			r.write(p+taskPath, strings.Replace(taskDoc("Completed", "Task one"), "---\n\n#",
				"links:\n  - type: related_to\n    target: /governance/notes.md\n---\n\n#", 1))
			r.write(p+"templates/task-template.md", taskDoc("Completed", "Template v2"))
		}},
	}
}

func TestProjection_FullRebuildEqualsIncrementalStore(t *testing.T) {
	for _, dir := range []string{"", "spine"} {
		t.Run(fmt.Sprintf("artifacts_dir=%q", dir), func(t *testing.T) {
			ctx := context.Background()
			prefix := ""
			if dir != "" {
				prefix = dir + "/"
			}
			repo := newScratchRepo(t)
			inc := allRowsStore{newMapStore()}
			svc := NewService(repo.git, inc, nil, 0)
			svc.WithArtifactsDir(dir)

			for i, step := range equivalenceHistory() {
				step.apply(repo, prefix)
				head := repo.commit(step.name)
				if i == 0 {
					if err := svc.FullRebuild(ctx); err != nil {
						t.Fatal(err)
					}
				} else if err := svc.IncrementalSync(ctx); err != nil {
					t.Fatalf("%s: IncrementalSync: %v", step.name, err)
				}
				if got := inc.syncState(); got.Status != "idle" || got.LastSyncedCommit != head {
					t.Fatalf("%s: incremental state %+v", step.name, got)
				}

				rebuilt := allRowsStore{inc.clone()}
				rsvc := NewService(repo.git, rebuilt, nil, 0)
				rsvc.WithArtifactsDir(dir)
				if err := rsvc.FullRebuild(ctx); err != nil {
					t.Fatalf("%s: FullRebuild: %v", step.name, err)
				}
				assertEquivalentStores(t, step.name, inc.mapStore, rebuilt.mapStore)
			}

			// Sanity: the history exercised the interesting cases.
			if r := inc.row("workflows/task-default.yaml"); r == nil || r.Status != "Active" {
				t.Fatalf("final task-default row: %+v", r)
			}
			if inc.row(prefix+"workflows/bad.yaml") != nil {
				t.Fatal("a rejected workflow without a previous row must not be projected")
			}
			if inc.row("workflows/steps/validate.yaml") != nil {
				t.Fatal("nested files are not workflows")
			}
		})
	}
}

func assertEquivalentStores(t *testing.T, step string, inc, reb *mapStore) {
	t.Helper()
	// Artifacts: every column except source_commit.
	if a, b := artifactView(inc), artifactView(reb); !reflect.DeepEqual(a, b) {
		t.Fatalf("%s: artifacts differ\nincremental: %v\nrebuild:     %v", step, a, b)
	}
	// Links as sets.
	if a, b := linkView(inc), linkView(reb); !reflect.DeepEqual(a, b) {
		t.Fatalf("%s: links differ\nincremental: %v\nrebuild:     %v", step, a, b)
	}
	// Workflows: every column; source_commit compared for kept
	// (quarantined) rows only — an accepted row is stamped with the
	// commit that changed it incrementally and with head by a rebuild.
	if a, b := workflowView(inc), workflowView(reb); !reflect.DeepEqual(a, b) {
		t.Fatalf("%s: workflows differ\nincremental: %v\nrebuild:     %v", step, a, b)
	}
	// Branch protection rows, without source_commit.
	if !reflect.DeepEqual(inc.bpRules, reb.bpRules) {
		t.Fatalf("%s: branch protection differs\nincremental: %+v\nrebuild:     %+v", step, inc.bpRules, reb.bpRules)
	}
	// Execution projections of live tasks, without blocked/blocked_by
	// (order-dependent in both entry points). An incremental delete
	// leaves the deleted task's execution row behind (pre-existing,
	// recorded as a follow-up), so only rows of projected tasks compare.
	if a, b := execView(inc), execView(reb); !reflect.DeepEqual(a, b) {
		t.Fatalf("%s: execution projections differ\nincremental: %v\nrebuild:     %v", step, a, b)
	}
}

func artifactView(s *mapStore) map[string]string {
	out := map[string]string{}
	for k, a := range s.artifacts {
		cp := *a
		cp.SourceCommit = ""
		out[k] = fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%v|%s", cp.ArtifactID, cp.ArtifactType, cp.Title, cp.Status,
			normJSON(cp.Metadata), cp.Content, normJSON(cp.Links), cp.Repositories, cp.ContentHash)
	}
	return out
}

func linkView(s *mapStore) map[string][]string {
	out := map[string][]string{}
	for src, links := range s.links {
		if len(links) == 0 {
			continue
		}
		var set []string
		for _, l := range links {
			set = append(set, l.TargetPath+"|"+l.LinkType)
		}
		sort.Strings(set)
		out[src] = set
	}
	return out
}

func workflowView(s *mapStore) map[string]string {
	out := map[string]string{}
	for k, w := range s.workflows {
		commit := ""
		if w.Status == "Quarantined" {
			commit = w.SourceCommit
		}
		out[k] = strings.Join([]string{w.WorkflowID, w.Name, w.Version, w.Status,
			normJSON(w.AppliesTo), normJSON(w.Definition), commit}, "|")
	}
	return out
}

func execView(s *mapStore) map[string]store.ExecutionProjection {
	out := map[string]store.ExecutionProjection{}
	for k, e := range s.execs {
		if _, live := s.artifacts[k]; !live {
			continue
		}
		cp := *e
		cp.Blocked, cp.BlockedBy = false, nil
		out[k] = cp
	}
	return out
}

func normJSON(b []byte) string {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "!" + string(b)
	}
	out, _ := json.Marshal(v)
	return string(out)
}
