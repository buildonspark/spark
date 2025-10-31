package tokens_test

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"sort"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/utils"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	TestValidityDurationSecs      = 30
	TestValidityDurationSecsPlus1 = TestValidityDurationSecs + 1
	TooLongValidityDurationSecs   = 300 + 1
	TooShortValidityDurationSecs  = 0
	TokenTransactionVersion1      = 1
	TokenTransactionVersion2      = 2
	TokenTransactionVersion3      = 3

	// Test token amounts for various operations
	testIssueOutput1Amount                          = 11
	testIssueOutput2Amount                          = 22
	testTransferOutput1Amount                       = 33
	withdrawalBondSatsInConfig                      = 10000
	withdrawalRelativeBlockLocktimeInConfig         = 1000
	testTokenName                                   = "TestToken"
	testTokenTicker                                 = "TEST"
	testTokenDecimals                               = 8
	testTokenIsFreezable                            = true
	testTokenMaxSupply                              = 0
	testIssueMultiplePerOutputAmount                = 500
	maxInputOrOutputTokenTransactionOutputsForTests = 48
	manyOutputsCount                                = 10
)

// Predefined issuer key for tests
type prederivedIdentityPrivateKeyFromMnemonic struct {
	identityPrivateKeyHex string
}

func (k *prederivedIdentityPrivateKeyFromMnemonic) IdentityPrivateKey() keys.Private {
	return keys.MustParsePrivateKeyHex(k.identityPrivateKeyHex)
}

var staticLocalIssuerKey = prederivedIdentityPrivateKeyFromMnemonic{
	identityPrivateKeyHex: "515c86ccb09faa2235acd0e287381bf286b37002328a8cc3c3b89738ab59dc93",
}

// Helper functions for tests
func int64ToUint128Bytes(high, low uint64) []byte {
	result := make([]byte, 0, 16)
	result = append(result, byte(high>>56), byte(high>>48), byte(high>>40), byte(high>>32), byte(high>>24), byte(high>>16), byte(high>>8), byte(high))
	result = append(result, byte(low>>56), byte(low>>48), byte(low>>40), byte(low>>32), byte(low>>24), byte(low>>16), byte(low>>8), byte(low))
	return result
}

func getSigningOperatorPublicKeyBytes(config *wallet.TestWalletConfig) [][]byte {
	var operatorKeys [][]byte
	for _, operator := range config.SigningOperators {
		operatorKeys = append(operatorKeys, operator.IdentityPublicKey.Serialize())
	}
	return operatorKeys
}

func bytesToBigInt(value []byte) *big.Int {
	return new(big.Int).SetBytes(value)
}

func uint64ToBigInt(value uint64) *big.Int {
	return new(big.Int).SetBytes(int64ToUint128Bytes(0, value))
}

func getTokenMaxSupplyBytes(maxSupply uint64) []byte {
	return int64ToUint128Bytes(0, maxSupply)
}

// Parameter structs for WithParams functions
type tokenTransactionParams struct {
	TokenIdentityPubKey            keys.Public
	IsNativeSparkToken             bool
	UseTokenIdentifier             bool
	FinalIssueTokenTransactionHash []byte   // Only used for transfers, nil for mints
	NumOutputs                     int      // Number of outputs to create (defaults to 2 for backward compatibility)
	OutputAmounts                  []uint64 // Exact amounts for each output (must match NumOutputs length)
	MintToSelf                     bool
	InvoiceAttachments             []*tokenpb.InvoiceAttachment
	Version                        int // Optional explicit token transaction version (defaults to V2 if 0)
}

type sparkTokenCreationTestParams struct {
	issuerPrivateKey keys.Private
	name             string
	ticker           string
	maxSupply        uint64
	expectedError    bool // optional, defaults to false
}

type CoordinatorScenario struct {
	name            string
	sameCoordinator bool
}

type TimestampScenario struct {
	name          string
	timestampMode TimestampScenarioMode
}

type PreemptionTestCase struct {
	name                  string
	sameCoordinator       bool
	timestampMode         TimestampScenarioMode
	secondRequestScenario SecondRequestScenarioMode
}

type TransactionResult struct {
	config        *wallet.TestWalletConfig
	resp          *tokenpb.StartTransactionResponse
	txFullHash    []byte
	txPartialHash []byte
}

type TimestampScenarioMode int

const (
	TimestampScenarioEqual TimestampScenarioMode = iota
	TimestampScenarioFirstEarlier
	TimestampScenarioSecondEarlier
	TimestampScenarioExpired
)

