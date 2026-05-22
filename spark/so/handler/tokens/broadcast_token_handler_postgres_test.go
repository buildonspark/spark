package tokens

import (
	"bytes"
	"context"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/protohash"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/entfixtures"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/protoconverter"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

type broadcastTokenPostgresTestSetup struct {
	t        *testing.T
	handler  *BroadcastTokenHandler
	config   *so.Config
	ctx      context.Context
	client   *ent.Client
	root     *ent.Client
	fixtures *entfixtures.Fixtures
}

func (s *broadcastTokenPostgresTestSetup) sortedOperatorKeys() [][]byte {
	var opKeys [][]byte
	for _, op := range s.config.GetSigningOperatorList() {
		opKeys = append(opKeys, op.PublicKey)
	}
	for i := 0; i < len(opKeys); i++ {
		for j := i + 1; j < len(opKeys); j++ {
			if bytes.Compare(opKeys[i], opKeys[j]) > 0 {
				opKeys[i], opKeys[j] = opKeys[j], opKeys[i]
			}
		}
	}
	return opKeys
}

func (s *broadcastTokenPostgresTestSetup) defaultMetadata() *tokenpb.TokenTransactionMetadata {
	return &tokenpb.TokenTransactionMetadata{
		SparkOperatorIdentityPublicKeys: s.sortedOperatorKeys(),
		Network:                         sparkpb.Network_REGTEST,
		ClientCreatedTimestamp:          timestamppb.New(utils.ToMicrosecondPrecision(time.Now().UTC())),
		ValidityDurationSeconds:         300,
	}
}

func (s *broadcastTokenPostgresTestSetup) computeHashes(partial *tokenpb.PartialTokenTransaction) (partialHash, finalHash []byte) {
	partialLegacy, err := protoconverter.ConvertPartialToV2TxShape(partial)
	require.NoError(s.t, err)
	partialHash, err = utils.HashTokenTransaction(partialLegacy, true)
	require.NoError(s.t, err)
	finalHash, err = utils.HashTokenTransaction(partialLegacy, false)
	require.NoError(s.t, err)
	return partialHash, finalHash
}

func (s *broadcastTokenPostgresTestSetup) signAndBuildRequest(partial *tokenpb.PartialTokenTransaction, signerKey keys.Private) *tokenpb.BroadcastTransactionRequest {
	// Hash the partial directly (including execute_before when set),
	// matching what the real client does.
	partialHash, err := protohash.Hash(partial)
	require.NoError(s.t, err)
	sig, err := schnorr.Sign(signerKey.ToBTCEC(), partialHash)
	require.NoError(s.t, err)

	return &tokenpb.BroadcastTransactionRequest{
		PartialTokenTransaction: partial,
		TokenTransactionOwnerSignatures: []*tokenpb.SignatureWithIndex{
			{InputIndex: 0, Signature: sig.Serialize()},
		},
		IdentityPublicKey: signerKey.Public().Serialize(),
	}
}

func (s *broadcastTokenPostgresTestSetup) buildMintPartial(issuerKey keys.Private, tokenCreate *ent.TokenCreate) *tokenpb.PartialTokenTransaction {
	cfgVals := s.config.Lrc20Configs[strings.ToLower(btcnetwork.Regtest.String())]
	return &tokenpb.PartialTokenTransaction{
		Version:                  3,
		TokenTransactionMetadata: s.defaultMetadata(),
		TokenInputs: &tokenpb.PartialTokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: issuerKey.Public().Serialize(),
				TokenIdentifier: tokenCreate.TokenIdentifier,
			},
		},
		PartialTokenOutputs: []*tokenpb.PartialTokenOutput{
			{
				OwnerPublicKey:                issuerKey.Public().Serialize(),
				TokenIdentifier:               tokenCreate.TokenIdentifier,
				TokenAmount:                   []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10},
				WithdrawBondSats:              cfgVals.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: cfgVals.WithdrawRelativeBlockLocktime,
			},
		},
	}
}

func (s *broadcastTokenPostgresTestSetup) buildCreatePartial(issuerKey keys.Private) *tokenpb.PartialTokenTransaction {
	return &tokenpb.PartialTokenTransaction{
		Version:                  3,
		TokenTransactionMetadata: s.defaultMetadata(),
		TokenInputs: &tokenpb.PartialTokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey: issuerKey.Public().Serialize(),
				TokenName:       "Test Token",
				TokenTicker:     "TST",
				Decimals:        8,
				MaxSupply:       make([]byte, 16),
				IsFreezable:     false,
			},
		},
	}
}

