package engine_test

// Compile-time interface satisfaction checks.
// These verify that existing services satisfy the engine's interfaces
// without modification.

import (
	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
)

// artifact.Service satisfies ArtifactReader.
var _ engine.ArtifactReader = (*artifact.Service)(nil)

// event.QueueRouter satisfies EventEmitter.
var _ engine.EventEmitter = (*event.QueueRouter)(nil)

// git.CLIClient satisfies GitOperator.
var _ engine.GitOperator = (*git.CLIClient)(nil)

// store.Store satisfies RunStore.
var _ engine.RunStore = store.Store(nil)

// actor.Gateway satisfies ActorAssigner.
var _ engine.ActorAssigner = (*actor.Gateway)(nil)

// engine.BindingResolver satisfies WorkflowResolver.
var _ engine.WorkflowResolver = (*engine.BindingResolver)(nil)

// engine.GitWorkflowLoader satisfies WorkflowLoader.
var _ engine.WorkflowLoader = (*engine.GitWorkflowLoader)(nil)
