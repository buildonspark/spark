package tokens

import (
	"bytes"
	"context"
	"math/big"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
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

type tokenInternalServerStream struct {
	method string
}

func (s tokenInternalServerStream) Method() string {
	return s.method
}

func (s tokenInternalServerStream) SetHeader(metadata.MD) error {
	return nil
}

func (s tokenInternalServerStream) SendHeader(metadata.MD) error {
	return nil
}

func (s tokenInternalServerStream) SetTrailer(metadata.MD) error {
	return nil
}

type validMintPrepareData struct {
	request  *tokeninternalpb.PrepareTransactionRequest
	keyshare *ent.SigningKeyshare
}

func testNonSelfCoordinatorPublicKey(t *testing.T, config *so.Config) []byte {
	t.Helper()
	return testNonSelfCoordinator(t, config).IdentityPublicKey.Serialize()
}

func testNonSelfCoordinator(t *testing.T, config *so.Config) *so.SigningOperator {
	t.Helper()

	var selected *so.SigningOperator
	for _, operator := range config.SigningOperatorMap {
		if operator.ID == config.Index {
			continue
		}
		if selected == nil || operator.ID < selected.ID {
			selected = operator
		}
	}
	if selected == nil {
		t.Fatalf("test config must include at least one non-self signing operator")
	}
	return selected
}

func testSortedOperatorPublicKeys(config *so.Config) [][]byte {
	operatorKeys := make([][]byte, 0, len(config.GetSigningOperatorList()))
	for _, operator := range config.GetSigningOperatorList() {
		operatorKeys = append(operatorKeys, operator.GetPublicKey())
	}
	slices.SortFunc(operatorKeys, bytes.Compare)
	return operatorKeys
}

func buildValidMintPrepareRequest(t *testing.T, rng *rand.ChaCha8, f *entfixtures.Fixtures, config *so.Config) validMintPrepareData {
	t.Helper()

	network := btcnetwork.Regtest
	issuerPriv, tokenCreate := f.CreateTokenCreateWithIssuer(network, f.RandomBytes(32), big.NewInt(1_000_000))
	keyshare := f.CreateKeyshare()
	coordinator := testNonSelfCoordinator(t, config)
	keyshare, err := f.Client.SigningKeyshare.UpdateOneID(keyshare.ID).
		SetCoordinatorIndex(coordinator.ID).
		Save(f.Ctx)
	require.NoError(t, err)
	now := time.Now()
	cfgVals := config.Lrc20Configs[strings.ToLower(network.String())]
	outputID := uuid.Must(uuid.NewV7()).String()

	txProto := &tokenpb.TokenTransaction{
		Version: 2,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: issuerPriv.Public().Serialize(),
				TokenIdentifier: tokenCreate.TokenIdentifier,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				Id:                            &outputID,
				OwnerPublicKey:                keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
				TokenIdentifier:               tokenCreate.TokenIdentifier,
				TokenAmount:                   []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10},
				RevocationCommitment:          keyshare.PublicKey.Serialize(),
				WithdrawBondSats:              &cfgVals.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: &cfgVals.WithdrawRelativeBlockLocktime,
			},
		},
		Network:                         sparkpb.Network_REGTEST,
		ExpiryTime:                      timestamppb.New(now.Add(24 * time.Hour)),
		ClientCreatedTimestamp:          timestamppb.New(now),
		SparkOperatorIdentityPublicKeys: testSortedOperatorPublicKeys(config),
	}

	partialHash, err := utils.HashTokenTransaction(txProto, true)
	require.NoError(t, err)
	schnorrSig, err := schnorr.Sign(issuerPriv.ToBTCEC(), partialHash)
	require.NoError(t, err)

	return validMintPrepareData{
		request: &tokeninternalpb.PrepareTransactionRequest{
			FinalTokenTransaction:      txProto,
			TokenTransactionSignatures: []*tokenpb.SignatureWithIndex{{InputIndex: 0, Signature: schnorrSig.Serialize()}},
			KeyshareIds:                []string{keyshare.ID.String()},
			CoordinatorPublicKey:       coordinator.IdentityPublicKey.Serialize(),
		},
		keyshare: keyshare,
	}
}

func withTokenInternalMethod(baseCtx context.Context, tb testing.TB, method string) context.Context {
	tb.Helper()
	return grpc.NewContextWithServerTransportStream(baseCtx, tokenInternalServerStream{method: method})
}

func TestPrepareTokenTransactionInternal_RejectsUnknownCoordinatorPublicKey(t *testing.T) {
	t.Parallel()
	rng := rand.NewChaCha8([32]byte{1})
	ctx, _ := db.ConnectToTestPostgres(t)
	dbtx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	fixtures := entfixtures.New(t, ctx, dbtx).WithRNG(rng)

	config := sparktesting.TestConfig(t)
	handler := NewInternalPrepareTokenHandler(config)
	data := buildValidMintPrepareRequest(t, rng, fixtures, config)
	data.request.CoordinatorPublicKey = keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()

	_, err = handler.PrepareTokenTransactionInternal(ctx, data.request)
	require.ErrorContains(t, err, "coordinator public key is not a configured signing operator")

	keyshare, err := dbtx.SigningKeyshare.Get(ctx, data.keyshare.ID)
	require.NoError(t, err)
	require.Equal(t, st.KeyshareStatusAvailable, keyshare.Status)
}

