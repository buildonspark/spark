package utils

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/google/go-cmp/cmp"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Test constants for consistent test data across all hash tests
var (
	testTokenPublicKey = mustParsePubKey([]byte{0x02,
		242, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
		252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 45,
	})

	testIdentityPubKey = mustParsePubKey([]byte{0x02,
		25, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
		252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
	})

	testRevocationPubKey = mustParsePubKey([]byte{0x02,
		100, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
		252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
	})

	testSparkOperatorPubKey = mustParsePubKey([]byte{0x02,
		200, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
		252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
	})
	seededRng = rand.NewChaCha8([32]byte{})
)

func mustParsePubKey(raw []byte) keys.Public {
	key, err := keys.ParsePublicKey(raw)
	if err != nil {
		panic(err)
	}
	return key
}

type testTokenTransactionData struct {
	tokenPublicKey     keys.Public
	identityPubKey     keys.Public
	revocationPubKey   keys.Public
	operatorPubKey     keys.Public
	leafID             string
	bondSats           uint64
	locktime           uint64
	tokenAmount        []byte
	maxSupply          []byte
	tokenName          string
	tokenTicker        string
	decimals           uint32
	issuerTimestamp    uint64
	clientTimestamp    uint64
	expiryTime         uint64
	prevTxHash         [32]byte
	tokenIdentifier    []byte
	invoiceAttachments []*tokenpb.InvoiceAttachment
}

var testData = testTokenTransactionData{
	tokenPublicKey:   testTokenPublicKey,
	identityPubKey:   testIdentityPubKey,
	revocationPubKey: testRevocationPubKey,
	operatorPubKey:   testSparkOperatorPubKey,
	leafID:           "db1a4e48-0fc5-4f6c-8a80-d9d6c561a436",
	bondSats:         10000,
	locktime:         100,
	tokenAmount:      []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 232}, // 1000 in BE format
	maxSupply:        []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 232}, // 1000 in BE format
	tokenName:        "TestToken",
	tokenTicker:      "TEST",
	decimals:         8,
	issuerTimestamp:  100,
	clientTimestamp:  100,
	expiryTime:       0,
	prevTxHash:       sha256.Sum256([]byte("previous transaction")),
	tokenIdentifier:  bytes.Repeat([]byte{0x07}, 32),
	invoiceAttachments: []*tokenpb.InvoiceAttachment{
		{SparkInvoice: "sparkrt1pgssx5us3wkqjza8g80xz3a9gznx25msq6g3ty8exfym9q3ahcv86vsnzfmssqgjzqqejtaxmwj8ms9rn58574nvlq4j5zr5v4ehgnt9d4hnyggr2wgghtqfpwn5rhnpg7j5pfn92dcqdyg4jrunyjdjsg7muxraxgfn5rqgandgr3sxzrqdmew8qydzvz3qpylysylkgcaw9vpm2jzspls0qtr5kfmlwz244rvuk25w5w2sgc2pyqsraqdyp8tf57a6cn2egttaas9ms3whssenmjqt8wag3lgyvdzjskfeupt8xwwdx4agxdm9f0wefzj28jmdxqeudwcwdj9vfl9sdr65x06r0tasf5fwz2"},
		{SparkInvoice: "sparkrt1pgssx5us3wkqjza8g80xz3a9gznx25msq6g3ty8exfym9q3ahcv86vsnzfmqsqgjzqqejtavuhf8n5uh9a74zw66kqaz5zr5v4ehgnt9d4hnyggr2wgghtqfpwn5rhnpg7j5pfn92dcqdyg4jrunyjdjsg7muxraxgfn5zcglrwcr3sxzzqt3wrjrgnq5gqf8eyp8ajx8t3tqw65s5q0urczca9jwlmsj4dgm89j4r4rj5zxzsfqyqlgrfqw9ucldgmfzs5zmkekj90thwzmn6ps55gdjnz23aarjkf245608yg0v2x6xdpdrz6m8xjlhtru0kygcu4zhqwlth9duadfqpruuzx4tc7fdckn"},
	},
}

