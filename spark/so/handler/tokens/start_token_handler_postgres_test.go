package tokens

import (
	"testing"
	"time"

	"bytes"
	"math/big"
	"slices"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/entexample"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func TestStartTokenTransaction_DuplicateV3StartedDifferentCoordinatorRejects(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Fixture setup
	f := entfixtures.New(t, ctx, dbClient)
	tokenCreate := f.CreateTokenCreate(btcnetwork.Regtest, nil, nil)

	issuerPubKey := tokenCreate.IssuerPublicKey
	// Truncate to microseconds to match PostgreSQL precision
	clientCreated := utils.ToMicrosecondPrecision(time.Now().UTC())
	validity := uint64(60)

	partial := buildV3CreateTransactionProto(
		t,
		cfg,
		tokenCreate,
		issuerPubKey,
		validity,
		clientCreated,
	)

	partialHash, err := utils.HashTokenTransaction(partial, true)
	require.NoError(t, err)

	ownerSigs := []*tokenpb.SignatureWithIndex{
		{Signature: []byte{1}, InputIndex: 0},
	}

	otherCoordinator := keys.GeneratePrivateKey().Public()
	entexample.NewTokenTransactionExample(t, dbClient).
		SetPartialTokenTransactionHash(partialHash).
		SetFinalizedTokenTransactionHash(partialHash).
		SetStatus(st.TokenTransactionStatusStarted).
		SetCoordinatorPublicKey(otherCoordinator).
		SetClientCreatedTimestamp(clientCreated).
		SetValidityDurationSeconds(validity).
		SetCreate(tokenCreate).
		MustExec(ctx)

	handler := NewStartTokenTransactionHandler(cfg)

	_, err = handler.StartTokenTransaction(ctx, &tokenpb.StartTransactionRequest{
		PartialTokenTransaction:                partial,
		PartialTokenTransactionOwnerSignatures: ownerSigs,
		IdentityPublicKey:                      cfg.IdentityPublicKey().Serialize(),
		ValidityDurationSeconds:                validity,
	})

	require.Error(t, err)
	sts, ok := status.FromError(err)
	require.True(t, ok)
	t.Logf("error: %v", sts.Message())
	require.Equal(t, codes.AlreadyExists, sts.Code())
}

func TestStartTokenTransaction_DuplicateV3SignedSameCoordinatorRejects(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	f := entfixtures.New(t, ctx, dbClient)
	tokenCreate := f.CreateTokenCreate(btcnetwork.Regtest, nil, nil)

	issuerPubKey := tokenCreate.IssuerPublicKey
	// Truncate to microseconds to match PostgreSQL precision
	clientCreated := utils.ToMicrosecondPrecision(time.Now().UTC())
	validity := uint64(60)

	partial := buildV3CreateTransactionProto(
		t,
		cfg,
		tokenCreate,
		issuerPubKey,
		validity,
		clientCreated,
	)

	partialHash, err := utils.HashTokenTransaction(partial, true)
	require.NoError(t, err)

	// Legitimate signature not needed because request will be rejected before signature validation.
	ownerSigs := []*tokenpb.SignatureWithIndex{
		{Signature: []byte{1}, InputIndex: 0},
	}

	entexample.NewTokenTransactionExample(t, dbClient).
		SetPartialTokenTransactionHash(partialHash).
		SetFinalizedTokenTransactionHash(partialHash).
		SetStatus(st.TokenTransactionStatusSigned).
		SetCoordinatorPublicKey(cfg.IdentityPublicKey()).
		SetClientCreatedTimestamp(clientCreated).
		SetValidityDurationSeconds(validity).
		SetCreate(tokenCreate).
		MustExec(ctx)

	handler := NewStartTokenTransactionHandler(cfg)

	_, err = handler.StartTokenTransaction(ctx, &tokenpb.StartTransactionRequest{
		PartialTokenTransaction:                partial,
		PartialTokenTransactionOwnerSignatures: ownerSigs,
		IdentityPublicKey:                      cfg.IdentityPublicKey().Serialize(),
		ValidityDurationSeconds:                validity,
	})

	require.Error(t, err)
	sts, ok := status.FromError(err)
	require.True(t, ok)
	t.Logf("error: %v", sts.Message())
	require.Equal(t, codes.AlreadyExists, sts.Code())
}

