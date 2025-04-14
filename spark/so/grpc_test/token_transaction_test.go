package grpctest

import (
	"bytes"
	"context"
	"encoding/binary"
	"log"
	"math/big"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/utils"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// Test token amounts for various operations
const (
	// The expected maximum number of leaves which can be created in a single transaction.
	ManyLeavesCount = 100
	// Amount for first output leaf in issuance transaction
	TestIssueLeaf1Amount = 11111
	// Amount for second output leaf in issuance transaction
	TestIssueLeaf2Amount = 22222
	// Amount for second output leaf in multiple leaf issuance transaction
	TestIssueMultiplePerLeafAmount = 1000
	// Amount for first (and only) output leaf in transfer transaction
	TestTransferLeaf1Amount = 33333
	// Configured at SO level. We validate in the tests to ensure these are populated correctly.
	WithdrawalBondSatsInConfig              = 1000000
	WithdrawalRelativeBlockLocktimeInConfig = 1000
)

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
	// Generate two user leaf key pairs
	userLeaf1PrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, nil, err
	}
	userLeaf1PubKeyBytes := userLeaf1PrivKey.PubKey().SerializeCompressed()

	userLeaf2PrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, nil, err
	}
	userLeaf2PubKeyBytes := userLeaf2PrivKey.PubKey().SerializeCompressed()

	// Create the issuance transaction
	issueTokenTransaction := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_MintInput{
			MintInput: &pb.MintInput{
				IssuerPublicKey:         tokenIdentityPubKeyBytes,
				IssuerProvidedTimestamp: uint64(time.Now().UnixMilli()),
			},
		},
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: userLeaf1PubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestIssueLeaf1Amount),
			},
			{
				OwnerPublicKey: userLeaf2PubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestIssueLeaf2Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	return issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, nil
}

func createTestTokenTransferTransaction(
	config *wallet.Config,
	finalIssueTokenTransactionHash []byte,
	tokenIdentityPubKeyBytes []byte,
) (*pb.TokenTransaction, *secp256k1.PrivateKey, error) {
	userLeaf3PrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, err
	}
	userLeaf3PubKeyBytes := userLeaf3PrivKey.PubKey().SerializeCompressed()

	transferTokenTransaction := &pb.TokenTransaction{
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
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: userLeaf3PubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestTransferLeaf1Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	return transferTokenTransaction, userLeaf3PrivKey, nil
}

func createTestTokenMintTransactionWithMultipleOutputLeaves(config *wallet.Config,
	tokenIdentityPubKeyBytes []byte, numLeaves int,
) (*pb.TokenTransaction, []*secp256k1.PrivateKey, error) {
	userLeafPrivKeys := make([]*secp256k1.PrivateKey, numLeaves)
	outputLeaves := make([]*pb.TokenLeafOutput, numLeaves)

	for i := 0; i < numLeaves; i++ {
		privKey, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			return nil, nil, err
		}
		userLeafPrivKeys[i] = privKey
		pubKeyBytes := privKey.PubKey().SerializeCompressed()

		outputLeaves[i] = &pb.TokenLeafOutput{
			OwnerPublicKey: pubKeyBytes,
			TokenPublicKey: tokenIdentityPubKeyBytes,
			TokenAmount:    int64ToUint128Bytes(0, TestIssueMultiplePerLeafAmount),
		}
	}

	issueTokenTransaction := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_MintInput{
			MintInput: &pb.MintInput{
				IssuerPublicKey:         tokenIdentityPubKeyBytes,
				IssuerProvidedTimestamp: uint64(time.Now().UnixMilli()),
			},
		},
		OutputLeaves:                    outputLeaves,
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	return issueTokenTransaction, userLeafPrivKeys, nil
}

// getHalfOperatorIDs returns approximately half of the operator IDs from the config
func getHalfOperatorIDs(config *wallet.Config) []string {
	var halfOperatorIDs []string
	halfOperatorCount := len(config.SigningOperators) / 2
	for operatorID := range config.SigningOperators {
		if len(halfOperatorIDs) < halfOperatorCount {
			halfOperatorIDs = append(halfOperatorIDs, operatorID)
		} else {
			break
		}
	}
	return halfOperatorIDs
}

// getRemainingOperatorIDs returns the operator IDs not included in the provided list
func getRemainingOperatorIDs(config *wallet.Config, excludedIDs []string) []string {
	var remainingOperatorIDs []string
	for operatorID := range config.SigningOperators {
		found := false
		for _, excludedID := range excludedIDs {
			if operatorID == excludedID {
				found = true
				break
			}
		}
		if !found {
			remainingOperatorIDs = append(remainingOperatorIDs, operatorID)
		}
	}
	return remainingOperatorIDs
}

