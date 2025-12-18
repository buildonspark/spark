package tokens

import (
	"context"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/entfixtures"
	othertokens "github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

const (
	testTokenName        = "test token"
	testTokenTicker      = "TTT"
	testTokenDecimals    = 8
	testTokenMaxSupply   = 1000
	testTokenIsFreezable = true
)

var testTokenMaxSupplyBytes = padBytes(big.NewInt(testTokenMaxSupply).Bytes(), 16)

// mockSparkTokenInternalServiceServer provides a mock implementation of the gRPC service
// for testing cross-operator communication in token transactions.
type mockSparkTokenInternalServiceServer struct {
	tokeninternalpb.UnimplementedSparkTokenInternalServiceServer
	privKey     keys.Private
	errToReturn error
	// blockSign allows tests to pause the RPC response until they mutate DB state
	blockSign chan struct{}
	// hitSign is closed when the RPC is received, letting tests know they can mutate state
	hitSign chan struct{}
}

func (s *mockSparkTokenInternalServiceServer) SignTokenTransactionFromCoordination(
	_ context.Context,
	req *tokeninternalpb.SignTokenTransactionFromCoordinationRequest,
) (*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, error) {
	if s.errToReturn != nil {
		return nil, s.errToReturn
	}
	if s.hitSign != nil {
		// Signal that we've received the RPC and are about to respond
		close(s.hitSign)
	}
	if s.blockSign != nil {
		// Block until test allows us to respond
		<-s.blockSign
	}
	signature := ecdsa.Sign(s.privKey.ToBTCEC(), req.FinalTokenTransactionHash)
	return &tokeninternalpb.SignTokenTransactionFromCoordinationResponse{
		SparkOperatorSignature: signature.Serialize(),
	}, nil
}

func (s *mockSparkTokenInternalServiceServer) ExchangeRevocationSecretsShares(
	_ context.Context,
	_ *tokeninternalpb.ExchangeRevocationSecretsSharesRequest,
) (*tokeninternalpb.ExchangeRevocationSecretsSharesResponse, error) {
	if s.errToReturn != nil {
		return nil, s.errToReturn
	}
	// For this test simulation, the non-coordinator operator should not return their revocation secrets share.
	return &tokeninternalpb.ExchangeRevocationSecretsSharesResponse{
		ReceivedOperatorShares: []*tokeninternalpb.OperatorRevocationShares{},
	}, nil
}

// startMockGRPCServer starts a mock gRPC server for testing inter-operator communication
func startMockGRPCServer(t *testing.T, mockServer *mockSparkTokenInternalServiceServer) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	t.Cleanup(func() { _ = l.Close() })

	server := grpc.NewServer()
	tokeninternalpb.RegisterSparkTokenInternalServiceServer(server, mockServer)
	go func() {
		if err := server.Serve(l); err != nil {
			t.Logf("Mock gRPC server error: %v", err)
		}
	}()
	t.Cleanup(server.Stop)
	return addr
}

// padBytes pads a byte slice with leading zeros to a specified length.
func padBytes(b []byte, length int) []byte {
	if len(b) >= length {
		return b
	}
	padded := make([]byte, length)
	copy(padded[length-len(b):], b)
	return padded
}

// testSetupCommon contains common test setup data
type testSetupCommon struct {
	ctx                 context.Context
	sessionCtx          *db.TestContext
	cfg                 *so.Config
	handler             *SignTokenHandler
	fixtures            *entfixtures.Fixtures
	privKey             keys.Private
	pubKey              keys.Public
	mockOperatorPrivKey keys.Private
	mockOperatorPubKey  keys.Public
	coordinatorPrivKey  keys.Private
	coordinatorPubKey   keys.Public
	mockAddr            string
	mockServer          *mockSparkTokenInternalServiceServer
}

