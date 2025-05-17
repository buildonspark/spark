package grpctest

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/utils"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/lightsparkdev/spark/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// Test token amounts for various operations
const (
	// The expected maximum number of outputs which can be created in a single transaction.
	ManyOutputsCount = 100
	// Amount for first created output in issuance transaction
	TestIssueOutput1Amount = 11111
	// Amount for second created output in issuance transaction
	TestIssueOutput2Amount = 22222
	// Amount for second created output in multiple output issuance transaction
	TestIssueMultiplePerOutputAmount = 1000
	// Amount for first (and only) created output in transfer transaction
	TestTransferOutput1Amount = 33333
	// Configured at SO level. We validate in the tests to ensure these are populated correctly.
	WithdrawalBondSatsInConfig              = 10000
	WithdrawalRelativeBlockLocktimeInConfig = 1000
	MinikubeTokenTransactionExpiryTimeSecs  = 60
)

type PrederivedIdentityPrivateKeyFromMnemonic struct {
	IdentityPrivateKeyHex string
}

func (k *PrederivedIdentityPrivateKeyFromMnemonic) IdentityPrivateKey() *secp256k1.PrivateKey {
	privKeyBytes, err := hex.DecodeString(k.IdentityPrivateKeyHex)
	if err != nil {
		panic("invalid issuer private key hex")
	}
	return secp256k1.PrivKeyFromBytes(privKeyBytes)
}

var staticLocalIssuerKey = PrederivedIdentityPrivateKeyFromMnemonic{
	// Mnemonic:           "table apology decrease custom deny client retire genius uniform find eager fish",
	// TokenL1Address:     "bcrt1q2mgym77n8ta8gn48xtusyrd6wr5uhecajyshku",
	IdentityPrivateKeyHex: "515c86ccb09faa2235acd0e287381bf286b37002328a8cc3c3b89738ab59dc93",
}

func bytesToBigInt(value []byte) *big.Int {
	return new(big.Int).SetBytes(value)
}

func uint64ToBigInt(value uint64) *big.Int {
	return new(big.Int).SetBytes(int64ToUint128Bytes(0, value))
}

func int64ToUint128Bytes(high, low uint64) []byte {
	return append(
		binary.BigEndian.AppendUint64(make([]byte, 0), high),
		binary.BigEndian.AppendUint64(make([]byte, 0), low)...,
	)
}

func getSigningOperatorPublicKeys(config *wallet.Config) [][]byte {
	var publicKeys [][]byte
	for _, operator := range config.SigningOperators {
		publicKeys = append(publicKeys, operator.IdentityPublicKey)
	}
	return publicKeys
}

func createTestTokenMintTransaction(config *wallet.Config,
	tokenIdentityPubKeyBytes []byte,
) (*pb.TokenTransaction, *secp256k1.PrivateKey, *secp256k1.PrivateKey, error) {
	// Generate two user output key pairs
	userOutput1PrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, nil, err
	}
	userOutput1PubKeyBytes := userOutput1PrivKey.PubKey().SerializeCompressed()

	userOutput2PrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, nil, err
	}
	userOutput2PubKeyBytes := userOutput2PrivKey.PubKey().SerializeCompressed()

	// Create the issuance transaction
	issueTokenTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_MintInput{
			MintInput: &pb.TokenMintInput{
				IssuerPublicKey:         tokenIdentityPubKeyBytes,
				IssuerProvidedTimestamp: uint64(time.Now().UnixMilli()),
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				OwnerPublicKey: userOutput1PubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestIssueOutput1Amount),
			},
			{
				OwnerPublicKey: userOutput2PubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestIssueOutput2Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	return issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, nil
}

func createTestTokenTransferTransaction(
	config *wallet.Config,
	finalIssueTokenTransactionHash []byte,
	tokenIdentityPubKeyBytes []byte,
) (*pb.TokenTransaction, *secp256k1.PrivateKey, error) {
	userOutput3PrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, err
	}
	userOutput3PubKeyBytes := userOutput3PrivKey.PubKey().SerializeCompressed()

	transferTokenTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TokenTransferInput{
				OutputsToSpend: []*pb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: finalIssueTokenTransactionHash,
						PrevTokenTransactionVout: 0,
					},
					{
						PrevTokenTransactionHash: finalIssueTokenTransactionHash,
						PrevTokenTransactionVout: 1,
					},
				},
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				OwnerPublicKey: userOutput3PubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestTransferOutput1Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	return transferTokenTransaction, userOutput3PrivKey, nil
}

func createTestTokenMintTransactionWithMultipleTokenOutputs(config *wallet.Config,
	tokenIdentityPubKeyBytes []byte, numOutputs int,
) (*pb.TokenTransaction, []*secp256k1.PrivateKey, error) {
	userOutputPrivKeys := make([]*secp256k1.PrivateKey, numOutputs)
	outputOutputs := make([]*pb.TokenOutput, numOutputs)

	for i := 0; i < numOutputs; i++ {
		privKey, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			return nil, nil, err
		}
		userOutputPrivKeys[i] = privKey
		pubKeyBytes := privKey.PubKey().SerializeCompressed()

		outputOutputs[i] = &pb.TokenOutput{
			OwnerPublicKey: pubKeyBytes,
			TokenPublicKey: tokenIdentityPubKeyBytes,
			TokenAmount:    int64ToUint128Bytes(0, TestIssueMultiplePerOutputAmount),
		}
	}

	issueTokenTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_MintInput{
			MintInput: &pb.TokenMintInput{
				IssuerPublicKey:         tokenIdentityPubKeyBytes,
				IssuerProvidedTimestamp: uint64(time.Now().UnixMilli()),
			},
		},
		TokenOutputs:                    outputOutputs,
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	return issueTokenTransaction, userOutputPrivKeys, nil
}

// OperatorKeysSplit contains two groups of operator public keys
type OperatorKeysSplit struct {
	FirstHalf  []wallet.SerializedPublicKey
	SecondHalf []wallet.SerializedPublicKey
}

// splitOperatorIdentityPublicKeys splits the operators from the config into two approximately equal groups
func splitOperatorIdentityPublicKeys(config *wallet.Config) OperatorKeysSplit {
	publicKeys := make([]wallet.SerializedPublicKey, 0, len(config.SigningOperators))
	for _, operator := range config.SigningOperators {
		publicKeys = append(publicKeys, wallet.SerializedPublicKey(operator.IdentityPublicKey))
	}

	halfOperatorCount := len(config.SigningOperators) / 2

	return OperatorKeysSplit{
		FirstHalf:  publicKeys[:halfOperatorCount],
		SecondHalf: publicKeys[halfOperatorCount:],
	}
}

// skipIfGithubActions skips the test if running in GitHub Actions
func skipIfGithubActions(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Skip("Skipping test on GitHub Actions CI")
	}
}