func TestBroadcastTokenTransactionMintAndTransferTokens(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %v", finalIssueTokenTransaction)

	// Validate withdrawal params match config
	for i, leaf := range finalIssueTokenTransaction.OutputLeaves {
		if leaf.GetWithdrawBondSats() != WithdrawalBondSatsInConfig {
			t.Errorf("leaf %d: expected withdrawal bond sats 1000000, got %d", i, leaf.GetWithdrawBondSats())
		}
		if leaf.GetWithdrawRelativeBlockLocktime() != uint64(WithdrawalRelativeBlockLocktimeInConfig) {
			t.Errorf("leaf %d: expected withdrawal relative block locktime 1000, got %d", i, leaf.GetWithdrawRelativeBlockLocktime())
		}
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance token transaction: %v", err)
	}
	transferTokenTransaction, userLeaf3PrivKey, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	userLeaf3PubKeyBytes := userLeaf3PrivKey.PubKey().SerializeCompressed()

	revPubKey1 := finalIssueTokenTransaction.OutputLeaves[0].RevocationPublicKey
	revPubKey2 := finalIssueTokenTransaction.OutputLeaves[1].RevocationPublicKey

	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	if err != nil {
		t.Fatalf("failed to broadcast transfer token transaction: %v", err)
	}
	log.Printf("transfer broadcast finalized token transaction: %v", transferTokenTransactionResponse)

	// Query token transactions with pagination - first page
	tokenTransactionsPage1, err := wallet.QueryTokenTransactions(
		context.Background(),
		config,
		[][]byte{tokenIdentityPubKeyBytes}, // token public key
		nil,                                // owner public keys
		nil,                                // leaf IDs
		nil,                                // transaction hashes
		0,                                  // offset
		1,                                  // limit - only get 1 transaction
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
		[][]byte{tokenIdentityPubKeyBytes}, // token public key
		nil,                                // owner public keys
		nil,                                // leaf IDs
		nil,                                // transaction hashes
		tokenTransactionsPage1.Offset,      // offset - use the offset from previous response (1)
		1,                                  // limit - only get 1 transaction
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
		[][]byte{tokenIdentityPubKeyBytes}, // token public key
		nil,                                // owner public keys
		nil,                                // leaf IDs
		nil,                                // transaction hashes
		tokenTransactionsPage2.Offset,      // offset - use the offset from previous response
		1,                                  // limit - only get 1 transaction
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
	// Validate transfer output leaf
	if len(transferTx.OutputLeaves) != 1 {
		t.Fatalf("expected 1 output leaf in transfer transaction, got %d", len(transferTx.OutputLeaves))
	}
	transferAmount := new(big.Int).SetBytes(transferTx.OutputLeaves[0].TokenAmount)
	expectedTransferAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestTransferLeaf1Amount))
	if transferAmount.Cmp(expectedTransferAmount) != 0 {
		t.Fatalf("transfer amount %d does not match expected %d", transferAmount, expectedTransferAmount)
	}
	if !bytes.Equal(transferTx.OutputLeaves[0].OwnerPublicKey, userLeaf3PubKeyBytes) {
		t.Fatal("transfer output leaf owner public key does not match expected")
	}

	// Validate mint output leaves
	if len(mintTx.OutputLeaves) != 2 {
		t.Fatalf("expected 2 output leaves in mint transaction, got %d", len(mintTx.OutputLeaves))
	}
	mintLeaf1Amount := new(big.Int).SetBytes(mintTx.OutputLeaves[0].TokenAmount)
	mintLeaf2Amount := new(big.Int).SetBytes(mintTx.OutputLeaves[1].TokenAmount)
	expectedLeaf1Amount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestIssueLeaf1Amount))
	expectedLeaf2Amount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestIssueLeaf2Amount))
	if mintLeaf1Amount.Cmp(expectedLeaf1Amount) != 0 {
		t.Fatalf("mint leaf 1 amount %d does not match expected %d", mintLeaf1Amount, expectedLeaf1Amount)
	}
	if mintLeaf2Amount.Cmp(expectedLeaf2Amount) != 0 {
		t.Fatalf("mint leaf 2 amount %d does not match expected %d", mintLeaf2Amount, expectedLeaf2Amount)
	}
}

