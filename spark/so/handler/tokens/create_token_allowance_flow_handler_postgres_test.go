package tokens

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenallowance"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func createAllowancePrepareRequest(payload *tokenpb.TokenAllowancePayload, signature []byte) *pbinternal.CreateTokenAllowancePrepareRequest {
	return &pbinternal.CreateTokenAllowancePrepareRequest{
		OriginalRequest: &tokenpb.CreateTokenAllowanceRequest{
			AllowancePayload: payload,
			OwnerSignature:   signature,
		},
	}
}

type alternatingAllowanceRolloutKnobs struct {
	knobs.Knobs
	rolloutCalls int
}

func (k *alternatingAllowanceRolloutKnobs) RolloutRandom(_ string, _ float64) bool {
	k.rolloutCalls++
	return k.rolloutCalls%2 == 0
}

func TestCreateTokenAllowanceFlowRollbackOwnsOnlyItsInsert(t *testing.T) {
	t.Run("replayed row survives", func(t *testing.T) {
		ctx, tc, cfg, _ := setupAllowanceTest(t)
		tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
		allowanceID := uuid.New()
		payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
		signature := signCreateAllowance(t, payload, allowanceOwnerKey)

		require.NoError(t, ValidateAndApplyCreateAllowance(ctx, cfg, payload, signature))
		flowID := uuid.New()
		flowCtx := consensus.ContextWithFlowExecutionID(ctx, flowID)
		flow := NewCreateTokenAllowanceFlowHandler(cfg)
		_, err := flow.Prepare(flowCtx, createAllowancePrepareRequest(payload, signature))
		require.NoError(t, err)
		require.NoError(t, flow.Rollback(flowCtx, &pbinternal.CreateTokenAllowanceRollbackRequest{AllowanceId: allowanceID[:]}))

		txClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		row, err := txClient.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(ctx)
		require.NoError(t, err)
		assert.Nil(t, row.FlowExecutionID)
	})

	t.Run("owned row is deleted after idempotent prepare redelivery", func(t *testing.T) {
		ctx, tc, cfg, _ := setupAllowanceTest(t)
		tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
		allowanceID := uuid.New()
		payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
		signature := signCreateAllowance(t, payload, allowanceOwnerKey)
		flowID := uuid.New()
		flowCtx := consensus.ContextWithFlowExecutionID(ctx, flowID)
		flow := NewCreateTokenAllowanceFlowHandler(cfg)
		prepare := createAllowancePrepareRequest(payload, signature)

		_, err := flow.Prepare(flowCtx, prepare)
		require.NoError(t, err)
		_, err = flow.Prepare(flowCtx, prepare)
		require.NoError(t, err)
		txClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		row, err := txClient.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(ctx)
		require.NoError(t, err)
		require.NotNil(t, row.FlowExecutionID)
		assert.Equal(t, flowID, *row.FlowExecutionID)

		require.NoError(t, flow.Rollback(flowCtx, &pbinternal.CreateTokenAllowanceRollbackRequest{AllowanceId: allowanceID[:]}))
		exists, err := txClient.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Exist(ctx)
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("row from a committed flow is safe to replay", func(t *testing.T) {
		ctx, tc, cfg, _ := setupAllowanceTest(t)
		tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
		allowanceID := uuid.New()
		payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
		signature := signCreateAllowance(t, payload, allowanceOwnerKey)
		ownerFlowID := uuid.New()
		flow := NewCreateTokenAllowanceFlowHandler(cfg)
		_, err := flow.Prepare(consensus.ContextWithFlowExecutionID(ctx, ownerFlowID), createAllowancePrepareRequest(payload, signature))
		require.NoError(t, err)

		txClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		_, err = txClient.FlowExecution.Create().
			SetID(ownerFlowID).
			SetRole(st.FlowExecutionRoleParticipant).
			SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CREATE_TOKEN_ALLOWANCE)).
			SetCoordinatorIndex(0).
			SetStatus(st.FlowExecutionStatusCommitted).
			Save(ctx)
		require.NoError(t, err)

		replayFlowID := uuid.New()
		replayCtx := consensus.ContextWithFlowExecutionID(ctx, replayFlowID)
		_, err = flow.Prepare(replayCtx, createAllowancePrepareRequest(payload, signature))
		require.NoError(t, err)
		require.NoError(t, flow.Rollback(replayCtx, &pbinternal.CreateTokenAllowanceRollbackRequest{AllowanceId: allowanceID[:]}))

		row, err := txClient.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(ctx)
		require.NoError(t, err)
		require.NotNil(t, row.FlowExecutionID)
		assert.Equal(t, ownerFlowID, *row.FlowExecutionID)
		require.NoError(t, flow.Commit(
			consensus.ContextWithFlowExecutionID(ctx, ownerFlowID),
			&pbinternal.CreateTokenAllowanceCommitRequest{AllowanceId: allowanceID[:]},
		))
		row, err = txClient.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(ctx)
		require.NoError(t, err)
		assert.Nil(t, row.FlowExecutionID)
	})

	t.Run("row whose terminal owner was purged is safe to replay", func(t *testing.T) {
		ctx, tc, cfg, _ := setupAllowanceTest(t)
		tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
		allowanceID := uuid.New()
		payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
		signature := signCreateAllowance(t, payload, allowanceOwnerKey)
		ownerFlowID := uuid.New()
		flow := NewCreateTokenAllowanceFlowHandler(cfg)
		txClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		_, err = txClient.FlowExecution.Create().
			SetID(ownerFlowID).
			SetRole(st.FlowExecutionRoleParticipant).
			SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CREATE_TOKEN_ALLOWANCE)).
			SetCoordinatorIndex(0).
			SetStatus(st.FlowExecutionStatusInFlight).
			Save(ctx)
		require.NoError(t, err)

		_, err = flow.Prepare(consensus.ContextWithFlowExecutionID(ctx, ownerFlowID), createAllowancePrepareRequest(payload, signature))
		require.NoError(t, err)
		require.NoError(t, flow.Commit(
			consensus.ContextWithFlowExecutionID(ctx, ownerFlowID),
			&pbinternal.CreateTokenAllowanceCommitRequest{AllowanceId: allowanceID[:]},
		))
		purgeCutoff := time.Now().Add(-7 * 24 * time.Hour)
		_, err = txClient.FlowExecution.UpdateOneID(ownerFlowID).
			SetStatus(st.FlowExecutionStatusCommitted).
			SetUpdateTime(purgeCutoff.Add(-time.Hour)).
			Save(ctx)
		require.NoError(t, err)
		idsToDelete, err := txClient.FlowExecution.Query().
			Where(
				flowexecution.RoleEQ(st.FlowExecutionRoleParticipant),
				flowexecution.StatusIn(st.FlowExecutionStatusCommitted, st.FlowExecutionStatusRolledBack),
				flowexecution.UpdateTimeLT(purgeCutoff),
			).
			IDs(ctx)
		require.NoError(t, err)
		require.Contains(t, idsToDelete, ownerFlowID)
		deleted, err := txClient.FlowExecution.Delete().Where(flowexecution.IDIn(idsToDelete...)).Exec(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, deleted)
		require.NoError(t, ent.DbCommit(ctx))

		txClient, err = ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		ownerFlowExists, err := txClient.FlowExecution.Query().Where(flowexecution.ID(ownerFlowID)).Exist(ctx)
		require.NoError(t, err)
		require.False(t, ownerFlowExists)

		replayFlowID := uuid.New()
		replayCtx := consensus.ContextWithFlowExecutionID(ctx, replayFlowID)
		_, err = flow.Prepare(replayCtx, createAllowancePrepareRequest(payload, signature))
		require.NoError(t, err)
		require.NoError(t, flow.Rollback(replayCtx, &pbinternal.CreateTokenAllowanceRollbackRequest{AllowanceId: allowanceID[:]}))

		row, err := txClient.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(ctx)
		require.NoError(t, err)
		assert.Nil(t, row.FlowExecutionID)
	})

	t.Run("row from a rolled-back flow is not adopted", func(t *testing.T) {
		ctx, tc, cfg, _ := setupAllowanceTest(t)
		tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
		allowanceID := uuid.New()
		payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
		signature := signCreateAllowance(t, payload, allowanceOwnerKey)
		ownerFlowID := uuid.New()
		flow := NewCreateTokenAllowanceFlowHandler(cfg)
		_, err := flow.Prepare(consensus.ContextWithFlowExecutionID(ctx, ownerFlowID), createAllowancePrepareRequest(payload, signature))
		require.NoError(t, err)

		txClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		_, err = txClient.FlowExecution.Create().
			SetID(ownerFlowID).
			SetRole(st.FlowExecutionRoleParticipant).
			SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CREATE_TOKEN_ALLOWANCE)).
			SetCoordinatorIndex(0).
			SetStatus(st.FlowExecutionStatusRolledBack).
			Save(ctx)
		require.NoError(t, err)

		_, err = flow.Prepare(consensus.ContextWithFlowExecutionID(ctx, uuid.New()), createAllowancePrepareRequest(payload, signature))
		require.Error(t, err)
		assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	})

	t.Run("owned row that moved off active is published by rollback", func(t *testing.T) {
		ctx, tc, cfg, _ := setupAllowanceTest(t)
		tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
		allowanceID := uuid.New()
		payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
		flowID := uuid.New()
		flowCtx := consensus.ContextWithFlowExecutionID(ctx, flowID)
		flow := NewCreateTokenAllowanceFlowHandler(cfg)
		_, err := flow.Prepare(flowCtx, createAllowancePrepareRequest(payload, signCreateAllowance(t, payload, allowanceOwnerKey)))
		require.NoError(t, err)

		txClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		_, err = txClient.TokenAllowance.Update().
			Where(tokenallowance.AllowanceID(allowanceID)).
			SetStatus(st.TokenAllowanceStatusRevoked).
			Save(ctx)
		require.NoError(t, err)

		require.NoError(t, flow.Rollback(flowCtx, &pbinternal.CreateTokenAllowanceRollbackRequest{AllowanceId: allowanceID[:]}))
		row, err := txClient.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(ctx)
		require.NoError(t, err)
		assert.Equal(t, st.TokenAllowanceStatusRevoked, row.Status)
		assert.Nil(t, row.FlowExecutionID)
	})
}

