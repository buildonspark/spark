package handler

import (
	"bytes"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/google/uuid"
	sparkProto "github.com/lightsparkdev/spark/proto/spark"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
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

func makeP2TRFundingTx(value int64, internalPriv *btcec.PrivateKey) (txBytes []byte, outpoint wire.OutPoint, pkScript []byte, prevAmt int64, tweakedPriv *btcec.PrivateKey, err error) {
	tweakedPriv = txscript.TweakTaprootPrivKey(*internalPriv, nil)
	xonly := schnorr.SerializePubKey(tweakedPriv.PubKey())
	pkScript = append([]byte{txscript.OP_1, 32}, xonly...)
	tx := wire.NewMsgTx(2)
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

func makeP2TRSpendTx(prevOut wire.OutPoint, prevPkScript []byte, prevAmt int64, tweakedPriv *btcec.PrivateKey, sendValue int64, destScript []byte) ([]byte, error) {
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(wire.NewTxIn(&prevOut, nil, nil))
	tx.AddTxOut(wire.NewTxOut(sendValue, destScript))
	prevFetcher := txscript.NewCannedPrevOutputFetcher(prevPkScript, prevAmt)
	hashes := txscript.NewTxSigHashes(tx, prevFetcher)
	sighash, err := txscript.CalcTaprootSignatureHash(hashes, txscript.SigHashDefault, tx, 0, prevFetcher)
	if err != nil {
		return nil, err
	}
	sig, err := schnorr.Sign(tweakedPriv, sighash)
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

		newRawRefundTx := createTestTxBytes(t, 3001)

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
			SetSecretShare(keysharePrivKey.Serialize()).
			SetPublicShares(map[string]keys.Public{"test": publicSharePrivKey.Public()}).
			SetPublicKey(keysharePrivKey.Public()).
			SetMinSigners(2).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		// Create test tree
		tree, err := dbCtx.Client.Tree.Create().
			SetStatus(st.TreeStatusAvailable).
			SetNetwork(st.NetworkRegtest).
			SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public().Serialize()).
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
			SetVerifyingPubkey(verifyingPrivKey.Public().Serialize()).
			SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public().Serialize()).
			SetOwnerSigningPubkey(ownerSigningPrivKey.Public().Serialize()).
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
			SetSenderIdentityPubkey(senderIdentityPrivKey.Public().Serialize()).
			SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public().Serialize()).
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
			Value:                  1000,                                  // Must match the original value since it's immutable
			VerifyingPubkey:        verifyingPrivKey.Public().Serialize(), // Must match the original value since it's immutable
			OwnerIdentityPubkey:    updatedOwnerIdentityPrivKey.Public().Serialize(),
			OwnerSigningPubkey:     updatedOwnerSigningPrivKey.Public().Serialize(),
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

func TestApplySignatures(t *testing.T) {
	t.Parallel()
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

	key, err := keys.GeneratePrivateKey()
	require.NoError(t, err)

	// Create test tx bytes
	btcecPriv := key.ToBTCEC()
	rawTx, outpoint, pkScript, prevAmt, tweakedPriv, err := makeP2TRFundingTx(1000, btcecPriv)
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
		SetSecretShare(secret.Serialize()).
		SetPublicShares(map[string]keys.Public{"test": secret.Public()}).
		SetPublicKey(pubKey).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	tree, err := dbCtx.Client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(st.NetworkRegtest).
		SetOwnerIdentityPubkey([]byte("test_owner_identity")).
		SetBaseTxid([]byte("test_base_txid")).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	leaf, err := dbCtx.Client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
		SetSigningKeyshare(signingKeyshare).
		SetValue(1000).
		SetVerifyingPubkey(key.Public().Serialize()).
		SetOwnerIdentityPubkey(key.Public().Serialize()).
		SetOwnerSigningPubkey(key.Public().Serialize()).
		SetRawTx(rawTx).
		SetRawRefundTx(rawRefundTx).
		SetDirectTx(directTx).
		SetDirectRefundTx(directRefundTx).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	transfer, err := dbCtx.Client.Transfer.Create().
		SetStatus(st.TransferStatusReceiverRefundSigned).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(key.Public().Serialize()).
		SetReceiverIdentityPubkey([]byte("test_receiver_identity")).
		SetTotalValue(900).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetCompletionTime(time.Now()).
		Save(ctx)

	require.NoError(t, err)

	_, err = dbCtx.Client.TransferLeaf.Create().
		SetLeaf(leaf).
		SetTransfer(transfer).
		SetPreviousRefundTx([]byte("test_previous_refund_tx")).
		SetIntermediateRefundTx([]byte("test_intermediate_refund_tx")).
		Save(ctx)
	require.NoError(t, err)

	handler := NewInternalTransferHandler(config)

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

	var tests = []struct {
		name                   string
		leafId                 string
		rawRefundTx            []byte
		directRefundTx         []byte
		directRefundSignatures map[string][]byte
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
			expectedError: "",
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
			expectedError: "no leaf refund found",
		},
		{
			name:           "broken leaf id",
			leafId:         testLeafId,
			rawRefundTx:    rawRefundTx,
			directRefundTx: directRefundTx,
			directRefundSignatures: map[string][]byte{
				testLeafId: signature,
			},
			expectedError: "unable to parse leaf id",
		},
		{
			name:           "unable to get leaf",
			leafId:         unknownLeafId,
			rawRefundTx:    rawRefundTx,
			directRefundTx: directRefundTx,
			directRefundSignatures: map[string][]byte{
				unknownLeafId: signature,
			},
			expectedError: "unable to get leaf",
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

			_, map2, _ := handler.loadLeafRefundMaps(req)
			_, err = applySignatures(ctx, map2, req.DirectRefundSignatures, true)

			if tt.expectedError != "" {
				require.ErrorContains(t, err, tt.expectedError)
				return
			}
			require.NoError(t, err)
		})
	}

}

func getTxOutputSignature(t *testing.T, directTx, directRefundTx []byte, tweakedPriv *btcec.PrivateKey) []byte {
	var dr wire.MsgTx
	require.NoError(t, dr.Deserialize(bytes.NewReader(directRefundTx)))

	prevOut1, prevPk1, prevAmt1 := getTxOutpoint(t, directTx, 0)
	_ = prevOut1

	prevFetcher := txscript.NewCannedPrevOutputFetcher(prevPk1, prevAmt1)
	hashes := txscript.NewTxSigHashes(&dr, prevFetcher)

	sigHash, err := txscript.CalcTaprootSignatureHash(
		hashes,
		txscript.SigHashDefault,
		&dr, 0, prevFetcher,
	)
	require.NoError(t, err)

	directRefundSig, err := schnorr.Sign(tweakedPriv, sigHash)
	require.NoError(t, err)

	return directRefundSig.Serialize()
}

func getTxOutpoint(t *testing.T, txBytes []byte, vout uint32) (wire.OutPoint, []byte, int64) {
	var tx wire.MsgTx
	require.NoError(t, tx.Deserialize(bytes.NewReader(txBytes)))
	require.Less(t, int(vout), len(tx.TxOut))
	return wire.OutPoint{Hash: tx.TxHash(), Index: vout}, tx.TxOut[vout].PkScript, tx.TxOut[vout].Value
}
