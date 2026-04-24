package handler

import (
	"context"
	"testing"

	"github.com/google/uuid"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// testDecisionPayload marshals a recognizable proto into the exact byte
// format TwoPCEngine.Execute stores on coordinator rows (the wire-format
// bytes of an *anypb.Any). Tests use this so the QueryOutcome round-trip
// can be verified against a known payload.
func testDecisionPayload(t *testing.T, op proto.Message) []byte {
	t.Helper()
	anyMsg, err := anypb.New(op)
	require.NoError(t, err)
	bytes, err := proto.Marshal(anyMsg)
	require.NoError(t, err)
	return bytes
}

// insertCoordinatorRow inserts a COORDINATOR FlowExecution row at the given
// status with a recognizable decision payload so QueryOutcome can be
// exercised over a realistic post-engine.Execute state.
func insertCoordinatorRow(t *testing.T, ctx context.Context, id uuid.UUID, status st.FlowExecutionStatus, payload proto.Message) {
	t.Helper()
	db, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	_, err = db.FlowExecution.Create().
		SetID(id).
		SetRole(st.FlowExecutionRoleCoordinator).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_RENEW_LEAF)).
		SetStatus(status).
		SetCoordinatorIndex(0).
		SetDecisionPayload(testDecisionPayload(t, payload)).
		Save(ctx)
	require.NoError(t, err)
}

// insertParticipantRowForQueryOutcome inserts a PARTICIPANT FlowExecution
// row used by the QueryOutcome negative test. QueryOutcome should refuse to
// serve PARTICIPANT rows because the querying operator should be asking the
// actual coordinator, not a participant.
//
// Named distinctly from insertParticipantRow in gossip_handler_test.go to
// avoid the package-level collision between the two helpers (both files are
// in package handler).
func insertParticipantRowForQueryOutcome(t *testing.T, ctx context.Context, id uuid.UUID) {
	t.Helper()
	db, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	_, err = db.FlowExecution.Create().
		SetID(id).
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_RENEW_LEAF)).
		SetStatus(st.FlowExecutionStatusInFlight).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)
}

func TestQueryOutcome_CoordinatorCommitted_ReturnsCommittedWithPayload(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	id := uuid.New()
	commitOp := &pbgossip.GossipMessage{MessageId: "commit-op"}
	insertCoordinatorRow(t, ctx, id, st.FlowExecutionStatusCommitted, commitOp)

	h := NewConsensusQueryHandler(sparktesting.TestConfig(t))
	resp, err := h.QueryOutcome(ctx, &pbinternal.ConsensusQueryOutcomeRequest{FlowExecutionId: id.String()})
	require.NoError(t, err)
	assert.Equal(t, pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_COMMITTED, resp.Outcome)
	assert.Equal(t, int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_RENEW_LEAF), resp.OpType)

	require.NotNil(t, resp.DecisionPayload)
	roundTripped, err := resp.DecisionPayload.UnmarshalNew()
	require.NoError(t, err)
	assert.True(t, proto.Equal(commitOp, roundTripped), "round-tripped commit payload should match")
}

func TestQueryOutcome_CoordinatorRolledBack_ReturnsRolledBackWithPayload(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	id := uuid.New()
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-op"}
	insertCoordinatorRow(t, ctx, id, st.FlowExecutionStatusRolledBack, rollbackOp)

	h := NewConsensusQueryHandler(sparktesting.TestConfig(t))
	resp, err := h.QueryOutcome(ctx, &pbinternal.ConsensusQueryOutcomeRequest{FlowExecutionId: id.String()})
	require.NoError(t, err)
	assert.Equal(t, pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_ROLLED_BACK, resp.Outcome)

	require.NotNil(t, resp.DecisionPayload)
	roundTripped, err := resp.DecisionPayload.UnmarshalNew()
	require.NoError(t, err)
	assert.True(t, proto.Equal(rollbackOp, roundTripped), "round-tripped rollback payload should match")
}

func TestQueryOutcome_CoordinatorInFlight_ReturnsInFlightWithRollbackPayload(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	id := uuid.New()
	// Matches the engine contract: while IN_FLIGHT the row already carries
	// the rollback payload (written at Create time). Callers receiving
	// IN_FLIGHT still wait; they only act on the payload after COMMITTED
	// or ROLLED_BACK, but the field being populated is what makes the
	// self-sweep path work without re-deriving.
	rollbackOp := &pbgossip.GossipMessage{MessageId: "prepopulated-rollback"}
	insertCoordinatorRow(t, ctx, id, st.FlowExecutionStatusInFlight, rollbackOp)

	h := NewConsensusQueryHandler(sparktesting.TestConfig(t))
	resp, err := h.QueryOutcome(ctx, &pbinternal.ConsensusQueryOutcomeRequest{FlowExecutionId: id.String()})
	require.NoError(t, err)
	assert.Equal(t, pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_IN_FLIGHT, resp.Outcome)
	require.NotNil(t, resp.DecisionPayload)
}

func TestQueryOutcome_MissingRow_ReturnsUnspecified(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	h := NewConsensusQueryHandler(sparktesting.TestConfig(t))
	resp, err := h.QueryOutcome(ctx, &pbinternal.ConsensusQueryOutcomeRequest{FlowExecutionId: uuid.NewString()})
	require.NoError(t, err)
	assert.Equal(t, pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_UNSPECIFIED, resp.Outcome)
	assert.Nil(t, resp.DecisionPayload)
}

func TestQueryOutcome_ParticipantRow_ReturnsUnspecified(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	id := uuid.New()
	insertParticipantRowForQueryOutcome(t, ctx, id)

	h := NewConsensusQueryHandler(sparktesting.TestConfig(t))
	resp, err := h.QueryOutcome(ctx, &pbinternal.ConsensusQueryOutcomeRequest{FlowExecutionId: id.String()})
	require.NoError(t, err)
	assert.Equal(t, pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_UNSPECIFIED, resp.Outcome,
		"querying a participant row should not leak participant state")
}

func TestQueryOutcome_InvalidUUID_ReturnsInvalidArgument(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	h := NewConsensusQueryHandler(sparktesting.TestConfig(t))
	_, err := h.QueryOutcome(ctx, &pbinternal.ConsensusQueryOutcomeRequest{FlowExecutionId: "not-a-uuid"})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}
