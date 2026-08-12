package consensus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestMain(m *testing.M) {
	stop := db.StartPostgresServer()
	defer stop()

	m.Run()
}

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

func (m *mockGossipSender) CreateCommitAndSendGossipMessageWithClient(_ context.Context, _ *ent.Client, msg *pbgossip.GossipMessage, participants []string) (*ent.Gossip, error) {
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
	ctx, tc := db.NewTestSQLiteContext(t)
	return engineForTestContext(ctx, tc)
}

// newTestPostgresEngine is newTestEngine on a real Postgres database, for
// tests that exercise row-locking semantics (readBackDecisionStatusFromDB's
// FOR UPDATE) which SQLite cannot express.
func newTestPostgresEngine(t *testing.T) (context.Context, *TwoPCEngine, *mockGossipSender, *ent.Client, *so.Config) {
	t.Helper()
	ctx, tc := db.ConnectToTestPostgres(t)
	return engineForTestContext(ctx, tc)
}

func engineForTestContext(ctx context.Context, tc *db.TestContext) (context.Context, *TwoPCEngine, *mockGossipSender, *ent.Client, *so.Config) {
	gs := &mockGossipSender{}
	config := testConfig()
	// Engine takes a SessionFactory (mirroring production) so its
	// bookkeeping writes flow through the same Begin/Save/Commit
	// machinery the rest of the codebase uses.
	return ctx, NewTwoPCEngine(config, gs, db.NewDefaultSessionFactory(tc.Client)), gs, tc.Client, config
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
	roundTripped, err := commit.GetOperation().UnmarshalNew()
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
	roundTripped, err := commit.GetOperation().UnmarshalNew()
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
	roundTripped, err := rollback.GetOperation().UnmarshalNew()
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
	assert.Equal(t, row.ID.String(), commit.GetFlowExecutionId())
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
	assert.Equal(t, row.ID.String(), rollback.GetFlowExecutionId())
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

// --- CAS conflict tests (recordCommitDecision vs. self-sweep race) ---

// TestExecute_RecordCommitDecision_PreemptedByExternalRollback simulates the
// race where the coordinator self-sweep transitions the row to ROLLED_BACK
// after the engine started Execute but before it records its commit decision.
// The CAS in recordCommitDecision detects the preemption: Execute must NOT
// commit the request tx (so the coordinator's domain work is rolled back, not
// stranded), must NOT send commit gossip, and instead dispatches rollback
// gossip so both sides converge on rolled-back.
func TestExecute_RecordCommitDecision_PreemptedByExternalRollback(t *testing.T) {
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

	_, err := NewTwoPCEngine(config, gs, db.NewDefaultSessionFactory(client)).Execute(ctx, testOpType, selfSelection(t, config), preempt)
	require.ErrorIs(t, err, ErrCoordinatorRowPreempted, "Execute must propagate the preemption")

	// Execute rolls back the doomed request tx on the preempted path, so no
	// dangling tx remains; the assertion query below reads committed state.

	// Row stays ROLLED_BACK — recordCommitDecision's conditional UPDATE matched
	// zero rows, leaving the sweep's transition intact.
	rows, err := client.FlowExecution.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, rows[0].Status,
		"sweep-driven ROLLED_BACK must not be clobbered by recordCommitDecision")

	// No commit gossip, and rollback gossip dispatched so peers converge.
	var sawRollback bool
	for _, c := range gs.calls {
		assert.Nil(t, c.msg.GetConsensusCommit(),
			"no ConsensusCommit gossip must be sent after a preemption")
		if c.msg.GetConsensusRollback() != nil {
			sawRollback = true
		}
	}
	assert.True(t, sawRollback, "Execute must dispatch rollback gossip on preemption")
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
// ROLLED_BACK out of band, so the engine's subsequent recordCommitDecision
// hits a CAS conflict.
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

// --- Cancellation resilience tests ---

// cancelDuringPrepareFlow models the bug case: the user (or anything else
// holding a cancellable parent of the request ctx) cancels in the middle of
// Prepare. The engine's pre-fix behavior was to lose the coordinator row
// entirely because its bookkeeping ran in the request session's tx; the
// post-fix behavior is to drive the row to ROLLED_BACK and dispatch
// rollback gossip on a detached cleanup ctx.
type cancelDuringPrepareFlow struct {
	cancel  context.CancelFunc
	payload proto.Message
}

func (f *cancelDuringPrepareFlow) Prepare(_ context.Context, _ proto.Message) (proto.Message, error) {
	f.cancel()
	return nil, context.Canceled
}

func (f *cancelDuringPrepareFlow) Commit(_ context.Context, _ proto.Message) error   { return nil }
func (f *cancelDuringPrepareFlow) Rollback(_ context.Context, _ proto.Message) error { return nil }
func (f *cancelDuringPrepareFlow) PrepareOp() proto.Message                          { return f.payload }
func (f *cancelDuringPrepareFlow) PrepareTask(_ context.Context, _ *so.SigningOperator) (proto.Message, error) {
	return nil, context.Canceled
}
func (f *cancelDuringPrepareFlow) BuildCommitPayload(_ context.Context, _ map[string]*anypb.Any) (proto.Message, error) {
	return f.payload, nil
}
func (f *cancelDuringPrepareFlow) RollbackPayload() proto.Message { return f.payload }

var _ CoordinatorFlow = (*cancelDuringPrepareFlow)(nil)

func TestExecute_UserCancelDuringPrepare_RowReachesRolledBackDurably(t *testing.T) {
	parentCtx, engine, gs, client, config := newTestEngine(t)
	// The cancellable ctx is what we'd pass into a gRPC handler.
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}
	flow := &cancelDuringPrepareFlow{cancel: cancel, payload: rollbackOp}

	_, err := engine.Execute(ctx, testOpType, selfSelection(t, config), flow)
	require.Error(t, err, "Execute should report the prepare failure to the caller")
	assert.Contains(t, err.Error(), "prepare failed")

	// Read via the unwrapped client (NOT the request ctx) — proves the
	// row hit disk through the engine's own dbClient and isn't tied to
	// the cancelled request.
	rows, err := client.FlowExecution.Query().All(parentCtx)
	require.NoError(t, err)
	require.Len(t, rows, 1, "coordinator row must exist even though the request was cancelled")
	assert.Equal(t, st.FlowExecutionStatusRolledBack, rows[0].Status,
		"engine must drive the row to ROLLED_BACK on the cleanup ctx")
	require.NotNil(t, rows[0].DecisionPayload)
	assert.True(t, proto.Equal(rollbackOp, payloadFromAnyBytes(t, *rows[0].DecisionPayload)),
		"row must carry the rollback payload that participants will see via reconcile")

	// Rollback gossip was dispatched even though the originating ctx was cancelled.
	require.Len(t, gs.calls, 1)
	assert.NotNil(t, gs.calls[0].msg.GetConsensusRollback())
}

