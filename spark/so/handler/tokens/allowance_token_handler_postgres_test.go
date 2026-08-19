package tokens

import (
	"context"
	cryptorand "crypto/rand"
	stderrors "errors"
	"math/big"
	mathrand "math/rand/v2"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/entexample"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenallowance"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	allowanceTestRng    = mathrand.NewChaCha8([32]byte{0xA1, 0x10, 0x0A, 0xCE})
	allowanceOwnerKey   = keys.MustGeneratePrivateKeyFromRand(allowanceTestRng)
	allowanceSpenderKey = keys.MustGeneratePrivateKeyFromRand(allowanceTestRng)
	allowanceWrongKey   = keys.MustGeneratePrivateKeyFromRand(allowanceTestRng)
)

// u128 encodes v as a 16-byte big-endian uint128, the on-wire width the allowance caps use.
func u128(v uint64) []byte {
	return new(big.Int).SetUint64(v).FillBytes(make([]byte, 16))
}

func createAllowanceTestTokenCreate(t *testing.T, ctx context.Context, client *ent.Client) *ent.TokenCreate {
	t.Helper()
	// entfixtures seeds its RNG deterministically, so every fixture instance would otherwise
	// mint the same token_identifier; supply a unique one so a test can create several tokens.
	tokenIdentifier := make([]byte, 32)
	_, err := cryptorand.Read(tokenIdentifier)
	require.NoError(t, err)
	fixtures := entfixtures.New(t, ctx, client)
	_, tokenCreate := fixtures.CreateTokenCreateWithOpts(btcnetwork.Regtest, entfixtures.TokenCreateOpts{
		TokenIdentifier: tokenIdentifier,
	})
	return tokenCreate
}

func newAllowancePayload(tokenCreate *ent.TokenCreate, ownerPub, spenderPub keys.Public, allowanceID uuid.UUID, ownerTimestamp uint64) *tokenpb.TokenAllowancePayload {
	return &tokenpb.TokenAllowancePayload{
		Version:                1,
		AllowanceId:            allowanceID[:],
		OwnerPublicKey:         ownerPub.Serialize(),
		SpenderPublicKey:       spenderPub.Serialize(),
		TokenIdentifier:        tokenCreate.TokenIdentifier,
		PerTransactionCap:      u128(10_000),
		TotalLimit:             u128(100_000),
		ExpiryTime:             timestamppb.New(time.Now().Add(24 * time.Hour)),
		Network:                sparkpb.Network_REGTEST,
		OwnerProvidedTimestamp: ownerTimestamp,
	}
}

func signCreateAllowance(t *testing.T, payload *tokenpb.TokenAllowancePayload, key keys.Private) []byte {
	t.Helper()
	hash, err := utils.HashCreateTokenAllowancePayload(payload)
	require.NoError(t, err)
	return ecdsa.Sign(key.ToBTCEC(), hash).Serialize()
}

type discardAllowanceGossipSender struct{}

func (s *discardAllowanceGossipSender) CreateCommitAndSendGossipMessage(
	_ context.Context,
	_ *pbgossip.GossipMessage,
	_ []string,
) (*ent.Gossip, error) {
	return nil, nil
}

type cancelingAllowanceGossipSender struct {
	cancel context.CancelFunc
}

func (s *cancelingAllowanceGossipSender) CreateCommitAndSendGossipMessage(
	_ context.Context,
	_ *pbgossip.GossipMessage,
	_ []string,
) (*ent.Gossip, error) {
	s.cancel()
	return nil, nil
}

// setupAllowanceTest returns a handler with allowances enabled and a
// single-operator consensus engine.
func setupAllowanceTest(t *testing.T) (context.Context, *db.TestContext, *so.Config, *AllowanceTokenHandler) {
	t.Helper()
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenAllowancesEnabled: 1.0,
	}))
	self := cfg.Identifier
	cfg.SigningOperatorMap = map[string]*so.SigningOperator{
		self: {Identifier: self, IdentityPublicKey: cfg.IdentityPublicKey()},
	}
	engine := consensus.NewTwoPCEngine(cfg, &discardAllowanceGossipSender{}, db.NewDefaultSessionFactory(tc.Client))
	ctx = consensus.InjectEngine(ctx, engine)
	return ctx, tc, cfg, NewAllowanceTokenHandler(cfg)
}