func createTestTransactions() (*tokenpb.TokenTransaction, *pb.TokenTransaction) {
	tokenTx := &tokenpb.TokenTransaction{
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: testData.tokenPublicKey.Serialize(),
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				Id:                            &testData.leafID,
				OwnerPublicKey:                testData.identityPubKey.Serialize(),
				TokenPublicKey:                testData.tokenPublicKey.Serialize(),
				TokenAmount:                   testData.tokenAmount,
				RevocationCommitment:          testData.identityPubKey.Serialize(),
				WithdrawBondSats:              &testData.bondSats,
				WithdrawRelativeBlockLocktime: &testData.locktime,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{testData.operatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
		Version:                         0,
		ClientCreatedTimestamp:          timestamppb.New(time.UnixMilli(100)),
	}

	sparkTx := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_MintInput{
			MintInput: &pb.TokenMintInput{
				IssuerPublicKey:         testData.tokenPublicKey.Serialize(),
				IssuerProvidedTimestamp: 100,
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				Id:                            &testData.leafID,
				OwnerPublicKey:                testData.identityPubKey.Serialize(),
				TokenPublicKey:                testData.tokenPublicKey.Serialize(),
				TokenAmount:                   testData.tokenAmount,
				RevocationCommitment:          testData.identityPubKey.Serialize(),
				WithdrawBondSats:              &testData.bondSats,
				WithdrawRelativeBlockLocktime: &testData.locktime,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{testData.operatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
	}

	return tokenTx, sparkTx
}

func TestHashTokenTransactionV0MintLegacyVector(t *testing.T) {
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
		TokenInputs: &pb.TokenTransaction_MintInput{
			MintInput: &pb.TokenMintInput{
				IssuerPublicKey:         tokenPublicKey,
				IssuerProvidedTimestamp: 100,
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				Id:                            &leafID,
				OwnerPublicKey:                identityPubKey,
				TokenPublicKey:                tokenPublicKey,
				TokenAmount:                   []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 232}, // 1000n in BE format
				RevocationCommitment:          identityPubKey,
				WithdrawBondSats:              &bondSats,
				WithdrawRelativeBlockLocktime: &locktime,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{},
		Network:                         pb.Network_REGTEST,
	}

	hash, err := HashTokenTransactionV0(partialTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash token transaction: %v", err)
	}

	want := []byte{
		66, 235, 134, 101, 172, 110, 147, 77, 122, 48, 86, 240, 239, 9, 163, 82,
		120, 234, 246, 206, 245, 242, 186, 180, 154, 41, 207, 179, 194, 31, 211, 36,
	}
	if diff := cmp.Diff(want, hash); diff != "" {
		t.Errorf("hash mismatch (-want +got):\n%s", diff)
	}
}

func TestHashTokenTransactionMintV0(t *testing.T) {
	partialTokenTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_MintInput{
			MintInput: &pb.TokenMintInput{
				IssuerPublicKey:         testTokenPublicKey.Serialize(),
				IssuerProvidedTimestamp: testData.issuerTimestamp,
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				Id:                            &testData.leafID,
				OwnerPublicKey:                testIdentityPubKey.Serialize(),
				TokenPublicKey:                testTokenPublicKey.Serialize(),
				TokenAmount:                   testData.tokenAmount,
				RevocationCommitment:          testRevocationPubKey.Serialize(),
				WithdrawBondSats:              &testData.bondSats,
				WithdrawRelativeBlockLocktime: &testData.locktime,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{testSparkOperatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
	}

	hash, err := HashTokenTransactionV0(partialTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash token transaction: %v", err)
	}

	want := []byte{
		0x8e, 0xad, 0xcb, 0x46, 0x25, 0x46, 0x88, 0x1d, 0xa9, 0x0f, 0xcc, 0x29, 0xba, 0x64, 0xba, 0xa6,
		0xf9, 0x0d, 0x4c, 0xc8, 0xb1, 0x51, 0xcb, 0xae, 0x48, 0xbc, 0xd7, 0x77, 0x28, 0xb8, 0xc4, 0xa5,
	}
	if diff := cmp.Diff(want, hash); diff != "" {
		t.Errorf("hash mismatch (-want +got):\n%s", diff)
	}
}

func TestHashTokenTransactionCreateV0(t *testing.T) {
	createTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_CreateInput{
			CreateInput: &pb.TokenCreateInput{
				IssuerPublicKey: testTokenPublicKey.Serialize(),
				TokenName:       testData.tokenName,
				TokenTicker:     testData.tokenTicker,
				Decimals:        testData.decimals,
				MaxSupply:       testData.maxSupply,
				IsFreezable:     false,
			},
		},
		TokenOutputs:                    []*pb.TokenOutput{},
		SparkOperatorIdentityPublicKeys: [][]byte{testSparkOperatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
	}

	hash, err := HashTokenTransactionV0(createTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash V0 create transaction: %v", err)
	}

	want := []byte{
		0x4c, 0xfb, 0xfe, 0x22, 0xcb, 0x4f, 0x07, 0xea, 0xac, 0x85, 0x2f, 0x94, 0xd9, 0x3e, 0x9d, 0xdb,
		0xed, 0xa0, 0x97, 0xdb, 0x18, 0xb3, 0x5d, 0xb8, 0x26, 0x7c, 0x55, 0x41, 0x0b, 0x32, 0xa5, 0x94,
	}
	if diff := cmp.Diff(want, hash); diff != "" {
		t.Errorf("hash mismatch (-want +got):\n%s", diff)
		t.Logf("Actual hash: %x", hash)
	}
}

func TestHashTokenTransactionTransferV0(t *testing.T) {
	// Create V0 transfer transaction
	transferTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TokenTransferInput{
				OutputsToSpend: []*pb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: testData.prevTxHash[:],
						PrevTokenTransactionVout: 0,
					},
				},
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				Id:                            &testData.leafID,
				OwnerPublicKey:                testIdentityPubKey.Serialize(),
				TokenPublicKey:                testTokenPublicKey.Serialize(),
				TokenAmount:                   testData.tokenAmount,
				RevocationCommitment:          testData.revocationPubKey.Serialize(),
				WithdrawBondSats:              &testData.bondSats,
				WithdrawRelativeBlockLocktime: &testData.locktime,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{testSparkOperatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
	}

	hash, err := HashTokenTransactionV0(transferTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash V0 transfer transaction: %v", err)
	}

	want := []byte{
		0xe3, 0x34, 0xdf, 0x5b, 0x88, 0x59, 0x1f, 0x69, 0x4f, 0xe9, 0xb3, 0x9f, 0x31, 0x83, 0xe2, 0x10,
		0x21, 0xff, 0x95, 0xd6, 0xbf, 0x2d, 0x4b, 0xb0, 0x13, 0xba, 0xc4, 0x4f, 0xd0, 0x9d, 0xba, 0xaf,
	}
	if diff := cmp.Diff(want, hash); diff != "" {
		t.Errorf("hash mismatch (-want +got):\n%s", diff)
		t.Logf("Actual hash: %x", hash)
	}
}

// TestHashTokenTransactionV0Nil ensures an error is returned when HashTokenTransaction is called with a nil transaction.
func TestHashTokenTransactionV0Nil(t *testing.T) {
	_, err := HashTokenTransactionV0(nil, false)
	if err == nil {
		t.Errorf("expected an error for nil token transaction, but got nil")
	}
}

// TestHashTokenTransactionV0Empty checks that hashing an empty transaction does not produce an error.
func TestHashTokenTransactionV0Empty(t *testing.T) {
	tx := &pb.TokenTransaction{
		TokenInputs:                     &pb.TokenTransaction_MintInput{},
		TokenOutputs:                    []*pb.TokenOutput{},
		SparkOperatorIdentityPublicKeys: [][]byte{},
	}
	_, err := HashTokenTransactionV0(tx, false)
	require.ErrorContains(t, err, "token transaction must have exactly one of create_input, mint_input, or transfer_input")
}

// TestHashTokenTransactionV0UniqueHash checks that hashing a valid token transaction does not produce an error
// and that when a field is changed, the hash changes.
func TestHashTokenTransactionV0UniqueHash(t *testing.T) {
	operatorKeys := [][]byte{
		bytes.Repeat([]byte{0x04}, 32),
		bytes.Repeat([]byte{0x05}, 32),
		bytes.Repeat([]byte{0x06}, 32),
	}

	partialMintTokenTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_MintInput{
			MintInput: &pb.TokenMintInput{
				IssuerPublicKey: bytes.Repeat([]byte{0x01}, 32),
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				OwnerPublicKey: bytes.Repeat([]byte{0x01}, 32),
				TokenPublicKey: bytes.Repeat([]byte{0x02}, 32),
				TokenAmount:    []byte{0x01},
			},
		},
		SparkOperatorIdentityPublicKeys: operatorKeys,
	}

	partialTransferTokenTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TokenTransferInput{
				OutputsToSpend: []*pb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: bytes.Repeat([]byte{0x01}, 32),
						PrevTokenTransactionVout: 1,
					},
				},
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				OwnerPublicKey: bytes.Repeat([]byte{0x01}, 32),
				TokenPublicKey: bytes.Repeat([]byte{0x02}, 32),
				TokenAmount:    []byte{0x01},
			},
		},
		SparkOperatorIdentityPublicKeys: operatorKeys,
	}

	outputID := "test-output-1"
	bondSats := uint64(1000000)
	blockLocktime := uint64(1000)
	finalMintTokenTransaction := proto.CloneOf(partialMintTokenTransaction)
	finalMintTokenTransaction.TokenOutputs[0].Id = &outputID
	finalMintTokenTransaction.TokenOutputs[0].RevocationCommitment = bytes.Repeat([]byte{0x03}, 32)
	finalMintTokenTransaction.TokenOutputs[0].WithdrawBondSats = &bondSats
	finalMintTokenTransaction.TokenOutputs[0].WithdrawRelativeBlockLocktime = &blockLocktime

	finalTransferTokenTransaction := proto.CloneOf(partialTransferTokenTransaction)
	finalTransferTokenTransaction.TokenOutputs[0].Id = &outputID
	finalTransferTokenTransaction.TokenOutputs[0].RevocationCommitment = bytes.Repeat([]byte{0x03}, 32)
	finalTransferTokenTransaction.TokenOutputs[0].WithdrawBondSats = &bondSats
	finalTransferTokenTransaction.TokenOutputs[0].WithdrawRelativeBlockLocktime = &blockLocktime

	// Hash all transactions
	partialMintHash, err := HashTokenTransactionV0(partialMintTokenTransaction, true)
	if err != nil {
		t.Fatalf("failed to hash partial issuance transaction: %v", err)
	}

	partialTransferHash, err := HashTokenTransactionV0(partialTransferTokenTransaction, true)
	if err != nil {
		t.Fatalf("failed to hash partial transfer transaction: %v", err)
	}

	finalMintHash, err := HashTokenTransactionV0(finalMintTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance transaction: %v", err)
	}

	finalTransferHash, err := HashTokenTransactionV0(finalTransferTokenTransaction, false)
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

// TestHashTokenTransactionV1Nil ensures an error is returned when HashTokenTransaction is called with a nil transaction.
func TestHashTokenTransactionV1Nil(t *testing.T) {
	_, err := HashTokenTransactionV1(nil, false)
	if err == nil {
		t.Errorf("expected an error for nil token transaction, but got nil")
	}
}

// TestHashTokenTransactionV1Empty checks that hashing an empty transaction does not produce an error.
func TestHashTokenTransactionV1Empty(t *testing.T) {
	tx := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: bytes.Repeat([]byte{0x01}, 32),
			},
		},
		TokenOutputs:                    []*tokenpb.TokenOutput{},
		SparkOperatorIdentityPublicKeys: [][]byte{},
		ClientCreatedTimestamp:          timestamppb.New(time.Unix(0, 0)),
		ExpiryTime:                      timestamppb.New(time.Unix(0, 0)),
	}
	hash, err := HashTokenTransactionV1(tx, false)
	if err != nil {
		t.Errorf("expected no error for empty transaction, got: %v", err)
	}
	if len(hash) == 0 {
		t.Errorf("expected a non-empty hash")
	}
}