func (s *broadcastTokenPostgresTestSetup) buildTransferPartial(ownerKey keys.Private, tokenCreate *ent.TokenCreate, inputTTXO *ent.TokenOutput) *tokenpb.PartialTokenTransaction {
	cfgVals := s.config.Lrc20Configs[strings.ToLower(btcnetwork.Regtest.String())]
	return &tokenpb.PartialTokenTransaction{
		Version:                  3,
		TokenTransactionMetadata: s.defaultMetadata(),
		TokenInputs: &tokenpb.PartialTokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: []*tokenpb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: inputTTXO.CreatedTransactionFinalizedHash,
						PrevTokenTransactionVout: uint32(inputTTXO.CreatedTransactionOutputVout),
					},
				},
			},
		},
		PartialTokenOutputs: []*tokenpb.PartialTokenOutput{
			{
				OwnerPublicKey:                ownerKey.Public().Serialize(),
				TokenIdentifier:               tokenCreate.TokenIdentifier,
				TokenAmount:                   inputTTXO.TokenAmount,
				WithdrawBondSats:              cfgVals.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: cfgVals.WithdrawRelativeBlockLocktime,
			},
		},
	}
}

func setUpBroadcastTokenTestHandlerPostgres(t *testing.T) *broadcastTokenPostgresTestSetup {
	t.Helper()

	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	return &broadcastTokenPostgresTestSetup{
		t:        t,
		handler:  NewBroadcastTokenHandler(config),
		config:   config,
		ctx:      ctx,
		client:   dbClient,
		root:     tc.Client,
		fixtures: entfixtures.New(t, ctx, dbClient),
	}
}

// mockBroadcastInternalServer mocks the SparkTokenInternalService for testing phase 2.
type mockBroadcastInternalServer struct {
	tokeninternalpb.UnimplementedSparkTokenInternalServiceServer
	privKey keys.Private
}

func (s *mockBroadcastInternalServer) SignTokenTransaction(
	_ context.Context,
	req *tokeninternalpb.SignTokenTransactionRequest,
) (*tokeninternalpb.SignTokenTransactionResponse, error) {
	finalTxHash, err := utils.HashTokenTransaction(req.FinalTokenTransaction, false)
	if err != nil {
		return nil, err
	}
	signature := ecdsa.Sign(s.privKey.ToBTCEC(), finalTxHash)
	return &tokeninternalpb.SignTokenTransactionResponse{
		SparkOperatorSignature: signature.Serialize(),
	}, nil
}

func (s *mockBroadcastInternalServer) ExchangeRevocationSecretsShares(
	_ context.Context,
	_ *tokeninternalpb.ExchangeRevocationSecretsSharesRequest,
) (*tokeninternalpb.ExchangeRevocationSecretsSharesResponse, error) {
	// Return empty response - the mock operator has no shares to contribute.
	return &tokeninternalpb.ExchangeRevocationSecretsSharesResponse{
		ReceivedOperatorShares: nil,
	}, nil
}

func startMockBroadcastGRPCServer(t *testing.T, mockServer *mockBroadcastInternalServer) string {
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

// setUpPhase2BroadcastTestHandlerPostgres creates a test setup with mock operators for phase 2.
func setUpPhase2BroadcastTestHandlerPostgres(t *testing.T) *broadcastTokenPostgresTestSetup {
	t.Helper()

	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Get the coordinator (this operator).
	coordinatorIdentifier := config.Identifier
	coordinatorPubKey := config.SigningOperatorMap[coordinatorIdentifier].IdentityPublicKey

	// Create a mock operator with a mock gRPC server.
	mockOperatorPrivKey := keys.GeneratePrivateKey()
	mockOperatorPubKey := mockOperatorPrivKey.Public()
	mockServer := &mockBroadcastInternalServer{privKey: mockOperatorPrivKey}
	mockAddr := startMockBroadcastGRPCServer(t, mockServer)

	mockOperatorIdentifier := so.IndexToIdentifier(1)

	// Rebuild the operator map with just coordinator + mock operator.
	config.SigningOperatorMap = map[string]*so.SigningOperator{
		coordinatorIdentifier: {
			Identifier:        coordinatorIdentifier,
			IdentityPublicKey: coordinatorPubKey,
		},
		mockOperatorIdentifier: {
			Identifier:                mockOperatorIdentifier,
			IdentityPublicKey:         mockOperatorPubKey,
			AddressRpc:                mockAddr,
			OperatorConnectionFactory: &sparktesting.DangerousTestOperatorConnectionFactoryNoTLS{},
		},
	}
	config.Threshold = 2
	config.Lrc20Configs = map[string]so.Lrc20Config{
		strings.ToLower(btcnetwork.Regtest.String()): {
			WithdrawBondSats:              1000,
			WithdrawRelativeBlockLocktime: 100,
		},
	}

	return &broadcastTokenPostgresTestSetup{
		t:        t,
		handler:  NewBroadcastTokenHandler(config),
		config:   config,
		ctx:      ctx,
		client:   dbClient,
		root:     tc.Client,
		fixtures: entfixtures.New(t, ctx, dbClient),
	}
}

func v3EnabledKnobs() knobs.Knobs {
	return knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenTransactionV3Enabled:       100,
		knobs.KnobTokenTransactionV3Phase2Enabled: 0,
	})
}

