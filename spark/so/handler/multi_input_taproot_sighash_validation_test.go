package handler

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// TestValidateRefundTxWithConnector_ValidatesMultiInputStructure verifies that
// validateRefundTxWithConnector properly validates 2-input cooperative exit refund
// transactions by checking that input 1 references a valid connector output.
func TestValidateRefundTxWithConnector_ValidatesMultiInputStructure(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	leafInternalPriv := keys.GeneratePrivateKey()
	leafPkScript, err := common.P2TRScriptFromPubKey(leafInternalPriv.Public())
	require.NoError(t, err)

	connectorPkScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	receiverPkScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	// Funding tx for the leaf outpoint (this is the node's RawTx).
	nodeTx := wire.NewMsgTx(3)
	nodeTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}, Index: 0}, nil, nil))
	nodeTx.AddTxOut(wire.NewTxOut(100_000, leafPkScript))
	nodeTxBytes, err := common.SerializeTx(nodeTx)
	require.NoError(t, err)

	// Connector transaction with outputs the refund tx should reference.
	connectorTx := wire.NewMsgTx(3)
	connectorTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{2}, Index: 0}, nil, nil))
	connectorTx.AddTxOut(wire.NewTxOut(200_000, connectorPkScript))
	connectorTxHash := connectorTx.TxHash()

	// Build connector prevouts map.
	connectorPrevOuts := map[wire.OutPoint]*wire.TxOut{
		{Hash: connectorTxHash, Index: 0}: connectorTx.TxOut[0],
	}

	// Create tree and node in database.
	tree, err := dbClient.Tree.Create().
		SetID(uuid.New()).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		Save(ctx)
	require.NoError(t, err)

	keyshareSecret := keys.GeneratePrivateKey()
	keyshare, err := dbClient.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keyshareSecret).
		SetPublicShares(map[string]keys.Public{"1": keyshareSecret.Public()}).
		SetPublicKey(keyshareSecret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	node, err := dbClient.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(keyshare).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeNodeStatusAvailable).
		SetValue(100_000).
		SetVerifyingPubkey(leafInternalPriv.Public()).
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerSigningPubkey(keys.GeneratePrivateKey().Public()).
		SetVout(0).
		SetRawTx(nodeTxBytes).
		Save(ctx)
	require.NoError(t, err)

	refundDestPubkey := keys.GeneratePrivateKey().Public()

	t.Run("passes connector validation for valid 2-input refund tx", func(t *testing.T) {
		// Create a valid 2-input refund tx.
		refundTx := wire.NewMsgTx(3)
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0},
			Sequence:         1000,
		})
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: connectorTxHash, Index: 0},
			Sequence:         0,
		})
		refundTx.AddTxOut(wire.NewTxOut(299_000, receiverPkScript))
		refundTxBytes, err := common.SerializeTx(refundTx)
		require.NoError(t, err)

		err = validateRefundTxWithConnector(
			ctx,
			refundTxBytes,
			node,
			connectorPrevOuts,
			bitcointransaction.TxTypeRefundCPFP,
			refundDestPubkey,
			btcnetwork.Regtest.String(),
		)
		if err != nil {
			require.NotContains(t, err.Error(), "does not reference a valid connector output")
			require.NotContains(t, err.Error(), "expected 2 inputs")
			require.NotContains(t, err.Error(), "does not reference the node tx")
		}
	})

	t.Run("rejects refund tx with invalid connector reference", func(t *testing.T) {
		// Create a refund tx with input 1 referencing a non-existent connector output.
		invalidConnectorHash := chainhash.Hash{99, 99, 99}
		refundTx := wire.NewMsgTx(3)
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0},
			Sequence:         1000,
		})
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: invalidConnectorHash, Index: 0},
			Sequence:         0,
		})
		refundTx.AddTxOut(wire.NewTxOut(299_000, receiverPkScript))
		refundTxBytes, err := common.SerializeTx(refundTx)
		require.NoError(t, err)

		err = validateRefundTxWithConnector(
			ctx,
			refundTxBytes,
			node,
			connectorPrevOuts,
			bitcointransaction.TxTypeRefundCPFP,
			refundDestPubkey,
			btcnetwork.Regtest.String(),
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not reference a valid connector output")
	})

	t.Run("rejects single-input refund tx", func(t *testing.T) {
		// Create a single-input refund tx (missing connector input).
		refundTx := wire.NewMsgTx(3)
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0},
			Sequence:         1000,
		})
		refundTx.AddTxOut(wire.NewTxOut(99_000, receiverPkScript))
		refundTxBytes, err := common.SerializeTx(refundTx)
		require.NoError(t, err)

		err = validateRefundTxWithConnector(
			ctx,
			refundTxBytes,
			node,
			connectorPrevOuts,
			bitcointransaction.TxTypeRefundCPFP,
			refundDestPubkey,
			btcnetwork.Regtest.String(),
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "expected 2 inputs")
	})

	t.Run("rejects refund tx with wrong node reference", func(t *testing.T) {
		// Create a refund tx with input 0 referencing wrong node tx.
		wrongNodeHash := chainhash.Hash{88, 88, 88}
		refundTx := wire.NewMsgTx(3)
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: wrongNodeHash, Index: 0},
			Sequence:         1000,
		})
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: connectorTxHash, Index: 0},
			Sequence:         0,
		})
		refundTx.AddTxOut(wire.NewTxOut(299_000, receiverPkScript))
		refundTxBytes, err := common.SerializeTx(refundTx)
		require.NoError(t, err)

		err = validateRefundTxWithConnector(
			ctx,
			refundTxBytes,
			node,
			connectorPrevOuts,
			bitcointransaction.TxTypeRefundCPFP,
			refundDestPubkey,
			btcnetwork.Regtest.String(),
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not reference the node tx")
	})
}

