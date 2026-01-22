package tokens

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

type internalBroadcastTokenPostgresTestSetup struct {
	handler  *InternalBroadcastTokenHandler
	config   *so.Config
	ctx      context.Context
	client   *ent.Client
	fixtures *entfixtures.Fixtures
}

func setUpInternalBroadcastTokenTestHandlerPostgres(t *testing.T) *internalBroadcastTokenPostgresTestSetup {
	t.Helper()

	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	return &internalBroadcastTokenPostgresTestSetup{
		handler:  NewInternalBroadcastTokenHandler(config),
		config:   config,
		ctx:      ctx,
		client:   dbClient,
		fixtures: entfixtures.New(t, ctx, dbClient),
	}
}

func phase2EnabledKnobs() knobs.Knobs {
	return knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenTransactionV3Phase2Enabled: 100,
	})
}

func phase2DisabledKnobs() knobs.Knobs {
	return knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenTransactionV3Phase2Enabled: 0,
	})
}

// broadcastTestData holds all the data needed to construct a valid broadcast request.
type broadcastTestData struct {
	TokenCreate       *ent.TokenCreate
	Keyshare          *ent.SigningKeyshare
	TxProto           *tokenpb.TokenTransaction
	Signature         []byte
	CoordinatorPubKey []byte
}

// buildValidBroadcastRequest constructs a valid BroadcastTransactionInternalRequest from test data.
func (d *broadcastTestData) buildValidBroadcastRequest() *tokeninternalpb.BroadcastTransactionInternalRequest {
	return &tokeninternalpb.BroadcastTransactionInternalRequest{
		FinalTokenTransaction:      d.TxProto,
		TokenTransactionSignatures: []*tokenpb.SignatureWithIndex{{InputIndex: 0, Signature: d.Signature}},
		KeyshareIds:                []string{d.Keyshare.ID.String()},
		CoordinatorPublicKey:       d.CoordinatorPubKey,
	}
}

// createBroadcastTestData creates all the entities and builds a valid V3 mint transaction for testing.
func createBroadcastTestData(t *testing.T, f *entfixtures.Fixtures, config *so.Config) *broadcastTestData {
	t.Helper()
	issuerPriv, tokenCreate := f.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)

	// Regular keyshare for output's RevocationCommitment (doesn't need entity DKG key).
	ks := f.CreateKeyshare()

	now := time.Now()
	validityDuration := uint64(300) // 5 minutes (max allowed)
	txProto := &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: issuerPriv.Public().Serialize(),
				TokenIdentifier: tokenCreate.TokenIdentifier,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				Id:                   proto.String(uuid.Must(uuid.NewV7()).String()),
				OwnerPublicKey:       issuerPriv.Public().Serialize(),
				TokenIdentifier:      tokenCreate.TokenIdentifier,
				TokenAmount:          []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10},
				RevocationCommitment: ks.PublicKey.Serialize(),
			},
		},
		ExpiryTime:              timestamppb.New(now.Add(24 * time.Hour)),
		ClientCreatedTimestamp:  timestamppb.New(now),
		ValidityDurationSeconds: &validityDuration,
		Network:                 sparkpb.Network_REGTEST,
	}

	// Add operator public keys (must be sorted bytewise ascending for V3).
	var opKeys [][]byte
	for _, op := range config.GetSigningOperatorList() {
		opKeys = append(opKeys, op.PublicKey)
	}
	// Sort bytewise ascending.
	for i := 0; i < len(opKeys); i++ {
		for j := i + 1; j < len(opKeys); j++ {
			if bytes.Compare(opKeys[i], opKeys[j]) > 0 {
				opKeys[i], opKeys[j] = opKeys[j], opKeys[i]
			}
		}
	}
	txProto.SparkOperatorIdentityPublicKeys = opKeys

	// Set withdraw bond and locktime from config.
	cfgVals := config.Lrc20Configs[strings.ToLower(btcnetwork.Regtest.String())]
	txProto.TokenOutputs[0].WithdrawBondSats = &cfgVals.WithdrawBondSats
	txProto.TokenOutputs[0].WithdrawRelativeBlockLocktime = &cfgVals.WithdrawRelativeBlockLocktime

	// Sign the transaction (issuer signature over partial hash).
	partialHash, err := utils.HashTokenTransaction(txProto, true)
	require.NoError(t, err)
	schnorrSig, err := schnorr.Sign(issuerPriv.ToBTCEC(), partialHash)
	require.NoError(t, err)

	// Get coordinator public key.
	operatorList := config.GetSigningOperatorList()
	var firstOperator *sparkpb.SigningOperatorInfo
	for _, operator := range operatorList {
		firstOperator = operator
		break
	}

	return &broadcastTestData{
		TokenCreate:       tokenCreate,
		Keyshare:          ks,
		TxProto:           txProto,
		Signature:         schnorrSig.Serialize(),
		CoordinatorPubKey: firstOperator.PublicKey,
	}
}

