package consensus

import (
	"context"
	"fmt"
	"testing"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const testOpType = pbgossip.ConsensusOperationType(999)

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
			"op-self": {Identifier: "op-self"},
		},
	}
}

func newTestEngineWithConfig() (*TwoPCEngine, *mockGossipSender, *so.Config) {
	gs := &mockGossipSender{}
	config := testConfig()
	return NewTwoPCEngine(config, gs), gs, config
}

// simpleFlowHandler is a FlowHandler without PostPrepare — commit uses op directly.
type simpleFlowHandler struct{}

func (h *simpleFlowHandler) Prepare(_ context.Context, _ proto.Message) ([]byte, error) {
	return nil, nil
}

func (h *simpleFlowHandler) Commit(_ context.Context, _ proto.Message) error {
	return nil
}

func (h *simpleFlowHandler) Rollback(_ context.Context, _ proto.Message) error {
	return nil
}

// aggregatingFlowHandler implements PostPrepare to build the commit message from results.
type aggregatingFlowHandler struct {
	simpleFlowHandler
	postPrepareResult proto.Message
	postPrepareErr    error
}

func (h *aggregatingFlowHandler) PostPrepare(_ context.Context, _ map[string][]byte) (proto.Message, error) {
	return h.postPrepareResult, h.postPrepareErr
}

var _ PostPrepare = (*aggregatingFlowHandler)(nil)

// --- Execute tests (simple flow, no PostPrepare) ---

func TestExecute_PrepareSucceeds_SendsCommitWithOp(t *testing.T) {
	engine, gs, config := newTestEngineWithConfig()
	op := &pbgossip.GossipMessage{MessageId: "op"}
	selection, err := helper.NewPreSelectedOperatorSelection(config, []string{"op-self"})
	require.NoError(t, err)

	err = engine.Execute(t.Context(), testOpType, op,
		func(_ context.Context, _ *so.SigningOperator) ([]byte, error) {
			return []byte("sig-share"), nil
		},
		selection, &simpleFlowHandler{})

	require.NoError(t, err)
	require.Len(t, gs.calls, 1)

	commit := gs.calls[0].msg.GetConsensusCommit()
	require.NotNil(t, commit)
	roundTripped, err := commit.Operation.UnmarshalNew()
	require.NoError(t, err)
	assert.True(t, proto.Equal(op, roundTripped))
}

func TestExecute_PrepareFails_SendsRollback(t *testing.T) {
	engine, gs, config := newTestEngineWithConfig()
	op := &pbgossip.GossipMessage{MessageId: "op"}
	selection, err := helper.NewPreSelectedOperatorSelection(config, []string{"op-self"})
	require.NoError(t, err)

	err = engine.Execute(t.Context(), testOpType, op,
		func(_ context.Context, _ *so.SigningOperator) ([]byte, error) {
			return nil, fmt.Errorf("validation failed")
		},
		selection, &simpleFlowHandler{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "prepare failed")
	require.Len(t, gs.calls, 1)
	assert.NotNil(t, gs.calls[0].msg.GetConsensusRollback())
}

func TestExecute_CommitGossipFails_NoRollback(t *testing.T) {
	engine, gs, config := newTestEngineWithConfig()
	gs.err = fmt.Errorf("gossip unavailable")
	op := &pbgossip.GossipMessage{MessageId: "op"}
	selection, err := helper.NewPreSelectedOperatorSelection(config, []string{"op-self"})
	require.NoError(t, err)

	err = engine.Execute(t.Context(), testOpType, op,
		func(_ context.Context, _ *so.SigningOperator) ([]byte, error) {
			return []byte("sig-share"), nil
		},
		selection, &simpleFlowHandler{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit gossip failed")
	require.Len(t, gs.calls, 1)
	assert.NotNil(t, gs.calls[0].msg.GetConsensusCommit())
}

// --- Execute tests (with PostPrepare) ---

func TestExecute_PostPrepare_CommitUsesAggregatedMessage(t *testing.T) {
	engine, gs, config := newTestEngineWithConfig()
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback"}
	commitOp := &pbgossip.GossipMessage{MessageId: "aggregated-commit"}
	selection, err := helper.NewPreSelectedOperatorSelection(config, []string{"op-self"})
	require.NoError(t, err)

	handler := &aggregatingFlowHandler{postPrepareResult: commitOp}

	err = engine.Execute(t.Context(), testOpType, rollbackOp,
		func(_ context.Context, _ *so.SigningOperator) ([]byte, error) {
			return []byte("sig-share"), nil
		},
		selection, handler)

	require.NoError(t, err)
	require.Len(t, gs.calls, 1)

	commit := gs.calls[0].msg.GetConsensusCommit()
	require.NotNil(t, commit)
	roundTripped, err := commit.Operation.UnmarshalNew()
	require.NoError(t, err)
	assert.True(t, proto.Equal(commitOp, roundTripped))
}

func TestExecute_PostPrepareFails_SendsRollback(t *testing.T) {
	engine, gs, config := newTestEngineWithConfig()
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback"}
	selection, err := helper.NewPreSelectedOperatorSelection(config, []string{"op-self"})
	require.NoError(t, err)

	handler := &aggregatingFlowHandler{postPrepareErr: fmt.Errorf("aggregation failed")}

	err = engine.Execute(t.Context(), testOpType, rollbackOp,
		func(_ context.Context, _ *so.SigningOperator) ([]byte, error) {
			return []byte("sig-share"), nil
		},
		selection, handler)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "post-prepare failed")
	require.Len(t, gs.calls, 1)

	rollback := gs.calls[0].msg.GetConsensusRollback()
	require.NotNil(t, rollback)
	roundTripped, err := rollback.Operation.UnmarshalNew()
	require.NoError(t, err)
	assert.True(t, proto.Equal(rollbackOp, roundTripped))
}