func TestQueryPartiallySpentTokenOutputsNotReturned(t *testing.T) {
	config, err := testutil.TestWalletConfigWithIdentityKey(*staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubkeyBytes := tokenPrivKey.PubKey().SerializeCompressed()

	// Create the issuance transaction
	mintTokenTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_MintInput{
			MintInput: &pb.TokenMintInput{
				IssuerPublicKey:         tokenIdentityPubkeyBytes,
				IssuerProvidedTimestamp: uint64(time.Now().UnixMilli()),
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				OwnerPublicKey: tokenIdentityPubkeyBytes,
				TokenPublicKey: tokenIdentityPubkeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestIssueOutput1Amount),
			},
			{
				OwnerPublicKey: tokenIdentityPubkeyBytes,
				TokenPublicKey: tokenIdentityPubkeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestIssueOutput2Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	ownerPrivateKeys := []*secp256k1.PrivateKey{&tokenPrivKey}

	broadcastMintResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, mintTokenTransaction, ownerPrivateKeys, nil,
	)
	require.NoError(t, err, "failed to start token transaction: %v", err)

	mintTxHash, err := utils.HashTokenTransaction(broadcastMintResponse, false)
	require.NoError(t, err, "failed to hash token transaction: %v", err)

	receiverPrivateKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err, "failed to generate receiver private key: %v", err)
	receiverPubKeyBytes := receiverPrivateKey.PubKey().SerializeCompressed()

	transferTokenTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TokenTransferInput{
				OutputsToSpend: []*pb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: mintTxHash,
						PrevTokenTransactionVout: 0,
					},
				},
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				OwnerPublicKey: receiverPubKeyBytes,
				TokenPublicKey: tokenIdentityPubkeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestIssueOutput1Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	transferTxResp, _, transferTxHash, err := wallet.StartTokenTransaction(
		context.Background(),
		config,
		transferTokenTransaction,
		ownerPrivateKeys,
		nil,
	)
	require.NoError(t, err, "failed to start token transaction: %v", err)

	_, _, err = wallet.SignTokenTransaction(
		context.Background(),
		config,
		transferTxResp.FinalTokenTransaction,
		transferTxHash,
		splitOperatorIdentityPublicKeys(config).SecondHalf,
		ownerPrivateKeys,
		nil,
	)
	require.NoError(t, err, "failed to sign token transaction: %v", err)

	// Query the coordinator for the above spent output
	notEnoughSignedOutput, err := wallet.QueryTokenOutputs(
		context.Background(),
		config,
		[]wallet.SerializedPublicKey{tokenIdentityPubkeyBytes},
		nil,
	)
	require.NoError(t, err, "failed to query token on not enough signatures")

	require.Equal(t, 1, len(notEnoughSignedOutput.OutputsWithPreviousTransactionData), "expected one output when using not enough signatures to transfer one of two outputs")
	require.Equal(t, uint64ToBigInt(TestIssueOutput2Amount), bytesToBigInt(notEnoughSignedOutput.OutputsWithPreviousTransactionData[0].Output.TokenAmount), "expected the second output to be returned when using not enough signatures to transfer one of two outputs")
}

func TestBroadcastTokenTransactionMintAndTransferTokens(t *testing.T) {
	skipIfGithubActions(t)
	config, err := testutil.TestWalletConfigWithIdentityKey(*staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[]wallet.SerializedPublicKey{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %v", finalIssueTokenTransaction)

	// Validate withdrawal params match config
	for i, output := range finalIssueTokenTransaction.TokenOutputs {
		if output.GetWithdrawBondSats() != WithdrawalBondSatsInConfig {
			t.Errorf("output %d: expected withdrawal bond sats 10000, got %d", i, output.GetWithdrawBondSats())
		}
		if output.GetWithdrawRelativeBlockLocktime() != uint64(WithdrawalRelativeBlockLocktimeInConfig) {
			t.Errorf("output %d: expected withdrawal relative block locktime 1000, got %d", i, output.GetWithdrawRelativeBlockLocktime())
		}
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance token transaction: %v", err)
	}
	transferTokenTransaction, userOutput3PrivKey, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	userOutput3PubKeyBytes := userOutput3PrivKey.PubKey().SerializeCompressed()

	revPubKey1 := finalIssueTokenTransaction.TokenOutputs[0].RevocationCommitment
	revPubKey2 := finalIssueTokenTransaction.TokenOutputs[1].RevocationCommitment

	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userOutput1PrivKey, userOutput2PrivKey},
		[]wallet.SerializedPublicKey{revPubKey1, revPubKey2},
	)
	if err != nil {
		t.Fatalf("failed to broadcast transfer token transaction: %v", err)
	}
	log.Printf("transfer broadcast finalized token transaction: %v", transferTokenTransactionResponse)

	// Query token transactions with pagination - first page
	tokenTransactionsPage1, err := wallet.QueryTokenTransactions(
		context.Background(),
		config,
		[]wallet.SerializedPublicKey{tokenIdentityPubKeyBytes}, // token public key
		nil, // owner public keys
		nil, // output IDs
		nil, // transaction hashes
		0,   // offset
		1,   // limit - only get 1 transaction
	)
	if err != nil {
		t.Fatalf("failed to query token transactions page 1: %v", err)
	}

	// Verify we got exactly 1 transaction
	if len(tokenTransactionsPage1.TokenTransactionsWithStatus) != 1 {
		t.Fatalf("expected 1 token transaction in page 1, got %d", len(tokenTransactionsPage1.TokenTransactionsWithStatus))
	}

	// Verify the offset is 1 (indicating there are more results)
	if tokenTransactionsPage1.Offset != 1 {
		t.Fatalf("expected next offset 1 for page 1, got %d", tokenTransactionsPage1.Offset)
	}

	// First transaction should be the transfer (reverse chronological)
	transferTx := tokenTransactionsPage1.TokenTransactionsWithStatus[0].TokenTransaction
	if transferTx.GetTransferInput() == nil {
		t.Fatal("first transaction should be a transfer transaction")
	}

	// Query token transactions with pagination - second page
	tokenTransactionsPage2, err := wallet.QueryTokenTransactions(
		context.Background(),
		config,
		[]wallet.SerializedPublicKey{tokenIdentityPubKeyBytes}, // token public key
		nil,                           // owner public keys
		nil,                           // output IDs
		nil,                           // transaction hashes
		tokenTransactionsPage1.Offset, // offset - use the offset from previous response (1)
		1,                             // limit - only get 1 transaction
	)
	if err != nil {
		t.Fatalf("failed to query token transactions page 2: %v", err)
	}

	// Verify we got exactly 1 transaction
	if len(tokenTransactionsPage2.TokenTransactionsWithStatus) != 1 {
		t.Fatalf("expected 1 token transaction in page 2, got %d", len(tokenTransactionsPage2.TokenTransactionsWithStatus))
	}

	// Verify the offset is 2 (indicating there are more results)
	if tokenTransactionsPage2.Offset != 2 {
		t.Fatalf("expected next offset 2 for page 2, got %d", tokenTransactionsPage2.Offset)
	}

	// Second transaction should be the mint (reverse chronological)
	mintTx := tokenTransactionsPage2.TokenTransactionsWithStatus[0].TokenTransaction
	if mintTx.GetMintInput() == nil {
		t.Fatal("second transaction should be a mint transaction")
	}
	if !bytes.Equal(mintTx.GetMintInput().GetIssuerPublicKey(), tokenIdentityPubKeyBytes) {
		t.Fatal("mint transaction issuer public key does not match expected")
	}

	// Query token transactions with pagination - third page (should be empty)
	tokenTransactionsPage3, err := wallet.QueryTokenTransactions(
		context.Background(),
		config,
		[]wallet.SerializedPublicKey{tokenIdentityPubKeyBytes}, // token public key
		nil,                           // owner public keys
		nil,                           // output IDs
		nil,                           // transaction hashes
		tokenTransactionsPage2.Offset, // offset - use the offset from previous response
		1,                             // limit - only get 1 transaction
	)
	if err != nil {
		t.Fatalf("failed to query token transactions page 3: %v", err)
	}

	// Verify we got no transactions
	if len(tokenTransactionsPage3.TokenTransactionsWithStatus) != 0 {
		t.Fatalf("expected 0 token transactions in page 3, got %d", len(tokenTransactionsPage3.TokenTransactionsWithStatus))
	}

	// Verify the offset is -1 (indicating end of results)
	if tokenTransactionsPage3.Offset != -1 {
		t.Fatalf("expected next offset -1 for page 3, got %d", tokenTransactionsPage3.Offset)
	}

	// Now validate the transaction details from the paginated results
	// Validate transfer created output
	if len(transferTx.TokenOutputs) != 1 {
		t.Fatalf("expected 1 created output in transfer transaction, got %d", len(transferTx.TokenOutputs))
	}
	transferAmount := new(big.Int).SetBytes(transferTx.TokenOutputs[0].TokenAmount)
	expectedTransferAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestTransferOutput1Amount))
	if transferAmount.Cmp(expectedTransferAmount) != 0 {
		t.Fatalf("transfer amount %d does not match expected %d", transferAmount, expectedTransferAmount)
	}
	if !bytes.Equal(transferTx.TokenOutputs[0].OwnerPublicKey, userOutput3PubKeyBytes) {
		t.Fatal("transfer created output owner public key does not match expected")
	}

	// Validate mint created outputs
	if len(mintTx.TokenOutputs) != 2 {
		t.Fatalf("expected 2 created outputs in mint transaction, got %d", len(mintTx.TokenOutputs))
	}

	userOutput1Pubkey := userOutput1PrivKey.PubKey().SerializeCompressed()
	userOutput2Pubkey := userOutput2PrivKey.PubKey().SerializeCompressed()

	if bytes.Equal(mintTx.TokenOutputs[0].OwnerPublicKey, userOutput1Pubkey) {
		assert.Equal(t, mintTx.TokenOutputs[1].OwnerPublicKey, userOutput2Pubkey)

		assert.Equal(t, bytesToBigInt(mintTx.TokenOutputs[0].TokenAmount), uint64ToBigInt(TestIssueOutput1Amount))
		assert.Equal(t, bytesToBigInt(mintTx.TokenOutputs[1].TokenAmount), uint64ToBigInt(TestIssueOutput2Amount))
	} else if bytes.Equal(mintTx.TokenOutputs[0].OwnerPublicKey, userOutput2Pubkey) {
		assert.Equal(t, mintTx.TokenOutputs[1].OwnerPublicKey, userOutput1Pubkey)

		assert.Equal(t, bytesToBigInt(mintTx.TokenOutputs[0].TokenAmount), uint64ToBigInt(TestIssueOutput2Amount))
		assert.Equal(t, bytesToBigInt(mintTx.TokenOutputs[1].TokenAmount), uint64ToBigInt(TestIssueOutput1Amount))
	} else {
		t.Fatalf("mint transaction output keys (%x, %x) do not match expected (%x, %x)",
			mintTx.TokenOutputs[0].OwnerPublicKey,
			mintTx.TokenOutputs[1].OwnerPublicKey,
			userOutput1Pubkey,
			userOutput2Pubkey,
		)
	}
}