// TestHashTokenTransactionV1UniqueHash checks that hashing a valid token transaction does not produce an error
// and that when a field is changed, the hash changes.
func TestHashTokenTransactionV1UniqueHash(t *testing.T) {
	operatorKeys := [][]byte{
		bytes.Repeat([]byte{0x04}, 32),
		bytes.Repeat([]byte{0x05}, 32),
		bytes.Repeat([]byte{0x06}, 32),
	}

	partialMintTokenTransaction := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: bytes.Repeat([]byte{0x01}, 32),
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey: bytes.Repeat([]byte{0x01}, 32),
				TokenPublicKey: bytes.Repeat([]byte{0x02}, 32),
				TokenAmount:    []byte{0x01},
			},
		},
		SparkOperatorIdentityPublicKeys: operatorKeys,
		ClientCreatedTimestamp:          timestamppb.New(time.Unix(0, 0)),
	}

	partialTransferTokenTransaction := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: []*tokenpb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: bytes.Repeat([]byte{0x01}, 32),
						PrevTokenTransactionVout: 1,
					},
				},
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey: bytes.Repeat([]byte{0x01}, 32),
				TokenPublicKey: bytes.Repeat([]byte{0x02}, 32),
				TokenAmount:    []byte{0x01},
			},
		},
		SparkOperatorIdentityPublicKeys: operatorKeys,
		ClientCreatedTimestamp:          timestamppb.New(time.Unix(0, 0)),
	}

	outputID := "test-output-1"
	bondSats := uint64(1000000)
	blockLocktime := uint64(1000)
	finalMintTokenTransaction := proto.CloneOf(partialMintTokenTransaction)
	finalMintTokenTransaction.TokenOutputs[0].Id = &outputID
	finalMintTokenTransaction.TokenOutputs[0].RevocationCommitment = bytes.Repeat([]byte{0x03}, 32)
	finalMintTokenTransaction.TokenOutputs[0].WithdrawBondSats = &bondSats
	finalMintTokenTransaction.TokenOutputs[0].WithdrawRelativeBlockLocktime = &blockLocktime
	finalMintTokenTransaction.ExpiryTime = timestamppb.New(time.Unix(1000, 0))

	finalTransferTokenTransaction := proto.CloneOf(partialTransferTokenTransaction)
	finalTransferTokenTransaction.TokenOutputs[0].Id = &outputID
	finalTransferTokenTransaction.TokenOutputs[0].RevocationCommitment = bytes.Repeat([]byte{0x03}, 32)
	finalTransferTokenTransaction.TokenOutputs[0].WithdrawBondSats = &bondSats
	finalTransferTokenTransaction.TokenOutputs[0].WithdrawRelativeBlockLocktime = &blockLocktime
	finalTransferTokenTransaction.ExpiryTime = timestamppb.New(time.Unix(1000, 0))

	// Hash all transactions
	partialMintHash, err := HashTokenTransactionV1(partialMintTokenTransaction, true)
	if err != nil {
		t.Fatalf("failed to hash partial issuance transaction: %v", err)
	}

	partialTransferHash, err := HashTokenTransactionV1(partialTransferTokenTransaction, true)
	if err != nil {
		t.Fatalf("failed to hash partial transfer transaction: %v", err)
	}

	finalMintHash, err := HashTokenTransactionV1(finalMintTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance transaction: %v", err)
	}

	finalTransferHash, err := HashTokenTransactionV1(finalTransferTokenTransaction, false)
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

func TestHashTokenTransactionMintV1(t *testing.T) {
	partialTokenTransaction := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: testTokenPublicKey.Serialize(),
				TokenIdentifier: testData.tokenIdentifier,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				Id:                            &testData.leafID,
				OwnerPublicKey:                testIdentityPubKey.Serialize(),
				TokenPublicKey:                testTokenPublicKey.Serialize(),
				TokenAmount:                   testData.tokenAmount,
				RevocationCommitment:          testRevocationPubKey.Serialize(),
				WithdrawBondSats:              &testData.bondSats,
				WithdrawRelativeBlockLocktime: &testData.locktime,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{testSparkOperatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
		ExpiryTime:                      timestamppb.New(time.UnixMilli(int64(testData.expiryTime))),
		ClientCreatedTimestamp:          timestamppb.New(time.UnixMilli(int64(testData.clientTimestamp))),
	}

	hash, err := HashTokenTransactionV1(partialTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash token transaction: %v", err)
	}

	want := []byte{
		0xfe, 0x93, 0x8b, 0x12, 0xbf, 0xed, 0x51, 0x79, 0xff, 0x29, 0x8d, 0x2e, 0xd9, 0x66, 0x2b, 0x4a,
		0xf6, 0xf8, 0x35, 0x18, 0x8f, 0x4e, 0xa4, 0xb1, 0xb3, 0x3b, 0x61, 0x23, 0x14, 0x49, 0xdc, 0x81,
	}
	if diff := cmp.Diff(want, hash); diff != "" {
		t.Errorf("hash mismatch (-want +got):\n%s", diff)
	}
}

func TestHashTokenTransactionCreateV1(t *testing.T) {
	// Create V1 token transaction
	createTransaction := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey: testTokenPublicKey.Serialize(),
				TokenName:       testData.tokenName,
				TokenTicker:     testData.tokenTicker,
				Decimals:        testData.decimals,
				MaxSupply:       testData.maxSupply,
				IsFreezable:     false,
			},
		},
		TokenOutputs:                    []*tokenpb.TokenOutput{},
		SparkOperatorIdentityPublicKeys: [][]byte{testSparkOperatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
		ExpiryTime:                      timestamppb.New(time.UnixMilli(int64(testData.expiryTime))),
		ClientCreatedTimestamp:          timestamppb.New(time.UnixMilli(int64(testData.clientTimestamp))),
	}

	hash, err := HashTokenTransactionV1(createTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash create transaction: %v", err)
	}

	// Expected hash for V1 create transaction
	want := []byte{
		0x04, 0x8a, 0xa2, 0xa0, 0x85, 0xab, 0xb9, 0xba, 0x96, 0x9c, 0x70, 0x7c, 0x5f, 0xc7, 0xb3, 0xf2,
		0x14, 0x8c, 0x89, 0x18, 0x5e, 0x0f, 0x7b, 0x16, 0x17, 0xf8, 0xe8, 0x0d, 0x9e, 0x91, 0x48, 0x18,
	}
	if diff := cmp.Diff(want, hash); diff != "" {
		t.Errorf("hash mismatch (-want +got):\n%s", diff)
		t.Logf("Actual hash: %x", hash)
	}
}

func TestHashTokenTransactionTransferV1(t *testing.T) {
	transferTransaction := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: []*tokenpb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: testData.prevTxHash[:],
						PrevTokenTransactionVout: 0,
					},
				},
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				Id:                            &testData.leafID,
				OwnerPublicKey:                testIdentityPubKey.Serialize(),
				TokenPublicKey:                testTokenPublicKey.Serialize(),
				TokenAmount:                   testData.tokenAmount,
				RevocationCommitment:          testData.revocationPubKey.Serialize(),
				WithdrawBondSats:              &testData.bondSats,
				WithdrawRelativeBlockLocktime: &testData.locktime,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{testSparkOperatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
		ExpiryTime:                      timestamppb.New(time.UnixMilli(int64(testData.expiryTime))),
		ClientCreatedTimestamp:          timestamppb.New(time.UnixMilli(int64(testData.clientTimestamp))),
	}

	hash, err := HashTokenTransactionV1(transferTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash transfer transaction: %v", err)
	}

	want := []byte{
		0xa9, 0xfa, 0xe6, 0x24, 0x05, 0xbb, 0x08, 0xe8, 0xa1, 0xf1, 0x6f, 0x9d, 0xc8, 0xa5, 0x53, 0x03,
		0xaf, 0x86, 0x6a, 0x67, 0x10, 0xb5, 0x50, 0x57, 0xca, 0x0c, 0x8d, 0x64, 0x70, 0x00, 0xa5, 0x8f,
	}
	if diff := cmp.Diff(want, hash); diff != "" {
		t.Errorf("hash mismatch (-want +got):\n%s", diff)
		t.Logf("Actual hash: %x", hash)
	}
}

