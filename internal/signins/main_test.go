package signins

import (
	"os"
	"testing"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// TestMain doubles as the sandbox's re-exec init (TestGuidedFlowSandboxed).
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2])
	}
	os.Exit(m.Run())
}
