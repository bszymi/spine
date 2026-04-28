package workspace

import (
	"errors"
	"testing"
)

// TestValidateStoredRefMatchesWorkspace covers the cross-tenant
// rejection path that protects DBProvider.Resolve from a registry
// row whose `database_url` ref points at another workspace's
// runtime_db. This is a unit test (no registry DB), since the
// validation is pure-functional.
func TestValidateStoredRefMatchesWorkspace(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		workspaceID string
		wantErr     error
	}{
		{
			name:        "literal URL passes",
			raw:         "postgres://localhost/wsa",
			workspaceID: "wsa",
		},
		{
			name:        "matching ref passes",
			raw:         "secret-store://workspaces/wsa/runtime_db",
			workspaceID: "wsa",
		},
		{
			name:        "cross-tenant ref rejected",
			raw:         "secret-store://workspaces/wsb/runtime_db",
			workspaceID: "wsa",
			wantErr:     ErrWorkspaceUnavailable,
		},
		{
			name:        "wrong purpose (git) rejected",
			raw:         "secret-store://workspaces/wsa/git",
			workspaceID: "wsa",
			wantErr:     ErrWorkspaceUnavailable,
		},
		{
			name:        "wrong purpose (projection_db) rejected",
			raw:         "secret-store://workspaces/wsa/projection_db",
			workspaceID: "wsa",
			wantErr:     ErrWorkspaceUnavailable,
		},
		{
			name:        "malformed ref passes (downstream Get returns ErrInvalidRef)",
			raw:         "secret-store://garbage",
			workspaceID: "wsa",
		},
		{
			name:        "empty value passes",
			raw:         "",
			workspaceID: "wsa",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateStoredRefMatchesWorkspace(c.raw, c.workspaceID)
			if c.wantErr == nil {
				if err != nil {
					t.Errorf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Errorf("err = %v, want %v", err, c.wantErr)
			}
		})
	}
}
