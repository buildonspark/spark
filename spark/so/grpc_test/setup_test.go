package grpctest

import (
	"fmt"
	"os"
	"testing"

	_ "github.com/lightsparkdev/spark/so/ent/runtime"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

var faucet *sparktesting.Faucet

func TestMain(m *testing.M) {
	// Setup
	client, err := sparktesting.InitBitcoinClient()
	if err != nil {
		fmt.Println("Error creating regtest client", err)
		os.Exit(1)
	}

	faucet = sparktesting.GetFaucetInstance(client)

	// Run tests
	code := m.Run()

	client.Shutdown()

	// Teardown
	os.Exit(code)
}

func skipIfGithubActions(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Skip("Skipping test on GitHub Actions CI")
	}
}