type SecondRequestScenarioMode int

const (
	SecondRequestScenarioAfterStart SecondRequestScenarioMode = iota
	SecondRequestScenarioAfterSignTokenTransactionFromCoordination
)

type SecondRequestScenario struct {
	name                  string
	secondRequestScenario SecondRequestScenarioMode
}

var signatureTypeTestCases = []struct {
	name                 string
	useSchnorrSignatures bool
}{
	{
		name:                 "ECDSA signatures",
		useSchnorrSignatures: false,
	},
	{
		name:                 "Schnorr signatures",
		useSchnorrSignatures: true,
	},
}

// createTestTokenMintTransactionTokenPbWithParams creates a test token mint transaction with custom parameters
func createTestTokenMintTransactionTokenPbWithParams(t *testing.T, config *wallet.TestWalletConfig, params tokenTransactionParams) (*tokenpb.TokenTransaction, []keys.Private, error) {
	numOutputs := params.NumOutputs
	if numOutputs == 0 {
		numOutputs = 2
	}

	if len(params.OutputAmounts) == 0 {
		return nil, nil, fmt.Errorf("OutputAmounts must be provided and cannot be empty")
	}
	if len(params.OutputAmounts) != numOutputs {
		return nil, nil, fmt.Errorf("OutputAmounts length (%d) must match NumOutputs (%d)", len(params.OutputAmounts), numOutputs)
	}

	outputAmounts := params.OutputAmounts

	userOutputPrivKeys := make([]keys.Private, numOutputs)
	tokenOutputs := make([]*tokenpb.TokenOutput, numOutputs)

	for i := range numOutputs {
		var pubKey keys.Public
		if params.MintToSelf {
			pubKey = params.TokenIdentityPubKey
			userOutputPrivKeys[i] = config.IdentityPrivateKey
		} else {
			privKey := keys.GeneratePrivateKey()
			userOutputPrivKeys[i] = privKey
			pubKey = privKey.Public()
		}

		tokenOutputs[i] = &tokenpb.TokenOutput{
			OwnerPublicKey: pubKey.Serialize(),
			TokenPublicKey: params.TokenIdentityPubKey.Serialize(),
			TokenAmount:    int64ToUint128Bytes(0, outputAmounts[i]),
		}
	}

	now := time.Now()
	version := uint32(TokenTransactionVersion2)
	if params.Version != 0 {
		version = uint32(params.Version)
	}
	mintTokenTransaction := &tokenpb.TokenTransaction{
		Version: version,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: params.TokenIdentityPubKey.Serialize(),
			},
		},
		TokenOutputs:                    tokenOutputs,
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
		ClientCreatedTimestamp:          timestamppb.New(now),
	}

	if version >= 3 {
		sort.Slice(mintTokenTransaction.SparkOperatorIdentityPublicKeys, func(i, j int) bool {
			return bytes.Compare(mintTokenTransaction.SparkOperatorIdentityPublicKeys[i], mintTokenTransaction.SparkOperatorIdentityPublicKeys[j]) < 0
		})
	}

	if params.UseTokenIdentifier {
		tokenIdentifier, err := getTokenIdentifierFromMetadata(t.Context(), config, params.TokenIdentityPubKey)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get token identifier from metadata: %w", err)
		}
		mintTokenTransaction.GetMintInput().TokenIdentifier = tokenIdentifier
		for _, output := range mintTokenTransaction.TokenOutputs {
			output.TokenIdentifier = tokenIdentifier
			output.TokenPublicKey = nil
		}
	}

	return mintTokenTransaction, userOutputPrivKeys, nil
}

// createTestTokenMintTransactionTokenPb creates a test token mint transaction with default parameters
func createTestTokenMintTransactionTokenPb(t *testing.T, config *wallet.TestWalletConfig, tokenIdentityPubKey keys.Public) (*tokenpb.TokenTransaction, keys.Private, keys.Private, error) {
	tx, privKeys, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey: tokenIdentityPubKey,
		IsNativeSparkToken:  false,
		UseTokenIdentifier:  true,
		NumOutputs:          2,
		OutputAmounts:       []uint64{uint64(testIssueOutput1Amount), uint64(testIssueOutput2Amount)},
	})
	if err != nil {
		return nil, keys.Private{}, keys.Private{}, err
	}
	if len(privKeys) != 2 {
		return nil, keys.Private{}, keys.Private{}, fmt.Errorf("expected 2 private keys, got %d", len(privKeys))
	}
	return tx, privKeys[0], privKeys[1], nil
}