func TestBroadcastTokenTransactionMintAndTransferTokensLotsOfOutputs(t *testing.T) {
	skipIfGithubActions(t)
	config, err := testutil.TestWalletConfigWithIdentityKey(*staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()

	// Try to create issuance transaction with 101 outputs (should fail)
	tooBigIssuanceTransaction, _, err := createTestTokenMintTransactionWithMultipleTokenOutputs(config,
		tokenIdentityPubKeyBytes, 101)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Attempt to broadcast the issuance transaction with too many outputs
	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, tooBigIssuanceTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[]wallet.SerializedPublicKey{})
	require.Error(t, err, "expected error when broadcasting issuance transaction with more than 100 created outputs")

	// Create issuance transaction with 100 outputs
	issueTokenTransactionFirst100, userOutputPrivKeysFirst100, err := createTestTokenMintTransactionWithMultipleTokenOutputs(config,
		tokenIdentityPubKeyBytes, ManyOutputsCount)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Broadcast the issuance transaction
	finalIssueTokenTransactionFirst100, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransactionFirst100,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[]wallet.SerializedPublicKey{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	// Create issuance transaction with 100 outputs
	issueTokenTransactionSecond100, userOutputPrivKeysSecond100, err := createTestTokenMintTransactionWithMultipleTokenOutputs(config,
		tokenIdentityPubKeyBytes, ManyOutputsCount)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Broadcast the issuance transaction
	finalIssueTokenTransactionSecond100, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransactionSecond100,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[]wallet.SerializedPublicKey{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	finalIssueTokenTransactionHashFirst100, err := utils.HashTokenTransaction(finalIssueTokenTransactionFirst100, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	finalIssueTokenTransactionHashSecond100, err := utils.HashTokenTransaction(finalIssueTokenTransactionSecond100, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	// Create consolidation transaction
	consolidatedOutputPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err, "failed to generate private key")

	consolidatedOutputPubKeyBytes := consolidatedOutputPrivKey.PubKey().SerializeCompressed()

	// Create a transfer transaction that consolidates all outputs with too many inputs.
	outputsToSpendTooMany := make([]*pb.TokenOutputToSpend, 200)
	for i := 0; i < 100; i++ {
		outputsToSpendTooMany[i] = &pb.TokenOutputToSpend{
			PrevTokenTransactionHash: finalIssueTokenTransactionHashFirst100,
			PrevTokenTransactionVout: uint32(i),
		}
	}
	for i := 0; i < 100; i++ {
		outputsToSpendTooMany[100+i] = &pb.TokenOutputToSpend{
			PrevTokenTransactionHash: finalIssueTokenTransactionHashSecond100,
			PrevTokenTransactionVout: uint32(i),
		}
	}

	tooManyTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TokenTransferInput{
				OutputsToSpend: outputsToSpendTooMany,
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				OwnerPublicKey: consolidatedOutputPubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestIssueMultiplePerOutputAmount*ManyOutputsCount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	// Combine private keys from both issuance transactions
	allUserOutputPrivKeys := append(userOutputPrivKeysFirst100, userOutputPrivKeysSecond100...)

	// Collect all revocation public keys from both transactions
	allRevPubKeys := make([]wallet.SerializedPublicKey, 200)
	for i := 0; i < 100; i++ {
		allRevPubKeys[i] = finalIssueTokenTransactionFirst100.TokenOutputs[i].RevocationCommitment
		allRevPubKeys[i+100] = finalIssueTokenTransactionSecond100.TokenOutputs[i].RevocationCommitment
	}

	// Broadcast the consolidation transaction
	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, tooManyTransaction,
		allUserOutputPrivKeys,
		allRevPubKeys,
	)
	require.Error(t, err, "expected error when broadcasting issuance transaction with more than 100 input outputs")

	// Now try with just the first 100
	outputsToSpend := make([]*pb.TokenOutputToSpend, 100)
	for i := 0; i < 100; i++ {
		outputsToSpend[i] = &pb.TokenOutputToSpend{
			PrevTokenTransactionHash: finalIssueTokenTransactionHashFirst100,
			PrevTokenTransactionVout: uint32(i),
		}
	}
	consolidateTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TokenTransferInput{
				OutputsToSpend: outputsToSpend,
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				OwnerPublicKey: consolidatedOutputPubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestIssueMultiplePerOutputAmount*ManyOutputsCount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	// Collect all revocation public keys
	revPubKeys := make([]wallet.SerializedPublicKey, 100)
	for i := 0; i < 100; i++ {
		revPubKeys[i] = finalIssueTokenTransactionFirst100.TokenOutputs[i].RevocationCommitment
	}

	// Broadcast the consolidation transaction
	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, consolidateTransaction,
		userOutputPrivKeysFirst100,
		revPubKeys,
	)
	require.NoError(t, err, "failed to broadcast consolidation transaction")

	// Verify the consolidated amount
	tokenOutputsResponse, err := wallet.QueryTokenOutputs(
		context.Background(),
		config,
		[]wallet.SerializedPublicKey{consolidatedOutputPubKeyBytes},
		[]wallet.SerializedPublicKey{tokenIdentityPubKeyBytes},
	)
	require.NoError(t, err, "failed to get owned token outputs")

	require.Equal(t, 1, len(tokenOutputsResponse.OutputsWithPreviousTransactionData), "expected 1 consolidated output")
}

func TestFreezeAndUnfreezeTokens(t *testing.T) {
	skipIfGithubActions(t)
	config, err := testutil.TestWalletConfigWithIdentityKey(*staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Broadcast the token transaction
	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[]wallet.SerializedPublicKey{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %v", finalIssueTokenTransaction)

	// Validate withdrawal params match config
	for i, output := range finalIssueTokenTransaction.TokenOutputs {
		require.Equal(t, uint64(WithdrawalBondSatsInConfig), output.GetWithdrawBondSats(),
			"output %d: expected withdrawal bond sats %d, got %d", i, uint64(WithdrawalBondSatsInConfig), output.GetWithdrawBondSats())
		require.Equal(t, uint64(WithdrawalRelativeBlockLocktimeInConfig), output.GetWithdrawRelativeBlockLocktime(),
			"output %d: expected withdrawal relative block locktime %d, got %d", i, uint64(WithdrawalRelativeBlockLocktimeInConfig), output.GetWithdrawRelativeBlockLocktime())
	}

	// Call FreezeTokens to freeze the created output
	freezeResponse, err := wallet.FreezeTokens(
		context.Background(),
		config,
		finalIssueTokenTransaction.TokenOutputs[0].OwnerPublicKey, // owner public key of the output to freeze
		tokenIdentityPubKeyBytes,                                  // token public key
		false,                                                     // unfreeze
	)
	require.NoError(t, err, "failed to freeze tokens")

	// Convert frozen amount bytes to big.Int for comparison
	frozenAmount := new(big.Int).SetBytes(freezeResponse.ImpactedTokenAmount)

	// Calculate total amount from transaction created outputs
	expectedAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestIssueOutput1Amount))
	expectedOutputID := finalIssueTokenTransaction.TokenOutputs[0].Id

	require.Equal(t, 0, frozenAmount.Cmp(expectedAmount),
		"frozen amount %s does not match expected amount %s", frozenAmount.String(), expectedAmount.String())
	require.Equal(t, 1, len(freezeResponse.ImpactedOutputIds), "expected 1 impacted output ID")
	require.Equal(t, *expectedOutputID, freezeResponse.ImpactedOutputIds[0],
		"frozen output ID %s does not match expected output ID %s", freezeResponse.ImpactedOutputIds[0], *expectedOutputID)

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final transfer token transaction")

	// Replace direct transaction creation with helper function call
	transferTokenTransaction, _, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	require.NoError(t, err, "failed to create test token transfer transaction")

	revPubKey1 := finalIssueTokenTransaction.TokenOutputs[0].RevocationCommitment
	revPubKey2 := finalIssueTokenTransaction.TokenOutputs[1].RevocationCommitment

	// Broadcast the token transaction
	transferFrozenTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userOutput1PrivKey, userOutput2PrivKey},
		[]wallet.SerializedPublicKey{revPubKey1, revPubKey2},
	)
	require.Error(t, err, "expected error when transferring frozen tokens")
	require.Nil(t, transferFrozenTokenTransactionResponse, "expected nil response when transferring frozen tokens")
	log.Printf("successfully froze tokens with response: %+v", freezeResponse)

	// Call FreezeTokens to thaw the created output
	unfreezeResponse, err := wallet.FreezeTokens(
		context.Background(),
		config,
		finalIssueTokenTransaction.TokenOutputs[0].OwnerPublicKey, // owner public key of the output to freeze
		tokenIdentityPubKeyBytes,
		true, // unfreeze
	)
	require.NoError(t, err, "failed to unfreeze tokens")

	// Convert frozen amount bytes to big.Int for comparison
	thawedAmount := new(big.Int).SetBytes(unfreezeResponse.ImpactedTokenAmount)

	require.Equal(t, 0, thawedAmount.Cmp(expectedAmount),
		"thawed amount %s does not match expected amount %s", thawedAmount.String(), expectedAmount.String())
	require.Equal(t, 1, len(unfreezeResponse.ImpactedOutputIds), "expected 1 impacted output ID")
	require.Equal(t, *expectedOutputID, unfreezeResponse.ImpactedOutputIds[0],
		"thawed output ID %s does not match expected output ID %s", unfreezeResponse.ImpactedOutputIds[0], *expectedOutputID)

	// Broadcast the token transaction
	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userOutput1PrivKey, userOutput2PrivKey},
		[]wallet.SerializedPublicKey{revPubKey1, revPubKey2},
	)
	require.NoError(t, err, "failed to broadcast thawed token transaction")
	require.NotNil(t, transferTokenTransactionResponse, "expected non-nil response when transferring thawed tokens")
	log.Printf("thawed token transfer broadcast finalized token transaction: %v", transferTokenTransactionResponse)
}

