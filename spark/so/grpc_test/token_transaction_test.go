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

func createTestTokenIssuanceTransaction(tokenIdentityPubKeyBytes []byte) (*pb.TokenTransaction, *secp256k1.PrivateKey, *secp256k1.PrivateKey, error) {
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
	}

	return issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, nil
}

func createTestTokenTransferTransaction(
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
	}

	return transferTokenTransaction, userLeaf3PrivKey, nil
}

func createTestTokenIssuanceTransactionWithMultipleOutputLeaves(tokenIdentityPubKeyBytes []byte, numLeaves int) (*pb.TokenTransaction, []*secp256k1.PrivateKey, error) {
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
		OutputLeaves: outputLeaves,
	}

	return issueTokenTransaction, userLeafPrivKeys, nil
}

func TestBroadcastTokenTransactionIssueAndTransferTokens(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, err := createTestTokenIssuanceTransaction(tokenIdentityPubKeyBytes)
	if err != nil {
		t.Fatalf("failed to create test token issuance transaction: %v", err)
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
		if leaf.GetWithdrawBondSats() != WithdrawalBondSatsInConfig {
			t.Errorf("leaf %d: expected withdrawal bond sats 1000000, got %d", i, leaf.GetWithdrawBondSats())
		}
		if leaf.GetWithdrawRelativeBlockLocktime() != WithdrawalRelativeBlockLocktimeInConfig {
			t.Errorf("leaf %d: expected withdrawal relative block locktime 1000, got %d", i, leaf.GetWithdrawRelativeBlockLocktime())
		}
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance token transaction: %v", err)
	}
	transferTokenTransaction, userLeaf3PrivKey, err := createTestTokenTransferTransaction(
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	userLeaf3PubKeyBytes := userLeaf3PrivKey.PubKey().SerializeCompressed()

	revPubKey1 := finalIssueTokenTransaction.OutputLeaves[0].RevocationPublicKey
	revPubKey2 := finalIssueTokenTransaction.OutputLeaves[1].RevocationPublicKey

	// Broadcast the token transaction
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
	log.Printf("RETURNED TOKEN TRANSACTIONS PAGE 1: %v", tokenTransactionsPage1)

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
	log.Printf("RETURNED TOKEN TRANSACTIONS PAGE 2: %v", tokenTransactionsPage2)

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
	log.Printf("RETURNED TOKEN TRANSACTIONS PAGE 3: %v", tokenTransactionsPage3)

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

func TestBroadcastTokenTransactionIssueAndTransferTokensLotsOfLeaves(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()

	// Try to create issuance transaction with 101 leaves (should fail)
	tooBigIssuanceTransaction, _, err := createTestTokenIssuanceTransactionWithMultipleOutputLeaves(
		tokenIdentityPubKeyBytes, 101)
	if err != nil {
		t.Fatalf("failed to create test token issuance transaction: %v", err)
	}

	// Attempt to broadcast the issuance transaction with too many leaves
	_, err = wallet.BroadcastTokenTransaction(
		context.Background(), config, tooBigIssuanceTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	if err == nil {
		t.Fatal("expected error when broadcasting issuance transaction with more than 100 output leaves, got nil")
	}

	// Create issuance transaction with 100 leaves
	issueTokenTransactionFirst100, userLeafPrivKeysFirst100, err := createTestTokenIssuanceTransactionWithMultipleOutputLeaves(
		tokenIdentityPubKeyBytes, ManyLeavesCount)
	if err != nil {
		t.Fatalf("failed to create test token issuance transaction: %v", err)
	}

	// Broadcast the issuance transaction
	finalIssueTokenTransactionFirst100, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransactionFirst100,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	if err != nil {
		t.Fatalf("failed to broadcast issuance token transaction: %v", err)
	}

	// Create issuance transaction with 100 leaves
	issueTokenTransactionSecond100, userLeafPrivKeysSecond100, err := createTestTokenIssuanceTransactionWithMultipleOutputLeaves(
		tokenIdentityPubKeyBytes, ManyLeavesCount)
	if err != nil {
		t.Fatalf("failed to create test token issuance transaction: %v", err)
	}

	// Broadcast the issuance transaction
	finalIssueTokenTransactionSecond100, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransactionSecond100,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	if err != nil {
		t.Fatalf("failed to broadcast issuance token transaction: %v", err)
	}

	finalIssueTokenTransactionHashFirst100, err := utils.HashTokenTransaction(finalIssueTokenTransactionFirst100, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance token transaction: %v", err)
	}
	finalIssueTokenTransactionHashSecond100, err := utils.HashTokenTransaction(finalIssueTokenTransactionSecond100, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance token transaction: %v", err)
	}

	// Create consolidation transaction
	consolidatedLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
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
	if err == nil {
		t.Fatal("expected error when broadcasting issuance transaction with more than 100 input leaves, got nil")
	}

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
	if err != nil {
		t.Fatalf("failed to broadcast consolidation transaction: %v", err)
	}

	// Verify the consolidated amount
	ownedLeavesResponse, err := wallet.GetOwnedTokenLeaves(
		context.Background(),
		config,
		[][]byte{consolidatedLeafPubKeyBytes},
		[][]byte{tokenIdentityPubKeyBytes},
	)
	if err != nil {
		t.Fatalf("failed to get owned token leaves: %v", err)
	}

	if len(ownedLeavesResponse.LeavesWithPreviousTransactionData) != 1 {
		t.Fatalf("expected 1 consolidated leaf, got %d", len(ownedLeavesResponse.LeavesWithPreviousTransactionData))
	}
}

func TestFreezeAndUnfreezeTokens(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, err := createTestTokenIssuanceTransaction(tokenIdentityPubKeyBytes)
	if err != nil {
		t.Fatalf("failed to create test token issuance transaction: %v", err)
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
		if leaf.GetWithdrawBondSats() != WithdrawalBondSatsInConfig {
			t.Errorf("leaf %d: expected withdrawal bond sats 1000000, got %d", i, leaf.GetWithdrawBondSats())
		}
		if leaf.GetWithdrawRelativeBlockLocktime() != WithdrawalRelativeBlockLocktimeInConfig {
			t.Errorf("leaf %d: expected withdrawal relative block locktime 1000, got %d", i, leaf.GetWithdrawRelativeBlockLocktime())
		}
	}

	// Call FreezeTokens to freeze the output leaf
	freezeResponse, err := wallet.FreezeTokens(
		context.Background(),
		config,
		finalIssueTokenTransaction.OutputLeaves[0].OwnerPublicKey, // owner public key of the leaf to freeze
		tokenIdentityPubKeyBytes,                                  // token public key
		false,                                                     // unfreeze
	)
	if err != nil {
		t.Fatalf("failed to freeze tokens: %v", err)
	}

	// Convert frozen amount bytes to big.Int for comparison
	frozenAmount := new(big.Int).SetBytes(freezeResponse.ImpactedTokenAmount)

	// Calculate total amount from transaction output leaves
	expectedAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestIssueLeaf1Amount))
	expectedLeafID := finalIssueTokenTransaction.OutputLeaves[0].Id

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

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final transfer token transaction: %v", err)
	}

	// Replace direct transaction creation with helper function call
	transferTokenTransaction, _, err := createTestTokenTransferTransaction(
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}

	revPubKey1 := finalIssueTokenTransaction.OutputLeaves[0].RevocationPublicKey
	revPubKey2 := finalIssueTokenTransaction.OutputLeaves[1].RevocationPublicKey

	// Broadcast the token transaction
	transferFrozenTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	if err == nil {
		t.Fatal("expected error when transferring frozen tokens, got nil")
	}
	if transferFrozenTokenTransactionResponse != nil {
		t.Errorf("expected nil response when transferring frozen tokens, got %+v", transferFrozenTokenTransactionResponse)
	}
	log.Printf("successfully froze tokens with response: %+v", freezeResponse)

	// Call FreezeTokens to thaw the output leaf
	unfreezeResponse, err := wallet.FreezeTokens(
		context.Background(),
		config,
		finalIssueTokenTransaction.OutputLeaves[0].OwnerPublicKey, // owner public key of the leaf to freeze
		tokenIdentityPubKeyBytes,
		true, // unfreeze
	)

	// Convert frozen amount bytes to big.Int for comparison
	thawedAmount := new(big.Int).SetBytes(unfreezeResponse.ImpactedTokenAmount)

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

	// Broadcast the token transaction
	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	if err != nil {
		t.Fatalf("failed to broadcast thawed token transaction: %v", err)
	}
	if transferTokenTransactionResponse == nil {
		t.Fatal("expected non-nil response when transferring thawed tokens")
	}
	log.Printf("thawed token transfer broadcast finalized token transaction: %v", transferTokenTransactionResponse)
}

