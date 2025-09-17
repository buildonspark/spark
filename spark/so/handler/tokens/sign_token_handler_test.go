package tokens

import (
	"context"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"encoding/binary"
	mathrand "math/rand/v2"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

const (
	testTokenName        = "test token"
	testTokenTicker      = "TTT"
	testTokenDecimals    = 8
	testTokenMaxSupply   = 1000
	testTokenAmount      = 100
	testTokenIsFreezable = true
	// LRC20 Regtest config values
	testWithdrawBondSats              = 10000 // From WithdrawalBondSatsInConfig
	testWithdrawRelativeBlockLocktime = 1000  // From WithdrawalRelativeBlockLocktimeInConfig
)

var (
	testTokenMaxSupplyBytes = padBytes(big.NewInt(testTokenMaxSupply).Bytes(), 16)
	testTokenAmountBytes    = padBytes(big.NewInt(testTokenAmount).Bytes(), 16)
)

// mockSparkTokenInternalServiceServer provides a mock implementation of the gRPC service
// for testing cross-operator communication in token transactions.
type mockSparkTokenInternalServiceServer struct {
	tokeninternalpb.UnimplementedSparkTokenInternalServiceServer
	privKey     keys.Private
	errToReturn error
}

func (s *mockSparkTokenInternalServiceServer) SignTokenTransactionFromCoordination(
	_ context.Context,
	req *tokeninternalpb.SignTokenTransactionFromCoordinationRequest,
) (*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, error) {
	if s.errToReturn != nil {
		return nil, s.errToReturn
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
	// Pick a free TCP port for the mock server
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

func createTestSigningKeyshare(_ *testing.T, ctx context.Context, rng io.Reader, client *ent.Client) *ent.SigningKeyshare {
	secret := keys.MustGeneratePrivateKeyFromRand(rng)
	return client.SigningKeyshare.Create().
		SetStatus(schematype.KeyshareStatusAvailable).
		SetSecretShare(secret.Serialize()).
		SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		SetPublicShares(map[string]keys.Public{"test": secret.Public()}).
		SaveX(ctx)
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
	privKey             keys.Private
	pubKey              keys.Public
	mockOperatorPrivKey keys.Private
	mockOperatorPubKey  keys.Public
	coordinatorPrivKey  keys.Private
	coordinatorPubKey   keys.Public
	mockAddr            string
}

// setUpCommonTest sets up common test infrastructure
func setUpCommonTest(t *testing.T) *testSetupCommon {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)

	// Use the coordinator's identity key from the test config
	privKey := cfg.IdentityPrivateKey
	pubKey := privKey.Public()

	mockOperatorPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	mockOperatorPubKey := mockOperatorPrivKey.Public()

	coordinatorPrivKey := cfg.IdentityPrivateKey
	coordinatorPubKey := coordinatorPrivKey.Public()

	handler := NewSignTokenHandler(cfg)

	// Set up mock gRPC server for inter-operator communication
	mockServer := &mockSparkTokenInternalServiceServer{
		privKey: mockOperatorPrivKey,
	}
	mockAddr := startMockGRPCServer(t, mockServer)

	// Update signing operator config to have just one mocked non-coordinator operator.
	cfg.SigningOperatorMap = make(map[string]*so.SigningOperator)
	cfg.Threshold = 2
	coordinatorIdentifier := utils.IndexToIdentifier(0)
	cfg.SigningOperatorMap[coordinatorIdentifier] = &so.SigningOperator{
		Identifier:        coordinatorIdentifier,
		IdentityPublicKey: coordinatorPubKey,
		// AddressRpc is not needed for self
	}
	mockOperatorIdentifier := utils.IndexToIdentifier(1)
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
		privKey:             privKey,
		pubKey:              pubKey,
		mockOperatorPrivKey: mockOperatorPrivKey,
		mockOperatorPubKey:  mockOperatorPubKey,
		coordinatorPrivKey:  coordinatorPrivKey,
		coordinatorPubKey:   coordinatorPubKey,
		mockAddr:            mockAddr,
	}
}

func createCreateTokenTransactionProto(t *testing.T, setup *testSetupCommon) (*tokenpb.TokenTransaction, []byte, []byte, []byte) {
	creationEntityPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
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
	tokenIdentifier, err := metadata.ComputeTokenIdentifierV1()
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
		SetNetwork(common.SchemaNetwork(common.Regtest)).
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
	tokenIdentifier  []byte
	prevTxHash       []byte
	tokenOutputId1   string
	tokenOutputId2   string
	keyshare         *ent.SigningKeyshare
	prevTokenOutput1 *ent.TokenOutput
	prevTokenOutput2 *ent.TokenOutput
	prevTokenTx      *ent.TokenTransaction
}

// setUpTransferTestData creates the prerequisite data for transfer transaction tests
func setUpTransferTestData(t *testing.T, rng io.Reader, setup *testSetupCommon) *transferTestData {
	// Create a token identifier for the transfer
	createInput := &tokenpb.TokenCreateInput{
		TokenName:               testTokenName,
		TokenTicker:             testTokenTicker,
		Decimals:                testTokenDecimals,
		MaxSupply:               testTokenMaxSupplyBytes,
		IsFreezable:             testTokenIsFreezable,
		IssuerPublicKey:         setup.pubKey.Serialize(),
		CreationEntityPublicKey: setup.coordinatorPubKey.Serialize(),
	}
	metadata, err := common.NewTokenMetadataFromCreateInput(createInput, sparkpb.Network_REGTEST)
	require.NoError(t, err)
	tokenIdentifier, err := metadata.ComputeTokenIdentifierV1()
	require.NoError(t, err)

	// Create some previous token outputs to spend
	prevTxHash := make([]byte, 0, 32)
	for range 4 {
		prevTxHash = binary.LittleEndian.AppendUint64(prevTxHash, mathrand.Uint64())
	}
	tokenOutputId1 := uuid.Must(uuid.NewRandomFromReader(rng)).String()
	tokenOutputId2 := uuid.Must(uuid.NewRandomFromReader(rng)).String()

	// Create keyshares for the token outputs (reuse for both out of convenience)
	keyshare := createTestSigningKeyshare(t, setup.ctx, rng, setup.sessionCtx.Client)

	// Create or fetch a TokenCreate for the token outputs
	tokenCreate, err := setup.sessionCtx.Client.TokenCreate.Query().
		Where(tokencreate.TokenIdentifier(tokenIdentifier)).
		Only(setup.ctx)
	if ent.IsNotFound(err) {
		tokenCreate, err = setup.sessionCtx.Client.TokenCreate.Create().
			SetIssuerPublicKey(setup.pubKey).
			SetTokenName(testTokenName).
			SetTokenTicker(testTokenTicker).
			SetDecimals(testTokenDecimals).
			SetMaxSupply(testTokenMaxSupplyBytes).
			SetIsFreezable(testTokenIsFreezable).
			SetCreationEntityPublicKey(setup.coordinatorPubKey).
			SetNetwork(common.SchemaNetwork(common.Regtest)).
			SetTokenIdentifier(tokenIdentifier).
			Save(setup.ctx)

	}
	require.NoError(t, err)

	// Create the previous token outputs that will be spent
	prevTokenOutput1, err := setup.sessionCtx.Client.TokenOutput.Create().
		SetID(uuid.New()).
		SetOwnerPublicKey(setup.coordinatorPubKey).
		SetTokenAmount(testTokenAmountBytes).
		SetStatus(schematype.TokenOutputStatusCreatedSigned).
		SetCreatedTransactionOutputVout(0).
		SetWithdrawRevocationCommitment(keyshare.PublicKey.Serialize()).
		SetWithdrawBondSats(testWithdrawBondSats).
		SetWithdrawRelativeBlockLocktime(testWithdrawRelativeBlockLocktime).
		SetRevocationKeyshare(keyshare).
		SetTokenIdentifier(tokenIdentifier).
		SetTokenCreateID(tokenCreate.ID).
		SetNetwork(common.SchemaNetwork(common.Regtest)).
		Save(setup.ctx)
	require.NoError(t, err)

	prevTokenOutput2, err := setup.sessionCtx.Client.TokenOutput.Create().
		SetID(uuid.New()).
		SetOwnerPublicKey(setup.coordinatorPubKey).
		SetTokenAmount(testTokenAmountBytes).
		SetStatus(schematype.TokenOutputStatusCreatedSigned).
		SetCreatedTransactionOutputVout(1).
		SetWithdrawRevocationCommitment(keyshare.PublicKey.Serialize()).
		SetWithdrawBondSats(testWithdrawBondSats).
		SetWithdrawRelativeBlockLocktime(testWithdrawRelativeBlockLocktime).
		SetRevocationKeyshare(keyshare).
		SetTokenIdentifier(tokenIdentifier).
		SetTokenCreateID(tokenCreate.ID).
		SetNetwork(common.SchemaNetwork(common.Regtest)).
		Save(setup.ctx)
	require.NoError(t, err)

	prevCoordinatorSignature := ecdsa.Sign(setup.coordinatorPrivKey.ToBTCEC(), prevTxHash)
	prevTokenTx, err := setup.sessionCtx.Client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(prevTxHash).
		SetFinalizedTokenTransactionHash(prevTxHash).
		SetStatus(schematype.TokenTransactionStatusFinalized).
		SetVersion(schematype.TokenTransactionVersionV1).
		SetClientCreatedTimestamp(time.Now().Add(-10 * time.Minute)).
		SetOperatorSignature(prevCoordinatorSignature.Serialize()).
		SetExpiryTime(time.Now().Add(10 * time.Minute)).
		Save(setup.ctx)
	require.NoError(t, err)
	_, err = prevTokenOutput1.Update().SetOutputCreatedTokenTransaction(prevTokenTx).Save(setup.ctx)
	require.NoError(t, err)
	_, err = prevTokenOutput2.Update().SetOutputCreatedTokenTransaction(prevTokenTx).Save(setup.ctx)
	require.NoError(t, err)

	return &transferTestData{
		tokenIdentifier:  tokenIdentifier,
		prevTxHash:       prevTxHash,
		tokenOutputId1:   tokenOutputId1,
		tokenOutputId2:   tokenOutputId2,
		keyshare:         keyshare,
		prevTokenOutput1: prevTokenOutput1,
		prevTokenOutput2: prevTokenOutput2,
		prevTokenTx:      prevTokenTx,
	}
}

// createTransferTokenTransactionProto creates just the proto for a transfer token transaction
func createTransferTokenTransactionProto(t *testing.T, setup *testSetupCommon, transferData *transferTestData) (*tokenpb.TokenTransaction, []byte, []byte) {
	// Create variables for values that need pointers
	withdrawBondSats := uint64(testWithdrawBondSats)
	withdrawRelativeBlockLocktime := uint64(testWithdrawRelativeBlockLocktime)
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

	// Create the token transaction proto first to compute the hash
	tokenTxProto := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: transferInput,
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				Id:                            &transferData.tokenOutputId1,
				TokenIdentifier:               transferData.tokenIdentifier,
				OwnerPublicKey:                setup.coordinatorPubKey.Serialize(),
				TokenAmount:                   padBytes(big.NewInt(50).Bytes(), 16), // Half of original amount
				RevocationCommitment:          transferData.keyshare.PublicKey.Serialize(),
				WithdrawBondSats:              &withdrawBondSats,
				WithdrawRelativeBlockLocktime: &withdrawRelativeBlockLocktime,
			},
			{
				Id:                            &transferData.tokenOutputId2,
				TokenIdentifier:               transferData.tokenIdentifier,
				OwnerPublicKey:                setup.coordinatorPubKey.Serialize(),
				TokenAmount:                   padBytes(big.NewInt(50).Bytes(), 16), // Other half
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

	// Compute the hash from the proto
	partialTxHash, err := utils.HashTokenTransaction(tokenTxProto, true)
	require.NoError(t, err)
	finalTxHash, err := utils.HashTokenTransaction(tokenTxProto, false)
	require.NoError(t, err)

	return tokenTxProto, partialTxHash, finalTxHash
}

// setupDBTransferTokenTransactionInternalSignFailedScenario sets up the database entities for a transfer token transaction
func setupDBTransferTokenTransactionInternalSignFailedScenario(t *testing.T, setup *testSetupCommon, transferData *transferTestData, tokenTxProto *tokenpb.TokenTransaction, partialTxHash, finalTxHash []byte) {
	coordinatorSignature := ecdsa.Sign(setup.coordinatorPrivKey.ToBTCEC(), finalTxHash)

	// Create or fetch TokenCreate for new outputs
	tokenCreate, err := setup.sessionCtx.Client.TokenCreate.Query().
		Where(tokencreate.TokenIdentifier(transferData.tokenIdentifier)).
		Only(setup.ctx)
	if ent.IsNotFound(err) {
		tokenCreate, err = setup.sessionCtx.Client.TokenCreate.Create().
			SetIssuerPublicKey(setup.pubKey).
			SetTokenName(testTokenName).
			SetTokenTicker(testTokenTicker).
			SetDecimals(testTokenDecimals).
			SetMaxSupply(testTokenMaxSupplyBytes).
			SetIsFreezable(testTokenIsFreezable).
			SetCreationEntityPublicKey(setup.coordinatorPubKey).
			SetNetwork(common.SchemaNetwork(common.Regtest)).
			SetTokenIdentifier(transferData.tokenIdentifier).
			Save(setup.ctx)
	}
	require.NoError(t, err)

	// Create the new token outputs for the transfer
	dbTokenOutput1, err := setup.sessionCtx.Client.TokenOutput.Create().
		SetID(uuid.MustParse(transferData.tokenOutputId1)).
		SetOwnerPublicKey(setup.coordinatorPubKey).
		SetTokenAmount(padBytes(big.NewInt(50).Bytes(), 16)).
		SetStatus(schematype.TokenOutputStatusCreatedSigned).
		SetCreatedTransactionOutputVout(0).
		SetWithdrawRevocationCommitment(transferData.keyshare.PublicKey.Serialize()).
		SetWithdrawBondSats(testWithdrawBondSats).
		SetWithdrawRelativeBlockLocktime(testWithdrawRelativeBlockLocktime).
		SetRevocationKeyshare(transferData.keyshare).
		SetTokenIdentifier(transferData.tokenIdentifier).
		SetTokenCreateID(tokenCreate.ID).
		SetNetwork(common.SchemaNetwork(common.Regtest)).
		Save(setup.ctx)
	require.NoError(t, err)

	dbTokenOutput2, err := setup.sessionCtx.Client.TokenOutput.Create().
		SetID(uuid.MustParse(transferData.tokenOutputId2)).
		SetOwnerPublicKey(setup.coordinatorPubKey).
		SetTokenAmount(padBytes(big.NewInt(50).Bytes(), 16)).
		SetStatus(schematype.TokenOutputStatusCreatedSigned).
		SetCreatedTransactionOutputVout(1).
		SetWithdrawRevocationCommitment(transferData.keyshare.PublicKey.Serialize()).
		SetWithdrawBondSats(testWithdrawBondSats).
		SetWithdrawRelativeBlockLocktime(testWithdrawRelativeBlockLocktime).
		SetRevocationKeyshare(transferData.keyshare).
		SetTokenIdentifier(transferData.tokenIdentifier).
		SetTokenCreateID(tokenCreate.ID).
		SetNetwork(common.SchemaNetwork(common.Regtest)).
		Save(setup.ctx)
	require.NoError(t, err)

	// Create the database transaction with the computed hash
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
	_, err = dbTokenOutput1.Update().SetOutputCreatedTokenTransaction(dbTx).Save(setup.ctx)
	require.NoError(t, err)
	_, err = dbTokenOutput2.Update().SetOutputCreatedTokenTransaction(dbTx).Save(setup.ctx)
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
	createSignatureForOperator := func(operatorPubKey []byte, _ uint32) []byte {
		payload := &sparkpb.OperatorSpecificTokenTransactionSignablePayload{
			FinalTokenTransactionHash: finalTxHash,
			OperatorIdentityPublicKey: operatorPubKey,
		}
		payloadHash, err := utils.HashOperatorSpecificTokenTransactionSignablePayload(payload)
		require.NoError(t, err)
		return ecdsa.Sign(setup.privKey.ToBTCEC(), payloadHash).Serialize()
	}
	coordinatorSigs := make([]*tokenpb.SignatureWithIndex, inputCount)
	for i := range coordinatorSigs {
		coordinatorSigs[i] = &tokenpb.SignatureWithIndex{
			Signature:  createSignatureForOperator(setup.coordinatorPubKey.Serialize(), uint32(i)),
			InputIndex: uint32(i),
		}
	}
	mockOperatorSigs := make([]*tokenpb.SignatureWithIndex, inputCount)
	for i := range mockOperatorSigs {
		mockOperatorSigs[i] = &tokenpb.SignatureWithIndex{
			Signature:  createSignatureForOperator(setup.mockOperatorPubKey.Serialize(), uint32(i)),
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
	rng := mathrand.NewChaCha8([32]byte{})
	transferData := setUpTransferTestData(t, rng, setup)
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
	rng := mathrand.NewChaCha8([32]byte{})
	transferData := setUpTransferTestData(t, rng, setup)
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
