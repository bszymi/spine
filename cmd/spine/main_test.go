package main

import (
	"strings"
	"testing"
)

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
