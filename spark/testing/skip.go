package sparktesting

import (
	"os"
	"testing"
)

// RequireGripMock skips the current test unless the GRIPMOCK environment variable is set to true.
func RequireGripMock(t testing.TB) {
	t.Helper()
	if !GripMockEnabled() {
		t.Skipf("skipping %s because it's a GripMock test; to enable it, set GRIPMOCK=true", t.Name())
	}
}

// GripMockEnabled returns true if the GRIPMOCK environment variable is set to true.
func GripMockEnabled() bool {
	return os.Getenv("GRIPMOCK") == "true"
}
