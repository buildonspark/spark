package grpctest

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
)

func TestTransfer(t *testing.T) {
	// Sender initiates transfer
	senderConfig, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create sender wallet config: %v", err)
	}

	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create node signing private key: %v", err)
	}
	rootNode, err := testutil.CreateNewTree(senderConfig, faucet, leafPrivKey, 100_000)
	if err != nil {
		t.Fatalf("failed to create new tree: %v", err)
	}

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}

	receiverPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create receiver private key: %v", err)
	}

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}
	senderTransfer, err := wallet.SendTransfer(
		context.Background(),
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.PubKey().SerializeCompressed(),
		time.Now().Add(10*time.Minute),
	)
	if err != nil {
		t.Fatalf("failed to transfer tree node: %v", err)
	}

	// Receiver queries pending transfer
	receiverConfig, err := testutil.TestWalletConfigWithIdentityKey(*receiverPrivKey)
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}
	receiverToken, err := wallet.AuthenticateWithServer(context.Background(), receiverConfig)
	if err != nil {
		t.Fatalf("failed to authenticate receiver: %v", err)
	}
	receiverCtx := wallet.ContextWithToken(context.Background(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	if err != nil {
		t.Fatalf("failed to query pending transfers: %v", err)
	}
	if len(pendingTransfer.Transfers) != 1 {
		t.Fatalf("expected 1 pending transfer, got %d", len(pendingTransfer.Transfers))
	}
	receiverTransfer := pendingTransfer.Transfers[0]
	if receiverTransfer.Id != senderTransfer.Id {
		t.Fatalf("expected transfer id %s, got %s", senderTransfer.Id, receiverTransfer.Id)
	}

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), receiverConfig, receiverTransfer)
	if err != nil {
		t.Fatalf("unable to verify pending transfer: %v", err)
	}
	if len(*leafPrivKeyMap) != 1 {
		t.Fatalf("Expected 1 leaf to transfer, got %d", len(*leafPrivKeyMap))
	}
	if !bytes.Equal((*leafPrivKeyMap)[rootNode.Id], newLeafPrivKey.Serialize()) {
		t.Fatalf("wrong leaf signing private key")
	}

	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim[:],
	)
	if err != nil {
		t.Fatalf("failed to ClaimTransfer: %v", err)
	}
}

func TestTransferWithSeparateSteps(t *testing.T) {
	// Sender initiates transfer
	senderConfig, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create sender wallet config: %v", err)
	}

	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create node signing private key: %v", err)
	}
	rootNode, err := testutil.CreateNewTree(senderConfig, faucet, leafPrivKey, 100_000)
	if err != nil {
		t.Fatalf("failed to create new tree: %v", err)
	}

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}

	receiverPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create receiver private key: %v", err)
	}

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}
	senderTransfer, err := wallet.SendTransfer(
		context.Background(),
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.PubKey().SerializeCompressed(),
		time.Now().Add(10*time.Minute),
	)
	if err != nil {
		t.Fatalf("failed to transfer tree node: %v", err)
	}

	// Receiver queries pending transfer
	receiverConfig, err := testutil.TestWalletConfigWithIdentityKey(*receiverPrivKey)
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}
	receiverToken, err := wallet.AuthenticateWithServer(context.Background(), receiverConfig)
	if err != nil {
		t.Fatalf("failed to authenticate receiver: %v", err)
	}
	receiverCtx := wallet.ContextWithToken(context.Background(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	if err != nil {
		t.Fatalf("failed to query pending transfers: %v", err)
	}
	if len(pendingTransfer.Transfers) != 1 {
		t.Fatalf("expected 1 pending transfer, got %d", len(pendingTransfer.Transfers))
	}
	receiverTransfer := pendingTransfer.Transfers[0]
	if receiverTransfer.Id != senderTransfer.Id {
		t.Fatalf("expected transfer id %s, got %s", senderTransfer.Id, receiverTransfer.Id)
	}

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), receiverConfig, receiverTransfer)
	if err != nil {
		t.Fatalf("unable to verify pending transfer: %v", err)
	}
	if len(*leafPrivKeyMap) != 1 {
		t.Fatalf("Expected 1 leaf to transfer, got %d", len(*leafPrivKeyMap))
	}
	if !bytes.Equal((*leafPrivKeyMap)[rootNode.Id], newLeafPrivKey.Serialize()) {
		t.Fatalf("wrong leaf signing private key")
	}

	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}

	err = wallet.ClaimTransferTweakKeys(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim[:],
	)
	if err != nil {
		t.Fatalf("failed to tweak keys when claiming leaves: %v", err)
	}

	pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	if err != nil {
		t.Fatalf("failed to query pending transfers: %v", err)
	}
	if len(pendingTransfer.Transfers) != 1 {
		t.Fatalf("expected 1 pending transfer, got %d", len(pendingTransfer.Transfers))
	}
	receiverTransfer = pendingTransfer.Transfers[0]
	if receiverTransfer.Id != senderTransfer.Id {
		t.Fatalf("expected transfer id %s, got %s", senderTransfer.Id, receiverTransfer.Id)
	}

	leafPrivKeyMap, err = wallet.VerifyPendingTransfer(context.Background(), receiverConfig, receiverTransfer)
	if err != nil {
		t.Fatalf("unable to verify pending transfer: %v", err)
	}
	if len(*leafPrivKeyMap) != 1 {
		t.Fatalf("Expected 1 leaf to transfer, got %d", len(*leafPrivKeyMap))
	}
	if !bytes.Equal((*leafPrivKeyMap)[rootNode.Id], newLeafPrivKey.Serialize()) {
		t.Fatalf("wrong leaf signing private key")
	}

	_, err = wallet.ClaimTransferSignRefunds(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim[:],
	)
	if err != nil {
		t.Fatalf("failed to sign refunds when claiming leaves: %v", err)
	}

	pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	if err != nil {
		t.Fatalf("failed to query pending transfers: %v", err)
	}
	if len(pendingTransfer.Transfers) != 1 {
		t.Fatalf("expected 1 pending transfer, got %d", len(pendingTransfer.Transfers))
	}

	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim[:],
	)
	if err != nil {
		t.Fatalf("failed to ClaimTransfer: %v", err)
	}
}

