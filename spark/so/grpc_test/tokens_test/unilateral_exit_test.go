package tokens_test

import (
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/keys"
	pbtoken "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/l1withdrawaltransaction"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

const (
	withdrawalPollInterval = 10 * time.Millisecond
	withdrawalPollTimeout  = 2 * time.Second
)

func waitForWithdrawal(t *testing.T, entClient *ent.Client, withdrawalTxid chainhash.Hash, outputsNum int) {
	require.Eventually(t, func() bool {
		withdrawal, err := entClient.L1WithdrawalTransaction.
			Query().
			WithWithdrawals().
			Where(
				l1withdrawaltransaction.ConfirmationTxid(schematype.NewTxID(withdrawalTxid)),
			).
			Only(t.Context())
		return err == nil && len(withdrawal.Edges.Withdrawals) == outputsNum
	}, withdrawalPollTimeout, withdrawalPollInterval, "timed out waiting for withdrawal with %d outputs", outputsNum)
}

func waitForWithdrawalNotFound(t *testing.T, entClient *ent.Client, withdrawalTxid chainhash.Hash) {
	// Give the watchtower time to process, then verify no withdrawal was created
	time.Sleep(100 * time.Millisecond)
	withdrawal, err := entClient.L1WithdrawalTransaction.
		Query().
		WithWithdrawals().
		Where(
			l1withdrawaltransaction.ConfirmationTxid(schematype.NewTxID(withdrawalTxid)),
		).
		Only(t.Context())
	require.Error(t, err)
	require.Nil(t, withdrawal, "withdrawal found in db when none expected")
}

func assertWithdrawal(t *testing.T, entClient *ent.Client, withdrawalTxid chainhash.Hash, outputsNum int) {
	withdrawal, err := entClient.L1WithdrawalTransaction.
		Query().
		WithWithdrawals().
		Where(
			l1withdrawaltransaction.ConfirmationTxid(schematype.NewTxID(withdrawalTxid)),
		).
		Only(t.Context())
	require.NoError(t, err, "failed to query withdrawal")

	require.NotNil(t, withdrawal, "withdrawal not found in db")
	require.Len(t, withdrawal.Edges.Withdrawals, outputsNum)
}

func assertPunishedTokenWithdrawal(t *testing.T, entClient *ent.Client, withdrawalTxid chainhash.Hash, outputIndex int) *ent.L1TokenJusticeTransaction {
	withdrawal, err := entClient.L1WithdrawalTransaction.
		Query().
		WithWithdrawals().
		Where(
			l1withdrawaltransaction.ConfirmationTxid(schematype.NewTxID(withdrawalTxid)),
		).
		Only(t.Context())
	require.NoError(t, err, "failed to query withdrawal")

	require.NotNil(t, withdrawal, "withdrawal not found in db")

	tokenOutputWithdrawal := withdrawal.Edges.Withdrawals[outputIndex]
	justiceTx, err := tokenOutputWithdrawal.QueryJusticeTx().Only(t.Context())
	require.NoError(t, err, "failed to query justice tx")
	require.NotNil(t, justiceTx, "justice tx not found in db")
	return justiceTx
}

func assertMultiplePunishedWithdrawals(t *testing.T, entClient *ent.Client, withdrawalTxid chainhash.Hash, expectedCount int) []*ent.L1TokenJusticeTransaction {
	withdrawal, err := entClient.L1WithdrawalTransaction.
		Query().
		WithWithdrawals().
		Where(
			l1withdrawaltransaction.ConfirmationTxid(schematype.NewTxID(withdrawalTxid)),
		).
		Only(t.Context())
	require.NoError(t, err, "failed to query withdrawal")
	require.NotNil(t, withdrawal, "withdrawal not found in db")
	require.Len(t, withdrawal.Edges.Withdrawals, expectedCount, "expected %d withdrawals", expectedCount)

	var justiceTxs []*ent.L1TokenJusticeTransaction
	for i, outputWithdrawal := range withdrawal.Edges.Withdrawals {
		justiceTx, err := outputWithdrawal.QueryJusticeTx().Only(t.Context())
		require.NoError(t, err, "failed to query justice tx for output %d", i)
		require.NotNil(t, justiceTx, "justice tx not found for output %d", i)
		justiceTxs = append(justiceTxs, justiceTx)
	}
	return justiceTxs
}

func assertAndReturnTokenOutputs(t *testing.T, config *wallet.TestWalletConfig, ownerPublicKey keys.Public, issuerPublicKey keys.Public, outputsNum int) *pbtoken.QueryTokenOutputsResponse {
	outputsResp, err := wallet.QueryTokenOutputs(
		t.Context(),
		config,
		[]keys.Public{ownerPublicKey},
		[]keys.Public{issuerPublicKey},
	)
	require.NoError(t, err, "failed to query token outputs")
	require.Len(t, outputsResp.OutputsWithPreviousTransactionData, outputsNum)

	return outputsResp
}

func broadcastWithdrawalTransaction(t *testing.T, client *rpcclient.Client, coin sparktesting.FaucetCoin, tokenOutputs []*pbtoken.OutputWithPreviousTransactionData, seEntityPublicKey *keys.Public, ownerSignature []byte) chainhash.Hash {
	withdrawTx, err := ConstructUnilateralWithdrawal(tokenOutputs, seEntityPublicKey.Serialize(), ownerSignature)
	require.NoError(t, err, "failed to construct unilateral withdrawal tx")

	txIn := wire.NewTxIn(coin.OutPoint, nil, [][]byte{})
	txIn.Sequence = 0xFFFFFFFF

	withdrawTx.AddTxIn(txIn)

	signedWithdrawTx, err := sparktesting.SignFaucetCoin(withdrawTx, coin.TxOut, coin.Key)
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}

	_, err = client.SendRawTransaction(signedWithdrawTx, true)
	require.NoError(t, err, "failed to broadcast withdrawal tx")

	withdrawalTxid := signedWithdrawTx.TxHash()

	return withdrawalTxid
}

