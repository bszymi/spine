// Package evidence provides the query and reporting surface over the
// per-repository ExecutionEvidence records committed to a workspace's
// primary repo by a Run's producers (architecture/execution-evidence.md
// §6.1). It is the production-side bridge from the deterministic
// on-disk schema to the run inspect API exposed by the gateway.
//
// EPIC-006 keeps governance authority on the primary repo: code-repo
// producers stage evidence, the merge step writes it durably under
// `/.spine/runs/{run_id}/evidence/{repository_id}.yaml`. This package
// reads those files, validates them against the domain schema, and
// shapes a deterministic summary the operator can consume.
//
// TASK-005 (INIT-014/EPIC-006) realizes the query and reporting ACs:
// run inspect / equivalent API includes evidence summary, output is
// grouped by repository, raw logs are referenced (not embedded), and
// missing evidence is visible before publication. The four behaviours
// — load, validate, summarize, surface — split across loader.go,
// summary.go, and querier.go so each piece is independently testable.
package evidence

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/adapters/git"
	"gopkg.in/yaml.v3"
)

// GitReader is the narrow Git read surface evidence loading needs.
// Mirrors the subset of [github.com/bszymi/spine/service/internal/gateway.GitReader]
// the loader uses so callers can inject a fake without dragging the
// whole gateway interface into tests.
//
// Production wiring passes a `*git.CLIClient`, which classifies
// not-found via [git.GitError] with `Kind == git.ErrKindNotFound`. The
// loader's not-found detection inspects that wrapper specifically; any
// other reader implementation MUST return a wrapped *git.GitError to
// signal absence, otherwise the loader treats absence as a generic
// read failure and the summary surfaces "load failed" rather than the
// expected "evidence not committed".
type GitReader interface {
	ReadFile(ctx context.Context, ref, path string) ([]byte, error)
}

// LoadResult bundles the load outcome. Found=false signals a missing
// file (the canonical "evidence not yet committed" case); err is
// reserved for read failures, parse errors, validation errors, or
// identity mismatches between the file's recorded keys and the
// requested ones.
type LoadResult struct {
	Evidence *domain.ExecutionEvidence
	Found    bool
}

// EvidencePath returns the canonical Git-tree path of the evidence
// file for (runID, repositoryID) per architecture/execution-evidence.md
// §6.1. Note the absence of a leading slash — `git show <ref>:<path>`
// rejects leading "/" — so the returned path is the on-disk relative
// path that pairs with a Git ref.
func EvidencePath(runID, repositoryID string) string {
	return fmt.Sprintf(".spine/runs/%s/evidence/%s.yaml", runID, repositoryID)
}

// validatePathComponent rejects path components that would let a
// caller-provided ID escape the canonical evidence prefix. RunIDs and
// RepositoryIDs come from a domain.Run that has already been validated
// upstream, but the loader is a public seam; defense-in-depth keeps
// the seam usable from less-trusted callers without surprise.
func validatePathComponent(name, value string) error {
	if value == "" {
		return fmt.Errorf("evidence: %s is empty", name)
	}
	if strings.ContainsAny(value, "/\\") || strings.Contains(value, "..") {
		return fmt.Errorf("evidence: %s %q contains path separator or traversal", name, value)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("evidence: %s %q contains control character", name, value)
		}
	}
	return nil
}

// Load reads and parses the evidence record for (runID, repositoryID)
// at the given Git ref. The returned LoadResult.Found is false when
// the file does not exist (a normal state for runs whose producers
// have not yet committed evidence); error is reserved for read
// failures classified as anything but not-found, YAML parse failures,
// schema validation failures, and identity mismatches between the
// file's stored run_id/repository_id and the requested ones.
//
// The identity cross-check matters: a typo in the on-disk path
// convention (e.g., a producer writing to `.spine/runs/<wrong>/...`)
// would otherwise produce an evidence summary that names the
// requested run while reporting checks from a different run. Refusing
// the load surfaces the producer-side bug rather than masking it.
func Load(ctx context.Context, reader GitReader, ref, runID, repositoryID string) (LoadResult, error) {
	if reader == nil {
		return LoadResult{}, errors.New("evidence: nil GitReader")
	}
	if err := validatePathComponent("run_id", runID); err != nil {
		return LoadResult{}, err
	}
	if err := validatePathComponent("repository_id", repositoryID); err != nil {
		return LoadResult{}, err
	}
	path := EvidencePath(runID, repositoryID)

	raw, err := reader.ReadFile(ctx, ref, path)
	if err != nil {
		var ge *git.GitError
		if errors.As(err, &ge) && ge.Kind == git.ErrKindNotFound {
			return LoadResult{Found: false}, nil
		}
		return LoadResult{}, fmt.Errorf("evidence: read %s@%s: %w", path, ref, err)
	}

	if len(raw) == 0 {
		return LoadResult{}, fmt.Errorf("evidence: empty file at %s@%s", path, ref)
	}

	var ev domain.ExecutionEvidence
	if err := yaml.Unmarshal(raw, &ev); err != nil {
		return LoadResult{}, fmt.Errorf("evidence: parse %s@%s: %w", path, ref, err)
	}
	if err := ev.Validate(); err != nil {
		return LoadResult{}, fmt.Errorf("evidence: validate %s@%s: %w", path, ref, err)
	}
	if ev.RunID != runID || ev.RepositoryID != repositoryID {
		return LoadResult{}, fmt.Errorf(
			"evidence: file %s@%s identity mismatch: stored run_id=%q repository_id=%q, requested run_id=%q repository_id=%q",
			path, ref, ev.RunID, ev.RepositoryID, runID, repositoryID,
		)
	}
	return LoadResult{Evidence: &ev, Found: true}, nil
}