// setUpCommonTest sets up common test infrastructure
func setUpCommonTest(t *testing.T) *testSetupCommon {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	fixtures := entfixtures.New(t, ctx, sessionCtx.Client)

	privKey := cfg.IdentityPrivateKey
	pubKey := privKey.Public()

	mockOperatorPrivKey := keys.GeneratePrivateKey()
	mockOperatorPubKey := mockOperatorPrivKey.Public()

	coordinatorPrivKey := cfg.IdentityPrivateKey
	coordinatorPubKey := coordinatorPrivKey.Public()

	handler := NewSignTokenHandler(cfg)

	mockServer := &mockSparkTokenInternalServiceServer{
		privKey: mockOperatorPrivKey,
	}
	mockAddr := startMockGRPCServer(t, mockServer)

	cfg.SigningOperatorMap = make(map[string]*so.SigningOperator)
	cfg.Threshold = 2
	coordinatorIdentifier := so.IndexToIdentifier(0)
	cfg.SigningOperatorMap[coordinatorIdentifier] = &so.SigningOperator{
		Identifier:        coordinatorIdentifier,
		IdentityPublicKey: coordinatorPubKey,
	}
	mockOperatorIdentifier := so.IndexToIdentifier(1)
	cfg.SigningOperatorMap[mockOperatorIdentifier] = &so.SigningOperator{
		Identifier:                mockOperatorIdentifier,
		IdentityPublicKey:         mockOperatorPubKey,
		AddressRpc:                mockAddr,
		OperatorConnectionFactory: &sparktesting.DangerousTestOperatorConnectionFactoryNoTLS{},
	}

	return &testSetupCommon{
		ctx:                 ctx,
		sessionCtx:          sessionCtx,
		cfg:                 cfg,
		handler:             handler,
		fixtures:            fixtures,
		privKey:             privKey,
		pubKey:              pubKey,
		mockOperatorPrivKey: mockOperatorPrivKey,
		mockOperatorPubKey:  mockOperatorPubKey,
		coordinatorPrivKey:  coordinatorPrivKey,
		coordinatorPubKey:   coordinatorPubKey,
		mockAddr:            mockAddr,
		mockServer:          mockServer,
	}
}

func createCreateTokenTransactionProto(t *testing.T, setup *testSetupCommon) (*tokenpb.TokenTransaction, []byte, []byte, []byte) {
	creationEntityPrivKey := keys.GeneratePrivateKey()
	createInput := &tokenpb.TokenCreateInput{
		TokenName:               testTokenName,
		TokenTicker:             testTokenTicker,
		Decimals:                testTokenDecimals,
		MaxSupply:               testTokenMaxSupplyBytes,
		IsFreezable:             testTokenIsFreezable,
		IssuerPublicKey:         setup.pubKey.Serialize(),
		CreationEntityPublicKey: creationEntityPrivKey.Public().Serialize(),
	}

	metadata, err := common.NewTokenMetadataFromCreateInput(createInput, sparkpb.Network_REGTEST)
	require.NoError(t, err)
	tokenIdentifier, err := metadata.ComputeTokenIdentifier()
	require.NoError(t, err)

	expiryTime := time.Now().Add(10 * time.Minute)
	clientCreatedTimestamp := time.Now()

	tokenTxProto := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_CreateInput{
			CreateInput: createInput,
		},
		TokenOutputs:                    []*tokenpb.TokenOutput{},
		SparkOperatorIdentityPublicKeys: [][]byte{setup.coordinatorPubKey.Serialize(), setup.mockOperatorPubKey.Serialize()},
		Network:                         sparkpb.Network_REGTEST,
		ExpiryTime:                      timestamppb.New(expiryTime),
		ClientCreatedTimestamp:          timestamppb.New(clientCreatedTimestamp),
	}
	partialTxHash, err := utils.HashTokenTransaction(tokenTxProto, true)
	require.NoError(t, err)
	finalTxHash, err := utils.HashTokenTransaction(tokenTxProto, false)
	require.NoError(t, err)

	return tokenTxProto, partialTxHash, finalTxHash, tokenIdentifier
}

