//go:build lightspark

package handler

import (
	"crypto/sha256"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestQueryPendingPreimageSwapTransfer(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{1})
	senderPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	sspPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	otherReceiverPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	sessionCtx := authn.InjectSessionForTests(ctx, sspPub.ToHex(), time.Now().Add(time.Hour).Unix())
	sspHandler := NewSspRequestHandler(&so.Config{Identifier: "test-operator"})

	paymentHashFor := func(label string) []byte {
		hash := sha256.Sum256([]byte(label))
		return hash[:]
	}

	createTransfer := func(network btcnetwork.Network, receivers ...keys.Public) *ent.Transfer {
		transfer, err := dbTx.Transfer.Create().
			SetNetwork(network).
			SetType(st.TransferTypePreimageSwap).
			SetStatus(st.TransferStatusSenderKeyTweakPending).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetTotalValue(1000).
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receivers[0]).
			Save(ctx)
		require.NoError(t, err)
		_, err = dbTx.TransferSender.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(senderPub).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)
		for _, receiver := range receivers {
			_, err = dbTx.TransferReceiver.Create().
				SetTransferID(transfer.ID).
				SetIdentityPubkey(receiver).
				SetStatus(st.TransferReceiverStatusInitiated).
				SetTransferType(transfer.Type).
				Save(ctx)
			require.NoError(t, err)
		}
		return transfer
	}

	createPreimageRequest := func(paymentHash []byte, status st.PreimageRequestStatus, receiver keys.Public, transfer *ent.Transfer) *ent.PreimageRequest {
		preimageRequest, err := dbTx.PreimageRequest.Create().
			SetPaymentHash(paymentHash).
			SetStatus(status).
			SetReceiverIdentityPubkey(receiver).
			SetTransfers(transfer).
			Save(ctx)
		require.NoError(t, err)
		return preimageRequest
	}

	t.Run("returns pending transfer with SIMO participant edges", func(t *testing.T) {
		paymentHash := paymentHashFor("payment_hash_simo")
		transfer := createTransfer(btcnetwork.Regtest, sspPub, otherReceiverPub)
		createPreimageRequest(paymentHash, st.PreimageRequestStatusWaitingForPreimage, sspPub, transfer)

		resp, err := sspHandler.QueryPendingPreimageSwapTransfer(sessionCtx, &pbssp.QueryPendingPreimageSwapTransferRequest{
			PaymentHash: paymentHash,
			Network:     pb.Network_REGTEST,
		})
		require.NoError(t, err)
		require.NotNil(t, resp.GetTransfer())
		assert.Equal(t, transfer.ID.String(), resp.GetTransfer().GetId())

		require.Len(t, resp.GetTransfer().GetReceivers(), 2)
		receiverKeys := make([][]byte, 0, 2)
		for _, receiver := range resp.GetTransfer().GetReceivers() {
			receiverKeys = append(receiverKeys, receiver.GetIdentityPublicKey())
		}
		assert.Contains(t, receiverKeys, sspPub.Serialize())
		assert.Contains(t, receiverKeys, otherReceiverPub.Serialize())

		require.Len(t, resp.GetTransfer().GetSenders(), 1)
		assert.Equal(t, senderPub.Serialize(), resp.GetTransfer().GetSenders()[0].GetIdentityPublicKey())
	})

	t.Run("ignores coexisting RETURNED request for the same payment hash", func(t *testing.T) {
		paymentHash := paymentHashFor("payment_hash_returned_coexists")
		returnedTransfer := createTransfer(btcnetwork.Regtest, sspPub)
		createPreimageRequest(paymentHash, st.PreimageRequestStatusReturned, sspPub, returnedTransfer)
		pendingTransfer := createTransfer(btcnetwork.Regtest, sspPub)
		createPreimageRequest(paymentHash, st.PreimageRequestStatusWaitingForPreimage, sspPub, pendingTransfer)

		resp, err := sspHandler.QueryPendingPreimageSwapTransfer(sessionCtx, &pbssp.QueryPendingPreimageSwapTransferRequest{
			PaymentHash: paymentHash,
			Network:     pb.Network_REGTEST,
		})
		require.NoError(t, err)
		assert.Equal(t, pendingTransfer.ID.String(), resp.GetTransfer().GetId())
	})

	t.Run("excludes requests that are not waiting for preimage", func(t *testing.T) {
		paymentHash := paymentHashFor("payment_hash_shared")
		transfer := createTransfer(btcnetwork.Regtest, sspPub)
		createPreimageRequest(paymentHash, st.PreimageRequestStatusPreimageShared, sspPub, transfer)

		_, err := sspHandler.QueryPendingPreimageSwapTransfer(sessionCtx, &pbssp.QueryPendingPreimageSwapTransferRequest{
			PaymentHash: paymentHash,
			Network:     pb.Network_REGTEST,
		})
		require.Error(t, err)
		require.ErrorContains(t, err, "no pending preimage request")
	})

	t.Run("scopes lookup to the session identity", func(t *testing.T) {
		paymentHash := paymentHashFor("payment_hash_other_receiver")
		transfer := createTransfer(btcnetwork.Regtest, otherReceiverPub)
		createPreimageRequest(paymentHash, st.PreimageRequestStatusWaitingForPreimage, otherReceiverPub, transfer)

		_, err := sspHandler.QueryPendingPreimageSwapTransfer(sessionCtx, &pbssp.QueryPendingPreimageSwapTransferRequest{
			PaymentHash: paymentHash,
			Network:     pb.Network_REGTEST,
		})
		require.Error(t, err)
		require.ErrorContains(t, err, "no pending preimage request")
	})

	t.Run("returns NotFound for a transfer-less preimage request", func(t *testing.T) {
		paymentHash := paymentHashFor("payment_hash_transferless")
		_, err := dbTx.PreimageRequest.Create().
			SetPaymentHash(paymentHash).
			SetStatus(st.PreimageRequestStatusWaitingForPreimage).
			SetReceiverIdentityPubkey(sspPub).
			Save(ctx)
		require.NoError(t, err)

		_, err = sspHandler.QueryPendingPreimageSwapTransfer(sessionCtx, &pbssp.QueryPendingPreimageSwapTransferRequest{
			PaymentHash: paymentHash,
			Network:     pb.Network_REGTEST,
		})
		require.Error(t, err)
		assert.Equal(t, codes.NotFound, status.Code(err))
		require.ErrorContains(t, err, "no transfer found for pending preimage request")
	})

	t.Run("rejects a transfer on a different network", func(t *testing.T) {
		paymentHash := paymentHashFor("payment_hash_wrong_network")
		transfer := createTransfer(btcnetwork.Regtest, sspPub)
		createPreimageRequest(paymentHash, st.PreimageRequestStatusWaitingForPreimage, sspPub, transfer)

		_, err := sspHandler.QueryPendingPreimageSwapTransfer(sessionCtx, &pbssp.QueryPendingPreimageSwapTransferRequest{
			PaymentHash: paymentHash,
			Network:     pb.Network_MAINNET,
		})
		require.Error(t, err)
		require.ErrorContains(t, err, "network")
	})

	t.Run("requires a session", func(t *testing.T) {
		paymentHash := paymentHashFor("payment_hash_no_session")
		transfer := createTransfer(btcnetwork.Regtest, sspPub)
		createPreimageRequest(paymentHash, st.PreimageRequestStatusWaitingForPreimage, sspPub, transfer)

		_, err := sspHandler.QueryPendingPreimageSwapTransfer(ctx, &pbssp.QueryPendingPreimageSwapTransferRequest{
			PaymentHash: paymentHash,
			Network:     pb.Network_REGTEST,
		})
		require.Error(t, err)
	})

	t.Run("rejects a malformed payment hash", func(t *testing.T) {
		for _, paymentHash := range [][]byte{nil, []byte("too_short")} {
			_, err := sspHandler.QueryPendingPreimageSwapTransfer(sessionCtx, &pbssp.QueryPendingPreimageSwapTransferRequest{
				PaymentHash: paymentHash,
				Network:     pb.Network_REGTEST,
			})
			require.Error(t, err)
			require.ErrorContains(t, err, "payment hash must be 32 bytes")
		}
	})

	t.Run("rejects an unspecified network", func(t *testing.T) {
		_, err := sspHandler.QueryPendingPreimageSwapTransfer(sessionCtx, &pbssp.QueryPendingPreimageSwapTransferRequest{
			PaymentHash: paymentHashFor("payment_hash_unspecified_network"),
			Network:     pb.Network_UNSPECIFIED,
		})
		require.Error(t, err)
		require.ErrorContains(t, err, "network")
	})
}
