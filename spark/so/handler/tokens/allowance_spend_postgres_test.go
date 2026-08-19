package tokens

import (
	"context"
	"math/big"
	mathrand "math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	multisigpb "github.com/lightsparkdev/spark/proto/multisig"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenallowance"
	"github.com/lightsparkdev/spark/so/ent/tokenallowancespend"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

type allowanceSpendEnv struct {
	t          *testing.T
	ctx        context.Context
	tc         *db.TestContext
	cfg        *so.Config
	fixtures   *entfixtures.Fixtures
	handler    *InternalPrepareTokenHandler
	ownerKey   keys.Private
	spenderKey keys.Private

	tokenCreate       *ent.TokenCreate
	coordinatorPubKey []byte
	coordinatorIndex  uint64
}

func newAllowanceSpendEnv(t *testing.T, seed byte) *allowanceSpendEnv {
	return newAllowanceSpendEnvWithKnob(t, seed, true)
}

func newAllowanceSpendEnvWithKnob(t *testing.T, seed byte, allowancesKnobEnabled bool) *allowanceSpendEnv {
	t.Helper()
	ctx, tc := db.ConnectToTestPostgres(t)
	if allowancesKnobEnabled {
		ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobTokenAllowancesEnabled: 1.0,
		}))
	}
	cfg := sparktesting.TestConfig(t)
	dbtx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	rng := mathrand.NewChaCha8([32]byte{seed, 0xA1, 0x10, 0x0A})
	f := entfixtures.New(t, ctx, dbtx).WithRNG(rng)

	ownerKey := keys.MustGeneratePrivateKeyFromRand(rng)
	spenderKey := keys.MustGeneratePrivateKeyFromRand(rng)
	_, tokenCreate := f.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, big.NewInt(1_000_000))

	coordinator := testNonSelfCoordinator(t, cfg)

	return &allowanceSpendEnv{
		t:                 t,
		ctx:               ctx,
		tc:                tc,
		cfg:               cfg,
		fixtures:          f,
		handler:           NewInternalPrepareTokenHandler(cfg),
		ownerKey:          ownerKey,
		spenderKey:        spenderKey,
		tokenCreate:       tokenCreate,
		coordinatorPubKey: coordinator.IdentityPublicKey.Serialize(),
		coordinatorIndex:  coordinator.ID,
	}
}

func (e *allowanceSpendEnv) client() *ent.Client {
	e.t.Helper()
	dbtx, err := ent.GetDbFromContext(e.ctx)
	require.NoError(e.t, err)
	return dbtx
}

func (e *allowanceSpendEnv) createKeyshare() *ent.SigningKeyshare {
	e.t.Helper()
	keyshare := e.fixtures.CreateKeyshare()
	return e.client().SigningKeyshare.UpdateOneID(keyshare.ID).
		SetCoordinatorIndex(e.coordinatorIndex).
		SaveX(e.ctx)
}

type allowanceSpendOpts struct {
	perTxCap   uint64
	totalLimit uint64
	spent      uint64
	status     st.TokenAllowanceStatus // defaults to ACTIVE
	expiryTime time.Time               // defaults to now+24h
	allowlist  [][]byte
	owner      keys.Public // defaults to env owner
	spender    keys.Public // defaults to env spender
}

func (e *allowanceSpendEnv) createAllowance(opts allowanceSpendOpts) *ent.TokenAllowance {
	e.t.Helper()
	status := opts.status
	if status == "" {
		status = st.TokenAllowanceStatusActive
	}
	expiry := opts.expiryTime
	if expiry.IsZero() {
		expiry = time.Now().Add(24 * time.Hour)
	}
	owner := opts.owner
	if owner.IsZero() {
		owner = e.ownerKey.Public()
	}
	spender := opts.spender
	if spender.IsZero() {
		spender = e.spenderKey.Public()
	}

	create := e.client().TokenAllowance.Create().
		SetAllowanceID(uuid.New()).
		SetStatus(status).
		SetOwnerPublicKey(owner).
		SetSpenderPublicKey(spender).
		SetTokenIdentifier(e.tokenCreate.TokenIdentifier).
		SetTokenCreateID(e.tokenCreate.ID).
		SetPerTransactionCap(u128(opts.perTxCap)).
		SetTotalLimit(u128(opts.totalLimit)).
		SetSpentAmount(u128(opts.spent)).
		SetExpiryTime(expiry).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerSignature(e.fixtures.RandomBytes(64)).
		SetStatementHash(e.fixtures.RandomBytes(32)).
		SetVersion(1).
		SetOwnerProvidedTimestamp(uint64(time.Now().UnixMilli()))
	if len(opts.allowlist) > 0 {
		create = create.SetRecipientAllowlist(opts.allowlist)
	}
	return create.SaveX(e.ctx)
}

// mintInputs creates finalized mint outputs owned by the given key, one per amount.
func (e *allowanceSpendEnv) mintInputs(owner keys.Public, amounts ...uint64) []*ent.TokenOutput {
	e.t.Helper()
	specs := make([]entfixtures.OutputSpec, len(amounts))
	for i, amount := range amounts {
		specs[i] = entfixtures.OutputSpec{Amount: new(big.Int).SetUint64(amount), Owner: owner}
	}
	_, outputs := e.fixtures.CreateMintTransaction(e.tokenCreate, specs, st.TokenTransactionStatusFinalized)
	return outputs
}

type transferOut struct {
	owner  keys.Public
	amount uint64
}

// buildTransferProto builds a final V3 transfer transaction and its keyshare IDs.
func (e *allowanceSpendEnv) buildTransferProto(inputs []*ent.TokenOutput, outs []transferOut) (*tokenpb.TokenTransaction, []string) {
	e.t.Helper()
	outputsToSpend := make([]*tokenpb.TokenOutputToSpend, len(inputs))
	for i, input := range inputs {
		outputsToSpend[i] = &tokenpb.TokenOutputToSpend{
			PrevTokenTransactionHash: input.CreatedTransactionFinalizedHash,
			PrevTokenTransactionVout: uint32(input.CreatedTransactionOutputVout),
		}
	}
	cfgVals := e.cfg.Lrc20Configs[strings.ToLower(btcnetwork.Regtest.String())]
	tokenOutputs := make([]*tokenpb.TokenOutput, len(outs))
	keyshareIDs := make([]string, len(outs))
	for i, out := range outs {
		ks := e.createKeyshare()
		keyshareIDs[i] = ks.ID.String()
		tokenOutputs[i] = &tokenpb.TokenOutput{
			Id:                            new(uuid.Must(uuid.NewV7()).String()),
			OwnerPublicKey:                out.owner.Serialize(),
			TokenIdentifier:               e.tokenCreate.TokenIdentifier,
			TokenAmount:                   u128(out.amount),
			RevocationCommitment:          ks.PublicKey.Serialize(),
			WithdrawBondSats:              &cfgVals.WithdrawBondSats,
			WithdrawRelativeBlockLocktime: &cfgVals.WithdrawRelativeBlockLocktime,
		}
	}
	validity := uint64(300)
	return &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{OutputsToSpend: outputsToSpend},
		},
		TokenOutputs:                    tokenOutputs,
		Network:                         sparkpb.Network_REGTEST,
		ClientCreatedTimestamp:          timestamppb.New(utils.ToMicrosecondPrecision(time.Now())),
		ValidityDurationSeconds:         &validity,
		SparkOperatorIdentityPublicKeys: sortedOperatorKeys(e.cfg),
	}, keyshareIDs
}

func sortedOperatorKeys(cfg *so.Config) [][]byte {
	var opKeys [][]byte
	for _, op := range cfg.GetSigningOperatorList() {
		opKeys = append(opKeys, op.GetPublicKey())
	}
	// Sort bytewise ascending (required for V3 metadata validation).
	for i := 0; i < len(opKeys); i++ {
		for j := i + 1; j < len(opKeys); j++ {
			if string(opKeys[i]) > string(opKeys[j]) {
				opKeys[i], opKeys[j] = opKeys[j], opKeys[i]
			}
		}
	}
	return opKeys
}

