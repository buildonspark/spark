package grpctest

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	sparktesting "github.com/lightsparkdev/spark/testing"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/utils"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// Test token amounts for various operations
const (
	// Amount for first created output in issuance transaction
	TestIssueOutput1Amount = 11
	// Amount for second created output in issuance transaction
	TestIssueOutput2Amount = 22
	// Amount for first (and only) created output in transfer transaction
	TestTransferOutput1Amount = 33
	// Configured at SO level. We validate in the tests to ensure these are populated correctly
	WithdrawalBondSatsInConfig              = 10000
	WithdrawalRelativeBlockLocktimeInConfig = 1000
	MinikubeTokenTransactionExpiryTimeSecs  = 30
	// Expiry cleanup tasks run every 30 seconds, + 3 seconds for processing time
	TokenTransactionExpiryProcessingTimeSecs = 33
	// Test token parameters shared between tokenMetadata and token transaction creation
	// In order to support L1 token creation enforcement testing, these should match
	// the params used when creating the static L1 token as part of test harness setup.
	TestTokenName        = "TestToken"
	TestTokenTicker      = "TEST"
	TestTokenDecimals    = 8
	TestTokenIsFreezable = true
	TestTokenMaxSupply   = 0
)

var MaxInputOrOutputTokenTransactionOutputsForTests = func() int {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		return int(math.Floor(float64(utils.MaxInputOrOutputTokenTransactionOutputs) * 0.5))
	}
	return utils.MaxInputOrOutputTokenTransactionOutputs
}()

var (
	// The expected maximum number of outputs which can be created in a single transaction.
	manyOutputsCount = MaxInputOrOutputTokenTransactionOutputsForTests
	// Amount for second created output in multiple output issuance transaction
	testIssueMultiplePerOutputAmount = MaxInputOrOutputTokenTransactionOutputsForTests
)

type prederivedIdentityPrivateKeyFromMnemonic struct {
	identityPrivateKeyHex string
}

func (k *prederivedIdentityPrivateKeyFromMnemonic) IdentityPrivateKey() keys.Private {
	privKeyBytes, err := hex.DecodeString(k.identityPrivateKeyHex)
	if err != nil {
		panic("invalid issuer private key hex")
	}
	privKey, err := keys.ParsePrivateKey(privKeyBytes)
	if err != nil {
		panic("invalid issuer private key")
	}
	return privKey
}

var staticLocalIssuerKey = prederivedIdentityPrivateKeyFromMnemonic{
	// Mnemonic:           "table apology decrease custom deny client retire genius uniform find eager fish",
	// TokenL1Address:     "bcrt1q2mgym77n8ta8gn48xtusyrd6wr5uhecajyshku",
	identityPrivateKeyHex: "515c86ccb09faa2235acd0e287381bf286b37002328a8cc3c3b89738ab59dc93",
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

// getTokenMaxSupplyBytes returns the max supply as a uint128 bytes
func getTokenMaxSupplyBytes(maxSupply uint64) []byte {
	return int64ToUint128Bytes(0, maxSupply)
}

func getSigningOperatorPublicKeyBytes(config *wallet.TestWalletConfig) [][]byte {
	var publicKeys [][]byte
	for _, operator := range config.SigningOperators {
		publicKeys = append(publicKeys, operator.IdentityPublicKey.Serialize())
	}
	return publicKeys
}

func createTestTokenMintTransaction(config *wallet.TestWalletConfig, tokenIdentityPubKey keys.Public) (*pb.TokenTransaction, keys.Private, keys.Private, error) {
	return createTestTokenMintTransactionWithParams(config, tokenIdentityPubKey)
}

func createTestTokenMintTransactionWithParams(config *wallet.TestWalletConfig, issuerPublicKey keys.Public) (*pb.TokenTransaction, keys.Private, keys.Private, error) {
	// Generate two user output key pairs
	userOutput1PrivKey, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, keys.Private{}, keys.Private{}, err
	}
	userOutput1PubKeyBytes := userOutput1PrivKey.Public().Serialize()

	userOutput2PrivKey, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, keys.Private{}, keys.Private{}, err
	}
	userOutput2PubKeyBytes := userOutput2PrivKey.Public().Serialize()

	mintTokenTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_MintInput{
			MintInput: &pb.TokenMintInput{
				IssuerPublicKey:         issuerPublicKey.Serialize(),
				IssuerProvidedTimestamp: uint64(time.Now().UnixMilli()),
			},
		},
		TokenOutputs: []*pb.TokenOutput{
			{
				OwnerPublicKey: userOutput1PubKeyBytes,
				TokenPublicKey: issuerPublicKey.Serialize(),
				TokenAmount:    int64ToUint128Bytes(0, TestIssueOutput1Amount),
			},
			{
				OwnerPublicKey: userOutput2PubKeyBytes,
				TokenPublicKey: issuerPublicKey.Serialize(),
				TokenAmount:    int64ToUint128Bytes(0, TestIssueOutput2Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
	}

	return mintTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, nil
}

func createTestTokenTransferTransaction(config *wallet.TestWalletConfig, finalIssueTokenTransactionHash []byte, issuerPublicKey keys.Public) (*pb.TokenTransaction, keys.Private, error) {
	return createTestTokenTransferTransactionWithParams(config, finalIssueTokenTransactionHash, issuerPublicKey)
}

func createTestTokenTransferTransactionWithParams(
	config *wallet.TestWalletConfig,
	finalIssueTokenTransactionHash []byte,
	issuerPublicKey keys.Public,
) (*pb.TokenTransaction, keys.Private, error) {
	userOutput3PrivKey, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, keys.Private{}, err
	}
	userOutput3PubKeyBytes := userOutput3PrivKey.Public().Serialize()

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
				TokenPublicKey: issuerPublicKey.Serialize(),
				TokenAmount:    int64ToUint128Bytes(0, TestTransferOutput1Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
	}
	return transferTokenTransaction, userOutput3PrivKey, nil
}

func createTestTokenMintTransactionWithMultipleTokenOutputs(config *wallet.TestWalletConfig, issuerPublicKey keys.Public, numOutputs int) (*pb.TokenTransaction, []keys.Private, error) {
	userOutputPrivKeys := make([]keys.Private, numOutputs)
	outputOutputs := make([]*pb.TokenOutput, numOutputs)

	for i := 0; i < numOutputs; i++ {
		privKey, err := keys.GeneratePrivateKey()
		if err != nil {
			return nil, nil, err
		}
		userOutputPrivKeys[i] = privKey
		pubKeyBytes := privKey.Public().Serialize()

		outputOutputs[i] = &pb.TokenOutput{
			OwnerPublicKey: pubKeyBytes,
			TokenPublicKey: issuerPublicKey.Serialize(),
			TokenAmount:    int64ToUint128Bytes(0, uint64(testIssueMultiplePerOutputAmount)),
		}
	}

	issueTokenTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_MintInput{
			MintInput: &pb.TokenMintInput{
				IssuerPublicKey:         issuerPublicKey.Serialize(),
				IssuerProvidedTimestamp: uint64(time.Now().UnixMilli()),
			},
		},
		TokenOutputs:                    outputOutputs,
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
	}

	return issueTokenTransaction, userOutputPrivKeys, nil
}

