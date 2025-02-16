package grpctest

import (
	"bytes"
	"context"
	"encoding/binary"
	"log"
	"math/big"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/utils"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
)

func int64ToUint128Bytes(high, low uint64) []byte {
	return append(
		binary.BigEndian.AppendUint64(make([]byte, 0), high),
		binary.BigEndian.AppendUint64(make([]byte, 0), low)...,
	)
}

func TestBroadcastTokenTransactionIssueAndTransferTokens(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKey := tokenPrivKey.PubKey()
	tokenIdentityPubKeyBytes := tokenIdentityPubKey.SerializeCompressed()

	// In practice this would be derived from the same private key.  For this test we'll just use a seperate keypair.
	userLeaf1PrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userLeaf1PubKey := userLeaf1PrivKey.PubKey()
	userLeaf1PubKeyBytes := userLeaf1PubKey.SerializeCompressed()

	userLeaf2PrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userLeaf2PubKey := userLeaf2PrivKey.PubKey()
	userLeaf2PubKeyBytes := userLeaf2PubKey.SerializeCompressed()

	// Create a token transaction
	issueTokenTransaction := &pb.TokenTransaction{
		// For an issuance transaction, we don't need any input leaves
		TokenInput: &pb.TokenTransaction_MintInput{
			MintInput: &pb.MintInput{
				IssuerPublicKey: tokenIdentityPubKeyBytes,
			},
		},
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: userLeaf1PubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,      // Using user pubkey as token ID for this example
				TokenAmount:    int64ToUint128Bytes(0, 11111), // high bits = 0, low bits = 99999
			},
			{
				OwnerPublicKey: userLeaf2PubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,      // Using user pubkey as token ID for this example
				TokenAmount:    int64ToUint128Bytes(0, 22222), // high bits = 0, low bits = 99999
			},
		},
	}

	// Broadcast the token transaction
	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	if err != nil {
		t.Fatalf("failed to broadcast issuance token transaction: %v", err)
	}
	log.Printf("issuance broadcast finalized token transaction: %v", finalIssueTokenTransaction)

	// Validate withdrawal params match config
	for i, leaf := range finalIssueTokenTransaction.OutputLeaves {
		if leaf.GetWithdrawBondSats() != 1000000 {
			t.Errorf("leaf %d: expected withdrawal bond sats 1000000, got %d", i, leaf.GetWithdrawBondSats())
		}
		if leaf.GetWithdrawRelativeBlockLocktime() != 1000 {
			t.Errorf("leaf %d: expected withdrawal relative block locktime 1000, got %d", i, leaf.GetWithdrawRelativeBlockLocktime())
		}
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance token transaction: %v", err)
	}

	userLeaf3PrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userLeaf3PubKey := userLeaf3PrivKey.PubKey()
	userLeaf3PubKeyBytes := userLeaf3PubKey.SerializeCompressed()

	transferTokenTransaction := &pb.TokenTransaction{
		// Spend the leaves created with issuance before into a new single leaf.
		TokenInput: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TransferInput{
				LeavesToSpend: []*pb.TokenLeafToSpend{
					{
						PrevTokenTransactionHash:     finalIssueTokenTransactionHash,
						PrevTokenTransactionLeafVout: 0,
					},
					{
						PrevTokenTransactionHash:     finalIssueTokenTransactionHash,
						PrevTokenTransactionLeafVout: 1,
					},
				},
			},
		},
		// Send the funds back to the issuer.
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: userLeaf3PubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,      // Using user pubkey as token ID for this example
				TokenAmount:    int64ToUint128Bytes(0, 33333), // high bits = 0, low bits = 99999
			},
		},
	}

	// Validate withdrawal params match config
	for i, leaf := range finalIssueTokenTransaction.OutputLeaves {
		if leaf.GetWithdrawBondSats() != 1000000 {
			t.Errorf("leaf %d: expected withdrawal bond sats 1000000, got %d", i, leaf.GetWithdrawBondSats())
		}
		if leaf.GetWithdrawRelativeBlockLocktime() != 1000 {
			t.Errorf("leaf %d: expected withdrawal relative block locktime 1000, got %d", i, leaf.GetWithdrawRelativeBlockLocktime())
		}
	}

	revPubKey1 := finalIssueTokenTransaction.OutputLeaves[0].RevocationPublicKey
	revPubKey2 := finalIssueTokenTransaction.OutputLeaves[1].RevocationPublicKey

	// Broadcast the token transaction
	finalTransferTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	if err != nil {
		t.Fatalf("failed to broadcast transfer token transaction: %v", err)
	}
	log.Printf("transfer broadcast finalized token transaction: %v", finalTransferTokenTransaction)

	// Call FreezeTokens to freeze the output leaf
	freezeResponse, err := wallet.FreezeTokens(
		context.Background(),
		config,
		finalTransferTokenTransaction.OutputLeaves[0].OwnerPublicKey, // owner public key of the leaf to freeze
		tokenIdentityPubKeyBytes,                                     // token public key
		false,                                                        // unfreeze
	)
	if err != nil {
		t.Fatalf("failed to freeze tokens: %v", err)
	}

	// Convert frozen amount bytes to big.Int for comparison
	frozenAmount := new(big.Int).SetBytes(freezeResponse.ImpactedTokenAmount[0])

	// Calculate total amount from transaction output leaves
	expectedAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, 33333))
	expectedLeafID := finalTransferTokenTransaction.OutputLeaves[0].Id

	if frozenAmount.Cmp(expectedAmount) != 0 {
		t.Errorf("frozen amount %s does not match expected amount %s",
			frozenAmount.String(), expectedAmount.String())
	}
	if len(freezeResponse.ImpactedLeafIds) != 1 {
		t.Errorf("expected 1 impacted leaf ID, got %d", len(freezeResponse.ImpactedLeafIds))
	}
	if freezeResponse.ImpactedLeafIds[0] != *expectedLeafID {
		t.Errorf("frozen leaf ID %s does not match expected leaf ID %s",
			freezeResponse.ImpactedLeafIds[0], *expectedLeafID)
	}

	if err != nil {
		t.Fatalf("failed to freeze tokens: %v", err)
	}

	finalTransferTokenTransactionHash, err := utils.HashTokenTransaction(finalTransferTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final transfer token transaction: %v", err)
	}
	revPubKey3 := finalTransferTokenTransaction.OutputLeaves[0].RevocationPublicKey

	transferFrozenTokenTransaction := &pb.TokenTransaction{
		// Spend the leaves created with issuance before into a new single leaf.
		TokenInput: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TransferInput{
				LeavesToSpend: []*pb.TokenLeafToSpend{
					{
						PrevTokenTransactionHash:     finalTransferTokenTransactionHash,
						PrevTokenTransactionLeafVout: 0,
					},
				},
			},
		},
		// Send the funds back to the issuer.
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: tokenIdentityPubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,      // Using user pubkey as token ID for this example
				TokenAmount:    int64ToUint128Bytes(0, 33333), // high bits = 0, low bits = 99999
			},
		},
	}

	// Broadcast the token transaction
	transferFrozenTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferFrozenTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf3PrivKey},
		[][]byte{revPubKey3},
	)
	if err == nil {
		t.Fatal("expected error when transferring frozen tokens, got nil")
	}
	if transferFrozenTokenTransactionResponse != nil {
		t.Errorf("expected nil response when transferring frozen tokens, got %+v", transferFrozenTokenTransactionResponse)
	}

	log.Printf("Froze tokens with response: %+v", freezeResponse)

	// Call FreezeTokens to thaw the output leaf
	unfreezeResponse, err := wallet.FreezeTokens(
		context.Background(),
		config,
		finalTransferTokenTransaction.OutputLeaves[0].OwnerPublicKey, // owner public key of the leaf to freeze
		tokenIdentityPubKeyBytes,
		true, // unfreeze
	)

	// Convert frozen amount bytes to big.Int for comparison
	thawedAmount := new(big.Int).SetBytes(unfreezeResponse.ImpactedTokenAmount[0])

	if thawedAmount.Cmp(expectedAmount) != 0 {
		t.Errorf("thawed amount %s does not match expected amount %s",
			thawedAmount.String(), expectedAmount.String())
	}
	if len(unfreezeResponse.ImpactedLeafIds) != 1 {
		t.Errorf("expected 1 impacted leaf ID, got %d", len(unfreezeResponse.ImpactedLeafIds))
	}
	if unfreezeResponse.ImpactedLeafIds[0] != *expectedLeafID {
		t.Errorf("thawed leaf ID %s does not match expected leaf ID %s",
			unfreezeResponse.ImpactedLeafIds[0], *expectedLeafID)
	}

	if err != nil {
		t.Fatalf("failed to freeze tokens: %v", err)
	}

	if err != nil {
		t.Fatalf("failed to hash final transfer token transaction: %v", err)
	}

	transferThawedTokenTransaction := &pb.TokenTransaction{
		// Spend the leaves created with issuance before into a new single leaf.
		TokenInput: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TransferInput{
				LeavesToSpend: []*pb.TokenLeafToSpend{
					{
						PrevTokenTransactionHash:     finalTransferTokenTransactionHash,
						PrevTokenTransactionLeafVout: 0,
					},
				},
			},
		},
		// Send the funds back to the issuer.
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: userLeaf1PubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,      // Using user pubkey as token ID for this example
				TokenAmount:    int64ToUint128Bytes(0, 33333), // high bits = 0, low bits = 99999
			},
		},
	}

	// Broadcast the token transaction
	transferThawedTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferThawedTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf3PrivKey},
		[][]byte{revPubKey3},
	)
	if err != nil {
		t.Fatalf("failed to broadcast thawed token transaction: %v", err)
	}
	if transferThawedTokenTransactionResponse == nil {
		t.Fatal("expected non-nil response when transferring thawed tokens")
	}
	log.Printf("thawed token transfer broadcast finalized token transaction: %v", transferThawedTokenTransactionResponse)

	// Test GetOwnedTokenLeaves
	ownedLeavesResponse, err := wallet.GetOwnedTokenLeaves(
		context.Background(),
		config,
		userLeaf1PubKeyBytes,
		tokenIdentityPubKeyBytes,
	)
	if err != nil {
		t.Fatalf("failed to get owned token leaves: %v", err)
	}

	// Validate the response
	if len(ownedLeavesResponse.LeavesWithPreviousTransactionData) != 1 {
		t.Fatalf("expected 1 owned leaf, got %d", len(ownedLeavesResponse.LeavesWithPreviousTransactionData))
	}

	leaf := ownedLeavesResponse.LeavesWithPreviousTransactionData[0]

	// Validate leaf details
	if !bytes.Equal(leaf.Leaf.OwnerPublicKey, userLeaf1PubKeyBytes) {
		t.Fatalf("leaf owner public key does not match expected")
	}
	if !bytes.Equal(leaf.Leaf.TokenPublicKey, tokenIdentityPubKeyBytes) {
		t.Fatalf("leaf token public key does not match expected")
	}

	// Validate amount
	expectedAmount = new(big.Int).SetBytes(int64ToUint128Bytes(0, 33333))
	actualAmount := new(big.Int).SetBytes(leaf.Leaf.TokenAmount)
	if actualAmount.Cmp(expectedAmount) != 0 {
		t.Fatalf("leaf token amount %d does not match expected %d", actualAmount, expectedAmount)
	}

	// Validate previous transaction data
	transferThawedTokenTransactionResponseHash, err := utils.HashTokenTransaction(transferThawedTokenTransactionResponse, false)
	if err != nil {
		t.Fatalf("failed to hash final transfer token transaction: %v", err)
	}
	if !bytes.Equal(leaf.PreviousTransactionHash, transferThawedTokenTransactionResponseHash) {
		t.Fatalf("previous transaction hash does not match expected")
	}
	if leaf.PreviousTransactionVout != 0 {
		t.Fatalf("previous transaction vout expected 0, got %d", leaf.PreviousTransactionVout)
	}
}