// cancelDuringBuildCommitFlow cancels the user ctx after Prepare succeeds —
// modelling a cancel that arrives between fan-out and BuildCommitPayload.
type cancelDuringBuildCommitFlow struct {
	cancel     context.CancelFunc
	rollbackOp proto.Message
}

func (f *cancelDuringBuildCommitFlow) Prepare(_ context.Context, _ proto.Message) (proto.Message, error) {
	return nil, nil
}
func (f *cancelDuringBuildCommitFlow) Commit(_ context.Context, _ proto.Message) error   { return nil }
func (f *cancelDuringBuildCommitFlow) Rollback(_ context.Context, _ proto.Message) error { return nil }
func (f *cancelDuringBuildCommitFlow) PrepareOp() proto.Message                          { return f.rollbackOp }
func (f *cancelDuringBuildCommitFlow) PrepareTask(_ context.Context, _ *so.SigningOperator) (proto.Message, error) {
	return nil, nil
}
func (f *cancelDuringBuildCommitFlow) BuildCommitPayload(_ context.Context, _ map[string]*anypb.Any) (proto.Message, error) {
	f.cancel()
	return nil, context.Canceled
}
func (f *cancelDuringBuildCommitFlow) RollbackPayload() proto.Message { return f.rollbackOp }

var _ CoordinatorFlow = (*cancelDuringBuildCommitFlow)(nil)