// operatorKeysSplit contains two groups of operator public keys
type operatorKeysSplit struct {
	firstHalf  []keys.Public
	secondHalf []keys.Public
}

// splitOperatorIdentityPublicKeys splits the operators from the config into two approximately equal groups
func splitOperatorIdentityPublicKeys(config *wallet.TestWalletConfig) operatorKeysSplit {
	publicKeys := make([]keys.Public, 0, len(config.SigningOperators))
	for _, operator := range config.SigningOperators {
		publicKeys = append(publicKeys, operator.IdentityPublicKey)
	}

	halfOperatorCount := len(config.SigningOperators) / 2

	return operatorKeysSplit{
		firstHalf:  publicKeys[:halfOperatorCount],
		secondHalf: publicKeys[halfOperatorCount:],
	}
}

// skipIfGithubActions skips the test if running in GitHub Actions
func skipIfGithubActions(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Skip("Skipping test on GitHub Actions CI")
	}
}

func TestQueryPartiallySpentTokenOutputsNotReturned(t *testing.T) {
	config, err := sparktesting.TestWalletConfigWithIdentityKey(staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubkeyBytes := tokenPrivKey.Public().Serialize()

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
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
	}

	ownerSigningPrivateKeys := []keys.Private{tokenPrivKey}

	broadcastMintResponse, err := wallet.BroadcastTokenTransaction(
		t.Context(), config, mintTokenTransaction, ownerSigningPrivateKeys, nil,
	)
	require.NoError(t, err, "failed to start token transaction: %v", err)

	mintTxHash, err := utils.HashTokenTransactionV0(broadcastMintResponse, false)
	require.NoError(t, err, "failed to hash token transaction: %v", err)

	receiverPrivateKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to generate receiver private key: %v", err)
	receiverPubKeyBytes := receiverPrivateKey.Public().Serialize()

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
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
	}

	transferTxResp, _, transferTxHash, err := wallet.StartTokenTransaction(
		t.Context(),
		config,
		transferTokenTransaction,
		ownerSigningPrivateKeys,
		nil,
	)
	require.NoError(t, err, "failed to start token transaction: %v", err)

	_, _, err = wallet.SignTokenTransaction(
		t.Context(),
		config,
		transferTxResp.FinalTokenTransaction,
		transferTxHash,
		splitOperatorIdentityPublicKeys(config).secondHalf,
		ownerSigningPrivateKeys,
		nil,
	)
	require.NoError(t, err, "failed to sign token transaction: %v", err)

	// Query the coordinator for the above spent output
	notEnoughSignedOutput, err := wallet.QueryTokenOutputs(
		t.Context(),
		config,
		[]keys.Public{tokenPrivKey.Public()},
		nil,
	)
	require.NoError(t, err, "failed to query token on not enough signatures")

	require.Len(t, notEnoughSignedOutput.OutputsWithPreviousTransactionData, 1, "expected one output when using not enough signatures to transfer one of two outputs")
	require.Equal(t, uint64ToBigInt(TestIssueOutput2Amount), bytesToBigInt(notEnoughSignedOutput.OutputsWithPreviousTransactionData[0].Output.TokenAmount), "expected the second output to be returned when using not enough signatures to transfer one of two outputs")
}

