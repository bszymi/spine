package projection

import (
	"context"
	"fmt"
	"strings"

	"github.com/bszymi/spine/adapters/git/refname"
	"github.com/bszymi/spine/core/observe"
)

// WithAuthoritativeBranch makes every sync — FullRebuild and
// IncrementalSync — read refs/heads/<branch> instead of the working
// tree's HEAD (INIT-023 TASK-011, audit P3-22).
//
// Why: in standalone Spine the engine and the projection share one
// non-bare working tree, and the engine checks run branches out in it
// (core/engine/merge.go applyCommitStatus). A sync tick in that window
// projected the unmerged run branch — a relaxed
// .spine/branch-protection.yaml, changed workflows — as live state and
// stamped last_synced_commit with the run-branch SHA. `spine init-repo`
// also leaves its spine/init branch checked out.
//
//   - "" restores the default, Head(). That is right for bare
//     repositories (SMP), whose HEAD is a symbolic ref a push cannot
//     move, and it is what every embedder gets without calling this.
//   - Otherwise the value is a short branch name. It must pass
//     refname.Validate and must not start with "refs/": the projection
//     always reads the fully qualified refs/heads/<branch>, which git
//     resolves ahead of any refs/tags/… or refs/… look-alike, so a pushed
//     ref cannot shadow it.
//
// Call it during wiring, before the service is shared.
func (s *Service) WithAuthoritativeBranch(branch string) error {
	if branch == "" {
		s.authoritativeBranch = ""
		return nil
	}
	if strings.HasPrefix(branch, "refs/") {
		return fmt.Errorf("projection authoritative branch %q: give a short branch name, not a qualified ref", branch)
	}
	if err := refname.Validate(branch); err != nil {
		return fmt.Errorf("projection authoritative branch: %w", err)
	}
	s.authoritativeBranch = branch
	return nil
}

// AuthoritativeBranch returns the configured short branch name, or ""
// when the projection follows the working tree's HEAD.
func (s *Service) AuthoritativeBranch() string {
	return s.authoritativeBranch
}

// authoritativeRef is the fully qualified ref the projection reads, or
// "" when it follows HEAD.
func (s *Service) authoritativeRef() string {
	if s.authoritativeBranch == "" {
		return ""
	}
	return "refs/heads/" + s.authoritativeBranch
}

// resolveHead returns the commit both sync entry points project.
func (s *Service) resolveHead(ctx context.Context) (string, error) {
	ref := s.authoritativeRef()
	if ref == "" {
		return s.git.Head(ctx) // unchanged: SMP and every caller that configures nothing
	}
	sha, err := s.git.RefSHA(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolve authoritative ref %s: %w", ref, err)
	}
	sha = strings.TrimSpace(sha)
	if !isHexSHA(sha) {
		return "", fmt.Errorf("resolve authoritative ref %s: unexpected output %q", ref, truncateForLog(sha, 80))
	}
	return sha, nil
}

// ProbeAuthoritativeRef resolves the configured authoritative ref once
// and logs a single Warn when it does not resolve — a repository whose
// governed branch is not main (e.g. `init-repo --no-branch` on master),
// or a fresh init-repo whose spine/init branch has not been merged yet.
// The projection then reports sync_state=error until the branch exists,
// instead of silently projecting whatever is checked out. Wiring calls
// it once at startup and does not fail on it; it returns the resolution
// error for tests. It is a no-op without an authoritative branch.
func (s *Service) ProbeAuthoritativeRef(ctx context.Context) error {
	if s.authoritativeRef() == "" {
		return nil
	}
	if _, err := s.resolveHead(ctx); err != nil {
		observe.Logger(ctx).Warn("projection: authoritative branch does not resolve; projection sync will report error until it exists",
			"ref", s.authoritativeRef(),
			"error", sanitizeForLog(err.Error()),
		)
		return err
	}
	return nil
}

// isHexSHA accepts a full SHA-1 (40) or SHA-256 (64) object id in
// lowercase hex — what `git rev-parse` prints for a resolved ref.
func isHexSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
