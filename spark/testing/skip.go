package sparktesting

import (
	"os"
	"testing"
	"time"
)

// RequireGripMock skips the current test unless the GRIPMOCK environment variable is set to true.
func RequireGripMock(t testing.TB) {
	t.Helper()
	if !IsGripmock() {
		t.Skipf("skipping %s because it's a GripMock test; to enable it, set GRIPMOCK=true", t.Name())
	}
}

// PostgresTestsEnabled returns true if the SKIP_POSTGRES_TESTS environment variable is not set.
func PostgresTestsEnabled() bool {
	return os.Getenv("SKIP_POSTGRES_TESTS") != "true"
}

// SkipIfGithubActions skips the test if running in GitHub Actions
func SkipIfGithubActions(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Skip("Skipping test on GitHub Actions CI")
	}
}

// Adds a timeout to the provided test function. If the test function does not complete
// within the specified duration, the test will fail.
func WithTimeout(t *testing.T, timeout time.Duration, testFunc func(t *testing.T)) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		testFunc(t)
		close(done)
	}()
	select {
	case <-done:
		// Test completed within the timeout
	case <-time.After(timeout):
		t.Fatalf("test timed out after %s", timeout)
	}
}
