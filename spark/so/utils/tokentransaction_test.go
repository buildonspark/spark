package utils

import (
	"bytes"
	"encoding/hex"
	"testing"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"google.golang.org/protobuf/proto"
)

func TestHashTokenTransaction(t *testing.T) {
	tokenPublicKey := []byte{
		242, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
		252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 45,
	}

	identityPubKey := []byte{
		25, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
		252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
	}

	leafID := "db1a4e48-0fc5-4f6c-8a80-d9d6c561a436"
	bondSats := uint64(10000)
	locktime := uint64(100)

	// Create the token transaction matching the JavaScript object
	partialTokenTransaction := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_MintInput{
			MintInput: &pb.MintInput{
				IssuerPublicKey:         tokenPublicKey,
				IssuerProvidedTimestamp: 100,
			},
		},
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				Id:                            &leafID,
				OwnerPublicKey:                identityPubKey,
				TokenPublicKey:                tokenPublicKey,
				TokenAmount:                   []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 232}, // 1000n in BE format
				RevocationPublicKey:           identityPubKey,
				WithdrawBondSats:              &bondSats,
				WithdrawRelativeBlockLocktime: &locktime,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{},
		Network:                         pb.Network_REGTEST,
	}

	hash, err := HashTokenTransaction(partialTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash token transaction: %v", err)
	}

	expectedHash := []byte{
		39, 154, 106, 90, 228, 192, 20, 72, 126, 11, 34, 149, 35, 65, 184, 120, 112, 131, 70, 59, 179, 34, 60, 184, 120, 169, 124, 135, 175, 146, 103, 167,
	}

	if !bytes.Equal(hash, expectedHash) {
		t.Errorf("hash mismatch\ngot:  %v\nwant: %v", hash, expectedHash)
	}
}

// TestHashTokenTransactionNil ensures an error is returned when HashTokenTransaction is called with a nil transaction.
func TestHashTokenTransactionNil(t *testing.T) {
	_, err := HashTokenTransaction(nil, false)
	if err == nil {
		t.Errorf("expected an error for nil token transaction, but got nil")
	}
}

// TestHashTokenTransactionEmpty checks that hashing an empty transaction does not produce an error.
func TestHashTokenTransactionEmpty(t *testing.T) {
	tx := &pb.TokenTransaction{
		OutputLeaves:                    []*pb.TokenLeafOutput{},
		SparkOperatorIdentityPublicKeys: [][]byte{},
	}
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
	operatorKeys := [][]byte{
		bytes.Repeat([]byte{0x04}, 32),
		bytes.Repeat([]byte{0x05}, 32),
		bytes.Repeat([]byte{0x06}, 32),
	}

	partialMintTokenTransaction := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_MintInput{
			MintInput: &pb.MintInput{
				IssuerPublicKey: bytes.Repeat([]byte{0x01}, 32),
			},
		},
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: bytes.Repeat([]byte{0x01}, 32),
				TokenPublicKey: bytes.Repeat([]byte{0x02}, 32),
				TokenAmount:    []byte{0x01},
			},
		},
		SparkOperatorIdentityPublicKeys: operatorKeys,
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
				OwnerPublicKey: bytes.Repeat([]byte{0x01}, 32),
				TokenPublicKey: bytes.Repeat([]byte{0x02}, 32),
				TokenAmount:    []byte{0x01},
			},
		},
		SparkOperatorIdentityPublicKeys: operatorKeys,
	}

	leafID := "test-leaf-1"
	bondSats := uint64(1000000)
	blockLocktime := uint64(1000)
	finalMintTokenTransaction := proto.Clone(partialMintTokenTransaction).(*pb.TokenTransaction)
	finalMintTokenTransaction.OutputLeaves[0].Id = &leafID
	finalMintTokenTransaction.OutputLeaves[0].RevocationPublicKey = bytes.Repeat([]byte{0x03}, 32)
	finalMintTokenTransaction.OutputLeaves[0].WithdrawBondSats = &bondSats
	finalMintTokenTransaction.OutputLeaves[0].WithdrawRelativeBlockLocktime = &blockLocktime

	finalTransferTokenTransaction := proto.Clone(partialTransferTokenTransaction).(*pb.TokenTransaction)
	finalTransferTokenTransaction.OutputLeaves[0].Id = &leafID
	finalTransferTokenTransaction.OutputLeaves[0].RevocationPublicKey = bytes.Repeat([]byte{0x03}, 32)
	finalTransferTokenTransaction.OutputLeaves[0].WithdrawBondSats = &bondSats
	finalTransferTokenTransaction.OutputLeaves[0].WithdrawRelativeBlockLocktime = &blockLocktime

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