func TestValidateRefundTxWithConnector_TxTypeRefundDirect(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	leafInternalPriv := keys.GeneratePrivateKey()
	leafPkScript, err := common.P2TRScriptFromPubKey(leafInternalPriv.Public())
	require.NoError(t, err)

	connectorPkScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	receiverPkScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	rawTx := wire.NewMsgTx(3)
	rawTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}, Index: 0}, nil, nil))
	rawTx.AddTxOut(wire.NewTxOut(100_000, leafPkScript))
	rawTxBytes, err := common.SerializeTx(rawTx)
	require.NoError(t, err)

	directTx := wire.NewMsgTx(3)
	directTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{2}, Index: 0}, nil, nil))
	directTx.AddTxOut(wire.NewTxOut(100_000, leafPkScript))
	directTxBytes, err := common.SerializeTx(directTx)
	require.NoError(t, err)

	connectorTx := wire.NewMsgTx(3)
	connectorTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{3}, Index: 0}, nil, nil))
	connectorTx.AddTxOut(wire.NewTxOut(200_000, connectorPkScript))
	connectorTxHash := connectorTx.TxHash()

	connectorPrevOuts := map[wire.OutPoint]*wire.TxOut{
		{Hash: connectorTxHash, Index: 0}: connectorTx.TxOut[0],
	}

	tree, err := dbClient.Tree.Create().
		SetID(uuid.New()).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		Save(ctx)
	require.NoError(t, err)

	keyshareSecret := keys.GeneratePrivateKey()
	keyshare, err := dbClient.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keyshareSecret).
		SetPublicShares(map[string]keys.Public{"1": keyshareSecret.Public()}).
		SetPublicKey(keyshareSecret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	node, err := dbClient.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(keyshare).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeNodeStatusAvailable).
		SetValue(100_000).
		SetVerifyingPubkey(leafInternalPriv.Public()).
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerSigningPubkey(keys.GeneratePrivateKey().Public()).
		SetVout(0).
		SetRawTx(rawTxBytes).
		SetDirectTx(directTxBytes).
		Save(ctx)
	require.NoError(t, err)

	refundDestPubkey := keys.GeneratePrivateKey().Public()

	t.Run("TxTypeRefundDirect validates against DirectTx", func(t *testing.T) {
		refundTx := wire.NewMsgTx(3)
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: directTx.TxHash(), Index: 0},
			Sequence:         1000,
		})
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: connectorTxHash, Index: 0},
			Sequence:         0,
		})
		refundTx.AddTxOut(wire.NewTxOut(299_000, receiverPkScript))
		refundTxBytes, err := common.SerializeTx(refundTx)
		require.NoError(t, err)

		err = validateRefundTxWithConnector(
			ctx,
			refundTxBytes,
			node,
			connectorPrevOuts,
			bitcointransaction.TxTypeRefundDirect,
			refundDestPubkey,
			btcnetwork.Regtest.String(),
		)
		if err != nil {
			require.NotContains(t, err.Error(), "does not reference the node tx")
		}
	})

	t.Run("TxTypeRefundDirect rejects refund referencing RawTx", func(t *testing.T) {
		refundTx := wire.NewMsgTx(3)
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: rawTx.TxHash(), Index: 0},
			Sequence:         1000,
		})
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: connectorTxHash, Index: 0},
			Sequence:         0,
		})
		refundTx.AddTxOut(wire.NewTxOut(299_000, receiverPkScript))
		refundTxBytes, err := common.SerializeTx(refundTx)
		require.NoError(t, err)

		err = validateRefundTxWithConnector(
			ctx,
			refundTxBytes,
			node,
			connectorPrevOuts,
			bitcointransaction.TxTypeRefundDirect,
			refundDestPubkey,
			btcnetwork.Regtest.String(),
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not reference the node tx")
	})

	t.Run("TxTypeRefundCPFP still validates against RawTx", func(t *testing.T) {
		refundTx := wire.NewMsgTx(3)
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: rawTx.TxHash(), Index: 0},
			Sequence:         1000,
		})
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: connectorTxHash, Index: 0},
			Sequence:         0,
		})
		refundTx.AddTxOut(wire.NewTxOut(299_000, receiverPkScript))
		refundTxBytes, err := common.SerializeTx(refundTx)
		require.NoError(t, err)

		err = validateRefundTxWithConnector(
			ctx,
			refundTxBytes,
			node,
			connectorPrevOuts,
			bitcointransaction.TxTypeRefundCPFP,
			refundDestPubkey,
			btcnetwork.Regtest.String(),
		)
		if err != nil {
			require.NotContains(t, err.Error(), "does not reference the node tx")
		}
	})

	t.Run("TxTypeRefundCPFP rejects refund referencing DirectTx", func(t *testing.T) {
		refundTx := wire.NewMsgTx(3)
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: directTx.TxHash(), Index: 0},
			Sequence:         1000,
		})
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: connectorTxHash, Index: 0},
			Sequence:         0,
		})
		refundTx.AddTxOut(wire.NewTxOut(299_000, receiverPkScript))
		refundTxBytes, err := common.SerializeTx(refundTx)
		require.NoError(t, err)

		err = validateRefundTxWithConnector(
			ctx,
			refundTxBytes,
			node,
			connectorPrevOuts,
			bitcointransaction.TxTypeRefundCPFP,
			refundDestPubkey,
			btcnetwork.Regtest.String(),
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not reference the node tx")
	})
}

