package git_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/testutil"
)

func TestRewriteRemoteURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		username string
		token    string
		want     string
		wantErr  bool
	}{
		{
			name:  "empty token returns unchanged",
			url:   "https://github.com/org/repo.git",
			token: "",
			want:  "https://github.com/org/repo.git",
		},
		{
			name:     "github HTTPS with default username",
			url:      "https://github.com/org/repo.git",
			username: "",
			token:    "ghp_abc123",
			want:     "https://x-access-token:ghp_abc123@github.com/org/repo.git",
		},
		{
			name:     "github HTTPS with custom username",
			url:      "https://github.com/org/repo.git",
			username: "oauth2",
			token:    "glpat_xyz",
			want:     "https://oauth2:glpat_xyz@github.com/org/repo.git",
		},
		{
			name:  "SSH URL unchanged",
			url:   "git@github.com:org/repo.git",
			token: "ghp_abc123",
			want:  "git@github.com:org/repo.git",
		},
		{
			name:     "URL with existing credentials gets replaced",
			url:      "https://old-user:old-pass@github.com/org/repo.git",
			username: "x-access-token",
			token:    "new-token",
			want:     "https://x-access-token:new-token@github.com/org/repo.git",
		},
		{
			name:     "gitlab HTTPS",
			url:      "https://gitlab.com/group/project.git",
			username: "oauth2",
			token:    "glpat_abc",
			want:     "https://oauth2:glpat_abc@gitlab.com/group/project.git",
		},
		{
			name:     "bitbucket HTTPS",
			url:      "https://bitbucket.org/team/repo.git",
			username: "x-token-auth",
			token:    "ATBB_abc",
			want:     "https://x-token-auth:ATBB_abc@bitbucket.org/team/repo.git",
		},
		{
			name:    "plain HTTP rejected",
			url:     "http://insecure.example.com/repo.git",
			token:   "secret",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := git.RewriteRemoteURL(tt.url, tt.username, tt.token)
			if (err != nil) != tt.wantErr {
				t.Fatalf("RewriteRemoteURL() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("RewriteRemoteURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "URL with credentials",
			url:  "https://x-access-token:ghp_secret@github.com/org/repo.git",
			want: "https://x-access-token:xxxxx@github.com/org/repo.git",
		},
		{
			name: "URL without credentials",
			url:  "https://github.com/org/repo.git",
			want: "https://github.com/org/repo.git",
		},
		{
			name: "invalid URL",
			url:  "not a url at all ::::",
			want: "***",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := git.RedactURL(tt.url)
			if got != tt.want {
				t.Errorf("RedactURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		wantErr string
	}{
		{name: "empty ref is allowed (means HEAD)", ref: ""},
		{name: "plain branch", ref: "feature/login"},
		{name: "tag-like", ref: "v1.2.3"},
		{name: "SHA-like", ref: "abcdef0123456789"},
		{name: "flag injection", ref: "--upload-pack=evil", wantErr: "must not start with '-'"},
		{name: "control character", ref: "foo\x01bar", wantErr: "unsafe character"},
		{name: "DEL character", ref: "foo\x7fbar", wantErr: "unsafe character"},
		{name: "space", ref: "foo bar", wantErr: "unsafe character"},
		{name: "tilde", ref: "foo~1", wantErr: "unsafe character"},
		{name: "caret", ref: "foo^1", wantErr: "unsafe character"},
		{name: "colon", ref: "foo:bar", wantErr: "unsafe character"},
		{name: "backslash", ref: "foo\\bar", wantErr: "unsafe character"},
		{name: "double-dot traversal", ref: "foo..bar", wantErr: "'..'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := git.ValidateRef(tt.ref)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateRef(%q) = %v, want nil", tt.ref, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateRef(%q) = nil, want error containing %q", tt.ref, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateRef(%q) error %q does not contain %q", tt.ref, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateCloneURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "https ok", url: "https://github.com/org/repo.git"},
		{name: "ssh ok", url: "ssh://git@github.com/org/repo.git"},
		{name: "scp-like ssh ok", url: "git@github.com:org/repo.git"},
		{name: "empty", url: "", wantErr: "empty"},
		{name: "ext:: command execution", url: "ext::bash -c 'id'", wantErr: "ext::"},
		{name: "ext:: case-insensitive", url: "EXT::bash", wantErr: "ext::"},
		{name: "file:// scheme", url: "file:///etc/passwd", wantErr: "file://"},
		{name: "File:// case-insensitive", url: "File:///etc/passwd", wantErr: "file://"},
		{name: "leading dash (flag injection)", url: "--upload-pack=evil", wantErr: "starting with '-'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := git.ValidateCloneURL(tt.url)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateCloneURL(%q) = %v, want nil", tt.url, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateCloneURL(%q) = nil, want error containing %q", tt.url, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateCloneURL(%q) error %q does not contain %q", tt.url, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestPushWithToken(t *testing.T) {
	// Set up a repo with a bare remote (local, no auth needed).
	repo := testutil.NewTempRepo(t)
	bare := setupRemote(t, repo)
	ctx := context.Background()

	// Get the remote URL.
	cmd := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url")
	cmd.Dir = repo
	urlOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("get remote URL: %v\n%s", err, urlOut)
	}
	remoteURL := strings.TrimSpace(string(urlOut))

	// Since the bare remote is a local path (not HTTPS), the token rewrite
	// will be a no-op (SSH/local paths are not rewritten). Verify push works.
	client := git.NewCLIClient(repo, git.WithPushToken("test-token", "x-access-token"))

	testutil.WriteFile(t, repo, "token-test.md", "# Token Test")
	stageFile(t, repo, "token-test.md")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "token test"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := client.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("Push with token: %v", err)
	}
	_ = bare
	_ = remoteURL
}

func TestPushToken_CredentialHelperTakesPriority(t *testing.T) {
	// When both credential helper and push token are set,
	// the credential helper takes priority (resolveRemote returns remote name as-is).
	repo := testutil.NewTempRepo(t)
	setupRemote(t, repo)
	ctx := context.Background()

	client := git.NewCLIClient(repo,
		git.WithCredentialHelper("/nonexistent/helper"),
		git.WithPushToken("should-not-be-used", ""),
	)

	testutil.WriteFile(t, repo, "priority-test.md", "# Priority Test")
	stageFile(t, repo, "priority-test.md")
	if _, err := client.Commit(ctx, git.CommitOpts{Message: "priority test"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Push should work — credential helper doesn't interfere with local push,
	// and the token is not used because credential helper takes priority.
	if err := client.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("Push: %v", err)
	}
}
