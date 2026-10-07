package main

// SMP_ADMIN_TOKEN strength gate (INIT-023 TASK-013, audit P3-35). The
// bearer is seeded as smp-admin into every workspace this process
// serves, so a guessable value is a cross-tenant admin credential.

import (
	"strings"
	"testing"
)

func TestValidateSMPAdminToken(t *testing.T) {
	const hex64 = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	// SMP's dev default (docker-compose.yaml): 58 characters, 24
	// distinct bytes. It passes, so SMP's dev stack is unaffected even
	// though the gate never runs in SMP.
	const smpDevDefault = "smp-admin-bootstrap-token-ca4fdbd0db8a338350477d733d4946c6"

	cases := []struct {
		name     string
		token    string
		env      string
		wantErr  bool
		wantWarn bool
	}{
		{name: "empty in production (feature off)", token: "", env: "production"},
		{name: "64-hex in production", token: hex64, env: "production"},
		{name: "SMP dev default in production", token: smpDevDefault, env: "production"},
		{name: "admin in production", token: "admin", env: "production", wantErr: true},
		{name: "31-character prefix in production", token: hex64[:31], env: "production", wantErr: true},
		{name: "ab x20 in production (2 distinct bytes)", token: strings.Repeat("ab", 20), env: "production", wantErr: true},
		{name: "admin with no SPINE_ENV", token: "admin", env: "", wantWarn: true},
		{name: "a x64 in staging (1 distinct byte)", token: strings.Repeat("a", 64), env: "staging", wantWarn: true},
		{name: "31-character prefix in development", token: hex64[:31], env: "development", wantWarn: true},
		{name: "32 characters, 8 distinct: the floor", token: strings.Repeat("abcdefgh", 4), env: "production"},
		{name: "32 characters, 7 distinct: below the floor", token: strings.Repeat("abcdefg", 5)[:32], env: "production", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.token) == 32 && strings.HasPrefix(tc.name, "32 characters") {
				// Guard the fixture itself.
				distinct := map[byte]bool{}
				for i := 0; i < len(tc.token); i++ {
					distinct[tc.token[i]] = true
				}
				if (len(distinct) >= 8) == tc.wantErr {
					t.Fatalf("fixture %q has %d distinct bytes", tc.token, len(distinct))
				}
			}
			warn, err := validateSMPAdminToken(tc.token, tc.env)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantWarn != (warn != "") {
				t.Fatalf("warning = %q, wantWarn %v", warn, tc.wantWarn)
			}
			for _, msg := range []string{warn, errString(err)} {
				if msg == "" {
					continue
				}
				if tc.token != "" && strings.Contains(msg, tc.token) {
					t.Fatalf("message echoes the token: %q", msg)
				}
				if !strings.Contains(msg, "SMP_ADMIN_TOKEN") || !strings.Contains(msg, "openssl rand -hex 32") {
					t.Fatalf("message %q must name SMP_ADMIN_TOKEN and the remedy", msg)
				}
			}
		})
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// The gate runs before any store, secret or listener is opened. With
// SPINE_ENV=production and no SPINE_SECRET_ENCRYPTION_KEY, a serve that
// skipped the gate would stop at the encryption-key gate instead, with
// an error that does not mention SMP_ADMIN_TOKEN.
func TestServeRefusesWeakSMPAdminTokenInProduction(t *testing.T) {
	t.Setenv("SPINE_ENV", "production")
	t.Setenv("SMP_ADMIN_TOKEN", "admin")
	t.Setenv("SPINE_OPERATOR_TOKEN", "")
	t.Setenv("SPINE_DEV_MODE", "")
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", "")

	err := serveCmd().RunE(serveCmd(), nil)
	if err == nil {
		t.Fatal("spine serve started with a weak SMP_ADMIN_TOKEN in production")
	}
	if !strings.Contains(err.Error(), "SMP_ADMIN_TOKEN") {
		t.Fatalf("err = %v, want the SMP_ADMIN_TOKEN gate (not a later startup gate)", err)
	}
	if strings.Contains(strings.ReplaceAll(err.Error(), "SMP_ADMIN_TOKEN", ""), "admin") {
		t.Fatalf("err echoes the token: %v", err)
	}
}