// Helper function for testing token mint transaction with various signing scenarios
// Parameters:
// - t: testing context
// - config: wallet configuration
// - testDoubleStartSameTransaction: whether to test double start
// - testDoubleStartDifferentOperator: whether to test double start with a different coordinator
// - testDoubleSign: whether to test double signing
// - testSignExpired: whether to test signing with an expired transaction
// - testDifferentTx: whether to test signing with a different transaction than was started
// - testInvalidSigningOperatorPublicKey: whether to test signing with an invalid operator public key in the payload
// - expectedSigningError: whether an error is expected during any of the signing operations
func testMintTransactionSigningScenarios(t *testing.T, config *wallet.Config,
	ownerPrivateKeys []*secp256k1.PrivateKey,
	testDoubleStart bool,
	testDoubleStartDifferentOperator bool,
	testDoubleSign bool,
	testSignExpired bool,
	testSignDifferentTx bool,
	testInvalidSigningOperatorPublicKey bool,
	expectedSigningError bool,
) (*pb.TokenTransaction, *secp256k1.PrivateKey, *secp256k1.PrivateKey) {
	tokenPrivKey := config.IdentityPrivateKey
	issuerPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	tokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransaction(config, issuerPubKeyBytes)
	require.NoError(t, err, "failed to create test token mint transaction")

	if ownerPrivateKeys == nil {
		ownerPrivateKeys = []*secp256k1.PrivateKey{&tokenPrivKey}
	}
	var startResp *pb.StartTokenTransactionResponse
	var finalTxHash []byte

	if testDoubleStart {
		startResp, _, finalTxHash, err = wallet.StartTokenTransaction(
			context.Background(), config, tokenTransaction, ownerPrivateKeys, nil,
		)
		require.NoError(t, err, "failed to start token transaction first time")

		startResp2, _, finalTxHash2, err := wallet.StartTokenTransaction(
			context.Background(), config, tokenTransaction, ownerPrivateKeys, nil,
		)
		require.NoError(t, err, "failed to start token transaction second time")

		require.True(t, bytes.Equal(finalTxHash, finalTxHash2), "transaction hashes should be identical")

		hash1, err := utils.HashTokenTransaction(startResp.FinalTokenTransaction, false)
		require.NoError(t, err, "failed to hash first final token transaction")

		hash2, err := utils.HashTokenTransaction(startResp2.FinalTokenTransaction, false)
		require.NoError(t, err, "failed to hash second final token transaction")

		require.True(t, bytes.Equal(hash1, hash2), "final transactions should hash to identical values")

	} else if testDoubleStartDifferentOperator {
		_, _, _, err = wallet.StartTokenTransaction(
			context.Background(), config, tokenTransaction, ownerPrivateKeys, nil,
		)
		require.NoError(t, err, "failed to start token transaction first time")

		modifiedConfig := *config
		differentCoordinatorID, err := getNonCoordinatorOperator(config)
		require.NoError(t, err, "failed to find a different coordinator identifier")
		modifiedConfig.CoodinatorIdentifier = differentCoordinatorID

		startResp, _, finalTxHash, err = wallet.StartTokenTransaction(
			context.Background(), &modifiedConfig, tokenTransaction, ownerPrivateKeys,
			nil,
		)
		require.NoError(t, err, "failed to start mint token transaction second time with different coordinator")
	} else {
		startResp, _, finalTxHash, err = wallet.StartTokenTransaction(
			context.Background(), config, tokenTransaction, ownerPrivateKeys, nil,
		)
		require.NoError(t, err, "failed to start token transaction")
	}

	txToSign := startResp.FinalTokenTransaction
	if testSignDifferentTx {
		differentIssueTokenTransaction, _, _, err := createTestTokenMintTransaction(config, issuerPubKeyBytes)
		require.NoError(t, err, "failed to create different test token issuance transaction")
		txToSign = differentIssueTokenTransaction
	}

	if testInvalidSigningOperatorPublicKey {
		// Generate a new random key to replace the valid one
		randomKey, err := secp256k1.GeneratePrivateKey()
		require.NoError(t, err, "failed to generate random key")
		for operatorID := range config.SigningOperators {
			config.SigningOperators[operatorID].IdentityPublicKey = randomKey.PubKey().SerializeCompressed()
			break // Only modify the first operator
		}
	}

	errorOccurred := false
	var halfSignOperatorSignatures wallet.OperatorSignatures
	if testDoubleSign {
		operatorKeys := splitOperatorIdentityPublicKeys(config)
		// Sign with half the operators to get in a partial signed state
		_, halfSignOperatorSignatures, err = wallet.SignTokenTransaction(
			context.Background(),
			config,
			startResp.FinalTokenTransaction, // Always use the original transaction for first sign (if double signing)
			finalTxHash,
			operatorKeys.FirstHalf,
			ownerPrivateKeys,
			nil,
		)
		require.NoError(t, err, "unexpected error during mint half signing")
	}

	if testSignExpired {
		// Wait for the transaction to expire (MinikubeTokenTransactionExpiryTimeSecs seconds)
		t.Logf("Waiting for %d seconds for transaction to expire...", MinikubeTokenTransactionExpiryTimeSecs)
		time.Sleep(time.Duration(MinikubeTokenTransactionExpiryTimeSecs) * time.Second)
	}

	// Complete the transaction signing with either the original or different transaction
	_, fullSignOperatorSignatures, err := wallet.SignTokenTransaction(
		context.Background(),
		config,
		txToSign,
		finalTxHash,
		nil, // Default to contact all operators
		ownerPrivateKeys,
		nil,
	)
	if err != nil {
		errorOccurred = true
		log.Printf("error when signing the mint transaction: %v", err)
	}

	if expectedSigningError {
		require.True(t, errorOccurred, "expected an error during mint signing operation but none occurred")
		return nil, nil, nil
	}

	require.False(t, errorOccurred, "unexpected error during mint signing operation: %v", err)
	if testDoubleSign {
		// Verify that all signatures from the half signing operation match the corresponding ones in the full signing
		for operatorID, halfSig := range halfSignOperatorSignatures {
			fullSig, exists := fullSignOperatorSignatures[operatorID]
			require.True(t, exists, "operator signature missing from full mint signing that was present in half signing")
			require.True(t, bytes.Equal(halfSig, fullSig), "signature mismatch between half and full mint signing for operator %s", operatorID)
		}
	}

	finalIssueTokenTransaction := startResp.FinalTokenTransaction
	log.Printf("mint transaction finalized: %v", finalIssueTokenTransaction)
	return finalIssueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey
}