func TestHashTokenTransactionV1RequiredFields(t *testing.T) {
	prevTxHash := sha256.Sum256([]byte("previous transaction"))

	// Create base valid transactions for each type
	baseMintTransaction := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: testTokenPublicKey.Serialize(),
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				Id:                            &testData.leafID,
				OwnerPublicKey:                testIdentityPubKey.Serialize(),
				TokenPublicKey:                testTokenPublicKey.Serialize(),
				TokenAmount:                   testData.tokenAmount,
				RevocationCommitment:          testIdentityPubKey.Serialize(),
				WithdrawBondSats:              &testData.bondSats,
				WithdrawRelativeBlockLocktime: &testData.locktime,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{testSparkOperatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
		ExpiryTime:                      timestamppb.New(time.UnixMilli(int64(testData.expiryTime))),
		ClientCreatedTimestamp:          timestamppb.New(time.UnixMilli(int64(testData.clientTimestamp))),
	}

	baseTransferTransaction := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: []*tokenpb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: prevTxHash[:],
						PrevTokenTransactionVout: 0,
					},
				},
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey: testIdentityPubKey.Serialize(),
				TokenPublicKey: testTokenPublicKey.Serialize(),
				TokenAmount:    testData.tokenAmount,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{testSparkOperatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
		ExpiryTime:                      timestamppb.New(time.UnixMilli(int64(testData.expiryTime))),
		ClientCreatedTimestamp:          timestamppb.New(time.UnixMilli(int64(testData.clientTimestamp))),
	}

	baseCreateTransaction := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey: testTokenPublicKey.Serialize(),
				TokenName:       testData.tokenName,
				TokenTicker:     testData.tokenTicker,
				Decimals:        testData.decimals,
				MaxSupply:       testData.maxSupply,
				IsFreezable:     false,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey: testIdentityPubKey.Serialize(),
				TokenPublicKey: testTokenPublicKey.Serialize(),
				TokenAmount:    testData.tokenAmount,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{testSparkOperatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
		ExpiryTime:                      timestamppb.New(time.UnixMilli(int64(testData.expiryTime))),
		ClientCreatedTimestamp:          timestamppb.New(time.UnixMilli(int64(testData.clientTimestamp))),
	}

	tests := []struct {
		name        string
		txType      string
		baseTx      *tokenpb.TokenTransaction
		modifyTx    func(*tokenpb.TokenTransaction)
		expectedErr string
	}{
		// Common field tests (apply to all transaction types)
		{
			name:   "nil client created timestamp",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.ClientCreatedTimestamp = nil
			},
			expectedErr: "client created timestamp cannot be empty",
		},
		{
			name:   "nil spark operator identity public keys",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.SparkOperatorIdentityPublicKeys = nil
			},
			expectedErr: "operator public keys cannot be nil",
		},
		{
			name:   "nil token output owner public key",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenOutputs[0].OwnerPublicKey = nil
			},
			expectedErr: "owner public key at index 0 cannot be nil or empty",
		},
		{
			name:   "empty token output id",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				emptyString := ""
				tx.TokenOutputs[0].Id = &emptyString
			},
			expectedErr: "token output ID at index 0 cannot be nil or empty",
		},
		{
			name:   "nil token output id",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenOutputs[0].Id = nil
			},
			expectedErr: "token output ID at index 0 cannot be nil or empty",
		},
		{
			name:   "nil token output token amount",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenOutputs[0].TokenAmount = nil
			},
			expectedErr: "token amount at index 0 cannot be nil or empty",
		},
		{
			name:   "empty token output token amount",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenOutputs[0].TokenAmount = []byte{}
			},
			expectedErr: "token amount at index 0 cannot be nil or empty",
		},
		{
			name:   "nil spark operator public key",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.SparkOperatorIdentityPublicKeys = [][]byte{nil}
			},
			expectedErr: "operator public key at index 0 cannot be nil",
		},
		{
			name:   "empty spark operator public key",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.SparkOperatorIdentityPublicKeys = [][]byte{{}}
			},
			expectedErr: "operator public key at index 0 cannot be empty",
		},

		// Mint-specific tests
		{
			name:   "nil mint input issuer public key",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenInputs = &tokenpb.TokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{
						IssuerPublicKey: nil,
					},
				}
			},
			expectedErr: "issuer public key cannot be nil or empty",
		},
		{
			name:   "empty mint input issuer public key",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenInputs = &tokenpb.TokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{
						IssuerPublicKey: []byte{},
					},
				}
			},
			expectedErr: "issuer public key cannot be nil or empty",
		},

		// Transfer-specific tests
		{
			name:   "nil transfer input outputs to spend",
			txType: "transfer",
			baseTx: baseTransferTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenInputs = &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: nil,
					},
				}
			},
			expectedErr: "transfer input outputs cannot be nil",
		},
		{
			name:   "nil output to spend",
			txType: "transfer",
			baseTx: baseTransferTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenInputs = &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{nil},
					},
				}
			},
			expectedErr: "transfer input token output at index 0 cannot be nil",
		},
		{
			name:   "invalid previous transaction hash length",
			txType: "transfer",
			baseTx: baseTransferTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenInputs = &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{
							{
								PrevTokenTransactionHash: []byte{1, 2, 3}, // Too short
								PrevTokenTransactionVout: 0,
							},
						},
					},
				}
			},
			expectedErr: "invalid previous transaction hash length at index 0",
		},

		// Create-specific tests
		{
			name:   "nil create input issuer public key",
			txType: "create",
			baseTx: baseCreateTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenInputs = &tokenpb.TokenTransaction_CreateInput{
					CreateInput: &tokenpb.TokenCreateInput{
						IssuerPublicKey: nil,
						TokenName:       "TestToken",
						TokenTicker:     "TEST",
						Decimals:        8,
						MaxSupply:       []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 100},
						IsFreezable:     false,
					},
				}
			},
			expectedErr: "issuer public key cannot be nil or empty",
		},
		{
			name:   "empty create input issuer public key",
			txType: "create",
			baseTx: baseCreateTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenInputs = &tokenpb.TokenTransaction_CreateInput{
					CreateInput: &tokenpb.TokenCreateInput{
						IssuerPublicKey: []byte{},
						TokenName:       "TestToken",
						TokenTicker:     "TEST",
						Decimals:        8,
						MaxSupply:       []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 100},
						IsFreezable:     false,
					},
				}
			},
			expectedErr: "issuer public key cannot be nil or empty",
		},
		{
			name:   "empty token name",
			txType: "create",
			baseTx: baseCreateTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenInputs = &tokenpb.TokenTransaction_CreateInput{
					CreateInput: &tokenpb.TokenCreateInput{
						IssuerPublicKey: testTokenPublicKey.Serialize(),
						TokenName:       "",
						TokenTicker:     testData.tokenTicker,
						Decimals:        testData.decimals,
						MaxSupply:       testData.maxSupply,
						IsFreezable:     false,
					},
				}
			},
			expectedErr: "token name cannot be empty",
		},
		{
			name:   "empty token ticker",
			txType: "create",
			baseTx: baseCreateTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenInputs = &tokenpb.TokenTransaction_CreateInput{
					CreateInput: &tokenpb.TokenCreateInput{
						IssuerPublicKey: testTokenPublicKey.Serialize(),
						TokenName:       testData.tokenName,
						TokenTicker:     "",
						Decimals:        testData.decimals,
						MaxSupply:       testData.maxSupply,
						IsFreezable:     false,
					},
				}
			},
			expectedErr: "token ticker cannot be empty",
		},
		{
			name:   "nil max supply",
			txType: "create",
			baseTx: baseCreateTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenInputs = &tokenpb.TokenTransaction_CreateInput{
					CreateInput: &tokenpb.TokenCreateInput{
						IssuerPublicKey: testTokenPublicKey.Serialize(),
						TokenName:       testData.tokenName,
						TokenTicker:     testData.tokenTicker,
						Decimals:        testData.decimals,
						MaxSupply:       nil,
						IsFreezable:     false,
					},
				}
			},
			expectedErr: "max supply cannot be nil",
		},
		{
			name:   "max supply wrong length",
			txType: "create",
			baseTx: baseCreateTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenInputs = &tokenpb.TokenTransaction_CreateInput{
					CreateInput: &tokenpb.TokenCreateInput{
						IssuerPublicKey: testTokenPublicKey.Serialize(),
						TokenName:       testData.tokenName,
						TokenTicker:     testData.tokenTicker,
						Decimals:        testData.decimals,
						MaxSupply:       []byte{1, 2, 3, 4, 5}, // Too short
						IsFreezable:     false,
					},
				}
			},
			expectedErr: "max supply must be exactly 16 bytes",
		},
		{
			name:   "max supply too long",
			txType: "create",
			baseTx: baseCreateTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenInputs = &tokenpb.TokenTransaction_CreateInput{
					CreateInput: &tokenpb.TokenCreateInput{
						IssuerPublicKey: testTokenPublicKey.Serialize(),
						TokenName:       testData.tokenName,
						TokenTicker:     testData.tokenTicker,
						Decimals:        testData.decimals,
						MaxSupply:       bytes.Repeat([]byte{1}, 20), // Too long
						IsFreezable:     false,
					},
				}
			},
			expectedErr: "max supply must be exactly 16 bytes",
		},

		// Expiry time tests (for final hash)
		{
			name:   "nil expiry time for final hash",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.ExpiryTime = nil
			},
			expectedErr: "expiry time cannot be empty",
		},
		{
			name:   "nil revocation commitment for final hash",
			txType: "mint",
			baseTx: baseMintTransaction,
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenOutputs[0].RevocationCommitment = nil
			},
			expectedErr: "revocation public key at index 0 cannot be nil or empty",
		},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%s", tt.txType, tt.name), func(t *testing.T) {
			tx := proto.CloneOf(tt.baseTx)

			tt.modifyTx(tx)

			_, err := HashTokenTransactionV1(tx, false)
			if err == nil {
				t.Fatalf("expected error for %s, but got nil", tt.name)
			}
			if !strings.Contains(err.Error(), tt.expectedErr) {
				t.Errorf("unexpected error message for %s: got %v, want containing %q", tt.name, err, tt.expectedErr)
			}
		})
	}
}