func TestPrepareTokenTransactionInternal_LocalCoordinatorRequiresReservedKeyshare(t *testing.T) {
	t.Parallel()
	rng := rand.NewChaCha8([32]byte{2})
	ctx, _ := db.ConnectToTestPostgres(t)
	dbtx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	fixtures := entfixtures.New(t, ctx, dbtx).WithRNG(rng)

	config := sparktesting.TestConfig(t)
	handler := NewInternalPrepareTokenHandler(config)
	data := buildValidMintPrepareRequest(t, rng, fixtures, config)
	data.request.CoordinatorPublicKey = config.IdentityPublicKey().Serialize()
	err = dbtx.SigningKeyshare.UpdateOneID(data.keyshare.ID).
		SetCoordinatorIndex(config.Index).
		Exec(ctx)
	require.NoError(t, err)

	_, err = handler.PrepareTokenTransactionInternal(ctx, data.request)
	require.ErrorContains(t, err, "local coordinator keyshare")

	keyshare, err := dbtx.SigningKeyshare.Get(ctx, data.keyshare.ID)
	require.NoError(t, err)
	require.Equal(t, st.KeyshareStatusAvailable, keyshare.Status)
}

func TestPrepareTokenTransactionInternal_RejectsWrongCoordinatorKeysharePool(t *testing.T) {
	t.Parallel()
	rng := rand.NewChaCha8([32]byte{5})
	ctx, _ := db.ConnectToTestPostgres(t)
	dbtx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	fixtures := entfixtures.New(t, ctx, dbtx).WithRNG(rng)

	config := sparktesting.TestConfig(t)
	handler := NewInternalPrepareTokenHandler(config)
	data := buildValidMintPrepareRequest(t, rng, fixtures, config)
	err = dbtx.SigningKeyshare.UpdateOneID(data.keyshare.ID).
		SetCoordinatorIndex(config.Index).
		Exec(ctx)
	require.NoError(t, err)

	_, err = handler.PrepareTokenTransactionInternal(ctx, data.request)
	require.ErrorContains(t, err, "coordinator index")

	keyshare, err := dbtx.SigningKeyshare.Get(ctx, data.keyshare.ID)
	require.NoError(t, err)
	require.Equal(t, st.KeyshareStatusAvailable, keyshare.Status)
}

func TestPrepareTokenTransactionInternal_LocalCoordinatorAcceptsReservedKeyshare(t *testing.T) {
	t.Parallel()
	rng := rand.NewChaCha8([32]byte{3})
	ctx, _ := db.ConnectToTestPostgres(t)
	dbtx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	fixtures := entfixtures.New(t, ctx, dbtx).WithRNG(rng)

	config := sparktesting.TestConfig(t)
	handler := NewInternalPrepareTokenHandler(config)
	data := buildValidMintPrepareRequest(t, rng, fixtures, config)
	data.request.CoordinatorPublicKey = config.IdentityPublicKey().Serialize()

	err = dbtx.SigningKeyshare.UpdateOneID(data.keyshare.ID).
		SetCoordinatorIndex(config.Index).
		SetStatus(st.KeyshareStatusInUse).
		Exec(ctx)
	require.NoError(t, err)

	_, err = handler.PrepareTokenTransactionInternal(ctx, data.request)
	require.NoError(t, err)
}

func TestPrepareTokenTransactionInternal_RejectsLocalCoordinatorOverInternalRPC(t *testing.T) {
	t.Parallel()
	rng := rand.NewChaCha8([32]byte{4})
	ctx, _ := db.ConnectToTestPostgres(t)
	dbtx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	fixtures := entfixtures.New(t, ctx, dbtx).WithRNG(rng)

	config := sparktesting.TestConfig(t)
	handler := NewInternalPrepareTokenHandler(config)
	data := buildValidMintPrepareRequest(t, rng, fixtures, config)
	data.request.CoordinatorPublicKey = config.IdentityPublicKey().Serialize()
	err = dbtx.SigningKeyshare.UpdateOneID(data.keyshare.ID).
		SetCoordinatorIndex(config.Index).
		SetStatus(st.KeyshareStatusInUse).
		Exec(ctx)
	require.NoError(t, err)

	internalCtx := withTokenInternalMethod(ctx, t, tokeninternalpb.SparkTokenInternalService_PrepareTransaction_FullMethodName)
	_, err = handler.PrepareTokenTransactionInternal(internalCtx, data.request)
	require.ErrorContains(t, err, "local coordinator public key cannot be used through token internal RPC")
}