// setupDBCreateTokenTransactionInternalSignFailedScenario sets up the database entities for a create token transaction
func setupDBCreateTokenTransactionInternalSignFailedScenario(t *testing.T, setup *testSetupCommon, tokenTxProto *tokenpb.TokenTransaction, partialTxHash, finalTxHash, tokenIdentifier []byte) {
	coordinatorSignature := ecdsa.Sign(setup.coordinatorPrivKey.ToBTCEC(), finalTxHash)
	createInput, ok := tokenTxProto.TokenInputs.(*tokenpb.TokenTransaction_CreateInput)
	require.True(t, ok, "invalid tokenTxProto.TokenInputs: %v", tokenTxProto)
	creationEntityPubKey, err := keys.ParsePublicKey(createInput.CreateInput.CreationEntityPublicKey)
	require.NoError(t, err)
	tokenCreate, err := setup.sessionCtx.Client.TokenCreate.Create().
		SetTokenName(testTokenName).
		SetTokenTicker(testTokenTicker).
		SetDecimals(testTokenDecimals).
		SetMaxSupply(testTokenMaxSupplyBytes).
		SetIsFreezable(true).
		SetIssuerPublicKey(setup.coordinatorPubKey).
		SetCreationEntityPublicKey(creationEntityPubKey).
		SetNetwork(btcnetwork.Regtest).
		SetTokenIdentifier(tokenIdentifier).
		Save(setup.ctx)
	require.NoError(t, err)

	_, err = setup.sessionCtx.Client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(partialTxHash).
		SetFinalizedTokenTransactionHash(finalTxHash).
		SetStatus(schematype.TokenTransactionStatusSigned).
		SetCreateID(tokenCreate.ID).
		SetVersion(schematype.TokenTransactionVersionV1).
		SetClientCreatedTimestamp(tokenTxProto.ClientCreatedTimestamp.AsTime()).
		SetOperatorSignature(coordinatorSignature.Serialize()).
		SetExpiryTime(tokenTxProto.ExpiryTime.AsTime()).
		Save(setup.ctx)
	require.NoError(t, err)
}

// transferTestData contains data needed for transfer transaction tests
type transferTestData struct {
	tokenCreate      *ent.TokenCreate
	prevTxHash       []byte
	tokenOutputId1   string
	tokenOutputId2   string
	keyshare         *ent.SigningKeyshare
	prevTokenOutput1 *ent.TokenOutput
	prevTokenOutput2 *ent.TokenOutput
	prevTokenTx      *ent.TokenTransaction
}

// setUpTransferTestData creates the prerequisite data for transfer transaction tests
func setUpTransferTestData(t *testing.T, setup *testSetupCommon) *transferTestData {
	tokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)

	outputSpecs := entfixtures.OutputSpecsWithOwner(
		setup.coordinatorPubKey,
		big.NewInt(100),
		big.NewInt(100),
	)
	prevTokenTx, outputs := setup.fixtures.CreateMintTransaction(
		tokenCreate,
		outputSpecs,
		schematype.TokenTransactionStatusFinalized,
	)

	keyshare := setup.fixtures.CreateKeyshare()

	return &transferTestData{
		tokenCreate:      tokenCreate,
		prevTxHash:       prevTokenTx.FinalizedTokenTransactionHash,
		tokenOutputId1:   uuid.New().String(),
		tokenOutputId2:   uuid.New().String(),
		keyshare:         keyshare,
		prevTokenOutput1: outputs[0],
		prevTokenOutput2: outputs[1],
		prevTokenTx:      prevTokenTx,
	}
}