// TestTokenMintTransactionSigning tests various signing scenarios for token mint transactions
func TestTokenMintTransactionSigning(t *testing.T) {
	config, err := testutil.TestWalletConfigWithIdentityKey(*staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey

	userOutputPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err, "failed to generate user output private key")

	testCases := []struct {
		name                            string
		ownerPrivateKeys                []*secp256k1.PrivateKey
		doubleStart                     bool
		doubleStartDifferentOperator    bool
		doubleSign                      bool
		expiredSign                     bool
		differentMintTx                 bool
		invalidSigningOperatorPublicKey bool
		expectedSigningError            bool
	}{
		{
			name:        "double start mint should succeed",
			doubleStart: true,
		},
		// BROKEN
		// {
		// 	name:                         "double start mint should succeed with a different operator via the different final transaction",
		// 	doubleStartDifferentOperator: true,
		// },
		{
			name:            "single sign mint should succeed with the same transaction",
			doubleSign:      false,
			differentMintTx: false,
		},
		{
			name:                 "single sign mint should fail with different transaction",
			doubleSign:           false,
			differentMintTx:      true,
			expectedSigningError: true,
		},
		{
			name:                 "double sign mint should fail with a different transaction",
			doubleSign:           true,
			differentMintTx:      true,
			expectedSigningError: true,
		},
		{
			name:            "double sign mint should succeed with same transaction",
			doubleSign:      true,
			differentMintTx: false,
		},
		{
			name:                 "mint should fail with expired transaction",
			expiredSign:          true,
			expectedSigningError: true,
		},
		{
			name:                 "mint should fail with too many issuer signing keys",
			ownerPrivateKeys:     []*secp256k1.PrivateKey{&tokenPrivKey, &tokenPrivKey},
			expectedSigningError: true,
		},
		{
			name:                            "mint should fail with invalid signing operator public key",
			invalidSigningOperatorPublicKey: true,
			expectedSigningError:            true,
		},
		{
			name:                 "mint should fail with incorrect issuer private key",
			ownerPrivateKeys:     []*secp256k1.PrivateKey{userOutputPrivKey},
			expectedSigningError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := testutil.TestWalletConfigWithIdentityKey(*staticLocalIssuerKey.IdentityPrivateKey())
			require.NoError(t, err, "failed to create wallet config")

			testMintTransactionSigningScenarios(
				t, config,
				tc.ownerPrivateKeys,
				tc.doubleStart,
				tc.doubleStartDifferentOperator,
				tc.doubleSign,
				tc.expiredSign,
				tc.differentMintTx,
				tc.invalidSigningOperatorPublicKey,
				tc.expectedSigningError)
		})
	}
}