func TestCreateTokenAllowanceValidateDecisionAgainstPrepare(t *testing.T) {
	handler := NewCreateTokenAllowanceFlowHandler(nil)
	allowanceID := uuid.New()
	prepare := &pbinternal.CreateTokenAllowancePrepareRequest{
		OriginalRequest: &tokenpb.CreateTokenAllowanceRequest{
			AllowancePayload: &tokenpb.TokenAllowancePayload{AllowanceId: allowanceID[:]},
		},
	}

	require.NoError(t, handler.ValidateDecisionAgainstPrepare(
		prepare,
		&pbinternal.CreateTokenAllowanceCommitRequest{AllowanceId: allowanceID[:]},
	))
	mismatchedID := uuid.New()
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(
		prepare,
		&pbinternal.CreateTokenAllowanceCommitRequest{AllowanceId: mismatchedID[:]},
	), "does not match prepared allowance_id")
}

func TestCreateTokenAllowanceFlowCommitPublishesAllowance(t *testing.T) {
	ctx, tc, cfg, handler := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
	flowID := uuid.New()
	flowCtx := consensus.ContextWithFlowExecutionID(ctx, flowID)
	flow := NewCreateTokenAllowanceFlowHandler(cfg)

	_, err := flow.Prepare(flowCtx, createAllowancePrepareRequest(payload, signCreateAllowance(t, payload, allowanceOwnerKey)))
	require.NoError(t, err)
	query := &tokenpb.QueryTokenAllowancesRequest{OwnerPublicKey: allowanceOwnerKey.Public().Serialize()}
	response, err := handler.QueryTokenAllowances(ctx, query)
	require.NoError(t, err)
	assert.Empty(t, response.GetAllowances())

	decision := &pbinternal.CreateTokenAllowanceCommitRequest{AllowanceId: allowanceID[:]}
	require.NoError(t, flow.Commit(flowCtx, decision))
	row, err := ent.GetAllowanceByAllowanceID(ctx, allowanceID)
	require.NoError(t, err)
	assert.Nil(t, row.FlowExecutionID)
	response, err = handler.QueryTokenAllowances(ctx, query)
	require.NoError(t, err)
	require.Len(t, response.GetAllowances(), 1)
	assert.Equal(t, allowanceID[:], response.GetAllowances()[0].GetAllowancePayload().GetAllowanceId())

	require.NoError(t, flow.Rollback(flowCtx, &pbinternal.CreateTokenAllowanceRollbackRequest{AllowanceId: allowanceID[:]}))
	row, err = ent.GetAllowanceByAllowanceID(ctx, allowanceID)
	require.NoError(t, err)
	assert.Nil(t, row.FlowExecutionID)
}