func TestBroadcastTokenTransactionIssueAndTransferTokensDoubleStart(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, _, _, err := createTestTokenIssuanceTransaction(tokenIdentityPubKeyBytes)
	if err != nil {
		t.Fatalf("failed to create test token issuance transaction: %v", err)
	}

	// Make a start token transaction that we will not continue.
	_, _, _, _ = wallet.StartTokenTransaction(context.Background(), config, issueTokenTransaction, []*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})

	// Create a new transaction which will change the issuer timestamp to avoid a DB unique key error.
	issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, err := createTestTokenIssuanceTransaction(tokenIdentityPubKeyBytes)
	if err != nil {
		t.Fatalf("failed to create test token issuance transaction: %v", err)
	}

	// Go through the full flow (including start token transaction)
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
		if leaf.GetWithdrawBondSats() != WithdrawalBondSatsInConfig {
			t.Errorf("leaf %d: expected withdrawal bond sats 1000000, got %d", i, leaf.GetWithdrawBondSats())
		}
		if leaf.GetWithdrawRelativeBlockLocktime() != WithdrawalRelativeBlockLocktimeInConfig {
			t.Errorf("leaf %d: expected withdrawal relative block locktime 1000, got %d", i, leaf.GetWithdrawRelativeBlockLocktime())
		}
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance token transaction: %v", err)
	}
	transferTokenTransaction, userLeaf3PrivKey, err := createTestTokenTransferTransaction(
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	userLeaf3PubKeyBytes := userLeaf3PrivKey.PubKey().SerializeCompressed()

	// Validate withdrawal params match config
	for i, leaf := range finalIssueTokenTransaction.OutputLeaves {
		if leaf.GetWithdrawBondSats() != WithdrawalBondSatsInConfig {
			t.Errorf("leaf %d: expected withdrawal bond sats 1000000, got %d", i, leaf.GetWithdrawBondSats())
		}
		if leaf.GetWithdrawRelativeBlockLocktime() != WithdrawalRelativeBlockLocktimeInConfig {
			t.Errorf("leaf %d: expected withdrawal relative block locktime 1000, got %d", i, leaf.GetWithdrawRelativeBlockLocktime())
		}
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
	if err != nil {
		t.Fatalf("failed to broadcast transfer token transaction: %v", err)
	}
	log.Printf("transfer broadcast finalized token transaction: %v", transferTokenTransactionResponse)

	// Test GetOwnedTokenLeaves
	ownedLeavesResponse, err := wallet.GetOwnedTokenLeaves(
		context.Background(),
		config,
		[][]byte{userLeaf3PubKeyBytes},
		[][]byte{tokenIdentityPubKeyBytes},
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
	if !bytes.Equal(leaf.Leaf.OwnerPublicKey, userLeaf3PubKeyBytes) {
		t.Fatalf("leaf owner public key does not match expected")
	}
	if !bytes.Equal(leaf.Leaf.TokenPublicKey, tokenIdentityPubKeyBytes) {
		t.Fatalf("leaf token public key does not match expected")
	}

	// Validate amount
	expectedAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestTransferLeaf1Amount))
	actualAmount := new(big.Int).SetBytes(leaf.Leaf.TokenAmount)
	if actualAmount.Cmp(expectedAmount) != 0 {
		t.Fatalf("leaf token amount %d does not match expected %d", actualAmount, expectedAmount)
	}

	// Validate previous transaction data
	transferTokenTransactionResponseHash, err := utils.HashTokenTransaction(transferTokenTransactionResponse, false)
	if err != nil {
		t.Fatalf("failed to hash final transfer token transaction: %v", err)
	}
	if !bytes.Equal(leaf.PreviousTransactionHash, transferTokenTransactionResponseHash) {
		t.Fatalf("previous transaction hash does not match expected")
	}
	if leaf.PreviousTransactionVout != 0 {
		t.Fatalf("previous transaction vout expected 0, got %d", leaf.PreviousTransactionVout)
	}
}

