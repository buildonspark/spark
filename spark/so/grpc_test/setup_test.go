package grpctest

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/lightsparkdev/spark/so/dkg"
	testutil "github.com/lightsparkdev/spark/test_util"
)

const (
	EnvRunDKG = "RUN_DKG"
)

var faucet *testutil.Faucet

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})))
	// Setup
	client, err := testutil.NewRegtestClient()
	if err != nil {
		slog.Error("Error creating regtest client", "error", err)
		os.Exit(1)
	}
	faucet = testutil.GetFaucetInstance(client)

	if shouldRunDKG() {
		if err := setupDKG(); err != nil {
			slog.Error("DKG setup encountered errors, tests may fail", "error", err)
		} else {
			slog.Info("DKG setup completed successfully")
		}
	} else {
		slog.Warn("DKG not run for test setup. Set RUN_DKG=true to run DKG if tests fail, " +
			"run scripts/run-development-dkg.sh, or re-run tests as they may work on retry")
	}
	btcjson.MustRegisterCmd("submitpackage", (*SubmitPackageCmd)(nil), btcjson.UsageFlag(0))

	// Run tests
	code := m.Run()

	// Teardown
	os.Exit(code)
}

func shouldRunDKG() bool {
	return os.Getenv(EnvRunDKG) == "true"
}

func setupDKG() error {
	config, err := testutil.TestConfig()
	if err != nil {
		return err
	}

	if err := dkg.GenerateKeys(context.Background(), config, 1000); err != nil {
		return err
	}

	// Allow time for propagation
	time.Sleep(5 * time.Second)
	return nil
}
