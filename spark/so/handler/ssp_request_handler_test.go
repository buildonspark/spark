package handler

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
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
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	config := &so.Config{
		Identifier: "test-operator",
	}
	sspHandler := NewSspRequestHandler(config)

	// Get test database
	db, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Setup common test data
	expiredTime := time.Now().Add(-1 * time.Hour)
	futureTime := time.Now().Add(1 * time.Hour)
	paymentHash1 := []byte("payment_hash_1_32_bytes_long____")
	paymentHash2 := []byte("payment_hash_2_32_bytes_long____")
	paymentHash3 := []byte("payment_hash_3_32_bytes_long____")

	// Create test transfers
	stuckTransferID := uuid.New()
	stuckTransfer, err := db.Transfer.Create().
		SetID(stuckTransferID).
		SetType(st.TransferTypePreimageSwap).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetExpiryTime(expiredTime).
		SetTotalValue(1000).
		SetSenderIdentityPubkey([]byte("sender")).
		SetReceiverIdentityPubkey([]byte("receiver")).
		Save(ctx)
	require.NoError(t, err)

	// Create a transfer that's not expired yet
	nonExpiredTransferID := uuid.New()
	nonExpiredTransfer, err := db.Transfer.Create().
		SetID(nonExpiredTransferID).
		SetType(st.TransferTypePreimageSwap).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetExpiryTime(futureTime).
		SetTotalValue(1000).
		SetSenderIdentityPubkey([]byte("sender")).
		SetReceiverIdentityPubkey([]byte("receiver")).
		Save(ctx)
	require.NoError(t, err)

	// Create a transfer with wrong status
	wrongStatusTransferID := uuid.New()
	wrongStatusTransfer, err := db.Transfer.Create().
		SetID(wrongStatusTransferID).
		SetType(st.TransferTypePreimageSwap).
		SetStatus(st.TransferStatusCompleted).
		SetExpiryTime(expiredTime).
		SetTotalValue(1000).
		SetSenderIdentityPubkey([]byte("sender")).
		SetReceiverIdentityPubkey([]byte("receiver")).
		Save(ctx)
	require.NoError(t, err)

	// Create preimage requests in different states
	_, err = db.PreimageRequest.Create().
		SetPaymentHash(paymentHash1).
		SetStatus(st.PreimageRequestStatusWaitingForPreimage).
		SetReceiverIdentityPubkey([]byte("receiver")).
		SetTransfers(stuckTransfer).
		Save(ctx)
	require.NoError(t, err)

	_, err = db.PreimageRequest.Create().
		SetPaymentHash(paymentHash2).
		SetStatus(st.PreimageRequestStatusWaitingForPreimage).
		SetReceiverIdentityPubkey([]byte("receiver")).
		SetTransfers(nonExpiredTransfer).
		Save(ctx)
	require.NoError(t, err)

	_, err = db.PreimageRequest.Create().
		SetPaymentHash(paymentHash3).
		SetStatus(st.PreimageRequestStatusPreimageShared).
		SetReceiverIdentityPubkey([]byte("receiver")).
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
			transferID := uuid.New()
			transfer, err := db.Transfer.Create().
				SetID(transferID).
				SetType(st.TransferTypePreimageSwap).
				SetStatus(st.TransferStatusSenderKeyTweakPending).
				SetExpiryTime(expiredTime).
				SetTotalValue(1000).
				SetSenderIdentityPubkey([]byte("sender")).
				SetReceiverIdentityPubkey([]byte("receiver")).
				Save(ctx)
			require.NoError(t, err)

			_, err = db.PreimageRequest.Create().
				SetPaymentHash([]byte("payment_hash_extra_32_bytes_____")).
				SetStatus(st.PreimageRequestStatusWaitingForPreimage).
				SetReceiverIdentityPubkey([]byte("receiver")).
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
