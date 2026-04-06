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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type tokenTransactionV3KnobProvider struct{}

func (tokenTransactionV3KnobProvider) GetValue(key string, defaultValue float64) float64 {
	if key == knobs.KnobTokenTransactionV3Enabled {
		return 100
	}
	return defaultValue
}

func TestBroadcastTokenHandlerRejectsPreV3Partial(t *testing.T) {
	handler := NewBroadcastTokenHandler(&so.Config{})
	ctx := knobs.InjectKnobsService(t.Context(), knobs.New(tokenTransactionV3KnobProvider{}))

	req := &tokenpb.BroadcastTransactionRequest{
		PartialTokenTransaction: &tokenpb.PartialTokenTransaction{
			Version: 2,
		},
	}

	resp, err := handler.BroadcastTokenTransaction(ctx, req)
	require.Error(t, err, "expected error for pre-v3 partial transaction")
	require.Nil(t, resp, "response should be nil on error")
	require.Contains(
		t,
		err.Error(),
		"broadcast transaction requires version 3+ partial token transaction",
	)
}

func phase2KnobsWithBroadcastAllowedFor(hexKey string) knobs.Knobs {
	return knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenTransactionV3Enabled:                                  100,
		knobs.KnobTokenTransactionV3Phase2Enabled:                            100,
		fmt.Sprintf("%s@%s", knobs.KnobTokenBroadcastAllowedPubkeys, hexKey): 1,
	})
}

func TestBroadcastTokenTransaction_Phase2_RejectsIdentityMismatch(t *testing.T) {
	sessionKey := keys.GeneratePrivateKey()
	differentKey := keys.GeneratePrivateKey()

	handler := NewBroadcastTokenHandler(&so.Config{AuthzEnforced: true})
	ctx := knobs.InjectKnobsService(t.Context(), v3Phase2EnabledKnobs())
	ctx = authn.InjectSessionForTests(ctx, sessionKey.Public().ToHex(), math.MaxInt64)

	req := &tokenpb.BroadcastTransactionRequest{
		IdentityPublicKey:       differentKey.Public().Serialize(),
		PartialTokenTransaction: &tokenpb.PartialTokenTransaction{Version: 3},
	}

	_, err := handler.broadcastTokenTransactionPhase2(ctx, req)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.PermissionDenied, st.Code())
}

func TestBroadcastTokenTransaction_Phase2_AuthorizedBroadcasterBypasses(t *testing.T) {
	broadcasterKey := keys.GeneratePrivateKey()
	targetKey := keys.GeneratePrivateKey()

	handler := NewBroadcastTokenHandler(&so.Config{AuthzEnforced: true})
	ctx := knobs.InjectKnobsService(t.Context(), phase2KnobsWithBroadcastAllowedFor(broadcasterKey.Public().ToHex()))
	ctx = authn.InjectSessionForTests(ctx, broadcasterKey.Public().ToHex(), math.MaxInt64)

	req := &tokenpb.BroadcastTransactionRequest{
		IdentityPublicKey:       targetKey.Public().Serialize(),
		PartialTokenTransaction: &tokenpb.PartialTokenTransaction{Version: 3},
	}

	_, err := handler.broadcastTokenTransactionPhase2(ctx, req)
	// Auth check passes; the error (if any) must come from later validation, not identity checks.
	if err != nil {
		st, ok := status.FromError(err)
		require.True(t, ok)
		require.NotEqual(t, codes.PermissionDenied, st.Code())
		require.NotEqual(t, codes.Unauthenticated, st.Code())
	}
}
