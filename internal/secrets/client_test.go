package secrets_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/secrets"
)

func TestWorkspaceRefRoundTrip(t *testing.T) {
	cases := []struct {
		workspaceID string
		purpose     string
		want        secrets.SecretRef
	}{
		{"acme", secrets.PurposeRuntimeDB, "secret-store://workspaces/acme/runtime_db"},
		{"default", secrets.PurposeProjectionDB, "secret-store://workspaces/default/projection_db"},
		{"ws-1", secrets.PurposeGit, "secret-store://workspaces/ws-1/git"},
	}

	for _, tc := range cases {
		t.Run(string(tc.want), func(t *testing.T) {
			got := secrets.WorkspaceRef(tc.workspaceID, tc.purpose)
			if got != tc.want {
				t.Fatalf("WorkspaceRef = %q, want %q", got, tc.want)
			}
			ws, purpose, err := secrets.ParseRef(got)
			if err != nil {
				t.Fatalf("ParseRef(%q) returned error: %v", got, err)
			}
			if ws != tc.workspaceID || purpose != tc.purpose {
				t.Fatalf("ParseRef(%q) = (%q, %q), want (%q, %q)",
					got, ws, purpose, tc.workspaceID, tc.purpose)
			}
		})
	}
}

func TestParseRefRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"not-a-ref",
		"secret-store://",
		"secret-store://workspaces/",
		"secret-store://workspaces/acme",
		"secret-store://workspaces/acme/",
		"secret-store://workspaces//runtime_db",
		"secret-store://workspaces/acme/runtime_db/extra",
		"https://workspaces/acme/runtime_db",
		"secret-store://workspaces/acme/unknown_purpose",
		"secret-store://workspaces/acme/RUNTIME_DB",
	}

	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, _, err := secrets.ParseRef(secrets.SecretRef(in))
			if !errors.Is(err, secrets.ErrInvalidRef) {
				t.Fatalf("ParseRef(%q): expected ErrInvalidRef, got %v", in, err)
			}
		})
	}
}

func TestSecretValueRedactsString(t *testing.T) {
	v := secrets.NewSecretValue([]byte("super-secret-token"))

	if got := v.String(); got != "<redacted>" {
		t.Fatalf("String() = %q, want \"<redacted>\"", got)
	}
	// Each verb is exercised explicitly to prove no format path leaks.
	//nolint:gocritic // intentional Sprintf to verify %v does not leak.
	if got := fmt.Sprintf("%v", v); strings.Contains(got, "super-secret-token") {
		t.Fatalf("fmt %%v leaked secret: %q", got)
	}
	//nolint:gocritic,staticcheck // intentional Sprintf to verify %s does not leak.
	if got := fmt.Sprintf("%s", v); strings.Contains(got, "super-secret-token") {
		t.Fatalf("fmt %%s leaked secret: %q", got)
	}
	if got := fmt.Sprintf("%#v", v); strings.Contains(got, "super-secret-token") {
		t.Fatalf("fmt %%#v leaked secret: %q", got)
	}
}

func TestSecretValueRedactsJSON(t *testing.T) {
	v := secrets.NewSecretValue([]byte("super-secret-token"))
	out, err := json.Marshal(struct {
		Password secrets.SecretValue `json:"password"`
	}{Password: v})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(out), "super-secret-token") {
		t.Fatalf("json marshal leaked secret: %s", out)
	}
	// json.Marshal escapes "<" and ">" as < / > by default.
	if !strings.Contains(string(out), "redacted") {
		t.Fatalf("expected redacted marker in %s", out)
	}
}

func TestSecretValueRedactsSlog(t *testing.T) {
	v := secrets.NewSecretValue([]byte("super-secret-token"))

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("loaded secret", "value", v)

	if strings.Contains(buf.String(), "super-secret-token") {
		t.Fatalf("slog leaked secret: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "<redacted>") {
		t.Fatalf("expected redacted marker in slog output: %s", buf.String())
	}
}

func TestSecretValueRevealReturnsBytes(t *testing.T) {
	want := []byte("plain-text-credential")
	v := secrets.NewSecretValue(want)
	if got := v.Reveal(); !bytes.Equal(got, want) {
		t.Fatalf("Reveal() = %q, want %q", got, want)
	}
}

func TestSentinelErrorsAreDistinguishable(t *testing.T) {
	all := []error{
		secrets.ErrSecretNotFound,
		secrets.ErrAccessDenied,
		secrets.ErrSecretStoreDown,
		secrets.ErrInvalidRef,
	}
	for i, a := range all {
		for j, b := range all {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Fatalf("sentinel %v matches sibling %v", a, b)
			}
		}
	}
}
