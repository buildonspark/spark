package consensus

import (
	"context"
	"fmt"
	"testing"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/helper"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	testOpType        = pbgossip.ConsensusOperationType(999)
	testCoordinatorID = uint64(7)
)

// mockGossipSender records gossip calls for testing.
type mockGossipSender struct {
	calls []gossipCall
	err   error
}

type gossipCall struct {
	msg          *pbgossip.GossipMessage
	participants []string
}

func (m *mockGossipSender) CreateCommitAndSendGossipMessage(_ context.Context, msg *pbgossip.GossipMessage, participants []string) (*ent.Gossip, error) {
	m.calls = append(m.calls, gossipCall{msg: msg, participants: participants})
	return nil, m.err
}

var _ GossipSender = (*mockGossipSender)(nil)

func testConfig() *so.Config {
	return &so.Config{
		Identifier: "op-self",
		SigningOperatorMap: map[string]*so.SigningOperator{
			"op-self": {Identifier: "op-self", ID: testCoordinatorID},
		},
	}
}

// newTestEngine spins up a fresh engine backed by a SQLite test DB so tests
// exercise Execute end-to-end including the FlowExecution row writes. Returns
// a ctx scoped to the test DB and a handle to the Ent client for assertions.
func newTestEngine(t *testing.T) (context.Context, *TwoPCEngine, *mockGossipSender, *ent.Client, *so.Config) {
	t.Helper()
	ctx, _ := db.NewTestSQLiteContext(t)
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	gs := &mockGossipSender{}
	config := testConfig()
	return ctx, NewTwoPCEngine(config, gs), gs, entTx.Client(), config
}

// simpleFlow is a CoordinatorFlow where commit and rollback use the same static payload.
type simpleFlow struct {
	prepareErr error
	payload    proto.Message
}

func (f *simpleFlow) Prepare(_ context.Context, _ proto.Message) (proto.Message, error) {
	return nil, f.prepareErr
}

func (f *simpleFlow) Commit(_ context.Context, _ proto.Message) error { return nil }

func (f *simpleFlow) Rollback(_ context.Context, _ proto.Message) error { return nil }

func (f *simpleFlow) PrepareOp() proto.Message { return f.payload }

func (f *simpleFlow) PrepareTask(_ context.Context, _ *so.SigningOperator) (proto.Message, error) {
	return nil, f.prepareErr
}

func (f *simpleFlow) BuildCommitPayload(_ context.Context, _ map[string]*anypb.Any) (proto.Message, error) {
	return f.payload, nil
}

func (f *simpleFlow) RollbackPayload() proto.Message {
	return f.payload
}

var _ CoordinatorFlow = (*simpleFlow)(nil)

// aggregatingFlow is a CoordinatorFlow where BuildCommitPayload produces a
// different message from the prepare results.
type aggregatingFlow struct {
	rollbackOp   proto.Message
	commitResult proto.Message
	commitErr    error
}

func (f *aggregatingFlow) Prepare(_ context.Context, _ proto.Message) (proto.Message, error) {
	return nil, nil
}

func (f *aggregatingFlow) Commit(_ context.Context, _ proto.Message) error { return nil }

func (f *aggregatingFlow) Rollback(_ context.Context, _ proto.Message) error { return nil }

func (f *aggregatingFlow) PrepareOp() proto.Message { return f.rollbackOp }

func (f *aggregatingFlow) PrepareTask(_ context.Context, _ *so.SigningOperator) (proto.Message, error) {
	return nil, nil
}

func (f *aggregatingFlow) BuildCommitPayload(_ context.Context, _ map[string]*anypb.Any) (proto.Message, error) {
	return f.commitResult, f.commitErr
}

func (f *aggregatingFlow) RollbackPayload() proto.Message {
	return f.rollbackOp
}

var _ CoordinatorFlow = (*aggregatingFlow)(nil)

