package grpctest

import (
	"os"
	"testing"

	_ "github.com/lightsparkdev/spark/so/ent/runtime"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"go.uber.org/zap"
)

var faucet *sparktesting.Faucet

func TestMain(m *testing.M) {
	// Setup
	client, err := sparktesting.InitBitcoinClient()
	if err != nil {
		zap.S().Fatal("Error creating regtest client", err)
		os.Exit(1)
	}

	faucet = sparktesting.GetFaucetInstance(client)

	// Run tests
	code := m.Run()

	client.Shutdown()

	// Teardown
	os.Exit(code)
}
