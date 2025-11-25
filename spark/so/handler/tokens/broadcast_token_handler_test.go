package tokens

import (
	"testing"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
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