func v3DisabledKnobs() knobs.Knobs {
	return knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenTransactionV3Enabled:       0,
		knobs.KnobTokenTransactionV3Phase2Enabled: 0,
	})
}

func v3Phase2EnabledKnobs() knobs.Knobs {
	return knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenTransactionV3Enabled:       100,
		knobs.KnobTokenTransactionV3Phase2Enabled: 100,
	})
}

// preInsertMintTransactionWithHashes creates a mint transaction with the given hashes for testing
// duplicate detection via the public BroadcastTokenTransaction API.
func preInsertMintTransactionWithHashes(
	t *testing.T,
	setup *broadcastTokenPostgresTestSetup,
	tokenCreate *ent.TokenCreate,
	issuerPubKey keys.Public,
	partialHash, finalHash []byte,
	status st.TokenTransactionStatus,
	expiryTime time.Time,
) {
	t.Helper()
	preInsertMintTransactionWithHashesAndExecuteBefore(
		t,
		setup,
		tokenCreate,
		issuerPubKey,
		partialHash,
		finalHash,
		status,
		expiryTime,
		time.Time{},
	)
}

func preInsertMintTransactionWithHashesAndExecuteBefore(
	t *testing.T,
	setup *broadcastTokenPostgresTestSetup,
	tokenCreate *ent.TokenCreate,
	issuerPubKey keys.Public,
	partialHash, finalHash []byte,
	status st.TokenTransactionStatus,
	expiryTime time.Time,
	executeBefore time.Time,
) {
	t.Helper()

	mint, err := setup.client.TokenMint.Create().
		SetIssuerPublicKey(issuerPubKey).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetWalletProvidedTimestamp(uint64(time.Now().UnixMilli())).
		SetIssuerSignature(make([]byte, 64)).
		Save(setup.ctx)
	require.NoError(t, err)

	txBuilder := setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(partialHash).
		SetFinalizedTokenTransactionHash(finalHash).
		SetStatus(status).
		SetMint(mint).
		SetExpiryTime(expiryTime).
		SetCoordinatorPublicKey(setup.config.IdentityPublicKey()).
		SetClientCreatedTimestamp(setup.defaultMetadata().ClientCreatedTimestamp.AsTime()).
		SetVersion(st.TokenTransactionVersionV3).
		SetValidityDurationSeconds(300)
	if !executeBefore.IsZero() {
		txBuilder = txBuilder.SetExecuteBefore(executeBefore)
	}

	if status == st.TokenTransactionStatusSigned || status == st.TokenTransactionStatusFinalized {
		operatorSig := ecdsa.Sign(setup.config.IdentityPrivateKey.ToBTCEC(), finalHash).Serialize()
		txBuilder = txBuilder.SetOperatorSignature(operatorSig)
	}

	tx := txBuilder.SaveX(setup.ctx)
	setup.fixtures.CreateOutputForTransaction(tokenCreate, big.NewInt(10), tx, 0)
}

func preInsertCreateTransactionWithHashesAndExecuteBefore(
	t *testing.T,
	setup *broadcastTokenPostgresTestSetup,
	partial *tokenpb.PartialTokenTransaction,
	issuerPubKey keys.Public,
	partialHash, finalHash []byte,
	status st.TokenTransactionStatus,
	expiryTime time.Time,
	executeBefore time.Time,
) []byte {
	t.Helper()

	partialLegacy, err := protoconverter.ConvertPartialToV2TxShape(partial)
	require.NoError(t, err)
	createInput := partialLegacy.GetCreateInput()
	require.NotNil(t, createInput)
	tokenMetadata, err := common.NewTokenMetadataFromCreateInput(createInput, partialLegacy.Network)
	require.NoError(t, err)
	tokenIdentifier, err := tokenMetadata.ComputeTokenIdentifier()
	require.NoError(t, err)

	tokenCreateEnt, err := setup.client.TokenCreate.Create().
		SetIssuerPublicKey(issuerPubKey).
		SetIssuerSignature(make([]byte, 64)).
		SetTokenName(createInput.GetTokenName()).
		SetTokenTicker(createInput.GetTokenTicker()).
		SetDecimals(uint8(createInput.GetDecimals())).
		SetMaxSupply(createInput.GetMaxSupply()).
		SetIsFreezable(createInput.GetIsFreezable()).
		SetExtraMetadata(createInput.GetExtraMetadata()).
		SetCreationEntityPublicKey(issuerPubKey).
		SetNetwork(btcnetwork.Regtest).
		SetTokenIdentifier(tokenIdentifier).
		Save(setup.ctx)
	require.NoError(t, err)

	txBuilder := setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(partialHash).
		SetFinalizedTokenTransactionHash(finalHash).
		SetStatus(status).
		SetCreate(tokenCreateEnt).
		SetExpiryTime(expiryTime).
		SetCoordinatorPublicKey(setup.config.IdentityPublicKey()).
		SetClientCreatedTimestamp(setup.defaultMetadata().ClientCreatedTimestamp.AsTime()).
		SetVersion(st.TokenTransactionVersionV3).
		SetValidityDurationSeconds(300)
	if !executeBefore.IsZero() {
		txBuilder = txBuilder.SetExecuteBefore(executeBefore)
	}
	if status == st.TokenTransactionStatusSigned || status == st.TokenTransactionStatusFinalized {
		operatorSig := ecdsa.Sign(setup.config.IdentityPrivateKey.ToBTCEC(), finalHash).Serialize()
		txBuilder = txBuilder.SetOperatorSignature(operatorSig)
	}
	txBuilder.SaveX(setup.ctx)

	return tokenIdentifier
}