// allowanceSignatures signs the transaction's partial hash with signer and wraps the result in
// the allowance_signature arm, one signature per input.
func (e *allowanceSpendEnv) allowanceSignatures(
	txProto *tokenpb.TokenTransaction,
	allowanceID uuid.UUID,
	signer keys.Private,
	embeddedKey keys.Public,
	numInputs int,
) []*tokenpb.SignatureWithIndex {
	e.t.Helper()
	hash, err := hashPartial(txProto)
	require.NoError(e.t, err)
	sigs := make([]*tokenpb.SignatureWithIndex, numInputs)
	for i := range numInputs {
		schnorrSig, err := schnorr.Sign(signer.ToBTCEC(), hash)
		require.NoError(e.t, err)
		sigs[i] = &tokenpb.SignatureWithIndex{
			InputIndex: uint32(i),
			AuthoritySignatures: &tokenpb.SignatureWithIndex_AllowanceSignature{
				AllowanceSignature: &tokenpb.AllowanceSignature{
					AllowanceId: allowanceID[:],
					SpenderSignature: &multisigpb.KeyedSignature{
						PublicKey: embeddedKey.Serialize(),
						Signature: schnorrSig.Serialize(),
					},
				},
			},
		}
	}
	return sigs
}

// ownerSignatures signs the transaction's partial hash with the owner key using the deprecated
// signature field, one signature per input.
func (e *allowanceSpendEnv) ownerSignatures(txProto *tokenpb.TokenTransaction, signer keys.Private, numInputs int) []*tokenpb.SignatureWithIndex {
	e.t.Helper()
	hash, err := hashPartial(txProto)
	require.NoError(e.t, err)
	sigs := make([]*tokenpb.SignatureWithIndex, numInputs)
	for i := range numInputs {
		schnorrSig, err := schnorr.Sign(signer.ToBTCEC(), hash)
		require.NoError(e.t, err)
		sigs[i] = &tokenpb.SignatureWithIndex{InputIndex: uint32(i), Signature: schnorrSig.Serialize()}
	}
	return sigs
}

func (e *allowanceSpendEnv) prepare(txProto *tokenpb.TokenTransaction, sigs []*tokenpb.SignatureWithIndex, keyshareIDs []string) error {
	e.t.Helper()
	_, err := e.handler.PrepareTokenTransactionInternal(e.ctx, &tokeninternalpb.PrepareTransactionRequest{
		FinalTokenTransaction:      txProto,
		TokenTransactionSignatures: sigs,
		KeyshareIds:                keyshareIDs,
		CoordinatorPublicKey:       e.coordinatorPubKey,
	})
	return err
}

func (e *allowanceSpendEnv) reloadAllowance(allowance *ent.TokenAllowance) *ent.TokenAllowance {
	e.t.Helper()
	row, err := e.client().TokenAllowance.Get(e.ctx, allowance.ID)
	require.NoError(e.t, err)
	return row
}

func (e *allowanceSpendEnv) spendsFor(allowance *ent.TokenAllowance) []*ent.TokenAllowanceSpend {
	e.t.Helper()
	rows, err := e.client().TokenAllowanceSpend.Query().
		Where(tokenallowancespend.TokenAllowanceID(allowance.ID)).
		WithTokenTransaction().
		All(e.ctx)
	require.NoError(e.t, err)
	return rows
}

// createExpiredAllowance builds a grant whose window has already closed. expiry_time is
// immutable, so the value is set at creation rather than updated afterward.
func (e *allowanceSpendEnv) createExpiredAllowance(opts allowanceSpendOpts) *ent.TokenAllowance {
	e.t.Helper()
	opts.expiryTime = time.Now().Add(-time.Minute)
	return e.createAllowance(opts)
}

// --- Prepare-time metering tests ---

func TestPrepareTransfer_AllowanceHappyPathDecrementsBudget(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x01)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	require.NoError(t, e.prepare(txProto, sigs, keyshareIDs))

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(100), row.SpentAmount)
	assert.Equal(t, st.TokenAllowanceStatusActive, row.Status)

	spends := e.spendsFor(allowance)
	require.Len(t, spends, 1)
	assert.Equal(t, st.TokenAllowanceSpendStatusReserved, spends[0].Status)
	assert.Equal(t, u128(100), spends[0].MeteredAmount)

	finalHash, err := utils.HashTokenTransaction(txProto, false)
	require.NoError(t, err)
	require.NotNil(t, spends[0].Edges.TokenTransaction)
	assert.Equal(t, finalHash, spends[0].Edges.TokenTransaction.FinalizedTokenTransactionHash)
}

func TestPrepareTransfer_AllowanceChangeToOwnerNotMetered(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x02)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{
		{recipient, 40},
		{e.ownerKey.Public(), 60}, // change back to the owner is free
	})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	require.NoError(t, e.prepare(txProto, sigs, keyshareIDs))

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(40), row.SpentAmount)
}

func TestPrepareTransfer_AllowanceRejectsOverPerTxCap(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x03)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 50, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, tokens.ErrAllowanceExceedsPerTxCap)

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(0), row.SpentAmount)
}

func TestPrepareTransfer_AllowanceRejectsInsufficientBudget(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x04)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 100, totalLimit: 100, spent: 80})
	inputs := e.mintInputs(e.ownerKey.Public(), 50)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 50}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, tokens.ErrAllowanceBudgetExhausted)

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(80), row.SpentAmount)
}

func TestPrepareTransfer_AllowanceRejectsExpiredAllowance(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x05)
	allowance := e.createAllowance(allowanceSpendOpts{
		perTxCap:   1_000,
		totalLimit: 10_000,
		expiryTime: time.Now().Add(-time.Minute),
	})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, tokens.ErrAllowanceExpired)
}

func TestPrepareTransfer_AllowanceRejectsRevokedAllowance(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x06)
	allowance := e.createAllowance(allowanceSpendOpts{
		perTxCap:   1_000,
		totalLimit: 10_000,
		status:     st.TokenAllowanceStatusRevoked,
	})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, tokens.ErrAllowanceRevoked)
}

func TestPrepareTransfer_AllowanceRejectsWrongSpenderKey(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x07)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	wrongKey := e.fixtures.GeneratePrivateKey()
	recipient := e.fixtures.GeneratePrivateKey().Public()

	// Signed by the wrong key with the wrong key embedded: the embedded key must match the
	// spender recorded on the grant, never a key the request claims.
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, wrongKey, wrongKey.Public(), 1)
	err := e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, "spender_signature public key does not match the allowance spender")

	// Signed by the wrong key with the correct spender key embedded: signature verification
	// against the grant's spender key must fail.
	inputs2 := e.mintInputs(e.ownerKey.Public(), 100)
	txProto2, keyshareIDs2 := e.buildTransferProto(inputs2, []transferOut{{recipient, 100}})
	sigs2 := e.allowanceSignatures(txProto2, allowance.AllowanceID, wrongKey, e.spenderKey.Public(), 1)
	err = e.prepare(txProto2, sigs2, keyshareIDs2)
	require.ErrorContains(t, err, "invalid ownership signature")
}

func TestPrepareTransfer_AllowanceRejectsOwnerMismatch(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x08)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	// Inputs owned by someone other than the allowance owner. The spender signature itself is
	// valid, so the metering-time owner check is the only guard against the spender using its
	// grant to move a third party's outputs.
	otherOwner := e.fixtures.GeneratePrivateKey()
	inputs := e.mintInputs(otherOwner.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, "owner does not match the allowance owner")
}

func TestPrepareTransfer_AllowanceRejectsRecipientNotInAllowlist(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x09)
	allowedRecipient := e.fixtures.GeneratePrivateKey().Public()
	allowance := e.createAllowance(allowanceSpendOpts{
		perTxCap:   1_000,
		totalLimit: 10_000,
		allowlist:  [][]byte{allowedRecipient.Serialize()},
	})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	notAllowed := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{notAllowed, 100}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	err := e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, tokens.ErrAllowanceRecipientNotAllowed)

	// The allowlisted recipient is accepted.
	inputs2 := e.mintInputs(e.ownerKey.Public(), 100)
	txProto2, keyshareIDs2 := e.buildTransferProto(inputs2, []transferOut{{allowedRecipient, 100}})
	sigs2 := e.allowanceSignatures(txProto2, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(txProto2, sigs2, keyshareIDs2))
}

