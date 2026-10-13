package gateway_test

// INIT-023 TASK-003 AC 10 (audit F4): reading an artifact whose blob is
// over the git client's per-blob read limit is 413 payload_too_large —
// on main and on a run branch (`?ref=`, the pre-merge path that needs no
// approval) — instead of buffering the blob (an OOM for the shared
// process before the fix). A small artifact and a missing one keep
// their 200 and 404.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/testutil"
)

func TestGateway_ReadOversizedArtifact_413(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	big := f5GatewayDoc + strings.Repeat("lorem ipsum dolor sit amet\n", int(git.DefaultMaxBlobBytes)/27+1)
	writeBig := func(rel string) {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(big), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeBig("governance/big.md")
	testutil.WriteFile(t, repo, "governance/small.md", f5GatewayDoc)
	f5GatewayGit(t, repo, "add", "-A")
	f5GatewayGit(t, repo, "commit", "-q", "-m", "seed")
	f5PlantOnRunBranch(t, repo, func() { writeBig("governance/run-big.md") })
	ts, token := f5GatewayServer(t, repo)

	for _, tc := range []struct {
		label, path string
		status      int
		code        domain.ErrorCode
	}{
		{"oversized on main", "/api/v1/artifacts/governance/big.md", http.StatusRequestEntityTooLarge, domain.ErrPayloadTooLarge},
		{"oversized on a run branch", "/api/v1/artifacts/governance/run-big.md?ref=" + f5RunBranch, http.StatusRequestEntityTooLarge, domain.ErrPayloadTooLarge},
		{"small", "/api/v1/artifacts/governance/small.md", http.StatusOK, ""},
		{"missing", "/api/v1/artifacts/governance/missing.md", http.StatusNotFound, domain.ErrNotFound},
	} {
		status, er := f5Do(t, ts, token, http.MethodGet, tc.path, "")
		if status != tc.status {
			t.Errorf("%s: status %d (%+v), want %d", tc.label, status, er, tc.status)
			continue
		}
		if tc.code == "" {
			continue
		}
		if len(er.Errors) == 0 || er.Errors[0].Code != string(tc.code) {
			t.Errorf("%s: errors %+v, want code %s", tc.label, er, tc.code)
			continue
		}
		if tc.code == domain.ErrPayloadTooLarge && !strings.Contains(er.Errors[0].Message, "read limit") {
			t.Errorf("%s: message %q does not name the read limit", tc.label, er.Errors[0].Message)
		}
	}
}
