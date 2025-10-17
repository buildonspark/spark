package testutil

import (
	"os"
	"testing"
)

// SkipIfGithubActions skips the test if running in GitHub Actions
func SkipIfGithubActions(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Skip("Skipping test on GitHub Actions CI")
	}
}