func TestPrepareTransfer_AllowanceRejectsMixedAuthorizationModes(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x0C)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100, 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 200}})
	allowanceSigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 2)
	ownerSigs := e.ownerSignatures(txProto, e.ownerKey, 2)
	ownerSigs[1].InputIndex = 1
	mixed := []*tokenpb.SignatureWithIndex{allowanceSigs[0], ownerSigs[1]}

	err := e.prepare(txProto, mixed, keyshareIDs)
	require.ErrorContains(t, err, tokens.ErrAllowanceMixedAuthorization)
}

func TestPrepareTransfer_AllowanceRejectsWhenKnobDisabled(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnvWithKnob(t, 0x0D, false)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, "token allowances are not enabled")
}

func TestPrepareTransfer_AllowanceRejectsVersionBelowV3(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x0E)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	txProto.Version = 2
	txProto.ValidityDurationSeconds = nil
	txProto.ExpiryTime = timestamppb.New(time.Now().Add(time.Hour))
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, "require token transaction version 3+")
}

// TestPrepareMint_RejectsAllowanceSignatureArm proves the security-critical default-reject: even
// a VALID issuer signature wrapped in the allowance_signature arm must never authorize a mint.
func TestPrepareMint_RejectsAllowanceSignatureArm(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x0F)
	issuerPriv, tokenCreate := e.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, big.NewInt(1_000_000))
	ks := e.createKeyshare()
	cfgVals := e.cfg.Lrc20Configs[strings.ToLower(btcnetwork.Regtest.String())]

	now := time.Now()
	txProto := &tokenpb.TokenTransaction{
		Version: 2,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: issuerPriv.Public().Serialize(),
				TokenIdentifier: tokenCreate.TokenIdentifier,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{{
			Id:                            new(uuid.Must(uuid.NewV7()).String()),
			OwnerPublicKey:                issuerPriv.Public().Serialize(),
			TokenIdentifier:               tokenCreate.TokenIdentifier,
			TokenAmount:                   u128(50),
			RevocationCommitment:          ks.PublicKey.Serialize(),
			WithdrawBondSats:              &cfgVals.WithdrawBondSats,
			WithdrawRelativeBlockLocktime: &cfgVals.WithdrawRelativeBlockLocktime,
		}},
		Network:                         sparkpb.Network_REGTEST,
		ExpiryTime:                      timestamppb.New(now.Add(24 * time.Hour)),
		ClientCreatedTimestamp:          timestamppb.New(now),
		SparkOperatorIdentityPublicKeys: sortedOperatorKeys(e.cfg),
	}

	partialHash, err := utils.HashTokenTransaction(txProto, true)
	require.NoError(t, err)
	validIssuerSig, err := schnorr.Sign(issuerPriv.ToBTCEC(), partialHash)
	require.NoError(t, err)

	allowanceID := uuid.New()
	sigs := []*tokenpb.SignatureWithIndex{{
		InputIndex: 0,
		AuthoritySignatures: &tokenpb.SignatureWithIndex_AllowanceSignature{
			AllowanceSignature: &tokenpb.AllowanceSignature{
				AllowanceId: allowanceID[:],
				SpenderSignature: &multisigpb.KeyedSignature{
					PublicKey: issuerPriv.Public().Serialize(),
					Signature: validIssuerSig.Serialize(),
				},
			},
		},
	}}

	_, err = e.handler.PrepareTokenTransactionInternal(e.ctx, &tokeninternalpb.PrepareTransactionRequest{
		FinalTokenTransaction:      txProto,
		TokenTransactionSignatures: sigs,
		KeyshareIds:                []string{ks.ID.String()},
		CoordinatorPublicKey:       e.coordinatorPubKey,
	})
	require.ErrorContains(t, err, "allowance signatures are not valid in this context")
}

// TestPrepareCreate_RejectsAllowanceSignatureArm mirrors the mint test for token creation: the
// allowance arm is rejected outright even when it wraps a valid issuer signature.
func TestPrepareCreate_RejectsAllowanceSignatureArm(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x10)
	issuerPriv := e.fixtures.GeneratePrivateKey()

	validity := uint64(300)
	txProto := &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				TokenName:               "Arm Reject",
				TokenTicker:             "ARJ",
				Decimals:                8,
				MaxSupply:               u128(1_000_000),
				IsFreezable:             false,
				IssuerPublicKey:         issuerPriv.Public().Serialize(),
				CreationEntityPublicKey: e.tokenCreate.CreationEntityPublicKey.Serialize(),
			},
		},
		Network:                         sparkpb.Network_REGTEST,
		ClientCreatedTimestamp:          timestamppb.New(utils.ToMicrosecondPrecision(time.Now())),
		ValidityDurationSeconds:         &validity,
		SparkOperatorIdentityPublicKeys: sortedOperatorKeys(e.cfg),
	}

	partialHash, err := hashPartial(txProto)
	require.NoError(t, err)
	validIssuerSig, err := schnorr.Sign(issuerPriv.ToBTCEC(), partialHash)
	require.NoError(t, err)

	allowanceID := uuid.New()
	sigs := []*tokenpb.SignatureWithIndex{{
		InputIndex: 0,
		AuthoritySignatures: &tokenpb.SignatureWithIndex_AllowanceSignature{
			AllowanceSignature: &tokenpb.AllowanceSignature{
				AllowanceId: allowanceID[:],
				SpenderSignature: &multisigpb.KeyedSignature{
					PublicKey: issuerPriv.Public().Serialize(),
					Signature: validIssuerSig.Serialize(),
				},
			},
		},
	}}

	_, err = e.handler.PrepareTokenTransactionInternal(e.ctx, &tokeninternalpb.PrepareTransactionRequest{
		FinalTokenTransaction:      txProto,
		TokenTransactionSignatures: sigs,
		KeyshareIds:                nil,
		CoordinatorPublicKey:       e.coordinatorPubKey,
	})
	require.ErrorContains(t, err, "allowance signatures are not valid")
}

// --- Release / lazy-release tests ---

func TestPrepareTransfer_PreemptionReleasesLoserBudget(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x11)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	// Transaction A: delegated spend, client timestamp now.
	txA, keysharesA := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigsA := e.allowanceSignatures(txA, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(txA, sigsA, keysharesA))
	require.Equal(t, u128(100), e.reloadAllowance(allowance).SpentAmount)

	// Transaction B: owner-signed spend of the same input with an EARLIER client timestamp, so
	// it preempts A. A's reserved budget must be returned.
	txB, keysharesB := e.buildTransferProto(inputs, []transferOut{{e.fixtures.GeneratePrivateKey().Public(), 100}})
	txB.ClientCreatedTimestamp = timestamppb.New(utils.ToMicrosecondPrecision(time.Now().Add(-2 * time.Second)))
	sigsB := e.ownerSignatures(txB, e.ownerKey, 1)
	require.NoError(t, e.prepare(txB, sigsB, keysharesB))

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(0), row.SpentAmount, "loser's reserved budget must be returned")
	assert.Equal(t, st.TokenAllowanceStatusActive, row.Status)

	spends := e.spendsFor(allowance)
	require.Len(t, spends, 1)
	assert.Equal(t, st.TokenAllowanceSpendStatusReleased, spends[0].Status)
}

