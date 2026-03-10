package handler

import (
	"bytes"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	sparkProto "github.com/lightsparkdev/spark/proto/spark"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
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

func makeP2TRFundingTx(value int64, internalPriv keys.Private) (txBytes []byte, outpoint wire.OutPoint, pkScript []byte, prevAmt int64, tweakedPriv keys.Private, err error) {
	tweakedPriv = keys.PrivateFromKey(*txscript.TweakTaprootPrivKey(*internalPriv.ToBTCEC(), nil))
	xOnly := tweakedPriv.Public().SerializeXOnly()
	pkScript = append([]byte{txscript.OP_1, 32}, xOnly...)
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}, Index: 0}, nil, nil))
	tx.AddTxOut(wire.NewTxOut(value, pkScript))
	var buf bytes.Buffer
	if err = tx.Serialize(&buf); err != nil {
		return
	}
	txid := tx.TxHash()
	outpoint = wire.OutPoint{Hash: txid, Index: 0}
	prevAmt = value
	txBytes = buf.Bytes()
	return
}

func makeP2TRSpendTx(prevOut wire.OutPoint, prevPkScript []byte, prevAmt int64, tweakedPriv keys.Private, sendValue int64, destScript []byte) ([]byte, error) {
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(wire.NewTxIn(&prevOut, nil, nil))
	tx.AddTxOut(wire.NewTxOut(sendValue, destScript))
	prevFetcher := txscript.NewCannedPrevOutputFetcher(prevPkScript, prevAmt)
	hashes := txscript.NewTxSigHashes(tx, prevFetcher)
	sighash, err := txscript.CalcTaprootSignatureHash(hashes, txscript.SigHashDefault, tx, 0, prevFetcher)
	if err != nil {
		return nil, err
	}
	sig, err := schnorr.Sign(tweakedPriv.ToBTCEC(), sighash)
	if err != nil {
		return nil, err
	}
	tx.TxIn[0].SignatureScript = nil
	tx.TxIn[0].Witness = wire.TxWitness{sig.Serialize()}
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func createTestTxBytes(t *testing.T, value int64) []byte {
	tx := wire.NewMsgTx(3)
	input := wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}, Index: 0}, nil, nil)
	input.Sequence = 2000
	tx.AddTxIn(input)
	pkScript, err := txscript.NewScriptBuilder().AddOp(txscript.OP_TRUE).Script()
	require.NoError(t, err)
	tx.AddTxOut(wire.NewTxOut(value, pkScript))
	var buf bytes.Buffer
	require.NoError(t, tx.Serialize(&buf))
	return buf.Bytes()
}

