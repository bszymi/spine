package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestParseGitPath(t *testing.T) {
	tests := []struct {
		name       string
		pattern    string
		url        string
		wantWsID   string
		wantPath   string
	}{
		{
			name:     "workspace with info refs",
			pattern:  "/git/{workspace_id}/*",
			url:      "/git/ws-1/info/refs",
			wantWsID: "ws-1",
			wantPath: "/info/refs",
		},
		{
			name:     "workspace with upload-pack",
			pattern:  "/git/{workspace_id}/*",
			url:      "/git/ws-1/git-upload-pack",
			wantWsID: "ws-1",
			wantPath: "/git-upload-pack",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up a chi router to populate URL params.
			r := chi.NewRouter()
			var gotWsID, gotPath string
			r.HandleFunc(tt.pattern, func(_ http.ResponseWriter, r *http.Request) {
				gotWsID, gotPath = parseGitPath(r)
			})

			req := httptest.NewRequest("GET", tt.url, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if gotWsID != tt.wantWsID {
				t.Errorf("workspaceID = %q, want %q", gotWsID, tt.wantWsID)
			}
			if gotPath != tt.wantPath {
				t.Errorf("gitPath = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

func TestParseGitPath_SingleMode(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantWsID string
		wantPath string
	}{
		{
			name:     "info refs no workspace",
			url:      "/git/info/refs",
			wantWsID: "",
			wantPath: "/info/refs",
		},
		{
			name:     "upload-pack no workspace",
			url:      "/git/git-upload-pack",
			wantWsID: "",
			wantPath: "/git-upload-pack",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := chi.NewRouter()
			var gotWsID, gotPath string
			r.HandleFunc("/git/*", func(_ http.ResponseWriter, r *http.Request) {
				gotWsID, gotPath = parseGitPath(r)
			})

			req := httptest.NewRequest("GET", tt.url, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if gotWsID != tt.wantWsID {
				t.Errorf("workspaceID = %q, want %q", gotWsID, tt.wantWsID)
			}
			if gotPath != tt.wantPath {
				t.Errorf("gitPath = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

func TestIsGitProtocolPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"info/refs", true},
		{"git-upload-pack", true},
		{"git-receive-pack", true},
		{"HEAD", true},
		{"objects/pack/pack-abc.pack", true},
		{"ws-1/info/refs", false},
		{"my-workspace/git-upload-pack", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isGitProtocolPath(tt.path)
			if got != tt.want {
				t.Errorf("isGitProtocolPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestHandleGit_NilHandler(t *testing.T) {
	s := &Server{} // gitHTTP is nil

	req := httptest.NewRequest("GET", "/git/info/refs", nil)
	w := httptest.NewRecorder()

	s.handleGit(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when gitHTTP is nil, got %d", w.Code)
	}
}
