package grpctest

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/sighash"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	entutxo "github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// opTypeStaticDepositUtxoRefund is the int32 value of
// CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_REFUND, derived from the proto enum
// so renumbering it surfaces a compile error rather than vacuously passing.
const opTypeStaticDepositUtxoRefund = int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_REFUND)

// TestStaticDepositUtxoRefund_Consensus_HappyPath drives a static-deposit refund
// through the 2PC engine and
// verifies:
//   - RefundStaticDeposit returns a signed spend tx that broadcasts on L1
//     (proves the consensus-built SigningResult aggregates to a valid signature)
//   - every operator's UtxoSwap row ends up COMPLETED (Prepare+Commit ran everywhere)
func TestStaticDepositUtxoRefund_Consensus_HappyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	bitcoinClient := sparktesting.GetBitcoinClient()
	aliceConfig, aliceCtx, aliceDepositPrivKey, spendTx, signedDepositTx, vout, userSignature := setUpConfirmedStaticDepositForRefund(t)

	signedSpendTx, err := wallet.RefundStaticDeposit(aliceCtx, aliceConfig, wallet.RefundStaticDepositParams{
		Network:                 btcnetwork.Regtest,
		SpendTx:                 spendTx,
		DepositAddressSecretKey: aliceDepositPrivKey,
		UserSignature:           userSignature,
		PrevTxOut:               signedDepositTx.TxOut[vout],
	})
	require.NoError(t, err, "consensus static deposit refund should succeed")

	// Broadcasting validates the consensus-aggregated signature: bitcoind rejects a
	// bad signature, so a successful broadcast proves the SigningResult was correct.
	txID, err := bitcoinClient.SendRawTransaction(signedSpendTx, true)
	require.NoError(t, err, "signed refund tx from the consensus path must broadcast")
	require.Len(t, txID, 32)

	// Every SO must have the UtxoSwap COMPLETED — without this, participants
	// diverged from the coordinator during Prepare/Commit. The stored Utxo.Txid is
	// the display-order (reversed) bytes, matching what the refund request sends.
	depositTxidBytes, err := hex.DecodeString(signedDepositTx.TxHash().String())
	require.NoError(t, err)
	// The coordinator identity recorded on every SO's row must be the real
	// coordinator's identity key, derived from the engine's authenticated
	// coordinator_index (not a self-declared payload), so the legacy
	// RollbackUtxoSwap cancel capability stays pinned to a real signing operator.
	coordinatorPubKey := aliceConfig.SigningOperators[aliceConfig.CoordinatorIdentifier].IdentityPublicKey
	for _, i := range operatorIndicesFromConfig(aliceConfig) {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })
		swap, err := entClient.UtxoSwap.Query().
			Where(utxoswap.HasUtxoWith(entutxo.Txid(depositTxidBytes), entutxo.Vout(vout))).
			Where(utxoswap.RequestTypeEQ(st.UtxoSwapRequestTypeRefund)).
			Only(t.Context())
		require.NoError(t, err, "operator %d missing refund utxo swap", i)
		assert.Equal(t, st.UtxoSwapStatusCompleted, swap.Status, "operator %d utxo swap status mismatch", i)
		assert.True(t, swap.ConsensusManaged, "operator %d refund swap must be consensus-managed", i)
		assert.Equal(t, coordinatorPubKey, swap.CoordinatorIdentityPublicKey, "operator %d coordinator identity mismatch", i)
	}
}

// TestStaticDepositUtxoRefund_Consensus_WritesFlowExecutionRows asserts every
// operator writes a STATIC_DEPOSIT_UTXO_REFUND FlowExecution row in COMMITTED
// state sharing the coordinator's execution id, with role aligned to
// coordinator/participant.
func TestStaticDepositUtxoRefund_Consensus_WritesFlowExecutionRows(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	aliceConfig, aliceCtx, aliceDepositPrivKey, spendTx, signedDepositTx, vout, userSignature := setUpConfirmedStaticDepositForRefund(t)
	coordinatorIdx := int(aliceConfig.SigningOperators[aliceConfig.CoordinatorIdentifier].ID)
	operatorIndices := operatorIndicesFromConfig(aliceConfig)

	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	_, err := wallet.RefundStaticDeposit(aliceCtx, aliceConfig, wallet.RefundStaticDepositParams{
		Network:                 btcnetwork.Regtest,
		SpendTx:                 spendTx,
		DepositAddressSecretKey: aliceDepositPrivKey,
		UserSignature:           userSignature,
		PrevTxOut:               signedDepositTx.TxOut[vout],
	})
	require.NoError(t, err)

	newRowsByOperator := make(map[int]*ent.FlowExecution, len(operatorIndices))
	for _, i := range operatorIndices {
		var rows []*ent.FlowExecution
		for _, r := range newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i]) {
			if r.OpType == opTypeStaticDepositUtxoRefund {
				rows = append(rows, r)
			}
		}
		require.Lenf(t, rows, 1, "operator %d must write exactly one new STATIC_DEPOSIT_UTXO_REFUND FlowExecution row", i)
		newRowsByOperator[i] = rows[0]
	}
	sharedID := newRowsByOperator[coordinatorIdx].ID
	for _, i := range operatorIndices {
		row := newRowsByOperator[i]
		assert.Equal(t, sharedID, row.ID, "operator %d FlowExecution id must match coordinator's", i)
		assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status, "operator %d FlowExecution must be COMMITTED", i)
		assert.Equal(t, uint(coordinatorIdx), row.CoordinatorIndex, "operator %d coordinator_index mismatch", i)
		if i == coordinatorIdx {
			assert.Equal(t, st.FlowExecutionRoleCoordinator, row.Role)
		} else {
			assert.Equal(t, st.FlowExecutionRoleParticipant, row.Role)
		}
	}
}