// Helper function for testing token transfer transaction with various signing scenarios
// Parameters:
// - t: testing context
// - config: wallet configuration
// - finalIssueTokenTransaction: the finalized mint transaction
// - startingOwnerPrivateKeys: private keys to use for starting the transaction
// - signingOwnerPrivateKeys: private keys to use for signing the transaction
// - startSignatureIndexOrder: order of signatures for starting the transaction
// - signSignatureIndexOrder: order of signatures for signing the transaction
// - testDoubleStart: whether to test double start with the same transaction
// - testDoubleStartDifferentOperator: whether to test double start with a different coordinator
// - testDoubleStartDifferentTransaction: whether to test double start with a different transaction
// - testDoubleStartSignFirst: whether to sign the first transaction when testing double start with different transactions
// - testDoubleSign: whether to test double signing
// - testSignExpired: whether to test signing with an expired transaction
// - testSignDifferentTx: whether to test signing with a different transaction than was started
// - testInvalidSigningOperatorPublicKey: whether to test signing with an invalid operator public key
// - expectedSigningError: whether an error is expected during any of the signing operations
// - expectedStartError: whether an error is expected during the start operation
func testTransferTransactionSigningScenarios(t *testing.T, config *wallet.Config,
	finalIssueTokenTransaction *pb.TokenTransaction,
	startingOwnerPrivateKeys []*secp256k1.PrivateKey,
	signingOwnerPrivateKeys []*secp256k1.PrivateKey,
	startSignatureIndexOrder []uint32,
	signSignatureIndexOrder []uint32,
	testDoubleStart bool,
	testDoubleStartDifferentOperator bool,
	testDoubleStartDifferentTransaction bool,
	testDoubleStartSignFirst bool,
	testDoubleSign bool,
	testSignExpired bool,
	testSignDifferentTx bool,
	testInvalidSigningOperatorPublicKey bool,
	expectedSigningError bool,
	expectedStartError bool,
) {
	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	if signingOwnerPrivateKeys == nil {
		signingOwnerPrivateKeys = startingOwnerPrivateKeys
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	transferTokenTransaction, _, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	require.NoError(t, err, "failed to create test token transfer transaction")

	revPubKey1 := finalIssueTokenTransaction.TokenOutputs[0].RevocationCommitment
	revPubKey2 := finalIssueTokenTransaction.TokenOutputs[1].RevocationCommitment

	var transferStartResp *pb.StartTokenTransactionResponse
	var transferFinalTxHash []byte
	var startErrorOccurred bool

	if testDoubleStart {
		transferStartResp, _, transferFinalTxHash, err = wallet.StartTokenTransaction(
			context.Background(), config, transferTokenTransaction, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)
		require.NoError(t, err, "failed to start token transaction first time")

		transferStartResp2, _, transferFinalTxHash2, err := wallet.StartTokenTransaction(
			context.Background(), config, transferTokenTransaction, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)

		require.NoError(t, err, "failed to start token transaction second time")

		require.True(t, bytes.Equal(transferFinalTxHash, transferFinalTxHash2), "transaction hashes should be identical")

		hash1, err := utils.HashTokenTransaction(transferStartResp.FinalTokenTransaction, false)
		require.NoError(t, err, "failed to hash first final token transaction")

		hash2, err := utils.HashTokenTransaction(transferStartResp2.FinalTokenTransaction, false)
		require.NoError(t, err, "failed to hash second final token transaction")

		require.True(t, bytes.Equal(hash1, hash2), "final transactions should hash to identical values")
	} else if testDoubleStartDifferentTransaction {
		secondTxToStart := cloneTransferTransactionWithDifferentOutputOwner(
			transferTokenTransaction,
			signingOwnerPrivateKeys[0].PubKey().SerializeCompressed(),
		)

		transferStartResp1, _, transferFinalTxHash1, err := wallet.StartTokenTransaction(
			context.Background(), config, transferTokenTransaction, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)
		require.NoError(t, err, "failed to start token transaction first time")

		transferStartResp2, _, transferFinalTxHash2, err := wallet.StartTokenTransaction(
			context.Background(), config, secondTxToStart, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)
		require.NoError(t, err, "failed to start token transaction second time")

		// Verify the hashes are different for different transactions
		require.False(t, bytes.Equal(transferFinalTxHash1, transferFinalTxHash2),
			"transaction hashes should be different for different transactions")

		if testDoubleStartSignFirst {
			transferStartResp = transferStartResp1
			transferFinalTxHash = transferFinalTxHash1
		} else {
			transferStartResp = transferStartResp2
			transferFinalTxHash = transferFinalTxHash2
		}
	} else if testDoubleStartDifferentOperator {
		transferStartRespInitial, _, _, err := wallet.StartTokenTransaction(
			context.Background(), config, transferTokenTransaction, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)
		require.NoError(t, err, "failed to start token transaction first time")

		modifiedConfig := *config
		differentCoordinatorID, err := getNonCoordinatorOperator(config)
		require.NoError(t, err, "failed to find a different coordinator identifier")
		modifiedConfig.CoodinatorIdentifier = differentCoordinatorID

		// Use this for later signing because once executed, the outputs previously mapped to that transaction
		// are remapped to the new transaction in the database.
		transferStartResp, _, transferFinalTxHash, err = wallet.StartTokenTransaction(
			context.Background(), &modifiedConfig, transferTokenTransaction, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)

		require.NoError(t, err, "failed to start token transaction second time with different coordinator")
		require.NotNil(t, transferStartResp, "expected non-nil response from second start")

		verifyDifferentTransactionOutputs(t, transferStartRespInitial.FinalTokenTransaction, transferStartResp.FinalTokenTransaction)
	} else {
		transferStartResp, _, transferFinalTxHash, err = wallet.StartTokenTransaction(
			context.Background(), config, transferTokenTransaction, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)
		if err != nil {
			startErrorOccurred = true
			log.Printf("error when starting the transfer transaction: %v", err)
		}

		if expectedStartError {
			require.True(t, startErrorOccurred, "expected an error during transfer start operation but none occurred")
			return
		}
		require.NoError(t, err, "failed to start token transaction")
	}

	errorOccurred := false
	// Prepare transaction to sign - either the original or a modified one
	txToSign := transferStartResp.FinalTokenTransaction

	if testSignDifferentTx {
		txToSign = cloneTransferTransactionWithDifferentOutputOwner(
			transferStartResp.FinalTokenTransaction,
			signingOwnerPrivateKeys[0].PubKey().SerializeCompressed(),
		)
	}

	if testInvalidSigningOperatorPublicKey {
		// Generate a new random key to replace the valid one
		randomKey, err := secp256k1.GeneratePrivateKey()
		require.NoError(t, err, "failed to generate random key")
		for operatorID := range config.SigningOperators {
			config.SigningOperators[operatorID].IdentityPublicKey = randomKey.PubKey().SerializeCompressed()
			break // Only modify the first operator
		}
	}

	// If testing double signing, first sign with half the operators
	var halfSignOperatorSignatures wallet.OperatorSignatures
	if testDoubleSign {
		operatorKeys := splitOperatorIdentityPublicKeys(config)
		_, halfSignOperatorSignatures, err = wallet.SignTokenTransaction(
			context.Background(),
			config,
			transferStartResp.FinalTokenTransaction, // Always use original transaction for first sign
			transferFinalTxHash,
			operatorKeys.FirstHalf,
			signingOwnerPrivateKeys,
			signSignatureIndexOrder,
		)
		require.NoError(t, err, "unexpected error during transfer half signing")
	}

	if testSignExpired {
		// Wait for the transaction to expire (MinikubeTokenTransactionExpiryTimeSecs seconds)
		t.Logf("Waiting for %d seconds for transaction to expire...", MinikubeTokenTransactionExpiryTimeSecs)
		time.Sleep(time.Duration(MinikubeTokenTransactionExpiryTimeSecs) * time.Second)
	}

	// Complete the transaction signing with either the original or different transaction
	signResponseTransferKeyshares, fullSignOperatorSignatures, err := wallet.SignTokenTransaction(
		context.Background(),
		config,
		txToSign,
		transferFinalTxHash,
		nil, // Default to contact all operators
		signingOwnerPrivateKeys,
		signSignatureIndexOrder,
	)
	if err != nil {
		errorOccurred = true
		log.Printf("error when signing the transfer transaction: %v", err)
	}

	if expectedSigningError {
		require.True(t, errorOccurred, "expected an error during transfer signing operation but none occurred")
		return
	}
	require.False(t, errorOccurred, "unexpected error during transfer signing operation")
	if testDoubleSign {
		// Verify that all signatures from the half signing operation match the corresponding ones in the full signing
		for operatorID, halfSig := range halfSignOperatorSignatures {
			fullSig, exists := fullSignOperatorSignatures[operatorID]
			require.True(t, exists, "operator signature missing from full transfer signing that was present in half signing")
			require.True(t, bytes.Equal(halfSig, fullSig), "signature mismatch between half and full transfer signing for operator %s", operatorID)
		}
	}

	err = wallet.FinalizeTokenTransaction(
		context.Background(),
		config,
		transferStartResp.FinalTokenTransaction,
		signResponseTransferKeyshares,
		[]wallet.SerializedPublicKey{revPubKey1, revPubKey2},
	)
	require.NoError(t, err, "failed to finalize the transfer transaction")
	log.Printf("transfer transaction finalized: %v", transferStartResp.FinalTokenTransaction)
}

// TestTokenTransferTransactionSigning tests various signing scenarios for token transfer transactions
func TestTokenTransferTransactionSigning(t *testing.T) {
	testCases := []struct {
		name                            string
		startOwnerPrivateKeysModifier   func([]*secp256k1.PrivateKey) []*secp256k1.PrivateKey
		startSignatureIndexOrder        []uint32
		doubleStart                     bool
		doubleStartDifferentOperator    bool
		doubleStartSignFirst            bool
		doubleStartDifferentTx          bool
		doubleSign                      bool
		expiredSign                     bool
		signDifferentTx                 bool
		signingOwnerPrivateKeysModifier func([]*secp256k1.PrivateKey) []*secp256k1.PrivateKey
		signingOwnerSignatureIndexOrder []uint32
		invalidSigningOperatorPublicKey bool
		expectedStartError              bool
		expectedSigningError            bool
	}{
		{
			name: "single sign transfer should succeed with the same transaction",
		},

		{
			name:        "double start transfer should succeed",
			doubleStart: true,
		},
		{
			name:                   "double start transfer with modified second tx should succeed when signing the second tx",
			doubleStartDifferentTx: true,
		},
		{
			name:                   "double start transfer with modified second tx should fail when signing the first tx",
			doubleStartDifferentTx: true,
			doubleStartSignFirst:   true,
			expectedSigningError:   true,
		},

		{
			name:                     "start should succeed with reversed signature order",
			startSignatureIndexOrder: []uint32{1, 0},
		},
		{
			name: "start should fail with reversing the owner signatures themselves",
			startOwnerPrivateKeysModifier: func(tokenOutputs []*secp256k1.PrivateKey) []*secp256k1.PrivateKey {
				return []*secp256k1.PrivateKey{tokenOutputs[1], tokenOutputs[0]}
			},
			expectedStartError: true,
		},
		{
			name: "start should fail with reversing the owner signatures and also the order of the signatures",
			startOwnerPrivateKeysModifier: func(tokenOutputs []*secp256k1.PrivateKey) []*secp256k1.PrivateKey {
				return []*secp256k1.PrivateKey{tokenOutputs[1], tokenOutputs[0]}
			},
			startSignatureIndexOrder: []uint32{1, 0},
			expectedStartError:       true,
		},
		// BROKEN
		// {
		// 	name:                                 "double start transfer should succeed with a different operator via the different final transaction",
		// 	doubleStartDifferentOperator: true,
		// },
		{
			name:                            "sign should succeed with reversed signature order",
			signingOwnerSignatureIndexOrder: []uint32{1, 0},
		},
		{
			name:                 "single sign transfer should fail with different transaction",
			signDifferentTx:      true,
			expectedSigningError: true,
		},
		{
			name:                 "double sign transfer should fail with a different transaction",
			doubleSign:           true,
			signDifferentTx:      true,
			expectedSigningError: true,
		},
		{
			name:       "double sign transfer should succeed with same transaction",
			doubleSign: true,
		},
		{
			name:                 "sign transfer should fail with expired transaction",
			expiredSign:          true,
			expectedSigningError: true,
		},
		{
			name: "sign transfer should fail with duplicate operator specific owner signing private keys",
			signingOwnerPrivateKeysModifier: func(tokenOutputs []*secp256k1.PrivateKey) []*secp256k1.PrivateKey {
				return []*secp256k1.PrivateKey{tokenOutputs[0], tokenOutputs[0]}
			},
			expectedSigningError: true,
		},
		{
			name: "sign transfer should fail with reversing the operator specific owner signatures and also the order of the signatures",
			signingOwnerPrivateKeysModifier: func(tokenOutputs []*secp256k1.PrivateKey) []*secp256k1.PrivateKey {
				return []*secp256k1.PrivateKey{tokenOutputs[0], tokenOutputs[0]}
			},
			signingOwnerSignatureIndexOrder: []uint32{1, 0},
			expectedSigningError:            true,
		},
		{
			name: "sign transfer should fail with swapped owner signing private keys",
			signingOwnerPrivateKeysModifier: func(tokenOutputs []*secp256k1.PrivateKey) []*secp256k1.PrivateKey {
				return []*secp256k1.PrivateKey{tokenOutputs[1], tokenOutputs[0]}
			},
			expectedSigningError: true,
		},
		{
			name: "sign transfer should fail with not enough owner signing keys",
			signingOwnerPrivateKeysModifier: func(tokenOutputs []*secp256k1.PrivateKey) []*secp256k1.PrivateKey {
				return []*secp256k1.PrivateKey{tokenOutputs[0]}
			},
			expectedSigningError: true,
		},
		{
			name: "sign transfer should fail with too many owner signing keys",
			signingOwnerPrivateKeysModifier: func(tokenOutputs []*secp256k1.PrivateKey) []*secp256k1.PrivateKey {
				return []*secp256k1.PrivateKey{tokenOutputs[0], tokenOutputs[1], tokenOutputs[0]}
			},
			expectedSigningError: true,
		},
		{
			name:                            "sign transfer should fail with invalid signing operator public key",
			invalidSigningOperatorPublicKey: true,
			expectedSigningError:            true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a fresh config for each test case
			config, err := testutil.TestWalletConfigWithIdentityKey(*staticLocalIssuerKey.IdentityPrivateKey())
			require.NoError(t, err, "failed to create wallet config")

			// Create and finalize a mint transaction for this specific test case
			finalIssueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey := testMintTransactionSigningScenarios(
				t, config, nil, false, false, false, false, false, false, false)

			defaultStartingOwnerPrivateKeys := []*secp256k1.PrivateKey{userOutput1PrivKey, userOutput2PrivKey}
			var startingPrivKeys []*secp256k1.PrivateKey
			if tc.startOwnerPrivateKeysModifier != nil {
				startingPrivKeys = tc.startOwnerPrivateKeysModifier(defaultStartingOwnerPrivateKeys)
			} else {
				startingPrivKeys = defaultStartingOwnerPrivateKeys
			}
			var startSignatureIndexOrder []uint32
			if tc.startSignatureIndexOrder != nil {
				startSignatureIndexOrder = tc.startSignatureIndexOrder
			}

			var signingPrivKeys []*secp256k1.PrivateKey
			if tc.signingOwnerPrivateKeysModifier != nil {
				signingPrivKeys = tc.signingOwnerPrivateKeysModifier(defaultStartingOwnerPrivateKeys)
			}

			var signSignatureIndexOrder []uint32
			if tc.startSignatureIndexOrder != nil {
				signSignatureIndexOrder = tc.startSignatureIndexOrder
			}

			testTransferTransactionSigningScenarios(
				t, config, finalIssueTokenTransaction,
				startingPrivKeys,
				signingPrivKeys,
				startSignatureIndexOrder,
				signSignatureIndexOrder,
				tc.doubleStart,
				tc.doubleStartDifferentOperator,
				tc.doubleStartDifferentTx,
				tc.doubleStartSignFirst,
				tc.doubleSign,
				tc.expiredSign,
				tc.signDifferentTx,
				tc.invalidSigningOperatorPublicKey,
				tc.expectedSigningError,
				tc.expectedStartError)
		})
	}
}