func TestFinalizeTransfer(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)

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

		// Create test signing keyshare
		rng := rand.NewChaCha8([32]byte{})
		keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		verifyingPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

		signingKeyshare, err := dbCtx.Client.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare(keysharePrivKey).
			SetPublicShares(map[string]keys.Public{"test": publicSharePrivKey.Public()}).
			SetPublicKey(keysharePrivKey.Public()).
			SetMinSigners(2).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		// Create test tree
		baseTxid := st.NewRandomTxIDForTesting(t)
		tree, err := dbCtx.Client.Tree.Create().
			SetStatus(st.TreeStatusAvailable).
			SetNetwork(btcnetwork.Regtest).
			SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
			SetBaseTxid(baseTxid).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)

		// Create test tree node (leaf)
		leaf, err := dbCtx.Client.TreeNode.Create().
			SetStatus(st.TreeNodeStatusAvailable).
			SetTree(tree).
			SetNetwork(tree.Network).
			SetSigningKeyshare(signingKeyshare).
			SetValue(1000).
			SetVerifyingPubkey(verifyingPrivKey.Public()).
			SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
			SetOwnerSigningPubkey(ownerSigningPrivKey.Public()).
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
			SetNetwork(tree.Network).
			SetStatus(st.TransferStatusReceiverRefundSigned).
			SetType(st.TransferTypeTransfer).
			SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
			SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(24 * time.Hour)).
			SetCompletionTime(time.Now()).
			Save(ctx)

		require.NoError(t, err)

		// Create transfer leaf linking transfer to tree node
		_, err = dbCtx.Client.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf).
			SetPreviousRefundTx(createTestTxBytes(t, 2000)).
			SetIntermediateRefundTx(createTestTxBytes(t, 2001)).
			Save(ctx)
		require.NoError(t, err)

		// Create internal node for the request
		updatedOwnerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		updatedOwnerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

		internalNode := &pbinternal.TreeNode{
			Id:                     leaf.ID.String(),
			Value:                  1000,                                  // Must match the original value since it's immutable
			VerifyingPubkey:        verifyingPrivKey.Public().Serialize(), // Must match the original value since it's immutable
			OwnerIdentityPubkey:    updatedOwnerIdentityPrivKey.Public().Serialize(),
			OwnerSigningPubkey:     updatedOwnerSigningPrivKey.Public().Serialize(),
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
		entTx, err := ent.GetTxFromContext(ctx)
		require.NoError(t, err)
		err = entTx.Commit()
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

		// Idempotent replay: send the same data again (transfer already Completed).
		err = internalTransferHandler.FinalizeTransfer(ctx, &pbinternal.FinalizeTransferRequest{
			TransferId: transfer.ID.String(),
			Nodes:      []*pbinternal.TreeNode{internalNode},
			Timestamp:  timestamppb.New(time.Now()),
		})
		require.NoError(t, err)

		// Commit the transaction to persist changes
		entTx, err = ent.GetTxFromContext(ctx)
		require.NoError(t, err)
		err = entTx.Commit()
		require.NoError(t, err)

		// Verify everything is still correct after replay.
		updatedTransfer2, err := dbCtx.Client.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusCompleted, updatedTransfer2.Status)

		updatedLeaf2, err := dbCtx.Client.TreeNode.Get(ctx, leaf.ID)
		require.NoError(t, err)
		assert.Equal(t, rawTxUpdated, updatedLeaf2.RawTx)
		assert.Equal(t, rawRefundTxUpdated, updatedLeaf2.RawRefundTx)
		assert.Equal(t, directTx, updatedLeaf2.DirectTx) // DirectTx is NOT updated by FinalizeTransfer
		assert.Equal(t, directRefundTxUpdated, updatedLeaf2.DirectRefundTx)
		assert.Equal(t, directFromCpfpRefundTxUpdated, updatedLeaf2.DirectFromCpfpRefundTx)
	})

	t.Run("marks receivers completed", func(t *testing.T) {
		rng := rand.NewChaCha8([32]byte{2})

		rawTx := createTestTxBytes(t, 5000)
		rawRefundTx := createTestTxBytes(t, 5001)
		directTx := createTestTxBytes(t, 5002)
		directRefundTx := createTestTxBytes(t, 5003)
		directFromCpfpRefundTx := createTestTxBytes(t, 5004)
		rawTxUpdated := createTestTxBytes(t, 6000)
		rawRefundTxUpdated := createTestTxBytes(t, 6001)
		directRefundTxUpdated := createTestTxBytes(t, 6003)
		directFromCpfpRefundTxUpdated := createTestTxBytes(t, 6004)

		keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		verifyingPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		otherReceiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

		signingKeyshare, err := dbCtx.Client.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare(keysharePrivKey).
			SetPublicShares(map[string]keys.Public{"test": publicSharePrivKey.Public()}).
			SetPublicKey(keysharePrivKey.Public()).
			SetMinSigners(2).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		baseTxid := st.NewRandomTxIDForTesting(t)
		tree, err := dbCtx.Client.Tree.Create().
			SetStatus(st.TreeStatusAvailable).
			SetNetwork(btcnetwork.Regtest).
			SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
			SetBaseTxid(baseTxid).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)

		leaf, err := dbCtx.Client.TreeNode.Create().
			SetStatus(st.TreeNodeStatusAvailable).
			SetTree(tree).
			SetNetwork(tree.Network).
			SetSigningKeyshare(signingKeyshare).
			SetValue(5000).
			SetVerifyingPubkey(verifyingPrivKey.Public()).
			SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
			SetOwnerSigningPubkey(ownerSigningPrivKey.Public()).
			SetRawTx(rawTx).
			SetRawRefundTx(rawRefundTx).
			SetDirectTx(directTx).
			SetDirectRefundTx(directRefundTx).
			SetDirectFromCpfpRefundTx(directFromCpfpRefundTx).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)

		transfer, err := dbCtx.Client.Transfer.Create().
			SetNetwork(tree.Network).
			SetStatus(st.TransferStatusReceiverRefundSigned).
			SetType(st.TransferTypeTransfer).
			SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
			SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
			SetTotalValue(5000).
			SetExpiryTime(time.Now().Add(24 * time.Hour)).
			SetCompletionTime(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		_, err = dbCtx.Client.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf).
			SetPreviousRefundTx(createTestTxBytes(t, 7000)).
			SetIntermediateRefundTx(createTestTxBytes(t, 7001)).
			Save(ctx)
		require.NoError(t, err)

		// Create a receiver pending completion.
		pendingReceiver, err := dbCtx.Client.TransferReceiver.Create().
			SetTransfer(transfer).
			SetIdentityPubkey(receiverIdentityPrivKey.Public()).
			SetStatus(st.TransferReceiverStatusRefundSigned).
			Save(ctx)
		require.NoError(t, err)

		// Create a second receiver already completed — should not be modified.
		alreadyDoneReceiver, err := dbCtx.Client.TransferReceiver.Create().
			SetTransfer(transfer).
			SetIdentityPubkey(otherReceiverPrivKey.Public()).
			SetStatus(st.TransferReceiverStatusCompleted).
			SetCompletionTime(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		internalNode := &pbinternal.TreeNode{
			Id:                     leaf.ID.String(),
			Value:                  5000,
			VerifyingPubkey:        verifyingPrivKey.Public().Serialize(),
			OwnerIdentityPubkey:    keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			OwnerSigningPubkey:     keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			RawTx:                  rawTxUpdated,
			RawRefundTx:            rawRefundTxUpdated,
			DirectTx:               createTestTxBytes(t, 7002),
			DirectRefundTx:         directRefundTxUpdated,
			DirectFromCpfpRefundTx: directFromCpfpRefundTxUpdated,
			TreeId:                 tree.ID.String(),
			SigningKeyshareId:      signingKeyshare.ID.String(),
			Vout:                   1,
		}

		internalTransferHandler := NewInternalTransferHandler(config)
		err = internalTransferHandler.FinalizeTransfer(ctx, &pbinternal.FinalizeTransferRequest{
			TransferId: transfer.ID.String(),
			Nodes:      []*pbinternal.TreeNode{internalNode},
			Timestamp:  timestamppb.New(time.Now()),
		})
		require.NoError(t, err)

		entTx, err := ent.GetTxFromContext(ctx)
		require.NoError(t, err)
		err = entTx.Commit()
		require.NoError(t, err)

		// Pending receiver is now completed.
		updatedPending, err := dbCtx.Client.TransferReceiver.Get(ctx, pendingReceiver.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusCompleted, updatedPending.Status)
		assert.NotNil(t, updatedPending.CompletionTime)

		// Already-completed receiver is unchanged.
		updatedDone, err := dbCtx.Client.TransferReceiver.Get(ctx, alreadyDoneReceiver.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusCompleted, updatedDone.Status)
	})
}

