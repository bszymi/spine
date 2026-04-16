package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestGuardDevModeEnv(t *testing.T) {
	cases := []struct {
		name      string
		env       string
		dev       bool
		wantError bool
	}{
		{name: "dev off, env production", env: "production", dev: false, wantError: false},
		{name: "dev off, env unspecified", env: "", dev: false, wantError: false},
		{name: "dev on, env development", env: "development", dev: true, wantError: false},
		{name: "dev on, env staging", env: "staging", dev: true, wantError: false},
		{name: "dev on, env unspecified", env: "", dev: true, wantError: false},
		{name: "dev on, env production (rejected)", env: "production", dev: true, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := guardDevModeEnv(tc.env, tc.dev)
			if tc.wantError && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestResolveRuntimeEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "production", want: "production"},
		{raw: "PRODUCTION", want: "production"},
		{raw: "  development  ", want: "development"},
		{raw: "staging", want: "staging"},
		{raw: "", want: ""},
		{raw: "nonsense", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("SPINE_ENV", tc.raw)
			if got := resolveRuntimeEnv(); got != tc.want {
				t.Fatalf("resolveRuntimeEnv() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDevModeEnabled(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{raw: "1", want: true},
		{raw: "true", want: true},
		{raw: "TRUE", want: true},
		{raw: "0", want: false},
		{raw: "false", want: false},
		{raw: "yes", want: false},
		{raw: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("SPINE_DEV_MODE", tc.raw)
			if got := devModeEnabled(); got != tc.want {
				t.Fatalf("devModeEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateOperatorToken(t *testing.T) {
	cases := []struct {
		name      string
		token     string
		wantError bool
	}{
		{name: "empty token allowed (operator endpoints 503 at request time)", token: "", wantError: false},
		{name: "16-char token rejected", token: "short-token-1234", wantError: true},
		{name: "31-char token rejected", token: strings.Repeat("a", 31), wantError: true},
		{name: "32-char token accepted", token: strings.Repeat("a", 32), wantError: false},
		{name: "64-char token accepted", token: strings.Repeat("b", 64), wantError: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOperatorToken(tc.token)
			if tc.wantError && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantError && err != nil && !strings.Contains(err.Error(), "SPINE_OPERATOR_TOKEN") {
				t.Fatalf("error should mention the env var: %v", err)
			}
		})
	}
}

func TestParseGitHTTPTrustedCIDRs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty input yields nil", raw: "", want: nil},
		{name: "single cidr", raw: "172.17.0.0/16", want: []string{"172.17.0.0/16"}},
		{name: "multiple with whitespace", raw: " 172.17.0.0/16 , 10.0.0.0/8 ", want: []string{"172.17.0.0/16", "10.0.0.0/8"}},
		{name: "trailing comma is dropped", raw: "172.17.0.0/16,", want: []string{"172.17.0.0/16"}},
		{name: "all blanks yield nil", raw: " , , ", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGitHTTPTrustedCIDRs(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseGitHTTPTrustedCIDRs(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestRequireSecureDBURL(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		insecure  string // value to set for SPINE_INSECURE_LOCAL ("" = unset)
		wantError bool
	}{
		{
			name:      "sslmode=require passes",
			url:       "postgres://u:p@h:5432/db?sslmode=require",
			wantError: false,
		},
		{
			name:      "sslmode=verify-full passes",
			url:       "postgres://u:p@h:5432/db?sslmode=verify-full",
			wantError: false,
		},
		{
			name:      "no sslmode specified passes",
			url:       "postgres://u:p@h:5432/db",
			wantError: false,
		},
		{
			name:      "sslmode=disable without opt-in is rejected",
			url:       "postgres://u:p@h:5432/db?sslmode=disable",
			wantError: true,
		},
		{
			name:      "sslmode=disable with SPINE_INSECURE_LOCAL=1 passes",
			url:       "postgres://u:p@h:5432/db?sslmode=disable",
			insecure:  "1",
			wantError: false,
		},
		{
			name:      "sslmode=disable with SPINE_INSECURE_LOCAL=0 is rejected",
			url:       "postgres://u:p@h:5432/db?sslmode=disable",
			insecure:  "0",
			wantError: true,
		},
		{
			name:      "sslmode=disable with SPINE_INSECURE_LOCAL=true is rejected (must be exactly 1)",
			url:       "postgres://u:p@h:5432/db?sslmode=disable",
			insecure:  "true",
			wantError: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.insecure == "" {
				t.Setenv("SPINE_INSECURE_LOCAL", "")
			} else {
				t.Setenv("SPINE_INSECURE_LOCAL", tc.insecure)
			}

			err := requireSecureDBURL(tc.url)
			if tc.wantError && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantError && err != nil && !strings.Contains(err.Error(), "sslmode=disable") {
				t.Fatalf("error should mention sslmode=disable: %v", err)
			}
		})
	}
}