// createTransferTokenTransactionProto creates just the proto for a transfer token transaction
func createTransferTokenTransactionProto(t *testing.T, setup *testSetupCommon, transferData *transferTestData) (*tokenpb.TokenTransaction, []byte, []byte) {
	withdrawBondSats := transferData.prevTokenOutput1.WithdrawBondSats
	withdrawRelativeBlockLocktime := transferData.prevTokenOutput1.WithdrawRelativeBlockLocktime
	transferInput := &tokenpb.TokenTransferInput{
		OutputsToSpend: []*tokenpb.TokenOutputToSpend{
			{
				PrevTokenTransactionHash: transferData.prevTxHash,
				PrevTokenTransactionVout: 0,
			},
			{
				PrevTokenTransactionHash: transferData.prevTxHash,
				PrevTokenTransactionVout: 1,
			},
		},
	}

	expiryTime := time.Now().Add(10 * time.Minute)
	clientCreatedTimestamp := time.Now()

	tokenTxProto := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: transferInput,
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				Id:                            &transferData.tokenOutputId1,
				TokenIdentifier:               transferData.tokenCreate.TokenIdentifier,
				OwnerPublicKey:                setup.coordinatorPubKey.Serialize(),
				TokenAmount:                   padBytes(big.NewInt(50).Bytes(), 16),
				RevocationCommitment:          transferData.keyshare.PublicKey.Serialize(),
				WithdrawBondSats:              &withdrawBondSats,
				WithdrawRelativeBlockLocktime: &withdrawRelativeBlockLocktime,
			},
			{
				Id:                            &transferData.tokenOutputId2,
				TokenIdentifier:               transferData.tokenCreate.TokenIdentifier,
				OwnerPublicKey:                setup.coordinatorPubKey.Serialize(),
				TokenAmount:                   padBytes(big.NewInt(50).Bytes(), 16),
				RevocationCommitment:          transferData.keyshare.PublicKey.Serialize(),
				WithdrawBondSats:              &withdrawBondSats,
				WithdrawRelativeBlockLocktime: &withdrawRelativeBlockLocktime,
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{setup.coordinatorPubKey.Serialize(), setup.mockOperatorPubKey.Serialize()},
		Network:                         sparkpb.Network_REGTEST,
		ExpiryTime:                      timestamppb.New(expiryTime),
		ClientCreatedTimestamp:          timestamppb.New(clientCreatedTimestamp),
	}

	partialTxHash, err := utils.HashTokenTransaction(tokenTxProto, true)
	require.NoError(t, err)
	finalTxHash, err := utils.HashTokenTransaction(tokenTxProto, false)
	require.NoError(t, err)

	return tokenTxProto, partialTxHash, finalTxHash
}