func TestPrepareTransfer_PreemptionDoesNotReactivateConflictingAllowance(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x21)
	firstAllowance := e.createAllowance(allowanceSpendOpts{perTxCap: 100, totalLimit: 100})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	losingTx, losingKeyshares := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	losingSigs := e.allowanceSignatures(losingTx, firstAllowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(losingTx, losingSigs, losingKeyshares))
	require.Equal(t, st.TokenAllowanceStatusExhausted, e.reloadAllowance(firstAllowance).Status)

	secondAllowance := e.createAllowance(allowanceSpendOpts{perTxCap: 100, totalLimit: 100})
	winningTx, winningKeyshares := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	winningTx.ClientCreatedTimestamp = timestamppb.New(utils.ToMicrosecondPrecision(time.Now().Add(-2 * time.Second)))
	winningSigs := e.ownerSignatures(winningTx, e.ownerKey, 1)
	require.NoError(t, e.prepare(winningTx, winningSigs, winningKeyshares))

	firstRow := e.reloadAllowance(firstAllowance)
	assert.Equal(t, u128(0), firstRow.SpentAmount)
	assert.Equal(t, st.TokenAllowanceStatusExhausted, firstRow.Status)
	assert.Equal(t, st.TokenAllowanceStatusActive, e.reloadAllowance(secondAllowance).Status)

	activeCount, err := e.client().TokenAllowance.Query().
		Where(
			tokenallowance.OwnerPublicKey(e.ownerKey.Public()),
			tokenallowance.SpenderPublicKey(e.spenderKey.Public()),
			tokenallowance.TokenCreateID(e.tokenCreate.ID),
			tokenallowance.StatusEQ(st.TokenAllowanceStatusActive),
		).
		Count(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, activeCount)

	spends := e.spendsFor(firstAllowance)
	require.Len(t, spends, 1)
	assert.Equal(t, st.TokenAllowanceSpendStatusReleased, spends[0].Status)
}

func TestPrepareTransfer_ExpiredStartedSpendReleasedLazily(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x12)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 100, totalLimit: 100, spent: 60})

	// A STARTED transaction that expired (V3 client timestamp far past validity) still holds a
	// RESERVED spend of 60 against the allowance.
	staleTx := e.client().TokenTransaction.Create().
		SetPartialTokenTransactionHash(e.fixtures.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(e.fixtures.RandomBytes(32)).
		SetStatus(st.TokenTransactionStatusStarted).
		SetVersion(st.TokenTransactionVersionV3).
		SetClientCreatedTimestamp(time.Now().Add(-time.Hour)).
		SetValidityDurationSeconds(60).
		SaveX(e.ctx)
	staleSpend := e.client().TokenAllowanceSpend.Create().
		SetTokenAllowanceID(allowance.ID).
		SetTokenTransaction(staleTx).
		SetMeteredAmount(u128(60)).
		SetStatus(st.TokenAllowanceSpendStatusReserved).
		SaveX(e.ctx)

	// A new delegated spend of 80 only fits if the expired reservation is lazily released.
	inputs := e.mintInputs(e.ownerKey.Public(), 80)
	recipient := e.fixtures.GeneratePrivateKey().Public()
	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 80}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(txProto, sigs, keyshareIDs))

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(80), row.SpentAmount, "60 released lazily, 80 newly reserved")

	reloadedStale, err := e.client().TokenAllowanceSpend.Get(e.ctx, staleSpend.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TokenAllowanceSpendStatusReleased, reloadedStale.Status)
}

func TestPrepareTransfer_ExhaustedFlipsBackToActiveOnRelease(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x13)
	allowance := e.createAllowance(allowanceSpendOpts{
		perTxCap:   100,
		totalLimit: 100,
		spent:      100,
		status:     st.TokenAllowanceStatusExhausted,
	})

	// The full budget is held by a CANCELLED transaction, so it is releasable.
	cancelledTx := e.client().TokenTransaction.Create().
		SetPartialTokenTransactionHash(e.fixtures.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(e.fixtures.RandomBytes(32)).
		SetStatus(st.TokenTransactionStatusStartedCancelled).
		SaveX(e.ctx)
	e.client().TokenAllowanceSpend.Create().
		SetTokenAllowanceID(allowance.ID).
		SetTokenTransaction(cancelledTx).
		SetMeteredAmount(u128(100)).
		SetStatus(st.TokenAllowanceSpendStatusReserved).
		SaveX(e.ctx)

	inputs := e.mintInputs(e.ownerKey.Public(), 50)
	recipient := e.fixtures.GeneratePrivateKey().Public()
	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 50}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(txProto, sigs, keyshareIDs))

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(50), row.SpentAmount)
	assert.Equal(t, st.TokenAllowanceStatusActive, row.Status, "EXHAUSTED must flip back to ACTIVE once budget frees")
}

func TestPrepareTransfer_ExhaustedAllowanceRejectsReactivationConflict(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x22)
	firstAllowance := e.createAllowance(allowanceSpendOpts{
		perTxCap:   100,
		totalLimit: 100,
		spent:      100,
		status:     st.TokenAllowanceStatusExhausted,
	})

	cancelledTx := e.client().TokenTransaction.Create().
		SetPartialTokenTransactionHash(e.fixtures.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(e.fixtures.RandomBytes(32)).
		SetStatus(st.TokenTransactionStatusStartedCancelled).
		SaveX(e.ctx)
	e.client().TokenAllowanceSpend.Create().
		SetTokenAllowanceID(firstAllowance.ID).
		SetTokenTransaction(cancelledTx).
		SetMeteredAmount(u128(100)).
		SetStatus(st.TokenAllowanceSpendStatusReserved).
		SaveX(e.ctx)
	e.createAllowance(allowanceSpendOpts{perTxCap: 100, totalLimit: 100})

	inputs := e.mintInputs(e.ownerKey.Public(), 50)
	recipient := e.fixtures.GeneratePrivateKey().Public()
	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 50}})
	sigs := e.allowanceSignatures(txProto, firstAllowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, tokens.ErrAllowanceBudgetExhausted)
	assert.NotContains(t, err.Error(), "failed to update allowance")
}

func TestPrepareTransfer_ConcurrentSpendsFailClosed(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x14)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 80, totalLimit: 100})

	inputs1 := e.mintInputs(e.ownerKey.Public(), 80)
	inputs2 := e.mintInputs(e.ownerKey.Public(), 80)
	tx1, keyshares1 := e.buildTransferProto(inputs1, []transferOut{{e.fixtures.GeneratePrivateKey().Public(), 80}})
	tx2, keyshares2 := e.buildTransferProto(inputs2, []transferOut{{e.fixtures.GeneratePrivateKey().Public(), 80}})
	sigs1 := e.allowanceSignatures(tx1, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	sigs2 := e.allowanceSignatures(tx2, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	// Commit fixture data so the concurrent sessions can see it.
	require.NoError(t, ent.DbCommit(e.ctx))

	results := make(chan error, 2)
	var wg sync.WaitGroup
	run := func(txProto *tokenpb.TokenTransaction, sigs []*tokenpb.SignatureWithIndex, keyshareIDs []string) {
		defer wg.Done()
		session := db.NewDefaultSessionFactory(e.tc.Client).NewSession(t.Context())
		gctx := ent.Inject(t.Context(), session)
		gctx = knobs.InjectKnobsService(gctx, knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobTokenAllowancesEnabled: 1.0,
		}))
		_, err := e.handler.PrepareTokenTransactionInternal(gctx, &tokeninternalpb.PrepareTransactionRequest{
			FinalTokenTransaction:      txProto,
			TokenTransactionSignatures: sigs,
			KeyshareIds:                keyshareIDs,
			CoordinatorPublicKey:       e.coordinatorPubKey,
		})
		if err == nil {
			err = ent.DbCommit(gctx)
		} else if tx := session.GetTxIfExists(); tx != nil {
			_ = tx.Rollback()
		}
		results <- err
	}
	wg.Add(2)
	go run(tx1, sigs1, keyshares1)
	go run(tx2, sigs2, keyshares2)
	wg.Wait()
	close(results)

	var failures []error
	for err := range results {
		if err != nil {
			failures = append(failures, err)
		}
	}
	require.Len(t, failures, 1, "exactly one of two concurrent spends must win the remaining budget")
	require.ErrorContains(t, failures[0], tokens.ErrAllowanceBudgetExhausted)

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(80), row.SpentAmount)
	spends := e.spendsFor(allowance)
	require.Len(t, spends, 1)
	assert.Equal(t, st.TokenAllowanceSpendStatusReserved, spends[0].Status)
}

// --- Sign and pre-reveal checkpoint tests ---

