package utils

import (
	"bytes"
	"encoding/hex"
	"testing"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"google.golang.org/protobuf/proto"
)

// TestHashTokenTransactionNil ensures an error is returned when HashTokenTransaction is called with a nil transaction.
func TestHashTokenTransactionNil(t *testing.T) {
	_, err := HashTokenTransaction(nil, false)
	if err == nil {
		t.Errorf("expected an error for nil token transaction, but got nil")
	}
}

// TestHashTokenTransactionEmpty checks that hashing an empty transaction does not produce an error.
func TestHashTokenTransactionEmpty(t *testing.T) {
	tx := &pb.TokenTransaction{}
	hash, err := HashTokenTransaction(tx, false)
	if err != nil {
		t.Errorf("expected no error for empty transaction, got: %v", err)
	}
	if len(hash) == 0 {
		t.Errorf("expected a non-empty hash")
	}
}

// TestHashTokenTransactionValid checks that hashing a valid token transaction does not produce an error.
func TestHashTokenTransactionUniqueHash(t *testing.T) {
	partialMintTokenTransaction := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_MintInput{
			MintInput: &pb.MintInput{
				IssuerPublicKey: bytes.Repeat([]byte{0x01}, 32),
			},
		},
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey:     bytes.Repeat([]byte{0x01}, 32),
				TokenPublicKey:     bytes.Repeat([]byte{0x02}, 32),
				TokenAmount:        []byte{0x01},
				WithdrawalBondSats: 500000,
				WithdrawalLocktime: 1000,
			},
		},
	}

	partialTransferTokenTransaction := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TransferInput{
				LeavesToSpend: []*pb.TokenLeafToSpend{
					{
						PrevTokenTransactionHash:     bytes.Repeat([]byte{0x01}, 32),
						PrevTokenTransactionLeafVout: 1,
					},
				},
			},
		},
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey:     bytes.Repeat([]byte{0x01}, 32),
				TokenPublicKey:     bytes.Repeat([]byte{0x02}, 32),
				TokenAmount:        []byte{0x01},
				WithdrawalBondSats: 500000,
				WithdrawalLocktime: 1000,
			},
		},
	}

	leafID := "test-leaf-1"
	finalMintTokenTransaction := proto.Clone(partialMintTokenTransaction).(*pb.TokenTransaction)
	finalMintTokenTransaction.OutputLeaves[0].Id = &leafID
	finalMintTokenTransaction.OutputLeaves[0].RevocationPublicKey = bytes.Repeat([]byte{0x03}, 32)

	finalTransferTokenTransaction := proto.Clone(partialTransferTokenTransaction).(*pb.TokenTransaction)
	finalTransferTokenTransaction.OutputLeaves[0].Id = &leafID
	finalTransferTokenTransaction.OutputLeaves[0].RevocationPublicKey = bytes.Repeat([]byte{0x03}, 32)

	// Hash all transactions
	partialMintHash, err := HashTokenTransaction(partialMintTokenTransaction, true)
	if err != nil {
		t.Fatalf("failed to hash partial issuance transaction: %v", err)
	}

	partialTransferHash, err := HashTokenTransaction(partialTransferTokenTransaction, true)
	if err != nil {
		t.Fatalf("failed to hash partial transfer transaction: %v", err)
	}

	finalMintHash, err := HashTokenTransaction(finalMintTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance transaction: %v", err)
	}

	finalTransferHash, err := HashTokenTransaction(finalTransferTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final transfer transaction: %v", err)
	}

	// Create map to check for duplicates
	hashes := map[string]string{
		"partialMint":     hex.EncodeToString(partialMintHash),
		"partialTransfer": hex.EncodeToString(partialTransferHash),
		"finalMint":       hex.EncodeToString(finalMintHash),
		"finalTransfer":   hex.EncodeToString(finalTransferHash),
	}

	// Check that all hashes are unique
	seen := make(map[string]bool)
	for name, hash := range hashes {
		if seen[hash] {
			t.Errorf("duplicate hash detected for %s", name)
		}
		seen[hash] = true
	}
}
