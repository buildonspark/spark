package grpctest

import (
	"log"
	"os"
	"testing"

	testutil "github.com/lightsparkdev/spark-go/test_util"
)

var faucet *testutil.Faucet

func TestMain(m *testing.M) {
	// Setup
	client, err := testutil.NewRegtestClient()
	if err != nil {
		log.Printf("Error creating regtest client: %v", err)
		os.Exit(1)
	}
	faucet = testutil.NewFaucet(client)

	// Run tests
	code := m.Run()

	// Teardown
	os.Exit(code)
}
