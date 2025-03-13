package grpctest

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/lightsparkdev/spark-go/so/dkg"
	testutil "github.com/lightsparkdev/spark-go/test_util"
)

const (
	EnvRunDKG = "RUN_DKG"
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

	if shouldRunDKG() {
		if err := setupDKG(); err != nil {
			log.Printf("DKG setup encountered errors: %v", err)
			log.Printf("Tests may fail")
		} else {
			log.Printf("DKG setup completed successfully")
		}
	} else {
		log.Printf("DKG not run for test setup. Set %s=true to run DKG if tests fail, "+
			"run scripts/run-development-dkg.sh, or re-run tests as they may work on retry",
			EnvRunDKG)
	}

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