// --- Create tests ---

func TestCreateTokenAllowance_Success(t *testing.T) {
	ctx, tc, _, handler := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)

	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
	req := &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
	}

	resp, err := handler.CreateTokenAllowance(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.GetAllowance())
	assert.Equal(t, tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_ACTIVE, resp.GetAllowance().GetStatus())
	assert.Equal(t, make([]byte, 16), resp.GetAllowance().GetSpentAmount())

	row, err := tc.Client.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(t.Context())
	require.NoError(t, err)
	assert.Equal(t, schematype.TokenAllowanceStatusActive, row.Status)
	assert.Equal(t, make([]byte, 16), row.SpentAmount)
	assert.True(t, allowanceOwnerKey.Public().Equals(row.OwnerPublicKey))
	assert.True(t, allowanceSpenderKey.Public().Equals(row.SpenderPublicKey))
	assert.Equal(t, tokenCreate.ID, row.TokenCreateID)
}

func TestCreateTokenAllowance_ReadBackFailureReturnsCommittedSuccess(t *testing.T) {
	ctx, tc, cfg, _ := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	requestCtx, cancel := context.WithCancel(ctx)
	engine := consensus.NewTwoPCEngine(cfg, &cancelingAllowanceGossipSender{cancel: cancel}, db.NewDefaultSessionFactory(tc.Client))
	requestCtx = consensus.InjectEngine(requestCtx, engine)

	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
	resp, err := NewAllowanceTokenHandler(cfg).CreateTokenAllowance(requestCtx, &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Nil(t, resp.GetAllowance())
	row, err := tc.Client.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(t.Context())
	require.NoError(t, err)
	assert.Equal(t, schematype.TokenAllowanceStatusActive, row.Status)
}

func TestCreateTokenAllowance_TruncatesExpiryToSignedSeconds(t *testing.T) {
	ctx, tc, _, handler := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)

	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
	payload.ExpiryTime = timestamppb.New(time.Now().Add(24 * time.Hour).Truncate(time.Second).Add(987654321 * time.Nanosecond))
	expectedExpiry := time.Unix(payload.GetExpiryTime().GetSeconds(), 0)
	req := &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
	}

	resp, err := handler.CreateTokenAllowance(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp.GetAllowance())
	assert.Equal(t, tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_ACTIVE, resp.GetAllowance().GetStatus())
	servedExpiry := resp.GetAllowance().GetAllowancePayload().GetExpiryTime().AsTime()
	assert.True(t, expectedExpiry.Equal(servedExpiry), "served expiry %s must equal signed expiry %s", servedExpiry, expectedExpiry)

	row, err := tc.Client.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(t.Context())
	require.NoError(t, err)
	// Compare instants, not wall-clock structs: the column is timestamptz, so
	// the driver returns UTC while time.Unix builds a Local-zone value.
	assert.True(t, expectedExpiry.Equal(row.ExpiryTime), "stored expiry %s must equal signed expiry %s", row.ExpiryTime, expectedExpiry)

	queryResp, err := handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{
		OwnerPublicKey: allowanceOwnerKey.Public().Serialize(),
	})
	require.NoError(t, err)
	require.Len(t, queryResp.GetAllowances(), 1)
	queriedExpiry := queryResp.GetAllowances()[0].GetAllowancePayload().GetExpiryTime().AsTime()
	assert.True(t, expectedExpiry.Equal(queriedExpiry), "queried expiry %s must equal signed expiry %s", queriedExpiry, expectedExpiry)
}

func TestCreateTokenAllowance_RejectsInvalidOwnerSignature(t *testing.T) {
	ctx, tc, _, handler := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)

	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
	req := &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: payload,
		// Signed by the wrong key.
		OwnerSignature: signCreateAllowance(t, payload, allowanceWrongKey),
	}

	resp, err := handler.CreateTokenAllowance(ctx, req)
	require.Error(t, err)
	require.Nil(t, resp)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "invalid owner signature")
	flowCount, err := tc.Client.FlowExecution.Query().
		Where(
			flowexecution.RoleEQ(schematype.FlowExecutionRoleCoordinator),
			flowexecution.OpTypeEQ(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CREATE_TOKEN_ALLOWANCE)),
		).
		Count(t.Context())
	require.NoError(t, err)
	assert.Zero(t, flowCount)
}