// selfSelection builds an OperatorSelection with only the self operator.
// Keeps tests hermetic — no real gRPC fan-out, just the local flow.Prepare path.
func selfSelection(t *testing.T, config *so.Config) *helper.OperatorSelection {
	t.Helper()
	sel, err := helper.NewPreSelectedOperatorSelection(config, []string{"op-self"})
	require.NoError(t, err)
	return sel
}

// payloadFromAnyBytes round-trips stored decision_payload bytes (a marshalled
// *anypb.Any) back into the underlying concrete proto.Message. Used by tests
// to assert the payload the row holds matches what the flow emitted.
func payloadFromAnyBytes(t *testing.T, anyBytes []byte) proto.Message {
	t.Helper()
	anyMsg := &anypb.Any{}
	require.NoError(t, proto.Unmarshal(anyBytes, anyMsg))
	msg, err := anyMsg.UnmarshalNew()
	require.NoError(t, err)
	return msg
}

// --- Execute tests (simple flow) ---

func TestExecute_PrepareSucceeds_SendsCommitWithPayload(t *testing.T) {
	ctx, engine, gs, _, config := newTestEngine(t)
	op := &pbgossip.GossipMessage{MessageId: "op"}

	result, err := engine.Execute(ctx, testOpType, selfSelection(t, config), &simpleFlow{payload: op})

	require.NoError(t, err)
	assert.True(t, proto.Equal(op, result))
	require.Len(t, gs.calls, 1)

	commit := gs.calls[0].msg.GetConsensusCommit()
	require.NotNil(t, commit)
	roundTripped, err := commit.Operation.UnmarshalNew()
	require.NoError(t, err)
	assert.True(t, proto.Equal(op, roundTripped))
}

func TestExecute_PrepareFails_SendsRollback(t *testing.T) {
	ctx, engine, gs, _, config := newTestEngine(t)
	op := &pbgossip.GossipMessage{MessageId: "op"}

	result, err := engine.Execute(ctx, testOpType, selfSelection(t, config),
		&simpleFlow{prepareErr: fmt.Errorf("validation failed"), payload: op})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "prepare failed")
	assert.Nil(t, result)
	require.Len(t, gs.calls, 1)
	assert.NotNil(t, gs.calls[0].msg.GetConsensusRollback())
}

func TestExecute_CommitGossipFails_NoRollback(t *testing.T) {
	ctx, engine, gs, _, config := newTestEngine(t)
	gs.err = fmt.Errorf("gossip unavailable")
	op := &pbgossip.GossipMessage{MessageId: "op"}

	result, err := engine.Execute(ctx, testOpType, selfSelection(t, config), &simpleFlow{payload: op})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit gossip failed")
	assert.Nil(t, result)
	require.Len(t, gs.calls, 1)
	assert.NotNil(t, gs.calls[0].msg.GetConsensusCommit())
}

// --- Execute tests (aggregating flow) ---

func TestExecute_BuildCommitPayload_CommitUsesAggregatedMessage(t *testing.T) {
	ctx, engine, gs, _, config := newTestEngine(t)
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback"}
	commitOp := &pbgossip.GossipMessage{MessageId: "aggregated-commit"}

	result, err := engine.Execute(ctx, testOpType, selfSelection(t, config),
		&aggregatingFlow{rollbackOp: rollbackOp, commitResult: commitOp})

	require.NoError(t, err)
	assert.True(t, proto.Equal(commitOp, result))
	require.Len(t, gs.calls, 1)

	commit := gs.calls[0].msg.GetConsensusCommit()
	require.NotNil(t, commit)
	roundTripped, err := commit.Operation.UnmarshalNew()
	require.NoError(t, err)
	assert.True(t, proto.Equal(commitOp, roundTripped))
}

