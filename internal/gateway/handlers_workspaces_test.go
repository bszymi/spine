package gateway

import "testing"

func TestRedactDatabaseURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"full URL", "postgres://spine:secret@localhost:5432/spine_db", "localhost:5432/spine_db"},
		{"no password", "postgres://spine@localhost:5432/spine_db", "localhost:5432/spine_db"},
		{"no user", "postgres://localhost:5432/spine_db", "localhost:5432/spine_db"},
		{"with sslmode", "postgres://user:pass@host:5432/db?sslmode=disable", "host:5432/db"},
		{"opaque string", "not-a-url", "not-a-url"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactDatabaseURL(tt.url)
			if got != tt.want {
				t.Errorf("redactDatabaseURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}