func TestExecute_UserCancelDuringBuildCommit_RowReachesRolledBackDurably(t *testing.T) {
	parentCtx, engine, gs, client, config := newTestEngine(t)
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}
	flow := &cancelDuringBuildCommitFlow{cancel: cancel, rollbackOp: rollbackOp}

	_, err := engine.Execute(ctx, testOpType, selfSelection(t, config), flow)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "build-commit failed")

	rows, err := client.FlowExecution.Query().All(parentCtx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, rows[0].Status)

	require.Len(t, gs.calls, 1)
	assert.NotNil(t, gs.calls[0].msg.GetConsensusRollback(),
		"rollback gossip must dispatch even though the request ctx was cancelled mid-flow")
}

func TestExecute_CommitDecisionDurablyCommittedOnSuccess(t *testing.T) {
	// In the atomic-commit model, Execute writes the COMMITTED decision into
	// the request transaction and commits it — together with the coordinator's
	// domain work — via a single internal ent.DbCommit before returning on
	// success. This asserts that the decision lands on disk durably: a
	// session-less read through the bare client (no request tx in scope) sees
	// the COMMITTED row and the commit payload, which is what lets participants
	// reconcile to a real outcome via ConsensusQueryOutcome. Durability of the
	// row across a request-tx rollback/cancellation mid-flow is covered
	// separately by TestExecute_UserCancelDuringBuildCommit_RowReachesRolledBackDurably.
	ctx, engine, gs, client, config := newTestEngine(t)
	commitOp := &pbgossip.GossipMessage{MessageId: "commit-payload"}

	_, err := engine.Execute(ctx, testOpType, selfSelection(t, config),
		&aggregatingFlow{rollbackOp: &pbgossip.GossipMessage{MessageId: "rb"}, commitResult: commitOp})
	require.NoError(t, err)
	require.Len(t, gs.calls, 1)

	// Read through a session-less context so the row is fetched via the bare
	// client alone — proving Execute's internal DbCommit already persisted it,
	// with no open request tx required.
	rows, err := client.FlowExecution.Query().All(parentlessCtx())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, st.FlowExecutionStatusCommitted, rows[0].Status)
	require.NotNil(t, rows[0].DecisionPayload)
	assert.True(t, proto.Equal(commitOp, payloadFromAnyBytes(t, *rows[0].DecisionPayload)))
}

// parentlessCtx returns a fresh context with no DB session attached —
// emphasizes that the post-rollback read goes through the bare client
// alone, with no session in scope.
func parentlessCtx() context.Context { return context.Background() }

// --- Ambiguous commit-outcome tests ---
//
// These exercise transaction-commit ambiguity, which is invisible at the gRPC
// boundary: tx.Commit() returns the same error whether the transaction aborted
// or durably committed and then lost its acknowledgement (DB failover, killed
// connection, or the request ctx cancelled mid-commit), and there is no way to
// induce a committed-but-reported-failed tx deterministically through the public
// API. Execute is the engine's public entry point; the injected seams
// (commitCoordinatorTx, readBackDecisionStatus) are at the DB/infrastructure
// boundary — the same style of system-boundary injection the reconciler uses
// for outcomeQueryFunc. The primary assertions are on the engine's observable
// output (the gossip it dispatches to participants); the durable row status is
// secondary corroboration.