func validWithdrawal(t *testing.T, outputNum int) {
	sparktesting.SkipIfGithubActions(t)

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	mintTokenOutputs := make([]uint64, 0, outputNum)
	for i := 1; i <= outputNum; i++ {
		mintTokenOutputs = append(mintTokenOutputs, uint64(i*1000))
	}

	setupResult, err := setupNativeTokenWithMint(t,
		testTokenName,
		testTokenTicker,
		testTokenMaxSupply,
		mintTokenOutputs,
		true,
	)
	require.NoError(t, err, "failed to create and mint native spark token")

	config := setupResult.Config
	ownerPrivateKey := setupResult.OutputOwners[0]

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	entityDkgKey, err := ent.GetEntityDkgKey(t.Context(), entClient)
	require.NoError(t, err, "failed to query SE entity public key")
	seEntityPublicKey := entityDkgKey.Edges.SigningKeyshare.PublicKey

	outputsResp := assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), outputNum)
	tokenOutputsWithTxData := outputsResp.OutputsWithPreviousTransactionData

	ownerSignature, err := ComputeUnilateralExitOwnerSignature(tokenOutputsWithTxData, ownerPrivateKey)
	require.NoError(t, err, "failed to compute owner's signature")

	withdrawalTxid := broadcastWithdrawalTransaction(t, client, coin, tokenOutputsWithTxData, &seEntityPublicKey, ownerSignature.Serialize())

	err = faucet.MineBlocks(6)
	require.NoError(t, err, "failed to mine withdrawal tx")

	waitForWithdrawal(t, entClient, withdrawalTxid, outputNum)
	assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), 0)
}

func TestValidTokenUnilateralExit(t *testing.T) {
	validWithdrawal(t, 1)
}

func TestValidTokenUnilateralExitMultipleOutputs(t *testing.T) {
	validWithdrawal(t, 3)
}

