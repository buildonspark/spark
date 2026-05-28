package grpctest

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestWalletKillSwitch_BlocksMutationsAllowsReads exercises the wallet kill
// switch from the gRPC boundary:
//   - With the kill switch set on walletA, walletA's state-mutating RPCs
//     (GenerateDepositAddress) return PermissionDenied with a message
//     indistinguishable from a normal identity-mismatch failure.
//   - walletA's read-only RPCs (QueryUnusedDepositAddresses) continue to work.
//   - walletB's mutations are unaffected — the gate is targeted per pubkey.
//   - Clearing the knob restores walletA's ability to mutate state.
//
// This test requires the minikube environment (it talks to a real knob
// ConfigMap and live SOs). It is skipped automatically when the knob
// controller is unavailable.
func TestWalletKillSwitch_BlocksMutationsAllowsReads(t *testing.T) {
	kc, err := sparktesting.NewKnobController(t)
	require.NoError(t, err)

	configA := wallet.NewTestWalletConfig(t)
	configB := wallet.NewTestWalletConfig(t)

	walletAHex := configA.IdentityPublicKey().ToHex()

	tokenA, err := wallet.AuthenticateWithServer(t.Context(), configA)
	require.NoError(t, err)
	ctxA := wallet.ContextWithToken(t.Context(), tokenA)

	tokenB, err := wallet.AuthenticateWithServer(t.Context(), configB)
	require.NoError(t, err)
	ctxB := wallet.ContextWithToken(t.Context(), tokenB)

	signingPubKey := keys.MustParsePublicKeyHex(
		"0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7",
	)

	// Sanity: with the kill switch off, walletA can generate an address.
	leafIDPre := uuid.NewString()
	_, err = wallet.GenerateDepositAddress(ctxA, configA, signingPubKey, &leafIDPre, false)
	require.NoError(t, err, "walletA should be able to mutate before the kill switch is set")

	// Activate the kill switch for walletA only. NewKnobController registers a
	// cleanup that restores the full ConfigMap (with a background context), so
	// no per-test cleanup is needed here — registering one would call
	// SetKnobWithTarget with t.Context, which is canceled before t.Cleanup runs.
	err = kc.SetKnobWithTarget(t, knobs.KnobKillSwitchWallet, walletAHex, 1)
	require.NoError(t, err)

	// Mutation by walletA is now denied.
	leafIDBlocked := uuid.NewString()
	_, err = wallet.GenerateDepositAddress(ctxA, configA, signingPubKey, &leafIDBlocked, false)
	require.Error(t, err, "walletA mutation should be blocked by the kill switch")
	st, ok := status.FromError(err)
	require.True(t, ok, "expected a gRPC status error, got %T: %v", err, err)
	assert.Equal(t, codes.PermissionDenied, st.Code())
	// Wire response must be indistinguishable from a normal identity mismatch —
	// see so/authz/killswitch.go for the rationale.
	assert.Equal(t, "session identity does not match request identity", st.Message())

	// Read-only RPC on walletA still works while the kill switch is active.
	_, err = wallet.QueryUnusedDepositAddresses(ctxA, configA)
	require.NoError(t, err, "walletA read should NOT be blocked by the kill switch")

	// walletB's mutation is unaffected — the kill switch is targeted.
	leafIDB := uuid.NewString()
	_, err = wallet.GenerateDepositAddress(ctxB, configB, signingPubKey, &leafIDB, false)
	require.NoError(t, err, "walletB mutation should be unaffected by walletA's kill switch")

	// Clear the kill switch and confirm walletA can mutate again.
	err = kc.SetKnobWithTarget(t, knobs.KnobKillSwitchWallet, walletAHex, 0)
	require.NoError(t, err)
	leafIDPost := uuid.NewString()
	_, err = wallet.GenerateDepositAddress(ctxA, configA, signingPubKey, &leafIDPost, false)
	require.NoError(t, err, "walletA mutation should succeed after the kill switch is cleared")
}
