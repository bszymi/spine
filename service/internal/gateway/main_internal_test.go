package gateway

import (
	"fmt"
	"os"
	"testing"

	"github.com/bszymi/spine/service/internal/githttp"
)

// TestMain: on a host whose temporary directory folds names (macOS), the
// git endpoint's case-sensitivity probe (INIT-024 TASK-005) would refuse
// every push these tests make, so their git handlers treat storage as
// case-sensitive there. On Linux they run the real probe.
func TestMain(m *testing.M) {
	faked, err := githttp.FakeCaseSensitiveStorageOnFoldingHostForTests()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gateway tests: probing the host's temporary directory: %v\n", err)
		os.Exit(2)
	}
	if faked {
		fmt.Fprintln(os.Stderr, "gateway tests: the host's temporary directory folds names; git handlers assume case-sensitive storage")
	}
	os.Exit(m.Run())
}
