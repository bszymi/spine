package engine_test

// Compile-time interface satisfaction checks.
// These verify that existing services satisfy the engine's interfaces
// without modification.

import (
	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/store"
)

// artifact.Service satisfies ArtifactReader.
var _ engine.ArtifactReader = (*artifact.Service)(nil)

// event.QueueRouter satisfies EventEmitter.
var _ engine.EventEmitter = (*event.QueueRouter)(nil)

// git.CLIClient satisfies GitOperator.
var _ engine.GitOperator = (*git.CLIClient)(nil)

// store.Store satisfies RunStore.
var _ engine.RunStore = (store.Store)(nil)

// actor.Gateway satisfies ActorAssigner.
var _ engine.ActorAssigner = (*actor.Gateway)(nil)

// engine.BindingResolver satisfies WorkflowResolver.
var _ engine.WorkflowResolver = (*engine.BindingResolver)(nil)