func TestBroadcastTokenTransactionIssueAndTransferTokensDoubleSign(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, err := createTestTokenIssuanceTransaction(tokenIdentityPubKeyBytes)
	if err != nil {
		t.Fatalf("failed to create test token issuance transaction: %v", err)
	}

	// Step 1: Start the token transaction
	startResp, _, finalTxHash, err := wallet.StartTokenTransaction(
		context.Background(),
		config,
		issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{},
	)
	if err != nil {
		t.Fatalf("failed to start token transaction: %v", err)
	}

	// Step 2: First sign attempt should succeed
	_, err = wallet.SignTokenTransaction(
		context.Background(),
		config,
		startResp.FinalTokenTransaction,
		finalTxHash,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
	)
	if err != nil {
		t.Fatalf("failed first sign attempt: %v", err)
	}

	// Step 2b: Second sign attempt should fail
	_, err = wallet.SignTokenTransaction(
		context.Background(),
		config,
		startResp.FinalTokenTransaction,
		finalTxHash,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
	)
	if err == nil {
		t.Fatal("expected error when signing same transaction twice, got nil")
	}

	finalIssueTokenTransaction := startResp.FinalTokenTransaction
	log.Printf("issuance transaction finalized: %v", finalIssueTokenTransaction)

	// Create transfer transaction
	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance token transaction: %v", err)
	}

	transferTokenTransaction, userLeaf3PrivKey, err := createTestTokenTransferTransaction(
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	userLeaf3PubKeyBytes := userLeaf3PrivKey.PubKey().SerializeCompressed()

	revPubKey1 := finalIssueTokenTransaction.OutputLeaves[0].RevocationPublicKey
	revPubKey2 := finalIssueTokenTransaction.OutputLeaves[1].RevocationPublicKey

	// Step 1: Start the transfer transaction
	transferStartResp, _, transferFinalTxHash, err := wallet.StartTokenTransaction(
		context.Background(),
		config,
		transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	if err != nil {
		t.Fatalf("failed to start transfer transaction: %v", err)
	}

	// Step 2: First sign attempt should succeed
	transferLeafKeyshares, err := wallet.SignTokenTransaction(
		context.Background(),
		config,
		transferStartResp.FinalTokenTransaction,
		transferFinalTxHash,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
	)
	if err != nil {
		t.Fatalf("failed first transfer sign attempt: %v", err)
	}

	// Step 2b: Second sign attempt should fail
	_, err = wallet.SignTokenTransaction(
		context.Background(),
		config,
		transferStartResp.FinalTokenTransaction,
		transferFinalTxHash,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
	)
	if err == nil {
		t.Fatal("expected error when signing same transfer transaction twice, got nil")
	}

	// Step 3: Finalize the transfer transaction with the successful keyshares
	err = wallet.FinalizeTokenTransaction(
		context.Background(),
		config,
		transferStartResp.FinalTokenTransaction,
		transferLeafKeyshares,
		[][]byte{revPubKey1, revPubKey2},
		transferStartResp,
	)
	if err != nil {
		t.Fatalf("failed to finalize transfer transaction: %v", err)
	}

	transferTokenTransactionResponse := transferStartResp.FinalTokenTransaction
	log.Printf("transfer transaction finalized: %v", transferTokenTransactionResponse)

	// Test GetOwnedTokenLeaves
	ownedLeavesResponse, err := wallet.GetOwnedTokenLeaves(
		context.Background(),
		config,
		[][]byte{userLeaf3PubKeyBytes},
		[][]byte{tokenIdentityPubKeyBytes},
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
	if !bytes.Equal(leaf.Leaf.OwnerPublicKey, userLeaf3PubKeyBytes) {
		t.Fatalf("leaf owner public key does not match expected")
	}
	if !bytes.Equal(leaf.Leaf.TokenPublicKey, tokenIdentityPubKeyBytes) {
		t.Fatalf("leaf token public key does not match expected")
	}

	// Validate amount
	expectedAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestTransferLeaf1Amount))
	actualAmount := new(big.Int).SetBytes(leaf.Leaf.TokenAmount)
	if actualAmount.Cmp(expectedAmount) != 0 {
		t.Fatalf("leaf token amount %d does not match expected %d", actualAmount, expectedAmount)
	}

	// Validate previous transaction data
	transferTokenTransactionResponseHash, err := utils.HashTokenTransaction(transferTokenTransactionResponse, false)
	if err != nil {
		t.Fatalf("failed to hash final transfer token transaction: %v", err)
	}
	if !bytes.Equal(leaf.PreviousTransactionHash, transferTokenTransactionResponseHash) {
		t.Fatalf("previous transaction hash does not match expected")
	}
	if leaf.PreviousTransactionVout != 0 {
		t.Fatalf("previous transaction vout expected 0, got %d", leaf.PreviousTransactionVout)
	}
}