func TestFinalizeTransferReceiver(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)

	config := &so.Config{
		BitcoindConfigs: map[string]so.BitcoindConfig{
			"regtest": {DepositConfirmationThreshold: 1},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	// Note: the database enforces uniqueness of (transfer_id, identity_pubkey) via
	// the "transferreceiver_transfer_id_identity_pubkey" constraint, so len > 1 cannot
	// occur in practice. The code assertion covers len == 0 and provides defense in depth.
	t.Run("errors when no receiver matches the identity pubkey", func(t *testing.T) {
		rng := rand.NewChaCha8([32]byte{3})
		senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		unknownPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

		transfer, err := dbCtx.Client.Transfer.Create().
			SetNetwork(btcnetwork.Regtest).
			SetStatus(st.TransferStatusReceiverRefundSigned).
			SetType(st.TransferTypeTransfer).
			SetSenderIdentityPubkey(senderPrivKey.Public()).
			SetReceiverIdentityPubkey(receiverPrivKey.Public()).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(24 * time.Hour)).
			SetCompletionTime(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		_, err = dbCtx.Client.TransferReceiver.Create().
			SetTransfer(transfer).
			SetIdentityPubkey(receiverPrivKey.Public()).
			SetStatus(st.TransferReceiverStatusRefundSigned).
			Save(ctx)
		require.NoError(t, err)

		handler := NewInternalTransferHandler(config)
		err = handler.FinalizeTransferReceiver(ctx, &pbgossip.GossipMessageFinalizeTransferReceiver{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: unknownPrivKey.Public().Serialize(),
			CompletionTimestamp:       timestamppb.Now(),
		})
		require.ErrorContains(t, err, "expected exactly 1")
	})

	t.Run("successful finalize and idempotent replay", func(t *testing.T) {
		rng := rand.NewChaCha8([32]byte{4})

		rawTx := createTestTxBytes(t, 8000)
		rawRefundTx := createTestTxBytes(t, 8001)
		directTx := createTestTxBytes(t, 8002)
		directRefundTx := createTestTxBytes(t, 8003)
		directFromCpfpRefundTx := createTestTxBytes(t, 8004)

		rawTxUpdated := createTestTxBytes(t, 9000)
		rawRefundTxUpdated := createTestTxBytes(t, 9001)
		directRefundTxUpdated := createTestTxBytes(t, 9003)
		directFromCpfpRefundTxUpdated := createTestTxBytes(t, 9004)

		keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		verifyingPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

		signingKeyshare, err := dbCtx.Client.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare(keysharePrivKey).
			SetPublicShares(map[string]keys.Public{"test": publicSharePrivKey.Public()}).
			SetPublicKey(keysharePrivKey.Public()).
			SetMinSigners(2).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		baseTxid := st.NewRandomTxIDForTesting(t)
		tree, err := dbCtx.Client.Tree.Create().
			SetStatus(st.TreeStatusAvailable).
			SetNetwork(btcnetwork.Regtest).
			SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
			SetBaseTxid(baseTxid).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)

		leaf, err := dbCtx.Client.TreeNode.Create().
			SetStatus(st.TreeNodeStatusTransferLocked).
			SetTree(tree).
			SetNetwork(tree.Network).
			SetSigningKeyshare(signingKeyshare).
			SetValue(8000).
			SetVerifyingPubkey(verifyingPrivKey.Public()).
			SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
			SetOwnerSigningPubkey(ownerSigningPrivKey.Public()).
			SetRawTx(rawTx).
			SetRawRefundTx(rawRefundTx).
			SetDirectTx(directTx).
			SetDirectRefundTx(directRefundTx).
			SetDirectFromCpfpRefundTx(directFromCpfpRefundTx).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)

		transfer, err := dbCtx.Client.Transfer.Create().
			SetNetwork(tree.Network).
			SetStatus(st.TransferStatusReceiverRefundSigned).
			SetType(st.TransferTypeTransfer).
			SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
			SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
			SetTotalValue(8000).
			SetExpiryTime(time.Now().Add(24 * time.Hour)).
			SetCompletionTime(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		receiver, err := dbCtx.Client.TransferReceiver.Create().
			SetTransfer(transfer).
			SetIdentityPubkey(receiverIdentityPrivKey.Public()).
			SetStatus(st.TransferReceiverStatusRefundSigned).
			Save(ctx)
		require.NoError(t, err)

		_, err = dbCtx.Client.TransferLeaf.Create().
			SetTransfer(transfer).
			SetTransferReceiver(receiver).
			SetLeaf(leaf).
			SetPreviousRefundTx(createTestTxBytes(t, 10000)).
			SetIntermediateRefundTx(createTestTxBytes(t, 10001)).
			Save(ctx)
		require.NoError(t, err)

		updatedOwnerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		updatedOwnerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

		internalNode := &pbinternal.TreeNode{
			Id:                     leaf.ID.String(),
			Value:                  8000,
			VerifyingPubkey:        verifyingPrivKey.Public().Serialize(),
			OwnerIdentityPubkey:    updatedOwnerIdentityPrivKey.Public().Serialize(),
			OwnerSigningPubkey:     updatedOwnerSigningPrivKey.Public().Serialize(),
			RawTx:                  rawTxUpdated,
			RawRefundTx:            rawRefundTxUpdated,
			DirectTx:               createTestTxBytes(t, 10002),
			DirectRefundTx:         directRefundTxUpdated,
			DirectFromCpfpRefundTx: directFromCpfpRefundTxUpdated,
			TreeId:                 tree.ID.String(),
			SigningKeyshareId:      signingKeyshare.ID.String(),
			Vout:                   1,
		}

		completionTime := timestamppb.New(time.Now())

		// --- First call: happy path ---
		handler := NewInternalTransferHandler(config)
		err = handler.FinalizeTransferReceiver(ctx, &pbgossip.GossipMessageFinalizeTransferReceiver{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiverIdentityPrivKey.Public().Serialize(),
			InternalNodes:             []*pbinternal.TreeNode{internalNode},
			CompletionTimestamp:       completionTime,
		})
		require.NoError(t, err)

		entTx, err := ent.GetTxFromContext(ctx)
		require.NoError(t, err)
		err = entTx.Commit()
		require.NoError(t, err)

		// Verify receiver is completed.
		updatedReceiver, err := dbCtx.Client.TransferReceiver.Get(ctx, receiver.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusCompleted, updatedReceiver.Status)

		// Verify transfer is completed (single receiver, so transfer completes too).
		updatedTransfer, err := dbCtx.Client.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusCompleted, updatedTransfer.Status)

		// Verify leaf node was updated.
		updatedLeaf, err := dbCtx.Client.TreeNode.Get(ctx, leaf.ID)
		require.NoError(t, err)
		assert.Equal(t, rawTxUpdated, updatedLeaf.RawTx)
		assert.Equal(t, rawRefundTxUpdated, updatedLeaf.RawRefundTx)
		assert.Equal(t, directRefundTxUpdated, updatedLeaf.DirectRefundTx)
		assert.Equal(t, directFromCpfpRefundTxUpdated, updatedLeaf.DirectFromCpfpRefundTx)

		// --- Second call: idempotent replay with same data ---
		err = handler.FinalizeTransferReceiver(ctx, &pbgossip.GossipMessageFinalizeTransferReceiver{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiverIdentityPrivKey.Public().Serialize(),
			InternalNodes:             []*pbinternal.TreeNode{internalNode},
			CompletionTimestamp:       completionTime,
		})
		require.NoError(t, err)

		entTx, err = ent.GetTxFromContext(ctx)
		require.NoError(t, err)
		err = entTx.Commit()
		require.NoError(t, err)

		// Verify leaf data unchanged after replay.
		updatedLeaf2, err := dbCtx.Client.TreeNode.Get(ctx, leaf.ID)
		require.NoError(t, err)
		assert.Equal(t, rawTxUpdated, updatedLeaf2.RawTx)
		assert.Equal(t, rawRefundTxUpdated, updatedLeaf2.RawRefundTx)

		// Verify transfer/receiver still completed.
		updatedTransfer2, err := dbCtx.Client.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusCompleted, updatedTransfer2.Status)

		// --- Third call: idempotent replay with different completion timestamp ---
		differentTime := timestamppb.New(time.Now().Add(5 * time.Minute))
		err = handler.FinalizeTransferReceiver(ctx, &pbgossip.GossipMessageFinalizeTransferReceiver{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiverIdentityPrivKey.Public().Serialize(),
			InternalNodes:             []*pbinternal.TreeNode{internalNode},
			CompletionTimestamp:       differentTime,
		})
		require.NoError(t, err, "should succeed even with different timestamp")
	})

}