// setupDBTransferTokenTransactionInternalSignFailedScenario sets up the database entities for a transfer token transaction
func setupDBTransferTokenTransactionInternalSignFailedScenario(t *testing.T, setup *testSetupCommon, transferData *transferTestData, tokenTxProto *tokenpb.TokenTransaction, partialTxHash, finalTxHash []byte) {
	coordinatorSignature := ecdsa.Sign(setup.coordinatorPrivKey.ToBTCEC(), finalTxHash)
	withdrawBondSats := transferData.prevTokenOutput1.WithdrawBondSats
	withdrawRelativeBlockLocktime := transferData.prevTokenOutput1.WithdrawRelativeBlockLocktime

	dbTx, err := setup.sessionCtx.Client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(partialTxHash).
		SetFinalizedTokenTransactionHash(finalTxHash).
		SetStatus(schematype.TokenTransactionStatusSigned).
		SetVersion(schematype.TokenTransactionVersionV1).
		SetClientCreatedTimestamp(tokenTxProto.ClientCreatedTimestamp.AsTime()).
		SetOperatorSignature(coordinatorSignature.Serialize()).
		SetExpiryTime(tokenTxProto.ExpiryTime.AsTime()).
		Save(setup.ctx)
	require.NoError(t, err)

	_, err = setup.sessionCtx.Client.TokenOutput.Create().
		SetID(uuid.MustParse(transferData.tokenOutputId1)).
		SetOwnerPublicKey(setup.coordinatorPubKey).
		SetTokenAmount(padBytes(big.NewInt(50).Bytes(), 16)).
		SetStatus(schematype.TokenOutputStatusCreatedSigned).
		SetCreatedTransactionOutputVout(0).
		SetWithdrawRevocationCommitment(transferData.keyshare.PublicKey.Serialize()).
		SetWithdrawBondSats(withdrawBondSats).
		SetWithdrawRelativeBlockLocktime(withdrawRelativeBlockLocktime).
		SetRevocationKeyshare(transferData.keyshare).
		SetTokenIdentifier(transferData.tokenCreate.TokenIdentifier).
		SetTokenCreateID(transferData.tokenCreate.ID).
		SetNetwork(transferData.tokenCreate.Network).
		SetOutputCreatedTokenTransaction(dbTx).
		SetCreatedTransactionFinalizedHash(finalTxHash).
		Save(setup.ctx)
	require.NoError(t, err)

	_, err = setup.sessionCtx.Client.TokenOutput.Create().
		SetID(uuid.MustParse(transferData.tokenOutputId2)).
		SetOwnerPublicKey(setup.coordinatorPubKey).
		SetTokenAmount(padBytes(big.NewInt(50).Bytes(), 16)).
		SetStatus(schematype.TokenOutputStatusCreatedSigned).
		SetCreatedTransactionOutputVout(1).
		SetWithdrawRevocationCommitment(transferData.keyshare.PublicKey.Serialize()).
		SetWithdrawBondSats(withdrawBondSats).
		SetWithdrawRelativeBlockLocktime(withdrawRelativeBlockLocktime).
		SetRevocationKeyshare(transferData.keyshare).
		SetTokenIdentifier(transferData.tokenCreate.TokenIdentifier).
		SetTokenCreateID(transferData.tokenCreate.ID).
		SetNetwork(transferData.tokenCreate.Network).
		SetOutputCreatedTokenTransaction(dbTx).
		SetCreatedTransactionFinalizedHash(finalTxHash).
		Save(setup.ctx)
	require.NoError(t, err)

	_, err = transferData.prevTokenOutput1.Update().
		SetOutputSpentTokenTransaction(dbTx).
		SetStatus(schematype.TokenOutputStatusSpentSigned).
		SetSpentTransactionInputVout(0).
		Save(setup.ctx)
	require.NoError(t, err)
	_, err = transferData.prevTokenOutput2.Update().
		SetOutputSpentTokenTransaction(dbTx).
		SetStatus(schematype.TokenOutputStatusSpentSigned).
		SetSpentTransactionInputVout(1).
		Save(setup.ctx)
	require.NoError(t, err)
}

// createInputTtxoSignatures creates the input TTXO signatures for a commit transaction request
func createInputTtxoSignatures(t *testing.T, setup *testSetupCommon, finalTxHash []byte, inputCount int) []*tokenpb.InputTtxoSignaturesPerOperator {
	createSignatureForOperator := func(operatorPubKey keys.Public, _ uint32) []byte {
		payloadHash, err := utils.HashOperatorSpecificPayload(finalTxHash, operatorPubKey)
		require.NoError(t, err)
		return ecdsa.Sign(setup.privKey.ToBTCEC(), payloadHash).Serialize()
	}
	coordinatorSigs := make([]*tokenpb.SignatureWithIndex, inputCount)
	for i := range coordinatorSigs {
		coordinatorSigs[i] = &tokenpb.SignatureWithIndex{
			Signature:  createSignatureForOperator(setup.coordinatorPubKey, uint32(i)),
			InputIndex: uint32(i),
		}
	}
	mockOperatorSigs := make([]*tokenpb.SignatureWithIndex, inputCount)
	for i := range mockOperatorSigs {
		mockOperatorSigs[i] = &tokenpb.SignatureWithIndex{
			Signature:  createSignatureForOperator(setup.mockOperatorPubKey, uint32(i)),
			InputIndex: uint32(i),
		}
	}

	return []*tokenpb.InputTtxoSignaturesPerOperator{
		{
			TtxoSignatures:            coordinatorSigs,
			OperatorIdentityPublicKey: setup.coordinatorPubKey.Serialize(),
		},
		{
			TtxoSignatures:            mockOperatorSigs,
			OperatorIdentityPublicKey: setup.mockOperatorPubKey.Serialize(),
		},
	}
}