func TestBroadcastTokenTransactionMintAndTransferTokensLotsOfLeaves(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()

	// Try to create issuance transaction with 101 leaves (should fail)
	tooBigIssuanceTransaction, _, err := createTestTokenMintTransactionWithMultipleOutputLeaves(config,
		tokenIdentityPubKeyBytes, 101)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Attempt to broadcast the issuance transaction with too many leaves
	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, tooBigIssuanceTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	require.Error(t, err, "expected error when broadcasting issuance transaction with more than 100 output leaves")

	// Create issuance transaction with 100 leaves
	issueTokenTransactionFirst100, userLeafPrivKeysFirst100, err := createTestTokenMintTransactionWithMultipleOutputLeaves(config,
		tokenIdentityPubKeyBytes, ManyLeavesCount)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Broadcast the issuance transaction
	finalIssueTokenTransactionFirst100, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransactionFirst100,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	// Create issuance transaction with 100 leaves
	issueTokenTransactionSecond100, userLeafPrivKeysSecond100, err := createTestTokenMintTransactionWithMultipleOutputLeaves(config,
		tokenIdentityPubKeyBytes, ManyLeavesCount)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Broadcast the issuance transaction
	finalIssueTokenTransactionSecond100, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransactionSecond100,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	finalIssueTokenTransactionHashFirst100, err := utils.HashTokenTransaction(finalIssueTokenTransactionFirst100, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	finalIssueTokenTransactionHashSecond100, err := utils.HashTokenTransaction(finalIssueTokenTransactionSecond100, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	// Create consolidation transaction
	consolidatedLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err, "failed to generate private key")

	consolidatedLeafPubKeyBytes := consolidatedLeafPrivKey.PubKey().SerializeCompressed()

	// Create a transfer transaction that consolidates all leaves with too many inputs.
	leavesToSpendTooMany := make([]*pb.TokenLeafToSpend, 200)
	for i := 0; i < 100; i++ {
		leavesToSpendTooMany[i] = &pb.TokenLeafToSpend{
			PrevTokenTransactionHash:     finalIssueTokenTransactionHashFirst100,
			PrevTokenTransactionLeafVout: uint32(i),
		}
	}
	for i := 0; i < 100; i++ {
		leavesToSpendTooMany[100+i] = &pb.TokenLeafToSpend{
			PrevTokenTransactionHash:     finalIssueTokenTransactionHashSecond100,
			PrevTokenTransactionLeafVout: uint32(i),
		}
	}

	tooManyTransaction := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TransferInput{
				LeavesToSpend: leavesToSpendTooMany,
			},
		},
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: consolidatedLeafPubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestIssueMultiplePerLeafAmount*ManyLeavesCount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	// Combine private keys from both issuance transactions
	allUserLeafPrivKeys := append(userLeafPrivKeysFirst100, userLeafPrivKeysSecond100...)

	// Collect all revocation public keys from both transactions
	allRevPubKeys := make([][]byte, 200)
	for i := 0; i < 100; i++ {
		allRevPubKeys[i] = finalIssueTokenTransactionFirst100.OutputLeaves[i].RevocationPublicKey
		allRevPubKeys[i+100] = finalIssueTokenTransactionSecond100.OutputLeaves[i].RevocationPublicKey
	}

	// Broadcast the consolidation transaction
	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, tooManyTransaction,
		allUserLeafPrivKeys,
		allRevPubKeys,
	)
	require.Error(t, err, "expected error when broadcasting issuance transaction with more than 100 input leaves")

	// Now try with just the first 100
	leavesToSpend := make([]*pb.TokenLeafToSpend, 100)
	for i := 0; i < 100; i++ {
		leavesToSpend[i] = &pb.TokenLeafToSpend{
			PrevTokenTransactionHash:     finalIssueTokenTransactionHashFirst100,
			PrevTokenTransactionLeafVout: uint32(i),
		}
	}
	consolidateTransaction := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TransferInput{
				LeavesToSpend: leavesToSpend,
			},
		},
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: consolidatedLeafPubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestIssueMultiplePerLeafAmount*ManyLeavesCount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	// Collect all revocation public keys
	revPubKeys := make([][]byte, 100)
	for i := 0; i < 100; i++ {
		revPubKeys[i] = finalIssueTokenTransactionFirst100.OutputLeaves[i].RevocationPublicKey
	}

	// Broadcast the consolidation transaction
	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, consolidateTransaction,
		userLeafPrivKeysFirst100,
		revPubKeys,
	)
	require.NoError(t, err, "failed to broadcast consolidation transaction")

	// Verify the consolidated amount
	tokenOutputsResponse, err := wallet.QueryTokenOutputs(
		context.Background(),
		config,
		[][]byte{consolidatedLeafPubKeyBytes},
		[][]byte{tokenIdentityPubKeyBytes},
	)
	require.NoError(t, err, "failed to get owned token leaves")

	require.Equal(t, 1, len(tokenOutputsResponse.LeavesWithPreviousTransactionData), "expected 1 consolidated leaf")
}

