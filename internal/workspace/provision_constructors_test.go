package workspace

import (
	"context"
	"testing"
)

func TestNewDatabaseProvisioner_DefaultDir(t *testing.T) {
	p := NewDatabaseProvisioner("postgres://admin@localhost/postgres", "")
	if p == nil {
		t.Fatal("expected non-nil DatabaseProvisioner")
	}
	if p.migrationsDir != "migrations" {
		t.Errorf("expected default migrationsDir 'migrations', got %q", p.migrationsDir)
	}
}

func TestNewDatabaseProvisioner_CustomDir(t *testing.T) {
	p := NewDatabaseProvisioner("postgres://admin@localhost/postgres", "schema/migrations")
	if p.migrationsDir != "schema/migrations" {
		t.Errorf("expected 'schema/migrations', got %q", p.migrationsDir)
	}
}

func TestNewRepoProvisioner(t *testing.T) {
	p := NewRepoProvisioner("/var/repos")
	if p == nil {
		t.Fatal("expected non-nil RepoProvisioner")
	}
	if p.baseDir != "/var/repos" {
		t.Errorf("expected baseDir '/var/repos', got %q", p.baseDir)
	}
}

func TestDatabaseProvisioner_ProvisionDatabase_ConnectError(t *testing.T) {
	p := NewDatabaseProvisioner("postgres://admin:pass@invalid-host:5432/postgres?connect_timeout=1", "")
	ctx := context.Background()
	_, err := p.ProvisionDatabase(ctx, "ws-new")
	if err == nil {
		t.Skip("unexpectedly connected to database — skipping")
	}
	// Error expected (connect to admin database fails) — no panic.
}