func TestCommitTransaction_CreateTransaction_Retry_AfterInternalSignFailed(t *testing.T) {
	setup := setUpCommonTest(t)
	tokenTxProto, partialTxHash, finalTxHash, tokenIdentifier := createCreateTokenTransactionProto(t, setup)
	setupDBCreateTokenTransactionInternalSignFailedScenario(t, setup, tokenTxProto, partialTxHash, finalTxHash, tokenIdentifier)
	req := &tokenpb.CommitTransactionRequest{
		FinalTokenTransaction:          tokenTxProto,
		FinalTokenTransactionHash:      finalTxHash,
		OwnerIdentityPublicKey:         setup.pubKey.Serialize(),
		InputTtxoSignaturesPerOperator: createInputTtxoSignatures(t, setup, finalTxHash, 1),
	}

	resp, err := setup.handler.CommitTransaction(setup.ctx, req)
	require.NoError(t, err)

	// Assert that the sign step went through and returned a finalized status.
	require.NotNil(t, resp)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.CommitStatus)

	// Verify the status in the DB is in "Signed".
	queriedDbTx, err := setup.sessionCtx.Client.TokenTransaction.Query().Only(setup.ctx)
	require.NoError(t, err)
	assert.Equal(t, schematype.TokenTransactionStatusSigned, queriedDbTx.Status)
}

func TestCommitTransaction_TransferTransaction_Retry_AfterInternalSignFailed(t *testing.T) {
	setup := setUpCommonTest(t)
	transferData := setUpTransferTestData(t, setup)
	tokenTxProto, partialTxHash, finalTxHash := createTransferTokenTransactionProto(t, setup, transferData)
	setupDBTransferTokenTransactionInternalSignFailedScenario(t, setup, transferData, tokenTxProto, partialTxHash, finalTxHash)

	req := &tokenpb.CommitTransactionRequest{
		FinalTokenTransaction:          tokenTxProto,
		FinalTokenTransactionHash:      finalTxHash,
		OwnerIdentityPublicKey:         setup.pubKey.Serialize(),
		InputTtxoSignaturesPerOperator: createInputTtxoSignatures(t, setup, finalTxHash, 2),
	}

	resp, err := setup.handler.CommitTransaction(setup.ctx, req)
	require.NoError(t, err)

	// Assert that the sign step went through and returned a processing status.
	require.NotNil(t, resp)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp.CommitStatus)

	// Verify the status in the DB is "Revealed".
	queriedDbTx, err := setup.sessionCtx.Client.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHash(finalTxHash)).
		Only(setup.ctx)
	require.NoError(t, err)
	assert.Equal(t, schematype.TokenTransactionStatusRevealed, queriedDbTx.Status)
}

func TestCommitTransaction_TransferTransaction_Retry_AfterInternalFinalizeFailed(t *testing.T) {
	setup := setUpCommonTest(t)
	transferData := setUpTransferTestData(t, setup)
	tokenTxProto, partialTxHash, finalTxHash := createTransferTokenTransactionProto(t, setup, transferData)
	setupDBTransferTokenTransactionInternalSignFailedScenario(t, setup, transferData, tokenTxProto, partialTxHash, finalTxHash)

	req := &tokenpb.CommitTransactionRequest{
		FinalTokenTransaction:          tokenTxProto,
		FinalTokenTransactionHash:      finalTxHash,
		OwnerIdentityPublicKey:         setup.pubKey.Serialize(),
		InputTtxoSignaturesPerOperator: createInputTtxoSignatures(t, setup, finalTxHash, 2),
	}

	// First call to CommitTransaction. This completes the 'signing' phase and initiates the 'reveal' logic,
	// for which it won't receive a keyshare from the non-coordinator operator.
	resp, err := setup.handler.CommitTransaction(setup.ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp.CommitStatus)
	require.NotNil(t, resp.CommitProgress)
	assert.Equal(t, setup.coordinatorPubKey.Serialize(), resp.CommitProgress.CommittedOperatorPublicKeys[0])
	assert.Equal(t, setup.mockOperatorPubKey.Serialize(), resp.CommitProgress.UncommittedOperatorPublicKeys[0])

	// Call CommitTransaction again to test retrying in the reveal state - should return early with COMMIT_PROCESSING
	resp2, err := setup.handler.CommitTransaction(setup.ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp2)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp2.CommitStatus)

	// Validate CommitProgress shows correct operator statuses
	require.NotNil(t, resp2.CommitProgress)
	// Verify coordinator is in committed operators list
	require.Len(t, resp2.CommitProgress.CommittedOperatorPublicKeys, 1)
	assert.Equal(t, setup.coordinatorPubKey.Serialize(), resp2.CommitProgress.CommittedOperatorPublicKeys[0])
	// Verify mock operator is in uncommitted operators list
	require.Len(t, resp2.CommitProgress.UncommittedOperatorPublicKeys, 1)
	assert.Equal(t, setup.mockOperatorPubKey.Serialize(), resp2.CommitProgress.UncommittedOperatorPublicKeys[0])
	// Verify the status in the DB is "Revealed".
	queriedDbTx, err := setup.sessionCtx.Client.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHash(finalTxHash)).
		Only(setup.ctx)
	require.NoError(t, err)
	assert.Equal(t, schematype.TokenTransactionStatusRevealed, queriedDbTx.Status)
}

