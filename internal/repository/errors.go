// Package repository provides the in-process registry that resolves a
// workspace's repositories from the governed catalog
// (/.spine/repositories.yaml) and the runtime binding rows in
// runtime.repositories. See ADR-013 and architecture/multi-repository-integration.md.
package repository

import (
	"errors"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
)

// Sentinel errors returned by the registry. They are wrapped in a
// domain.SpineError so the gateway can map them to HTTP status codes
// while callers still match via errors.Is.
var (
	// ErrRepositoryNotFound — no catalog entry for the requested ID.
	ErrRepositoryNotFound = errors.New("repository not found")

	// ErrRepositoryInactive — catalog entry exists and a binding row
	// exists, but the binding is marked inactive. Callers on the
	// execution hot-path must treat this as "do not use".
	ErrRepositoryInactive = errors.New("repository binding is inactive")

	// ErrRepositoryUnbound — catalog entry exists but no runtime
	// binding has been registered yet. The repository is governed but
	// not operationally connected.
	ErrRepositoryUnbound = errors.New("repository has no runtime binding")
)

func newNotFoundError(id string) error {
	return domain.NewErrorWithCause(domain.ErrNotFound,
		fmt.Sprintf("repository %q not found in catalog", id),
		ErrRepositoryNotFound)
}

func newInactiveError(id string) error {
	return domain.NewErrorWithCause(domain.ErrPrecondition,
		fmt.Sprintf("repository %q binding is inactive", id),
		ErrRepositoryInactive)
}

func newUnboundError(id string) error {
	return domain.NewErrorWithCause(domain.ErrPrecondition,
		fmt.Sprintf("repository %q has no active runtime binding", id),
		ErrRepositoryUnbound)
}