func TestBroadcastTokenTransactionIssueAndTransferTokensSchnorr(t *testing.T) {
	config, err := testutil.TestWalletConfigWithTokenTransactionSchnorr()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, userLeaf1PrivKey, userLeaf2PrivKey, err := createTestTokenIssuanceTransaction(tokenIdentityPubKeyBytes)
	if err != nil {
		t.Fatalf("failed to create test token issuance transaction: %v", err)
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
		if leaf.GetWithdrawBondSats() != WithdrawalBondSatsInConfig {
			t.Errorf("leaf %d: expected withdrawal bond sats 1000000, got %d", i, leaf.GetWithdrawBondSats())
		}
		if leaf.GetWithdrawRelativeBlockLocktime() != WithdrawalRelativeBlockLocktimeInConfig {
			t.Errorf("leaf %d: expected withdrawal relative block locktime 1000, got %d", i, leaf.GetWithdrawRelativeBlockLocktime())
		}
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	if err != nil {
		t.Fatalf("failed to hash final issuance token transaction: %v", err)
	}

	transferTokenTransaction, _, err := createTestTokenTransferTransaction(
		finalIssueTokenTransactionHash,
		tokenIdentityPubKeyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}

	revPubKey1 := finalIssueTokenTransaction.OutputLeaves[0].RevocationPublicKey
	revPubKey2 := finalIssueTokenTransaction.OutputLeaves[1].RevocationPublicKey

	// Broadcast the token transaction
	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, transferTokenTransaction,
		[]*secp256k1.PrivateKey{userLeaf1PrivKey, userLeaf2PrivKey},
		[][]byte{revPubKey1, revPubKey2},
	)
	if err != nil {
		t.Fatalf("failed to broadcast transfer token transaction: %v", err)
	}
	log.Printf("transfer broadcast finalized token transaction: %v", transferTokenTransactionResponse)
}

func TestFreezeAndUnfreezeTokensSchnorr(t *testing.T) {
	config, err := testutil.TestWalletConfigWithTokenTransactionSchnorr()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKeyBytes := tokenPrivKey.PubKey().SerializeCompressed()
	issueTokenTransaction, _, _, err := createTestTokenIssuanceTransaction(tokenIdentityPubKeyBytes)
	if err != nil {
		t.Fatalf("failed to create test token issuance transaction: %v", err)
	}

	// Broadcast the token transaction
	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransaction(
		context.Background(), config, issueTokenTransaction,
		[]*secp256k1.PrivateKey{&tokenPrivKey},
		[][]byte{})
	if err != nil {
		t.Fatalf("failed to broadcast issuance token transaction: %v", err)
	}

	// Call FreezeTokens to freeze the output leaf
	_, err = wallet.FreezeTokens(
		context.Background(),
		config,
		finalIssueTokenTransaction.OutputLeaves[0].OwnerPublicKey,
		tokenIdentityPubKeyBytes,
		false,
	)
	if err != nil {
		t.Fatalf("failed to freeze tokens: %v", err)
	}
}