// preInsertTransferTransactionWithHashes creates a transfer transaction with the given hashes for testing
// duplicate detection via the public BroadcastTokenTransaction API.
func preInsertTransferTransactionWithHashes(
	t *testing.T,
	setup *broadcastTokenPostgresTestSetup,
	tokenCreate *ent.TokenCreate,
	spentOutput *ent.TokenOutput,
	partialHash, finalHash []byte,
	status st.TokenTransactionStatus,
	expiryTime time.Time,
) {
	t.Helper()
	preInsertTransferTransactionWithHashesAndExecuteBefore(
		t,
		setup,
		tokenCreate,
		spentOutput,
		partialHash,
		finalHash,
		status,
		expiryTime,
		time.Time{},
	)
}

func preInsertTransferTransactionWithHashesAndExecuteBefore(
	t *testing.T,
	setup *broadcastTokenPostgresTestSetup,
	tokenCreate *ent.TokenCreate,
	spentOutput *ent.TokenOutput,
	partialHash, finalHash []byte,
	status st.TokenTransactionStatus,
	expiryTime time.Time,
	executeBefore time.Time,
) {
	t.Helper()

	// Create transaction in SIGNED status first to avoid balance constraint violations during setup.
	// The constraint only fires for FINALIZED status.
	operatorSig := ecdsa.Sign(setup.config.IdentityPrivateKey.ToBTCEC(), finalHash).Serialize()
	tx := setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(partialHash).
		SetFinalizedTokenTransactionHash(finalHash).
		SetStatus(st.TokenTransactionStatusSigned).
		SetExpiryTime(expiryTime).
		SetCoordinatorPublicKey(setup.config.IdentityPublicKey()).
		SetClientCreatedTimestamp(setup.defaultMetadata().ClientCreatedTimestamp.AsTime()).
		SetVersion(st.TokenTransactionVersionV3).
		SetValidityDurationSeconds(300).
		SetOperatorSignature(operatorSig)
	if !executeBefore.IsZero() {
		tx = tx.SetExecuteBefore(executeBefore)
	}
	txEnt := tx.SaveX(setup.ctx)

	// Set up balanced inputs and outputs
	setup.fixtures.CreateOutputForTransaction(tokenCreate, big.NewInt(100), txEnt, 0)
	setup.client.TokenOutput.UpdateOne(spentOutput).
		SetOutputSpentTokenTransaction(txEnt).
		SaveX(setup.ctx)

	// Now update to target status if different
	if status != st.TokenTransactionStatusSigned {
		setup.client.TokenTransaction.UpdateOne(txEnt).
			SetStatus(status).
			SaveX(setup.ctx)
	}
}

func TestBroadcastTokenTransaction_V3Disabled(t *testing.T) {
	setup := setUpBroadcastTokenTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3DisabledKnobs())

	req := &tokenpb.BroadcastTransactionRequest{
		PartialTokenTransaction: &tokenpb.PartialTokenTransaction{
			Version: 3,
		},
	}

	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "BroadcastTokenTransaction is not enabled")
}

func TestBroadcastTokenTransaction_MissingPartialTransaction(t *testing.T) {
	setup := setUpBroadcastTokenTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3EnabledKnobs())

	req := &tokenpb.BroadcastTransactionRequest{
		PartialTokenTransaction: nil,
	}

	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "partial token transaction is required")
}

func TestBroadcastTokenTransaction_RejectsPreV3(t *testing.T) {
	setup := setUpBroadcastTokenTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3EnabledKnobs())

	req := &tokenpb.BroadcastTransactionRequest{
		PartialTokenTransaction: &tokenpb.PartialTokenTransaction{
			Version: 2,
		},
	}

	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "broadcast transaction requires version 3+")
}

func TestBroadcastTokenTransaction_RejectsExpiredExecuteBefore(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	issuerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	setup.fixtures.CreateKeyshare()

	partial := setup.buildMintPartial(issuerPriv, tokenCreate)
	// Set timestamps so execute_before is after client_created but has already passed
	// client_created: 10 seconds ago, execute_before: 1 second ago (passed but after client_created)
	now := time.Now().UTC()
	clientCreatedTs := utils.ToMicrosecondPrecision(now.Add(-10 * time.Second))
	expiredExecuteBefore := utils.ToMicrosecondPrecision(now.Add(-1 * time.Second))
	partial.TokenTransactionMetadata.ClientCreatedTimestamp = timestamppb.New(clientCreatedTs)
	partial.ExecuteBefore = timestamppb.New(expiredExecuteBefore)

	req := setup.signAndBuildRequest(partial, issuerPriv)

	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "has already passed")
}

