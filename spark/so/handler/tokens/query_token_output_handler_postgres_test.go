package tokens

import (
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entfixtures"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func TestQueryTokenOutputs_SpentSignedReturnsPendingOutbound(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	f := entfixtures.New(t, ctx, dbClient)
	tokenCreate := f.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	ownerKey := keys.GeneratePrivateKey().Public()

	// Create a finalized mint transaction with an output owned by ownerKey.
	_, outputs := f.CreateMintTransaction(
		tokenCreate,
		entfixtures.OutputSpecsWithOwner(ownerKey, big.NewInt(100)),
		st.TokenTransactionStatusFinalized,
	)
	require.Len(t, outputs, 1)
	output := outputs[0]

	// Create a spending transaction with status Signed and a future expiry
	// so the output is not considered expired.
	spendingTx, err := dbClient.TokenTransaction.Create().
		SetPartialTokenTransactionHash(f.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(f.RandomBytes(32)).
		SetStatus(st.TokenTransactionStatusSigned).
		SetExpiryTime(time.Now().Add(1 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	// Mark the output as spent-signed and link it to the spending transaction.
	_, err = output.Update().
		SetOutputSpentTokenTransaction(spendingTx).
		AddOutputSpentStartedTokenTransactions(spendingTx).
		SetStatus(st.TokenOutputStatusSpentSigned).
		SetSpentTransactionInputVout(0).
		Save(ctx)
	require.NoError(t, err)

	handler := NewQueryTokenOutputsHandler(cfg)
	resp, err := handler.QueryTokenOutputs(ctx, &tokenpb.QueryTokenOutputsRequest{
		OwnerPublicKeys: [][]byte{ownerKey.Serialize()},
		Network:         sparkpb.Network_REGTEST,
	})
	require.NoError(t, err)
	require.Len(t, resp.OutputsWithPreviousTransactionData, 1)

	returnedOutput := resp.OutputsWithPreviousTransactionData[0].Output
	assert.Equal(t,
		tokenpb.TokenOutputStatus_TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND,
		returnedOutput.GetStatus(),
	)
}

func TestQueryTokenOutputs_SpentSignedExpiredReturnsAvailable(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	f := entfixtures.New(t, ctx, dbClient)
	tokenCreate := f.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	ownerKey := keys.GeneratePrivateKey().Public()

	_, outputs := f.CreateMintTransaction(
		tokenCreate,
		entfixtures.OutputSpecsWithOwner(ownerKey, big.NewInt(100)),
		st.TokenTransactionStatusFinalized,
	)
	require.Len(t, outputs, 1)
	output := outputs[0]

	// Create a spending transaction with status Signed and a past expiry
	// so the output is considered expired
	spendingTx, err := dbClient.TokenTransaction.Create().
		SetPartialTokenTransactionHash(f.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(f.RandomBytes(32)).
		SetVersion(3).
		SetStatus(st.TokenTransactionStatusSigned).
		SetExpiryTime(time.Now().Add(-1 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	// Mark the output as spent-signed and link it to the expired spending transaction.
	_, err = output.Update().
		SetOutputSpentTokenTransaction(spendingTx).
		AddOutputSpentStartedTokenTransactions(spendingTx).
		SetStatus(st.TokenOutputStatusSpentSigned).
		SetSpentTransactionInputVout(0).
		Save(ctx)
	require.NoError(t, err)

	handler := NewQueryTokenOutputsHandler(cfg)
	resp, err := handler.QueryTokenOutputs(ctx, &tokenpb.QueryTokenOutputsRequest{
		OwnerPublicKeys: [][]byte{ownerKey.Serialize()},
		Network:         sparkpb.Network_REGTEST,
	})
	require.NoError(t, err)
	require.Len(t, resp.OutputsWithPreviousTransactionData, 1)

	returnedOutput := resp.OutputsWithPreviousTransactionData[0].Output
	assert.Equal(t,
		tokenpb.TokenOutputStatus_TOKEN_OUTPUT_STATUS_AVAILABLE,
		returnedOutput.GetStatus(),
	)
}