// createTestTokenTransferTransactionTokenPbWithParams creates a test token transfer transaction with custom parameters
func createTestTokenTransferTransactionTokenPbWithParams(t *testing.T, config *wallet.TestWalletConfig, params tokenTransactionParams) (*tokenpb.TokenTransaction, keys.Private, error) {
	userOutput3PrivKey := keys.GeneratePrivateKey()
	version := uint32(TokenTransactionVersion2)
	if params.Version != 0 {
		version = uint32(params.Version)
	}
	transferTokenTransaction := &tokenpb.TokenTransaction{
		Version: version,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: []*tokenpb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: params.FinalIssueTokenTransactionHash,
						PrevTokenTransactionVout: 0,
					},
					{
						PrevTokenTransactionHash: params.FinalIssueTokenTransactionHash,
						PrevTokenTransactionVout: 1,
					},
				},
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey: userOutput3PrivKey.Public().Serialize(),
				TokenPublicKey: params.TokenIdentityPubKey.Serialize(),
				TokenAmount:    int64ToUint128Bytes(0, testTransferOutput1Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
		ClientCreatedTimestamp:          timestamppb.New(time.Now()),
		InvoiceAttachments:              params.InvoiceAttachments,
	}

	if version >= 3 {
		sort.Slice(transferTokenTransaction.SparkOperatorIdentityPublicKeys, func(i, j int) bool {
			return bytes.Compare(transferTokenTransaction.SparkOperatorIdentityPublicKeys[i], transferTokenTransaction.SparkOperatorIdentityPublicKeys[j]) < 0
		})
	}

	if params.UseTokenIdentifier {
		tokenIdentifier, err := getTokenIdentifierFromMetadata(t.Context(), config, params.TokenIdentityPubKey)
		if err != nil {
			return nil, keys.Private{}, fmt.Errorf("failed to get token identifier from metadata: %w", err)
		}
		transferTokenTransaction.TokenOutputs[0].TokenIdentifier = tokenIdentifier
		transferTokenTransaction.TokenOutputs[0].TokenPublicKey = nil
	}
	return transferTokenTransaction, userOutput3PrivKey, nil
}

// createTestTokenTransferTransactionTokenPb creates a test token transfer transaction with default parameters
func createTestTokenTransferTransactionTokenPb(
	t *testing.T,
	config *wallet.TestWalletConfig,
	finalIssueTokenTransactionHash []byte,
	tokenIdentityPubKey keys.Public,
) (*tokenpb.TokenTransaction, keys.Private, error) {
	return createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey:            tokenIdentityPubKey,
		IsNativeSparkToken:             false,
		UseTokenIdentifier:             true,
		FinalIssueTokenTransactionHash: finalIssueTokenTransactionHash,
		NumOutputs:                     1,
		OutputAmounts:                  []uint64{uint64(testTransferOutput1Amount)},
	})
}

// createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb creates a test mint transaction with multiple outputs
func createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb(t *testing.T,
	config *wallet.TestWalletConfig,
	tokenIdentityPubKey keys.Public, numOutputs int,
) (*tokenpb.TokenTransaction, []keys.Private, error) {
	outputAmounts := make([]uint64, numOutputs)
	for i := 0; i < numOutputs; i++ {
		outputAmounts[i] = uint64(testIssueMultiplePerOutputAmount)
	}

	return createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey: tokenIdentityPubKey,
		IsNativeSparkToken:  false,
		UseTokenIdentifier:  true,
		NumOutputs:          numOutputs,
		OutputAmounts:       outputAmounts,
	})
}

// testCoordinatedCreateNativeSparkTokenWithParams creates a native Spark token with custom parameters
func testCoordinatedCreateNativeSparkTokenWithParams(t *testing.T, config *wallet.TestWalletConfig, params sparkTokenCreationTestParams) error {
	createTx, err := createTestCoordinatedTokenCreateTransactionWithParams(config, params)
	if err != nil {
		return err
	}
	_, err = wallet.BroadcastTokenTransfer(
		t.Context(),
		config,
		createTx,
		[]keys.Private{params.issuerPrivateKey},
	)
	return err
}

