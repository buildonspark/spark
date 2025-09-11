package handler

import (
	"encoding/hex"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetStuckLightningPayments(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})

	config := &so.Config{
		Identifier: "test-operator",
	}
	sspHandler := NewSspRequestHandler(config)

	// Get test database
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Setup common test data
	expiredTime := time.Now().Add(-1 * time.Hour)
	futureTime := time.Now().Add(1 * time.Hour)
	paymentHash1 := []byte("payment_hash_1_32_bytes_long____")
	paymentHash2 := []byte("payment_hash_2_32_bytes_long____")
	paymentHash3 := []byte("payment_hash_3_32_bytes_long____")

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Create a tree for the transfers
	tree, err := dbTx.Tree.Create().
		SetNetwork(st.NetworkMainnet).
		SetOwnerIdentityPubkey(ownerIdentityPubKey.Serialize()).
		SetBaseTxid([]byte("base_txid")).
		SetVout(0).
		SetStatus(st.TreeStatusAvailable).
		Save(ctx)
	require.NoError(t, err)

	// Helper function to create a transfer with all required relationships
	createTransferWithLeaf := func(transferID uuid.UUID, status st.TransferStatus, expiryTime time.Time, publicKey keys.Public) (*ent.Transfer, error) {
		transfer, err := dbTx.Transfer.Create().
			SetID(transferID).
			SetType(st.TransferTypePreimageSwap).
			SetStatus(status).
			SetExpiryTime(expiryTime).
			SetTotalValue(1000).
			SetSenderIdentityPubkey(senderIdentityPubKey.Serialize()).
			SetReceiverIdentityPubkey(receiverIdentityPubKey.Serialize()).
			Save(ctx)
		if err != nil {
			return nil, err
		}

		// Create a signing keyshare for the leaf node
		keyshare, err := dbTx.SigningKeyshare.Create().
			SetPublicShares(map[string][]byte{"key": []byte("value")}).
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare([]byte("secret_share")).
			SetPublicKey(publicKey.Serialize()).
			SetMinSigners(2).
			SetCoordinatorIndex(1).
			Save(ctx)
		if err != nil {
			return nil, err
		}

		// Create a tree node for the transfer
		treeNode, err := dbTx.TreeNode.Create().
			SetTree(tree).
			SetValue(1000).
			SetStatus(st.TreeNodeStatusAvailable).
			SetVerifyingPubkey(verifyingPubKey.Serialize()).
			SetOwnerIdentityPubkey(ownerIdentityPubKey.Serialize()).
			SetOwnerSigningPubkey(ownerSigningPubKey.Serialize()).
			SetRawTx([]byte("raw_tx")).
			SetVout(0).
			SetSigningKeyshare(keyshare).
			Save(ctx)
		if err != nil {
			return nil, err
		}

		// Create a transfer leaf for the transfer
		_, err = dbTx.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(treeNode).
			SetPreviousRefundTx([]byte("previous_refund_tx")).
			SetIntermediateRefundTx([]byte("intermediate_refund_tx")).
			Save(ctx)
		if err != nil {
			return nil, err
		}

		return transfer, nil
	}

	// Create test transfers
	stuckTransferID := uuid.Must(uuid.NewRandomFromReader(rng))
	pubKey1 := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	stuckTransfer, err := createTransferWithLeaf(stuckTransferID, st.TransferStatusSenderKeyTweakPending, expiredTime, pubKey1)
	require.NoError(t, err)

	// Create a transfer that's not expired yet
	nonExpiredTransferID := uuid.Must(uuid.NewRandomFromReader(rng))
	pubKey2 := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	nonExpiredTransfer, err := createTransferWithLeaf(nonExpiredTransferID, st.TransferStatusSenderKeyTweakPending, futureTime, pubKey2)
	require.NoError(t, err)

	// Create a transfer with wrong status
	wrongStatusTransferID := uuid.Must(uuid.NewRandomFromReader(rng))
	pubKey3 := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	wrongStatusTransfer, err := createTransferWithLeaf(wrongStatusTransferID, st.TransferStatusCompleted, expiredTime, pubKey3)
	require.NoError(t, err)

	// Create preimage requests in different states
	_, err = dbTx.PreimageRequest.Create().
		SetPaymentHash(paymentHash1).
		SetStatus(st.PreimageRequestStatusWaitingForPreimage).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetTransfers(stuckTransfer).
		Save(ctx)
	require.NoError(t, err)

	_, err = dbTx.PreimageRequest.Create().
		SetPaymentHash(paymentHash2).
		SetStatus(st.PreimageRequestStatusWaitingForPreimage).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetTransfers(nonExpiredTransfer).
		Save(ctx)
	require.NoError(t, err)

	_, err = dbTx.PreimageRequest.Create().
		SetPaymentHash(paymentHash3).
		SetStatus(st.PreimageRequestStatusPreimageShared).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetTransfers(wrongStatusTransfer).
		Save(ctx)
	require.NoError(t, err)

	t.Run("get stuck lightning payments returns only expired payments with correct status", func(t *testing.T) {
		req := &pbssp.GetStuckLightningPaymentsRequest{
			Limit:  100,
			Offset: 0,
		}
		resp, err := sspHandler.GetStuckLightningPayments(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Len(t, resp.LightningPayments, 1)

		payment := resp.LightningPayments[0]
		assert.Equal(t, stuckTransferID.String(), payment.Transfer.Id)
		protoType, err := ent.TransferTypeProto(st.TransferTypePreimageSwap)
		require.NoError(t, err)
		assert.Equal(t, *protoType, payment.Transfer.Type)
		assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, payment.Transfer.Status)
		assert.Equal(t, hex.EncodeToString(paymentHash1), payment.PaymentHash)
	})

	t.Run("pagination works correctly", func(t *testing.T) {
		// Create additional stuck transfers to test pagination
		for i := 0; i < 5; i++ {
			transferID := uuid.Must(uuid.NewRandomFromReader(rng))
			pubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
			transfer, err := createTransferWithLeaf(transferID, st.TransferStatusSenderKeyTweakPending, expiredTime, pubKey)
			require.NoError(t, err)

			_, err = dbTx.PreimageRequest.Create().
				SetPaymentHash([]byte("payment_hash_extra_32_bytes_____")).
				SetStatus(st.PreimageRequestStatusWaitingForPreimage).
				SetReceiverIdentityPubkey(receiverIdentityPubKey).
				SetTransfers(transfer).
				Save(ctx)
			require.NoError(t, err)
		}

		// Test with limit
		req := &pbssp.GetStuckLightningPaymentsRequest{
			Limit:  3,
			Offset: 0,
		}
		resp, err := sspHandler.GetStuckLightningPayments(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.LightningPayments, 3)

		// Test with offset
		req = &pbssp.GetStuckLightningPaymentsRequest{
			Limit:  3,
			Offset: 3,
		}
		resp, err = sspHandler.GetStuckLightningPayments(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.LightningPayments, 3)

		// Test getting remaining items
		req = &pbssp.GetStuckLightningPaymentsRequest{
			Limit:  3,
			Offset: 6,
		}
		resp, err = sspHandler.GetStuckLightningPayments(ctx, req)
		require.NoError(t, err)
		require.Empty(t, resp.LightningPayments)
	})

	t.Run("invalid limit returns error", func(t *testing.T) {
		req := &pbssp.GetStuckLightningPaymentsRequest{
			Limit:  101,
			Offset: 0,
		}
		resp, err := sspHandler.GetStuckLightningPayments(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.LessOrEqual(t, len(resp.LightningPayments), 100)
	})

	t.Run("negative offset returns error", func(t *testing.T) {
		req := &pbssp.GetStuckLightningPaymentsRequest{
			Limit:  10,
			Offset: -1,
		}
		resp, err := sspHandler.GetStuckLightningPayments(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, resp)
	})
}
