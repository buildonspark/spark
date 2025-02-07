package grpctest

import (
	"bytes"
	"context"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so/chain"
	"github.com/lightsparkdev/spark-go/so/handler"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
	"github.com/stretchr/testify/assert"
)

func TestCoopExit(t *testing.T) {
	client, err := chain.NewRegtestClient()
	assert.NoError(t, err)

	sspOnChainKey, fundingTxOut, fundingOutPoint := testutil.FundFaucet(t, client)

	config, err := testutil.TestWalletConfig()
	assert.NoError(t, err)

	amountSats := int64(100_000) // TODO: this should match the amount from the leaves

	// Setup a user with some leaves
	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	rootNode, err := testutil.CreateNewTree(t, config, leafPrivKey, amountSats)
	assert.NoError(t, err)

	// Initiate SSP
	sspPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	sspPubkey := sspPrivKey.PubKey()
	sspIntermediateAddress, err := common.P2TRAddressFromPublicKey(sspPubkey.SerializeCompressed(), config.Network)
	assert.NoError(t, err)
	sspConfig, err := testutil.TestWalletConfigWithIdentityKey(*sspPrivKey)
	assert.NoError(t, err)

	// Initiate exit - SSP is just another user, providing a service external to the SO
	withdrawPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	withdrawPubKey := withdrawPrivKey.PubKey()
	withdrawAddress, err := common.P2TRAddressFromPublicKey(withdrawPubKey.SerializeCompressed(), config.Network)
	assert.NoError(t, err)

	leafCount := 1                                                    // TODO: this should match the number of leaves
	dustAmountSats := 354                                             // TODO: this should match the proper dust
	intermediateAmountSats := int64((leafCount + 1) * dustAmountSats) // +1 for an output SSP can fee bump

	exitTx, err := testutil.CreateTestCoopExitTransaction(fundingOutPoint, *withdrawAddress, amountSats, *sspIntermediateAddress, intermediateAmountSats)
	assert.NoError(t, err)

	exitTxHash := exitTx.TxHash()
	intermediateOutPoint := wire.NewOutPoint(&exitTxHash, 1)
	connectorP2trAddrs := make([]string, 0)
	for i := 0; i < leafCount+1; i++ {
		connectorPrivKey, err := secp256k1.GeneratePrivateKey()
		assert.NoError(t, err)
		connectorPubKey := connectorPrivKey.PubKey()
		connectorAddress, err := common.P2TRAddressFromPublicKey(connectorPubKey.SerializeCompressed(), config.Network)
		assert.NoError(t, err)
		connectorP2trAddrs = append(connectorP2trAddrs, *connectorAddress)
	}
	feeBumpAddr := connectorP2trAddrs[len(connectorP2trAddrs)-1]
	connectorP2trAddrs = connectorP2trAddrs[:len(connectorP2trAddrs)-1]
	connectorTx, err := testutil.CreateTestConnectorTransaction(intermediateOutPoint, intermediateAmountSats, connectorP2trAddrs, feeBumpAddr)
	assert.NoError(t, err)

	connectorOutputs := make([]*wire.OutPoint, 0)
	for i := range connectorTx.TxOut[:len(connectorTx.TxOut)-1] {
		txHash := connectorTx.TxHash()
		connectorOutputs = append(connectorOutputs, wire.NewOutPoint(&txHash, uint32(i)))
	}

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	}

	senderTransfer, _, err := wallet.GetConnectorRefundSignatures(
		context.Background(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxHash.CloneBytes(),
		connectorOutputs,
		sspPubkey,
	)
	assert.NoError(t, err)

	sspToken, err := wallet.AuthenticateWithServer(context.Background(), sspConfig)
	assert.NoError(t, err)
	sspCtx := wallet.ContextWithToken(context.Background(), sspToken)

	pendingTransfer, err := wallet.QueryPendingTransfers(sspCtx, sspConfig)
	assert.NoError(t, err)
	if len(pendingTransfer.Transfers) != 1 {
		t.Fatalf("expected 1 pending transfer, got %d", len(pendingTransfer.Transfers))
	}
	receiverTransfer := pendingTransfer.Transfers[0]
	if receiverTransfer.Id != senderTransfer.Id {
		t.Fatalf("expected transfer id %s, got %s", senderTransfer.Id, receiverTransfer.Id)
	}

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), sspConfig, receiverTransfer)
	assert.NoError(t, err)
	if len(*leafPrivKeyMap) != 1 {
		t.Fatalf("Expected 1 leaf to transfer, got %d", len(*leafPrivKeyMap))
	}
	if !bytes.Equal((*leafPrivKeyMap)[rootNode.Id], newLeafPrivKey.Serialize()) {
		t.Fatalf("wrong leaf signing private key")
	}

	// Try to claim leaf before exit tx confirms -> should fail
	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	err = wallet.ClaimTransfer(
		sspCtx,
		receiverTransfer,
		sspConfig,
		leavesToClaim[:],
	)
	if err == nil {
		t.Fatalf("expected error claiming transfer before exit tx confirms")
	}

	// Sign exit tx and broadcast
	signedExitTx := testutil.SignOnChainTx(t, exitTx, fundingTxOut, sspOnChainKey)

	_, err = client.SendRawTransaction(signedExitTx, true)
	assert.NoError(t, err)

	// Make sure the exit tx gets enough confirmations
	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)
	_, err = client.GenerateToAddress(handler.CoopExitConfirmationThreshold, randomAddress, nil)
	assert.NoError(t, err)

	// Claim leaf
	err = wallet.ClaimTransfer(
		sspCtx,
		receiverTransfer,
		sspConfig,
		leavesToClaim[:],
	)
	assert.NoError(t, err)
}