func TestBroadcastTokenTransaction_ExecuteBeforeRelaxesCCT(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	issuerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	setup.fixtures.CreateKeyshare()

	partial := setup.buildMintPartial(issuerPriv, tokenCreate)
	// Set CCT to 1 hour ago — would normally fail tight freshness check.
	// But set execute_before to 1 hour from now, which should relax the validation.
	now := time.Now().UTC()
	oldCCT := utils.ToMicrosecondPrecision(now.Add(-1 * time.Hour))
	futureDeadline := utils.ToMicrosecondPrecision(now.Add(1 * time.Hour))
	partial.TokenTransactionMetadata.ClientCreatedTimestamp = timestamppb.New(oldCCT)
	partial.ExecuteBefore = timestamppb.New(futureDeadline)

	req := setup.signAndBuildRequest(partial, issuerPriv)
	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.CommitStatus)
}

func TestBroadcastTokenTransaction_OldCCTWithoutExecuteBeforeFails(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	issuerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	setup.fixtures.CreateKeyshare()

	partial := setup.buildMintPartial(issuerPriv, tokenCreate)
	// Set CCT to 1 hour ago with no execute_before — should fail the tight freshness check
	now := time.Now().UTC()
	oldCCT := utils.ToMicrosecondPrecision(now.Add(-1 * time.Hour))
	partial.TokenTransactionMetadata.ClientCreatedTimestamp = timestamppb.New(oldCCT)

	req := setup.signAndBuildRequest(partial, issuerPriv)
	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "client created timestamp too old")
}

func TestBroadcastTokenTransaction_Phase2_MintSuccess(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	issuerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	setup.fixtures.CreateKeyshare()

	partial := setup.buildMintPartial(issuerPriv, tokenCreate)
	req := setup.signAndBuildRequest(partial, issuerPriv)
	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.CommitStatus)
	assert.NotNil(t, resp.FinalTokenTransaction)
	assert.Nil(t, resp.TokenIdentifier, "MINT transactions should not return token identifier")
}

func TestBroadcastTokenTransaction_DuplicateFinalizedExecuteBeforeMintReturnsExisting(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	issuerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	setup.fixtures.CreateKeyshare()
	setup.fixtures.CreateKeyshare()

	recipientPriv := keys.GeneratePrivateKey()
	partial := setup.buildMintPartial(issuerPriv, tokenCreate)
	partial.TokenTransactionMetadata.ValidityDurationSeconds = 1
	partial.PartialTokenOutputs[0].OwnerPublicKey = recipientPriv.Public().Serialize()
	partial.ExecuteBefore = timestamppb.New(utils.ToMicrosecondPrecision(time.Now().UTC().Add(1 * time.Hour)))

	partialHash, finalHash := setup.computeHashes(partial)
	executeBefore := partial.GetExecuteBefore().AsTime()
	preInsertMintTransactionWithHashesAndExecuteBefore(
		t,
		setup,
		tokenCreate,
		issuerPriv.Public(),
		partialHash,
		finalHash,
		st.TokenTransactionStatusFinalized,
		time.Now().Add(-1*time.Hour),
		executeBefore,
	)

	req := setup.signAndBuildRequest(partial, issuerPriv)
	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)
	require.NoError(t, err)
	require.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.GetCommitStatus())

	txs, err := setup.client.TokenTransaction.Query().
		Where(tokentransaction.PartialTokenTransactionHash(partialHash)).
		WithCreatedOutput().
		WithMint().
		All(ctx)
	require.NoError(t, err)
	require.Len(t, txs, 1, "replaying a finalized execute_before mint must return the existing transaction, not mint again")
	require.Equal(t, finalHash, txs[0].FinalizedTokenTransactionHash)
	require.Len(t, txs[0].Edges.CreatedOutput, 1)
	require.NotNil(t, txs[0].Edges.Mint)
}

func TestBroadcastTokenTransaction_DuplicateFinalizedExecuteBeforeCreateReturnsExisting(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	issuerPriv := setup.fixtures.GeneratePrivateKey()
	partial := setup.buildCreatePartial(issuerPriv)
	partial.TokenTransactionMetadata.ValidityDurationSeconds = 1
	partial.ExecuteBefore = timestamppb.New(utils.ToMicrosecondPrecision(time.Now().UTC().Add(1 * time.Hour)))

	partialHash, finalHash := setup.computeHashes(partial)
	executeBefore := partial.GetExecuteBefore().AsTime()
	tokenIdentifier := preInsertCreateTransactionWithHashesAndExecuteBefore(
		t,
		setup,
		partial,
		issuerPriv.Public(),
		partialHash,
		finalHash,
		st.TokenTransactionStatusFinalized,
		time.Now().Add(-1*time.Hour),
		executeBefore,
	)

	req := setup.signAndBuildRequest(partial, issuerPriv)
	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)
	require.NoError(t, err)
	require.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.GetCommitStatus())

	txs, err := setup.client.TokenTransaction.Query().
		Where(tokentransaction.PartialTokenTransactionHash(partialHash)).
		WithCreate().
		All(ctx)
	require.NoError(t, err)
	require.Len(t, txs, 1, "replaying a finalized execute_before create must return the existing transaction, not create again")
	require.Equal(t, finalHash, txs[0].FinalizedTokenTransactionHash)
	require.NotNil(t, txs[0].Edges.Create)

	creates, err := setup.client.TokenCreate.Query().
		Where(tokencreate.TokenIdentifier(tokenIdentifier)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, creates, 1)
}