// TestCreateTokenAllowance_RejectsStaleTimestampAtPublicEdge verifies freshness is still enforced
// at the public coordinator edge (the internal replication path deliberately skips it).
func TestCreateTokenAllowance_RejectsStaleTimestampAtPublicEdge(t *testing.T) {
	ctx, tc, _, handler := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)

	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Minute))
	req := &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
	}

	resp, err := handler.CreateTokenAllowance(ctx, req)
	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "too old")
}

func TestCreateTokenAllowance_RejectsWhenKnobDisabled(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	// No allowance knob injected -> feature disabled.
	handler := NewAllowanceTokenHandler(cfg)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)

	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
	req := &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
	}

	resp, err := handler.CreateTokenAllowance(ctx, req)
	require.Error(t, err)
	require.Nil(t, resp)
	assert.Equal(t, codes.Unimplemented, status.Code(err))
}

func TestCreateTokenAllowance_IdempotentSamePayload(t *testing.T) {
	ctx, tc, _, handler := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)

	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
	req := &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
	}

	_, err := handler.CreateTokenAllowance(ctx, req)
	require.NoError(t, err)

	currentSpentAmount := u128(42_000)
	_, err = tc.Client.TokenAllowance.Update().
		Where(tokenallowance.AllowanceID(allowanceID)).
		SetSpentAmount(currentSpentAmount).
		SetStatus(schematype.TokenAllowanceStatusExhausted).
		Save(t.Context())
	require.NoError(t, err)

	// Replaying the identical signed grant is a no-op success that returns the current row.
	resp, err := handler.CreateTokenAllowance(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.GetAllowance())
	assert.Equal(t, tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_EXHAUSTED, resp.GetAllowance().GetStatus())
	assert.Equal(t, currentSpentAmount, resp.GetAllowance().GetSpentAmount())

	count, err := tc.Client.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCreateTokenAllowance_RejectsSecondActiveForSameOwnerSpenderToken(t *testing.T) {
	ctx, tc, _, handler := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)

	firstID := uuid.New()
	firstPayload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), firstID, recentTimestamp(10*time.Second))
	firstReq := &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: firstPayload,
		OwnerSignature:   signCreateAllowance(t, firstPayload, allowanceOwnerKey),
	}
	_, err := handler.CreateTokenAllowance(ctx, firstReq)
	require.NoError(t, err)

	// A different allowance_id but same (owner, spender, token) must collide on the partial index.
	secondID := uuid.New()
	secondPayload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), secondID, recentTimestamp(9*time.Second))
	secondReq := &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: secondPayload,
		OwnerSignature:   signCreateAllowance(t, secondPayload, allowanceOwnerKey),
	}
	resp, err := handler.CreateTokenAllowance(ctx, secondReq)
	require.Error(t, err)
	require.Nil(t, resp)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
	assert.Contains(t, err.Error(), "an active allowance already exists for this owner, spender, and token")
}

