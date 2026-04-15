package grpctest

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/proto/spark"
)

// TestPreimageSwapSettleFailure_TransferSurvives verifies that a preimage swap
// succeeds even when non-coordinator SOs cannot process settlement, and that
// those SOs eventually converge once the endpoint is available again.
//
// The test disables the SettleSenderKeyTweak internal endpoint via knob,
// verifies the call still returns the preimage, then re-enables the endpoint
// and asserts all SOs reach SENDER_KEY_TWEAKED via gossip-based recovery.
func TestPreimageSwapSettleFailure_TransferSurvives(t *testing.T) {
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)

	amountSats := uint64(100)
	preimageHex := "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c"
	preimage, err := hex.DecodeString(preimageHex)
	require.NoError(t, err)
	paymentHash := sha256.Sum256(preimage)

	fakeInvoiceCreator := NewFakeLightningInvoiceCreator()
	defer cleanUp(t, userConfig, paymentHash)

	// Create lightning invoice to store preimage shares on SOs.
	invoice, err := wallet.CreateLightningInvoiceWithPreimage(
		t.Context(),
		userConfig,
		fakeInvoiceCreator,
		amountSats,
		"test",
		[32]byte(preimage),
	)
	require.NoError(t, err)
	require.NotNil(t, invoice)

	// Create a leaf for the SSP to swap.
	sspLeafPrivKey := keys.GeneratePrivateKey()
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12345)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	conn, err := sspConfig.NewCoordinatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), sspConfig, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	client := spark.NewSparkServiceClient(conn)

	transferID, err := uuid.NewV7()
	require.NoError(t, err)

	keyTweakInputMap, err := wallet.PrepareSendTransferKeyTweaks(
		sspConfig,
		transferID,
		userConfig.IdentityPublicKey(),
		leaves,
		map[string][]byte{},
	)
	require.NoError(t, err)

	transferPackage, err := wallet.PrepareTransferPackage(
		ctx,
		sspConfig,
		client,
		transferID,
		keyTweakInputMap,
		leaves,
		userConfig.IdentityPublicKey(),
		keys.Public{},
	)
	require.NoError(t, err)

	userSignedLeavesToSend, err := wallet.PrepareUserSignedLeafSigningJobs(
		ctx,
		sspConfig,
		client,
		leaves,
		userConfig.IdentityPublicKey(),
		keys.Public{},
	)
	require.NoError(t, err)

	// Disable SettleSenderKeyTweak on non-coordinator SOs to simulate the
	// production failure where one SO returns 500 during settlement.
	kc, err := sparktesting.NewKnobController(t)
	require.NoError(t, err)

	settleMethod := pbinternal.SparkInternalService_SettleSenderKeyTweak_FullMethodName
	err = kc.SetKnobWithTarget(t, knobs.KnobGrpcServerMethodEnabled, settleMethod, 0)
	require.NoError(t, err)

	// Call InitiatePreimageSwapV2 with RECEIVE reason and TransferRequest.
	// The handler will:
	//   1. Fan out the transfer to non-coordinator SOs (succeeds)
	//   2. Recover the preimage from shares (succeeds)
	//   3. Attempt settleSenderKeyTweaks (FAILS — endpoint disabled)
	// The transfer data is committed before settle, so it should survive
	// despite the error.
	response, err := client.InitiatePreimageSwapV2(ctx, &spark.InitiatePreimageSwapRequest{
		PaymentHash: paymentHash[:],
		Reason:      spark.InitiatePreimageSwapRequest_REASON_RECEIVE,
		InvoiceAmount: &spark.InvoiceAmount{
			InvoiceAmountProof: &spark.InvoiceAmountProof{
				Bolt11Invoice: invoice,
			},
			ValueSats: amountSats,
		},
		Transfer: &spark.StartUserSignedTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    sspConfig.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: userConfig.IdentityPublicKey().Serialize(),
			LeavesToSend:              userSignedLeavesToSend,
		},
		TransferRequest: &spark.StartTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    sspConfig.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: userConfig.IdentityPublicKey().Serialize(),
			TransferPackage:           transferPackage,
		},
		ReceiverIdentityPublicKey: userConfig.IdentityPublicKey().Serialize(),
		FeeSats:                   0,
	})

	// The call should succeed: the coordinator settles its own key tweaks
	// locally and returns the preimage. Other SOs receive settlement via
	// gossip, which is persisted even though delivery is blocked by the
	// disabled knob.
	require.NoError(t, err, "InitiatePreimageSwapV2 should succeed even when settle RPC is disabled")
	require.NotNil(t, response)
	assert.Equal(t, transferID.String(), response.Transfer.Id)

	network, err := sspConfig.Network.ToProtoNetwork()
	require.NoError(t, err)

	// Coordinator (SO0) should be settled; non-coordinators may still be
	// pending because gossip delivery is blocked by the disabled knob.
	assertTransferOnAllOperators(t, sspConfig, transferID.String(), network, []spark.TransferStatus{
		spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
		spark.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR,
	})

	// Re-enable SettleSenderKeyTweak so the gossip retry task can deliver
	// the settlement to non-coordinator SOs.
	err = kc.SetKnobWithTarget(t, knobs.KnobGrpcServerMethodEnabled, settleMethod, 100)
	require.NoError(t, err)

	// ProvidePreimage triggers gossip-based settlement on any SOs that
	// haven't settled yet.
	receiverTransfer, err := wallet.ProvidePreimage(t.Context(), userConfig, preimage)
	require.NoError(t, err, "ProvidePreimage should succeed after settle is re-enabled")
	assert.Equal(t,
		spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		receiverTransfer.Status,
		"transfer should reach SENDER_KEY_TWEAKED after recovery",
	)
	assert.Equal(t, transferID.String(), receiverTransfer.Id)

	// Verify all operators converged to the same settled state.
	assertTransferOnAllOperators(t, sspConfig, transferID.String(), network, []spark.TransferStatus{
		spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
	})
}

// assertTransferOnAllOperators queries each SO and asserts the transfer exists
// with one of the expected statuses.
func assertTransferOnAllOperators(
	t *testing.T,
	config *wallet.TestWalletConfig,
	transferID string,
	network spark.Network,
	expectedStatuses []spark.TransferStatus,
) {
	t.Helper()
	for identifier, operator := range config.SigningOperators {
		operatorConn, err := operator.NewOperatorGRPCConnection()
		require.NoError(t, err, "failed to connect to operator %s", identifier)

		operatorToken, err := wallet.AuthenticateWithConnection(t.Context(), config, operatorConn)
		require.NoError(t, err, "failed to authenticate with operator %s", identifier)
		operatorCtx := wallet.ContextWithToken(t.Context(), operatorToken)

		operatorClient := spark.NewSparkServiceClient(operatorConn)
		queryResp, err := operatorClient.QueryAllTransfers(operatorCtx, &spark.TransferFilter{
			Participant: &spark.TransferFilter_SenderOrReceiverIdentityPublicKey{
				SenderOrReceiverIdentityPublicKey: config.IdentityPublicKey().Serialize(),
			},
			Limit:   10,
			Offset:  0,
			Network: network,
		})
		operatorConn.Close()
		require.NoError(t, err, "failed to query transfers from operator %s", identifier)

		var found bool
		for _, transfer := range queryResp.Transfers {
			if transfer.Id == transferID {
				found = true
				assert.Contains(t, expectedStatuses, transfer.Status,
					"operator %s has unexpected transfer status", identifier,
				)
				break
			}
		}
		assert.True(t, found, "operator %s should have the transfer in its database", identifier)
	}
}