func TestCommitTransaction_TransferTransactionSimulateRace_ControlSucceedsWithValidInputs(t *testing.T) {
	setup := setUpCommonTest(t)
	transferData := setUpTransferTestData(t, setup)
	tokenTxProto, _, finalTxHash := createTransferTokenTransactionProto(t, setup, transferData)
	setupDBTransferTokenTransactionInternalSignFailedScenario(t, setup, transferData, tokenTxProto, finalTxHash, finalTxHash)

	hit := make(chan struct{})
	block := make(chan struct{})
	setup.mockServer.hitSign = hit
	setup.mockServer.blockSign = block

	go func() {
		<-hit
		close(block) // no mutation; allow response
	}()

	req := &tokenpb.CommitTransactionRequest{
		FinalTokenTransaction:          tokenTxProto,
		FinalTokenTransactionHash:      finalTxHash,
		OwnerIdentityPublicKey:         setup.pubKey.Serialize(),
		InputTtxoSignaturesPerOperator: createInputTtxoSignatures(t, setup, finalTxHash, 2),
	}

	resp, err := setup.handler.CommitTransaction(setup.ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	// For transfer, we expect processing state in this test harness
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp.CommitStatus)
}

func TestCommitTransaction_TransferTransactionSimulateRace_TestFailsWhenInputStatusFinalized(t *testing.T) {
	setup := setUpCommonTest(t)
	transferData := setUpTransferTestData(t, setup)
	tokenTxProto, _, finalTxHash := createTransferTokenTransactionProto(t, setup, transferData)
	setupDBTransferTokenTransactionInternalSignFailedScenario(t, setup, transferData, tokenTxProto, finalTxHash, finalTxHash)

	// Prepare blocking in mock to simulate race
	hit := make(chan struct{})
	block := make(chan struct{})
	setup.mockServer.hitSign = hit
	setup.mockServer.blockSign = block

	// Flip one spent input status to SPENT_FINALIZED to trigger validation failure
	go func() {
		<-hit // wait until RPC is in-flight to external operator
		_, err := transferData.prevTokenOutput1.Update().
			SetStatus(schematype.TokenOutputStatusSpentFinalized).
			Save(setup.ctx)
		if err != nil {
			t.Error("unexpected error: %w", err) // Can't use require.NoError or t.Fatal from another goroutine.
			return
		}
		close(block) // allow mock to respond
	}()

	req := &tokenpb.CommitTransactionRequest{
		FinalTokenTransaction:          tokenTxProto,
		FinalTokenTransactionHash:      finalTxHash,
		OwnerIdentityPublicKey:         setup.pubKey.Serialize(),
		InputTtxoSignaturesPerOperator: createInputTtxoSignatures(t, setup, finalTxHash, 2),
	}

	_, commitErr := setup.handler.CommitTransaction(setup.ctx, req)
	require.ErrorContains(t, commitErr, othertokens.ErrInvalidInputs)
}

