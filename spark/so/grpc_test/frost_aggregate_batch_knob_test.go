package grpctest

import (
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTransferAndClaim_SerialFrostAggregation pins
// KnobFrostAggregateBatchEnabled to 0 and drives a transfer + claim
// end-to-end. The tilt environment enables the batch RPC path everywhere, so
// without this pin the knob-off serial dispatch in
// frostAggregationBatch.aggregate — the production default until the batch
// rollout completes — would have no integration coverage at all.
func TestTransferAndClaim_SerialFrostAggregation(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot pin KnobFrostAggregateBatchEnabled=0: %v", err)
	}
	require.NoError(t, kc.SetKnob(t, knobs.KnobFrostAggregateBatchEnabled, 0))

	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	leavesToTransfer := []wallet.LeafKeyTweak{{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}
	leafReceiverMap := map[string]keys.Public{
		rootNode.GetId(): receiverPrivKey.Public(),
	}

	senderTransfer, err := wallet.SendTransferV3WithKeyTweaks(
		t.Context(), senderConfig, leavesToTransfer, leafReceiverMap,
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to send V3 transfer with serial FROST aggregation")
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, senderTransfer.GetStatus())

	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)

	pending, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err)
	require.Len(t, pending.GetTransfers(), 1)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimLeaves := []wallet.LeafKeyTweak{{
		Leaf:              pending.GetTransfers()[0].GetLeaves()[0].GetLeaf(),
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}}
	claimed, err := wallet.ClaimTransferV2(receiverCtx, pending.GetTransfers()[0], receiverConfig, claimLeaves)
	require.NoError(t, err, "receiver claim should succeed with serial FROST aggregation")
	assert.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_COMPLETED, claimed.GetStatus())
}
