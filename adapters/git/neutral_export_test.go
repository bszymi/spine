package git

// Test seams for the neutral attributes' renormalisation (INIT-024
// TASK-003 review round 4).

// SetRenormaliseHeadProbeHookForTest sets the function renormalise runs
// right before it looks at HEAD (nil clears it).
func SetRenormaliseHeadProbeHookForTest(fn func()) {
	renormaliseHeadProbeHook = fn
}

// TreeRefsForTest is the number of holders and waiters of the tree at
// dir: 2 while one call holds it and another waits for it.
func TreeRefsForTest(dir string) int {
	key := canonicalTreeKey(dir)
	treeMu.Lock()
	defer treeMu.Unlock()
	if l := treeLocks[key]; l != nil {
		return l.refs
	}
	return 0
}

// RenormaliseMarkerName is the marker's file name in a work tree's git
// directory.
const RenormaliseMarkerName = renormaliseMarker

// ReadRenormaliseRecordForTest reads the marker at path as renormalise
// does (INIT-024 integration A): ok false means "no record".
func ReadRenormaliseRecordForTest(path string) (changes []string, proof map[string]string, ok bool, err error) {
	rec, ok, err := readRenormaliseRecord(path)
	return rec.changes, rec.proof, ok, err
}

// WriteRenormaliseRecordForTest writes a marker as markWorkTrees and
// renormalise do.
func WriteRenormaliseRecordForTest(path string, changes []string, proof map[string]string) error {
	return writeRenormaliseRecord(path, renormaliseRecord{changes: changes, proof: proof})
}

// MaxRenormaliseRecordEntries is the per-section entry bound.
const MaxRenormaliseRecordEntries = maxRenormaliseRecordEntries
