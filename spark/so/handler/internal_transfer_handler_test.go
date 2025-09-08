package handler

import (
	"bytes"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func createTestTxBytes(t *testing.T, value int64) []byte {
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}, Index: 0}, nil, nil))
	pkScript, err := txscript.NewScriptBuilder().AddOp(txscript.OP_TRUE).Script()
	require.NoError(t, err)
	tx.AddTxOut(wire.NewTxOut(value, pkScript))
	var buf bytes.Buffer
	require.NoError(t, tx.Serialize(&buf))
	return buf.Bytes()
}

func TestFinalizeTransfer(t *testing.T) {
	ctx, dbCtx := db.SetUpPostgresTestContext(t)

	config := &so.Config{
		BitcoindConfigs: map[string]so.BitcoindConfig{
			"regtest": {DepositConfirmationThreshold: 1},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	t.Run("successful finalize transfer", func(t *testing.T) {
		// Create test tx bytes
		rawTx := createTestTxBytes(t, 1000)
		rawRefundTx := createTestTxBytes(t, 1001)
		directTx := createTestTxBytes(t, 1002)
		directRefundTx := createTestTxBytes(t, 1003)
		directFromCpfpRefundTx := createTestTxBytes(t, 1004)

		rawTxUpdated := createTestTxBytes(t, 2000)
		rawRefundTxUpdated := createTestTxBytes(t, 2001)
		directRefundTxUpdated := createTestTxBytes(t, 2003)
		directFromCpfpRefundTxUpdated := createTestTxBytes(t, 2004)

		newRawRefundTx := createTestTxBytes(t, 3001)

		// Create test signing keyshare
		signingKeyshare, err := dbCtx.Client.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare([]byte("test_secret_share")).
			SetPublicShares(map[string][]byte{"test": []byte("test_public_share")}).
			SetPublicKey([]byte("test_public_key")).
			SetMinSigners(2).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		// Create test tree
		tree, err := dbCtx.Client.Tree.Create().
			SetStatus(st.TreeStatusAvailable).
			SetNetwork(st.NetworkRegtest).
			SetOwnerIdentityPubkey([]byte("test_owner_identity")).
			SetBaseTxid([]byte("test_base_txid")).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)

		// Create test tree node (leaf)
		leaf, err := dbCtx.Client.TreeNode.Create().
			SetStatus(st.TreeNodeStatusAvailable).
			SetTree(tree).
			SetSigningKeyshare(signingKeyshare).
			SetValue(1000).
			SetVerifyingPubkey([]byte("test_verifying_pubkey")).
			SetOwnerIdentityPubkey([]byte("test_owner_identity")).
			SetOwnerSigningPubkey([]byte("test_owner_signing")).
			SetRawTx(rawTx).
			SetRawRefundTx(rawRefundTx).
			SetDirectTx(directTx).
			SetDirectRefundTx(directRefundTx).
			SetDirectFromCpfpRefundTx(directFromCpfpRefundTx).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)

		// Create test transfer
		transfer, err := dbCtx.Client.Transfer.Create().
			SetStatus(st.TransferStatusReceiverRefundSigned).
			SetType(st.TransferTypeTransfer).
			SetSenderIdentityPubkey([]byte("test_sender_identity")).
			SetReceiverIdentityPubkey([]byte("test_receiver_identity")).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(24 * time.Hour)).
			SetCompletionTime(time.Now()).
			Save(ctx)

		require.NoError(t, err)

		// Create transfer leaf linking transfer to tree node
		_, err = dbCtx.Client.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf).
			SetPreviousRefundTx([]byte("test_previous_refund_tx")).
			SetIntermediateRefundTx([]byte("test_intermediate_refund_tx")).
			Save(ctx)
		require.NoError(t, err)

		// Create internal node for the request
		internalNode := &pbinternal.TreeNode{
			Id:                     leaf.ID.String(),
			Value:                  1000,                            // Must match the original value since it's immutable
			VerifyingPubkey:        []byte("test_verifying_pubkey"), // Must match the original value since it's immutable
			OwnerIdentityPubkey:    []byte("test_owner_identity_updated"),
			OwnerSigningPubkey:     []byte("test_owner_signing_updated"),
			RawTx:                  rawTxUpdated,
			RawRefundTx:            rawRefundTxUpdated,
			DirectTx:               createTestTxBytes(t, 2002),
			DirectRefundTx:         directRefundTxUpdated,
			DirectFromCpfpRefundTx: directFromCpfpRefundTxUpdated,
			TreeId:                 tree.ID.String(),
			SigningKeyshareId:      signingKeyshare.ID.String(),
			Vout:                   1,
		}

		// Test the FinalizeTransfer method
		internalTransferHandler := NewInternalTransferHandler(config)

		err = internalTransferHandler.FinalizeTransfer(ctx, &pbinternal.FinalizeTransferRequest{
			TransferId: transfer.ID.String(),
			Nodes:      []*pbinternal.TreeNode{internalNode},
			Timestamp:  timestamppb.New(time.Now()),
		})
		require.NoError(t, err)

		// Commit the transaction to persist changes
		tx, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		err = tx.Commit()
		require.NoError(t, err)

		// Verify the transfer status was updated
		updatedTransfer, err := dbCtx.Client.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusCompleted, updatedTransfer.Status)

		// Verify the leaf node was updated (only certain fields are updated by FinalizeTransfer)
		updatedLeaf, err := dbCtx.Client.TreeNode.Get(ctx, leaf.ID)
		require.NoError(t, err)
		assert.Equal(t, rawTxUpdated, updatedLeaf.RawTx)
		assert.Equal(t, rawRefundTxUpdated, updatedLeaf.RawRefundTx)
		assert.Equal(t, directTx, updatedLeaf.DirectTx) // DirectTx is NOT updated by FinalizeTransfer
		assert.Equal(t, directRefundTxUpdated, updatedLeaf.DirectRefundTx)
		assert.Equal(t, directFromCpfpRefundTxUpdated, updatedLeaf.DirectFromCpfpRefundTx)

		// Create another copy of the internal node for the request, but with different RawRefundTx
		internalNode2 := &pbinternal.TreeNode{
			Id:                     leaf.ID.String(),
			Value:                  1000,                            // Must match the original value since it's immutable
			VerifyingPubkey:        []byte("test_verifying_pubkey"), // Must match the original value since it's immutable
			OwnerIdentityPubkey:    []byte("test_owner_identity_updated"),
			OwnerSigningPubkey:     []byte("test_owner_signing_updated"),
			RawTx:                  rawTxUpdated,
			RawRefundTx:            newRawRefundTx,
			DirectTx:               createTestTxBytes(t, 2002),
			DirectRefundTx:         directRefundTxUpdated,
			DirectFromCpfpRefundTx: directFromCpfpRefundTxUpdated,
			TreeId:                 tree.ID.String(),
			SigningKeyshareId:      signingKeyshare.ID.String(),
			Vout:                   1,
		}

		// Test the FinalizeTransfer method with the new internal node
		err = internalTransferHandler.FinalizeTransfer(ctx, &pbinternal.FinalizeTransferRequest{
			TransferId: transfer.ID.String(),
			Nodes:      []*pbinternal.TreeNode{internalNode2},
			Timestamp:  timestamppb.New(time.Now()),
		})
		require.NoError(t, err)

		// Commit the transaction to persist changes
		tx, err = ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		err = tx.Commit()
		require.NoError(t, err)

		// Verify the transfer status was updated
		updatedTransfer2, err := dbCtx.Client.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusCompleted, updatedTransfer2.Status)

		// Verify the leaf node was updated (only certain fields are updated by FinalizeTransfer)
		updatedLeaf2, err := dbCtx.Client.TreeNode.Get(ctx, leaf.ID)
		require.NoError(t, err)
		assert.Equal(t, rawTxUpdated, updatedLeaf2.RawTx)
		assert.Equal(t, newRawRefundTx, updatedLeaf2.RawRefundTx)
		assert.Equal(t, directTx, updatedLeaf2.DirectTx) // DirectTx is NOT updated by FinalizeTransfer
		assert.Equal(t, directRefundTxUpdated, updatedLeaf2.DirectRefundTx)
		assert.Equal(t, directFromCpfpRefundTxUpdated, updatedLeaf2.DirectFromCpfpRefundTx)
	})
}
