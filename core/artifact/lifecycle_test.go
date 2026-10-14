package artifact_test

// INIT-023 TASK-017: the lifecycle reader (one decoder for the plan, R1
// and V1), the rewrite-equality check, and the shared path scope.

import (
	"testing"

	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
)

func TestLifecycleFields(t *testing.T) {
	for _, c := range []struct {
		name    string
		content string
		typ     domain.ArtifactType
		status  domain.ArtifactStatus
		ok      bool
	}{
		{"plain", "---\ntype: Task\nstatus: Pending\n---\n# x\n", "Task", "Pending", true},
		{"quoted key is read", "---\ntype: Task\n\"status\": Completed\n---\n", "Task", "Completed", true},
		{"single-quoted key", "---\ntype: Task\n'status': Completed\n---\n", "Task", "Completed", true},
		{"spaced colon", "---\ntype: Task\nstatus : Completed\n---\n", "Task", "Completed", true},
		{"complex key", "---\ntype: Task\n? status\n: Completed\n---\n", "Task", "Completed", true},
		{"continuation line", "---\ntype: Task\nstatus: Pending\n  Completed\n---\n", "Task", "Pending Completed", true},
		{"status removed", "---\ntype: Task\ntitle: x\n---\n", "Task", "", true},
		{"undeclared status kept raw", "---\ntype: Architecture\nstatus: Active\n---\n", "Architecture", "Active", true},
		{"duplicate status key", "---\ntype: Task\nstatus: Pending\nstatus: Completed\n---\n", "", "", false},
		{"unknown type", "---\ntype: Taskx\nstatus: Pending\n---\n", "", "", false},
		{"workflow type", "---\ntype: Workflow\nstatus: Active\n---\n", "", "", false},
		{"no front matter", "# just markdown\nstatus: Pending\n", "", "", false},
		{"unclosed front matter", "---\ntype: Task\nstatus: Pending\n", "", "", false},
		{"non-scalar status", "---\ntype: Task\nstatus: [a, b]\n---\n", "", "", false},
		{"symlink target text", "../other/task.md", "", "", false},
		{"empty", "", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			typ, status, ok := artifact.LifecycleFields([]byte(c.content))
			if typ != c.typ || status != c.status || ok != c.ok {
				t.Fatalf("LifecycleFields = (%q, %q, %v), want (%q, %q, %v)", typ, status, ok, c.typ, c.status, c.ok)
			}
		})
	}
}

func TestEqualExceptStatus(t *testing.T) {
	base := "---\nid: TASK-1\ntype: Task\nstatus: Pending\nlinks:\n  - type: parent\n    target: /e.md\n---\n# body\n"
	for _, c := range []struct {
		name string
		b    string
		want bool
	}{
		{"status only", "---\nid: TASK-1\ntype: Task\nstatus: Completed\nlinks:\n  - type: parent\n    target: /e.md\n---\n# body\n", true},
		{"identical", base, true},
		{"key reordered", "---\ntype: Task\nid: TASK-1\nstatus: Completed\nlinks:\n  - type: parent\n    target: /e.md\n---\n# body\n", true},
		{"id swallowed", "---\nstatus: Completed\ntype: Task\nlinks:\n  - type: parent\n    target: /e.md\n---\n# body\n", false},
		{"link changed", "---\nid: TASK-1\ntype: Task\nstatus: Completed\nlinks:\n  - type: blocked_by\n    target: /e.md\n---\n# body\n", false},
		{"body changed", "---\nid: TASK-1\ntype: Task\nstatus: Completed\nlinks:\n  - type: parent\n    target: /e.md\n---\n# other\n", false},
		{"unparseable", "---\nid: TASK-1\nid: TASK-2\n---\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := artifact.EqualExceptStatus([]byte(base), []byte(c.b)); got != c.want {
				t.Fatalf("EqualExceptStatus = %v, want %v", got, c.want)
			}
		})
	}
}

func TestPathScope(t *testing.T) {
	root := artifact.NewPathScope("")
	for _, in := range []string{"", ".", "./", "/", "  "} {
		if artifact.NewPathScope(in) != root {
			t.Errorf("NewPathScope(%q) must be the root", in)
		}
	}
	sub := artifact.NewPathScope("spine")
	for _, in := range []string{"spine", "./spine", "spine/", "./spine/", " spine "} {
		if artifact.NewPathScope(in) != sub {
			t.Errorf("NewPathScope(%q) = %+v, want spine", in, artifact.NewPathScope(in))
		}
	}
	if sub.Dir() != "spine" || root.Dir() != "" {
		t.Fatalf("Dir: %q %q", sub.Dir(), root.Dir())
	}

	for _, c := range []struct {
		scope   artifact.PathScope
		path    string
		key     string
		ok      bool
		govOK   bool
		comment string
	}{
		{root, "initiatives/x.md", "initiatives/x.md", true, true, "root"},
		{root, "templates/x.md", "", false, false, "templates excluded"},
		{root, "src/x.go", "", false, false, "not markdown"},
		{root, "a/‮x.md", "a/‮x.md", true, false, "bidi control: projected never"},
		{sub, "spine/initiatives/x.md", "initiatives/x.md", true, true, "in dir"},
		{sub, "initiatives/x.md", "", false, false, "outside dir"},
		{sub, "spine/templates/x.md", "", false, false, "templates under dir"},
		{sub, "spine/x.yaml", "", false, false, "not markdown"},
		{sub, "spinex/x.md", "", false, false, "prefix is a directory"},
	} {
		key, ok := c.scope.ArtifactKey(c.path)
		if key != c.key || ok != c.ok {
			t.Errorf("%s: ArtifactKey(%q) = (%q, %v), want (%q, %v)", c.comment, c.path, key, ok, c.key, c.ok)
		}
		if _, gok := c.scope.GovernedArtifactKey(c.path); gok != c.govOK {
			t.Errorf("%s: GovernedArtifactKey(%q) ok = %v, want %v", c.comment, c.path, gok, c.govOK)
		}
	}

	if got := sub.RepoPath("initiatives/x.md"); got != "spine/initiatives/x.md" {
		t.Errorf("RepoPath = %q", got)
	}
	if got := sub.RepoPath("/initiatives/x.md"); got != "spine/initiatives/x.md" {
		t.Errorf("RepoPath with a leading slash = %q", got)
	}
	if got := root.RepoPath("/initiatives/x.md"); got != "initiatives/x.md" {
		t.Errorf("root RepoPath = %q", got)
	}
}