// TestExecute_AmbiguousCommit_TxDurablyCommitted_HonorsCommit models the core
// vulnerability: the request tx durably COMMITs but tx.Commit() reports an
// error. The engine must read back the COMMITTED decision and dispatch COMMIT
// gossip — never rollback — so participants converge with the committed
// coordinator instead of terminally rolling back a committed transfer.
func TestExecute_AmbiguousCommit_TxDurablyCommitted_HonorsCommit(t *testing.T) {
	// Postgres: the real read-back locks the row FOR UPDATE, which SQLite
	// cannot express.
	ctx, engine, gs, client, config := newTestPostgresEngine(t)
	commitOp := &pbgossip.GossipMessage{MessageId: "commit-payload"}
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}

	// Actually commit the request tx (so the decision lands durably as
	// COMMITTED) but return an error, simulating a lost commit acknowledgement.
	engine.commitCoordinatorTx = func(commitCtx context.Context) error {
		if err := ent.DbCommit(commitCtx); err != nil {
			return err
		}
		return fmt.Errorf("simulated lost commit acknowledgement")
	}

	result, err := engine.Execute(ctx, testOpType, selfSelection(t, config),
		&aggregatingFlow{rollbackOp: rollbackOp, commitResult: commitOp})
	require.NoError(t, err, "an ambiguous commit that durably applied must be honored, not surfaced as an error")
	assert.True(t, proto.Equal(commitOp, result))

	// COMMIT gossip dispatched, no ROLLBACK gossip.
	require.Len(t, gs.calls, 1)
	assert.NotNil(t, gs.calls[0].msg.GetConsensusCommit(),
		"a durably-committed decision must dispatch COMMIT gossip")
	assert.Nil(t, gs.calls[0].msg.GetConsensusRollback(),
		"a durably-committed decision must NEVER dispatch ROLLBACK gossip")

	// Durable row is COMMITTED.
	rows, err := client.FlowExecution.Query().All(parentlessCtx())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, st.FlowExecutionStatusCommitted, rows[0].Status)
}

// TestExecute_AmbiguousCommit_TxDidNotCommit_RollsBack confirms the other side:
// when the request tx genuinely did not apply (the decision UPDATE was rolled
// back with it, so the row reverts to the IN_FLIGHT baseline written at
// creation), the read-back sees IN_FLIGHT and the engine rolls back — the
// correct behavior, preserved.
func TestExecute_AmbiguousCommit_TxDidNotCommit_RollsBack(t *testing.T) {
	// Postgres: the real read-back locks the row FOR UPDATE, which SQLite
	// cannot express.
	ctx, engine, gs, client, config := newTestPostgresEngine(t)
	commitOp := &pbgossip.GossipMessage{MessageId: "commit-payload"}
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}

	engine.commitCoordinatorTx = func(commitCtx context.Context) error {
		// The real request tx aborts: roll it back, discarding the COMMITTED
		// decision UPDATE so the row reverts to its committed IN_FLIGHT baseline.
		require.NoError(t, ent.DbRollback(commitCtx))
		return fmt.Errorf("commit failed")
	}

	result, err := engine.Execute(ctx, testOpType, selfSelection(t, config),
		&aggregatingFlow{rollbackOp: rollbackOp, commitResult: commitOp})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request tx commit failed")
	assert.Nil(t, result)

	require.Len(t, gs.calls, 1)
	assert.NotNil(t, gs.calls[0].msg.GetConsensusRollback(),
		"a tx that did not commit must dispatch ROLLBACK gossip")
	assert.Nil(t, gs.calls[0].msg.GetConsensusCommit())

	rows, err := client.FlowExecution.Query().All(parentlessCtx())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, rows[0].Status)
}