func TestCreateTokenAllowanceFlowCommitPublishesNonActiveAllowance(t *testing.T) {
	ctx, tc, cfg, _ := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
	flowID := uuid.New()
	flowCtx := consensus.ContextWithFlowExecutionID(ctx, flowID)
	flow := NewCreateTokenAllowanceFlowHandler(cfg)

	_, err := flow.Prepare(flowCtx, createAllowancePrepareRequest(payload, signCreateAllowance(t, payload, allowanceOwnerKey)))
	require.NoError(t, err)
	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	_, err = txClient.TokenAllowance.Update().
		Where(tokenallowance.AllowanceID(allowanceID)).
		SetStatus(st.TokenAllowanceStatusRevoked).
		Save(ctx)
	require.NoError(t, err)

	require.NoError(t, flow.Commit(flowCtx, &pbinternal.CreateTokenAllowanceCommitRequest{AllowanceId: allowanceID[:]}))
	row, err := ent.GetAllowanceByAllowanceID(ctx, allowanceID)
	require.NoError(t, err)
	assert.Equal(t, st.TokenAllowanceStatusRevoked, row.Status)
	assert.Nil(t, row.FlowExecutionID)
}

func TestCreateTokenAllowanceFlowPrepareBypassesPublicQuota(t *testing.T) {
	ctx, tc, cfg, _ := setupAllowanceTest(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenAllowancesEnabled:           1,
		knobs.KnobTokenMaxActiveAllowancesPerOwner: 1,
	}))
	flow := NewCreateTokenAllowanceFlowHandler(cfg)

	for _, tokenCreate := range []*ent.TokenCreate{
		createAllowanceTestTokenCreate(t, ctx, tc.Client),
		createAllowanceTestTokenCreate(t, ctx, tc.Client),
	} {
		payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), uuid.New(), recentTimestamp(10*time.Second))
		flowCtx := consensus.ContextWithFlowExecutionID(ctx, uuid.New())
		_, err := flow.Prepare(flowCtx, createAllowancePrepareRequest(payload, signCreateAllowance(t, payload, allowanceOwnerKey)))
		require.NoError(t, err)
	}

	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	count, err := txClient.TokenAllowance.Query().Where(tokenallowance.OwnerPublicKey(allowanceOwnerKey.Public())).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestCreateTokenAllowanceFlowPrepareRejectsWhenFeatureDisabled(t *testing.T) {
	ctx, tc, cfg, _ := setupAllowanceTest(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenAllowancesEnabled: 0,
	}))
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
	flowCtx := consensus.ContextWithFlowExecutionID(ctx, uuid.New())

	_, err := NewCreateTokenAllowanceFlowHandler(cfg).Prepare(
		flowCtx,
		createAllowancePrepareRequest(payload, signCreateAllowance(t, payload, allowanceOwnerKey)),
	)
	require.Error(t, err)
	assert.Equal(t, codes.Unimplemented, status.Code(err))
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	exists, err := client.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Exist(ctx)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestCreateTokenAllowanceFlowPrepareRejectsKillSwitchedOwner(t *testing.T) {
	ctx, tc, cfg, _ := setupAllowanceTest(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenAllowancesEnabled:                                      1,
		knobs.KnobKillSwitchWallet + "@" + allowanceOwnerKey.Public().ToHex(): 1,
	}))
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
	flowCtx := consensus.ContextWithFlowExecutionID(ctx, uuid.New())

	_, err := NewCreateTokenAllowanceFlowHandler(cfg).Prepare(
		flowCtx,
		createAllowancePrepareRequest(payload, signCreateAllowance(t, payload, allowanceOwnerKey)),
	)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	var authzErr *authz.Error
	require.ErrorAs(t, err, &authzErr)
	assert.Equal(t, authz.ErrorCodeWalletKillSwitched, authzErr.Code)
	exists, err := tc.Client.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Exist(t.Context())
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestCreateTokenAllowanceFlowFeatureGateReadsAreStable(t *testing.T) {
	ctx, tc, cfg, _ := setupAllowanceTest(t)
	gate := &alternatingAllowanceRolloutKnobs{
		Knobs: knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobTokenAllowancesEnabled: 1,
		}),
	}
	ctx = knobs.InjectKnobsService(ctx, gate)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), uuid.New(), recentTimestamp(10*time.Second))
	flowCtx := consensus.ContextWithFlowExecutionID(ctx, uuid.New())

	for range 10 {
		assert.True(t, allowancesEnabled(flowCtx))
	}
	_, err := NewCreateTokenAllowanceFlowHandler(cfg).Prepare(
		flowCtx,
		createAllowancePrepareRequest(payload, signCreateAllowance(t, payload, allowanceOwnerKey)),
	)
	require.NoError(t, err)
	for range 10 {
		assert.True(t, allowancesEnabled(flowCtx))
	}
	assert.Zero(t, gate.rolloutCalls)
}