func TestBroadcastTokenTransaction_NonTerminalExecuteBeforeMintCreateDoNotResubmit(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, setup *broadcastTokenPostgresTestSetup, ctx context.Context)
	}{
		{
			name: "mint",
			run: func(t *testing.T, setup *broadcastTokenPostgresTestSetup, ctx context.Context) {
				issuerPriv, tokenCreateEnt := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
				setup.fixtures.CreateKeyshare()

				partial := setup.buildMintPartial(issuerPriv, tokenCreateEnt)
				partial.ExecuteBefore = timestamppb.New(utils.ToMicrosecondPrecision(time.Now().UTC().Add(1 * time.Hour)))
				partialHash, finalHash := setup.computeHashes(partial)
				preInsertMintTransactionWithHashesAndExecuteBefore(
					t,
					setup,
					tokenCreateEnt,
					issuerPriv.Public(),
					partialHash,
					finalHash,
					st.TokenTransactionStatusSigned,
					time.Now().Add(-1*time.Hour),
					partial.GetExecuteBefore().AsTime(),
				)

				req := setup.signAndBuildRequest(partial, issuerPriv)
				resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)
				require.Error(t, err)
				require.Nil(t, resp)
				_, reason := sparkerrors.CodeAndReasonFrom(err)
				require.Equal(t, sparkerrors.ReasonAlreadyExistsExpiredTransaction, reason)

				txs, err := setup.client.TokenTransaction.Query().
					Where(tokentransaction.PartialTokenTransactionHash(partialHash)).
					All(ctx)
				require.NoError(t, err)
				require.Len(t, txs, 1)
			},
		},
		{
			name: "create",
			run: func(t *testing.T, setup *broadcastTokenPostgresTestSetup, ctx context.Context) {
				issuerPriv := setup.fixtures.GeneratePrivateKey()
				partial := setup.buildCreatePartial(issuerPriv)
				partial.ExecuteBefore = timestamppb.New(utils.ToMicrosecondPrecision(time.Now().UTC().Add(1 * time.Hour)))
				partialHash, finalHash := setup.computeHashes(partial)
				tokenIdentifier := preInsertCreateTransactionWithHashesAndExecuteBefore(
					t,
					setup,
					partial,
					issuerPriv.Public(),
					partialHash,
					finalHash,
					st.TokenTransactionStatusSigned,
					time.Now().Add(-1*time.Hour),
					partial.GetExecuteBefore().AsTime(),
				)

				req := setup.signAndBuildRequest(partial, issuerPriv)
				resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)
				require.Error(t, err)
				require.Nil(t, resp)
				_, reason := sparkerrors.CodeAndReasonFrom(err)
				require.Equal(t, sparkerrors.ReasonAlreadyExistsExpiredTransaction, reason)

				txs, err := setup.client.TokenTransaction.Query().
					Where(tokentransaction.PartialTokenTransactionHash(partialHash)).
					All(ctx)
				require.NoError(t, err)
				require.Len(t, txs, 1)

				creates, err := setup.client.TokenCreate.Query().
					Where(tokencreate.TokenIdentifier(tokenIdentifier)).
					All(ctx)
				require.NoError(t, err)
				require.Len(t, creates, 1)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setup := setUpPhase2BroadcastTestHandlerPostgres(t)
			ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())
			tc.run(t, setup, ctx)
		})
	}
}

func TestBroadcastTokenTransaction_Phase2_CreateSuccess(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	setup.fixtures.CreateKeyshareWithEntityDkgKey()
	issuerPriv := setup.fixtures.GeneratePrivateKey()

	partial := setup.buildCreatePartial(issuerPriv)

	req := setup.signAndBuildRequest(partial, issuerPriv)
	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.CommitStatus)
	assert.NotNil(t, resp.FinalTokenTransaction)
	assert.NotEmpty(t, resp.TokenIdentifier)
}

func TestBroadcastTokenTransaction_Phase2_TransferSuccess(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	ownerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	_, outputs := setup.fixtures.CreateMintTransaction(
		tokenCreate,
		entfixtures.OutputSpecsWithOwner(ownerPriv.Public(), big.NewInt(100)),
		st.TokenTransactionStatusFinalized,
	)
	inputTTXO := outputs[0]
	setup.fixtures.CreateKeyshare()

	partial := setup.buildTransferPartial(ownerPriv, tokenCreate, inputTTXO)
	req := setup.signAndBuildRequest(partial, ownerPriv)
	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp.CommitStatus)
	assert.NotNil(t, resp.FinalTokenTransaction)
}

