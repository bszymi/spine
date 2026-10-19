package worktreefs_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// scannedPackages are the packages whose non-test files must do all
// working-tree file I/O through worktreefs (INIT-023 TASK-004 AC9). The
// last three had no filesystem calls when the scan was introduced; they
// are scanned so a future sink cannot appear there unnoticed.
// core/checkrunner is excluded on purpose: it runs governed commands in a
// working directory (code-execution plane), it writes no file.
var scannedPackages = []string{
	"core/artifact",
	"core/workflow",
	"adapters/git",
	"core/engine",
	"core/divergence",
	"adapters/projection",
}

// forbidden lists, per import path, the identifiers that touch the
// filesystem by name. "*" forbids every identifier of the package.
var forbidden = map[string]map[string]bool{
	"os": set("WriteFile", "ReadFile", "Open", "OpenFile", "Create", "CreateTemp", "Mkdir", "MkdirAll",
		"MkdirTemp", "Remove", "RemoveAll", "Rename", "Stat", "Lstat", "ReadDir", "Readlink", "Symlink",
		"Chmod", "Truncate", "Link", "Chtimes", "Chown", "Lchown", "DirFS", "NewFile", "OpenRoot", "OpenInRoot"),
	"path/filepath": set("Walk", "WalkDir", "EvalSymlinks", "Glob"),
	"io/ioutil":     {"*": true},
	"syscall": set("Open", "Openat", "Creat", "Unlink", "Unlinkat", "Rename", "Renameat", "Mkdir", "Mkdirat",
		"Rmdir", "Symlink", "Link", "Truncate", "Chmod"),
}

// allowed are the reviewed exceptions: private temp files and directories
// outside any working tree, never a repo-joined path.
var allowed = map[string]bool{
	"adapters/git/cli.go:createAskpassScript": true, // GIT_ASKPASS helper in os.TempDir
	"adapters/git/cli.go:CLIClient.Close":     true, // removes that helper
	"adapters/git/branchwrite.go:EnterBranch": true, // MkdirTemp/RemoveAll of the private worktree parent (RemoveAll never follows links inside it)
	// MkdirTemp/RemoveAll of the private GIT_INDEX_FILE directory in
	// os.TempDir (INIT-023 TASK-005): plumbing only, no working tree.
	"adapters/git/commitbinder.go:CLIClient.CommitFilesOnto": true,
	// INIT-023 TASK-012: EvalSymlinks of the tree ROOT to key the shared
	// working-tree lock (never a path inside the tree, nothing opened),
	// and an Lstat existence check of <git-dir>/index.lock before a
	// restore (read-only, never followed, never removed).
	"adapters/git/treelock.go:treeReal":              true,
	"adapters/git/treelock.go:CLIClient.restoreHeld": true,
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// TestNoUnconfinedWorktreeIO is AC9: a go/ast scan of the non-test files of
// the packages that write working trees finds no direct filesystem call
// outside the reviewed exceptions, and resolveToExistingAncestor (the
// EvalSymlinks-based check F5 bypassed) is gone.
//
// Mutation check: re-adding `os.WriteFile(fullPath, …)` in
// core/artifact/service.go's writeAndCommit fails this test.
func TestNoUnconfinedWorktreeIO(t *testing.T) {
	moduleRoot := filepath.Join("..", "..")
	var violations []string
	for _, pkg := range scannedPackages {
		dir := filepath.Join(moduleRoot, filepath.FromSlash(pkg))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", pkg, err)
		}
		scanned := 0
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			scanned++
			rel := pkg + "/" + name
			violations = append(violations, scanFile(t, filepath.Join(dir, name), rel)...)
		}
		if scanned == 0 {
			t.Fatalf("%s: no Go files scanned — the package moved; update scannedPackages", pkg)
		}
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("unconfined filesystem call (use core/worktreefs): %s", v)
	}
}

func scanFile(t *testing.T, path, rel string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}

	// Local name -> import path, for the packages we police.
	local := map[string]string{}
	var out []string
	for _, imp := range file.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if _, policed := forbidden[p]; !policed {
			continue
		}
		name := filepath.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			out = append(out, rel+": dot-import of "+p)
			continue
		}
		local[name] = p
	}

	for _, decl := range file.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		scope := "<package>"
		if isFunc {
			scope = funcName(fn)
			if strings.Contains(rel, "core/artifact/") && fn.Name.Name == "resolveToExistingAncestor" {
				out = append(out, rel+": resolveToExistingAncestor must stay deleted")
			}
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			p, ok := local[id.Name]
			if !ok {
				return true
			}
			names := forbidden[p]
			if !names["*"] && !names[sel.Sel.Name] {
				return true
			}
			if allowed[rel+":"+scope] {
				return true
			}
			pos := fset.Position(sel.Pos())
			out = append(out, rel+":"+strconv.Itoa(pos.Line)+" "+scope+": "+p+"."+sel.Sel.Name)
			return true
		})
	}
	return out
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if idx, ok := typ.(*ast.IndexExpr); ok {
		typ = idx.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}