func TestInvalidTokenUnilateralExit_InvalidOwnerSignature(t *testing.T) {
	sparktesting.SkipIfGithubActions(t)

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	setupResult, err := setupNativeTokenWithMint(t,
		testTokenName,
		testTokenTicker,
		testTokenMaxSupply,
		[]uint64{1000},
		true,
	)
	require.NoError(t, err, "failed to create and mint native spark token")

	config := setupResult.Config
	ownerPrivateKey := setupResult.OutputOwners[0]

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	entityDkgKey, err := ent.GetEntityDkgKey(t.Context(), entClient)
	require.NoError(t, err, "failed to query SE entity public key")
	seEntityPublicKey := entityDkgKey.Edges.SigningKeyshare.PublicKey

	outputsResp := assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), 1)

	tokenOutputsWithTxData := outputsResp.OutputsWithPreviousTransactionData

	invalidOwnerSignature := make([]byte, 64)

	withdrawalTxid := broadcastWithdrawalTransaction(t, client, coin, tokenOutputsWithTxData, &seEntityPublicKey, invalidOwnerSignature)

	err = faucet.MineBlocks(6)
	require.NoError(t, err, "failed to mine withdrawal tx")

	waitForWithdrawalNotFound(t, entClient, withdrawalTxid)
	assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), 1)
}

func TestInvalidTokenUnilateralExit_InvalidOwner(t *testing.T) {
	sparktesting.SkipIfGithubActions(t)

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	setupResult, err := setupNativeTokenWithMint(t,
		testTokenName,
		testTokenTicker,
		testTokenMaxSupply,
		[]uint64{1000, 2000, 3000},
		true,
	)
	require.NoError(t, err, "failed to create and mint native spark token")

	config := setupResult.Config
	ownerPrivateKey := keys.GeneratePrivateKey()

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	entityDkgKey, err := ent.GetEntityDkgKey(t.Context(), entClient)
	require.NoError(t, err, "failed to query SE entity public key")
	seEntityPublicKey := entityDkgKey.Edges.SigningKeyshare.PublicKey

	outputsResp := assertAndReturnTokenOutputs(t, config, setupResult.OutputOwners[0].Public(), setupResult.IssuerPrivateKey.Public(), 3)
	tokenOutputsWithTxData := outputsResp.OutputsWithPreviousTransactionData

	ownerSignature, err := ComputeUnilateralExitOwnerSignature(tokenOutputsWithTxData, ownerPrivateKey)
	require.NoError(t, err, "failed to compute owner's signature")

	withdrawalTxid := broadcastWithdrawalTransaction(t, client, coin, tokenOutputsWithTxData, &seEntityPublicKey, ownerSignature.Serialize())

	err = faucet.MineBlocks(6)
	require.NoError(t, err, "failed to mine withdrawal tx")

	waitForWithdrawalNotFound(t, entClient, withdrawalTxid)
	assertAndReturnTokenOutputs(t, config, setupResult.OutputOwners[0].Public(), setupResult.IssuerPrivateKey.Public(), 3)
}

// TestInvalidTokenUnilateralExit_DoubleSpend_PunishedWithdrawal tests the scenario where
// a user transfers a token off-chain, then tries to withdraw the old state to L1.
// The withdrawal should be detected as invalid and punished with a justice tx.
func TestInvalidTokenUnilateralExit_DoubleSpend_PunishedWithdrawal(t *testing.T) {
	sparktesting.SkipIfGithubActions(t)

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	setupResult, err := setupNativeTokenWithMint(t,
		testTokenName,
		testTokenTicker,
		testTokenMaxSupply,
		[]uint64{1000},
		true,
	)
	require.NoError(t, err, "failed to create and mint native spark token")

	config := setupResult.Config
	ownerPrivateKey := setupResult.OutputOwners[0]

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	entityDkgKey, err := ent.GetEntityDkgKey(t.Context(), entClient)
	require.NoError(t, err, "failed to query SE entity public key")
	seEntityPublicKey := entityDkgKey.Edges.SigningKeyshare.PublicKey

	outputsResp := assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), 1)

	transferTokenTransaction, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey:            setupResult.IssuerPrivateKey.Public(),
		TokenIdentifier:                setupResult.TokenIdentifier,
		FinalIssueTokenTransactionHash: setupResult.MintTxHash,
		NumOutputs:                     1,
		NumOutputsToSpend:              1,
		OutputAmounts:                  []uint64{1000},
	})
	require.NoError(t, err, "failed to create test token transfer transaction")

	_, err = broadcastTokenTransaction(
		t,
		t.Context(),
		config,
		transferTokenTransaction,
		[]keys.Private{ownerPrivateKey},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")

	tokenOutputsWithTxData := outputsResp.OutputsWithPreviousTransactionData

	ownerSignature, err := ComputeUnilateralExitOwnerSignature(tokenOutputsWithTxData, ownerPrivateKey)
	require.NoError(t, err, "failed to compute owner's signature")

	withdrawalTxid := broadcastWithdrawalTransaction(t, client, coin, tokenOutputsWithTxData, &seEntityPublicKey, ownerSignature.Serialize())

	err = faucet.MineBlocks(6)
	require.NoError(t, err, "failed to mine withdrawal tx")

	waitForWithdrawal(t, entClient, withdrawalTxid, 1)
	_ = assertPunishedTokenWithdrawal(t, entClient, withdrawalTxid, 0)
	assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), 0)
}

