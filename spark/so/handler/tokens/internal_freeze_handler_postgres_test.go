package tokens

import (
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so/db"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

func TestInternalFreezeTokens_Success(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewInternalFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)
	externalReq := createFreezeTestRequestWithKey(t, cfg, tokenCreate, false, freezeTestIssuerKey)

	internalReq := &tokeninternalpb.InternalFreezeTokensRequest{
		FreezeTokensPayload: externalReq.FreezeTokensPayload,
		IssuerSignature:     externalReq.IssuerSignature,
	}

	resp, err := handler.InternalFreezeTokens(ctx, internalReq)

	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestInternalFreezeTokens_FailsWhenNotFreezable(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewInternalFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, false)
	externalReq := createFreezeTestRequestWithKey(t, cfg, tokenCreate, false, freezeTestIssuerKey)

	internalReq := &tokeninternalpb.InternalFreezeTokensRequest{
		FreezeTokensPayload: externalReq.FreezeTokensPayload,
		IssuerSignature:     externalReq.IssuerSignature,
	}

	resp, err := handler.InternalFreezeTokens(ctx, internalReq)

	require.Error(t, err)
	require.Nil(t, resp)
}

func TestInternalFreezeTokens_ValidatesSignatureIndependently(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewInternalFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)
	wrongKey := keys.GeneratePrivateKey()
	externalReq := createFreezeTestRequestWithKey(t, cfg, tokenCreate, false, wrongKey)

	internalReq := &tokeninternalpb.InternalFreezeTokensRequest{
		FreezeTokensPayload: externalReq.FreezeTokensPayload,
		IssuerSignature:     externalReq.IssuerSignature,
	}

	resp, err := handler.InternalFreezeTokens(ctx, internalReq)

	require.Error(t, err)
	require.Nil(t, resp)
}