func TestSignTransfer_AllowanceRevokedAfterPrepareBlocksSigning(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x15)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	recipient := e.fixtures.GeneratePrivateKey().Public()
	signHandler := NewInternalSignTokenHandler(e.cfg)

	// Control: an allowance transfer prepared while the allowance is active signs fine.
	inputs1 := e.mintInputs(e.ownerKey.Public(), 100)
	tx1, keyshares1 := e.buildTransferProto(inputs1, []transferOut{{recipient, 100}})
	sigs1 := e.allowanceSignatures(tx1, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(tx1, sigs1, keyshares1))

	locked1, err := ent.FetchAndLockTokenTransactionData(e.ctx, tx1)
	require.NoError(t, err)
	hash1, err := utils.HashTokenTransaction(tx1, false)
	require.NoError(t, err)
	_, err = signHandler.SignAndPersistTokenTransaction(e.ctx, locked1, tx1, hash1, nil)
	require.NoError(t, err)

	// A second transfer prepared before revocation must be blocked at signing once the owner
	// revokes the allowance.
	inputs2 := e.mintInputs(e.ownerKey.Public(), 100)
	tx2, keyshares2 := e.buildTransferProto(inputs2, []transferOut{{recipient, 100}})
	sigs2 := e.allowanceSignatures(tx2, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(tx2, sigs2, keyshares2))

	e.client().TokenAllowance.UpdateOneID(allowance.ID).
		SetStatus(st.TokenAllowanceStatusRevoked).
		SetOwnerProvidedRevokeTimestamp(uint64(time.Now().UnixMilli())).
		SetRevokeSignature(e.fixtures.RandomBytes(64)).
		SaveX(e.ctx)

	locked2, err := ent.FetchAndLockTokenTransactionData(e.ctx, tx2)
	require.NoError(t, err)
	hash2, err := utils.HashTokenTransaction(tx2, false)
	require.NoError(t, err)
	_, err = signHandler.SignAndPersistTokenTransaction(e.ctx, locked2, tx2, hash2, nil)
	require.ErrorContains(t, err, tokens.ErrAllowanceRevoked)
}

func TestSignTransfer_AllowanceSignatureMustMatchPreparedAllowance(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x16)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	prepareSigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(txProto, prepareSigs, keyshareIDs))

	lockedTx, err := ent.FetchAndLockTokenTransactionData(e.ctx, txProto)
	require.NoError(t, err)
	finalHash, err := utils.HashTokenTransaction(txProto, false)
	require.NoError(t, err)
	payloadHash, err := utils.HashOperatorSpecificPayload(finalHash, e.cfg.IdentityPublicKey())
	require.NoError(t, err)

	makeSignSig := func(allowanceID uuid.UUID) []*tokenpb.SignatureWithIndex {
		schnorrSig, err := schnorr.Sign(e.spenderKey.ToBTCEC(), payloadHash)
		require.NoError(t, err)
		return []*tokenpb.SignatureWithIndex{{
			InputIndex: 0,
			AuthoritySignatures: &tokenpb.SignatureWithIndex_AllowanceSignature{
				AllowanceSignature: &tokenpb.AllowanceSignature{
					AllowanceId: allowanceID[:],
					SpenderSignature: &multisigpb.KeyedSignature{
						PublicKey: e.spenderKey.Public().Serialize(),
						Signature: schnorrSig.Serialize(),
					},
				},
			},
		}}
	}

	// Citing the allowance the transaction was prepared under validates.
	require.NoError(t, validateTransferOwnerSignatures(
		e.ctx, e.cfg.IdentityPublicKey(), makeSignSig(allowance.AllowanceID), lockedTx, finalHash))

	// Citing a different allowance is rejected even though the spender signature is valid.
	otherAllowanceID := uuid.New()
	err = validateTransferOwnerSignatures(
		e.ctx, e.cfg.IdentityPublicKey(), makeSignSig(otherAllowanceID), lockedTx, finalHash)
	require.ErrorContains(t, err, "prepared under allowance")
}

func TestExchangeRevocationSecrets_BlocksWhenAllowanceRevoked(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x17)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)

	var operatorPubKeys []keys.Public
	for _, op := range e.cfg.SigningOperatorMap {
		operatorPubKeys = append(operatorPubKeys, op.IdentityPublicKey)
	}
	result := e.fixtures.CreateTransferTransactionWithProto(
		e.tokenCreate,
		inputs,
		[]entfixtures.OutputSpec{{Amount: big.NewInt(100)}},
		entfixtures.TransferTransactionOpts{
			OperatorPublicKeys: operatorPubKeys,
			Status:             st.TokenTransactionStatusSigned,
		},
	)

	// The SIGNED transfer holds a RESERVED allowance spend, then the owner revokes the grant.
	e.client().TokenAllowanceSpend.Create().
		SetTokenAllowanceID(allowance.ID).
		SetTokenTransaction(result.Transaction).
		SetMeteredAmount(u128(100)).
		SetStatus(st.TokenAllowanceSpendStatusReserved).
		SaveX(e.ctx)
	e.client().TokenAllowance.UpdateOneID(allowance.ID).
		SetStatus(st.TokenAllowanceStatusRevoked).
		SetOwnerProvidedRevokeTimestamp(uint64(time.Now().UnixMilli())).
		SetRevokeSignature(e.fixtures.RandomBytes(64)).
		SaveX(e.ctx)

	outputsToSpend := make([]*tokeninternalpb.OutputToSpend, len(inputs))
	for i, input := range inputs {
		outputsToSpend[i] = &tokeninternalpb.OutputToSpend{
			CreatedTokenTransactionHash: input.CreatedTransactionFinalizedHash,
			CreatedTokenTransactionVout: uint32(input.CreatedTransactionOutputVout),
			SpentTokenTransactionVout:   uint32(i),
		}
	}

	signHandler := NewInternalSignTokenHandler(e.cfg)
	_, err := signHandler.ExchangeRevocationSecretsShares(e.ctx, &tokeninternalpb.ExchangeRevocationSecretsSharesRequest{
		FinalTokenTransaction:     result.Proto,
		FinalTokenTransactionHash: result.Hash,
		OperatorIdentityPublicKey: e.cfg.IdentityPublicKey().Serialize(),
		OperatorShares: []*tokeninternalpb.OperatorRevocationShares{
			{OperatorIdentityPublicKey: e.cfg.IdentityPublicKey().Serialize()},
		},
		OutputsToSpend: outputsToSpend,
	})
	require.ErrorContains(t, err, "revoked allowance cannot exchange revocation secrets")
}

