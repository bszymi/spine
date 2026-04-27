package secrets_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/secrets"
	"github.com/bszymi/spine/internal/secrets/contract"
)

func writeSecret(t *testing.T, root string, ref secrets.SecretRef, value string) {
	t.Helper()
	ws, purpose, err := secrets.ParseRef(ref)
	if err != nil {
		t.Fatalf("ParseRef(%q): %v", ref, err)
	}
	dir := filepath.Join(root, "workspaces", ws)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, purpose+".json"), body, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func newFileClient(t *testing.T) (*secrets.FileClient, string) {
	t.Helper()
	root := t.TempDir()
	c, err := secrets.NewFileClient(secrets.FileConfig{Root: root})
	if err != nil {
		t.Fatalf("NewFileClient: %v", err)
	}
	return c, root
}

func TestNewFileClient_RejectsMissingRoot(t *testing.T) {
	if _, err := secrets.NewFileClient(secrets.FileConfig{Root: ""}); err == nil {
		t.Fatalf("expected error for empty root")
	}
	if _, err := secrets.NewFileClient(secrets.FileConfig{Root: "/no/such/path-xxx"}); err == nil {
		t.Fatalf("expected error for missing root")
	}
}

func TestNewFileClient_RejectsFileAsRoot(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := secrets.NewFileClient(secrets.FileConfig{Root: path}); err == nil {
		t.Fatalf("expected error for non-directory root")
	}
}

func TestFileClient_GetReturnsValue(t *testing.T) {
	c, root := newFileClient(t)
	ref := secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB)
	writeSecret(t, root, ref, "postgres://u:p@h/db")

	val, ver, err := c.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := string(val.Reveal()); got != "postgres://u:p@h/db" {
		t.Fatalf("Reveal() = %q", got)
	}
	if ver == "" {
		t.Fatalf("VersionID should be non-empty")
	}
}

func TestFileClient_VersionChangesOnEdit(t *testing.T) {
	c, root := newFileClient(t)
	ref := secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB)
	path := filepath.Join(root, "workspaces", "acme", "runtime_db.json")

	writeSecret(t, root, ref, "v1")
	_, ver1, err := c.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get v1: %v", err)
	}

	// Force mtime forward so the version change is observable even on
	// filesystems with coarse mtime granularity.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	writeSecret(t, root, ref, "v2")

	val2, ver2, err := c.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get v2: %v", err)
	}
	if string(val2.Reveal()) != "v2" {
		t.Fatalf("expected v2, got %q", val2.Reveal())
	}
	if ver1 == ver2 {
		t.Fatalf("VersionID did not change after edit: %s", ver2)
	}
}

func TestFileClient_GetMissingRefReturnsNotFound(t *testing.T) {
	c, _ := newFileClient(t)
	_, _, err := c.Get(context.Background(), secrets.WorkspaceRef("ghost", secrets.PurposeRuntimeDB))
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
}

func TestFileClient_GetInvalidRef(t *testing.T) {
	c, _ := newFileClient(t)
	_, _, err := c.Get(context.Background(), secrets.SecretRef("not-a-ref"))
	if !errors.Is(err, secrets.ErrInvalidRef) {
		t.Fatalf("expected ErrInvalidRef, got %v", err)
	}
}

func TestFileClient_GetMalformedJSONIsExplicit(t *testing.T) {
	c, root := newFileClient(t)
	ref := secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB)
	dir := filepath.Join(root, "workspaces", "acme")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime_db.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, _, err := c.Get(context.Background(), ref)
	if err == nil {
		t.Fatalf("expected error for malformed JSON")
	}
	// Must NOT be ErrSecretNotFound — the file is there, just bad.
	if errors.Is(err, secrets.ErrSecretNotFound) {
		t.Fatalf("malformed-JSON error should not match ErrSecretNotFound: %v", err)
	}
}

func TestFileClient_GetRejectsObjectShape(t *testing.T) {
	// The file provider only accepts a top-level JSON string, to
	// match the AWS provider's SecretString contract.
	c, root := newFileClient(t)
	ref := secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB)
	dir := filepath.Join(root, "workspaces", "acme")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime_db.json"), []byte(`{"value":"x"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, _, err := c.Get(context.Background(), ref); err == nil {
		t.Fatalf("expected error for object-shaped JSON")
	}
}

func TestFileClient_GetPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics required")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses POSIX permissions")
	}
	c, root := newFileClient(t)
	ref := secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB)
	writeSecret(t, root, ref, "x")
	path := filepath.Join(root, "workspaces", "acme", "runtime_db.json")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, _, err := c.Get(context.Background(), ref)
	if !errors.Is(err, secrets.ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestFileClient_InvalidateNoop(t *testing.T) {
	c, _ := newFileClient(t)
	if err := c.Invalidate(context.Background(), secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB)); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
}

func TestFileClient_InvalidateRejectsBadRef(t *testing.T) {
	c, _ := newFileClient(t)
	if err := c.Invalidate(context.Background(), secrets.SecretRef("garbage")); !errors.Is(err, secrets.ErrInvalidRef) {
		t.Fatalf("expected ErrInvalidRef, got %v", err)
	}
}

func TestFileClient_RefPathDoesNotEscapeRoot(t *testing.T) {
	// ParseRef rejects "/", "\\", ".", and ".." in segments, so a
	// malicious ref cannot escape Root via path traversal. Assert the
	// parser blocks the attack at the boundary.
	c, _ := newFileClient(t)
	for _, evil := range []string{
		"secret-store://workspaces/../runtime_db",
		"secret-store://workspaces/./runtime_db",
		`secret-store://workspaces/acme\runtime_db/runtime_db`,
		"secret-store://workspaces/../../../etc/passwd/runtime_db",
		"secret-store://workspaces/acme/../../../etc/passwd",
	} {
		_, _, err := c.Get(context.Background(), secrets.SecretRef(evil))
		if !errors.Is(err, secrets.ErrInvalidRef) {
			t.Fatalf("Get(%q): expected ErrInvalidRef, got %v", evil, err)
		}
	}
}

func TestFileClient_GetMissingRootMapsToStoreDown(t *testing.T) {
	c, root := newFileClient(t)
	ref := secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB)
	writeSecret(t, root, ref, "x")

	// Yank the mount out from under the client.
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("RemoveAll(%s): %v", root, err)
	}

	_, _, err := c.Get(context.Background(), ref)
	if !errors.Is(err, secrets.ErrSecretStoreDown) {
		t.Fatalf("expected ErrSecretStoreDown when Root vanishes, got %v", err)
	}
	if errors.Is(err, secrets.ErrSecretNotFound) {
		t.Fatalf("missing-mount must not be classified as ErrSecretNotFound: %v", err)
	}
}

func TestFileClient_Contract(t *testing.T) {
	root := t.TempDir()
	writeSecret(t, root, contract.RefRuntimeDB, contract.FixtureRuntimeDBValue)
	writeSecret(t, root, contract.RefGit, contract.FixtureGitValue)

	contract.RunContract(t, func() secrets.SecretClient {
		c, err := secrets.NewFileClient(secrets.FileConfig{Root: root})
		if err != nil {
			t.Fatalf("NewFileClient: %v", err)
		}
		return c
	})
}
