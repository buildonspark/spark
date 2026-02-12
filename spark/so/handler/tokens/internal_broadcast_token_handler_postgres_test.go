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
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

type signTokenTransactionPostgresTestSetup struct {
	handler  *SignTokenTransactionHandler
	config   *so.Config
	ctx      context.Context
	client   *ent.Client
	fixtures *entfixtures.Fixtures
}

func setUpSignTokenTransactionTestHandlerPostgres(t *testing.T) *signTokenTransactionPostgresTestSetup {
	t.Helper()

	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	return &signTokenTransactionPostgresTestSetup{
		handler:  NewSignTokenTransactionHandler(config),
		config:   config,
		ctx:      ctx,
		client:   dbClient,
		fixtures: entfixtures.New(t, ctx, dbClient),
	}
}

// signTokenTxTestData holds all the data needed to construct a valid sign request.
type signTokenTxTestData struct {
	TokenCreate       *ent.TokenCreate
	Keyshare          *ent.SigningKeyshare
	TxProto           *tokenpb.TokenTransaction
	Signature         []byte
	CoordinatorPubKey []byte
}

// buildValidSignRequest constructs a valid SignTokenTransactionRequest from test data.
func (d *signTokenTxTestData) buildValidSignRequest() *tokeninternalpb.SignTokenTransactionRequest {
	return &tokeninternalpb.SignTokenTransactionRequest{
		FinalTokenTransaction:      d.TxProto,
		TokenTransactionSignatures: []*tokenpb.SignatureWithIndex{{InputIndex: 0, Signature: d.Signature}},
		KeyshareIds:                []string{d.Keyshare.ID.String()},
		CoordinatorPublicKey:       d.CoordinatorPubKey,
	}
}

// createSignTokenTxTestData creates all the entities and builds a valid V3 mint transaction for testing.
func createSignTokenTxTestData(t *testing.T, f *entfixtures.Fixtures, config *so.Config) *signTokenTxTestData {
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
		ClientCreatedTimestamp:  timestamppb.New(utils.ToMicrosecondPrecision(now)),
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

	return &signTokenTxTestData{
		TokenCreate:       tokenCreate,
		Keyshare:          ks,
		TxProto:           txProto,
		Signature:         schnorrSig.Serialize(),
		CoordinatorPubKey: firstOperator.PublicKey,
	}
}

func TestSignTokenTransaction_MissingFinalTransaction(t *testing.T) {
	setup := setUpSignTokenTransactionTestHandlerPostgres(t)

	req := &tokeninternalpb.SignTokenTransactionRequest{
		FinalTokenTransaction: nil,
	}

	resp, err := setup.handler.SignTokenTransaction(setup.ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "final token transaction is required")
}

func TestSignTokenTransaction_IdempotencyReturnsSigned(t *testing.T) {
	setup := setUpSignTokenTransactionTestHandlerPostgres(t)

	testData := createSignTokenTxTestData(t, setup.fixtures, setup.config)
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
		SaveX(setup.ctx)

	req := testData.buildValidSignRequest()

	resp, err := setup.handler.SignTokenTransaction(setup.ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, operatorSig, resp.SparkOperatorSignature)
}

func TestSignTokenTransaction_IdempotencyRejectsNonSigned(t *testing.T) {
	setup := setUpSignTokenTransactionTestHandlerPostgres(t)

	testData := createSignTokenTxTestData(t, setup.fixtures, setup.config)
	hash, err := utils.HashTokenTransaction(testData.TxProto, false)
	require.NoError(t, err)

	// Create an existing transaction that is NOT in signed state.
	setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(hash).
		SetFinalizedTokenTransactionHash(hash).
		SetStatus(st.TokenTransactionStatusFinalized).
		SetCreateID(testData.TokenCreate.ID).
		SaveX(setup.ctx)

	req := testData.buildValidSignRequest()

	resp, err := setup.handler.SignTokenTransaction(setup.ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "repeat sign attempt but the transaction is not in signed state")
}

func TestSignTokenTransaction_RejectsPreV3(t *testing.T) {
	setup := setUpSignTokenTransactionTestHandlerPostgres(t)

	// Build valid test data then modify version to v2.
	testData := createSignTokenTxTestData(t, setup.fixtures, setup.config)
	testData.TxProto.Version = 2

	req := testData.buildValidSignRequest()

	resp, err := setup.handler.SignTokenTransaction(setup.ctx, req)

	// V2 transactions are rejected (fails at hash validation since v2 has different format requirements).
	require.Error(t, err)
	require.Nil(t, resp)
}

func TestSignTokenTransaction_Success(t *testing.T) {
	setup := setUpSignTokenTransactionTestHandlerPostgres(t)

	testData := createSignTokenTxTestData(t, setup.fixtures, setup.config)
	req := testData.buildValidSignRequest()

	resp, err := setup.handler.SignTokenTransaction(setup.ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.NotEmpty(t, resp.SparkOperatorSignature)
	hash, err := utils.HashTokenTransaction(testData.TxProto, false)
	require.NoError(t, err)
	sig, err := ecdsa.ParseDERSignature(resp.SparkOperatorSignature)
	require.NoError(t, err)
	assert.True(t, sig.Verify(hash, setup.config.IdentityPrivateKey.Public().ToBTCEC()))
}