func TestCreateTokenAllowanceFlowPrepareAcceptsStaleTimestamp(t *testing.T) {
	ctx, tc, cfg, _ := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(48*time.Hour))
	flowCtx := consensus.ContextWithFlowExecutionID(ctx, uuid.New())

	_, err := NewCreateTokenAllowanceFlowHandler(cfg).Prepare(
		flowCtx,
		createAllowancePrepareRequest(payload, signCreateAllowance(t, payload, allowanceOwnerKey)),
	)
	require.NoError(t, err)
	row, err := ent.GetAllowanceByAllowanceID(ctx, allowanceID)
	require.NoError(t, err)
	assert.Equal(t, st.TokenAllowanceStatusActive, row.Status)
}

func TestCreateTokenAllowanceFlowPrepareUnreadableTokenIsNotNotFound(t *testing.T) {
	ctx, tc, cfg, _ := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), uuid.New(), recentTimestamp(10*time.Second))
	flowCtx, cancel := context.WithCancel(consensus.ContextWithFlowExecutionID(ctx, uuid.New()))
	cancel()

	_, err := NewCreateTokenAllowanceFlowHandler(cfg).Prepare(
		flowCtx,
		createAllowancePrepareRequest(payload, signCreateAllowance(t, payload, allowanceOwnerKey)),
	)
	require.Error(t, err)
	assert.NotEqual(t, codes.NotFound, status.Code(err), "an unreadable token row must not be reported as missing")
}

func TestCreateTokenAllowanceFlowPrepareRejectsNetworkMismatch(t *testing.T) {
	ctx, tc, cfg, _ := setupAllowanceTest(t)
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))
	payload.Network = sparkpb.Network_MAINNET
	flowCtx := consensus.ContextWithFlowExecutionID(ctx, uuid.New())

	_, err := NewCreateTokenAllowanceFlowHandler(cfg).Prepare(
		flowCtx,
		createAllowancePrepareRequest(payload, signCreateAllowance(t, payload, allowanceOwnerKey)),
	)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	exists, err := tc.Client.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Exist(t.Context())
	require.NoError(t, err)
	assert.False(t, exists)
}
