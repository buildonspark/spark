package consensus

import (
	"context"
	"fmt"
	"testing"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// testOpType is a placeholder enum value for tests. The real enum values
// will be added as domain flows are migrated.
const testOpType = pbgossip.ConsensusOperationType(999)

// mockFlowHandler records calls for testing.
type mockFlowHandler struct {
	prepareCalled  bool
	commitCalled   bool
	rollbackCalled bool
	lastOp         proto.Message
	prepareResult  []byte
	prepareErr     error
	commitErr      error
	rollbackErr    error
}

func (m *mockFlowHandler) Prepare(_ context.Context, op proto.Message) ([]byte, error) {
	m.prepareCalled = true
	m.lastOp = op
	return m.prepareResult, m.prepareErr
}

func (m *mockFlowHandler) Commit(_ context.Context, op proto.Message) error {
	m.commitCalled = true
	m.lastOp = op
	return m.commitErr
}

func (m *mockFlowHandler) Rollback(_ context.Context, op proto.Message) error {
	m.rollbackCalled = true
	m.lastOp = op
	return m.rollbackErr
}

var _ FlowHandler = (*mockFlowHandler)(nil)

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

func newTestEngine() (*TwoPCEngine, *mockGossipSender) {
	gs := &mockGossipSender{}
	return NewTwoPCEngine(nil, gs), gs
}

func mustRegister(t *testing.T, engine *TwoPCEngine, opType pbgossip.ConsensusOperationType, handler FlowHandler) {
	t.Helper()
	require.NoError(t, engine.Register(opType, handler))
}

// --- Dispatch tests (local FlowHandler routing) ---

func TestDispatch_Prepare(t *testing.T) {
	engine, _ := newTestEngine()
	h := &mockFlowHandler{prepareResult: []byte("sig-share")}
	mustRegister(t, engine, testOpType, h)

	op := &pbgossip.GossipMessage{MessageId: "op-1"}
	result, err := engine.Dispatch(t.Context(), testOpType, PhasePrepare, op)

	require.NoError(t, err)
	assert.True(t, h.prepareCalled)
	assert.Equal(t, op, h.lastOp)
	assert.Equal(t, []byte("sig-share"), result)
}

func TestDispatch_Commit(t *testing.T) {
	engine, _ := newTestEngine()
	h := &mockFlowHandler{}
	mustRegister(t, engine, testOpType, h)

	op := &pbgossip.GossipMessage{MessageId: "op-1"}
	result, err := engine.Dispatch(t.Context(), testOpType, PhaseCommit, op)

	require.NoError(t, err)
	assert.True(t, h.commitCalled)
	assert.Equal(t, op, h.lastOp)
	assert.Nil(t, result)
}

func TestDispatch_Rollback(t *testing.T) {
	engine, _ := newTestEngine()
	h := &mockFlowHandler{}
	mustRegister(t, engine, testOpType, h)

	op := &pbgossip.GossipMessage{MessageId: "op-1"}
	result, err := engine.Dispatch(t.Context(), testOpType, PhaseRollback, op)

	require.NoError(t, err)
	assert.True(t, h.rollbackCalled)
	assert.Equal(t, op, h.lastOp)
	assert.Nil(t, result)
}

func TestDispatch_UnregisteredOpType(t *testing.T) {
	engine, _ := newTestEngine()

	_, err := engine.Dispatch(t.Context(), testOpType, PhasePrepare, &pbgossip.GossipMessage{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no handler registered for operation type")
}

func TestDispatch_UnknownPhase(t *testing.T) {
	engine, _ := newTestEngine()
	h := &mockFlowHandler{}
	mustRegister(t, engine, testOpType, h)

	_, err := engine.Dispatch(t.Context(), testOpType, OperationPhase(99), &pbgossip.GossipMessage{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown operation phase 99")
}

func TestDispatch_PropagatesPrepareError(t *testing.T) {
	engine, _ := newTestEngine()
	h := &mockFlowHandler{prepareErr: fmt.Errorf("validation failed")}
	mustRegister(t, engine, testOpType, h)

	_, err := engine.Dispatch(t.Context(), testOpType, PhasePrepare, &pbgossip.GossipMessage{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation failed")
}

func TestDispatch_PropagatesCommitError(t *testing.T) {
	engine, _ := newTestEngine()
	h := &mockFlowHandler{commitErr: fmt.Errorf("commit failed")}
	mustRegister(t, engine, testOpType, h)

	_, err := engine.Dispatch(t.Context(), testOpType, PhaseCommit, &pbgossip.GossipMessage{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit failed")
}

func TestDispatch_PropagatesRollbackError(t *testing.T) {
	engine, _ := newTestEngine()
	h := &mockFlowHandler{rollbackErr: fmt.Errorf("rollback failed")}
	mustRegister(t, engine, testOpType, h)

	_, err := engine.Dispatch(t.Context(), testOpType, PhaseRollback, &pbgossip.GossipMessage{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback failed")
}

func TestRegister_ErrorsOnDuplicate(t *testing.T) {
	engine, _ := newTestEngine()
	require.NoError(t, engine.Register(testOpType, &mockFlowHandler{}))

	err := engine.Register(testOpType, &mockFlowHandler{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handler already registered for operation type")
}

// --- Commit/Rollback tests (gossip message construction + delegation) ---

func TestCommit_BuildsConsensusCommitGossip(t *testing.T) {
	engine, gs := newTestEngine()
	participants := []string{"op1", "op2"}
	op := &pbgossip.GossipMessage{MessageId: "commit-payload"}

	err := engine.Commit(t.Context(), testOpType, op, participants)

	require.NoError(t, err)
	require.Len(t, gs.calls, 1)
	assert.Equal(t, participants, gs.calls[0].participants)

	commit := gs.calls[0].msg.GetConsensusCommit()
	require.NotNil(t, commit)
	assert.Equal(t, testOpType, commit.OpType)

	// Verify the Any contains the original message.
	roundTripped, err := commit.Operation.UnmarshalNew()
	require.NoError(t, err)
	assert.True(t, proto.Equal(op, roundTripped))
}

func TestCommit_PropagatesGossipError(t *testing.T) {
	engine, gs := newTestEngine()
	gs.err = fmt.Errorf("gossip failed")

	err := engine.Commit(t.Context(), testOpType, &pbgossip.GossipMessage{}, []string{"op1"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "gossip failed")
}

func TestRollback_BuildsConsensusRollbackGossip(t *testing.T) {
	engine, gs := newTestEngine()
	participants := []string{"op1"}
	op := &pbgossip.GossipMessage{MessageId: "rollback-payload"}

	err := engine.Rollback(t.Context(), testOpType, op, participants)

	require.NoError(t, err)
	require.Len(t, gs.calls, 1)
	assert.Equal(t, participants, gs.calls[0].participants)

	rollback := gs.calls[0].msg.GetConsensusRollback()
	require.NotNil(t, rollback)
	assert.Equal(t, testOpType, rollback.OpType)

	roundTripped, err := rollback.Operation.UnmarshalNew()
	require.NoError(t, err)
	assert.True(t, proto.Equal(op, roundTripped))
}

func TestRollback_PropagatesGossipError(t *testing.T) {
	engine, gs := newTestEngine()
	gs.err = fmt.Errorf("rollback gossip failed")

	err := engine.Rollback(t.Context(), testOpType, &pbgossip.GossipMessage{}, []string{"op1"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback gossip failed")
}