// TestExecute_AmbiguousCommit_ReadBackFails_SendsNoGossip covers the
// unresolvable case: the commit errored AND the durable-status read-back also
// failed. The engine must send NO gossip — a wrong rollback over a possibly-
// committed decision is unrecoverable, whereas silence lets the participant
// reconciler resolve peers against the coordinator's true outcome later.
func TestExecute_AmbiguousCommit_ReadBackFails_SendsNoGossip(t *testing.T) {
	ctx, engine, gs, _, config := newTestEngine(t)
	commitOp := &pbgossip.GossipMessage{MessageId: "commit-payload"}
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}

	engine.commitCoordinatorTx = func(commitCtx context.Context) error {
		if err := ent.DbCommit(commitCtx); err != nil {
			return err
		}
		return fmt.Errorf("simulated lost commit acknowledgement")
	}
	engine.readBackDecisionStatus = func(context.Context, uuid.UUID) (st.FlowExecutionStatus, error) {
		return "", fmt.Errorf("database unreachable")
	}

	result, err := engine.Execute(ctx, testOpType, selfSelection(t, config),
		&aggregatingFlow{rollbackOp: rollbackOp, commitResult: commitOp})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outcome unknown")
	assert.Nil(t, result)

	assert.Empty(t, gs.calls,
		"when the commit outcome is unresolvable the engine must send neither COMMIT nor ROLLBACK gossip")
}

// TestExecute_AmbiguousCommit_StaleInFlightReadBack_HonorsCommittedDecision:
// the request tx durably COMMITTED (e.g. the client cancelled mid-commit and
// the COMMIT still landed), but the ambiguity read-back raced the still-landing
// COMMIT and observed the stale IN_FLIGHT row version, sending the engine down
// the rollback path. Once that path's CAS proves the decision is durably
// COMMITTED, the engine must honor it — dispatch COMMIT gossip and return
// success, exactly as if the read-back had seen COMMITTED directly — rather
// than withhold all gossip and strand participants IN_FLIGHT until the
// reconciler pulls the outcome.
func TestExecute_AmbiguousCommit_StaleInFlightReadBack_HonorsCommittedDecision(t *testing.T) {
	ctx, engine, gs, client, config := newTestEngine(t)
	commitOp := &pbgossip.GossipMessage{MessageId: "commit-payload"}
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}

	// The tx durably commits but reports an error (lost ack).
	engine.commitCoordinatorTx = func(commitCtx context.Context) error {
		if err := ent.DbCommit(commitCtx); err != nil {
			return err
		}
		return fmt.Errorf("simulated lost commit acknowledgement")
	}
	// The read-back returns the stale pre-commit snapshot: IN_FLIGHT. In prod
	// this happens when the read races a COMMIT that is still landing — MVCC
	// serves the old row version without blocking on the committing tx.
	engine.readBackDecisionStatus = func(context.Context, uuid.UUID) (st.FlowExecutionStatus, error) {
		return st.FlowExecutionStatusInFlight, nil
	}

	result, err := engine.Execute(ctx, testOpType, selfSelection(t, config),
		&aggregatingFlow{rollbackOp: rollbackOp, commitResult: commitOp})
	require.NoError(t, err,
		"a durably-COMMITTED decision discovered via the rollback CAS must be honored, not surfaced as an error")
	assert.True(t, proto.Equal(commitOp, result))

	// COMMIT gossip dispatched so participants converge without waiting for
	// the reconciler; never a rollback.
	require.Len(t, gs.calls, 1)
	assert.NotNil(t, gs.calls[0].msg.GetConsensusCommit(),
		"engine must dispatch COMMIT gossip after discovering the committed decision")
	assert.Nil(t, gs.calls[0].msg.GetConsensusRollback(),
		"a durably-committed decision must NEVER dispatch ROLLBACK gossip")

	rows, err := client.FlowExecution.Query().All(parentlessCtx())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, st.FlowExecutionStatusCommitted, rows[0].Status)
}

