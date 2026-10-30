package githttp

import (
	"fmt"
	"os"
	"testing"
)

// TestMain: on a host whose temporary directory folds names (macOS), the
// case-sensitivity probe (INIT-024 TASK-005) would refuse every push the
// tests make, so handlers treat storage as case-sensitive there; the
// probe's own tests install the probe they need. On Linux every handler
// runs the real probe. The hook process this binary also serves as exits
// in the package init, before TestMain.
func TestMain(m *testing.M) {
	faked, err := FakeCaseSensitiveStorageOnFoldingHostForTests()
	if err != nil {
		fmt.Fprintf(os.Stderr, "githttp tests: probing the host's temporary directory: %v\n", err)
		os.Exit(2)
	}
	if faked {
		fmt.Fprintln(os.Stderr, "githttp tests: the host's temporary directory folds names; handlers assume case-sensitive storage (the probe tests run the real probe)")
	}
	os.Exit(m.Run())
}