// TestStartTokenTransaction_RevealedExpiredTransactionBlocksNewTransaction verifies that
// a StartTokenTransaction call is rejected when its inputs are consumed by an existing
// REVEALED transaction, even if that transaction has crossed its expiry time. REVEALED is
// a non-preemptable state — all operators have committed to revealing revocation secrets —
// so expiry must not cause it to be silently skipped during pre-emption filtering.
func TestStartTokenTransaction_RevealedExpiredTransactionBlocksNewTransaction(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	f := entfixtures.New(t, ctx, dbClient)
	tokenCreate := f.CreateTokenCreate(btcnetwork.Regtest, nil, nil)

	_, mintOutputs := f.CreateMintTransaction(tokenCreate, entfixtures.OutputSpecs(big.NewInt(1000)), st.TokenTransactionStatusFinalized)
	mintOutput := mintOutputs[0]

	pastTimestamp := time.Now().Add(-2 * time.Hour)
	validitySeconds := uint64(60)
	competingTx, _ := f.CreateBalancedTransferTransactionWithOpts(
		tokenCreate,
		[]*ent.TokenOutput{mintOutput},
		entfixtures.OutputSpecs(big.NewInt(1000)),
		st.TokenTransactionStatusRevealed,
		&entfixtures.BalancedTransferTransactionOpts{
			ClientCreatedTimestamp:  &pastTimestamp,
			ValidityDurationSeconds: &validitySeconds,
		},
	)

	require.Equal(t, st.TokenTransactionStatusRevealed, competingTx.Status)
	require.Error(t, competingTx.ValidateNotExpired(), "competing tx must be expired for this test to be meaningful")

	validity := uint64(60)
	clientCreated := utils.ToMicrosecondPrecision(time.Now().UTC())
	newTransferProto := buildV3TransferTransactionProto(t, cfg, tokenCreate, mintOutput.CreatedTransactionFinalizedHash, validity, clientCreated)

	ownerSigs := []*tokenpb.SignatureWithIndex{
		{Signature: []byte{1}, InputIndex: 0},
	}

	handler := NewStartTokenTransactionHandler(cfg)
	_, err = handler.StartTokenTransaction(ctx, &tokenpb.StartTransactionRequest{
		PartialTokenTransaction:                newTransferProto,
		PartialTokenTransactionOwnerSignatures: ownerSigs,
		IdentityPublicKey:                      cfg.IdentityPublicKey().Serialize(),
		ValidityDurationSeconds:                validity,
	})

	require.Error(t, err, "expected the new transaction to be rejected because the competing REVEALED tx is non-preemptable even when expired")
}

// TestStartTokenTransaction_FinalizedExpiredTransactionBlocksNewTransaction verifies the
// same invariant as the REVEALED test above, but for FINALIZED transactions.
func TestStartTokenTransaction_FinalizedExpiredTransactionBlocksNewTransaction(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	f := entfixtures.New(t, ctx, dbClient)
	tokenCreate := f.CreateTokenCreate(btcnetwork.Regtest, nil, nil)

	_, mintOutputs := f.CreateMintTransaction(tokenCreate, entfixtures.OutputSpecs(big.NewInt(1000)), st.TokenTransactionStatusFinalized)
	mintOutput := mintOutputs[0]

	pastTimestamp := time.Now().Add(-2 * time.Hour)
	validitySeconds := uint64(60)
	competingTx, _ := f.CreateBalancedTransferTransactionWithOpts(
		tokenCreate,
		[]*ent.TokenOutput{mintOutput},
		entfixtures.OutputSpecs(big.NewInt(1000)),
		st.TokenTransactionStatusFinalized,
		&entfixtures.BalancedTransferTransactionOpts{
			ClientCreatedTimestamp:  &pastTimestamp,
			ValidityDurationSeconds: &validitySeconds,
		},
	)

	require.Equal(t, st.TokenTransactionStatusFinalized, competingTx.Status)
	require.Error(t, competingTx.ValidateNotExpired(), "competing tx must be expired for this test to be meaningful")

	validity := uint64(60)
	clientCreated := utils.ToMicrosecondPrecision(time.Now().UTC())
	newTransferProto := buildV3TransferTransactionProto(t, cfg, tokenCreate, mintOutput.CreatedTransactionFinalizedHash, validity, clientCreated)

	ownerSigs := []*tokenpb.SignatureWithIndex{
		{Signature: []byte{1}, InputIndex: 0},
	}

	handler := NewStartTokenTransactionHandler(cfg)
	_, err = handler.StartTokenTransaction(ctx, &tokenpb.StartTransactionRequest{
		PartialTokenTransaction:                newTransferProto,
		PartialTokenTransactionOwnerSignatures: ownerSigs,
		IdentityPublicKey:                      cfg.IdentityPublicKey().Serialize(),
		ValidityDurationSeconds:                validity,
	})

	require.Error(t, err, "expected the new transaction to be rejected because the competing FINALIZED tx is non-preemptable even when expired")
}

