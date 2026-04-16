package main

import (
	"reflect"
	"strings"
	"testing"
)

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