func TestCreateTokenAllowance_PreflightRejectsKnownConflictsWithoutCoordinatorFlow(t *testing.T) {
	assertRejectedWithoutCoordinatorFlow := func(
		t *testing.T,
		ctx context.Context,
		req *tokenpb.CreateTokenAllowanceRequest,
		handler *AllowanceTokenHandler,
		expectedCode codes.Code,
	) {
		t.Helper()
		resp, err := handler.CreateTokenAllowance(ctx, req)
		require.Error(t, err)
		require.Nil(t, resp)
		assert.Equal(t, expectedCode, status.Code(err))

		client, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		flowCount, err := client.FlowExecution.Query().
			Where(
				flowexecution.RoleEQ(schematype.FlowExecutionRoleCoordinator),
				flowexecution.OpTypeEQ(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CREATE_TOKEN_ALLOWANCE)),
			).
			Count(ctx)
		require.NoError(t, err)
		assert.Zero(t, flowCount)
	}

	t.Run("allowance ID has a different statement", func(t *testing.T) {
		ctx, tc, cfg, handler := setupAllowanceTest(t)
		tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
		allowanceID := uuid.New()
		original := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(20*time.Second))
		require.NoError(t, ValidateAndApplyCreateAllowance(ctx, cfg, original, signCreateAllowance(t, original, allowanceOwnerKey)))

		conflicting := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceWrongKey.Public(), allowanceID, recentTimestamp(10*time.Second))
		assertRejectedWithoutCoordinatorFlow(t, ctx, &tokenpb.CreateTokenAllowanceRequest{
			AllowancePayload: conflicting,
			OwnerSignature:   signCreateAllowance(t, conflicting, allowanceOwnerKey),
		}, handler, codes.FailedPrecondition)
	})

	t.Run("allowance ID is revoked", func(t *testing.T) {
		ctx, tc, cfg, handler := setupAllowanceTest(t)
		tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
		allowanceID := uuid.New()
		payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
		require.NoError(t, ValidateAndApplyCreateAllowance(ctx, cfg, payload, signCreateAllowance(t, payload, allowanceOwnerKey)))
		client, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		_, err = client.TokenAllowance.Update().
			Where(tokenallowance.AllowanceID(allowanceID)).
			SetStatus(schematype.TokenAllowanceStatusRevoked).
			Save(ctx)
		require.NoError(t, err)

		assertRejectedWithoutCoordinatorFlow(t, ctx, &tokenpb.CreateTokenAllowanceRequest{
			AllowancePayload: payload,
			OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
		}, handler, codes.FailedPrecondition)
	})

	t.Run("active owner spender token tuple exists", func(t *testing.T) {
		ctx, tc, cfg, handler := setupAllowanceTest(t)
		tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
		original := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), uuid.New(), recentTimestamp(20*time.Second))
		require.NoError(t, ValidateAndApplyCreateAllowance(ctx, cfg, original, signCreateAllowance(t, original, allowanceOwnerKey)))

		conflicting := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), uuid.New(), recentTimestamp(10*time.Second))
		assertRejectedWithoutCoordinatorFlow(t, ctx, &tokenpb.CreateTokenAllowanceRequest{
			AllowancePayload: conflicting,
			OwnerSignature:   signCreateAllowance(t, conflicting, allowanceOwnerKey),
		}, handler, codes.AlreadyExists)
	})
}

func TestAllowanceCreateConstraintErrorMessages(t *testing.T) {
	allowanceID := uuid.New()
	tests := []struct {
		name              string
		constraint        string
		expectedMessage   string
		unexpectedMessage string
	}{
		{
			name:              "allowance ID",
			constraint:        `duplicate key value violates unique constraint "token_allowances_allowance_id_key"`,
			expectedMessage:   "allowance ID " + allowanceID.String() + " already exists",
			unexpectedMessage: "active allowance already exists",
		},
		{
			name:              "active owner spender token tuple",
			constraint:        `duplicate key value violates unique constraint "tokenallowance_unique_active_grant"`,
			expectedMessage:   "an active allowance already exists for this owner, spender, and token",
			unexpectedMessage: "allowance ID " + allowanceID.String() + " already exists",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := duplicateAllowanceCreateError(allowanceID, stderrors.New(test.constraint))
			assert.Equal(t, codes.AlreadyExists, status.Code(err))
			assert.Contains(t, err.Error(), test.expectedMessage)
			assert.NotContains(t, err.Error(), test.unexpectedMessage)
		})
	}
}

