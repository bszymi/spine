package workspace

// NewWorkspaceDBPoolForTest constructs a WorkspaceDBPool without
// opening a real pgxpool, so unit tests can exercise the saturation
// gate without a database. maxConns and queueSize are used verbatim
// (no defaults), so passing queueSize=0 produces a strictly-bounded
// gate of size maxConns. The pool is registered so metrics
// integration is exercised end-to-end.
func NewWorkspaceDBPoolForTest(workspaceID string, maxConns int32, queueSize int) *WorkspaceDBPool {
	policy := PoolPolicy{MaxConns: maxConns, QueueSize: queueSize, AcquireTimeout: 0, HealthCheckPeriod: 0, MinConns: 0}
	wp := &WorkspaceDBPool{
		workspaceID: workspaceID,
		policy:      policy,
		inFlight:    make(chan struct{}, int(maxConns)+queueSize),
	}
	registerPool(wp)
	return wp
}

// EnterGateForTest exposes the gate-only Acquire path so tests
// can drive saturation without needing a backing pgxpool.
func (w *WorkspaceDBPool) EnterGateForTest() (func(), error) {
	return w.enterGate()
}

// CloseForTest tears down the pool without closing a (nil)
// underlying pgxpool. Used by tests that built a pool via
// NewWorkspaceDBPoolForTest.
func (w *WorkspaceDBPool) CloseForTest() {
	if !w.closed.CompareAndSwap(false, true) {
		return
	}
	unregisterPool(w)
}