func TestCommitTransaction_TransferTransactionSimulateRace_TestFailsWhenInputRemappedToDifferentTransaction(t *testing.T) {
	setup := setUpCommonTest(t)
	transferData := setUpTransferTestData(t, setup)
	tokenTxProto, _, finalTxHash := createTransferTokenTransactionProto(t, setup, transferData)
	setupDBTransferTokenTransactionInternalSignFailedScenario(t, setup, transferData, tokenTxProto, finalTxHash, finalTxHash)

	hit := make(chan struct{})
	block := make(chan struct{})
	setup.mockServer.hitSign = hit
	setup.mockServer.blockSign = block

	// Create a different transaction and remap one input's spent mapping to it
	go func() {
		<-hit
		otherHash := make([]byte, len(finalTxHash))
		copy(otherHash, finalTxHash)
		otherHash[0] ^= 0xFF // make it different
		otherTx, err := setup.sessionCtx.Client.TokenTransaction.Create().
			SetPartialTokenTransactionHash(otherHash).
			SetFinalizedTokenTransactionHash(otherHash).
			SetStatus(schematype.TokenTransactionStatusSigned).
			SetVersion(schematype.TokenTransactionVersionV1).
			SetClientCreatedTimestamp(time.Now()).
			SetOperatorSignature(setup.coordinatorPrivKey.Public().Serialize()).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			Save(setup.ctx)
		if err != nil {
			t.Error("unexpected error: %w", err) // We can't use require.NoError or t.Fail from a goroutine
			return
		}

		otherKeyshare := setup.fixtures.CreateKeyshare()

		_, err = setup.sessionCtx.Client.TokenOutput.Create().
			SetStatus(schematype.TokenOutputStatusCreatedStarted).
			SetOwnerPublicKey(setup.coordinatorPubKey).
			SetTokenAmount(transferData.prevTokenOutput1.TokenAmount).
			SetCreatedTransactionOutputVout(0).
			SetWithdrawRevocationCommitment(otherKeyshare.PublicKey.Serialize()).
			SetWithdrawBondSats(transferData.prevTokenOutput1.WithdrawBondSats).
			SetWithdrawRelativeBlockLocktime(transferData.prevTokenOutput1.WithdrawRelativeBlockLocktime).
			SetRevocationKeyshare(otherKeyshare).
			SetTokenIdentifier(transferData.tokenCreate.TokenIdentifier).
			SetTokenCreateID(transferData.tokenCreate.ID).
			SetNetwork(transferData.tokenCreate.Network).
			SetOutputCreatedTokenTransaction(otherTx).
			SetCreatedTransactionFinalizedHash(otherHash).
			Save(setup.ctx)
		if err != nil {
			t.Error("unexpected error: %w", err) // We can't use require.NoError or t.Fail from a goroutine
			return
		}
		_, err = transferData.prevTokenOutput1.Update().
			SetOutputSpentTokenTransaction(otherTx).
			SetStatus(schematype.TokenOutputStatusSpentStarted).
			Save(setup.ctx)
		if err != nil {
			t.Error("unexpected error: %w", err) // We can't use require.NoError or t.Fail from a goroutine
			return
		}

		_, err = otherTx.Update().
			SetStatus(schematype.TokenTransactionStatusRevealed).
			Save(setup.ctx)
		if err != nil {
			t.Error("unexpected error: %w", err) // We can't use require.NoError or t.Fail from a goroutine
			return
		}

		close(block)
	}()

	req := &tokenpb.CommitTransactionRequest{
		FinalTokenTransaction:          tokenTxProto,
		FinalTokenTransactionHash:      finalTxHash,
		OwnerIdentityPublicKey:         setup.pubKey.Serialize(),
		InputTtxoSignaturesPerOperator: createInputTtxoSignatures(t, setup, finalTxHash, 2),
	}

	_, commitErr := setup.handler.CommitTransaction(setup.ctx, req)
	require.ErrorContains(t, commitErr, "number of inputs in proto")
}