func TestFreezeAndUnfreezeTokens(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Broadcast the token transaction
	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %v", finalIssueTokenTransaction)

	// Validate withdrawal params match config
	for i, leaf := range finalIssueTokenTransaction.OutputLeaves {
		require.Equal(t, uint64(WithdrawalBondSatsInConfig), leaf.GetWithdrawBondSats(),
			"leaf %d: expected withdrawal bond sats %d, got %d", i, uint64(WithdrawalBondSatsInConfig), leaf.GetWithdrawBondSats())
		require.Equal(t, uint64(WithdrawalRelativeBlockLocktimeInConfig), leaf.GetWithdrawRelativeBlockLocktime(),
			"leaf %d: expected withdrawal relative block locktime %d, got %d", i, uint64(WithdrawalRelativeBlockLocktimeInConfig), leaf.GetWithdrawRelativeBlockLocktime())
	}

	// Call FreezeTokens to freeze the output leaf
	freezeResponse, err := wallet.FreezeTokens(
		context.Background(),
		config,
		finalIssueTokenTransaction.OutputLeaves[0].OwnerPublicKey, // owner public key of the leaf to freeze
		tokenIdentityPubKeyBytes,                                  // token public key
		false,                                                     // unfreeze
	)
	require.NoError(t, err, "failed to freeze tokens")

	// Convert frozen amount bytes to big.Int for comparison
	frozenAmount := new(big.Int).SetBytes(freezeResponse.ImpactedTokenAmount)

	// Calculate total amount from transaction output leaves
	expectedAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestIssueLeaf1Amount))
	expectedLeafID := finalIssueTokenTransaction.OutputLeaves[0].Id

	require.Equal(t, 0, frozenAmount.Cmp(expectedAmount),
		"frozen amount %s does not match expected amount %s", frozenAmount.String(), expectedAmount.String())
	require.Equal(t, 1, len(freezeResponse.ImpactedLeafIds), "expected 1 impacted leaf ID")
	require.Equal(t, *expectedLeafID, freezeResponse.ImpactedLeafIds[0],
		"frozen leaf ID %s does not match expected leaf ID %s", freezeResponse.ImpactedLeafIds[0], *expectedLeafID)

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final transfer token transaction")

	// Replace direct transaction creation with helper function call
	transferTokenTransaction, _, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	require.NoError(t, err, "failed to create test token transfer transaction")

	revPubKey1 := finalIssueTokenTransaction.OutputLeaves[0].RevocationPublicKey
	revPubKey2 := finalIssueTokenTransaction.OutputLeaves[1].RevocationPublicKey

	// Broadcast the token transaction
	transferFrozenTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	require.Error(t, err, "expected error when transferring frozen tokens")
	require.Nil(t, transferFrozenTokenTransactionResponse, "expected nil response when transferring frozen tokens")
	log.Printf("successfully froze tokens with response: %+v", freezeResponse)

	// Call FreezeTokens to thaw the output leaf
	unfreezeResponse, err := wallet.FreezeTokens(
		context.Background(),
		config,
		finalIssueTokenTransaction.OutputLeaves[0].OwnerPublicKey, // owner public key of the leaf to freeze
		tokenIdentityPubKeyBytes,
		true, // unfreeze
	)
	require.NoError(t, err, "failed to unfreeze tokens")

	// Convert frozen amount bytes to big.Int for comparison
	thawedAmount := new(big.Int).SetBytes(unfreezeResponse.ImpactedTokenAmount)

	require.Equal(t, 0, thawedAmount.Cmp(expectedAmount),
		"thawed amount %s does not match expected amount %s", thawedAmount.String(), expectedAmount.String())
	require.Equal(t, 1, len(unfreezeResponse.ImpactedLeafIds), "expected 1 impacted leaf ID")
	require.Equal(t, *expectedLeafID, unfreezeResponse.ImpactedLeafIds[0],
		"thawed leaf ID %s does not match expected leaf ID %s", unfreezeResponse.ImpactedLeafIds[0], *expectedLeafID)

	// Broadcast the token transaction
	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	require.NoError(t, err, "failed to broadcast thawed token transaction")
	require.NotNil(t, transferTokenTransactionResponse, "expected non-nil response when transferring thawed tokens")
	log.Printf("thawed token transfer broadcast finalized token transaction: %v", transferTokenTransactionResponse)
}