// TestInvalidTokenUnilateralExit_DoubleSpend_BlockedTransfer tests the scenario where
// a user withdraws to L1 first, then tries to transfer the same token off-chain.
// The transfer should be rejected by the SO.
func TestInvalidTokenUnilateralExit_DoubleSpend_BlockedTransfer(t *testing.T) {
	sparktesting.SkipIfGithubActions(t)

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	setupResult, err := setupNativeTokenWithMint(t,
		testTokenName,
		testTokenTicker,
		testTokenMaxSupply,
		[]uint64{1000},
		true,
	)
	require.NoError(t, err, "failed to create and mint native spark token")

	config := setupResult.Config
	ownerPrivateKey := setupResult.OutputOwners[0]

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	entityDkgKey, err := ent.GetEntityDkgKey(t.Context(), entClient)
	require.NoError(t, err, "failed to query SE entity public key")
	seEntityPublicKey := entityDkgKey.Edges.SigningKeyshare.PublicKey

	outputsResp := assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), 1)
	tokenOutputsWithTxData := outputsResp.OutputsWithPreviousTransactionData

	ownerSignature, err := ComputeUnilateralExitOwnerSignature(tokenOutputsWithTxData, ownerPrivateKey)
	require.NoError(t, err, "failed to compute owner's signature")

	withdrawalTxid := broadcastWithdrawalTransaction(t, client, coin, tokenOutputsWithTxData, &seEntityPublicKey, ownerSignature.Serialize())

	err = faucet.MineBlocks(6)
	require.NoError(t, err, "failed to mine withdrawal tx")

	// Wait for watchtower to record the withdrawal before attempting transfer
	waitForWithdrawal(t, entClient, withdrawalTxid, 1)

	transferTokenTransaction, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey:            setupResult.IssuerPrivateKey.Public(),
		TokenIdentifier:                setupResult.TokenIdentifier,
		FinalIssueTokenTransactionHash: setupResult.MintTxHash,
		NumOutputs:                     1,
		NumOutputsToSpend:              1,
		OutputAmounts:                  []uint64{1000},
	})
	require.NoError(t, err, "failed to create test token transfer transaction")

	_, err = broadcastTokenTransaction(
		t,
		t.Context(),
		config,
		transferTokenTransaction,
		[]keys.Private{ownerPrivateKey},
	)
	require.Error(t, err, "Successfully broadcast transfer token transaction")

	assertWithdrawal(t, entClient, withdrawalTxid, 1)
	assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), 0)
}