// TestSignGate_ExpiryBoundaryLazyReleaseCannotExceedTotalLimit reproduces the expiry-boundary
// interleaving between the one-shot revocation-exchange path and prepare-time lazy release:
//
//  1. Transaction A (delegated, meters 60 of a 100 budget) is STARTED and close to its expiry.
//  2. A's finalize path (ExchangeRevocationSecretsShares on a STARTED transfer) locks the
//     transaction data and passes validateTokenTransactionForSigning just BEFORE expiry, then
//     signs, reveals, and finalizes in the same database transaction without re-checking the
//     allowance.
//  3. After the expiry boundary passes, transaction B (same allowance, different inputs, meters
//  80. runs prepare. Lazy release sees A as an expired STARTED transaction, releases A's
//     RESERVED spend, and reserves 80 for itself.
//  4. A commits FINALIZED. Both A (60) and B (80) settle against a total_limit of 100.
//
// The signing gate must serialize with lazy release so a spend whose transaction can still
// finalize is never released: either A finalizes first and B is rejected for exhausting the
// budget, or B releases first and A is blocked at the gate.
func TestSignGate_ExpiryBoundaryLazyReleaseCannotExceedTotalLimit(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x19)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 100, totalLimit: 100})
	recipient := e.fixtures.GeneratePrivateKey().Public()

	// Transaction A: delegated spend metering 60.
	inputsA := e.mintInputs(e.ownerKey.Public(), 60)
	txA, keysharesA := e.buildTransferProto(inputsA, []transferOut{{recipient, 60}})
	sigsA := e.allowanceSignatures(txA, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(txA, sigsA, keysharesA))

	finalHashA, err := utils.HashTokenTransaction(txA, false)
	require.NoError(t, err)
	entTxA := e.client().TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalHashA)).
		OnlyX(e.ctx)

	// Move A right up against its expiry: with the stored 300s validity, a client timestamp of
	// expiry-300s makes A expire ~3s from now - after A's gate runs but before B prepares.
	expiryA := time.Now().Add(3 * time.Second)
	e.client().TokenTransaction.UpdateOneID(entTxA.ID).
		SetClientCreatedTimestamp(expiryA.Add(-300 * time.Second)).
		SaveX(e.ctx)

	// Transaction B: delegated spend of different inputs metering 80.
	inputsB := e.mintInputs(e.ownerKey.Public(), 80)
	txB, keysharesB := e.buildTransferProto(inputsB, []transferOut{{recipient, 80}})
	sigsB := e.allowanceSignatures(txB, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	// Publish the fixture state so the two concurrent sessions below can see it.
	require.NoError(t, ent.DbCommit(e.ctx))

	// Session A: the peer-side one-shot exchange critical section for a STARTED transfer. It
	// locks the transaction data, passes the signing gate pre-expiry, and then (standing in for
	// sign + persist shares + finalize, none of which re-check the allowance) marks A FINALIZED.
	// The database transaction stays open, exactly like an in-flight
	// ExchangeRevocationSecretsShares call.
	sessionA := db.NewDefaultSessionFactory(e.tc.Client).NewSession(t.Context())
	ctxA := ent.Inject(t.Context(), sessionA)
	lockedTxA, err := ent.FetchAndLockTokenTransactionDataByHash(ctxA, finalHashA)
	require.NoError(t, err)
	require.NoError(t, validateTokenTransactionForSigning(ctxA, e.cfg, lockedTxA, txA),
		"gate must pass: A is not yet expired and its spend is RESERVED")
	dbA, err := ent.GetDbFromContext(ctxA)
	require.NoError(t, err)
	dbA.TokenTransaction.UpdateOneID(lockedTxA.ID).
		SetStatus(st.TokenTransactionStatusFinalized).
		SaveX(ctxA)

	// Cross the expiry boundary so B's lazy release sees A as an expired STARTED transaction.
	time.Sleep(time.Until(expiryA) + 250*time.Millisecond)

	// Session B: prepare the second delegated spend concurrently with A's open transaction.
	bResult := make(chan error, 1)
	go func() {
		sessionB := db.NewDefaultSessionFactory(e.tc.Client).NewSession(t.Context())
		gctx := ent.Inject(t.Context(), sessionB)
		gctx = knobs.InjectKnobsService(gctx, knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobTokenAllowancesEnabled: 1.0,
		}))
		_, err := e.handler.PrepareTokenTransactionInternal(gctx, &tokeninternalpb.PrepareTransactionRequest{
			FinalTokenTransaction:      txB,
			TokenTransactionSignatures: sigsB,
			KeyshareIds:                keysharesB,
			CoordinatorPublicKey:       e.coordinatorPubKey,
		})
		if err == nil {
			err = ent.DbCommit(gctx)
		} else if tx := sessionB.GetTxIfExists(); tx != nil {
			_ = tx.Rollback()
		}
		bResult <- err
	}()

	// Give B time to either finish (if it does not serialize with A's gate - the bug) or block
	// on A's locked spend row (the fixed behavior), then let A's finalize commit.
	var bErr error
	bDone := false
	select {
	case bErr = <-bResult:
		bDone = true
	case <-time.After(1500 * time.Millisecond):
	}
	require.NoError(t, ent.DbCommit(ctxA))
	if !bDone {
		bErr = <-bResult
	}

	// A finalized 60 against the 100 budget, so B's 80 must not fit. If B succeeded, lazy
	// release refunded a spend whose transaction still finalized: 140 settled against a 100 cap.
	require.Error(t, bErr, "delegated spends totaling 140 settled against a total_limit of 100")
	require.ErrorContains(t, bErr, tokens.ErrAllowanceBudgetExhausted)

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(60), row.SpentAmount, "the finalized transaction's spend must remain metered")
	spends := e.spendsFor(allowance)
	require.Len(t, spends, 1)
	assert.Equal(t, st.TokenAllowanceSpendStatusReserved, spends[0].Status)
}

// TestSignGate_ReleasedSpendBlocksRevivedTransaction verifies both halves of legitimate lazy
// release: a genuinely expired STARTED transaction's budget is returned, and if that transaction
// later comes back to life it is blocked at the signing gate because its spend is RELEASED.
func TestSignGate_ReleasedSpendBlocksRevivedTransaction(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x1A)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 100, totalLimit: 100})
	recipient := e.fixtures.GeneratePrivateKey().Public()

	inputsA := e.mintInputs(e.ownerKey.Public(), 60)
	txA, keysharesA := e.buildTransferProto(inputsA, []transferOut{{recipient, 60}})
	sigsA := e.allowanceSignatures(txA, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(txA, sigsA, keysharesA))

	finalHashA, err := utils.HashTokenTransaction(txA, false)
	require.NoError(t, err)
	entTxA := e.client().TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalHashA)).
		OnlyX(e.ctx)

	// A expires long before anyone signs it, so B's prepare lazily releases its budget.
	e.client().TokenTransaction.UpdateOneID(entTxA.ID).
		SetClientCreatedTimestamp(time.Now().Add(-2 * time.Hour)).
		SaveX(e.ctx)

	inputsB := e.mintInputs(e.ownerKey.Public(), 80)
	txB, keysharesB := e.buildTransferProto(inputsB, []transferOut{{recipient, 80}})
	sigsB := e.allowanceSignatures(txB, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(txB, sigsB, keysharesB))

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(80), row.SpentAmount, "A's 60 released, B's 80 reserved")

	// Revive A (its clock anomaly is repaired) and drive it through the signing gate: the
	// RELEASED spend must block it even though the transaction itself is no longer expired.
	e.client().TokenTransaction.UpdateOneID(entTxA.ID).
		SetClientCreatedTimestamp(time.Now()).
		SaveX(e.ctx)

	lockedTxA, err := ent.FetchAndLockTokenTransactionDataByHash(e.ctx, finalHashA)
	require.NoError(t, err)
	err = validateTokenTransactionForSigning(e.ctx, e.cfg, lockedTxA, txA)
	require.ErrorContains(t, err, "was released")
}

func TestPrepareTransfer_FrozenOwnerBlocksAllowanceSpend(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x18)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	// An active freeze on the owner+token pair blocks delegated spends the same as owner spends:
	// the spender's grant must not become a freeze bypass.
	e.client().TokenFreeze.Create().
		SetStatus(st.TokenFreezeStatusFrozen).
		SetOwnerPublicKey(e.ownerKey.Public()).
		SetTokenCreateID(e.tokenCreate.ID).
		SetWalletProvidedFreezeTimestamp(uint64(time.Now().UnixMilli())).
		SetIssuerSignature(e.fixtures.RandomBytes(64)).
		SaveX(e.ctx)

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, "at least one input is frozen")
}

// TestSignTransfer_AllowanceExpiredAfterPrepareBlocksSigning verifies expiry is enforced at the
// authoritative signing checkpoint, not just at prepare. A grant whose window closes between
// prepare and sign must not settle: expiry is a limit the owner signed, exactly like revocation.
func TestSignTransfer_AllowanceExpiredAfterPrepareBlocksSigning(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x21)
	activeAllowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	recipient := e.fixtures.GeneratePrivateKey().Public()
	signHandler := NewInternalSignTokenHandler(e.cfg)

	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, activeAllowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(txProto, sigs, keyshareIDs))

	// Repoint the reservation at a grant whose window has already closed, standing in for the
	// same grant expiring between prepare and sign.
	expired := e.createExpiredAllowance(allowanceSpendOpts{
		perTxCap: 1_000, totalLimit: 10_000, status: st.TokenAllowanceStatusExhausted,
	})
	spends := e.spendsFor(activeAllowance)
	require.Len(t, spends, 1)
	e.client().TokenAllowanceSpend.DeleteOneID(spends[0].ID).ExecX(e.ctx)
	e.client().TokenAllowanceSpend.Create().
		SetTokenAllowanceID(expired.ID).
		SetTokenTransaction(spends[0].Edges.TokenTransaction).
		SetMeteredAmount(u128(100)).
		SetStatus(st.TokenAllowanceSpendStatusReserved).
		SaveX(e.ctx)

	lockedTx, err := ent.FetchAndLockTokenTransactionData(e.ctx, txProto)
	require.NoError(t, err)
	finalHash, err := utils.HashTokenTransaction(txProto, false)
	require.NoError(t, err)
	_, err = signHandler.SignAndPersistTokenTransaction(e.ctx, lockedTx, txProto, finalHash, nil)
	require.ErrorContains(t, err, tokens.ErrAllowanceExpired)
}