// createTestCoordinatedTokenCreateTransactionWithParams creates a token create transaction
func createTestCoordinatedTokenCreateTransactionWithParams(config *wallet.TestWalletConfig, params sparkTokenCreationTestParams) (*tokenpb.TokenTransaction, error) {
	createTokenTransaction := &tokenpb.TokenTransaction{
		Version: TokenTransactionVersion2,
		TokenInputs: &tokenpb.TokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey: params.issuerPrivateKey.Public().Serialize(),
				TokenName:       params.name,
				TokenTicker:     params.ticker,
				Decimals:        testTokenDecimals,
				MaxSupply:       getTokenMaxSupplyBytes(params.maxSupply),
				IsFreezable:     testTokenIsFreezable,
			},
		},
		TokenOutputs:                    []*tokenpb.TokenOutput{},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
		ClientCreatedTimestamp:          timestamppb.New(time.Now()),
	}
	return createTokenTransaction, nil
}

// verifyTokenMetadata verifies individual token metadata entries
func verifyTokenMetadata(t *testing.T, metadata *tokenpb.TokenMetadata, expectedParams sparkTokenCreationTestParams, queryMethod string) {
	issuerPublicKey := expectedParams.issuerPrivateKey.Public().Serialize()
	require.Equal(t, expectedParams.name, metadata.TokenName, "%s: token name should match, expected: %s, found: %s", queryMethod, expectedParams.name, metadata.TokenName)
	require.Equal(t, expectedParams.ticker, metadata.TokenTicker, "%s: token ticker should match, expected: %s, found: %s", queryMethod, expectedParams.ticker, metadata.TokenTicker)
	require.Equal(t, uint32(testTokenDecimals), metadata.Decimals, "%s: token decimals should match, expected: %d, found: %d", queryMethod, uint32(testTokenDecimals), metadata.Decimals)
	require.Equal(t, testTokenIsFreezable, metadata.IsFreezable, "%s: token freezable flag should match, expected: %t, found: %t", queryMethod, testTokenIsFreezable, metadata.IsFreezable)
	require.True(t, bytes.Equal(issuerPublicKey, metadata.IssuerPublicKey), "%s: issuer public key should match, expected: %x, found: %x", queryMethod, issuerPublicKey, metadata.IssuerPublicKey)
	require.True(t, bytes.Equal(getTokenMaxSupplyBytes(expectedParams.maxSupply), metadata.MaxSupply), "%s: max supply should match, expected: %x, found: %x", queryMethod, getTokenMaxSupplyBytes(expectedParams.maxSupply), metadata.MaxSupply)
}

// createNativeToken creates a native token (no verification)
func createNativeToken(t *testing.T, params sparkTokenCreationTestParams) error {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, params.issuerPrivateKey)
	return testCoordinatedCreateNativeSparkTokenWithParams(t, config, params)
}

// verifyNativeToken verifies a token exists and returns its identifier
func verifyNativeToken(t *testing.T, params sparkTokenCreationTestParams) []byte {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, params.issuerPrivateKey)

	issuerPubKey := params.issuerPrivateKey.Public()
	resp, err := wallet.QueryTokenMetadata(t.Context(), config, nil, []keys.Public{issuerPubKey})
	require.NoError(t, err, "failed to query created token metadata")
	require.Len(t, resp.TokenMetadata, 1, "expected exactly 1 token metadata entry")

	return resp.TokenMetadata[0].TokenIdentifier
}

// queryAndVerifyTokenOutputs verifies the token outputs from the given finalTokenTransaction assigned to the owner private key are queryable
func queryAndVerifyTokenOutputs(t *testing.T, coordinatorIdentifiers []string, finalTokenTransaction *tokenpb.TokenTransaction, ownerPrivateKey keys.Private) {
	ownerPubKeyBytes := ownerPrivateKey.Public().Serialize()
	var expectedOutputs []*tokenpb.TokenOutput
	for _, output := range finalTokenTransaction.TokenOutputs {
		if bytes.Equal(output.OwnerPublicKey, ownerPubKeyBytes) {
			expectedOutputs = append(expectedOutputs, output)
		}
	}

	for _, coordinatorIdentifier := range coordinatorIdentifiers {
		config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
		config.CoordinatorIdentifier = coordinatorIdentifier

		outputs, err := wallet.QueryTokenOutputs(t.Context(), config, []keys.Public{ownerPrivateKey.Public()}, nil)
		require.NoError(t, err, "failed to query token outputs from coordinator: %s", coordinatorIdentifier)
		require.Len(t, outputs.OutputsWithPreviousTransactionData, len(expectedOutputs), "expected %d outputs from coordinator: %s", len(expectedOutputs), coordinatorIdentifier)

		for j, expectedOutput := range expectedOutputs {
			assert.Equal(t, expectedOutput.Id, outputs.OutputsWithPreviousTransactionData[j].Output.Id, "expected the same output ID for output %d from coordinator: %s", j, coordinatorIdentifier)
		}
	}
}