// TestParseConnectorTxOutputs verifies that parseConnectorTxOutputs correctly
// parses a connector transaction into a map of outpoints to outputs.
func TestParseConnectorTxOutputs(t *testing.T) {
	t.Run("returns nil for empty connector tx", func(t *testing.T) {
		prevOuts, err := parseConnectorTxOutputs(nil)
		require.NoError(t, err)
		require.Nil(t, prevOuts)

		prevOuts, err = parseConnectorTxOutputs([]byte{})
		require.NoError(t, err)
		require.Nil(t, prevOuts)
	})

	t.Run("parses valid connector tx", func(t *testing.T) {
		connectorPkScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
		require.NoError(t, err)

		connectorTx := wire.NewMsgTx(3)
		connectorTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}, Index: 0}, nil, nil))
		connectorTx.AddTxOut(wire.NewTxOut(100_000, connectorPkScript))
		connectorTx.AddTxOut(wire.NewTxOut(200_000, connectorPkScript))
		connectorTxBytes, err := common.SerializeTx(connectorTx)
		require.NoError(t, err)

		prevOuts, err := parseConnectorTxOutputs(connectorTxBytes)
		require.NoError(t, err)
		require.Len(t, prevOuts, 2)

		connectorTxHash := connectorTx.TxHash()
		out0, exists := prevOuts[wire.OutPoint{Hash: connectorTxHash, Index: 0}]
		require.True(t, exists)
		require.Equal(t, int64(100_000), out0.Value)

		out1, exists := prevOuts[wire.OutPoint{Hash: connectorTxHash, Index: 1}]
		require.True(t, exists)
		require.Equal(t, int64(200_000), out1.Value)
	})

	t.Run("returns error for invalid connector tx bytes", func(t *testing.T) {
		_, err := parseConnectorTxOutputs([]byte{0x00, 0x01, 0x02})
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to parse connector transaction")
	})
}