// TestCreateTokenAllowance_RejectsOverOwnerQuota: the N+1th ACTIVE allowance
// for one owner fails closed at the public edge; revoking an allowance frees
// quota (only ACTIVE rows count).
// TestCreateTokenAllowance_AtCapAllowsIdenticalRetry: an owner sitting at the
// per-owner cap must still be able to retry a grant that was already admitted.
// The retry is what repairs operators that missed the original, so rejecting it
// on quota would strand them permanently.
func TestCreateTokenAllowance_AtCapAllowsIdenticalRetry(t *testing.T) {
	ctx, tc, _, handler := setupAllowanceTest(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenAllowancesEnabled:           1.0,
		knobs.KnobTokenMaxActiveAllowancesPerOwner: 1,
	}))
	token1 := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	token2 := createAllowanceTestTokenCreate(t, ctx, tc.Client)

	allowanceID := uuid.New()
	payload := newAllowancePayload(token1, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(20*time.Second))
	req := &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
	}
	_, err := handler.CreateTokenAllowance(ctx, req)
	require.NoError(t, err)

	// The owner is now at the cap. The identical signed request is a no-op success.
	_, err = handler.CreateTokenAllowance(ctx, req)
	require.NoError(t, err, "identical retry at the cap must be admitted as a replay")

	count, err := tc.Client.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, count, "the replay must not create a second row")

	// A genuinely new grant is still refused while at the cap.
	otherPayload := newAllowancePayload(token2, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), uuid.New(), recentTimestamp(15*time.Second))
	_, err = handler.CreateTokenAllowance(ctx, &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: otherPayload,
		OwnerSignature:   signCreateAllowance(t, otherPayload, allowanceOwnerKey),
	})
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
}

func TestCreateTokenAllowance_RejectsOverOwnerQuota(t *testing.T) {
	ctx, tc, _, handler := setupAllowanceTest(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenAllowancesEnabled:           1.0,
		knobs.KnobTokenMaxActiveAllowancesPerOwner: 1,
	}))
	token1 := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	token2 := createAllowanceTestTokenCreate(t, ctx, tc.Client)

	createRequest := func(tokenCreate *ent.TokenCreate, allowanceID uuid.UUID, ageAgo time.Duration) *tokenpb.CreateTokenAllowanceRequest {
		payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(ageAgo))
		return &tokenpb.CreateTokenAllowanceRequest{
			AllowancePayload: payload,
			OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
		}
	}
	create := func(req *tokenpb.CreateTokenAllowanceRequest) error {
		_, err := handler.CreateTokenAllowance(ctx, req)
		return err
	}

	firstID := uuid.New()
	firstRequest := createRequest(token1, firstID, 20*time.Second)
	require.NoError(t, create(firstRequest))
	require.NoError(t, create(firstRequest), "an identical retry must not consume another quota slot")

	// A second ACTIVE allowance (different token, so no uniqueness collision)
	// exceeds the per-owner quota.
	err := create(createRequest(token2, uuid.New(), 15*time.Second))
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	assert.Contains(t, err.Error(), "per-owner cap")
}

// --- Query tests ---

func TestQueryTokenAllowancesGeneratedFixtureDefaultsToCommitted(t *testing.T) {
	ctx, tc, _, handler := setupAllowanceTest(t)
	allowance := entexample.NewTokenAllowanceExample(t, tc.Client).MustExec(ctx)
	require.Nil(t, allowance.FlowExecutionID)

	resp, err := handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{
		OwnerPublicKey: allowance.OwnerPublicKey.Serialize(),
	})
	require.NoError(t, err)
	require.Len(t, resp.GetAllowances(), 1)
	assert.Equal(t, allowance.AllowanceID[:], resp.GetAllowances()[0].GetAllowancePayload().GetAllowanceId())
}