func TestBroadcastTokenTransactionMintAndTransferTokensDoubleStart(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, _, _, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Make a start token transaction that we will not continue.
	_, _, _, _ = wallet.StartTokenTransaction(context.Background(), config, issueTokenTransaction, []*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})

	// Create a new transaction which will change the issuer timestamp to avoid a DB unique key error.
	issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	// Go through the full flow (including start token transaction)
	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %v", finalIssueTokenTransaction)

	// Validate withdrawal params match config
	for i, leaf := range finalIssueTokenTransaction.OutputLeaves {
		require.Equal(t, uint64(WithdrawalBondSatsInConfig), leaf.GetWithdrawBondSats(),
			"leaf %d: expected withdrawal bond sats %d, got %d", i, uint64(WithdrawalBondSatsInConfig), leaf.GetWithdrawBondSats())
		require.Equal(t, uint64(WithdrawalRelativeBlockLocktimeInConfig), leaf.GetWithdrawRelativeBlockLocktime(),
			"leaf %d: expected withdrawal relative block locktime %d, got %d", i, uint64(WithdrawalRelativeBlockLocktimeInConfig), leaf.GetWithdrawRelativeBlockLocktime())
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	transferTokenTransaction, userLeaf3PrivKey, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	require.NoError(t, err, "failed to create test token transfer transaction")

	userLeaf3PubKeyBytes := userLeaf3PrivKey.PubKey().SerializeCompressed()

	// Validate withdrawal params match config
	for i, leaf := range finalIssueTokenTransaction.OutputLeaves {
		require.Equal(t, uint64(WithdrawalBondSatsInConfig), leaf.GetWithdrawBondSats(),
			"leaf %d: expected withdrawal bond sats %d, got %d", i, uint64(WithdrawalBondSatsInConfig), leaf.GetWithdrawBondSats())
		require.Equal(t, uint64(WithdrawalRelativeBlockLocktimeInConfig), leaf.GetWithdrawRelativeBlockLocktime(),
			"leaf %d: expected withdrawal relative block locktime %d, got %d", i, uint64(WithdrawalRelativeBlockLocktimeInConfig), leaf.GetWithdrawRelativeBlockLocktime())
	}

	revPubKey1 := finalIssueTokenTransaction.OutputLeaves[0].RevocationPublicKey
	revPubKey2 := finalIssueTokenTransaction.OutputLeaves[1].RevocationPublicKey

	// Make a start token transaction with identical params but dont continue.
	_, _, _, _ = wallet.StartTokenTransaction(context.Background(), config, transferTokenTransaction, []*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2})

	// Broadcast the transfer token transaction
	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")
	log.Printf("transfer broadcast finalized token transaction: %v", transferTokenTransactionResponse)

	// Test QueryTokenOutputs
	tokenOutputsResponse, err := wallet.QueryTokenOutputs(
		context.Background(),
		config,
		[][]byte{userLeaf3PubKeyBytes},
		[][]byte{tokenIdentityPubKeyBytes},
	)
	require.NoError(t, err, "failed to get owned token leaves")

	// Validate the response
	require.Equal(t, 1, len(tokenOutputsResponse.LeavesWithPreviousTransactionData), "expected 1 owned leaf")

	leaf := tokenOutputsResponse.LeavesWithPreviousTransactionData[0]

	// Validate leaf details
	require.True(t, bytes.Equal(leaf.Leaf.OwnerPublicKey, userLeaf3PubKeyBytes), "leaf owner public key does not match expected")
	require.True(t, bytes.Equal(leaf.Leaf.TokenPublicKey, tokenIdentityPubKeyBytes), "leaf token public key does not match expected")

	// Validate amount
	expectedAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestTransferLeaf1Amount))
	actualAmount := new(big.Int).SetBytes(leaf.Leaf.TokenAmount)
	require.Equal(t, 0, actualAmount.Cmp(expectedAmount), "leaf token amount %d does not match expected %d", actualAmount, expectedAmount)

	// Validate previous transaction data
	transferTokenTransactionResponseHash, err := utils.HashTokenTransaction(transferTokenTransactionResponse, false)
	require.NoError(t, err, "failed to hash final transfer token transaction")

	require.True(t, bytes.Equal(leaf.PreviousTransactionHash, transferTokenTransactionResponseHash), "previous transaction hash does not match expected")
	require.Equal(t, uint32(0), leaf.PreviousTransactionVout, "previous transaction vout expected 0, got %d", leaf.PreviousTransactionVout)
}

