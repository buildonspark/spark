package tokens_test

import (
	"encoding/hex"
	"testing"

	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

func TestMarshalProtoHashConsistency(t *testing.T) {
	ctx := t.Context()
	config, finalTransferTokenTransactionHash, finalTransferProto, err := createTransferTokenTransactionForWallet(t, ctx)
	require.NoError(t, err)

	entClient, err := ent.Open("postgres", config.CoordinatorDatabaseURI)
	require.NoError(t, err)
	defer entClient.Close()
	ctx = ent.NewContext(ctx, entClient)

	tx, err := entClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithSparkInvoice().
		WithSpentOutput(func(q *ent.TokenOutputQuery) {
			q.WithOutputCreatedTokenTransaction()
		}).
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err, "failed to query transaction")

	soConfig := sparktesting.SpecificOperatorTestConfig(t, 0)

	marshalledProto, err := tx.MarshalProto(ctx, soConfig)
	require.NoError(t, err, "failed to marshal proto")

	originalHash, err := utils.HashTokenTransaction(finalTransferProto, false)
	require.NoError(t, err, "failed to hash original transaction")
	marshalledHash, err := utils.HashTokenTransaction(marshalledProto, false)
	require.NoError(t, err, "failed to hash marshalled transaction")

	require.Equal(t, hex.EncodeToString(marshalledHash), hex.EncodeToString(originalHash), "hash mismatch between marshalled and original transaction proto")
	require.Equal(t, hex.EncodeToString(marshalledHash), hex.EncodeToString(finalTransferTokenTransactionHash), "hash mismatch between marshalled and queried hash")
	require.Equal(t, hex.EncodeToString(marshalledHash), hex.EncodeToString(tx.FinalizedTokenTransactionHash), "hash mismatch between marshalled and stored hash in database")
}