func TestBroadcastTokenTransactionMintAndTransferTokensSchnorr(t *testing.T) {
	config, err := testutil.TestWalletConfigWithIdentityKey(*staticLocalIssuerKey.IdentityPrivateKey())
	config.UseTokenTransactionSchnorrSignatures = true
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[]wallet.SerializedPublicKey{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %v", finalIssueTokenTransaction)

	// Validate withdrawal params match config
	for i, output := range finalIssueTokenTransaction.TokenOutputs {
		require.Equal(t, uint64(WithdrawalBondSatsInConfig), output.GetWithdrawBondSats(),
			"output %d: expected withdrawal bond sats %d, got %d", i, uint64(WithdrawalBondSatsInConfig), output.GetWithdrawBondSats())
		require.Equal(t, uint64(WithdrawalRelativeBlockLocktimeInConfig), output.GetWithdrawRelativeBlockLocktime(),
			"output %d: expected withdrawal relative block locktime %d, got %d", i, uint64(WithdrawalRelativeBlockLocktimeInConfig), output.GetWithdrawRelativeBlockLocktime())
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	transferTokenTransaction, _, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	require.NoError(t, err, "failed to create test token transfer transaction")

	revPubKey1 := finalIssueTokenTransaction.TokenOutputs[0].RevocationCommitment
	revPubKey2 := finalIssueTokenTransaction.TokenOutputs[1].RevocationCommitment

	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userOutput1PrivKey, userOutput2PrivKey},
		[]wallet.SerializedPublicKey{revPubKey1, revPubKey2},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")
	log.Printf("transfer broadcast finalized token transaction: %v", transferTokenTransactionResponse)
}

func TestFreezeAndUnfreezeTokensSchnorr(t *testing.T) {
	skipIfGithubActions(t)
	config, err := testutil.TestWalletConfigWithIdentityKey(*staticLocalIssuerKey.IdentityPrivateKey())
	config.UseTokenTransactionSchnorrSignatures = true
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, _, _, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[]wallet.SerializedPublicKey{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	_, err = wallet.FreezeTokens(
		context.Background(),
		config,
		finalIssueTokenTransaction.TokenOutputs[0].OwnerPublicKey,
		tokenIdentityPubKeyBytes,
		false,
	)
	require.NoError(t, err, "failed to freeze tokens")
}

func TestCancelTokenTransaction(t *testing.T) {
	skipIfGithubActions(t)
	config, err := testutil.TestWalletConfigWithIdentityKey(*staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	operatorKeys := splitOperatorIdentityPublicKeys(config)

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	startResp, _, finalTxHash, err := wallet.StartTokenTransaction(
		context.Background(),
		config,
		issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		nil,
	)
	require.NoError(t, err, "failed to start token transaction")
	finalIssueTokenTransaction := startResp.FinalTokenTransaction

	_, _, err = wallet.SignTokenTransaction(
		context.Background(),
		config,
		startResp.FinalTokenTransaction,
		finalTxHash,
		operatorKeys.FirstHalf,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		nil,
	)
	require.NoError(t, err, "failed to sign the mint transaction with the first half of SOs")

	err = wallet.CancelTokenTransaction(
		context.Background(),
		config,
		startResp.FinalTokenTransaction,
		operatorKeys.FirstHalf,
	)
	require.Error(t, err, "expected cancel failure on mint transaction. Mint cancellation is not supported")

	_, _, err = wallet.SignTokenTransaction(
		context.Background(),
		config,
		startResp.FinalTokenTransaction,
		finalTxHash,
		operatorKeys.SecondHalf,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		nil,
	)
	require.NoError(t, err, "failed to sign the mint transaction with the second half of SOs")

	// Test cancellation of a transfer transaction
	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	transferTokenTransaction, _, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	require.NoError(t, err, "failed to create test token transfer transaction")

	revPubKey1 := finalIssueTokenTransaction.TokenOutputs[0].RevocationCommitment
	revPubKey2 := finalIssueTokenTransaction.TokenOutputs[1].RevocationCommitment

	transferStartResp, _, transferFinalTxHash, err := wallet.StartTokenTransaction(
		context.Background(),
		config,
		transferTokenTransaction,
		[]*secp256k1.PrivateKey{userOutput1PrivKey, userOutput2PrivKey},
		[]uint32{0, 1},
	)
	require.NoError(t, err, "failed to start transfer transaction")

	log.Printf("transfer tx hash: %x", transferFinalTxHash)

	// Sign with only half of the operators
	_, _, err = wallet.SignTokenTransaction(
		context.Background(),
		config,
		transferStartResp.FinalTokenTransaction,
		transferFinalTxHash,
		operatorKeys.FirstHalf,
		[]*secp256k1.PrivateKey{userOutput1PrivKey, userOutput2PrivKey},
		[]uint32{0, 1},
	)
	require.NoError(t, err, "failed partial signing")

	// Cancel the transfer transaction after partial signing
	err = wallet.CancelTokenTransaction(
		context.Background(),
		config,
		transferStartResp.FinalTokenTransaction,
		operatorKeys.FirstHalf, // Cancel for the half of the operators that signed.
	)
	require.NoError(t, err, "failed to cancel partially signed transfer token transaction")

	// Attempt to cancel the transaction with the SOs that did not sign
	err = wallet.CancelTokenTransaction(
		context.Background(),
		config,
		transferStartResp.FinalTokenTransaction,
		operatorKeys.SecondHalf,
	)
	require.Error(t, err, "expected error when trying to cancel transfer transaction with remaining operators")

	// Verify we can create a new transfer transaction after cancellation
	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(),
		config,
		transferTokenTransaction,
		[]*secp256k1.PrivateKey{userOutput1PrivKey, userOutput2PrivKey},
		[]wallet.SerializedPublicKey{revPubKey1, revPubKey2},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction after cancellation")
	log.Printf("successfully transferred tokens after cancellation: %v", transferTokenTransactionResponse)
}

func TestBroadcastTokenTransactionWithInvalidPrevTxHash(t *testing.T) {
	config, err := testutil.TestWalletConfigWithIdentityKey(*staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[]wallet.SerializedPublicKey{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %v", finalIssueTokenTransaction)

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	// Corrupt the transaction hash by adding a byte
	corruptedHash := append(finalIssueTokenTransactionHash, 0xFF)

	// Create transfer transaction with corrupted hash
	transferTokenTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TokenTransferInput{
				OutputsToSpend: []*pb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: corruptedHash, // Corrupted hash
						PrevTokenTransactionVout: 0,
					},
					{
						PrevTokenTransactionHash: finalIssueTokenTransactionHash,
						PrevTokenTransactionVout: 1,
					},
				},
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				OwnerPublicKey: userOutput1PrivKey.PubKey().SerializeCompressed(),
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestTransferOutput1Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	revPubKey1 := finalIssueTokenTransaction.TokenOutputs[0].RevocationCommitment
	revPubKey2 := finalIssueTokenTransaction.TokenOutputs[1].RevocationCommitment

	// Attempt to broadcast the transfer transaction with corrupted hash
	// This should fail validation
	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userOutput1PrivKey, userOutput2PrivKey},
		[]wallet.SerializedPublicKey{revPubKey1, revPubKey2},
	)

	require.Error(t, err, "expected transaction with invalid hash to be rejected")
	log.Printf("successfully detected invalid transaction hash: %v", err)

	// Try with only the second hash corrupted
	transferTokenTransaction2 := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TokenTransferInput{
				OutputsToSpend: []*pb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: finalIssueTokenTransactionHash,
						PrevTokenTransactionVout: 0,
					},
					{
						PrevTokenTransactionHash: append(finalIssueTokenTransactionHash, 0xAA), // Corrupted hash
						PrevTokenTransactionVout: 1,
					},
				},
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				OwnerPublicKey: userOutput1PrivKey.PubKey().SerializeCompressed(),
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestTransferOutput1Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	// Attempt to broadcast the second transfer transaction with corrupted hash
	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction2,
		[]*secp256k1.PrivateKey{userOutput1PrivKey, userOutput2PrivKey},
		[]wallet.SerializedPublicKey{revPubKey1, revPubKey2},
	)

	require.Error(t, err, "expected transaction with second invalid hash to be rejected")
	log.Printf("successfully detected second invalid transaction hash: %v", err)
}