// setUpConfirmedStaticDepositForRefund funds Alice, registers a static deposit
// address, broadcasts + confirms a deposit tx, builds the spend (refund) tx and
// the user signature. Mirrors the setup in TestStaticDepositUserRefund.
func setUpConfirmedStaticDepositForRefund(t *testing.T) (*wallet.TestWalletConfig, context.Context, keys.Private, *wire.MsgTx, *wire.MsgTx, uint32, []byte) {
	t.Helper()
	bitcoinClient := sparktesting.GetBitcoinClient()
	coin, err := faucet.Fund()
	require.NoError(t, err)

	aliceConfig := wallet.NewTestWalletConfig(t)
	aliceLeafPrivKey := keys.GeneratePrivateKey()
	_, err = wallet.CreateNewTree(aliceConfig, faucet, aliceLeafPrivKey, 100_000)
	require.NoError(t, err)

	aliceConn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(aliceConfig.CoordinatorAddress(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = aliceConn.Close() })
	aliceConnectionToken, err := wallet.AuthenticateWithConnection(t.Context(), aliceConfig, aliceConn)
	require.NoError(t, err)
	aliceCtx := wallet.ContextWithToken(t.Context(), aliceConnectionToken)

	aliceDepositPrivKey := keys.GeneratePrivateKey()
	depositResp, err := wallet.GenerateDepositAddress(aliceCtx, aliceConfig, aliceDepositPrivKey.Public(), new(uuid.NewString()), true)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	depositAmount := uint64(100_000)
	quoteAmount := uint64(90_000)
	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), btcnetwork.Regtest)
	require.NoError(t, err)
	unsignedDepositTx, err := sparktesting.CreateTestDepositTransactionManyOutputs(
		coin.OutPoint, []string{randomAddress.String(), depositResp.GetDepositAddress().GetAddress()}, int64(depositAmount))
	require.NoError(t, err)
	vout := uint32(1)
	require.Equal(t, int64(depositAmount), unsignedDepositTx.TxOut[vout].Value)
	signedDepositTx, err := sparktesting.SignFaucetCoin(unsignedDepositTx, coin.TxOut, coin.Key)
	require.NoError(t, err)
	_, err = bitcoinClient.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	depositOutPoint := &wire.OutPoint{Hash: signedDepositTx.TxHash(), Index: vout}
	spendTx := wire.NewMsgTx(3)
	spendTx.AddTxIn(&wire.TxIn{PreviousOutPoint: *depositOutPoint, Sequence: wire.MaxTxInSequenceNum})
	spendPkScript, err := common.P2TRScriptFromPubKey(aliceConfig.IdentityPublicKey())
	require.NoError(t, err)
	spendTx.AddTxOut(wire.NewTxOut(int64(quoteAmount), spendPkScript))

	spendTxSighash, err := sighash.FromTx(spendTx, 0, signedDepositTx.TxOut[vout])
	require.NoError(t, err)
	userSignature, err := wallet.CreateUserSignature(
		signedDepositTx.TxHash().String(), vout, btcnetwork.Regtest,
		pb.UtxoSwapRequestType_Refund, quoteAmount, spendTxSighash.Serialize(), aliceConfig.IdentityPrivateKey)
	require.NoError(t, err)

	// Confirm the deposit (extra block to avoid racing the chain watcher).
	_, err = bitcoinClient.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)
	time.Sleep(1000 * time.Millisecond)

	return aliceConfig, aliceCtx, aliceDepositPrivKey, spendTx, signedDepositTx, vout, userSignature
}
