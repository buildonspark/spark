//go:build lightspark

package grpctest

import (
	"crypto/rand"
	"crypto/sha256"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestInitiatePreimageSwap_RejectsReceiveWithoutTransferPackage is the
// end-to-end regression for the "preimage revealed without key-tweak commit"
// theft, closed structurally once preimage swaps resolve from transfer_request.
//
// The theft required a HODL receive swap that reached SENDER_INITIATED — a
// transfer created without staged sender key tweaks — so a revealed preimage
// could be read back by the SSP (via QueryPreimage), settling the inbound
// Lightning HTLC while the receiver held an unclaimable transfer.
//
// Under the transfer_request path (the ignore-legacy-transfer knob is on, and
// the legacy transfer field is removed entirely in SP-3285), a preimage swap
// that withholds the committed transfer package is rejected at input resolution
// before any transfer is created. There is no uncommitted transfer to strand
// and no preimage-bearing swap for the sender to read — the vector is gone.
func TestInitiatePreimageSwap_RejectsReceiveWithoutTransferPackage(t *testing.T) {
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)

	amountSats := uint64(100)
	_, paymentHash := randomPreimageHash(t)
	defer cleanUp(t, userConfig, paymentHash)

	sspLeafPrivKey := keys.GeneratePrivateKey()
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12_345)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	// Malicious SSP shape: a receive preimage swap that withholds the committed
	// key-tweak package. Input resolution rejects it before creating a transfer.
	_, err = wallet.SwapNodesForPreimageWithHTLC(
		t.Context(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		0,
		true,
		amountSats,
		wallet.WithoutTransferPackage(),
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "transfer_request.transfer_package is required")

	leaked := queryPreimageAsSender(t, sspConfig, paymentHash[:], userConfig.IdentityPublicKey())
	assert.Empty(t, leaked, "sender must not be able to read a preimage for a rejected swap")

	userToken, err := wallet.AuthenticateWithServer(t.Context(), userConfig)
	require.NoError(t, err)
	userCtx := wallet.ContextWithToken(t.Context(), userToken)
	pending, err := wallet.QueryPendingTransfers(userCtx, userConfig)
	require.NoError(t, err)
	// userConfig is a fresh, isolated wallet, so zero pending transfers is the
	// precise assertion that the rejected swap created nothing for the receiver.
	require.Empty(t, pending.GetTransfers(),
		"receiver must have no claimable transfer after an uncommitted swap was rejected")
}

func randomPreimageHash(t *testing.T) ([32]byte, [32]byte) {
	t.Helper()

	var preimage [32]byte
	_, err := rand.Read(preimage[:])
	require.NoError(t, err)

	return preimage, sha256.Sum256(preimage[:])
}

func queryPreimageAsSender(
	t *testing.T,
	senderConfig *wallet.TestWalletConfig,
	paymentHash []byte,
	receiverIdentityPubKey keys.Public,
) []byte {
	t.Helper()

	conn, err := senderConfig.NewCoordinatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), senderConfig, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	client := sparkpb.NewSparkServiceClient(conn)
	resp, err := client.QueryPreimage(ctx, &sparkpb.QueryPreimageRequest{
		PaymentHash:            paymentHash,
		ReceiverIdentityPubkey: receiverIdentityPubKey.Serialize(),
	})
	// A NotFound is itself proof the sender cannot read the preimage, so treat
	// it as an empty (passing) result rather than a test failure. Any other
	// error is an unexpected infrastructure problem and should surface.
	if status.Code(err) == codes.NotFound {
		return nil
	}
	require.NoError(t, err)

	return resp.GetPreimage()
}