func TestBroadcastTokenTransactionUnspecifiedNetwork(t *testing.T) {
	config, err := testutil.TestWalletConfigWithIdentityKey(*staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, _, _, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")
	issueTokenTransaction.Network = pb.Network_UNSPECIFIED

	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[]wallet.SerializedPublicKey{})

	require.Error(t, err, "expected transaction without a network to be rejected")
	log.Printf("successfully detected unspecified network and rejected with error: %v", err)
}

// cloneTransferTransactionWithDifferentOutputOwner creates a copy of a transfer transaction
// with a modified owner public key in the first output
func cloneTransferTransactionWithDifferentOutputOwner(
	tx *pb.TokenTransaction,
	newOwnerPubKey []byte,
) *pb.TokenTransaction {
	clone := proto.Clone(tx).(*pb.TokenTransaction)
	if len(clone.TokenOutputs) > 0 {
		clone.TokenOutputs[0].OwnerPublicKey = newOwnerPubKey
	}
	return clone
}

func verifyDifferentTransactionOutputs(t *testing.T, firstTx, secondTx *pb.TokenTransaction) {
	for i, output := range firstTx.TokenOutputs {
		secondOutput := secondTx.TokenOutputs[i]

		require.NotEqual(t, output.Id, secondOutput.Id,
			"expected different output IDs when starting with different coordinator")

		// Revocation commitments should be different
		require.False(t, bytes.Equal(output.RevocationCommitment, secondOutput.RevocationCommitment),
			"expected different revocation commitments when starting with different coordinator")
	}

	hash1, err := utils.HashTokenTransaction(firstTx, false)
	require.NoError(t, err, "failed to hash first final token transaction")

	hash2, err := utils.HashTokenTransaction(secondTx, false)
	require.NoError(t, err, "failed to hash second final token transaction")

	require.False(t, bytes.Equal(hash1, hash2),
		"transaction hashes should be different when double starting with different coordinator")
}

func getNonCoordinatorOperator(config *wallet.Config) (string, error) {
	for id := range config.SigningOperators {
		if id != config.CoodinatorIdentifier {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not find a non-coordinator operator")
}