// TestExecute_AmbiguousCommit_RollbackSafetyUnconfirmable_SendsNoGossip covers
// the remaining ambiguous-commit branch: the read-back reports IN_FLIGHT but the
// rollback attempt can neither transition the row nor confirm its terminal
// status (here, the row is gone by the time markRolledBack runs). The engine
// must send NO gossip — it cannot justify a rollback and cannot prove a commit —
// and must report the commit outcome as unknown, not as a definite failure.
func TestExecute_AmbiguousCommit_RollbackSafetyUnconfirmable_SendsNoGossip(t *testing.T) {
	ctx, engine, gs, client, config := newTestEngine(t)
	commitOp := &pbgossip.GossipMessage{MessageId: "commit-payload"}
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}

	// The tx durably commits but reports an error, and the coordinator row
	// vanishes out-of-band so the rollback attempt's CAS matches nothing and
	// its re-read fails — the unconfirmable case.
	engine.commitCoordinatorTx = func(commitCtx context.Context) error {
		if err := ent.DbCommit(commitCtx); err != nil {
			return err
		}
		if _, err := client.FlowExecution.Delete().Exec(parentlessCtx()); err != nil {
			return err
		}
		return fmt.Errorf("simulated lost commit acknowledgement")
	}
	engine.readBackDecisionStatus = func(context.Context, uuid.UUID) (st.FlowExecutionStatus, error) {
		return st.FlowExecutionStatusInFlight, nil
	}

	result, err := engine.Execute(ctx, testOpType, selfSelection(t, config),
		&aggregatingFlow{rollbackOp: rollbackOp, commitResult: commitOp})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit outcome unknown",
		"an unconfirmable rollback must be reported as an unknown outcome, not a definite commit failure")
	assert.Nil(t, result)

	assert.Empty(t, gs.calls,
		"when rollback safety cannot be confirmed the engine must send neither COMMIT nor ROLLBACK gossip")
}

// TestReadBackDecisionStatus_WaitsOutInFlightCommit pins the transaction-level
// contract underneath resolveAmbiguousCommit: the read-back must not serve a
// stale MVCC snapshot while the request tx's COMMIT is still landing — it must
// block on the row lock until the in-flight tx resolves and then report the
// post-resolution status. This is tested below the Execute boundary because the
// contract is a row-locking semantic (FOR UPDATE vs. plain read) that is
// invisible at the API level except as a rare timing race; it requires real
// Postgres, since SQLite has neither row locks nor concurrent transactions.
func TestReadBackDecisionStatus_WaitsOutInFlightCommit(t *testing.T) {
	ctx, engine, _, client, _ := newTestPostgresEngine(t)

	// Committed baseline: an IN_FLIGHT coordinator row (as written at row creation).
	row, err := client.FlowExecution.Create().
		SetRole(st.FlowExecutionRoleCoordinator).
		SetOpType(int32(testOpType)).
		SetCoordinatorIndex(uint(testCoordinatorID)).
		SetStatus(st.FlowExecutionStatusInFlight).
		Save(parentlessCtx())
	require.NoError(t, err)

	// A separate tx plays the part of the request tx mid-commit: it has
	// written the COMMITTED decision (holding the row lock) but not yet
	// committed.
	lockTx, err := client.Tx(parentlessCtx())
	require.NoError(t, err)
	_, err = lockTx.FlowExecution.UpdateOneID(row.ID).
		SetStatus(st.FlowExecutionStatusCommitted).
		Save(parentlessCtx())
	require.NoError(t, err)

	// Resolve the in-flight tx shortly after the read-back has started; the
	// goroutine touches only lockTx, never the engine's session state.
	const commitDelay = 250 * time.Millisecond
	committed := make(chan error, 1)
	start := time.Now()
	go func() {
		time.Sleep(commitDelay)
		committed <- lockTx.Commit()
	}()

	status, err := engine.readBackDecisionStatusFromDB(ctx, row.ID)
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusCommitted, status,
		"read-back must wait out the in-flight COMMIT and report the resolved status, not the stale IN_FLIGHT snapshot")
	// Guard against a vacuous pass: a read that doesn't take the row lock
	// returns in milliseconds (with the stale IN_FLIGHT snapshot); only a
	// lock-waiting read can span the full commit delay.
	assert.GreaterOrEqual(t, elapsed, commitDelay,
		"read-back must have blocked on the in-flight tx's row lock, not returned early from a snapshot")
	require.NoError(t, <-committed)
}