// queryAndVerifyNoTokenOutputs verifies that no token outputs are queryable for the ownerPrivateKey
func queryAndVerifyNoTokenOutputs(t *testing.T, coordinatorIdentifiers []string, ownerPrivateKey keys.Private) {
	queryAndVerifyTokenOutputs(t, coordinatorIdentifiers, &tokenpb.TokenTransaction{TokenOutputs: []*tokenpb.TokenOutput{}}, ownerPrivateKey)
}

// getTokenIdentifierFromMetadata retrieves token identifier by querying token metadata
func getTokenIdentifierFromMetadata(ctx context.Context, config *wallet.TestWalletConfig, issuerPubKey keys.Public) ([]byte, error) {
	response, err := wallet.QueryTokenMetadata(
		ctx,
		config,
		nil,
		[]keys.Public{issuerPubKey},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query token metadata: %w", err)
	}

	if len(response.TokenMetadata) == 0 {
		return nil, fmt.Errorf("no token metadata found for issuer public key")
	}

	return response.TokenMetadata[0].TokenIdentifier, nil
}

// verifyMultipleTokenIdentifiersQuery verifies querying for multiple token identifiers in a single RPC call
func verifyMultipleTokenIdentifiersQuery(t *testing.T, config *wallet.TestWalletConfig, tokenIdentifiers [][]byte, expectedCount int) {
	resp, err := wallet.QueryTokenMetadata(t.Context(), config, tokenIdentifiers, nil)
	require.NoError(t, err, "failed to query multiple tokens by their identifiers")
	require.Len(t, resp.TokenMetadata, expectedCount, "expected exactly %d token metadata entries when querying multiple tokens", expectedCount)

	responseIdentifiers := make(map[string]bool)
	for _, metadata := range resp.TokenMetadata {
		responseIdentifiers[string(metadata.TokenIdentifier)] = true
	}

	for i, tokenID := range tokenIdentifiers {
		assert.Contains(t, responseIdentifiers, string(tokenID), "token identifier %d should be present in response", i)
	}
}

// setTransactionTimestamps sets the client timestamps on both transactions based on the test scenario
func setTransactionTimestamps(transaction1, transaction2 *tokenpb.TokenTransaction, timestampMode TimestampScenarioMode) {
	now := time.Now()
	switch timestampMode {
	case TimestampScenarioEqual:
		transaction1.ClientCreatedTimestamp = timestamppb.New(now)
		transaction2.ClientCreatedTimestamp = timestamppb.New(now)
	case TimestampScenarioFirstEarlier, TimestampScenarioExpired:
		transaction1.ClientCreatedTimestamp = timestamppb.New(now.Add(-time.Second))
		transaction2.ClientCreatedTimestamp = timestamppb.New(now)
	case TimestampScenarioSecondEarlier:
		transaction1.ClientCreatedTimestamp = timestamppb.New(now)
		transaction2.ClientCreatedTimestamp = timestamppb.New(now.Add(-time.Second))
	default:
		panic(fmt.Sprintf("unknown timestamp scenario mode: %d", timestampMode))
	}
}

// determineWinningAndLosingTransactions determines which transaction should win based on the test scenario
func determineWinningAndLosingTransactions(
	tc PreemptionTestCase,
	transactionResult1, transactionResult2 *TransactionResult,
) (*TransactionResult, *TransactionResult) {
	var firstShouldWin bool

	switch tc.timestampMode {
	case TimestampScenarioEqual:
		firstShouldWin = bytes.Compare(transactionResult1.txPartialHash, transactionResult2.txPartialHash) < 0
	case TimestampScenarioFirstEarlier:
		firstShouldWin = true
	case TimestampScenarioSecondEarlier, TimestampScenarioExpired:
		firstShouldWin = false
	default:
		panic(fmt.Sprintf("unknown timestamp scenario mode: %d", tc.timestampMode))
	}

	var winningResult, losingResult *TransactionResult
	if firstShouldWin {
		winningResult = transactionResult1
		losingResult = nil
	} else {
		winningResult = transactionResult2
		losingResult = transactionResult1
	}

	return winningResult, losingResult
}

