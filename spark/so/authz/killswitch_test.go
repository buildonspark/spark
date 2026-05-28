package authz

// These tests cover authz.EnforceWalletNotKillSwitched directly because the
// kill-switch case's on-wire response is *deliberately* identical to the
// existing identity-mismatch case (same gRPC code, same message — see
// killswitch.go for the rationale). The indistinguishability invariant is
// therefore invisible at the gRPC boundary: an integration test cannot tell
// "kill switch fired" apart from "session identity didn't match" without
// looking at the internal Error.Code field. We assert on Code here.
//
// Application-boundary coverage (knob actually blocks a real gRPC call,
// reads still work, intermediate operations also blocked) lives in
// so/grpc_test/wallet_killswitch_test.go.

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func ctxWithKnobs(t *testing.T, values map[string]float64) context.Context {
	t.Helper()
	return knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(values))
}

func TestEnforceWalletNotKillSwitched(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})
	walletA := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	walletB := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	t.Run("unset knob allows operation", func(t *testing.T) {
		ctx := ctxWithKnobs(t, map[string]float64{})
		require.NoError(t, EnforceWalletNotKillSwitched(ctx, walletA))
	})

	t.Run("blocks with kill-switch internal code", func(t *testing.T) {
		ctx := ctxWithKnobs(t, map[string]float64{
			knobs.KnobKillSwitchWallet + "@" + walletA.ToHex(): 1,
		})
		err := EnforceWalletNotKillSwitched(ctx, walletA)
		require.Error(t, err)
		var authzErr *Error
		require.ErrorAs(t, err, &authzErr)
		assert.Equal(t, ErrorCodeWalletKillSwitched, authzErr.Code)
	})

	t.Run("wire response is identical to identity mismatch", func(t *testing.T) {
		ctx := ctxWithKnobs(t, map[string]float64{
			knobs.KnobKillSwitchWallet + "@" + walletA.ToHex(): 1,
		})
		killErr := EnforceWalletNotKillSwitched(ctx, walletA)
		require.Error(t, killErr)
		var killAuthzErr *Error
		require.ErrorAs(t, killErr, &killAuthzErr)

		// Produce the reference identity-mismatch error by driving the real
		// EnforceSessionIdentityPublicKeyMatches into its mismatch branch.
		// Building the Error by hand would only assert that the kill-switch
		// constant matches itself; calling the real function asserts that the
		// kill-switch wire surface matches whatever production code actually
		// emits for identity mismatch.
		mismatchCtx := authn.InjectSessionForTests(t.Context(), walletB.ToHex(), 0)
		mismatchErr := EnforceSessionIdentityPublicKeyMatches(mismatchCtx, &simpleConfig{authzEnforced: true}, walletA)
		require.Error(t, mismatchErr)
		var mismatchAuthzErr *Error
		require.ErrorAs(t, mismatchErr, &mismatchAuthzErr)

		killStatus, ok := status.FromError(killAuthzErr.ToGRPCError())
		require.True(t, ok)
		mismatchStatus, ok := status.FromError(mismatchAuthzErr.ToGRPCError())
		require.True(t, ok)

		assert.Equal(t, codes.PermissionDenied, killStatus.Code())
		assert.Equal(t, mismatchStatus.Code(), killStatus.Code())
		assert.Equal(t, mismatchStatus.Message(), killStatus.Message())
	})

	t.Run("targeting: other pubkey not blocked", func(t *testing.T) {
		ctx := ctxWithKnobs(t, map[string]float64{
			knobs.KnobKillSwitchWallet + "@" + walletA.ToHex(): 1,
		})
		require.NoError(t, EnforceWalletNotKillSwitched(ctx, walletB))
	})

	t.Run("knob value 0 does not block", func(t *testing.T) {
		ctx := ctxWithKnobs(t, map[string]float64{
			knobs.KnobKillSwitchWallet + "@" + walletA.ToHex(): 0,
		})
		require.NoError(t, EnforceWalletNotKillSwitched(ctx, walletA))
	})
}