func TestCancelTransfer(t *testing.T) {
	// Sender initiates transfer
	senderConfig, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create sender wallet config: %v", err)
	}

	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create node signing private key: %v", err)
	}
	rootNode, err := testutil.CreateNewTree(senderConfig, faucet, leafPrivKey, 100_000)
	if err != nil {
		t.Fatalf("failed to create new tree: %v", err)
	}

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}

	receiverPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create receiver private key: %v", err)
	}

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}
	senderTransfer, _, _, err := wallet.SendTransferSignRefund(
		context.Background(),
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.PubKey().SerializeCompressed(),
		time.Now().Add(10*time.Minute),
	)
	if err != nil {
		t.Fatalf("failed to transfer tree node: %v", err)
	}

	_, err = wallet.CancelSendTransfer(context.Background(), senderConfig, senderTransfer)
	if err != nil {
		t.Fatalf("failed to cancel transfer: %v", err)
	}

	senderTransfer, err = wallet.SendTransfer(
		context.Background(),
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.PubKey().SerializeCompressed(),
		time.Now().Add(10*time.Minute),
	)
	if err != nil {
		t.Fatalf("failed to transfer tree node: %v", err)
	}

	receiverConfig, err := testutil.TestWalletConfigWithIdentityKey(*receiverPrivKey)
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}
	receiverToken, err := wallet.AuthenticateWithServer(context.Background(), receiverConfig)
	if err != nil {
		t.Fatalf("failed to authenticate receiver: %v", err)
	}
	receiverCtx := wallet.ContextWithToken(context.Background(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	if err != nil {
		t.Fatalf("failed to query pending transfers: %v", err)
	}
	if len(pendingTransfer.Transfers) != 1 {
		t.Fatalf("expected 1 pending transfer, got %d", len(pendingTransfer.Transfers))
	}
	receiverTransfer := pendingTransfer.Transfers[0]
	if receiverTransfer.Id != senderTransfer.Id {
		t.Fatalf("expected transfer id %s, got %s", senderTransfer.Id, receiverTransfer.Id)
	}

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), receiverConfig, receiverTransfer)
	if err != nil {
		t.Fatalf("unable to verify pending transfer: %v", err)
	}
	if len(*leafPrivKeyMap) != 1 {
		t.Fatalf("Expected 1 leaf to transfer, got %d", len(*leafPrivKeyMap))
	}
	if !bytes.Equal((*leafPrivKeyMap)[rootNode.Id], newLeafPrivKey.Serialize()) {
		t.Fatalf("wrong leaf signing private key")
	}

	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim[:],
	)
	if err != nil {
		t.Fatalf("failed to ClaimTransfer: %v", err)
	}
}