// TestValidateTransactionCooperativeExitLegacyLeavesToSend_UsesConnectorValidation verifies
// that validateTransactionCooperativeExitLegacyLeavesToSend uses multi-input validation
// when a connector tx is provided.
func TestValidateTransactionCooperativeExitLegacyLeavesToSend_UsesConnectorValidation(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = context.WithValue(ctx, "skip_knobs", true)

	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	leafInternalPriv := keys.GeneratePrivateKey()
	leafPkScript, err := common.P2TRScriptFromPubKey(leafInternalPriv.Public())
	require.NoError(t, err)

	connectorPkScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	receiverPriv := keys.GeneratePrivateKey()
	receiverPkScript, err := common.P2TRScriptFromPubKey(receiverPriv.Public())
	require.NoError(t, err)

	// Node tx (leaf's RawTx).
	nodeTx := wire.NewMsgTx(3)
	nodeTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}, Index: 0}, nil, nil))
	nodeTx.AddTxOut(wire.NewTxOut(100_000, leafPkScript))
	nodeTxBytes, err := common.SerializeTx(nodeTx)
	require.NoError(t, err)

	// Connector transaction.
	connectorTx := wire.NewMsgTx(3)
	connectorTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{2}, Index: 0}, nil, nil))
	connectorTx.AddTxOut(wire.NewTxOut(200_000, connectorPkScript))
	connectorTxBytes, err := common.SerializeTx(connectorTx)
	require.NoError(t, err)
	connectorTxHash := connectorTx.TxHash()

	// Create tree and node.
	tree, err := dbClient.Tree.Create().
		SetID(uuid.New()).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		Save(ctx)
	require.NoError(t, err)

	keyshareSecret := keys.GeneratePrivateKey()
	keyshare, err := dbClient.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keyshareSecret).
		SetPublicShares(map[string]keys.Public{"1": keyshareSecret.Public()}).
		SetPublicKey(keyshareSecret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	node, err := dbClient.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(keyshare).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeNodeStatusAvailable).
		SetValue(100_000).
		SetVerifyingPubkey(leafInternalPriv.Public()).
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerSigningPubkey(keys.GeneratePrivateKey().Public()).
		SetVout(0).
		SetRawTx(nodeTxBytes).
		Save(ctx)
	require.NoError(t, err)

	nodesByID := map[string]*ent.TreeNode{
		node.ID.String(): node,
	}

	// Create a valid 2-input CPFP refund tx.
	cpfpRefundTx := wire.NewMsgTx(3)
	cpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0},
		Sequence:         1000,
	})
	cpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: connectorTxHash, Index: 0},
		Sequence:         0,
	})
	cpfpRefundTx.AddTxOut(wire.NewTxOut(299_000, receiverPkScript))
	cpfpRefundTxBytes, err := common.SerializeTx(cpfpRefundTx)
	require.NoError(t, err)

	leafCpfpRefundMap := map[string][]byte{
		node.ID.String(): cpfpRefundTxBytes,
	}

	t.Run("rejects invalid connector reference when connector tx provided", func(t *testing.T) {
		// Create a refund tx with invalid connector reference.
		badRefundTx := wire.NewMsgTx(3)
		badRefundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0},
			Sequence:         1000,
		})
		badRefundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{99}, Index: 0}, // Invalid
			Sequence:         0,
		})
		badRefundTx.AddTxOut(wire.NewTxOut(299_000, receiverPkScript))
		badRefundTxBytes, err := common.SerializeTx(badRefundTx)
		require.NoError(t, err)

		badLeafCpfpRefundMap := map[string][]byte{
			node.ID.String(): badRefundTxBytes,
		}

		err = validateTransactionCooperativeExitLegacyLeavesToSend(
			ctx,
			nodesByID,
			badLeafCpfpRefundMap,
			nil, // directRefundMap
			nil, // directFromCpfpRefundMap
			receiverPriv.Public(),
			connectorTxBytes,
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not reference a valid connector output")
	})

	t.Run("accepts valid connector reference when connector tx provided", func(t *testing.T) {
		err = validateTransactionCooperativeExitLegacyLeavesToSend(
			ctx,
			nodesByID,
			leafCpfpRefundMap,
			nil, // directRefundMap
			nil, // directFromCpfpRefundMap
			receiverPriv.Public(),
			connectorTxBytes,
		)

		if err != nil {
			require.NotContains(t, err.Error(), "does not reference a valid connector output")
		}
	})
}
