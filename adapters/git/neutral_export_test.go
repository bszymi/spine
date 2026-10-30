package git

// Test seams for the neutral attributes' renormalisation (INIT-024
// TASK-003 review round 4; integration A phases 3-4).

// SetRenormaliseHeadProbeHookForTest sets the function the
// renormalisation runs right before it looks at HEAD (nil clears it).
func SetRenormaliseHeadProbeHookForTest(fn func()) {
	renormaliseHeadProbeHook = fn
}

// SetRenormaliseAfterLineHookForTest sets the function renormalise runs
// once the line is in place, before it compares the marked tree; an error
// stops the renormalisation there, as an I/O error or a crash would (nil
// clears it).
func SetRenormaliseAfterLineHookForTest(fn func() error) {
	renormaliseAfterLineHook = fn
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
func ReadRenormaliseRecordForTest(path string) (changes []string, ok bool, err error) {
	rec, ok, err := readRenormaliseRecord(path)
	return rec.changes, ok, err
}

// WriteRenormaliseRecordForTest writes a marker as markWorkTrees does.
func WriteRenormaliseRecordForTest(path string, changes []string) error {
	return writeRenormaliseRecord(path, renormaliseRecord{changes: changes})
}

// MaxRenormaliseRecordEntries is the record's entry bound.
const MaxRenormaliseRecordEntries = maxRenormaliseRecordEntries