// TestAttemptRollback_DurablyCommittedRow_RefusesGossip exercises the
// defense-in-depth guard directly: if a row is somehow already COMMITTED when
// attemptRollback runs (an invariant violation — no real caller passes a
// committed row), markRolledBack must report it as unsafe and attemptRollback
// must refuse to dispatch rollback gossip, so participants are never told to
// contradict a committed coordinator.
func TestAttemptRollback_DurablyCommittedRow_RefusesGossip(t *testing.T) {
	ctx, engine, gs, client, _ := newTestEngine(t)
	row, err := client.FlowExecution.Create().
		SetRole(st.FlowExecutionRoleCoordinator).
		SetOpType(int32(testOpType)).
		SetCoordinatorIndex(uint(testCoordinatorID)).
		SetStatus(st.FlowExecutionStatusCommitted).
		Save(ctx)
	require.NoError(t, err)

	// markRolledBack reports the committed row as unsafe to roll back.
	markErr := engine.markRolledBack(ctx, row)
	require.ErrorIs(t, markErr, errRollbackUnsafe)

	// attemptRollback therefore dispatches no rollback gossip...
	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}
	engine.attemptRollback(ctx, row, testOpType, &simpleFlow{payload: rollbackOp}, row.ID.String(), []string{"op-self"})
	assert.Empty(t, gs.calls,
		"attemptRollback must not gossip a rollback that would contradict a durably-COMMITTED coordinator")

	// ...and leaves the row COMMITTED (the CAS never matched it).
	updated, err := client.FlowExecution.Get(parentlessCtx(), row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusCommitted, updated.Status)
}

// TestAttemptRollback_ReadBackFails_RefusesGossip exercises markRolledBack's
// CAS-miss re-read-failure branch: when the CAS matches zero rows and the
// follow-up Get also fails (here, because the row no longer exists), markRolledBack
// returns a non-nil error and attemptRollback withholds rollback gossip — we can't
// confirm the coordinator is uncommitted, so convergence is left to the reconciler.
func TestAttemptRollback_ReadBackFails_RefusesGossip(t *testing.T) {
	ctx, engine, gs, client, _ := newTestEngine(t)
	row, err := client.FlowExecution.Create().
		SetRole(st.FlowExecutionRoleCoordinator).
		SetOpType(int32(testOpType)).
		SetCoordinatorIndex(uint(testCoordinatorID)).
		SetStatus(st.FlowExecutionStatusInFlight).
		Save(ctx)
	require.NoError(t, err)
	// Delete the row so the CAS matches nothing and the re-read Get fails
	// (NotFound) — the terminal status is unconfirmable.
	require.NoError(t, client.FlowExecution.DeleteOne(row).Exec(ctx))

	markErr := engine.markRolledBack(ctx, row)
	require.Error(t, markErr, "a CAS miss whose re-read fails must not be reported as safe")
	require.NotErrorIs(t, markErr, errRollbackUnsafe,
		"re-read failure is unconfirmable, distinct from the confirmed-COMMITTED sentinel")

	rollbackOp := &pbgossip.GossipMessage{MessageId: "rollback-payload"}
	engine.attemptRollback(ctx, row, testOpType, &simpleFlow{payload: rollbackOp}, row.ID.String(), []string{"op-self"})
	assert.Empty(t, gs.calls,
		"attemptRollback must withhold rollback gossip when the coordinator's terminal status cannot be confirmed")
}