// TestPartiallyValidTokenUnilateralExit tests the scenario where a user tries to withdraw
// 3 outputs but only 1 has been spent (transferred). The 2 unspent outputs should be
// approved, and the 1 spent output should be punished with a justice transaction.
func TestPartiallyValidTokenUnilateralExit(t *testing.T) {
	sparktesting.SkipIfGithubActions(t)

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	setupResult, err := setupNativeTokenWithMint(t,
		testTokenName,
		testTokenTicker,
		testTokenMaxSupply,
		[]uint64{10000, 20000, 30000}, // Use larger amounts to cover justice tx fees
		true,
	)
	require.NoError(t, err, "failed to create and mint native spark token")

	config := setupResult.Config
	ownerPrivateKey := setupResult.OutputOwners[0]

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	entityDkgKey, err := ent.GetEntityDkgKey(t.Context(), entClient)
	require.NoError(t, err, "failed to query SE entity public key")
	seEntityPublicKey := entityDkgKey.Edges.SigningKeyshare.PublicKey

	outputsResp := assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), 3)

	// Transfer only output 0 - this reveals its revocation secret
	transferTokenTransaction, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey:            setupResult.IssuerPrivateKey.Public(),
		TokenIdentifier:                setupResult.TokenIdentifier,
		FinalIssueTokenTransactionHash: setupResult.MintTxHash,
		NumOutputs:                     1,
		NumOutputsToSpend:              1,
		OutputAmounts:                  []uint64{10000},
	})
	require.NoError(t, err, "failed to create test token transfer transaction")

	_, err = broadcastTokenTransaction(
		t,
		t.Context(),
		config,
		transferTokenTransaction,
		[]keys.Private{ownerPrivateKey},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")

	// Try to withdraw all 3 original outputs (1 spent, 2 valid)
	tokenOutputsWithTxData := outputsResp.OutputsWithPreviousTransactionData

	ownerSignature, err := ComputeUnilateralExitOwnerSignature(tokenOutputsWithTxData, ownerPrivateKey)
	require.NoError(t, err, "failed to compute owner's signature")

	withdrawalTxid := broadcastWithdrawalTransaction(t, client, coin, tokenOutputsWithTxData, &seEntityPublicKey, ownerSignature.Serialize())

	err = faucet.MineBlocks(6)
	require.NoError(t, err, "failed to mine withdrawal tx")

	// Should have 3 withdrawal records: 2 approved + 1 punished
	waitForWithdrawal(t, entClient, withdrawalTxid, 3)

	// The spent output (index 0) should have a justice tx
	_ = assertPunishedTokenWithdrawal(t, entClient, withdrawalTxid, 0)

	// All token outputs should now be gone (2 withdrawn validly, 1 punished)
	assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), 0)
}

// TestJusticeTransaction_ConfirmedOnChain verifies that the justice transaction
// actually gets mined and the withdrawal output is spent on-chain.
func TestJusticeTransaction_ConfirmedOnChain(t *testing.T) {
	sparktesting.SkipIfGithubActions(t)

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	setupResult, err := setupNativeTokenWithMint(t,
		testTokenName,
		testTokenTicker,
		testTokenMaxSupply,
		[]uint64{10000}, // Use larger amount to cover fees
		true,
	)
	require.NoError(t, err, "failed to create and mint native spark token")

	config := setupResult.Config
	ownerPrivateKey := setupResult.OutputOwners[0]

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	entityDkgKey, err := ent.GetEntityDkgKey(t.Context(), entClient)
	require.NoError(t, err, "failed to query SE entity public key")
	seEntityPublicKey := entityDkgKey.Edges.SigningKeyshare.PublicKey

	outputsResp := assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), 1)

	// Transfer the token to reveal the revocation secret
	transferTokenTransaction, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey:            setupResult.IssuerPrivateKey.Public(),
		TokenIdentifier:                setupResult.TokenIdentifier,
		FinalIssueTokenTransactionHash: setupResult.MintTxHash,
		NumOutputs:                     1,
		NumOutputsToSpend:              1,
		OutputAmounts:                  []uint64{10000},
	})
	require.NoError(t, err, "failed to create test token transfer transaction")

	_, err = broadcastTokenTransaction(
		t,
		t.Context(),
		config,
		transferTokenTransaction,
		[]keys.Private{ownerPrivateKey},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")

	tokenOutputsWithTxData := outputsResp.OutputsWithPreviousTransactionData

	ownerSignature, err := ComputeUnilateralExitOwnerSignature(tokenOutputsWithTxData, ownerPrivateKey)
	require.NoError(t, err, "failed to compute owner's signature")

	// Broadcast the invalid withdrawal (trying to withdraw already-spent output)
	withdrawalTxid := broadcastWithdrawalTransaction(t, client, coin, tokenOutputsWithTxData, &seEntityPublicKey, ownerSignature.Serialize())

	// Mine blocks to confirm the withdrawal tx and trigger justice tx
	err = faucet.MineBlocks(6)
	require.NoError(t, err, "failed to mine withdrawal tx")

	// Wait for watchtower to process and record the withdrawal
	waitForWithdrawal(t, entClient, withdrawalTxid, 1)

	// Verify the justice tx was recorded in the database
	justiceTx := assertPunishedTokenWithdrawal(t, entClient, withdrawalTxid, 0)
	require.NotNil(t, justiceTx.JusticeTxHash, "justice tx hash should be set")

	// Mine more blocks to confirm the justice tx
	err = faucet.MineBlocks(6)
	require.NoError(t, err, "failed to mine justice tx")

	// Verify the justice tx output is now spendable (the SO claimed the funds)
	// We check this by verifying the withdrawal output (vout 0) has been spent
	justiceTxHash := justiceTx.JusticeTxHash.Hash()

	// The justice tx should have one output (to the SO's address)
	txOut, err := client.GetTxOut(&justiceTxHash, 0, false)
	require.NoError(t, err, "failed to get justice tx output")
	require.NotNil(t, txOut, "justice tx output should exist (unspent)")
	require.Positive(t, txOut.Confirmations, "justice tx should be confirmed")
}