func TestQueryTokenOutputsByNetworkReturnsNoneForMismatchedNetwork(t *testing.T) {
	config, err := sparktesting.TestWalletConfigWithIdentityKey(staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	// Create the issuance transaction
	_, userOutput1PrivKey, _, err := createTestTokenMintTransaction(config, tokenPrivKey.Public())
	require.NoError(t, err, "failed to create test token issuance transaction")

	userOneConfig, err := sparktesting.TestWalletConfigWithIdentityKey(userOutput1PrivKey)
	require.NoError(t, err, "failed to create test user one wallet config")

	correctNetworkResponse, err := wallet.QueryTokenOutputs(
		t.Context(),
		userOneConfig,
		[]keys.Public{tokenPrivKey.Public()},
		nil,
	)
	require.NoError(t, err, "failed to query token outputs")
	require.Len(t, correctNetworkResponse.OutputsWithPreviousTransactionData, 1, "expected one outputs when using the correct network")

	wrongNetworkConfig := userOneConfig
	wrongNetworkConfig.Network = common.Mainnet

	wrongNetworkResponse, err := wallet.QueryTokenOutputs(
		t.Context(),
		wrongNetworkConfig,
		[]keys.Public{tokenPrivKey.Public()},
		nil,
	)
	require.NoError(t, err, "failed to query token outputs")
	require.Empty(t, wrongNetworkResponse.OutputsWithPreviousTransactionData, "expected no outputs when using a different network")
}

func TestBroadcastTokenTransactionMintAndTransferTokensExpectedOutputAndTxRetrieval(t *testing.T) {
	// Use a fresh issuer key for this test to avoid cross-test interference.
	issuerPrivKey := getRandomPrivateKey(t)
	config, err := sparktesting.TestWalletConfigWithIdentityKey(issuerPrivKey)
	require.NoError(t, err, "failed to create wallet config")

	// Create a native Spark token for this issuer so that subsequent
	// mint/transfer operations are scoped to this isolated token.
	err = testCoordinatedCreateNativeSparkTokenWithParams(t, config, createNativeSparkTokenParams{
		IssuerPrivateKey: issuerPrivKey,
		Name:             TestTokenName,
		Ticker:           TestTokenTicker,
		MaxSupply:        TestTokenMaxSupply,
	})
	require.NoError(t, err, "failed to create native spark token")

	tokenPrivKey := config.IdentityPrivateKey
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransaction(config, tokenPrivKey.Public())
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		t.Context(), config, issueTokenTransaction,
		[]keys.Private{tokenPrivKey},
		[]keys.Public{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %s", logging.FormatProto("token_transaction", finalIssueTokenTransaction))

	// Validate withdrawal params match config
	for i, output := range finalIssueTokenTransaction.TokenOutputs {
		if output.GetWithdrawBondSats() != WithdrawalBondSatsInConfig {
			t.Errorf("output %d: expected withdrawal bond sats 10000, got %d", i, output.GetWithdrawBondSats())
		}
		if output.GetWithdrawRelativeBlockLocktime() != uint64(WithdrawalRelativeBlockLocktimeInConfig) {
			t.Errorf("output %d: expected withdrawal relative block locktime 1000, got %d", i, output.GetWithdrawRelativeBlockLocktime())
		}
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransactionV0(finalIssueTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance token transaction: %v", err)
	}
	transferTokenTransaction, userOutput3PrivKey, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenPrivKey.Public(),
	)
	if err != nil {
		t.Fatal(err)
	}
	userOutput3PubKeyBytes := userOutput3PrivKey.Public().Serialize()

	revPubKey1, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[0].RevocationCommitment)
	require.NoError(t, err)
	revPubKey2, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[1].RevocationCommitment)
	require.NoError(t, err)

	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		t.Context(), config, transferTokenTransaction,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
		[]keys.Public{revPubKey1, revPubKey2},
	)
	if err != nil {
		t.Fatalf("failed to broadcast transfer token transaction: %v", err)
	}
	log.Printf("transfer broadcast finalized token transaction: %s", logging.FormatProto("token_transaction", transferTokenTransactionResponse))

	// Query token transactions with pagination - first page
	tokenTransactionsPage1, err := wallet.QueryTokenTransactions(
		t.Context(),
		config,
		[]keys.Public{tokenPrivKey.Public()}, // token public key
		nil,                                  // owner public keys
		nil,                                  // output IDs
		nil,                                  // transaction hashes
		0,                                    // offset
		1,                                    // limit - only get 1 transaction
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
		t.Context(),
		config,
		[]keys.Public{tokenPrivKey.Public()}, // token public key
		nil,                                  // owner public keys
		nil,                                  // output IDs
		nil,                                  // transaction hashes
		tokenTransactionsPage1.Offset,        // offset - use the offset from previous response (1)
		1,                                    // limit - only get 1 transaction
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
	mintPubKey, err := keys.ParsePublicKey(mintTx.GetMintInput().GetIssuerPublicKey())
	require.NoError(t, err)
	if !mintPubKey.Equals(issuerPrivKey.Public()) {
		t.Fatal("mint transaction issuer public key does not match expected")
	}

	// Query token transactions with pagination - third page (should be empty)
	tokenTransactionsPage3, err := wallet.QueryTokenTransactions(
		t.Context(),
		config,
		[]keys.Public{issuerPrivKey.Public()}, // token public key
		nil,                                   // owner public keys
		nil,                                   // output IDs
		nil,                                   // transaction hashes
		tokenTransactionsPage2.Offset,         // offset - use the offset from previous response
		1,                                     // limit - only get 1 transaction
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

	userOutput1Pubkey := userOutput1PrivKey.Public().Serialize()
	userOutput2Pubkey := userOutput2PrivKey.Public().Serialize()

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
	config, err := sparktesting.TestWalletConfigWithIdentityKey(staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	// Try to create issuance transaction with 101 outputs (should fail)
	tooBigIssuanceTransaction, _, err := createTestTokenMintTransactionWithMultipleTokenOutputs(config, tokenPrivKey.Public(), 101)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Attempt to broadcast the issuance transaction with too many outputs
	_, err = wallet.BroadcastTokenTransaction(
		t.Context(), config, tooBigIssuanceTransaction,
		[]keys.Private{tokenPrivKey},
		[]keys.Public{})
	require.Error(t, err, "expected error when broadcasting issuance transaction with more than 100 created outputs")

	// Create issuance transaction with 100 outputs
	issueTokenTransactionFirst100, userOutputPrivKeysFirst100, err := createTestTokenMintTransactionWithMultipleTokenOutputs(config,
		tokenPrivKey.Public(), manyOutputsCount)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Broadcast the issuance transaction
	finalIssueTokenTransactionFirst100, err := wallet.BroadcastTokenTransaction(
		t.Context(), config, issueTokenTransactionFirst100,
		[]keys.Private{tokenPrivKey},
		[]keys.Public{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %s", logging.FormatProto("token_transaction", finalIssueTokenTransactionFirst100))

	// Create issuance transaction with 100 outputs
	issueTokenTransactionSecond100, userOutputPrivKeysSecond100, err := createTestTokenMintTransactionWithMultipleTokenOutputs(config,
		tokenPrivKey.Public(), manyOutputsCount)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Broadcast the issuance transaction
	finalIssueTokenTransactionSecond100, err := wallet.BroadcastTokenTransaction(
		t.Context(), config, issueTokenTransactionSecond100,
		[]keys.Private{tokenPrivKey},
		[]keys.Public{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %s", logging.FormatProto("token_transaction", finalIssueTokenTransactionSecond100))

	finalIssueTokenTransactionHashFirst100, err := utils.HashTokenTransactionV0(finalIssueTokenTransactionFirst100, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	finalIssueTokenTransactionHashSecond100, err := utils.HashTokenTransactionV0(finalIssueTokenTransactionSecond100, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	// Create consolidation transaction
	consolidatedOutputPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to generate private key")

	consolidatedOutputPubKeyBytes := consolidatedOutputPrivKey.Public().Serialize()

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
				TokenPublicKey: tokenPrivKey.Public().Serialize(),
				TokenAmount:    int64ToUint128Bytes(0, uint64(testIssueMultiplePerOutputAmount)*uint64(manyOutputsCount)),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
	}

	// Combine private keys from both issuance transactions
	allUserOutputPrivKeys := append(userOutputPrivKeysFirst100, userOutputPrivKeysSecond100...)

	// Collect all revocation public keys from both transactions
	allRevPubKeys := make([]keys.Public, 200)
	for i := 0; i < 100; i++ {
		key1, err := keys.ParsePublicKey(finalIssueTokenTransactionFirst100.TokenOutputs[i].RevocationCommitment)
		require.NoError(t, err)
		allRevPubKeys[i] = key1
		key2, err := keys.ParsePublicKey(finalIssueTokenTransactionSecond100.TokenOutputs[i].RevocationCommitment)
		require.NoError(t, err)
		allRevPubKeys[i+100] = key2
	}

	// Broadcast the consolidation transaction
	_, err = wallet.BroadcastTokenTransaction(
		t.Context(), config, tooManyTransaction,
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
				TokenPublicKey: tokenPrivKey.Public().Serialize(),
				TokenAmount:    int64ToUint128Bytes(0, uint64(testIssueMultiplePerOutputAmount)*uint64(manyOutputsCount)),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
	}

	// Collect all revocation public keys
	revPubKeys := make([]keys.Public, 100)
	for i := 0; i < 100; i++ {
		key, err := keys.ParsePublicKey(finalIssueTokenTransactionFirst100.TokenOutputs[i].RevocationCommitment)
		require.NoError(t, err)
		revPubKeys[i] = key
	}

	// Broadcast the consolidation transaction
	_, err = wallet.BroadcastTokenTransaction(
		t.Context(), config, consolidateTransaction,
		userOutputPrivKeysFirst100,
		revPubKeys,
	)
	require.NoError(t, err, "failed to broadcast consolidation transaction")

	// Verify the consolidated amount
	tokenOutputsResponse, err := wallet.QueryTokenOutputs(
		t.Context(),
		config,
		[]keys.Public{consolidatedOutputPrivKey.Public()},
		[]keys.Public{tokenPrivKey.Public()},
	)
	require.NoError(t, err, "failed to get owned token outputs")

	require.Len(t, tokenOutputsResponse.OutputsWithPreviousTransactionData, 1, "expected 1 consolidated output")
}

func TestV0FreezeAndUnfreezeTokens(t *testing.T) {
	config, err := sparktesting.TestWalletConfigWithIdentityKey(staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")
	tokenPrivKey := config.IdentityPrivateKey
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransaction(config, tokenPrivKey.Public())
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Broadcast the token transaction
	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		t.Context(), config, issueTokenTransaction,
		[]keys.Private{tokenPrivKey},
		[]keys.Public{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %s", logging.FormatProto("token_transaction", finalIssueTokenTransaction))

	// Validate withdrawal params match config
	for i, output := range finalIssueTokenTransaction.TokenOutputs {
		require.Equal(t, uint64(WithdrawalBondSatsInConfig), output.GetWithdrawBondSats(),
			"output %d: expected withdrawal bond sats %d, got %d", i, uint64(WithdrawalBondSatsInConfig), output.GetWithdrawBondSats())
		require.Equal(t, uint64(WithdrawalRelativeBlockLocktimeInConfig), output.GetWithdrawRelativeBlockLocktime(),
			"output %d: expected withdrawal relative block locktime %d, got %d", i, uint64(WithdrawalRelativeBlockLocktimeInConfig), output.GetWithdrawRelativeBlockLocktime())
	}

	// Call FreezeTokens to freeze the created output
	ownerPublicKey, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[0].OwnerPublicKey)
	require.NoError(t, err)
	freezeResponse, err := wallet.FreezeTokens(
		t.Context(),
		config,
		ownerPublicKey,        // owner public key of the output to freeze
		tokenPrivKey.Public(), // token public key
		false,                 // unfreeze
	)
	require.NoError(t, err, "failed to freeze tokens")

	// Convert frozen amount bytes to big.Int for comparison
	frozenAmount := new(big.Int).SetBytes(freezeResponse.ImpactedTokenAmount)

	// Calculate total amount from transaction created outputs
	expectedAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestIssueOutput1Amount))
	expectedOutputID := finalIssueTokenTransaction.TokenOutputs[0].Id

	require.Equal(t, 0, frozenAmount.Cmp(expectedAmount),
		"frozen amount %s does not match expected amount %s", frozenAmount.String(), expectedAmount.String())
	require.Len(t, freezeResponse.ImpactedOutputIds, 1, "expected 1 impacted output ID")
	require.Equal(t, *expectedOutputID, freezeResponse.ImpactedOutputIds[0],
		"frozen output ID %s does not match expected output ID %s", freezeResponse.ImpactedOutputIds[0], *expectedOutputID)

	finalIssueTokenTransactionHash, err := utils.HashTokenTransactionV0(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final transfer token transaction")

	transferTokenTransaction, _, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenPrivKey.Public(),
	)
	require.NoError(t, err, "failed to create test token transfer transaction")

	revPubKey1, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[0].RevocationCommitment)
	require.NoError(t, err)
	revPubKey2, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[1].RevocationCommitment)
	require.NoError(t, err)

	// Broadcast the token transaction
	transferFrozenTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		t.Context(), config, transferTokenTransaction,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
		[]keys.Public{revPubKey1, revPubKey2},
	)
	require.Error(t, err, "expected error when transferring frozen tokens")
	require.Nil(t, transferFrozenTokenTransactionResponse, "expected nil response when transferring frozen tokens")
	log.Printf("successfully froze tokens with response: %s", logging.FormatProto("freeze_response", freezeResponse))

	// Call FreezeTokens to thaw the created output
	unfreezeResponse, err := wallet.FreezeTokens(
		t.Context(),
		config,
		ownerPublicKey, // owner public key of the output to freeze
		tokenPrivKey.Public(),
		true, // unfreeze
	)
	require.NoError(t, err, "failed to unfreeze tokens")

	// Convert frozen amount bytes to big.Int for comparison
	thawedAmount := new(big.Int).SetBytes(unfreezeResponse.ImpactedTokenAmount)

	require.Equal(t, 0, thawedAmount.Cmp(expectedAmount),
		"thawed amount %s does not match expected amount %s", thawedAmount.String(), expectedAmount.String())
	require.Len(t, unfreezeResponse.ImpactedOutputIds, 1, "expected 1 impacted output ID")
	require.Equal(t, *expectedOutputID, unfreezeResponse.ImpactedOutputIds[0],
		"thawed output ID %s does not match expected output ID %s", unfreezeResponse.ImpactedOutputIds[0], *expectedOutputID)

	// Broadcast the token transaction
	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		t.Context(), config, transferTokenTransaction,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
		[]keys.Public{revPubKey1, revPubKey2},
	)
	require.NoError(t, err, "failed to broadcast thawed token transaction")
	require.NotNil(t, transferTokenTransactionResponse, "expected non-nil response when transferring thawed tokens")
	log.Printf("thawed token transfer broadcast finalized token transaction: %s", logging.FormatProto("token_transaction", transferTokenTransactionResponse))
}

// Enables creation of a unique issuer key for each token creation to avoid duplicate key errors across tests.
func getRandomPrivateKey(t *testing.T) keys.Private {
	uniqueIssuerPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to generate unique issuer private key")
	return uniqueIssuerPrivKey
}

// Helper function for testing token mint transaction with various signing scenarios
// Parameters:
// - t: testing context
// - config: wallet configuration
// - ownerSigningPrivateKeys: custom private keys to use for signing inputs
// - testDoubleStart: whether to test double start
// - testDoubleStartDifferentOperator: whether to test double start with a different coordinator
// - testDoubleSign: whether to test double signing
// - testSignExpired: whether to test signing with an expired transaction
// - testDifferentTx: whether to test signing with a different transaction than was started
// - testInvalidSigningOperatorPublicKey: whether to test signing with an invalid operator public key in the payload
// - expectedStartError: whether an error is expected during the start operation
// - expectedSigningError: whether an error is expected during any of the signing operations
func testMintTransactionSigningScenarios(t *testing.T, config *wallet.TestWalletConfig,
	ownerSigningPrivateKeys []keys.Private,
	testDoubleStart bool,
	testDoubleStartDifferentOperator bool,
	testDoubleSign bool,
	testSignExpired bool,
	testSignDifferentTx bool,
	testInvalidSigningOperatorPublicKey bool,
	expectedStartError bool,
	expectedSigningError bool,
) (*pb.TokenTransaction, keys.Private, keys.Private) {
	if ownerSigningPrivateKeys == nil {
		ownerSigningPrivateKeys = []keys.Private{config.IdentityPrivateKey}
	}

	tokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionWithParams(config, config.IdentityPublicKey())
	require.NoError(t, err, "failed to create test token mint transaction")

	var startResp *pb.StartTokenTransactionResponse
	var finalTxHash []byte
	var startErrorOccurred bool

	if testDoubleStart {
		startResp, _, finalTxHash, err = wallet.StartTokenTransaction(
			t.Context(), config, tokenTransaction, ownerSigningPrivateKeys, nil,
		)
		require.NoError(t, err, "failed to start token transaction first time")

		startResp2, _, finalTxHash2, err := wallet.StartTokenTransaction(
			t.Context(), config, tokenTransaction, ownerSigningPrivateKeys, nil,
		)
		require.NoError(t, err, "failed to start token transaction second time")

		require.Equal(t, finalTxHash, finalTxHash2, "transaction hashes should be identical")

		hash1, err := utils.HashTokenTransactionV0(startResp.FinalTokenTransaction, false)
		require.NoError(t, err, "failed to hash first final token transaction")

		hash2, err := utils.HashTokenTransactionV0(startResp2.FinalTokenTransaction, false)
		require.NoError(t, err, "failed to hash second final token transaction")

		require.Equal(t, hash1, hash2, "final transactions should hash to identical values")

	} else if testDoubleStartDifferentOperator {
		_, _, _, err = wallet.StartTokenTransaction(
			t.Context(), config, tokenTransaction, ownerSigningPrivateKeys, nil,
		)
		require.NoError(t, err, "failed to start token transaction first time")

		modifiedConfig := *config
		differentCoordinatorID, err := getNonCoordinatorOperator(config)
		require.NoError(t, err, "failed to find a different coordinator identifier")
		modifiedConfig.CoordinatorIdentifier = differentCoordinatorID

		startResp, _, finalTxHash, err = wallet.StartTokenTransaction(
			t.Context(), &modifiedConfig, tokenTransaction, ownerSigningPrivateKeys,
			nil,
		)
		require.NoError(t, err, "failed to start mint token transaction second time with different coordinator")
	} else {
		startResp, _, finalTxHash, err = wallet.StartTokenTransaction(
			t.Context(), config, tokenTransaction, ownerSigningPrivateKeys, nil,
		)
		if err != nil {
			startErrorOccurred = true
			log.Printf("error when starting the mint transaction: %v", err)
		}

		if expectedStartError {
			require.True(t, startErrorOccurred, "expected an error mint transfer start operation but none occurred")
			return nil, keys.Private{}, keys.Private{}
		}
		require.NoError(t, err, "failed to start mint token transaction")
	}

	txToSign := startResp.FinalTokenTransaction
	if testSignDifferentTx {
		differentIssueTokenTransaction, _, _, err := createTestTokenMintTransaction(config, config.IdentityPublicKey())
		require.NoError(t, err, "failed to create different test token issuance transaction")
		txToSign = differentIssueTokenTransaction
	}

	if testInvalidSigningOperatorPublicKey {
		// Generate a new random key to replace the valid one
		randomKey, err := keys.GeneratePrivateKey()
		require.NoError(t, err, "failed to generate random key")
		for operatorID := range config.SigningOperators {
			config.SigningOperators[operatorID].IdentityPublicKey = randomKey.Public()
			break // Only modify the first operator
		}
	}

	errorOccurred := false
	var halfSignOperatorSignatures wallet.OperatorSignatures
	if testDoubleSign {
		operatorKeys := splitOperatorIdentityPublicKeys(config)
		// Sign with half the operators to get in a partial signed state
		_, halfSignOperatorSignatures, err = wallet.SignTokenTransaction(
			t.Context(),
			config,
			startResp.FinalTokenTransaction, // Always use the original transaction for first sign (if double signing)
			finalTxHash,
			operatorKeys.firstHalf,
			ownerSigningPrivateKeys,
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
		t.Context(),
		config,
		txToSign,
		finalTxHash,
		nil, // Default to contact all operators
		ownerSigningPrivateKeys,
		nil,
	)
	if err != nil {
		errorOccurred = true
		log.Printf("error when signing the mint transaction: %v", err)
	}

	if expectedSigningError {
		require.True(t, errorOccurred, "expected an error during mint signing operation but none occurred")
		return nil, keys.Private{}, keys.Private{}
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
	log.Printf("mint transaction finalized: %s", logging.FormatProto("token_transaction", finalIssueTokenTransaction))
	return finalIssueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey
}

// TestTokenMintTransactionSigning tests various signing scenarios for token mint transactions
func TestTokenMintTransactionSigning(t *testing.T) {
	testCases := []struct {
		name                            string
		ownerSigningPrivateKeys         []keys.Private
		explicitWalletPrivateKey        keys.Private
		createNativeSparkToken          bool
		doubleStart                     bool
		doubleStartDifferentOperator    bool
		doubleSign                      bool
		expiredSign                     bool
		differentMintTx                 bool
		invalidSigningOperatorPublicKey bool
		expectedStartError              bool
		expectedSigningError            bool
	}{
		{
			name: "mint should succeed with l1 token without token identifier",
		},
		{
			name:                     "mint should succeed with native spark token without token identifier",
			createNativeSparkToken:   true,
			explicitWalletPrivateKey: getRandomPrivateKey(t),
		},
		{
			name:                     "mint should fail with no associated token create",
			expectedStartError:       true,
			explicitWalletPrivateKey: getRandomPrivateKey(t),
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
			name: "mint should fail with too many issuer signing keys",
			ownerSigningPrivateKeys: []keys.Private{
				staticLocalIssuerKey.IdentityPrivateKey(),
				staticLocalIssuerKey.IdentityPrivateKey(),
			},
			expectedSigningError: true,
		},
		{
			name:                            "mint should fail with invalid signing operator public key",
			invalidSigningOperatorPublicKey: true,
			expectedSigningError:            true,
		},
		{
			name:                    "mint should fail with incorrect issuer private key",
			ownerSigningPrivateKeys: []keys.Private{getRandomPrivateKey(t)},
			expectedSigningError:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var issuerPrivateKey keys.Private
			if tc.explicitWalletPrivateKey != (keys.Private{}) {
				issuerPrivateKey = tc.explicitWalletPrivateKey
			} else {
				issuerPrivateKey = staticLocalIssuerKey.IdentityPrivateKey()
			}
			config, err := sparktesting.TestWalletConfigWithIdentityKey(issuerPrivateKey)
			require.NoError(t, err, "failed to create wallet config")

			if tc.createNativeSparkToken {
				err := testCreateNativeSparkTokenWithParams(t, config, issuerPrivateKey, TestTokenName, TestTokenTicker, TestTokenMaxSupply)
				require.NoError(t, err, "failed to create native spark token")
			}
			testMintTransactionSigningScenarios(
				t, config,
				tc.ownerSigningPrivateKeys,
				tc.doubleStart,
				tc.doubleStartDifferentOperator,
				tc.doubleSign,
				tc.expiredSign,
				tc.differentMintTx,
				tc.invalidSigningOperatorPublicKey,
				tc.expectedStartError,
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
// - createNativeSparkToken: whether to use the native spark token
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
func testTransferTransactionSigningScenarios(t *testing.T, config *wallet.TestWalletConfig,
	finalIssueTokenTransaction *pb.TokenTransaction,
	startingOwnerPrivateKeys []keys.Private,
	signingOwnerPrivateKeys []keys.Private,
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
	if signingOwnerPrivateKeys == nil {
		signingOwnerPrivateKeys = startingOwnerPrivateKeys
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransactionV0(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	transferTokenTransaction, _, err := createTestTokenTransferTransactionWithParams(config,
		finalIssueTokenTransactionHash,
		config.IdentityPublicKey(),
	)
	require.NoError(t, err, "failed to create test token transfer transaction")

	revPubKey1, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[0].RevocationCommitment)
	require.NoError(t, err)
	revPubKey2, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[1].RevocationCommitment)
	require.NoError(t, err)

	var transferStartResp *pb.StartTokenTransactionResponse
	var transferFinalTxHash []byte
	var startErrorOccurred bool

	if testDoubleStart {
		transferStartResp, _, transferFinalTxHash, err = wallet.StartTokenTransaction(
			t.Context(), config, transferTokenTransaction, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)
		require.NoError(t, err, "failed to start token transaction first time")

		transferStartResp2, _, transferFinalTxHash2, err := wallet.StartTokenTransaction(
			t.Context(), config, transferTokenTransaction, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)

		require.NoError(t, err, "failed to start token transaction second time")

		require.Equal(t, transferFinalTxHash, transferFinalTxHash2, "transaction hashes should be identical")

		hash1, err := utils.HashTokenTransactionV0(transferStartResp.FinalTokenTransaction, false)
		require.NoError(t, err, "failed to hash first final token transaction")

		hash2, err := utils.HashTokenTransactionV0(transferStartResp2.FinalTokenTransaction, false)
		require.NoError(t, err, "failed to hash second final token transaction")

		require.Equal(t, hash1, hash2, "final transactions should hash to identical values")
	} else if testDoubleStartDifferentTransaction {
		secondTxToStart := cloneTransferTransactionWithDifferentOutputOwner(
			transferTokenTransaction,
			signingOwnerPrivateKeys[0].Public(),
		)

		transferStartResp1, _, transferFinalTxHash1, err := wallet.StartTokenTransaction(
			t.Context(), config, transferTokenTransaction, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)
		require.NoError(t, err, "failed to start token transaction first time")

		transferStartResp2, _, transferFinalTxHash2, err := wallet.StartTokenTransaction(
			t.Context(), config, secondTxToStart, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)
		require.NoError(t, err, "failed to start token transaction second time")

		// Verify the hashes are different for different transactions
		require.NotEqual(t, transferFinalTxHash1, transferFinalTxHash2, "transaction hashes should be different for different transactions")

		if testDoubleStartSignFirst {
			transferStartResp = transferStartResp1
			transferFinalTxHash = transferFinalTxHash1
		} else {
			transferStartResp = transferStartResp2
			transferFinalTxHash = transferFinalTxHash2
		}
	} else if testDoubleStartDifferentOperator {
		transferStartRespInitial, _, _, err := wallet.StartTokenTransaction(
			t.Context(), config, transferTokenTransaction, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)
		require.NoError(t, err, "failed to start token transaction first time")

		modifiedConfig := *config
		differentCoordinatorID, err := getNonCoordinatorOperator(config)
		require.NoError(t, err, "failed to find a different coordinator identifier")
		modifiedConfig.CoordinatorIdentifier = differentCoordinatorID

		// Use this for later signing because once executed, the outputs previously mapped to that transaction
		// are remapped to the new transaction in the database.
		transferStartResp, _, transferFinalTxHash, err = wallet.StartTokenTransaction(
			t.Context(), &modifiedConfig, transferTokenTransaction, startingOwnerPrivateKeys, startSignatureIndexOrder,
		)

		require.NoError(t, err, "failed to start token transaction second time with different coordinator")
		require.NotNil(t, transferStartResp, "expected non-nil response from second start")

		verifyDifferentTransactionOutputs(t, transferStartRespInitial.FinalTokenTransaction, transferStartResp.FinalTokenTransaction)
	} else {
		transferStartResp, _, transferFinalTxHash, err = wallet.StartTokenTransaction(
			t.Context(), config, transferTokenTransaction, startingOwnerPrivateKeys, startSignatureIndexOrder,
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
			signingOwnerPrivateKeys[0].Public(),
		)
	}

	if testInvalidSigningOperatorPublicKey {
		// Generate a new random key to replace the valid one
		randomKey, err := keys.GeneratePrivateKey()
		require.NoError(t, err, "failed to generate random key")
		for operatorID := range config.SigningOperators {
			config.SigningOperators[operatorID].IdentityPublicKey = randomKey.Public()
			break // Only modify the first operator
		}
	}

	// If testing double signing, first sign with half the operators
	var halfSignOperatorSignatures wallet.OperatorSignatures
	if testDoubleSign {
		operatorKeys := splitOperatorIdentityPublicKeys(config)
		_, halfSignOperatorSignatures, err = wallet.SignTokenTransaction(
			t.Context(),
			config,
			transferStartResp.FinalTokenTransaction, // Always use original transaction for first sign
			transferFinalTxHash,
			operatorKeys.firstHalf,
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
		t.Context(),
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
		t.Context(),
		config,
		transferStartResp.FinalTokenTransaction,
		nil, // Default to contact all operators
		signResponseTransferKeyshares,
		[]keys.Public{revPubKey1, revPubKey2},
	)

	require.NoError(t, err, "failed to finalize the transfer transaction")
	log.Printf("transfer transaction finalized: %s", logging.FormatProto("token_transaction", transferStartResp.FinalTokenTransaction))
}

// TestTokenTransferTransactionSigning tests various signing scenarios for token transfer transactions
func TestTokenTransferTransactionSigning(t *testing.T) {
	testCases := []struct {
		name                            string
		startOwnerPrivateKeysModifier   func([]keys.Private) []keys.Private
		startSignatureIndexOrder        []uint32
		explicitWalletPrivateKey        keys.Private
		createNativeSparkToken          bool
		doubleStart                     bool
		doubleStartDifferentOperator    bool
		doubleStartSignFirst            bool
		doubleStartDifferentTx          bool
		doubleSign                      bool
		expiredSign                     bool
		signDifferentTx                 bool
		signingOwnerPrivateKeysModifier func([]keys.Private) []keys.Private
		signingOwnerSignatureIndexOrder []uint32
		invalidSigningOperatorPublicKey bool
		expectedStartError              bool
		expectedSigningError            bool
	}{
		{
			name: "transfer should succeed with l1 token",
		},

		{
			name:                     "transfer should succeed with native spark token without token identifier",
			createNativeSparkToken:   true,
			explicitWalletPrivateKey: getRandomPrivateKey(t),
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
			startOwnerPrivateKeysModifier: func(tokenOutputs []keys.Private) []keys.Private {
				return []keys.Private{tokenOutputs[1], tokenOutputs[0]}
			},
			expectedStartError: true,
		},
		{
			name: "start should fail with reversing the owner signatures and also the order of the signatures",
			startOwnerPrivateKeysModifier: func(tokenOutputs []keys.Private) []keys.Private {
				return []keys.Private{tokenOutputs[1], tokenOutputs[0]}
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
			signingOwnerPrivateKeysModifier: func(tokenOutputs []keys.Private) []keys.Private {
				return []keys.Private{tokenOutputs[0], tokenOutputs[0]}
			},
			expectedSigningError: true,
		},
		{
			name: "sign transfer should fail with reversing the operator specific owner signatures and also the order of the signatures",
			signingOwnerPrivateKeysModifier: func(tokenOutputs []keys.Private) []keys.Private {
				return []keys.Private{tokenOutputs[0], tokenOutputs[0]}
			},
			signingOwnerSignatureIndexOrder: []uint32{1, 0},
			expectedSigningError:            true,
		},
		{
			name: "sign transfer should fail with swapped owner signing private keys",
			signingOwnerPrivateKeysModifier: func(tokenOutputs []keys.Private) []keys.Private {
				return []keys.Private{tokenOutputs[1], tokenOutputs[0]}
			},
			expectedSigningError: true,
		},
		{
			name: "sign transfer should fail with not enough owner signing keys",
			signingOwnerPrivateKeysModifier: func(tokenOutputs []keys.Private) []keys.Private {
				return []keys.Private{tokenOutputs[0]}
			},
			expectedSigningError: true,
		},
		{
			name: "sign transfer should fail with too many owner signing keys",
			signingOwnerPrivateKeysModifier: func(tokenOutputs []keys.Private) []keys.Private {
				return []keys.Private{tokenOutputs[0], tokenOutputs[1], tokenOutputs[0]}
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
			var issuerPrivateKey keys.Private
			if tc.explicitWalletPrivateKey != (keys.Private{}) {
				issuerPrivateKey = tc.explicitWalletPrivateKey
			} else {
				issuerPrivateKey = staticLocalIssuerKey.IdentityPrivateKey()
			}

			config, err := sparktesting.TestWalletConfigWithIdentityKey(issuerPrivateKey)
			require.NoError(t, err, "failed to create wallet config")

			if tc.createNativeSparkToken {
				err := testCreateNativeSparkTokenWithParams(t, config, issuerPrivateKey, TestTokenName, TestTokenTicker, TestTokenMaxSupply)
				require.NoError(t, err, "failed to create native spark token")
			}

			// Create and finalize a mint transaction for this specific test case
			finalIssueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey := testMintTransactionSigningScenarios(
				t, config, nil, false, false, false, false, false, false, false, false)

			defaultStartingOwnerPrivateKeys := []keys.Private{userOutput1PrivKey, userOutput2PrivKey}
			var startingPrivKeys []keys.Private
			if tc.startOwnerPrivateKeysModifier != nil {
				startingPrivKeys = tc.startOwnerPrivateKeysModifier(defaultStartingOwnerPrivateKeys)
			} else {
				startingPrivKeys = defaultStartingOwnerPrivateKeys
			}
			var startSignatureIndexOrder []uint32
			if tc.startSignatureIndexOrder != nil {
				startSignatureIndexOrder = tc.startSignatureIndexOrder
			}

			var signingPrivKeys []keys.Private
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
	config, err := sparktesting.TestWalletConfigWithIdentityKey(staticLocalIssuerKey.IdentityPrivateKey())
	config.UseTokenTransactionSchnorrSignatures = true
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransaction(config, tokenPrivKey.Public())
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		t.Context(), config, issueTokenTransaction,
		[]keys.Private{tokenPrivKey},
		[]keys.Public{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %s", logging.FormatProto("token_transaction", finalIssueTokenTransaction))

	// Validate withdrawal params match config
	for i, output := range finalIssueTokenTransaction.TokenOutputs {
		require.Equal(t, uint64(WithdrawalBondSatsInConfig), output.GetWithdrawBondSats(),
			"output %d: expected withdrawal bond sats %d, got %d", i, uint64(WithdrawalBondSatsInConfig), output.GetWithdrawBondSats())
		require.Equal(t, uint64(WithdrawalRelativeBlockLocktimeInConfig), output.GetWithdrawRelativeBlockLocktime(),
			"output %d: expected withdrawal relative block locktime %d, got %d", i, uint64(WithdrawalRelativeBlockLocktimeInConfig), output.GetWithdrawRelativeBlockLocktime())
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransactionV0(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	transferTokenTransaction, _, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenPrivKey.Public(),
	)
	require.NoError(t, err, "failed to create test token transfer transaction")

	revPubKey1, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[0].RevocationCommitment)
	require.NoError(t, err)
	revPubKey2, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[1].RevocationCommitment)
	require.NoError(t, err)

	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		t.Context(), config, transferTokenTransaction,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
		[]keys.Public{revPubKey1, revPubKey2},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")
	log.Printf("transfer broadcast finalized token transaction: %s", logging.FormatProto("token_transaction", transferTokenTransactionResponse))
}

func TestV0FreezeAndUnfreezeTokensSchnorr(t *testing.T) {
	config, err := sparktesting.TestWalletConfigWithIdentityKey(staticLocalIssuerKey.IdentityPrivateKey())
	config.UseTokenTransactionSchnorrSignatures = true
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	issueTokenTransaction, _, _, err := createTestTokenMintTransaction(config, tokenPrivKey.Public())
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		t.Context(), config, issueTokenTransaction,
		[]keys.Private{tokenPrivKey},
		[]keys.Public{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %s", logging.FormatProto("token_transaction", finalIssueTokenTransaction))

	ownerPublicKey, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[0].OwnerPublicKey)
	require.NoError(t, err)

	_, err = wallet.FreezeTokens(
		t.Context(),
		config,
		ownerPublicKey,
		tokenPrivKey.Public(),
		false,
	)
	require.NoError(t, err, "failed to freeze tokens")
}

func TestBroadcastTokenTransactionWithInvalidPrevTxHash(t *testing.T) {
	config, err := sparktesting.TestWalletConfigWithIdentityKey(staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransaction(config, tokenPrivKey.Public())
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		t.Context(), config, issueTokenTransaction,
		[]keys.Private{tokenPrivKey},
		[]keys.Public{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %s", logging.FormatProto("token_transaction", finalIssueTokenTransaction))

	finalIssueTokenTransactionHash, err := utils.HashTokenTransactionV0(finalIssueTokenTransaction, false)
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
				OwnerPublicKey: userOutput1PrivKey.Public().Serialize(),
				TokenPublicKey: tokenPrivKey.Public().Serialize(),
				TokenAmount:    int64ToUint128Bytes(0, TestTransferOutput1Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
	}

	revPubKey1, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[0].RevocationCommitment)
	require.NoError(t, err)
	revPubKey2, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[1].RevocationCommitment)
	require.NoError(t, err)

	// Attempt to broadcast the transfer transaction with corrupted hash
	// This should fail validation
	_, err = wallet.BroadcastTokenTransaction(
		t.Context(), config, transferTokenTransaction,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
		[]keys.Public{revPubKey1, revPubKey2},
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
				OwnerPublicKey: userOutput1PrivKey.Public().Serialize(),
				TokenPublicKey: tokenPrivKey.Public().Serialize(),
				TokenAmount:    int64ToUint128Bytes(0, TestTransferOutput1Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
	}

	// Attempt to broadcast the second transfer transaction with corrupted hash
	_, err = wallet.BroadcastTokenTransaction(
		t.Context(), config, transferTokenTransaction2,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
		[]keys.Public{revPubKey1, revPubKey2},
	)

	require.Error(t, err, "expected transaction with second invalid hash to be rejected")
	log.Printf("successfully detected second invalid transaction hash: %v", err)
}

func TestBroadcastTokenTransactionUnspecifiedNetwork(t *testing.T) {
	config, err := sparktesting.TestWalletConfigWithIdentityKey(staticLocalIssuerKey.IdentityPrivateKey())
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	issueTokenTransaction, _, _, err := createTestTokenMintTransaction(config, tokenPrivKey.Public())
	require.NoError(t, err, "failed to create test token issuance transaction")
	issueTokenTransaction.Network = pb.Network_UNSPECIFIED

	_, err = wallet.BroadcastTokenTransaction(
		t.Context(), config, issueTokenTransaction,
		[]keys.Private{tokenPrivKey},
		[]keys.Public{})

	require.Error(t, err, "expected transaction without a network to be rejected")
	log.Printf("successfully detected unspecified network and rejected with error: %v", err)
}

// cloneTransferTransactionWithDifferentOutputOwner creates a copy of a transfer transaction
// with a modified owner public key in the first output
func cloneTransferTransactionWithDifferentOutputOwner(tx *pb.TokenTransaction, newOwnerPubKey keys.Public) *pb.TokenTransaction {
	clone := proto.CloneOf(tx)
	if len(clone.TokenOutputs) > 0 {
		clone.TokenOutputs[0].OwnerPublicKey = newOwnerPubKey.Serialize()
	}
	return clone
}

func verifyDifferentTransactionOutputs(t *testing.T, firstTx, secondTx *pb.TokenTransaction) {
	for i, output := range firstTx.TokenOutputs {
		secondOutput := secondTx.TokenOutputs[i]

		require.NotEqual(t, output.Id, secondOutput.Id,
			"expected different output IDs when starting with different coordinator")

		// Revocation commitments should be different
		require.NotEqual(t, output.RevocationCommitment, secondOutput.RevocationCommitment,
			"expected different revocation commitments when starting with different coordinator")
	}

	hash1, err := utils.HashTokenTransactionV0(firstTx, false)
	require.NoError(t, err, "failed to hash first final token transaction")

	hash2, err := utils.HashTokenTransactionV0(secondTx, false)
	require.NoError(t, err, "failed to hash second final token transaction")

	require.NotEqual(t, hash1, hash2, "transaction hashes should be different when double starting with different coordinator")
}

func getNonCoordinatorOperator(config *wallet.TestWalletConfig) (string, error) {
	for id := range config.SigningOperators {
		if id != config.CoordinatorIdentifier {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not find a non-coordinator operator")
}

// verifyTokenOutputs verifies that a transaction's outputs are properly finalized by querying them

// TestCreateNativeSparkToken tests various token creation scenarios
func TestCreateNativeSparkToken(t *testing.T) {
	fixedRandomKey := getRandomPrivateKey(t)

	testCases := []struct {
		name              string
		firstTokenParams  sparkTokenCreationTestParams
		secondTokenParams *sparkTokenCreationTestParams
	}{
		{
			name: "create second token with same issuer key should fail",
			firstTokenParams: sparkTokenCreationTestParams{
				issuerPrivateKey: fixedRandomKey,
				name:             TestTokenName,
				ticker:           TestTokenTicker,
				maxSupply:        TestTokenMaxSupply,
			},
			secondTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: fixedRandomKey,
				name:             "Different Name",
				ticker:           "DIFF",
				maxSupply:        TestTokenMaxSupply + 1000,
				expectedError:    true,
			},
		},
		{
			name: "create two tokens with same metadata but different random keys should succeed",
			firstTokenParams: sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             TestTokenName,
				ticker:           TestTokenTicker,
				maxSupply:        TestTokenMaxSupply,
			},
			secondTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             "Different Name",
				ticker:           "DIFF",
				maxSupply:        TestTokenMaxSupply,
			},
		},
		{
			name: "create two tokens with different metadata and different random keys should succeed",
			firstTokenParams: sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             TestTokenName,
				ticker:           TestTokenTicker,
				maxSupply:        TestTokenMaxSupply,
			},
			secondTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             "Different Name",
				ticker:           "DIFF",
				maxSupply:        TestTokenMaxSupply + 1000,
			},
		},
		{
			name: "create token with name longer than 20 characters should fail",
			firstTokenParams: sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             "This Token Name Is Way Too Long For The System",
				ticker:           TestTokenTicker,
				maxSupply:        TestTokenMaxSupply,
				expectedError:    true,
			},
		},
		{
			name: "create token with empty name should fail",
			firstTokenParams: sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             "",
				ticker:           TestTokenTicker,
				maxSupply:        TestTokenMaxSupply,
				expectedError:    true,
			},
		},
		{
			name: "create token with empty ticker should fail",
			firstTokenParams: sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             TestTokenName,
				ticker:           "",
				maxSupply:        TestTokenMaxSupply,
				expectedError:    true,
			},
		},
		{
			name: "create token with ticker longer than 5 characters should fail",
			firstTokenParams: sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             TestTokenName,
				ticker:           "TOOLONG",
				maxSupply:        TestTokenMaxSupply,
				expectedError:    true,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			firstTokenConfig, err := sparktesting.TestWalletConfigWithIdentityKey(tc.firstTokenParams.issuerPrivateKey)
			require.NoError(t, err, "failed to create wallet for first token")

			// Create first token
			err = testCreateNativeSparkTokenWithParams(
				t,
				firstTokenConfig,
				tc.firstTokenParams.issuerPrivateKey,
				tc.firstTokenParams.name,
				tc.firstTokenParams.ticker,
				tc.firstTokenParams.maxSupply,
			)

			if tc.firstTokenParams.expectedError {
				require.Error(t, err, "expected error but got none for first token creation")
				return
			}
			require.NoError(t, err, "unexpected error during first token creation")

			// Create second token if needed
			if tc.secondTokenParams != nil {
				secondTokenConfig, err := sparktesting.TestWalletConfigWithIdentityKey(tc.secondTokenParams.issuerPrivateKey)
				require.NoError(t, err, "failed to create wallet for second token")

				err = testCreateNativeSparkTokenWithParams(
					t,
					secondTokenConfig,
					tc.secondTokenParams.issuerPrivateKey,
					tc.secondTokenParams.name,
					tc.secondTokenParams.ticker,
					tc.secondTokenParams.maxSupply,
				)
				if tc.secondTokenParams.expectedError {
					require.Error(t, err, "expected error but got none for second token creation")
				} else {
					require.NoError(t, err, "unexpected error during second token creation")
				}
			}
		})
	}
}

// createTestTokenCreateTransactionWithParams creates a token transaction with custom parameters
func createTestTokenCreateTransactionWithParams(config *wallet.TestWalletConfig, issuerPubKey keys.Public, name string, ticker string, maxSupply uint64) (*pb.TokenTransaction, error) {
	createTokenTransaction := &pb.TokenTransaction{
		TokenInputs: &pb.TokenTransaction_CreateInput{
			CreateInput: &pb.TokenCreateInput{
				IssuerPublicKey: issuerPubKey.Serialize(),
				TokenName:       name,
				TokenTicker:     ticker,
				Decimals:        uint32(TestTokenDecimals),
				IsFreezable:     TestTokenIsFreezable,
				MaxSupply:       getTokenMaxSupplyBytes(maxSupply),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
	}

	return createTokenTransaction, nil
}

// testCreateNativeSparkTokenWithParams creates a native spark token with custom parameters
func testCreateNativeSparkTokenWithParams(t *testing.T, config *wallet.TestWalletConfig, issuerPrivateKey keys.Private, name string, ticker string, maxSupply uint64) error {
	createTokenTransaction, err := createTestTokenCreateTransactionWithParams(config, issuerPrivateKey.Public(), name, ticker, maxSupply)
	if err != nil {
		return err
	}
	_, err = wallet.BroadcastTokenTransaction(
		t.Context(),
		config,
		createTokenTransaction,
		[]keys.Private{issuerPrivateKey},
		nil,
	)
	if err != nil {
		return err
	}
	log.Printf("token create transaction finalized: %s", logging.FormatProto("token_transaction", createTokenTransaction))
	return nil
}