// TestExchangeRevocationSecrets_BlocksWhenAllowanceExpired verifies the pre-reveal checkpoint
// rejects an expired allowance. Without it, a transaction signed before expiry could still reveal
// revocation secrets and finalize afterward.
func TestExchangeRevocationSecrets_BlocksWhenAllowanceExpired(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x22)
	allowance := e.createExpiredAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)

	var operatorPubKeys []keys.Public
	for _, op := range e.cfg.SigningOperatorMap {
		operatorPubKeys = append(operatorPubKeys, op.IdentityPublicKey)
	}
	result := e.fixtures.CreateTransferTransactionWithProto(
		e.tokenCreate,
		inputs,
		[]entfixtures.OutputSpec{{Amount: big.NewInt(100)}},
		entfixtures.TransferTransactionOpts{
			OperatorPublicKeys: operatorPubKeys,
			Status:             st.TokenTransactionStatusSigned,
		},
	)

	e.client().TokenAllowanceSpend.Create().
		SetTokenAllowanceID(allowance.ID).
		SetTokenTransaction(result.Transaction).
		SetMeteredAmount(u128(100)).
		SetStatus(st.TokenAllowanceSpendStatusReserved).
		SaveX(e.ctx)
	outputsToSpend := make([]*tokeninternalpb.OutputToSpend, len(inputs))
	for i, input := range inputs {
		outputsToSpend[i] = &tokeninternalpb.OutputToSpend{
			CreatedTokenTransactionHash: input.CreatedTransactionFinalizedHash,
			CreatedTokenTransactionVout: uint32(input.CreatedTransactionOutputVout),
			SpentTokenTransactionVout:   uint32(i),
		}
	}

	signHandler := NewInternalSignTokenHandler(e.cfg)
	_, err := signHandler.ExchangeRevocationSecretsShares(e.ctx, &tokeninternalpb.ExchangeRevocationSecretsSharesRequest{
		FinalTokenTransaction:     result.Proto,
		FinalTokenTransactionHash: result.Hash,
		OperatorIdentityPublicKey: e.cfg.IdentityPublicKey().Serialize(),
		OperatorShares: []*tokeninternalpb.OperatorRevocationShares{
			{OperatorIdentityPublicKey: e.cfg.IdentityPublicKey().Serialize()},
		},
		OutputsToSpend: outputsToSpend,
	})
	require.ErrorContains(t, err, tokens.ErrAllowanceExpired)
}

// TestExchangeRevocationSecrets_ReleasedSpendReportsReleasedNotRevoked verifies a released
// reservation is reported as released. Conflating it with revocation sends on-call after an owner
// action that never happened.
func TestExchangeRevocationSecrets_ReleasedSpendReportsReleasedNotRevoked(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x23)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)

	var operatorPubKeys []keys.Public
	for _, op := range e.cfg.SigningOperatorMap {
		operatorPubKeys = append(operatorPubKeys, op.IdentityPublicKey)
	}
	result := e.fixtures.CreateTransferTransactionWithProto(
		e.tokenCreate,
		inputs,
		[]entfixtures.OutputSpec{{Amount: big.NewInt(100)}},
		entfixtures.TransferTransactionOpts{
			OperatorPublicKeys: operatorPubKeys,
			Status:             st.TokenTransactionStatusSigned,
		},
	)

	// The allowance stays ACTIVE; only the reservation was returned.
	e.client().TokenAllowanceSpend.Create().
		SetTokenAllowanceID(allowance.ID).
		SetTokenTransaction(result.Transaction).
		SetMeteredAmount(u128(100)).
		SetStatus(st.TokenAllowanceSpendStatusReleased).
		SaveX(e.ctx)

	outputsToSpend := make([]*tokeninternalpb.OutputToSpend, len(inputs))
	for i, input := range inputs {
		outputsToSpend[i] = &tokeninternalpb.OutputToSpend{
			CreatedTokenTransactionHash: input.CreatedTransactionFinalizedHash,
			CreatedTokenTransactionVout: uint32(input.CreatedTransactionOutputVout),
			SpentTokenTransactionVout:   uint32(i),
		}
	}

	signHandler := NewInternalSignTokenHandler(e.cfg)
	_, err := signHandler.ExchangeRevocationSecretsShares(e.ctx, &tokeninternalpb.ExchangeRevocationSecretsSharesRequest{
		FinalTokenTransaction:     result.Proto,
		FinalTokenTransactionHash: result.Hash,
		OperatorIdentityPublicKey: e.cfg.IdentityPublicKey().Serialize(),
		OperatorShares: []*tokeninternalpb.OperatorRevocationShares{
			{OperatorIdentityPublicKey: e.cfg.IdentityPublicKey().Serialize()},
		},
		OutputsToSpend: outputsToSpend,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "was released")
	assert.NotContains(t, err.Error(), "revoked allowance cannot exchange revocation secrets")
}

// TestPrepareTransfer_UnknownAllowanceStatusFailsClosed verifies an unrecognized status is
// rejected rather than treated as spendable. A status this build does not know cannot be assumed
// permissive - a future EXPIRED or SUSPENDED value must not become a spend path on an old binary.
func TestPrepareTransfer_UnknownAllowanceStatusFailsClosed(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x24)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	// The status column is a varchar, so a newer operator can write a value this build has no
	// constant for. Ent rejects unknown enum values on write, so the row is aged through SQL to
	// reach the state an older binary would actually read.
	//nolint:forbidigo // ent's enum validator cannot express a value this build does not know.
	_, err := e.client().ExecContext(e.ctx,
		`UPDATE token_allowances SET status = 'SUSPENDED' WHERE id = $1`, allowance.ID)
	require.NoError(t, err)

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err = e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, "unrecognized")
}

// TestPrepareTransfer_MetersEveryDistinctRecipient verifies the metering loop sums across
// multiple distinct non-owner recipients rather than counting only one of them.
func TestPrepareTransfer_MetersEveryDistinctRecipient(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x25)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipientA := e.fixtures.GeneratePrivateKey().Public()
	recipientB := e.fixtures.GeneratePrivateKey().Public()
	recipientC := e.fixtures.GeneratePrivateKey().Public()

	// Three distinct recipients plus change back to the owner: only the 70 leaving the owner
	// is metered.
	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{
		{recipientA, 20},
		{recipientB, 30},
		{recipientC, 20},
		{e.ownerKey.Public(), 30},
	})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.prepare(txProto, sigs, keyshareIDs))

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(70), row.SpentAmount)
}

// TestPrepareTransfer_MultiRecipientTotalEnforcedAgainstPerTxCap verifies the per-transaction cap
// is applied to the summed recipient total. Metering only the largest single output would let a
// transaction split its way past the cap.
func TestPrepareTransfer_MultiRecipientTotalEnforcedAgainstPerTxCap(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x26)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 50, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 90)
	recipientA := e.fixtures.GeneratePrivateKey().Public()
	recipientB := e.fixtures.GeneratePrivateKey().Public()

	// Each output is under the 50 cap; together they are 90 and must be rejected.
	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{
		{recipientA, 45},
		{recipientB, 45},
	})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.prepare(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, tokens.ErrAllowanceExceedsPerTxCap)
}

// signSinglePhase drives the one-shot V3 sign entry point, which creates SIGNED entities and
// meters the delegated spend in a single database transaction.
func (e *allowanceSpendEnv) signSinglePhase(txProto *tokenpb.TokenTransaction, sigs []*tokenpb.SignatureWithIndex, keyshareIDs []string) error {
	e.t.Helper()
	_, err := NewSignTokenTransactionHandler(e.cfg).SignTokenTransaction(e.ctx, &tokeninternalpb.SignTokenTransactionRequest{
		FinalTokenTransaction:      txProto,
		TokenTransactionSignatures: sigs,
		KeyshareIds:                keyshareIDs,
		CoordinatorPublicKey:       e.coordinatorPubKey,
	})
	return err
}

