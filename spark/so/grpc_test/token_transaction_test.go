package grpctest

import (
	"context"
	"encoding/binary"
	"log"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
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
				Id:                 uuid.New().String(), // Generate a unique ID for the leaf
				OwnerPublicKey:     userLeaf1PubKeyBytes,
				WithdrawalBondSats: 10000,                                         // Example bond amount
				WithdrawalLocktime: uint64(time.Now().Add(24 * time.Hour).Unix()), // 24 hour locktime
				TokenPublicKey:     tokenIdentityPubKeyBytes,                      // Using user pubkey as token ID for this example
				TokenAmount:        int64ToUint128Bytes(0, 11111),                 // high bits = 0, low bits = 99999
			},
			{
				Id:                 uuid.New().String(), // Generate a unique ID for the leaf
				OwnerPublicKey:     userLeaf2PubKeyBytes,
				WithdrawalBondSats: 10000,                                         // Example bond amount
				WithdrawalLocktime: uint64(time.Now().Add(24 * time.Hour).Unix()), // 24 hour locktime
				TokenPublicKey:     tokenIdentityPubKeyBytes,                      // Using user pubkey as token ID for this example
				TokenAmount:        int64ToUint128Bytes(0, 22222),                 // high bits = 0, low bits = 99999
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

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance token transaction: %v", err)
	}

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
				Id:                 uuid.New().String(), // Generate a unique ID for the leaf
				OwnerPublicKey:     tokenIdentityPubKeyBytes,
				WithdrawalBondSats: 10000,                                         // Example bond amount
				WithdrawalLocktime: uint64(time.Now().Add(24 * time.Hour).Unix()), // 24 hour locktime
				TokenPublicKey:     tokenIdentityPubKeyBytes,                      // Using user pubkey as token ID for this example
				TokenAmount:        int64ToUint128Bytes(0, 33333),                 // high bits = 0, low bits = 99999
			},
		},
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
}