func TestQueryTokenAllowances_FiltersByOwnerSpenderToken(t *testing.T) {
	ctx, tc, cfg, handler := setupAllowanceTest(t)
	token1 := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	token2 := createAllowanceTestTokenCreate(t, ctx, tc.Client)

	ownerA := keys.GeneratePrivateKey()
	ownerB := keys.GeneratePrivateKey()
	spenderX := keys.GeneratePrivateKey().Public()
	spenderY := keys.GeneratePrivateKey().Public()

	create := func(tokenCreate *ent.TokenCreate, owner keys.Private, spender keys.Public) uuid.UUID {
		id := uuid.New()
		payload := newAllowancePayload(tokenCreate, owner.Public(), spender, id, recentTimestamp(10*time.Second))
		req := &tokenpb.CreateTokenAllowanceRequest{
			AllowancePayload: payload,
			OwnerSignature:   signCreateAllowance(t, payload, owner),
		}
		_, err := handler.CreateTokenAllowance(ctx, req)
		require.NoError(t, err)
		return id
	}

	create(token1, ownerA, spenderX) // ownerA / spenderX / token1
	create(token1, ownerA, spenderY) // ownerA / spenderY / token1
	create(token2, ownerB, spenderX) // ownerB / spenderX / token2

	t.Run("by owner", func(t *testing.T) {
		resp, err := handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{
			OwnerPublicKey: ownerA.Public().Serialize(),
		})
		require.NoError(t, err)
		assert.Len(t, resp.GetAllowances(), 2)
		for _, a := range resp.GetAllowances() {
			assert.Equal(t, ownerA.Public().Serialize(), a.GetAllowancePayload().GetOwnerPublicKey())
			assert.Equal(t, tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_ACTIVE, a.GetStatus())
			assert.Equal(t, make([]byte, 16), a.GetSpentAmount())
		}
	})

	t.Run("by spender", func(t *testing.T) {
		resp, err := handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{
			SpenderPublicKey: spenderX.Serialize(),
		})
		require.NoError(t, err)
		assert.Len(t, resp.GetAllowances(), 2)
	})

	t.Run("by owner and token", func(t *testing.T) {
		resp, err := handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{
			OwnerPublicKey:  ownerA.Public().Serialize(),
			TokenIdentifier: token1.TokenIdentifier,
		})
		require.NoError(t, err)
		assert.Len(t, resp.GetAllowances(), 2)
	})

	t.Run("by token", func(t *testing.T) {
		resp, err := handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{
			TokenIdentifier: token2.TokenIdentifier,
		})
		require.NoError(t, err)
		assert.Len(t, resp.GetAllowances(), 1)
		assert.Equal(t, ownerB.Public().Serialize(), resp.GetAllowances()[0].GetAllowancePayload().GetOwnerPublicKey())
	})

	t.Run("authz allows the owner and rejects a non-party", func(t *testing.T) {
		cfg.AuthzEnforced = true
		defer func() { cfg.AuthzEnforced = false }()

		ownerCtx := authn.InjectSessionForTests(ctx, ownerB.Public(), time.Now().Add(time.Hour).Unix())
		resp, err := handler.QueryTokenAllowances(ownerCtx, &tokenpb.QueryTokenAllowancesRequest{
			OwnerPublicKey: ownerB.Public().Serialize(),
		})
		require.NoError(t, err)
		assert.Len(t, resp.GetAllowances(), 1)

		strangerCtx := authn.InjectSessionForTests(ctx, allowanceWrongKey.Public(), time.Now().Add(time.Hour).Unix())
		_, err = handler.QueryTokenAllowances(strangerCtx, &tokenpb.QueryTokenAllowancesRequest{
			OwnerPublicKey: ownerB.Public().Serialize(),
		})
		require.Error(t, err)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	})
}

