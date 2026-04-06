package tokens

import (
	"fmt"
	"math"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
)

func knobsWithBroadcastAllowedFor(hexKey string) knobs.Knobs {
	targetKey := fmt.Sprintf("%s@%s", knobs.KnobTokenBroadcastAllowedPubkeys, hexKey)
	return knobs.New(knobs.NewStaticValuesProvider(map[string]float64{
		targetKey: 1,
	}))
}

func TestCanBroadcastForSession_NoSession(t *testing.T) {
	ctx := knobs.InjectKnobsService(t.Context(), knobs.New(nil))
	require.False(t, canBroadcastForSession(ctx))
}

func TestCanBroadcastForSession_KnobNotSet(t *testing.T) {
	sessionKey := keys.GeneratePrivateKey().Public()
	ctx := authn.InjectSessionForTests(t.Context(), sessionKey.ToHex(), math.MaxInt64)
	ctx = knobs.InjectKnobsService(ctx, knobs.New(nil))
	require.False(t, canBroadcastForSession(ctx))
}

func TestCanBroadcastForSession_KnobSetForDifferentKey(t *testing.T) {
	sessionKey := keys.GeneratePrivateKey().Public()
	otherKey := keys.GeneratePrivateKey().Public()

	ctx := authn.InjectSessionForTests(t.Context(), sessionKey.ToHex(), math.MaxInt64)
	ctx = knobs.InjectKnobsService(ctx, knobsWithBroadcastAllowedFor(otherKey.ToHex()))
	require.False(t, canBroadcastForSession(ctx))
}

func TestCanBroadcastForSession_KnobSetForSessionKey(t *testing.T) {
	sessionKey := keys.GeneratePrivateKey().Public()

	ctx := authn.InjectSessionForTests(t.Context(), sessionKey.ToHex(), math.MaxInt64)
	ctx = knobs.InjectKnobsService(ctx, knobsWithBroadcastAllowedFor(sessionKey.ToHex()))
	require.True(t, canBroadcastForSession(ctx))
}

func TestStartTokenTransaction_RejectsIdentityMismatch(t *testing.T) {
	sessionKey := keys.GeneratePrivateKey()
	differentKey := keys.GeneratePrivateKey()

	handler := NewStartTokenTransactionHandler(&so.Config{AuthzEnforced: true})
	ctx := authn.InjectSessionForTests(t.Context(), sessionKey.Public().ToHex(), math.MaxInt64)
	ctx = knobs.InjectKnobsService(ctx, knobs.New(nil))

	_, err := handler.StartTokenTransaction(ctx, &tokenpb.StartTransactionRequest{
		IdentityPublicKey:       differentKey.Public().Serialize(),
		PartialTokenTransaction: &tokenpb.TokenTransaction{},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity public key authentication failed")
}

func TestStartTokenTransaction_AuthorizedBroadcasterBypasses(t *testing.T) {
	broadcasterKey := keys.GeneratePrivateKey()
	targetKey := keys.GeneratePrivateKey()

	handler := NewStartTokenTransactionHandler(&so.Config{AuthzEnforced: true})
	ctx := authn.InjectSessionForTests(t.Context(), broadcasterKey.Public().ToHex(), math.MaxInt64)
	ctx = knobs.InjectKnobsService(ctx, knobsWithBroadcastAllowedFor(broadcasterKey.Public().ToHex()))

	_, err := handler.StartTokenTransaction(ctx, &tokenpb.StartTransactionRequest{
		IdentityPublicKey:       targetKey.Public().Serialize(),
		PartialTokenTransaction: &tokenpb.TokenTransaction{},
	})
	// Auth check passes; any error must come from transaction validation, not identity checks.
	if err != nil {
		require.NotContains(t, err.Error(), "identity public key authentication failed")
	}
}