func buildV3TransferTransactionProto(
	t *testing.T,
	cfg *so.Config,
	tokenCreate *ent.TokenCreate,
	prevTxFinalizedHash []byte,
	validity uint64,
	clientCreated time.Time,
) *tokenpb.TokenTransaction {
	t.Helper()

	operatorKeys := make([][]byte, 0, len(cfg.SigningOperatorMap))
	for _, op := range cfg.SigningOperatorMap {
		operatorKeys = append(operatorKeys, op.IdentityPublicKey.Serialize())
	}
	slices.SortFunc(operatorKeys, func(a, b []byte) int {
		return bytes.Compare(a, b)
	})

	amountBytes := make([]byte, 16)
	big.NewInt(1000).FillBytes(amountBytes)

	// expectedBondSats/expectedRelativeBlockLocktime are read from Lrc20Configs which
	// is nil in TestConfig, so the zero values are expected by validation.
	bondSats := cfg.Lrc20Configs["regtest"].WithdrawBondSats
	relativeBlockLocktime := cfg.Lrc20Configs["regtest"].WithdrawRelativeBlockLocktime

	return &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: []*tokenpb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: prevTxFinalizedHash,
						PrevTokenTransactionVout: 0,
					},
				},
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey:                cfg.IdentityPublicKey().Serialize(),
				TokenIdentifier:               tokenCreate.TokenIdentifier,
				TokenAmount:                   amountBytes,
				WithdrawBondSats:              &bondSats,
				WithdrawRelativeBlockLocktime: &relativeBlockLocktime,
			},
		},
		SparkOperatorIdentityPublicKeys: operatorKeys,
		Network:                         sparkpb.Network_REGTEST,
		ValidityDurationSeconds:         &validity,
		ClientCreatedTimestamp:          timestamppb.New(clientCreated),
	}
}

func buildV3CreateTransactionProto(
	t *testing.T,
	cfg *so.Config,
	tokenCreate *ent.TokenCreate,
	issuer keys.Public,
	validity uint64,
	clientCreated time.Time,
) *tokenpb.TokenTransaction {
	t.Helper()

	var network sparkpb.Network
	switch tokenCreate.Network {
	case btcnetwork.Regtest:
		network = sparkpb.Network_REGTEST
	case btcnetwork.Mainnet:
		network = sparkpb.Network_MAINNET
	default:
		require.FailNow(t, "unsupported network for test proto", "network: %v", tokenCreate.Network)
	}

	operatorKeys := make([][]byte, 0, len(cfg.SigningOperatorMap))
	for _, op := range cfg.SigningOperatorMap {
		operatorKeys = append(operatorKeys, op.IdentityPublicKey.Serialize())
	}
	slices.SortFunc(operatorKeys, func(a, b []byte) int {
		return bytes.Compare(a, b)
	})

	maxSupply := tokenCreate.MaxSupply
	maxSupplyPadded := make([]byte, 16)
	copy(maxSupplyPadded[16-len(maxSupply):], maxSupply)

	return &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				TokenName:       "Test Token",
				TokenTicker:     "TST",
				Decimals:        8,
				MaxSupply:       maxSupplyPadded,
				IsFreezable:     false,
				IssuerPublicKey: issuer.Serialize(),
				// CreationEntityPublicKey must be nil for validation
			},
		},
		SparkOperatorIdentityPublicKeys: operatorKeys,
		Network:                         network,
		ValidityDurationSeconds:         &validity,
		ClientCreatedTimestamp:          timestamppb.New(clientCreated),
	}
}
