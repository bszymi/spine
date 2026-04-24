package workspace

import (
	"strings"
	"testing"
)

func TestValidateID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		// Accept — the shapes the existing codebase and tests already use.
		{name: "ws-1", id: "ws-1"},
		{name: "ws_alpha", id: "ws_alpha"},
		{name: "spine42", id: "spine42"},
		{name: "default", id: "default"},
		{name: "uppercase ok", id: "Prod1"},
		{name: "digit lead", id: "1ws"},
		{name: "max length 63", id: strings.Repeat("a", 63)},

		// Reject — acceptance criteria shapes.
		{name: "empty", id: "", wantErr: true},
		{name: "traversal dotdot", id: "../x", wantErr: true},
		{name: "absolute path", id: "/tmp/x", wantErr: true},
		{name: "contains slash", id: "a/b", wantErr: true},
		{name: "contains backslash", id: `a\b`, wantErr: true},
		{name: "whitespace", id: "ws 1", wantErr: true},
		{name: "tab", id: "ws\t1", wantErr: true},
		{name: "newline", id: "ws\n1", wantErr: true},
		{name: "leading dash (flag injection)", id: "-rm", wantErr: true},
		{name: "leading underscore", id: "_ws", wantErr: true},
		{name: "leading dot (hidden)", id: ".ws", wantErr: true},
		{name: "dot in middle", id: "ws.1", wantErr: true},
		{name: "unicode", id: "wś-1", wantErr: true},
		{name: "null byte", id: "ws\x00x", wantErr: true},
		{name: "too long", id: strings.Repeat("a", 64), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateID(tt.id)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateID(%q) = nil, want error", tt.id)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateID(%q) = %v, want nil", tt.id, err)
			}
		})
	}
}