// Helper function for testing token mint transaction with various signing scenarios
// Parameters:
// - t: testing context
// - config: wallet configuration
// - testDoubleSign: whether to test double signing
// - testDifferentTx: whether to test signing with a different transaction than was started
// - expectedError: whether an error is expected during any of the signing operations
func testMintTransactionSigningScenarios(t *testing.T, config *wallet.Config,
	testDoubleSign bool, testDifferentTx bool, expectedError bool,
) (*pb.TokenTransaction, *secp256k1.PrivateKey, *secp256k1.PrivateKey) {
	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token mint transaction")

	startResp, _, finalTxHash, err := wallet.StartTokenTransaction(
		context.Background(),
		config,
		issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{},
	)
	require.NoError(t, err, "failed to start token transaction")

	txToSign := startResp.FinalTokenTransaction
	if testDifferentTx {
		differentIssueTokenTransaction, _, _, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
		require.NoError(t, err, "failed to create different test token issuance transaction")
		txToSign = differentIssueTokenTransaction
	}

	errorOccurred := false
	var halfSignOperatorSignatures wallet.OperatorSignatures
	if testDoubleSign {
		halfOperatorIDs := getHalfOperatorIDs(config)
		// Sign with half the operators to get in a partial signed state
		_, halfSignOperatorSignatures, err = wallet.SignTokenTransaction(
			context.Background(),
			config,
			startResp.FinalTokenTransaction, // Always use the original transaction for first sign (if double signing)
			finalTxHash,
			[]*secp256k1.PrivateKey{&tokenPrivKey},
			halfOperatorIDs...,
		)
		require.NoError(t, err, "unexpected error during mint half signing")
	}

	// Complete the transaction signing with either the original or different transaction
	_, fullSignOperatorSignatures, err := wallet.SignTokenTransaction(
		context.Background(),
		config,
		txToSign,
		finalTxHash,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
	)
	if err != nil {
		errorOccurred = true
		log.Printf("error when signing the mint transaction: %v", err)
	}

	if expectedError {
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
	return finalIssueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey
}

// Helper function for testing token transfer transaction with various signing scenarios
// Parameters:
// - t: testing context
// - config: wallet configuration
// - finalIssueTokenTransaction: the finalized mint transaction
// - userLeaf1PrivKey, userLeaf2PrivKey: private keys for the leaves
// - testDoubleSign: whether to test double signing
// - testDifferentTx: whether to test signing with a different transaction than was started
// - expectedError: whether an error is expected during any of the signing operations
func testTransferTransactionSigningScenarios(t *testing.T, config *wallet.Config,
	finalIssueTokenTransaction *pb.TokenTransaction,
	userLeaf1PrivKey, userLeaf2PrivKey *secp256k1.PrivateKey,
	testDoubleSign bool, testDifferentTx bool, expectedError bool,
) {
	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	transferTokenTransaction, _, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	require.NoError(t, err, "failed to create test token transfer transaction")

	revPubKey1 := finalIssueTokenTransaction.OutputLeaves[0].RevocationPublicKey
	revPubKey2 := finalIssueTokenTransaction.OutputLeaves[1].RevocationPublicKey

	transferStartResp, _, transferFinalTxHash, err := wallet.StartTokenTransaction(
		context.Background(),
		config,
		transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	require.NoError(t, err, "failed to start transfer transaction")

	errorOccurred := false
	// Prepare transaction to sign - either the original or a modified one
	txToSign := transferStartResp.FinalTokenTransaction
	if testDifferentTx {
		txToSign = cloneTransferTransactionWithDifferentOutputOwner(
			transferTokenTransaction,
			userLeaf1PrivKey.PubKey().SerializeCompressed(),
		)
	}

	// If testing double signing, first sign with half the operators
	var halfSignOperatorSignatures wallet.OperatorSignatures
	if testDoubleSign {
		halfOperatorIDs := getHalfOperatorIDs(config)
		_, halfSignOperatorSignatures, err = wallet.SignTokenTransaction(
			context.Background(),
			config,
			transferStartResp.FinalTokenTransaction, // Always use original transaction for first sign
			transferFinalTxHash,
			[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
			halfOperatorIDs...,
		)
		require.NoError(t, err, "unexpected error during transfer half signing")
	}

	// Complete the transaction signing with either the original or different transaction
	signResponseTransferKeyshares, fullSignOperatorSignatures, err := wallet.SignTokenTransaction(
		context.Background(),
		config,
		txToSign,
		transferFinalTxHash,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
	)
	if err != nil {
		errorOccurred = true
		log.Printf("error when signing the transfer transaction: %v", err)
	}

	if expectedError {
		require.True(t, errorOccurred, "expected an error during transfer signing operation but none occurred")
		return // Don't proceed with finalization if we expected an error
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
		[][]byte{revPubKey1, revPubKey2},
		transferStartResp,
	)
	require.NoError(t, err, "failed to finalize the transfer transaction")
	log.Printf("transfer transaction finalized: %v", transferStartResp.FinalTokenTransaction)
}

// TestTokenMintTransactionSigning tests various signing scenarios for token mint transactions
func TestTokenMintTransactionSigning(t *testing.T) {
	testCases := []struct {
		name            string
		doubleMintSign  bool
		differentMintTx bool
		expectedError   bool
	}{
		{
			name:            "single sign mint should succeed with the same transaction",
			doubleMintSign:  false,
			differentMintTx: false,
			expectedError:   false,
		},
		{
			name:            "single sign mint should fail with different transaction",
			doubleMintSign:  false,
			differentMintTx: true,
			expectedError:   true,
		},
		{
			name:            "double sign mint should fail with a different transaction",
			doubleMintSign:  true,
			differentMintTx: true,
			expectedError:   true,
		},
		{
			name:            "double sign mint should succeed with same transaction",
			doubleMintSign:  true,
			differentMintTx: false,
			expectedError:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := testutil.TestWalletConfig()
			require.NoError(t, err, "failed to create wallet config")

			testMintTransactionSigningScenarios(
				t, config, tc.doubleMintSign, tc.differentMintTx, tc.expectedError)
		})
	}
}

// TestTokenTransferTransactionSigning tests various signing scenarios for token transfer transactions
func TestTokenTransferTransactionSigning(t *testing.T) {
	testCases := []struct {
		name                string
		doubleTransferSign  bool
		differentTransferTx bool
		expectedError       bool
	}{
		{
			name:                "single sign transfer should succeed with the same transaction",
			doubleTransferSign:  false,
			differentTransferTx: false,
			expectedError:       false,
		},
		{
			name:                "single sign transfer  should fail with different transaction",
			doubleTransferSign:  false,
			differentTransferTx: true,
			expectedError:       true,
		},
		{
			name:                "double sign transfer should fail with a different transaction",
			doubleTransferSign:  true,
			differentTransferTx: true,
			expectedError:       true,
		},
		{
			name:                "double sign transfer should succeed with same transaction",
			doubleTransferSign:  true,
			differentTransferTx: false,
			expectedError:       false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := testutil.TestWalletConfig()
			require.NoError(t, err, "failed to create wallet config")

			// First create and finalize a mint transaction to use for the transfer tests
			finalIssueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey := testMintTransactionSigningScenarios(
				t, config, false, false, false) // Simple mint with no errors expected

			testTransferTransactionSigningScenarios(
				t, config, finalIssueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey,
				tc.doubleTransferSign, tc.differentTransferTx, tc.expectedError)
		})
	}
}

func TestBroadcastTokenTransactionMintAndTransferTokensSchnorr(t *testing.T) {
	config, err := testutil.TestWalletConfigWithTokenTransactionSchnorr()
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %v", finalIssueTokenTransaction)

	// Validate withdrawal params match config
	for i, leaf := range finalIssueTokenTransaction.OutputLeaves {
		require.Equal(t, uint64(WithdrawalBondSatsInConfig), leaf.GetWithdrawBondSats(),
			"leaf %d: expected withdrawal bond sats %d, got %d", i, uint64(WithdrawalBondSatsInConfig), leaf.GetWithdrawBondSats())
		require.Equal(t, uint64(WithdrawalRelativeBlockLocktimeInConfig), leaf.GetWithdrawRelativeBlockLocktime(),
			"leaf %d: expected withdrawal relative block locktime %d, got %d", i, uint64(WithdrawalRelativeBlockLocktimeInConfig), leaf.GetWithdrawRelativeBlockLocktime())
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	transferTokenTransaction, _, err := createTestTokenTransferTransaction(config,
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	require.NoError(t, err, "failed to create test token transfer transaction")

	revPubKey1 := finalIssueTokenTransaction.OutputLeaves[0].RevocationPublicKey
	revPubKey2 := finalIssueTokenTransaction.OutputLeaves[1].RevocationPublicKey

	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")
	log.Printf("transfer broadcast finalized token transaction: %v", transferTokenTransactionResponse)
}

func TestFreezeAndUnfreezeTokensSchnorr(t *testing.T) {
	config, err := testutil.TestWalletConfigWithTokenTransactionSchnorr()
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, _, _, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	_, err = wallet.FreezeTokens(
		context.Background(),
		config,
		finalIssueTokenTransaction.OutputLeaves[0].OwnerPublicKey,
		tokenIdentityPubKeyBytes,
		false,
	)
	require.NoError(t, err, "failed to freeze tokens")
}

func TestCancelTokenTransaction(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	require.NoError(t, err, "failed to create wallet config")

	halfOperatorIDs := getHalfOperatorIDs(config)
	remainingOperatorIDs := getRemainingOperatorIDs(config, halfOperatorIDs)

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	startResp, _, finalTxHash, err := wallet.StartTokenTransaction(
		context.Background(),
		config,
		issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{},
	)
	require.NoError(t, err, "failed to start token transaction")
	finalIssueTokenTransaction := startResp.FinalTokenTransaction

	_, _, err = wallet.SignTokenTransaction(
		context.Background(),
		config,
		startResp.FinalTokenTransaction,
		finalTxHash,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		halfOperatorIDs..., // Only sign with half of the operators
	)
	require.NoError(t, err, "failed to sign the mint transaction with the first half of SOs")

	err = wallet.CancelTokenTransaction(
		context.Background(),
		config,
		startResp.FinalTokenTransaction,
		halfOperatorIDs...,
	)
	require.Error(t, err, "expected cancel failure on mint transaction. Mint cancellation is not supported")

	_, _, err = wallet.SignTokenTransaction(
		context.Background(),
		config,
		startResp.FinalTokenTransaction,
		finalTxHash,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		remainingOperatorIDs...,
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

	revPubKey1 := finalIssueTokenTransaction.OutputLeaves[0].RevocationPublicKey
	revPubKey2 := finalIssueTokenTransaction.OutputLeaves[1].RevocationPublicKey

	transferStartResp, _, transferFinalTxHash, err := wallet.StartTokenTransaction(
		context.Background(),
		config,
		transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	require.NoError(t, err, "failed to start transfer transaction")

	log.Printf("transfer tx hash: %x", transferFinalTxHash)

	// Sign with only half of the operators
	_, _, err = wallet.SignTokenTransaction(
		context.Background(),
		config,
		transferStartResp.FinalTokenTransaction,
		transferFinalTxHash,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		halfOperatorIDs..., // Only sign with half of the operators
	)
	require.NoError(t, err, "failed partial signing")

	// Cancel the transfer transaction after partial signing
	err = wallet.CancelTokenTransaction(
		context.Background(),
		config,
		transferStartResp.FinalTokenTransaction,
		halfOperatorIDs..., // Cancel for the half of the operators that signed.
	)
	require.NoError(t, err, "failed to cancel partially signed transfer token transaction")

	// Attempt to cancel the transaction with the SOs that did not sign
	err = wallet.CancelTokenTransaction(
		context.Background(),
		config,
		transferStartResp.FinalTokenTransaction,
		remainingOperatorIDs..., // Only sign with half of the operators
	)
	require.Error(t, err, "expected error when trying to cancel transfer transaction with remaining operators")

	// Verify we can create a new transfer transaction after cancellation
	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(),
		config,
		transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction after cancellation")
	log.Printf("successfully transferred tokens after cancellation: %v", transferTokenTransactionResponse)
}

func TestBroadcastTokenTransactionWithInvalidPrevTxHash(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	require.NoError(t, err, "failed to broadcast issuance token transaction")
	log.Printf("issuance broadcast finalized token transaction: %v", finalIssueTokenTransaction)

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	// Corrupt the transaction hash by adding a byte
	corruptedHash := append(finalIssueTokenTransactionHash, 0xFF)

	// Create transfer transaction with corrupted hash
	transferTokenTransaction := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TransferInput{
				LeavesToSpend: []*pb.TokenLeafToSpend{
					{
						PrevTokenTransactionHash:     corruptedHash, // Corrupted hash
						PrevTokenTransactionLeafVout: 0,
					},
					{
						PrevTokenTransactionHash:     finalIssueTokenTransactionHash,
						PrevTokenTransactionLeafVout: 1,
					},
				},
			},
		},
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: userLeaf1PrivKey.PubKey().SerializeCompressed(),
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestTransferLeaf1Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	revPubKey1 := finalIssueTokenTransaction.OutputLeaves[0].RevocationPublicKey
	revPubKey2 := finalIssueTokenTransaction.OutputLeaves[1].RevocationPublicKey

	// Attempt to broadcast the transfer transaction with corrupted hash
	// This should fail validation
	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)

	require.Error(t, err, "expected transaction with invalid hash to be rejected")
	log.Printf("successfully detected invalid transaction hash: %v", err)

	// Try with only the second hash corrupted
	transferTokenTransaction2 := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TransferInput{
				LeavesToSpend: []*pb.TokenLeafToSpend{
					{
						PrevTokenTransactionHash:     finalIssueTokenTransactionHash,
						PrevTokenTransactionLeafVout: 0,
					},
					{
						PrevTokenTransactionHash:     append(finalIssueTokenTransactionHash, 0xAA), // Corrupted hash
						PrevTokenTransactionLeafVout: 1,
					},
				},
			},
		},
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: userLeaf1PrivKey.PubKey().SerializeCompressed(),
				TokenPublicKey: tokenIdentityPubKeyBytes,
				TokenAmount:    int64ToUint128Bytes(0, TestTransferLeaf1Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeys(config),
	}

	// Attempt to broadcast the second transfer transaction with corrupted hash
	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction2,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)

	require.Error(t, err, "expected transaction with second invalid hash to be rejected")
	log.Printf("successfully detected second invalid transaction hash: %v", err)
}

func TestBroadcastTokenTransactionUnspecifiedNetwork(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	require.NoError(t, err, "failed to create wallet config")

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, _, _, err := createTestTokenMintTransaction(config, tokenIdentityPubKeyBytes)
	require.NoError(t, err, "failed to create test token issuance transaction")
	issueTokenTransaction.Network = pb.Network_UNSPECIFIED

	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})

	require.Error(t, err, "expected transaction without a network to be rejected")
	log.Printf("successfully detected unspecified network and rejected with error: %v", err)
}

// cloneTransferTransactionWithDifferentOutputOwner creates a copy of a transfer transaction
// with a modified owner public key in the first output leaf
func cloneTransferTransactionWithDifferentOutputOwner(
	tx *pb.TokenTransaction,
	newOwnerPubKey []byte,
) *pb.TokenTransaction {
	clone := proto.Clone(tx).(*pb.TokenTransaction)
	if len(clone.OutputLeaves) > 0 {
		clone.OutputLeaves[0].OwnerPublicKey = newOwnerPubKey
	}
	return clone
}
