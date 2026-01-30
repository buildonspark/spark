package tokens_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"go.uber.org/zap"
)

var faucet *sparktesting.Faucet

func TestMain(m *testing.M) {
	client, err := sparktesting.InitBitcoinClient()
	if err != nil {
		zap.S().Fatal("Error creating regtest client", err)
		os.Exit(1)
	}

	faucet = sparktesting.GetFaucetInstance(client)

	var exitCode int

	// Test TTV2 (no phase2 knob needed)
	broadcastTokenTestsUseV3 = false
	broadcastTokenTestsUsePhase2 = false
	if code := m.Run(); code != 0 {
		exitCode = code
	}

	// Test TTV3 with phase2 disabled
	broadcastTokenTestsUseV3 = true
	broadcastTokenTestsUsePhase2 = false
	if err := setPhase2Knob(0); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set phase2 knob to 0: %v\n", err)
		exitCode = 1
	} else if code := m.Run(); code != 0 && exitCode == 0 {
		exitCode = code
	}

	// Test TTV3 with phase2 enabled
	broadcastTokenTestsUsePhase2 = true
	if err := setPhase2Knob(100); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set phase2 knob to 100: %v\n", err)
		if exitCode == 0 {
			exitCode = 1
		}
	} else if code := m.Run(); code != 0 && exitCode == 0 {
		exitCode = code
	}

	// Restore original knob value (delete it since it likely didn't exist before)
	if err := sparktesting.DeleteKnobForTestMain(knobs.KnobTokenTransactionV3Phase2Enabled); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to restore phase2 knob: %v\n", err)
	}

	client.Shutdown()

	os.Exit(exitCode)
}

func setPhase2Knob(value float64) error {
	_, _, err := sparktesting.SetKnobForTestMain(knobs.KnobTokenTransactionV3Phase2Enabled, value)
	return err
}