func TestHashTokenTransactionV1PartialHashRequiredFields(t *testing.T) {
	// Create base valid transaction for partial hash testing
	baseTransaction := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: testTokenPublicKey.Serialize(),
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey: testIdentityPubKey.Serialize(),
				TokenPublicKey: testTokenPublicKey.Serialize(),
				TokenAmount:    testData.tokenAmount,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{testSparkOperatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
		ClientCreatedTimestamp:          timestamppb.New(time.UnixMilli(int64(testData.clientTimestamp))),
		// Note: ExpiryTime is intentionally nil for partial hash testing
	}

	tests := []struct {
		name        string
		modifyTx    func(*tokenpb.TokenTransaction)
		expectedErr string
		shouldPass  bool
	}{
		{
			name: "nil expiry time for partial hash should pass",
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.ExpiryTime = nil
			},
			expectedErr: "",
			shouldPass:  true,
		},
		{
			name: "nil client created timestamp for partial hash should fail",
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.ClientCreatedTimestamp = nil
			},
			expectedErr: "client created timestamp cannot be empty",
			shouldPass:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := proto.CloneOf(baseTransaction)

			tt.modifyTx(tx)

			_, err := HashTokenTransactionV1(tx, true) // true for partial hash
			if tt.shouldPass {
				if err != nil {
					t.Fatalf("expected no error for %s, but got: %v", tt.name, err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error for %s, but got nil", tt.name)
				}
				if !strings.Contains(err.Error(), tt.expectedErr) {
					t.Errorf("unexpected error message for %s: got %v, want containing %q", tt.name, err, tt.expectedErr)
				}
			}
		})
	}
}