func TestBroadcastTokenTransaction_ExpiredExecuteBeforeTransferCanResubmit(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	ownerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	_, outputs := setup.fixtures.CreateMintTransaction(
		tokenCreate,
		entfixtures.OutputSpecsWithOwner(ownerPriv.Public(), big.NewInt(100)),
		st.TokenTransactionStatusFinalized,
	)
	inputTTXO := outputs[0]
	setup.fixtures.CreateKeyshare()

	partial := setup.buildTransferPartial(ownerPriv, tokenCreate, inputTTXO)
	partial.ExecuteBefore = timestamppb.New(utils.ToMicrosecondPrecision(time.Now().UTC().Add(1 * time.Hour)))
	partialHash, finalHash := setup.computeHashes(partial)
	preInsertTransferTransactionWithHashesAndExecuteBefore(
		t,
		setup,
		tokenCreate,
		inputTTXO,
		partialHash,
		finalHash,
		st.TokenTransactionStatusSigned,
		time.Now().Add(-1*time.Hour),
		partial.GetExecuteBefore().AsTime(),
	)

	req := setup.signAndBuildRequest(partial, ownerPriv)
	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp.CommitStatus)

	txs, err := setup.root.TokenTransaction.Query().
		Where(tokentransaction.PartialTokenTransactionHash(partialHash)).
		All(t.Context())
	require.NoError(t, err)
	require.Len(t, txs, 2, "expired non-terminal execute_before transfers should be allowed to create a fresh processing window")

	resp, err = setup.handler.BroadcastTokenTransaction(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp.CommitStatus)

	txs, err = setup.root.TokenTransaction.Query().
		Where(tokentransaction.PartialTokenTransactionHash(partialHash)).
		All(t.Context())
	require.NoError(t, err)
	require.Len(t, txs, 2, "a duplicate replay should return the active resubmission instead of failing on multiple partial rows")
}

func TestBroadcastTokenTransaction_DuplicateMintRequest(t *testing.T) {
	tests := []struct {
		name          string
		status        st.TokenTransactionStatus
		expired       bool
		wantErr       bool
		wantStatus    tokenpb.CommitStatus
		wantProgress  bool
		wantErrReason string
	}{
		{
			name:         "finalized transaction returns finalized status",
			status:       st.TokenTransactionStatusFinalized,
			expired:      true, // expiry doesn't matter for finalized
			wantErr:      false,
			wantStatus:   tokenpb.CommitStatus_COMMIT_FINALIZED,
			wantProgress: false,
		},
		{
			name:          "expired transaction returns error",
			status:        st.TokenTransactionStatusSigned,
			expired:       true,
			wantErr:       true,
			wantErrReason: sparkerrors.ReasonAlreadyExistsExpiredTransaction,
		},
		{
			name:         "processing transaction returns progress",
			status:       st.TokenTransactionStatusSigned,
			expired:      false,
			wantErr:      false,
			wantStatus:   tokenpb.CommitStatus_COMMIT_PROCESSING,
			wantProgress: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setup := setUpPhase2BroadcastTestHandlerPostgres(t)
			ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

			issuerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
			setup.fixtures.CreateKeyshare()

			partial := setup.buildMintPartial(issuerPriv, tokenCreate)
			partialHash, finalHash := setup.computeHashes(partial)

			expiryTime := time.Now().Add(1 * time.Hour)
			if tc.expired {
				expiryTime = time.Now().Add(-1 * time.Hour)
			}
			preInsertMintTransactionWithHashes(
				t, setup, tokenCreate, issuerPriv.Public(),
				partialHash, finalHash,
				tc.status, expiryTime,
			)

			req := setup.signAndBuildRequest(partial, issuerPriv)
			resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

			if tc.wantErr {
				require.Error(t, err)
				require.Nil(t, resp)
				_, reason := sparkerrors.CodeAndReasonFrom(err)
				assert.Equal(t, tc.wantErrReason, reason)
			} else {
				require.NoError(t, err)
				require.NotNil(t, resp)
				assert.Equal(t, tc.wantStatus, resp.CommitStatus)
				assert.NotNil(t, resp.FinalTokenTransaction)
				if tc.wantProgress {
					require.NotNil(t, resp.CommitProgress)
					assert.NotEmpty(t, resp.CommitProgress.CommittedOperatorPublicKeys)
				} else {
					assert.Nil(t, resp.CommitProgress)
				}
			}
		})
	}
}