func TestExecute_BuildCommitPayloadFails_SendsRollback(t *testing.T) {
	ctx, engine, gs, _, config := newTestEngine(t)
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback"}

	result, err := engine.Execute(ctx, testOpType, selfSelection(t, config),
		&aggregatingFlow{rollbackOp: rollbackOp, commitErr: fmt.Errorf("aggregation failed")})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "build-commit failed")
	assert.Nil(t, result)
	require.Len(t, gs.calls, 1)

	rollback := gs.calls[0].msg.GetConsensusRollback()
	require.NotNil(t, rollback)
	roundTripped, err := rollback.Operation.UnmarshalNew()
	require.NoError(t, err)
	assert.True(t, proto.Equal(rollbackOp, roundTripped))
}

// --- FlowExecution row tests ---

func TestExecute_WritesCoordinatorRow_CommittedOnSuccess(t *testing.T) {
	ctx, engine, gs, client, config := newTestEngine(t)
	commitOp := &pbgossip.GossipMessage{MessageId: "commit-payload"}
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}

	_, err := engine.Execute(ctx, testOpType, selfSelection(t, config),
		&aggregatingFlow{rollbackOp: rollbackOp, commitResult: commitOp})
	require.NoError(t, err)

	rows, err := client.FlowExecution.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	row := rows[0]
	assert.Equal(t, st.FlowExecutionRoleCoordinator, row.Role)
	assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status)
	assert.Equal(t, int32(testOpType), row.OpType)
	assert.Equal(t, uint(testCoordinatorID), row.CoordinatorIndex)
	require.NotNil(t, row.DecisionPayload)
	assert.True(t, proto.Equal(commitOp, payloadFromAnyBytes(t, *row.DecisionPayload)),
		"on success decision_payload should be overwritten with the commit payload")

	// The gossip message carries the same row id as its flow_execution_id.
	require.Len(t, gs.calls, 1)
	commit := gs.calls[0].msg.GetConsensusCommit()
	require.NotNil(t, commit)
	assert.Equal(t, row.ID.String(), commit.FlowExecutionId)
}

func TestExecute_WritesCoordinatorRow_RolledBackOnPrepareFailure(t *testing.T) {
	ctx, engine, gs, client, config := newTestEngine(t)
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}

	_, err := engine.Execute(ctx, testOpType, selfSelection(t, config),
		&simpleFlow{prepareErr: fmt.Errorf("nope"), payload: rollbackOp})
	require.Error(t, err)

	rows, err := client.FlowExecution.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	row := rows[0]
	assert.Equal(t, st.FlowExecutionStatusRolledBack, row.Status)
	require.NotNil(t, row.DecisionPayload)
	assert.True(t, proto.Equal(rollbackOp, payloadFromAnyBytes(t, *row.DecisionPayload)),
		"on prepare failure decision_payload should still hold the rollback bytes written at row creation")

	require.Len(t, gs.calls, 1)
	rollback := gs.calls[0].msg.GetConsensusRollback()
	require.NotNil(t, rollback)
	assert.Equal(t, row.ID.String(), rollback.FlowExecutionId)
}

func TestExecute_WritesCoordinatorRow_RolledBackOnBuildCommitFailure(t *testing.T) {
	ctx, engine, _, client, config := newTestEngine(t)
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}

	_, err := engine.Execute(ctx, testOpType, selfSelection(t, config),
		&aggregatingFlow{rollbackOp: rollbackOp, commitErr: fmt.Errorf("aggregation failed")})
	require.Error(t, err)

	rows, err := client.FlowExecution.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	row := rows[0]
	assert.Equal(t, st.FlowExecutionStatusRolledBack, row.Status)
	require.NotNil(t, row.DecisionPayload)
	assert.True(t, proto.Equal(rollbackOp, payloadFromAnyBytes(t, *row.DecisionPayload)))
}

// --- CAS conflict tests (markCommitted vs. self-sweep race) ---

