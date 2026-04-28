package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/repository"
)

// Repository precondition failure categories. Recorded in error detail
// and structured logs so callers and observability can distinguish the
// run-start failure mode without parsing message strings.
const (
	repoPreconditionNotFound = "not_found"
	repoPreconditionUnbound  = "unbound"
	repoPreconditionInactive = "inactive"
	repoPreconditionInternal = "internal"
)

// RepositoryPreconditionFailure carries the failing repository ID and
// failure category alongside the wrapped registry error, so gateway
// responses and metrics can surface both fields without re-deriving
// them from message strings.
type RepositoryPreconditionFailure struct {
	RepositoryID string `json:"repository_id"`
	Category     string `json:"category"`
}

// checkRepositoryPreconditions resolves every repository ID declared
// on the task against the runtime registry and reports the first
// unresolvable one as a precondition failure. A nil resolver or empty
// repository list passes through (a primary-repo-only run never goes
// through registry lookup since the primary is implicit).
//
// The check produces a typed domain.SpineError with ErrPrecondition
// code, a structured RepositoryPreconditionFailure detail, and the
// underlying registry sentinel wrapped via NewErrorWithCause so
// callers can match with errors.Is.
func (o *Orchestrator) checkRepositoryPreconditions(ctx context.Context, art *domain.Artifact) error {
	if o.repositories == nil || art == nil || len(art.Repositories) == 0 {
		return nil
	}

	log := observe.Logger(ctx)
	for _, id := range art.Repositories {
		repo, err := o.repositories.Lookup(ctx, id)
		if err != nil {
			category := categorizeRepositoryError(err)
			log.Warn("repository precondition failed",
				"task_path", art.Path,
				"repository_id", id,
				"category", category,
				"error", err,
			)
			spineErr := domain.NewErrorWithCause(
				domain.ErrPrecondition,
				fmt.Sprintf("repository %q precondition failed: %s", id, category),
				err,
			)
			spineErr.Detail = RepositoryPreconditionFailure{RepositoryID: id, Category: category}
			return spineErr
		}
		log.Info("repository precondition passed",
			"task_path", art.Path,
			"repository_id", repo.ID,
			"status", repo.Status,
		)
	}
	return nil
}

func categorizeRepositoryError(err error) string {
	switch {
	case errors.Is(err, repository.ErrRepositoryNotFound):
		return repoPreconditionNotFound
	case errors.Is(err, repository.ErrRepositoryUnbound):
		return repoPreconditionUnbound
	case errors.Is(err, repository.ErrRepositoryInactive):
		return repoPreconditionInactive
	default:
		return repoPreconditionInternal
	}
}