// TestQueryTokenAllowances_Paginates: results page in stable row-id order, the
// response offset is -1 once exhausted, oversized limits are clamped, and
// negative paging inputs are rejected.
func TestQueryTokenAllowances_Paginates(t *testing.T) {
	ctx, tc, _, handler := setupAllowanceTest(t)
	owner := keys.GeneratePrivateKey()

	for range 5 {
		tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
		payload := newAllowancePayload(tokenCreate, owner.Public(), allowanceSpenderKey.Public(), uuid.New(), recentTimestamp(10*time.Second))
		_, err := handler.CreateTokenAllowance(ctx, &tokenpb.CreateTokenAllowanceRequest{
			AllowancePayload: payload,
			OwnerSignature:   signCreateAllowance(t, payload, owner),
		})
		require.NoError(t, err)
	}
	ownerFilter := owner.Public().Serialize()

	// Walk pages of 2: 2 + 2 + 1, then the offset sentinel says done.
	seen := make(map[string]struct{})
	offset := int64(0)
	for i, expectedPageSize := range []int{2, 2, 1} {
		resp, err := handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{
			OwnerPublicKey: ownerFilter,
			Limit:          2,
			Offset:         offset,
		})
		require.NoError(t, err)
		require.Len(t, resp.GetAllowances(), expectedPageSize, "page %d", i)
		for _, a := range resp.GetAllowances() {
			seen[string(a.GetAllowancePayload().GetAllowanceId())] = struct{}{}
		}
		offset = resp.GetOffset()
	}
	require.Len(t, seen, 5, "pages must cover every allowance exactly once")
	require.EqualValues(t, -1, offset)

	// Unset limit falls back to the default page size and reports exhaustion; a
	// limit above the server cap is clamped, never honored.
	resp, err := handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{OwnerPublicKey: ownerFilter})
	require.NoError(t, err)
	require.Len(t, resp.GetAllowances(), 5)
	require.EqualValues(t, -1, resp.GetOffset())
	resp, err = handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{OwnerPublicKey: ownerFilter, Limit: 100000})
	require.NoError(t, err)
	require.Len(t, resp.GetAllowances(), 5)

	// Negative paging inputs are rejected.
	_, err = handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{OwnerPublicKey: ownerFilter, Limit: -1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{OwnerPublicKey: ownerFilter, Offset: -1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestQueryTokenAllowances_ExactMultiplePageEndsClean verifies that when the
// total number of allowances is an exact multiple of the page size, the final
// full page reports exhaustion (offset -1) instead of forcing an extra empty
// request.
func TestQueryTokenAllowances_ExactMultiplePageEndsClean(t *testing.T) {
	ctx, tc, _, handler := setupAllowanceTest(t)
	owner := keys.GeneratePrivateKey()

	for range 4 {
		tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
		payload := newAllowancePayload(tokenCreate, owner.Public(), allowanceSpenderKey.Public(), uuid.New(), recentTimestamp(10*time.Second))
		_, err := handler.CreateTokenAllowance(ctx, &tokenpb.CreateTokenAllowanceRequest{
			AllowancePayload: payload,
			OwnerSignature:   signCreateAllowance(t, payload, owner),
		})
		require.NoError(t, err)
	}
	ownerFilter := owner.Public().Serialize()

	// 4 allowances, page size 2: two full pages, then done - no empty third page.
	resp, err := handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{OwnerPublicKey: ownerFilter, Limit: 2, Offset: 0})
	require.NoError(t, err)
	require.Len(t, resp.GetAllowances(), 2)
	require.EqualValues(t, 2, resp.GetOffset())

	resp, err = handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{OwnerPublicKey: ownerFilter, Limit: 2, Offset: resp.GetOffset()})
	require.NoError(t, err)
	require.Len(t, resp.GetAllowances(), 2)
	require.EqualValues(t, -1, resp.GetOffset(), "final full page must report exhaustion, not force an empty request")
}

// TestQueryTokenAllowances_ReturnsVerifiableOwnerProof exercises the client
// verification flow server-side: a caller recomputes the create statement hash
// from the RETURNED payload and checks owner_signature against the returned
// owner key, proving the queried SO cannot fabricate or alter grant terms.
func TestQueryTokenAllowances_ReturnsVerifiableOwnerProof(t *testing.T) {
	ctx, tc, _, handler := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)

	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(20*time.Second))
	_, err := handler.CreateTokenAllowance(ctx, &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
	})
	require.NoError(t, err)

	resp, err := handler.QueryTokenAllowances(ctx, &tokenpb.QueryTokenAllowancesRequest{
		OwnerPublicKey: allowanceOwnerKey.Public().Serialize(),
	})
	require.NoError(t, err)
	require.Len(t, resp.GetAllowances(), 1)
	record := resp.GetAllowances()[0]

	// Everything below uses only the response, exactly as a client would.
	returnedOwner, err := keys.ParsePublicKey(record.GetAllowancePayload().GetOwnerPublicKey())
	require.NoError(t, err)
	statementHash, err := utils.HashCreateTokenAllowancePayload(record.GetAllowancePayload())
	require.NoError(t, err)
	require.NoError(t, utils.ValidateOwnershipSignature(record.GetOwnerSignature(), statementHash, returnedOwner))

	// A tampered policy field breaks verification: the proof binds the terms.
	record.GetAllowancePayload().OwnerProvidedTimestamp++
	tamperedHash, err := utils.HashCreateTokenAllowancePayload(record.GetAllowancePayload())
	require.NoError(t, err)
	require.Error(t, utils.ValidateOwnershipSignature(record.GetOwnerSignature(), tamperedHash, returnedOwner))
}