// TestSignSinglePhase_AllowanceMetersBudget verifies the single-phase V3 sign path meters the
// delegated spend, so a transaction that never goes through prepare cannot settle unmetered.
func TestSignSinglePhase_AllowanceMetersBudget(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x27)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 70}, {e.ownerKey.Public(), 30}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)
	require.NoError(t, e.signSinglePhase(txProto, sigs, keyshareIDs))

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(70), row.SpentAmount, "change back to the owner is not metered")
	spends := e.spendsFor(allowance)
	require.Len(t, spends, 1)
	assert.Equal(t, st.TokenAllowanceSpendStatusReserved, spends[0].Status)
}

// TestSignSinglePhase_AllowanceRejectsOverBudget verifies a metering failure on the single-phase
// path rolls the whole sign back: no operator signature, and no reservation left behind.
func TestSignSinglePhase_AllowanceRejectsOverBudget(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x28)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 50})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.signSinglePhase(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, tokens.ErrAllowanceBudgetExhausted)

	row := e.reloadAllowance(allowance)
	assert.Equal(t, u128(0), row.SpentAmount)
	assert.Empty(t, e.spendsFor(allowance))
}

// TestSignSinglePhase_AllowanceRejectsExpiredAllowance verifies the single-phase path enforces
// the expiry window, matching the two-phase prepare hook.
func TestSignSinglePhase_AllowanceRejectsExpiredAllowance(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x29)
	allowance := e.createExpiredAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, allowance.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.signSinglePhase(txProto, sigs, keyshareIDs)
	require.ErrorContains(t, err, tokens.ErrAllowanceExpired)
	assert.Empty(t, e.spendsFor(allowance))
}

// TestPrepareTransfer_ExhaustedAllowanceCannotSpendFreedBudgetAlongsideReplacement verifies a
// superseded grant cannot authorize value just because its spend lands exactly back on the limit.
// The owner replaced it, so any spend against it is out of policy - not only the ones that would
// flip it back to ACTIVE.
func TestPrepareTransfer_ExhaustedAllowanceCannotSpendFreedBudgetAlongsideReplacement(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x2A)
	recipient := e.fixtures.GeneratePrivateKey().Public()

	// The old grant is EXHAUSTED, with its whole budget held by a transaction that has since
	// been cancelled, so the budget is releasable.
	old := e.createAllowance(allowanceSpendOpts{
		perTxCap: 100, totalLimit: 100, spent: 100, status: st.TokenAllowanceStatusExhausted,
	})
	cancelledTx := e.client().TokenTransaction.Create().
		SetPartialTokenTransactionHash(e.fixtures.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(e.fixtures.RandomBytes(32)).
		SetStatus(st.TokenTransactionStatusStartedCancelled).
		SaveX(e.ctx)
	e.client().TokenAllowanceSpend.Create().
		SetTokenAllowanceID(old.ID).
		SetTokenTransaction(cancelledTx).
		SetMeteredAmount(u128(100)).
		SetStatus(st.TokenAllowanceSpendStatusReserved).
		SaveX(e.ctx)

	// The owner then issued a replacement grant, which is the live one.
	replacement := e.createAllowance(allowanceSpendOpts{perTxCap: 100, totalLimit: 100})
	require.Equal(t, st.TokenAllowanceStatusActive, replacement.Status)

	// Spending the freed budget in full against the OLD grant lands back on EXHAUSTED, so it
	// never trips a guard that only fires on the transition back to ACTIVE.
	inputs := e.mintInputs(e.ownerKey.Public(), 100)
	txProto, keyshareIDs := e.buildTransferProto(inputs, []transferOut{{recipient, 100}})
	sigs := e.allowanceSignatures(txProto, old.AllowanceID, e.spenderKey, e.spenderKey.Public(), 1)

	err := e.prepare(txProto, sigs, keyshareIDs)
	require.Error(t, err, "a superseded allowance must not authorize a spend while its replacement is active")
	require.ErrorContains(t, err, "another active allowance exists")
}

// stampSpentOwnershipSignatures gives each input a valid-length signature. The coordinator's
// exchange path validates signature length while assembling outputs-to-spend, which runs before
// the allowance checkpoint under test.
func (e *allowanceSpendEnv) stampSpentOwnershipSignatures(inputs []*ent.TokenOutput) {
	e.t.Helper()
	for _, input := range inputs {
		e.client().TokenOutput.UpdateOneID(input.ID).
			SetSpentOwnershipSignature(e.fixtures.RandomBytes(64)).
			SaveX(e.ctx)
	}
}

// TestCoordinatorExchangeRevocationSecrets_BlocksWhenAllowanceRevoked covers the coordinator-side
// pre-reveal checkpoint. It is a distinct code path from the peer-side exchange: the coordinator
// resolves the transaction by hash rather than by ent, so a gap here would let a revoked grant
// reveal on the coordinator even while every peer rejects it.
func TestCoordinatorExchangeRevocationSecrets_BlocksWhenAllowanceRevoked(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x2B)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)

	var operatorPubKeys []keys.Public
	for _, op := range e.cfg.SigningOperatorMap {
		operatorPubKeys = append(operatorPubKeys, op.IdentityPublicKey)
	}
	result := e.fixtures.CreateTransferTransactionWithProto(
		e.tokenCreate,
		inputs,
		[]entfixtures.OutputSpec{{Amount: big.NewInt(100)}},
		entfixtures.TransferTransactionOpts{
			OperatorPublicKeys: operatorPubKeys,
			Status:             st.TokenTransactionStatusSigned,
		},
	)

	e.stampSpentOwnershipSignatures(inputs)
	e.client().TokenAllowanceSpend.Create().
		SetTokenAllowanceID(allowance.ID).
		SetTokenTransaction(result.Transaction).
		SetMeteredAmount(u128(100)).
		SetStatus(st.TokenAllowanceSpendStatusReserved).
		SaveX(e.ctx)
	e.client().TokenAllowance.UpdateOneID(allowance.ID).
		SetStatus(st.TokenAllowanceStatusRevoked).
		SetOwnerProvidedRevokeTimestamp(uint64(time.Now().UnixMilli())).
		SetRevokeSignature(e.fixtures.RandomBytes(64)).
		SaveX(e.ctx)

	_, err := NewSignTokenHandler(e.cfg).ExchangeRevocationSecretsAndFinalizeIfPossible(
		e.ctx, result.Proto, nil, result.Hash)
	require.ErrorContains(t, err, "revoked allowance cannot exchange revocation secrets")
}

// TestCoordinatorExchangeRevocationSecrets_ReleasedSpendReportsReleasedNotRevoked verifies the
// coordinator reports a returned reservation as released rather than as an owner revocation.
func TestCoordinatorExchangeRevocationSecrets_ReleasedSpendReportsReleasedNotRevoked(t *testing.T) {
	t.Parallel()
	e := newAllowanceSpendEnv(t, 0x2C)
	allowance := e.createAllowance(allowanceSpendOpts{perTxCap: 1_000, totalLimit: 10_000})
	inputs := e.mintInputs(e.ownerKey.Public(), 100)

	var operatorPubKeys []keys.Public
	for _, op := range e.cfg.SigningOperatorMap {
		operatorPubKeys = append(operatorPubKeys, op.IdentityPublicKey)
	}
	result := e.fixtures.CreateTransferTransactionWithProto(
		e.tokenCreate,
		inputs,
		[]entfixtures.OutputSpec{{Amount: big.NewInt(100)}},
		entfixtures.TransferTransactionOpts{
			OperatorPublicKeys: operatorPubKeys,
			Status:             st.TokenTransactionStatusSigned,
		},
	)

	// The allowance stays ACTIVE; only the reservation was returned.
	e.stampSpentOwnershipSignatures(inputs)
	e.client().TokenAllowanceSpend.Create().
		SetTokenAllowanceID(allowance.ID).
		SetTokenTransaction(result.Transaction).
		SetMeteredAmount(u128(100)).
		SetStatus(st.TokenAllowanceSpendStatusReleased).
		SaveX(e.ctx)

	_, err := NewSignTokenHandler(e.cfg).ExchangeRevocationSecretsAndFinalizeIfPossible(
		e.ctx, result.Proto, nil, result.Hash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "was released")
	assert.NotContains(t, err.Error(), "revoked allowance cannot exchange revocation secrets")
}