// TestJusticeTransaction_MultiplePunishedOutputs verifies that when multiple
// spent outputs are included in a single withdrawal tx, each gets a justice tx.
func TestJusticeTransaction_MultiplePunishedOutputs(t *testing.T) {
	sparktesting.SkipIfGithubActions(t)

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	// Mint 3 outputs
	setupResult, err := setupNativeTokenWithMint(t,
		testTokenName,
		testTokenTicker,
		testTokenMaxSupply,
		[]uint64{10000, 20000, 30000},
		true,
	)
	require.NoError(t, err, "failed to create and mint native spark token")

	config := setupResult.Config
	ownerPrivateKey := setupResult.OutputOwners[0]

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	entityDkgKey, err := ent.GetEntityDkgKey(t.Context(), entClient)
	require.NoError(t, err, "failed to query SE entity public key")
	seEntityPublicKey := entityDkgKey.Edges.SigningKeyshare.PublicKey

	outputsResp := assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), 3)

	// Transfer ALL 3 outputs to reveal revocation secrets for all of them
	transferTokenTransaction, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey:            setupResult.IssuerPrivateKey.Public(),
		TokenIdentifier:                setupResult.TokenIdentifier,
		FinalIssueTokenTransactionHash: setupResult.MintTxHash,
		NumOutputs:                     1,
		NumOutputsToSpend:              3, // Spend all 3 outputs
		OutputAmounts:                  []uint64{60000},
	})
	require.NoError(t, err, "failed to create test token transfer transaction")

	_, err = broadcastTokenTransaction(
		t,
		t.Context(),
		config,
		transferTokenTransaction,
		[]keys.Private{ownerPrivateKey},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")

	tokenOutputsWithTxData := outputsResp.OutputsWithPreviousTransactionData

	ownerSignature, err := ComputeUnilateralExitOwnerSignature(tokenOutputsWithTxData, ownerPrivateKey)
	require.NoError(t, err, "failed to compute owner's signature")

	// Try to withdraw all 3 (now spent) outputs
	withdrawalTxid := broadcastWithdrawalTransaction(t, client, coin, tokenOutputsWithTxData, &seEntityPublicKey, ownerSignature.Serialize())

	err = faucet.MineBlocks(6)
	require.NoError(t, err, "failed to mine withdrawal tx")

	// Wait for watchtower to process and record the withdrawal
	waitForWithdrawal(t, entClient, withdrawalTxid, 3)

	// Verify all 3 outputs got justice transactions
	justiceTxs := assertMultiplePunishedWithdrawals(t, entClient, withdrawalTxid, 3)
	require.Len(t, justiceTxs, 3, "expected 3 justice transactions")

	// Verify each justice tx has a unique hash
	seenHashes := make(map[string]bool)
	for i, jt := range justiceTxs {
		hashStr := jt.JusticeTxHash.String()
		require.False(t, seenHashes[hashStr], "justice tx %d has duplicate hash", i)
		seenHashes[hashStr] = true
		require.Positive(t, jt.AmountSats, "justice tx %d should have non-zero amount", i)
	}

	// All token outputs should now be gone
	assertAndReturnTokenOutputs(t, config, ownerPrivateKey.Public(), setupResult.IssuerPrivateKey.Public(), 0)
}