func TestBroadcastTokenTransactionInternal_Phase2Disabled(t *testing.T) {
	setup := setUpInternalBroadcastTokenTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, phase2DisabledKnobs())

	testData := createBroadcastTestData(t, setup.fixtures, setup.config)
	req := testData.buildValidBroadcastRequest()

	resp, err := setup.handler.BroadcastTokenTransactionInternal(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "broadcastTokenTransactionInternal flow is not enabled")
}

func TestBroadcastTokenTransactionInternal_MissingFinalTransaction(t *testing.T) {
	setup := setUpInternalBroadcastTokenTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, phase2EnabledKnobs())

	req := &tokeninternalpb.BroadcastTransactionInternalRequest{
		FinalTokenTransaction: nil,
	}

	resp, err := setup.handler.BroadcastTokenTransactionInternal(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "final token transaction is required")
}

func TestBroadcastTokenTransactionInternal_IdempotencyReturnsSigned(t *testing.T) {
	setup := setUpInternalBroadcastTokenTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, phase2EnabledKnobs())

	testData := createBroadcastTestData(t, setup.fixtures, setup.config)
	hash, err := utils.HashTokenTransaction(testData.TxProto, false)
	require.NoError(t, err)

	// Create an existing signed transaction in the database.
	operatorSig := ecdsa.Sign(setup.config.IdentityPrivateKey.ToBTCEC(), hash).Serialize()
	setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(hash).
		SetFinalizedTokenTransactionHash(hash).
		SetStatus(st.TokenTransactionStatusSigned).
		SetCreateID(testData.TokenCreate.ID).
		SetOperatorSignature(operatorSig).
		SaveX(ctx)

	req := testData.buildValidBroadcastRequest()

	resp, err := setup.handler.BroadcastTokenTransactionInternal(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, operatorSig, resp.SparkOperatorSignature)
}

func TestBroadcastTokenTransactionInternal_IdempotencyRejectsNonSigned(t *testing.T) {
	setup := setUpInternalBroadcastTokenTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, phase2EnabledKnobs())

	testData := createBroadcastTestData(t, setup.fixtures, setup.config)
	hash, err := utils.HashTokenTransaction(testData.TxProto, false)
	require.NoError(t, err)

	// Create an existing transaction that is NOT in signed state.
	setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(hash).
		SetFinalizedTokenTransactionHash(hash).
		SetStatus(st.TokenTransactionStatusFinalized).
		SetCreateID(testData.TokenCreate.ID).
		SaveX(ctx)

	req := testData.buildValidBroadcastRequest()

	resp, err := setup.handler.BroadcastTokenTransactionInternal(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "repeat sign attempt but the transaction is not in signed state")
}

func TestBroadcastTokenTransactionInternal_RejectsPreV3(t *testing.T) {
	setup := setUpInternalBroadcastTokenTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, phase2EnabledKnobs())

	// Build valid test data then modify version to v2.
	testData := createBroadcastTestData(t, setup.fixtures, setup.config)
	testData.TxProto.Version = 2

	req := testData.buildValidBroadcastRequest()

	resp, err := setup.handler.BroadcastTokenTransactionInternal(ctx, req)

	// V2 transactions are rejected (fails at hash validation since v2 has different format requirements).
	require.Error(t, err)
	require.Nil(t, resp)
}

func TestBroadcastTokenTransactionInternal_Success(t *testing.T) {
	setup := setUpInternalBroadcastTokenTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, phase2EnabledKnobs())

	testData := createBroadcastTestData(t, setup.fixtures, setup.config)
	req := testData.buildValidBroadcastRequest()

	resp, err := setup.handler.BroadcastTokenTransactionInternal(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.NotEmpty(t, resp.SparkOperatorSignature)
	hash, err := utils.HashTokenTransaction(testData.TxProto, false)
	require.NoError(t, err)
	sig, err := ecdsa.ParseDERSignature(resp.SparkOperatorSignature)
	require.NoError(t, err)
	assert.True(t, sig.Verify(hash, setup.config.IdentityPrivateKey.Public().ToBTCEC()))
}