func TestBroadcastTokenTransaction_TransferWithDuplicateOutputsToSpend(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	ownerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	_, outputs := setup.fixtures.CreateMintTransaction(
		tokenCreate,
		entfixtures.OutputSpecsWithOwner(ownerPriv.Public(), big.NewInt(100)),
		st.TokenTransactionStatusFinalized,
	)
	inputTTXO := outputs[0]
	setup.fixtures.CreateKeyshare()

	cfgVals := setup.config.Lrc20Configs[strings.ToLower(btcnetwork.Regtest.String())]
	partial := &tokenpb.PartialTokenTransaction{
		Version:                  3,
		TokenTransactionMetadata: setup.defaultMetadata(),
		TokenInputs: &tokenpb.PartialTokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: []*tokenpb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: inputTTXO.CreatedTransactionFinalizedHash,
						PrevTokenTransactionVout: uint32(inputTTXO.CreatedTransactionOutputVout),
					},
					{
						PrevTokenTransactionHash: inputTTXO.CreatedTransactionFinalizedHash,
						PrevTokenTransactionVout: uint32(inputTTXO.CreatedTransactionOutputVout),
					},
				},
			},
		},
		PartialTokenOutputs: []*tokenpb.PartialTokenOutput{
			{
				OwnerPublicKey:                ownerPriv.Public().Serialize(),
				TokenIdentifier:               tokenCreate.TokenIdentifier,
				TokenAmount:                   inputTTXO.TokenAmount,
				WithdrawBondSats:              cfgVals.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: cfgVals.WithdrawRelativeBlockLocktime,
			},
		},
	}

	req := setup.signAndBuildRequest(partial, ownerPriv)
	req.TokenTransactionOwnerSignatures = append(req.TokenTransactionOwnerSignatures, &tokenpb.SignatureWithIndex{
		InputIndex: 1,
		Signature:  req.TokenTransactionOwnerSignatures[0].Signature,
	})

	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "duplicate output")
}

func TestBroadcastTokenTransaction_DuplicateTransferRequest(t *testing.T) {
	tests := []struct {
		name          string
		status        st.TokenTransactionStatus
		expired       bool
		wantErr       bool
		wantStatus    tokenpb.CommitStatus
		wantProgress  bool
		wantErrReason string
	}{
		{
			name:         "finalized transaction returns finalized status",
			status:       st.TokenTransactionStatusFinalized,
			expired:      true, // expiry doesn't matter for finalized
			wantErr:      false,
			wantStatus:   tokenpb.CommitStatus_COMMIT_FINALIZED,
			wantProgress: false,
		},
		{
			name:          "expired transaction returns error",
			status:        st.TokenTransactionStatusSigned,
			expired:       true,
			wantErr:       true,
			wantErrReason: sparkerrors.ReasonAlreadyExistsExpiredTransaction,
		},
		{
			name:         "processing transaction returns reveal progress",
			status:       st.TokenTransactionStatusSigned,
			expired:      false,
			wantErr:      false,
			wantStatus:   tokenpb.CommitStatus_COMMIT_PROCESSING,
			wantProgress: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setup := setUpPhase2BroadcastTestHandlerPostgres(t)
			ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

			ownerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
			_, outputs := setup.fixtures.CreateMintTransaction(
				tokenCreate,
				entfixtures.OutputSpecsWithOwner(ownerPriv.Public(), big.NewInt(100)),
				st.TokenTransactionStatusFinalized,
			)
			inputTTXO := outputs[0]
			setup.fixtures.CreateKeyshare()

			partial := setup.buildTransferPartial(ownerPriv, tokenCreate, inputTTXO)
			partialHash, finalHash := setup.computeHashes(partial)

			expiryTime := time.Now().Add(1 * time.Hour)
			if tc.expired {
				expiryTime = time.Now().Add(-1 * time.Hour)
			}
			preInsertTransferTransactionWithHashes(
				t, setup, tokenCreate, inputTTXO,
				partialHash, finalHash,
				tc.status, expiryTime,
			)

			req := setup.signAndBuildRequest(partial, ownerPriv)
			resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

			if tc.wantErr {
				require.Error(t, err)
				require.Nil(t, resp)
				_, reason := sparkerrors.CodeAndReasonFrom(err)
				assert.Equal(t, tc.wantErrReason, reason)
			} else {
				require.NoError(t, err)
				require.NotNil(t, resp)
				assert.Equal(t, tc.wantStatus, resp.CommitStatus)
				assert.NotNil(t, resp.FinalTokenTransaction)
				if tc.wantProgress {
					require.NotNil(t, resp.CommitProgress)
					// Transfer uses reveal progress - only coordinator has keyshare
					assert.Len(t, resp.CommitProgress.CommittedOperatorPublicKeys, 1,
						"Only coordinator should be committed (has keyshare)")
					assert.NotEmpty(t, resp.CommitProgress.UncommittedOperatorPublicKeys,
						"Other operators should be uncommitted (no reveals)")
					assert.Equal(t, setup.config.IdentityPublicKey().Serialize(),
						resp.CommitProgress.CommittedOperatorPublicKeys[0])
				} else {
					assert.Nil(t, resp.CommitProgress)
				}
			}
		})
	}
}
