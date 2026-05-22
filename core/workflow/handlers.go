package workflow

// KnownInternalHandlers enumerates the engine handler names that an `internal`
// step's execution.handler may reference. The engine package is the canonical
// owner of the actual handler implementations; this list mirrors their names
// so the validator can reject unknown references at workflow-load time
// without creating an import cycle (the engine already imports workflow).
//
// Keep in sync with internal/engine/handlers.go.
var KnownInternalHandlers = map[string]bool{
	"merge": true,
}

// IsKnownInternalHandler reports whether name is a registered engine handler.
func IsKnownInternalHandler(name string) bool {
	return KnownInternalHandlers[name]
}