// signAndCommitTransaction signs and commits a transaction
func signAndCommitTransaction(t *testing.T, transactionResult *TransactionResult, ownerPrivateKeys []keys.Private) (*tokenpb.CommitTransactionResponse, error) {
	operatorSignatures, err := wallet.CreateOperatorSpecificSignatures(
		transactionResult.config,
		ownerPrivateKeys,
		transactionResult.txFullHash,
	)
	require.NoError(t, err, "failed to create operator-specific signatures for winning transaction")

	commitReq := &tokenpb.CommitTransactionRequest{
		FinalTokenTransaction:          transactionResult.resp.FinalTokenTransaction,
		FinalTokenTransactionHash:      transactionResult.txFullHash,
		InputTtxoSignaturesPerOperator: operatorSignatures,
		OwnerIdentityPublicKey:         transactionResult.config.IdentityPublicKey().Serialize(),
	}

	return wallet.CommitTransaction(t.Context(), transactionResult.config, commitReq)
}

// sumUint64Slice sums a slice of uint64 values
func sumUint64Slice(values []uint64) uint64 {
	var sum uint64
	for _, v := range values {
		sum += v
	}
	return sum
}

// tokenSetupResult contains the result of setting up a token with minted outputs
type tokenSetupResult struct {
	IssuerPrivateKey      keys.Private
	Config                *wallet.TestWalletConfig
	TokenIdentifier       []byte
	MintTxHash            []byte
	MintTx                *tokenpb.TokenTransaction
	MintTxBeforeBroadcast *tokenpb.TokenTransaction
	OutputOwners          []keys.Private
}

// setupNativeTokenWithMint creates a native token, mints to outputs, and returns all relevant data
func setupNativeTokenWithMint(
	t *testing.T,
	name string,
	ticker string,
	maxSupply uint64,
	mintOutputAmounts []uint64,
) (*tokenSetupResult, error) {
	issuerPrivKey := keys.GeneratePrivateKey()
	config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivKey)

	err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, sparkTokenCreationTestParams{
		issuerPrivateKey: issuerPrivKey,
		name:             name,
		ticker:           ticker,
		maxSupply:        maxSupply,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create native spark token: %w", err)
	}

	tokenIdentifier, err := getTokenIdentifierFromMetadata(t.Context(), config, issuerPrivKey.Public())
	if err != nil {
		return nil, fmt.Errorf("failed to get token identifier: %w", err)
	}

	mintTxBeforeBroadcast, outputOwners, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey: issuerPrivKey.Public(),
		IsNativeSparkToken:  true,
		UseTokenIdentifier:  true,
		NumOutputs:          len(mintOutputAmounts),
		OutputAmounts:       mintOutputAmounts,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create mint transaction: %w", err)
	}

	mintTxForBroadcast := proto.Clone(mintTxBeforeBroadcast).(*tokenpb.TokenTransaction)
	finalMintTx, err := wallet.BroadcastTokenTransfer(
		t.Context(), config, mintTxForBroadcast,
		[]keys.Private{issuerPrivKey},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to broadcast mint transaction: %w", err)
	}

	mintTxHash, err := utils.HashTokenTransaction(finalMintTx, false)
	if err != nil {
		return nil, fmt.Errorf("failed to hash mint transaction: %w", err)
	}

	return &tokenSetupResult{
		IssuerPrivateKey:      issuerPrivKey,
		Config:                config,
		TokenIdentifier:       tokenIdentifier,
		MintTxHash:            mintTxHash,
		MintTx:                finalMintTx,
		MintTxBeforeBroadcast: mintTxBeforeBroadcast,
		OutputOwners:          outputOwners,
	}, nil
}

// verifyTokenBalance verifies that a user has the expected token balance
func verifyTokenBalance(
	t *testing.T,
	ownerPrivKey keys.Private,
	issuerPubKey keys.Public,
	expectedAmount uint64,
	description string,
) {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, ownerPrivKey)
	outputs, err := wallet.QueryTokenOutputs(
		t.Context(),
		config,
		[]keys.Public{ownerPrivKey.Public()},
		[]keys.Public{issuerPubKey},
	)
	require.NoError(t, err, "failed to query token outputs for %s", description)
	require.Len(t, outputs.OutputsWithPreviousTransactionData, 1, "expected 1 output for %s", description)

	amount := bytesToBigInt(outputs.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
	require.Equal(t, uint64ToBigInt(expectedAmount), amount, "%s should have %d tokens", description, expectedAmount)
}