func TestHashTokenTransactionVersioning(t *testing.T) {
	// Create a basic token transaction
	tokenTx := &tokenpb.TokenTransaction{
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: bytes.Repeat([]byte{0x01}, 33),
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				Id:                   &testData.leafID,
				RevocationCommitment: bytes.Repeat([]byte{0x05}, 33),
				OwnerPublicKey:       bytes.Repeat([]byte{0x02}, 33),
				TokenPublicKey:       bytes.Repeat([]byte{0x03}, 33),
				TokenAmount:          []byte{0x01},
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{bytes.Repeat([]byte{0x04}, 33)},
		Network:                         pb.Network_REGTEST,
		ClientCreatedTimestamp:          timestamppb.New(time.Unix(0, 0)),
		ExpiryTime:                      timestamppb.New(time.Unix(1000, 0)),
	}

	t.Run("version 0", func(t *testing.T) {
		tokenTx.Version = 0
		hash, err := HashTokenTransaction(tokenTx, false)
		if err != nil {
			t.Errorf("unexpected error for version 0: %v", err)
		}
		if len(hash) == 0 {
			t.Error("expected non-empty hash for version 0")
		}
	})

	t.Run("version 1", func(t *testing.T) {
		tokenTx.Version = 1
		hash, err := HashTokenTransaction(tokenTx, false)
		if err != nil {
			t.Errorf("unexpected error for version 1: %v", err)
		}
		if len(hash) == 0 {
			t.Error("expected non-empty hash for version 1")
		}
	})

	t.Run("nil transaction", func(t *testing.T) {
		_, err := HashTokenTransaction(nil, false)
		if err == nil {
			t.Error("expected error for nil transaction")
		}
		if !strings.Contains(err.Error(), "cannot be nil") {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}

func TestHashTokenTransactionProtoEquivalence(t *testing.T) {
	tokenTx, sparkTx := createTestTransactions()

	t.Run("full hash equivalence", func(t *testing.T) {
		tokenHash, err := HashTokenTransaction(tokenTx, false)
		if err != nil {
			t.Fatalf("failed to hash token transaction: %v", err)
		}

		sparkHash, err := HashTokenTransactionV0(sparkTx, false)
		if err != nil {
			t.Fatalf("failed to hash spark transaction: %v", err)
		}

		if !bytes.Equal(tokenHash, sparkHash) {
			t.Errorf("hash mismatch between proto types\ntoken hash: %x\nspark hash: %x", tokenHash, sparkHash)
		}
	})

	t.Run("partial hash equivalence", func(t *testing.T) {
		tokenHash, err := HashTokenTransaction(tokenTx, true)
		if err != nil {
			t.Fatalf("failed to hash token transaction (partial): %v", err)
		}

		sparkHash, err := HashTokenTransactionV0(sparkTx, true)
		if err != nil {
			t.Fatalf("failed to hash spark transaction (partial): %v", err)
		}

		if !bytes.Equal(tokenHash, sparkHash) {
			t.Errorf("hash mismatch between proto types\ntoken hash: %x\nspark hash: %x", tokenHash, sparkHash)
		}
	})
}

func TestHashTokenTransactionPartialVsFull(t *testing.T) {
	tokenTx, sparkTx := createTestTransactions()

	t.Run("token transaction partial vs full", func(t *testing.T) {
		fullHash, err := HashTokenTransaction(tokenTx, false)
		if err != nil {
			t.Fatalf("failed to hash token transaction (full): %v", err)
		}

		partialHash, err := HashTokenTransaction(tokenTx, true)
		if err != nil {
			t.Fatalf("failed to hash token transaction (partial): %v", err)
		}

		if bytes.Equal(fullHash, partialHash) {
			t.Error("full and partial hashes should be different for token transaction")
		}
	})

	t.Run("spark transaction partial vs full", func(t *testing.T) {
		fullHash, err := HashTokenTransactionV0(sparkTx, false)
		if err != nil {
			t.Fatalf("failed to hash spark transaction (full): %v", err)
		}

		partialHash, err := HashTokenTransactionV0(sparkTx, true)
		if err != nil {
			t.Fatalf("failed to hash spark transaction (partial): %v", err)
		}

		if bytes.Equal(fullHash, partialHash) {
			t.Error("full and partial hashes should be different for spark transaction")
		}
	})
}

func TestValidateOwnershipSignature(t *testing.T) {
	privKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
	pubKey := privKey.Public()
	messageHash := sha256.Sum256([]byte("test message"))
	schnorrSig, _ := schnorr.Sign(privKey.ToBTCEC(), messageHash[:])
	ecdsaSig := ecdsa.Sign(privKey.ToBTCEC(), messageHash[:])

	tests := []struct {
		name               string
		ownershipSignature []byte
		txHash             []byte
		ownerPublicKey     keys.Public
		wantErr            bool
	}{
		{
			name:               "valid Schnorr signature",
			ownershipSignature: schnorrSig.Serialize(),
			txHash:             messageHash[:],
			ownerPublicKey:     pubKey,
			wantErr:            false,
		},
		{
			name:               "valid ECDSA DER signature",
			ownershipSignature: ecdsaSig.Serialize(),
			txHash:             messageHash[:],
			ownerPublicKey:     pubKey,
			wantErr:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateOwnershipSignature(tt.ownershipSignature, tt.txHash, tt.ownerPublicKey)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateOwnershipSignature() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateOwnershipSignatureErrors(t *testing.T) {
	privKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
	pubKey := privKey.Public()
	messageHash := sha256.Sum256([]byte("test message"))
	validSig, _ := schnorr.Sign(privKey.ToBTCEC(), messageHash[:])

	tests := []struct {
		name               string
		ownershipSignature []byte
		txHash             []byte
		ownerPublicKey     keys.Public
		wantErr            string
	}{
		{
			name:               "nil signature",
			ownershipSignature: nil,
			txHash:             messageHash[:],
			ownerPublicKey:     pubKey,
			wantErr:            "ownership signature cannot be nil",
		},
		{
			name:               "nil transaction hash",
			ownershipSignature: validSig.Serialize(),
			txHash:             nil,
			ownerPublicKey:     pubKey,
			wantErr:            "hash to verify cannot be nil",
		},
		{
			name:               "empty owner public key",
			ownershipSignature: validSig.Serialize(),
			txHash:             messageHash[:],
			ownerPublicKey:     keys.Public{},
			wantErr:            "owner public key cannot be zero",
		},
		{
			name:               "invalid Schnorr signature",
			ownershipSignature: bytes.Repeat([]byte("1"), 64),
			txHash:             messageHash[:],
			ownerPublicKey:     pubKey,
			wantErr:            "failed to parse signature as either Schnorr or DER",
		},
		{
			name:               "too short Schnorr signature",
			ownershipSignature: []byte{0x01, 0x02, 0x03}, // Too short for a valid Schnorr signature
			txHash:             messageHash[:],
			ownerPublicKey:     pubKey,
			wantErr:            "malformed signature: too short",
		},
		{
			name:               "invalid ECDSA DER signature",
			ownershipSignature: []byte{0x30, 0x01, 0x02}, // Invalid DER format
			txHash:             messageHash[:],
			ownerPublicKey:     pubKey,
			wantErr:            "failed to parse signature as either Schnorr or DER",
		},
		{
			name:               "valid Schnorr with different tx hash falls through to ECDSA and fails",
			ownershipSignature: validSig.Serialize(),
			txHash:             []byte("different message hash"), // Different message hash will cause verification to fail
			ownerPublicKey:     pubKey,
			wantErr:            "failed to parse signature as either Schnorr or DER",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateOwnershipSignature(tt.ownershipSignature, tt.txHash, tt.ownerPublicKey); err == nil {
				t.Errorf("ValidateOwnershipSignature() expected error %v, got nil", tt.wantErr)
			} else if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidateOwnershipSignature() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsNetworkSupported(t *testing.T) {
	tests := []struct {
		name              string
		providedNetwork   common.Network
		supportedNetworks []common.Network
		want              bool
	}{
		{
			name:              "unspecified network",
			providedNetwork:   common.Unspecified,
			supportedNetworks: []common.Network{common.Mainnet, common.Testnet},
			want:              false,
		},
		{
			name:              "mainnet in list",
			providedNetwork:   common.Mainnet,
			supportedNetworks: []common.Network{common.Mainnet, common.Testnet},
			want:              true,
		},
		{
			name:              "testnet in list",
			providedNetwork:   common.Testnet,
			supportedNetworks: []common.Network{common.Mainnet, common.Testnet},
			want:              true,
		},
		{
			name:              "regtest in list",
			providedNetwork:   common.Regtest,
			supportedNetworks: []common.Network{common.Regtest},
			want:              true,
		},
		{
			name:              "network not in list",
			providedNetwork:   common.Signet,
			supportedNetworks: []common.Network{common.Mainnet, common.Testnet},
			want:              false,
		},
		{
			name:              "empty supported list",
			providedNetwork:   common.Mainnet,
			supportedNetworks: []common.Network{},
			want:              false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isNetworkSupported(tt.providedNetwork, tt.supportedNetworks)
			if got != tt.want {
				t.Errorf("isNetworkSupported(%v, %v) = %v, want %v", tt.providedNetwork, tt.supportedNetworks, got, tt.want)
			}
		})
	}
}

func TestValidateRevocationKeys(t *testing.T) {
	t.Parallel()
	privKey1 := keys.MustGeneratePrivateKeyFromRand(seededRng)
	privKey2 := keys.MustGeneratePrivateKeyFromRand(seededRng)
	privateKeys := []keys.Private{privKey1, privKey2}
	publicKeys := []keys.Public{privKey1.Public(), privKey2.Public()}

	require.NoError(t, ValidateRevocationKeys(privateKeys, publicKeys))
}

func TestValidateRevocationKeysErrors(t *testing.T) {
	t.Parallel()
	privKey1 := keys.MustGeneratePrivateKeyFromRand(seededRng)
	privKey2 := keys.MustGeneratePrivateKeyFromRand(seededRng)
	// Generate a mismatched key pair
	wrongPrivKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
	wrongPubKey := wrongPrivKey.Public()

	tests := []struct {
		name               string
		privateKeys        []keys.Private
		expectedPublicKeys []keys.Public
		errMsg             string
	}{
		{
			name:               "nil private keys",
			privateKeys:        nil,
			expectedPublicKeys: []keys.Public{privKey1.Public()},
			errMsg:             "revocation private keys cannot be nil",
		},
		{
			name:               "nil expected public keys",
			privateKeys:        []keys.Private{privKey1},
			expectedPublicKeys: nil,
			errMsg:             "expected revocation public keys cannot be nil",
		},
		{
			name:               "mismatched lengths",
			privateKeys:        []keys.Private{privKey1},
			expectedPublicKeys: []keys.Public{privKey1.Public(), privKey2.Public()},
			errMsg:             "number of revocation private keys (1) does not match number of expected public keys (2)",
		},
		{
			name:               "nil private key at index",
			privateKeys:        []keys.Private{privKey1, {}},
			expectedPublicKeys: []keys.Public{privKey1.Public(), privKey2.Public()},
			errMsg:             "revocation private key at index 1 cannot be empty",
		},
		{
			name:               "nil expected public key at index",
			privateKeys:        []keys.Private{privKey1, privKey2},
			expectedPublicKeys: []keys.Public{privKey1.Public(), {}},
			errMsg:             "expected revocation public key at index 1 cannot be empty",
		},
		{
			name:               "key mismatch",
			privateKeys:        []keys.Private{privKey1, privKey2},
			expectedPublicKeys: []keys.Public{privKey1.Public(), wrongPubKey},
			errMsg:             "revocation key mismatch at index 1: derived public key does not match expected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.ErrorContains(t, ValidateRevocationKeys(tt.privateKeys, tt.expectedPublicKeys), tt.errMsg)
		})
	}
}

func TestHashFreezeTokensPayloadErrors(t *testing.T) {
	t.Parallel()

	ownerPrivKey, _ := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	tokenPrivKey, _ := keys.GeneratePrivateKey()
	tokenPubKey := tokenPrivKey.Public()
	operatorPrivKey, _ := keys.GeneratePrivateKey()
	operatorPubKey := operatorPrivKey.Public()
	tokenIdentifier := make([]byte, 32)
	copy(tokenIdentifier, "test_token_identifier_32bytes___")

	tests := []struct {
		name    string
		payload *tokenpb.FreezeTokensPayload
		wantErr string
	}{
		{
			name:    "nil payload",
			payload: nil,
			wantErr: "freeze tokens payload cannot be nil",
		},
		{
			name: "empty owner public key v0",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            []byte{},
				TokenPublicKey:            tokenPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			wantErr: "owner public key cannot be empty",
		},
		{
			name: "empty token public key v0",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            []byte{},
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			wantErr: "token public key cannot be empty",
		},
		{
			name: "zero timestamp v0",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            tokenPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   0,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			wantErr: "issuer provided timestamp cannot be 0",
		},
		{
			name: "empty operator public key v0",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            tokenPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: []byte{},
			},
			wantErr: "operator identity public key cannot be empty",
		},
		{
			name: "empty owner public key v1",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   1,
				OwnerPublicKey:            []byte{},
				TokenIdentifier:           tokenIdentifier,
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			wantErr: "owner public key cannot be empty",
		},
		{
			name: "missing token identifier v1",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   1,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			wantErr: "token identifier cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := HashFreezeTokensPayload(tt.payload)
			if err == nil {
				t.Errorf("HashFreezeTokensPayload() expected error %v, got nil", tt.wantErr)
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("HashFreezeTokensPayload() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestHashFreezeTokensPayloadParameterChanges(t *testing.T) {
	t.Parallel()
	ownerPrivKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
	ownerPubKey := ownerPrivKey.Public()
	tokenPrivKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
	tokenPubKey := tokenPrivKey.Public()
	operatorPrivKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
	operatorPubKey := operatorPrivKey.Public()
	tokenIdentifier := make([]byte, 32)
	copy(tokenIdentifier, "test_token_identifier_32bytes___")

	ownerPrivKey2 := keys.MustGeneratePrivateKeyFromRand(seededRng)
	ownerPubKey2 := ownerPrivKey2.Public()
	tokenPrivKey2 := keys.MustGeneratePrivateKeyFromRand(seededRng)
	tokenPubKey2 := tokenPrivKey2.Public()
	operatorPrivKey2 := keys.MustGeneratePrivateKeyFromRand(seededRng)
	operatorPubKey2 := operatorPrivKey2.Public()
	tokenIdentifier2 := make([]byte, 32)
	copy(tokenIdentifier2, "different_token_id_32bytes______")

	// Test version 0 base payload with valid values
	basePayloadV0 := &tokenpb.FreezeTokensPayload{
		Version:                   0,
		OwnerPublicKey:            ownerPubKey.Serialize(),
		TokenPublicKey:            tokenPubKey.Serialize(),
		ShouldUnfreeze:            false,
		IssuerProvidedTimestamp:   1234567890,
		OperatorIdentityPublicKey: operatorPubKey.Serialize(),
	}
	baseHashV0, _ := HashFreezeTokensPayload(basePayloadV0)

	// Test version 1 base payload with valid values
	basePayloadV1 := &tokenpb.FreezeTokensPayload{
		Version:                   1,
		OwnerPublicKey:            ownerPubKey.Serialize(),
		TokenIdentifier:           tokenIdentifier,
		ShouldUnfreeze:            false,
		IssuerProvidedTimestamp:   1234567890,
		OperatorIdentityPublicKey: operatorPubKey.Serialize(),
	}
	baseHashV1, _ := HashFreezeTokensPayload(basePayloadV1)

	// Ensure v0 and v1 produce different hashes
	if bytes.Equal(baseHashV0, baseHashV1) {
		t.Fatal("Version 0 and version 1 should produce different hashes")
	}

	tests := []struct {
		name     string
		payload  *tokenpb.FreezeTokensPayload
		baseHash []byte
	}{
		// Version 0 tests
		{
			name: "v0 different owner public key",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey2.Serialize(),
				TokenPublicKey:            tokenPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			baseHash: baseHashV0,
		},
		{
			name: "v0 different token public key",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            tokenPubKey2.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			baseHash: baseHashV0,
		},
		{
			name: "v0 different shouldUnfreeze",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            tokenPubKey.Serialize(),
				ShouldUnfreeze:            true,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			baseHash: baseHashV0,
		},
		{
			name: "v0 different timestamp",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            tokenPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   9876543210,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			baseHash: baseHashV0,
		},
		{
			name: "v0 different operator public key",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            tokenPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey2.Serialize(),
			},
			baseHash: baseHashV0,
		},
		// Version 1 tests
		{
			name: "v1 different owner public key",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   1,
				OwnerPublicKey:            ownerPubKey2.Serialize(),
				TokenIdentifier:           tokenIdentifier,
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			baseHash: baseHashV1,
		},
		{
			name: "v1 different token identifier",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   1,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenIdentifier:           tokenIdentifier2,
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			baseHash: baseHashV1,
		},
		{
			name: "v1 different shouldUnfreeze",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   1,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenIdentifier:           tokenIdentifier,
				ShouldUnfreeze:            true,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			baseHash: baseHashV1,
		},
		{
			name: "v1 different timestamp",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   1,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenIdentifier:           tokenIdentifier,
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   9876543210,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			baseHash: baseHashV1,
		},
		{
			name: "v1 different operator public key",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   1,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenIdentifier:           tokenIdentifier,
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey2.Serialize(),
			},
			baseHash: baseHashV1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hash, err := HashFreezeTokensPayload(tt.payload)
			if err != nil {
				t.Fatalf("HashFreezeTokensPayload() error = %v", err)
			}
			if len(hash) != 32 {
				t.Errorf("HashFreezeTokensPayload() hash length = %v, want 32", len(hash))
			}
			if bytes.Equal(hash, tt.baseHash) {
				t.Fatalf("HashFreezeTokensPayload() produced same hash as base for %s", tt.name)
			}
		})
	}
}

func TestValidateFreezeTokensPayload(t *testing.T) {
	t.Parallel()

	ownerPrivKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
	ownerPubKey := ownerPrivKey.Public()
	tokenPrivKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
	tokenPubKey := tokenPrivKey.Public()
	operatorPrivKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
	operatorPubKey := operatorPrivKey.Public()
	tokenIdentifier := make([]byte, 32)
	copy(tokenIdentifier, "test_token_identifier_32bytes___")

	validPayloadV0 := &tokenpb.FreezeTokensPayload{
		Version:                   0,
		OwnerPublicKey:            ownerPubKey.Serialize(),
		TokenPublicKey:            tokenPubKey.Serialize(),
		ShouldUnfreeze:            false,
		IssuerProvidedTimestamp:   1234567890,
		OperatorIdentityPublicKey: operatorPubKey.Serialize(),
	}

	validPayloadV1 := &tokenpb.FreezeTokensPayload{
		Version:                   1,
		OwnerPublicKey:            ownerPubKey.Serialize(),
		TokenIdentifier:           tokenIdentifier,
		ShouldUnfreeze:            false,
		IssuerProvidedTimestamp:   1234567890,
		OperatorIdentityPublicKey: operatorPubKey.Serialize(),
	}

	tests := []struct {
		name                string
		payload             *tokenpb.FreezeTokensPayload
		expectedOperatorKey keys.Public
		wantErr             string
	}{
		{
			name:                "nil payload",
			payload:             nil,
			expectedOperatorKey: operatorPubKey,
			wantErr:             "freeze tokens payload cannot be nil",
		},
		{
			name: "invalid version",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   2,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            tokenPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			expectedOperatorKey: operatorPubKey,
			wantErr:             "invalid freeze tokens payload version: 2",
		},
		{
			name: "v0 empty owner public key",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            []byte{},
				TokenPublicKey:            tokenPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			expectedOperatorKey: operatorPubKey,
			wantErr:             "owner public key cannot be empty",
		},
		{
			name: "v0 nil token public key",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			expectedOperatorKey: operatorPubKey,
			wantErr:             "token public key cannot be nil for version 0",
		},
		{
			name: "v0 with token identifier (should fail)",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            tokenPubKey.Serialize(),
				TokenIdentifier:           tokenIdentifier,
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			expectedOperatorKey: operatorPubKey,
			wantErr:             "token identifier must be nil for version 0",
		},
		{
			name: "v1 nil token identifier",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   1,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			expectedOperatorKey: operatorPubKey,
			wantErr:             "token identifier must be exactly 32 bytes, got 0",
		},
		{
			name: "v1 with token public key (should fail)",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   1,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            tokenPubKey.Serialize(),
				TokenIdentifier:           tokenIdentifier,
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			expectedOperatorKey: operatorPubKey,
			wantErr:             "token public key must be nil for version 1",
		},
		{
			name: "v1 wrong token identifier length",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   1,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenIdentifier:           []byte("short"),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			expectedOperatorKey: operatorPubKey,
			wantErr:             "token identifier must be exactly 32 bytes, got 5",
		},
		{
			name: "zero timestamp",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            tokenPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   0,
				OperatorIdentityPublicKey: operatorPubKey.Serialize(),
			},
			expectedOperatorKey: operatorPubKey,
			wantErr:             "issuer provided timestamp cannot be 0",
		},
		{
			name: "empty operator public key",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            tokenPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: []byte{},
			},
			expectedOperatorKey: operatorPubKey,
			wantErr:             "failed to parse operator identity public key",
		},
		{
			name: "operator public key not in config",
			payload: &tokenpb.FreezeTokensPayload{
				Version:                   0,
				OwnerPublicKey:            ownerPubKey.Serialize(),
				TokenPublicKey:            tokenPubKey.Serialize(),
				ShouldUnfreeze:            false,
				IssuerProvidedTimestamp:   1234567890,
				OperatorIdentityPublicKey: []byte{0x03, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x11, 0x22},
			},
			expectedOperatorKey: operatorPubKey, // Different from the payload's operator key
			wantErr:             "does not match expected operator",
		},
		{
			name:                "valid v0 payload with matching operator",
			payload:             validPayloadV0,
			expectedOperatorKey: operatorPubKey,
			wantErr:             "",
		},
		{
			name:                "valid v1 payload with matching operator",
			payload:             validPayloadV1,
			expectedOperatorKey: operatorPubKey,
			wantErr:             "",
		},
		{
			name:                "valid payload with nil expected operator (should fail)",
			payload:             validPayloadV0,
			expectedOperatorKey: keys.Public{},
			wantErr:             "does not match expected operator",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateFreezeTokensPayload(tt.payload, tt.expectedOperatorKey)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestHashFreezeTokensPayloadVersionConsistency(t *testing.T) {
	t.Parallel()

	ownerPrivKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
	ownerPubKey := ownerPrivKey.Public()
	tokenPrivKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
	tokenPubKey := tokenPrivKey.Public()
	operatorPrivKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
	operatorPubKey := operatorPrivKey.Public()
	tokenIdentifier := make([]byte, 32)
	copy(tokenIdentifier, "test_token_identifier_32bytes___")

	// Create a v0 payload
	payloadV0 := &tokenpb.FreezeTokensPayload{
		Version:                   0,
		OwnerPublicKey:            ownerPubKey.Serialize(),
		TokenPublicKey:            tokenPubKey.Serialize(),
		ShouldUnfreeze:            false,
		IssuerProvidedTimestamp:   1234567890,
		OperatorIdentityPublicKey: operatorPubKey.Serialize(),
	}

	// Create a v1 payload
	payloadV1 := &tokenpb.FreezeTokensPayload{
		Version:                   1,
		OwnerPublicKey:            ownerPubKey.Serialize(),
		TokenIdentifier:           tokenIdentifier,
		ShouldUnfreeze:            false,
		IssuerProvidedTimestamp:   1234567890,
		OperatorIdentityPublicKey: operatorPubKey.Serialize(),
	}

	hashV0, err := HashFreezeTokensPayload(payloadV0)
	if err != nil {
		t.Fatalf("HashFreezeTokensPayload() v0 error = %v", err)
	}

	hashV1, err := HashFreezeTokensPayload(payloadV1)
	if err != nil {
		t.Fatalf("HashFreezeTokensPayload() v1 error = %v", err)
	}

	// Verify different versions produce different hashes
	if bytes.Equal(hashV0, hashV1) {
		t.Error("Version 0 and version 1 payloads should produce different hashes")
	}

	// Verify hash consistency - same input should always produce same hash
	hashV0Again, err := HashFreezeTokensPayload(payloadV0)
	if err != nil {
		t.Fatalf("HashFreezeTokensPayload() v0 second hash error = %v", err)
	}

	if !bytes.Equal(hashV0, hashV0Again) {
		t.Error("Version 0 payload should produce consistent hashes")
	}

	hashV1Again, err := HashFreezeTokensPayload(payloadV1)
	if err != nil {
		t.Fatalf("HashFreezeTokensPayload() v1 second hash error = %v", err)
	}

	if !bytes.Equal(hashV1, hashV1Again) {
		t.Error("Version 1 payload should produce consistent hashes")
	}
}

func TestHashTokenTransactionV2UniqueHash(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	key1 := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	key2 := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	key4 := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	key5 := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	key6 := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	operatorKeys := [][]byte{
		key4.Serialize(),
		key5.Serialize(),
		key6.Serialize(),
	}
	baseTransaction := &tokenpb.TokenTransaction{
		Version: 2,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: key1.Serialize(),
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey: key1.Serialize(),
				TokenPublicKey: key2.Serialize(),
				TokenAmount:    []byte{0x01},
			},
		},
		SparkOperatorIdentityPublicKeys: operatorKeys,
		ClientCreatedTimestamp:          timestamppb.New(time.Unix(100, 0)),
		InvoiceAttachments:              []*tokenpb.InvoiceAttachment{},
	}

	txWithOneAttachment := proto.CloneOf(baseTransaction)
	txWithOneAttachment.InvoiceAttachments = []*tokenpb.InvoiceAttachment{
		testData.invoiceAttachments[0],
	}

	txWithTwoAttachments := proto.CloneOf(baseTransaction)
	txWithTwoAttachments.InvoiceAttachments = []*tokenpb.InvoiceAttachment{
		testData.invoiceAttachments[0],
		testData.invoiceAttachments[1],
	}

	txWithTwoAttachmentsReordered := proto.CloneOf(baseTransaction)
	txWithTwoAttachmentsReordered.InvoiceAttachments = []*tokenpb.InvoiceAttachment{
		testData.invoiceAttachments[1],
		testData.invoiceAttachments[0],
	}

	txWithDifferentTimestamp := proto.CloneOf(baseTransaction)
	txWithDifferentTimestamp.ClientCreatedTimestamp = timestamppb.New(time.Unix(200, 0))

	txWithEmptyInvoiceString := proto.CloneOf(baseTransaction)
	txWithEmptyInvoiceString.InvoiceAttachments = []*tokenpb.InvoiceAttachment{{SparkInvoice: ""}}
	txWithInvalidInvoiceString := proto.CloneOf(baseTransaction)
	txWithInvalidInvoiceString.InvoiceAttachments = []*tokenpb.InvoiceAttachment{{SparkInvoice: "invalid"}}
	txWithNilAttachment := proto.CloneOf(baseTransaction)
	txWithNilAttachment.InvoiceAttachments = []*tokenpb.InvoiceAttachment{nil}

	// Hash all transactions
	baseHash, err := HashTokenTransactionV2(baseTransaction, true)
	require.NoError(t, err)
	hashWithOne, err := HashTokenTransactionV2(txWithOneAttachment, true)
	require.NoError(t, err)
	hashWithTwo, err := HashTokenTransactionV2(txWithTwoAttachments, true)
	require.NoError(t, err)
	hashWithTwoReordered, err := HashTokenTransactionV2(txWithTwoAttachmentsReordered, true)
	require.NoError(t, err)
	hashWithDifferentTimestamp, err := HashTokenTransactionV2(txWithDifferentTimestamp, true)
	require.NoError(t, err)
	_, err = HashTokenTransactionV2(txWithEmptyInvoiceString, true)
	require.Error(t, err, "expected error for empty invoice string")
	_, err = HashTokenTransactionV2(txWithInvalidInvoiceString, true)
	require.Error(t, err, "expected error for invalid invoice string")
	_, err = HashTokenTransactionV2(txWithNilAttachment, true)
	require.Error(t, err, "expected error for nil attachment")
	// Verify that reordering the attachments produces the same hash: deterministic sorting by UUID
	require.Equal(t, hashWithTwo, hashWithTwoReordered, "reordered attachments should produce the same hash")
	// Create map to check for duplicates
	hashes := map[string]string{
		"base":               hex.EncodeToString(baseHash),
		"oneAttachment":      hex.EncodeToString(hashWithOne),
		"twoAttachments":     hex.EncodeToString(hashWithTwo),
		"differentTimestamp": hex.EncodeToString(hashWithDifferentTimestamp),
	}
	// Check that all hashes are unique
	seen := make(map[string]bool)
	for name, hash := range hashes {
		if seen[hash] {
			t.Errorf("duplicate hash detected for %s. Hash: %s", name, hash)
		}
		seen[hash] = true
	}
}

func TestHashTokenTransactionTransferV2(t *testing.T) {
	transferTransaction := &tokenpb.TokenTransaction{
		Version: 2,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: []*tokenpb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: testData.prevTxHash[:],
						PrevTokenTransactionVout: 0,
					},
				},
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				Id:                            &testData.leafID,
				OwnerPublicKey:                testIdentityPubKey.Serialize(),
				TokenPublicKey:                testTokenPublicKey.Serialize(),
				TokenAmount:                   testData.tokenAmount,
				RevocationCommitment:          testData.revocationPubKey.Serialize(),
				WithdrawBondSats:              &testData.bondSats,
				WithdrawRelativeBlockLocktime: &testData.locktime,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{testSparkOperatorPubKey.Serialize()},
		Network:                         pb.Network_REGTEST,
		ExpiryTime:                      timestamppb.New(time.UnixMilli(int64(testData.expiryTime))),
		ClientCreatedTimestamp:          timestamppb.New(time.UnixMilli(int64(testData.clientTimestamp))),
		InvoiceAttachments:              testData.invoiceAttachments,
	}
	hash, err := HashTokenTransactionV2(transferTransaction, false)
	require.NoError(t, err)
	want := []byte{
		0xb0, 0x98, 0xdc, 0x22, 0x8a, 0xd, 0x82, 0x64, 0x25, 0x4a, 0x2d, 0xef, 0x34, 0x42, 0x5c, 0xab,
		0xe2, 0x23, 0xd, 0x4f, 0x7b, 0xa4, 0x3c, 0xf2, 0xa3, 0x2c, 0x27, 0xf0, 0x31, 0xae, 0x8, 0x83,
	}
	assert.Equal(t, want, hash)
}