// TestExecute_MarkCommitted_PreemptedByExternalRollback simulates the race
// where the coordinator self-sweep transitions the row to ROLLED_BACK after
// the engine started Execute but before it gets to markCommitted. The CAS
// in markCommitted should detect the preemption, return
// ErrCoordinatorRowPreempted, and Execute must not send commit gossip.
func TestExecute_MarkCommitted_PreemptedByExternalRollback(t *testing.T) {
	ctx, _, gs, client, config := newTestEngine(t)
	commitOp := &pbgossip.GossipMessage{MessageId: "commit-payload"}
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}

	// preemptingFlow flips the engine's coordinator row to ROLLED_BACK
	// inside BuildCommitPayload, simulating the self-sweep winning the
	// race. The flow's Commit/Rollback handlers are no-ops; we're
	// testing the engine's response, not the flow's.
	preempt := &preemptingFlow{
		ctx:          ctx,
		client:       client,
		commitResult: commitOp,
		rollbackOp:   rollbackOp,
	}

	_, err := NewTwoPCEngine(config, gs).Execute(ctx, testOpType, selfSelection(t, config), preempt)
	require.ErrorIs(t, err, ErrCoordinatorRowPreempted, "Execute must propagate the preemption")

	// Row stays ROLLED_BACK — markCommitted's conditional UPDATE matched
	// zero rows, leaving the sweep's transition intact.
	rows, err := client.FlowExecution.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, rows[0].Status,
		"sweep-driven ROLLED_BACK must not be clobbered by markCommitted")

	// No commit gossip sent — the engine bailed before reaching e.commit.
	for _, c := range gs.calls {
		assert.Nil(t, c.msg.GetConsensusCommit(),
			"no ConsensusCommit gossip must be sent after a markCommitted preemption")
	}
}

// TestMarkRolledBack_AlreadyRolledBack_IsNoOp confirms markRolledBack's CAS
// is benign on an already-terminal row: it returns nil rather than erroring
// or overwriting (the row is already in the rolled-back state we wanted).
func TestMarkRolledBack_AlreadyRolledBack_IsNoOp(t *testing.T) {
	ctx, engine, _, client, _ := newTestEngine(t)
	row, err := client.FlowExecution.Create().
		SetRole(st.FlowExecutionRoleCoordinator).
		SetOpType(int32(testOpType)).
		SetCoordinatorIndex(uint(testCoordinatorID)).
		SetStatus(st.FlowExecutionStatusRolledBack).
		Save(ctx)
	require.NoError(t, err)

	require.NoError(t, engine.markRolledBack(ctx, row), "CAS conflict on markRolledBack must be benign")

	updated, err := client.FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, updated.Status)
}

// preemptingFlow simulates the coordinator self-sweep racing the engine: in
// BuildCommitPayload it transitions the engine's coordinator row to
// ROLLED_BACK out of band, so the engine's subsequent markCommitted hits
// a CAS conflict.
type preemptingFlow struct {
	ctx          context.Context
	client       *ent.Client
	commitResult proto.Message
	rollbackOp   proto.Message
}

func (f *preemptingFlow) Prepare(_ context.Context, _ proto.Message) (proto.Message, error) {
	return nil, nil
}
func (f *preemptingFlow) Commit(_ context.Context, _ proto.Message) error   { return nil }
func (f *preemptingFlow) Rollback(_ context.Context, _ proto.Message) error { return nil }
func (f *preemptingFlow) PrepareOp() proto.Message                          { return f.rollbackOp }
func (f *preemptingFlow) PrepareTask(_ context.Context, _ *so.SigningOperator) (proto.Message, error) {
	return nil, nil
}

// BuildCommitPayload flips the (single) coordinator row to ROLLED_BACK
// before returning the commit payload. This is the moral equivalent of the
// sweep transitioning the row while the engine is mid-flight.
func (f *preemptingFlow) BuildCommitPayload(_ context.Context, _ map[string]*anypb.Any) (proto.Message, error) {
	if _, err := f.client.FlowExecution.Update().
		SetStatus(st.FlowExecutionStatusRolledBack).
		Save(f.ctx); err != nil {
		return nil, fmt.Errorf("preempt: %w", err)
	}
	return f.commitResult, nil
}

func (f *preemptingFlow) RollbackPayload() proto.Message { return f.rollbackOp }

var _ CoordinatorFlow = (*preemptingFlow)(nil)