func TestFinalizeTransferReceiverMultiReceiver(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)

	config := &so.Config{
		BitcoindConfigs: map[string]so.BitcoindConfig{
			"regtest": {DepositConfirmationThreshold: 1},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	// Deterministic key generation for reproducibility.
	rng := rand.NewChaCha8([32]byte{10})

	// Keys.
	senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiver1IdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiver2IdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	verifyingPrivKey1 := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningPrivKey1 := keys.MustGeneratePrivateKeyFromRand(rng)
	verifyingPrivKey2 := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningPrivKey2 := keys.MustGeneratePrivateKeyFromRand(rng)
	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	// Initial tx bytes stored on tree nodes before the transfer.
	rawTx1 := createTestTxBytes(t, 10_000)
	rawRefundTx1 := createTestTxBytes(t, 10_001)
	directRefundTx1 := createTestTxBytes(t, 10_003)
	directFromCpfpRefundTx1 := createTestTxBytes(t, 10_004)

	rawTx2 := createTestTxBytes(t, 20_000)
	rawRefundTx2 := createTestTxBytes(t, 20_001)
	directRefundTx2 := createTestTxBytes(t, 20_003)
	directFromCpfpRefundTx2 := createTestTxBytes(t, 20_004)

	// Tx bytes delivered via gossip after the coordinator finalizes the claim.
	// In production, RawTx is unchanged by transfers (the UTXO stays the same;
	// only FROST key shares are tweaked so the new owner can spend it). The
	// coordinator's updateNode passes RawTx through as-is for TRANSFER intent
	// and MarshalInternalProto includes it in the gossip payload. The receiving
	// SO writes it back unconditionally. We use distinct values here so that
	// assertions can verify which leaf was written by which gossip message.
	//
	// Refund txs DO change in production — they get new aggregated FROST
	// signatures for the receiver's tweaked key shares.
	rawTx1Updated := createTestTxBytes(t, 11_000)
	rawRefundTx1Updated := createTestTxBytes(t, 11_001)
	directRefundTx1Updated := createTestTxBytes(t, 11_003)
	directFromCpfpRefundTx1Updated := createTestTxBytes(t, 11_004)

	rawTx2Updated := createTestTxBytes(t, 21_000)
	rawRefundTx2Updated := createTestTxBytes(t, 21_001)
	directRefundTx2Updated := createTestTxBytes(t, 21_003)
	directFromCpfpRefundTx2Updated := createTestTxBytes(t, 21_004)

	// Shared signing keyshare.
	signingKeyshare, err := dbCtx.Client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keysharePrivKey).
		SetPublicShares(map[string]keys.Public{"test": publicSharePrivKey.Public()}).
		SetPublicKey(keysharePrivKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// Two trees — one per leaf.
	baseTxid1 := st.NewRandomTxIDForTesting(t)
	tree1, err := dbCtx.Client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
		SetBaseTxid(baseTxid1).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	baseTxid2 := st.NewRandomTxIDForTesting(t)
	tree2, err := dbCtx.Client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
		SetBaseTxid(baseTxid2).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	// Leaf 1 — will be assigned to receiver 1 via TransferLeaf.
	leaf1, err := dbCtx.Client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusTransferLocked).
		SetTree(tree1).
		SetNetwork(tree1.Network).
		SetSigningKeyshare(signingKeyshare).
		SetValue(5000).
		SetVerifyingPubkey(verifyingPrivKey1.Public()).
		SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
		SetOwnerSigningPubkey(ownerSigningPrivKey1.Public()).
		SetRawTx(rawTx1).
		SetRawRefundTx(rawRefundTx1).
		SetDirectRefundTx(directRefundTx1).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTx1).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	// Leaf 2 — will be assigned to receiver 2 via TransferLeaf.
	leaf2, err := dbCtx.Client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusTransferLocked).
		SetTree(tree2).
		SetNetwork(tree2.Network).
		SetSigningKeyshare(signingKeyshare).
		SetValue(3000).
		SetVerifyingPubkey(verifyingPrivKey2.Public()).
		SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
		SetOwnerSigningPubkey(ownerSigningPrivKey2.Public()).
		SetRawTx(rawTx2).
		SetRawRefundTx(rawRefundTx2).
		SetDirectRefundTx(directRefundTx2).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTx2).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	// Transfer with two receivers.
	transfer, err := dbCtx.Client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusReceiverRefundSigned).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiver1IdentityPrivKey.Public()).
		SetTotalValue(8000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	// Receiver 1.
	receiver1, err := dbCtx.Client.TransferReceiver.Create().
		SetTransfer(transfer).
		SetIdentityPubkey(receiver1IdentityPrivKey.Public()).
		SetStatus(st.TransferReceiverStatusRefundSigned).
		Save(ctx)
	require.NoError(t, err)

	// Receiver 2.
	receiver2, err := dbCtx.Client.TransferReceiver.Create().
		SetTransfer(transfer).
		SetIdentityPubkey(receiver2IdentityPrivKey.Public()).
		SetStatus(st.TransferReceiverStatusRefundSigned).
		Save(ctx)
	require.NoError(t, err)

	// TransferLeaf 1 → leaf1, linked to receiver1.
	_, err = dbCtx.Client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf1).
		SetTransferReceiver(receiver1).
		SetPreviousRefundTx(createTestTxBytes(t, 30_000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 30_001)).
		Save(ctx)
	require.NoError(t, err)

	// TransferLeaf 2 → leaf2, linked to receiver2.
	_, err = dbCtx.Client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf2).
		SetTransferReceiver(receiver2).
		SetPreviousRefundTx(createTestTxBytes(t, 30_002)).
		SetIntermediateRefundTx(createTestTxBytes(t, 30_003)).
		Save(ctx)
	require.NoError(t, err)

	handler := NewInternalTransferHandler(config)

	// Gossip nodes delivered by coordinator.
	gossipNode1 := &pbinternal.TreeNode{
		Id:                     leaf1.ID.String(),
		RawTx:                  rawTx1Updated,
		RawRefundTx:            rawRefundTx1Updated,
		DirectRefundTx:         directRefundTx1Updated,
		DirectFromCpfpRefundTx: directFromCpfpRefundTx1Updated,
	}
	gossipNode2 := &pbinternal.TreeNode{
		Id:                     leaf2.ID.String(),
		RawTx:                  rawTx2Updated,
		RawRefundTx:            rawRefundTx2Updated,
		DirectRefundTx:         directRefundTx2Updated,
		DirectFromCpfpRefundTx: directFromCpfpRefundTx2Updated,
	}

	t.Run("two receivers finalize independently", func(t *testing.T) {
		ts1 := timestamppb.New(time.Now().Truncate(time.Microsecond))

		// --- Finalize receiver 1 ---
		err = handler.FinalizeTransferReceiver(ctx, &pbgossip.GossipMessageFinalizeTransferReceiver{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiver1IdentityPrivKey.Public().Serialize(),
			InternalNodes:             []*pbinternal.TreeNode{gossipNode1},
			CompletionTimestamp:       ts1,
		})
		require.NoError(t, err)

		// Mid-transaction verification: receiver 1 completed, receiver 2 unchanged, transfer NOT completed.
		txDB, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)

		r1, err := txDB.TransferReceiver.Get(ctx, receiver1.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusCompleted, r1.Status)

		r2, err := txDB.TransferReceiver.Get(ctx, receiver2.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusRefundSigned, r2.Status, "receiver 2 should be unchanged")

		xfer, err := txDB.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusReceiverRefundSigned, xfer.Status, "transfer should not be completed yet")

		// Leaf 1 should be Available; leaf 2 should still be TransferLocked.
		l1, err := txDB.TreeNode.Get(ctx, leaf1.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusAvailable, l1.Status)
		assert.Equal(t, rawTx1Updated, l1.RawTx)

		l2, err := txDB.TreeNode.Get(ctx, leaf2.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusTransferLocked, l2.Status, "leaf 2 should be untouched")
		assert.Equal(t, rawTx2, l2.RawTx, "leaf 2 raw tx should be original")

		// --- Finalize receiver 2 ---
		ts2 := timestamppb.New(time.Now().Truncate(time.Microsecond))
		err = handler.FinalizeTransferReceiver(ctx, &pbgossip.GossipMessageFinalizeTransferReceiver{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiver2IdentityPrivKey.Public().Serialize(),
			InternalNodes:             []*pbinternal.TreeNode{gossipNode2},
			CompletionTimestamp:       ts2,
		})
		require.NoError(t, err)

		// Commit and verify final state.
		entTx, err := ent.GetTxFromContext(ctx)
		require.NoError(t, err)
		err = entTx.Commit()
		require.NoError(t, err)

		// Both receivers completed.
		r1Final, err := dbCtx.Client.TransferReceiver.Get(ctx, receiver1.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusCompleted, r1Final.Status)

		r2Final, err := dbCtx.Client.TransferReceiver.Get(ctx, receiver2.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusCompleted, r2Final.Status)

		// Transfer completed.
		xferFinal, err := dbCtx.Client.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusCompleted, xferFinal.Status)

		// Both leaves available with updated txs.
		l1Final, err := dbCtx.Client.TreeNode.Get(ctx, leaf1.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusAvailable, l1Final.Status)
		assert.Equal(t, rawTx1Updated, l1Final.RawTx)
		assert.Equal(t, rawRefundTx1Updated, l1Final.RawRefundTx)
		assert.Equal(t, directRefundTx1Updated, l1Final.DirectRefundTx)
		assert.Equal(t, directFromCpfpRefundTx1Updated, l1Final.DirectFromCpfpRefundTx)

		l2Final, err := dbCtx.Client.TreeNode.Get(ctx, leaf2.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusAvailable, l2Final.Status)
		assert.Equal(t, rawTx2Updated, l2Final.RawTx)
		assert.Equal(t, rawRefundTx2Updated, l2Final.RawRefundTx)
		assert.Equal(t, directRefundTx2Updated, l2Final.DirectRefundTx)
		assert.Equal(t, directFromCpfpRefundTx2Updated, l2Final.DirectFromCpfpRefundTx)
	})

	t.Run("idempotent replay with same timestamp succeeds", func(t *testing.T) {
		// receiver1 is already Completed from the previous subtest.
		// Replaying with the same timestamp and same nodes should succeed.
		r1, err := dbCtx.Client.TransferReceiver.Get(ctx, receiver1.ID)
		require.NoError(t, err)
		ts := timestamppb.New(r1.CompletionTime)

		err = handler.FinalizeTransferReceiver(ctx, &pbgossip.GossipMessageFinalizeTransferReceiver{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiver1IdentityPrivKey.Public().Serialize(),
			InternalNodes:             []*pbinternal.TreeNode{gossipNode1},
			CompletionTimestamp:       ts,
		})
		require.NoError(t, err)
	})

	t.Run("replay with different timestamp succeeds idempotently", func(t *testing.T) {
		differentTs := timestamppb.New(time.Now().Add(1 * time.Hour).Truncate(time.Microsecond))

		err = handler.FinalizeTransferReceiver(ctx, &pbgossip.GossipMessageFinalizeTransferReceiver{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiver1IdentityPrivKey.Public().Serialize(),
			InternalNodes:             []*pbinternal.TreeNode{gossipNode1},
			CompletionTimestamp:       differentTs,
		})
		require.NoError(t, err, "should succeed idempotently even with different timestamp")
	})

	t.Run("node not belonging to receiver is rejected", func(t *testing.T) {
		// Send receiver1's gossip message but with receiver2's node.
		wrongNode := &pbinternal.TreeNode{
			Id:                     leaf2.ID.String(),
			RawTx:                  rawTx2Updated,
			RawRefundTx:            rawRefundTx2Updated,
			DirectRefundTx:         directRefundTx2Updated,
			DirectFromCpfpRefundTx: directFromCpfpRefundTx2Updated,
		}
		err = handler.FinalizeTransferReceiver(ctx, &pbgossip.GossipMessageFinalizeTransferReceiver{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiver1IdentityPrivKey.Public().Serialize(),
			InternalNodes:             []*pbinternal.TreeNode{wrongNode},
			CompletionTimestamp:       timestamppb.Now(),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not in receiver's leaves")
	})
}

func TestFinalizeTransferReceiver_RejectsEarlyTransferStatus(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)

	config := &so.Config{
		BitcoindConfigs: map[string]so.BitcoindConfig{
			"regtest": {DepositConfirmationThreshold: 1},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	rng := rand.NewChaCha8([32]byte{44})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	t.Run("rejects SenderInitiated transfer", func(t *testing.T) {
		transfer, err := dbCtx.Client.Transfer.Create().
			SetNetwork(btcnetwork.Regtest).
			SetStatus(st.TransferStatusSenderInitiated).
			SetType(st.TransferTypeTransfer).
			SetSenderIdentityPubkey(senderPrivKey.Public()).
			SetReceiverIdentityPubkey(receiverPrivKey.Public()).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(24 * time.Hour)).
			Save(ctx)
		require.NoError(t, err)

		_, err = dbCtx.Client.TransferReceiver.Create().
			SetTransfer(transfer).
			SetIdentityPubkey(receiverPrivKey.Public()).
			SetStatus(st.TransferReceiverStatusSenderInitiated).
			Save(ctx)
		require.NoError(t, err)

		handler := NewInternalTransferHandler(config)
		err = handler.FinalizeTransferReceiver(ctx, &pbgossip.GossipMessageFinalizeTransferReceiver{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiverPrivKey.Public().Serialize(),
			CompletionTimestamp:       timestamppb.Now(),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not ready for receiver claim")
	})

	t.Run("rejects Expired transfer", func(t *testing.T) {
		transfer, err := dbCtx.Client.Transfer.Create().
			SetNetwork(btcnetwork.Regtest).
			SetStatus(st.TransferStatusExpired).
			SetType(st.TransferTypeTransfer).
			SetSenderIdentityPubkey(senderPrivKey.Public()).
			SetReceiverIdentityPubkey(receiverPrivKey.Public()).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(24 * time.Hour)).
			Save(ctx)
		require.NoError(t, err)

		_, err = dbCtx.Client.TransferReceiver.Create().
			SetTransfer(transfer).
			SetIdentityPubkey(receiverPrivKey.Public()).
			SetStatus(st.TransferReceiverStatusSenderInitiated).
			Save(ctx)
		require.NoError(t, err)

		handler := NewInternalTransferHandler(config)
		err = handler.FinalizeTransferReceiver(ctx, &pbgossip.GossipMessageFinalizeTransferReceiver{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiverPrivKey.Public().Serialize(),
			CompletionTimestamp:       timestamppb.Now(),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "terminal state")
	})
}

func TestApplySignatures(t *testing.T) {
	t.Parallel()
	ctx, dbCtx := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})

	key := keys.GeneratePrivateKey()
	rawTx, outpoint, pkScript, prevAmt, tweakedPriv, err := makeP2TRFundingTx(1000, key)
	require.NoError(t, err)
	destScript := pkScript
	rawRefundTx, err := makeP2TRSpendTx(outpoint, pkScript, prevAmt, tweakedPriv, 900, destScript)
	require.NoError(t, err)

	dest1 := pkScript
	directTx, err := makeP2TRSpendTx(outpoint, pkScript, prevAmt, tweakedPriv, 880, dest1)
	require.NoError(t, err)

	out1, pk1, amt1 := getTxOutpoint(t, directTx, 0)
	dest2 := pkScript
	directRefundTx, err := makeP2TRSpendTx(out1, pk1, amt1, tweakedPriv, 860, dest2)
	require.NoError(t, err)

	// Create test signing keyshare
	secret := keys.MustGeneratePrivateKeyFromRand(rng)
	pubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	signingKeyshare, err := dbCtx.Client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"test": secret.Public()}).
		SetPublicKey(pubKey).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	baseTxid2 := st.NewRandomTxIDForTesting(t)
	tree, err := dbCtx.Client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetBaseTxid(baseTxid2).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	// Use the same key for both verifying and the tweaked key used in signing
	// This ensures signature verification will work correctly
	verifyingPrivKey := key // Use the same key that generates tweakedPriv
	verifyingPubKey := verifyingPrivKey.Public()
	leaf, err := dbCtx.Client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetSigningKeyshare(signingKeyshare).
		SetValue(1000).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerIdentityPubkey(key.Public()).
		SetOwnerSigningPubkey(key.Public()).
		SetRawTx(rawTx).
		SetRawRefundTx(rawRefundTx).
		SetDirectTx(directTx).
		SetDirectRefundTx(directRefundTx).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	transfer, err := dbCtx.Client.Transfer.Create().
		SetNetwork(tree.Network).
		SetStatus(st.TransferStatusReceiverRefundSigned).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(key.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetTotalValue(900).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetCompletionTime(time.Now()).
		Save(ctx)

	require.NoError(t, err)

	_, err = dbCtx.Client.TransferLeaf.Create().
		SetLeaf(leaf).
		SetTransfer(transfer).
		SetPreviousRefundTx(createTestTxBytes(t, 2000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 2001)).
		Save(ctx)
	require.NoError(t, err)

	// sign the P2TR output
	signature := getTxOutputSignature(t, directTx, directRefundTx, tweakedPriv)

	req := &pbinternal.InitiateTransferRequest{
		SenderIdentityPublicKey:   []byte("test_sender_identity"),
		ReceiverIdentityPublicKey: []byte("test_receiver_identity"),
		Leaves: []*pbinternal.InitiateTransferLeaf{{
			RawRefundTx:    rawRefundTx,
			DirectRefundTx: directRefundTx,
		}},
		Type: sparkProto.TransferType_TRANSFER,
	}

	testLeafId := "test_leaf_id"
	unknownLeafId := uuid.New().String()

	// Create adaptor signature test data using the correct transaction context
	_, adaptorSignature, adaptorPubKey := getTxOutputSignatureWithAdaptor(t, directTx, directRefundTx, tweakedPriv)

	// Create wrong adaptor key for failure test
	wrongAdaptorPrivKey := keys.GeneratePrivateKey()
	wrongAdaptorPubKey := wrongAdaptorPrivKey.Public()

	// Create invalid adaptor signature by modifying the real signature
	invalidAdaptorSig := make([]byte, len(signature)) // Use same length as original signature
	copy(invalidAdaptorSig, signature)
	invalidAdaptorSig[0] = ^invalidAdaptorSig[0] // Flip first byte

	tests := []struct {
		name                   string
		leafId                 string
		rawRefundTx            []byte
		directRefundTx         []byte
		directRefundSignatures map[string][]byte
		adaptorPublicKey       keys.Public
		expectedError          string
	}{
		{
			name:           "successfuly applied signatures",
			leafId:         leaf.ID.String(),
			rawRefundTx:    rawRefundTx,
			directRefundTx: directRefundTx,
			directRefundSignatures: map[string][]byte{
				leaf.ID.String(): signature,
			},
			adaptorPublicKey: keys.Public{}, // Empty adaptor key - regular signature verification
			expectedError:    "",
		},
		{
			name:           "successfully applied adaptor signatures",
			leafId:         leaf.ID.String(),
			rawRefundTx:    rawRefundTx,
			directRefundTx: directRefundTx,
			directRefundSignatures: map[string][]byte{
				leaf.ID.String(): adaptorSignature,
			},
			adaptorPublicKey: adaptorPubKey, // Valid adaptor key
			expectedError:    "",
		},
		{
			name:           "failed adaptor signature verification - wrong adaptor key",
			leafId:         leaf.ID.String(),
			rawRefundTx:    rawRefundTx,
			directRefundTx: directRefundTx,
			directRefundSignatures: map[string][]byte{
				leaf.ID.String(): adaptorSignature,
			},
			adaptorPublicKey: wrongAdaptorPubKey, // Wrong adaptor key
			expectedError:    "unable to validate adaptor signature",
		},
		{
			name:           "failed adaptor signature verification - invalid adaptor signature",
			leafId:         leaf.ID.String(),
			rawRefundTx:    rawRefundTx,
			directRefundTx: directRefundTx,
			directRefundSignatures: map[string][]byte{
				leaf.ID.String(): invalidAdaptorSig,
			},
			adaptorPublicKey: adaptorPubKey, // Correct adaptor key but invalid signature
			expectedError:    "unable to validate adaptor signature",
		},
		{
			name:           "unknown leaf refund signatures",
			leafId:         leaf.ID.String(),
			rawRefundTx:    rawRefundTx,
			directRefundTx: directRefundTx,
			directRefundSignatures: map[string][]byte{
				leaf.ID.String(): signature,
				unknownLeafId:    []byte("test_signature"),
			},
			adaptorPublicKey: keys.Public{},
			expectedError:    "no leaf refund found",
		},
		{
			name:           "broken leaf id",
			leafId:         testLeafId,
			rawRefundTx:    rawRefundTx,
			directRefundTx: directRefundTx,
			directRefundSignatures: map[string][]byte{
				testLeafId: signature,
			},
			adaptorPublicKey: keys.Public{},
			expectedError:    "unable to parse leaf id",
		},
		{
			name:           "unable to get tree node",
			leafId:         unknownLeafId,
			rawRefundTx:    rawRefundTx,
			directRefundTx: directRefundTx,
			directRefundSignatures: map[string][]byte{
				unknownLeafId: signature,
			},
			adaptorPublicKey: keys.Public{},
			expectedError:    "unable to get tree node",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req.DirectRefundSignatures = tt.directRefundSignatures
			req.Leaves = []*pbinternal.InitiateTransferLeaf{{
				LeafId:         tt.leafId,
				RawRefundTx:    tt.rawRefundTx,
				DirectRefundTx: tt.directRefundTx,
			}}

			_, map2, _ := loadInternalLeafRefundMaps(req)
			_, err = applySignaturesToTransactionsAndVerify(ctx, map2, req.DirectRefundSignatures, true, tt.adaptorPublicKey)

			if tt.expectedError != "" {
				require.ErrorContains(t, err, tt.expectedError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func getTxOutputSignature(t *testing.T, directTx, directRefundTx []byte, tweakedPriv keys.Private) []byte {
	var dr wire.MsgTx
	require.NoError(t, dr.Deserialize(bytes.NewReader(directRefundTx)))

	_, prevPk1, prevAmt1 := getTxOutpoint(t, directTx, 0)

	prevFetcher := txscript.NewCannedPrevOutputFetcher(prevPk1, prevAmt1)
	hashes := txscript.NewTxSigHashes(&dr, prevFetcher)

	sigHash, err := txscript.CalcTaprootSignatureHash(hashes, txscript.SigHashDefault, &dr, 0, prevFetcher)
	require.NoError(t, err)

	directRefundSig, err := schnorr.Sign(tweakedPriv.ToBTCEC(), sigHash)
	require.NoError(t, err)

	return directRefundSig.Serialize()
}

// Helper function to get both signature and adaptor signature for the same transaction
func getTxOutputSignatureWithAdaptor(t *testing.T, directTx, directRefundTx []byte, tweakedPriv keys.Private) ([]byte, []byte, keys.Public) {
	regularSig := getTxOutputSignature(t, directTx, directRefundTx, tweakedPriv)

	// Generate adaptor signature from the regular signature
	adaptorSignature, adaptorPrivateKey, err := common.GenerateAdaptorFromSignature(regularSig)
	require.NoError(t, err)
	return regularSig, adaptorSignature, adaptorPrivateKey.Public()
}

func getTxOutpoint(t *testing.T, txBytes []byte, vout uint32) (wire.OutPoint, []byte, int64) {
	var tx wire.MsgTx
	require.NoError(t, tx.Deserialize(bytes.NewReader(txBytes)))
	require.Less(t, int(vout), len(tx.TxOut))
	return wire.OutPoint{Hash: tx.TxHash(), Index: vout}, tx.TxOut[vout].PkScript, tx.TxOut[vout].Value
}

// makeP2TRSpendTxUnsigned creates a v2 transaction spending prevOut with a single
// output of sendValue/destScript, but with no witness (unsigned).
func makeP2TRSpendTxUnsigned(prevOut wire.OutPoint, sendValue int64, destScript []byte) ([]byte, error) {
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(wire.NewTxIn(&prevOut, nil, nil))
	tx.AddTxOut(wire.NewTxOut(sendValue, destScript))
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// corruptWitness flips the first byte of the first witness element on input 0,
// producing a transaction that is structurally valid but carries an invalid signature.
func corruptWitness(t *testing.T, rawTx []byte) []byte {
	t.Helper()
	var tx wire.MsgTx
	require.NoError(t, tx.Deserialize(bytes.NewReader(rawTx)))
	require.NotEmpty(t, tx.TxIn)
	require.NotEmpty(t, tx.TxIn[0].Witness)
	tx.TxIn[0].Witness[0][0] ^= 0xFF
	var buf bytes.Buffer
	require.NoError(t, tx.Serialize(&buf))
	return buf.Bytes()
}

func TestCompareTxs(t *testing.T) {
	t.Parallel()

	key := keys.GeneratePrivateKey()
	_, outpoint, pkScript, prevAmt, tweakedPriv, err := makeP2TRFundingTx(1000, key)
	require.NoError(t, err)

	signedTx, err := makeP2TRSpendTx(outpoint, pkScript, prevAmt, tweakedPriv, 900, pkScript)
	require.NoError(t, err)

	unsignedTx, err := makeP2TRSpendTxUnsigned(outpoint, 900, pkScript)
	require.NoError(t, err)

	differentOutpointTx, err := makeP2TRSpendTxUnsigned(wire.OutPoint{Hash: chainhash.Hash{2}, Index: 0}, 900, pkScript)
	require.NoError(t, err)

	differentValueTx, err := makeP2TRSpendTxUnsigned(outpoint, 800, pkScript)
	require.NoError(t, err)

	tests := []struct {
		name    string
		tx1     []byte
		tx2     []byte
		want    bool
		wantErr bool
	}{
		{
			name: "both nil",
			tx1:  nil, tx2: nil,
			want: true,
		},
		{
			name: "identical unsigned txs",
			tx1:  unsignedTx, tx2: unsignedTx,
			want: true,
		},
		{
			name: "identical signed txs",
			tx1:  signedTx, tx2: signedTx,
			want: true,
		},
		{
			name: "different outpoints",
			tx1:  unsignedTx, tx2: differentOutpointTx,
			want: false,
		},
		{
			name: "different output values",
			tx1:  unsignedTx, tx2: differentValueTx,
			want: false,
		},
		{
			name: "same structure, different witnesses",
			tx1:  unsignedTx, tx2: signedTx,
			want: false,
		},
		{
			name: "one nil",
			tx1:  nil, tx2: signedTx,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := compareTxs(tt.tx1, tt.tx2)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCompareAndVerifyTxs(t *testing.T) {
	t.Parallel()

	key := keys.GeneratePrivateKey()
	_, outpoint, pkScript, prevAmt, tweakedPriv, err := makeP2TRFundingTx(1000, key)
	require.NoError(t, err)

	prevOut := &wire.TxOut{Value: prevAmt, PkScript: pkScript}

	signedTx, err := makeP2TRSpendTx(outpoint, pkScript, prevAmt, tweakedPriv, 900, pkScript)
	require.NoError(t, err)

	unsignedTx, err := makeP2TRSpendTxUnsigned(outpoint, 900, pkScript)
	require.NoError(t, err)

	corruptedTx := corruptWitness(t, signedTx)

	tests := []struct {
		name    string
		tx1     []byte
		tx2     []byte
		prevOut *wire.TxOut
		want    bool
		wantErr bool
	}{
		{
			name: "both nil",
			tx1:  nil, tx2: nil,
			prevOut: nil,
			want:    true,
		},
		{
			name: "identical signed txs",
			tx1:  signedTx, tx2: signedTx,
			prevOut: prevOut,
			want:    true,
		},
		{
			name: "tx1 unsigned, tx2 has valid sig",
			tx1:  unsignedTx, tx2: signedTx,
			prevOut: prevOut,
			want:    true,
		},
		{
			name: "tx2 has corrupted signature",
			tx1:  signedTx, tx2: corruptedTx,
			prevOut: prevOut,
			wantErr: true,
		},
		{
			name: "tx2 has no witness",
			tx1:  signedTx, tx2: unsignedTx,
			prevOut: prevOut,
			wantErr: true,
		},
		{
			name: "structural mismatch - different outpoint",
			tx1:  signedTx,
			tx2: func() []byte {
				tx, _ := makeP2TRSpendTxUnsigned(wire.OutPoint{Hash: chainhash.Hash{2}, Index: 0}, 900, pkScript)
				return tx
			}(),
			prevOut: prevOut,
			want:    false,
		},
		{
			name:    "structural mismatch - different value",
			tx1:     signedTx,
			tx2:     func() []byte { tx, _ := makeP2TRSpendTxUnsigned(outpoint, 800, pkScript); return tx }(),
			prevOut: prevOut,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := compareAndVerifyTxs(tt.tx1, tt.tx2, tt.prevOut)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestUpdateTransferLeavesSignatures(t *testing.T) {
	t.Parallel()

	t.Run("clears optional fields when signatures not provided", func(t *testing.T) {
		ctx, dbCtx := db.NewTestSQLiteContext(t)
		rng := rand.NewChaCha8([32]byte{})

		config := &so.Config{
			BitcoindConfigs: map[string]so.BitcoindConfig{
				"regtest": {
					DepositConfirmationThreshold: 1,
				},
			},
			FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
		}

		key := keys.GeneratePrivateKey()
		rawTx, outpoint, pkScript, prevAmt, tweakedPriv, err := makeP2TRFundingTx(1000, key)
		require.NoError(t, err)

		destScript := pkScript
		rawRefundTx, err := makeP2TRSpendTx(outpoint, pkScript, prevAmt, tweakedPriv, 900, destScript)
		require.NoError(t, err)

		dest1 := pkScript
		directTx, err := makeP2TRSpendTx(outpoint, pkScript, prevAmt, tweakedPriv, 880, dest1)
		require.NoError(t, err)

		out1, pk1, amt1 := getTxOutpoint(t, directTx, 0)
		dest2 := pkScript
		directRefundTx, err := makeP2TRSpendTx(out1, pk1, amt1, tweakedPriv, 860, dest2)
		require.NoError(t, err)

		// Create test signing keyshare
		secret := keys.MustGeneratePrivateKeyFromRand(rng)
		pubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		signingKeyshare, err := dbCtx.Client.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare(secret).
			SetPublicShares(map[string]keys.Public{"test": secret.Public()}).
			SetPublicKey(pubKey).
			SetMinSigners(2).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		baseTxid := st.NewRandomTxIDForTesting(t)
		tree, err := dbCtx.Client.Tree.Create().
			SetStatus(st.TreeStatusAvailable).
			SetNetwork(btcnetwork.Regtest).
			SetOwnerIdentityPubkey(ownerIdentityPubKey).
			SetBaseTxid(baseTxid).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)

		verifyingPubKey := key.Public()
		leaf, err := dbCtx.Client.TreeNode.Create().
			SetStatus(st.TreeNodeStatusAvailable).
			SetTree(tree).
			SetNetwork(tree.Network).
			SetSigningKeyshare(signingKeyshare).
			SetValue(1000).
			SetVerifyingPubkey(verifyingPubKey).
			SetOwnerIdentityPubkey(key.Public()).
			SetOwnerSigningPubkey(key.Public()).
			SetRawTx(rawTx).
			SetRawRefundTx(rawRefundTx).
			SetDirectTx(directTx).
			SetDirectRefundTx(directRefundTx).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)

		receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		transfer, err := dbCtx.Client.Transfer.Create().
			SetNetwork(tree.Network).
			SetStatus(st.TransferStatusReceiverRefundSigned).
			SetType(st.TransferTypeTransfer).
			SetSenderIdentityPubkey(key.Public()).
			SetReceiverIdentityPubkey(receiverIdentityPubKey).
			SetTotalValue(900).
			SetExpiryTime(time.Now().Add(24 * time.Hour)).
			SetCompletionTime(time.Now()).
			Save(ctx)
		require.NoError(t, err)

		intermediateRefundTx := createTestTxBytes(t, 2001)
		intermediateDirectRefundTx := createTestTxBytes(t, 3001)
		intermediateDirectFromCpfpRefundTx := createTestTxBytes(t, 4001)

		transferLeaf, err := dbCtx.Client.TransferLeaf.Create().
			SetLeaf(leaf).
			SetTransfer(transfer).
			SetPreviousRefundTx(createTestTxBytes(t, 2000)).
			SetIntermediateRefundTx(intermediateRefundTx).
			SetIntermediateDirectRefundTx(intermediateDirectRefundTx).
			SetIntermediateDirectFromCpfpRefundTx(intermediateDirectFromCpfpRefundTx).
			Save(ctx)
		require.NoError(t, err)

		handler := NewTransferHandler(config)

		// Sign the transactions
		cpfpSignature := getTxOutputSignature(t, rawTx, intermediateRefundTx, tweakedPriv)

		// Call with only cpfp signature (no direct signatures)
		cpfpSignatureMap := map[string][]byte{leaf.ID.String(): cpfpSignature}
		emptyDirectSignatureMap := map[string][]byte{}
		emptyDirectFromCpfpSignatureMap := map[string][]byte{}

		err = handler.UpdateTransferLeavesSignatures(ctx, transfer, cpfpSignatureMap, emptyDirectSignatureMap, emptyDirectFromCpfpSignatureMap)
		require.NoError(t, err)

		// Commit the transaction to persist changes
		entTx, err := ent.GetTxFromContext(ctx)
		require.NoError(t, err)
		err = entTx.Commit()
		require.NoError(t, err)

		// Verify optional fields were cleared (set to NULL)
		updated, err := dbCtx.Client.TransferLeaf.Get(t.Context(), transferLeaf.ID)
		require.NoError(t, err)
		assert.NotEqual(t, intermediateRefundTx, updated.IntermediateRefundTx, "cpfp refund tx should be updated")
		assert.Nil(t, updated.IntermediateDirectRefundTx, "direct refund tx should be cleared when no signature provided")
		assert.Nil(t, updated.IntermediateDirectFromCpfpRefundTx, "direct from cpfp refund tx should be cleared when no signature provided")
	})
}
