package tokens

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

// recordingGossipSender records consensus gossip for assertions; delivery is
// asynchronous in production so the entrypoint result never depends on it.
type recordingGossipSender struct {
	messages []*pbgossip.GossipMessage
}

func (r *recordingGossipSender) CreateCommitAndSendGossipMessage(_ context.Context, msg *pbgossip.GossipMessage, _ []string) (*ent.Gossip, error) {
	r.messages = append(r.messages, msg)
	return nil, nil
}

var _ consensus.GossipSender = (*recordingGossipSender)(nil)

type consensusBroadcastTestSetup struct {
	*broadcastTokenPostgresTestSetup
	gossip *recordingGossipSender
}

// setUpConsensusBroadcastTest builds a single-operator (self-coordinator)
// setup with a real TwoPCEngine injected into ctx, exercising the consensus
// create path end-to-end through the public BroadcastTokenTransaction
// entrypoint: engine coordinator row, local Prepare, BuildCommitPayload,
// commit-decision write, and commit gossip dispatch.
func setUpConsensusBroadcastTest(t *testing.T) *consensusBroadcastTestSetup {
	t.Helper()

	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	coordinatorID := config.Identifier
	coordinatorPubKey := config.SigningOperatorMap[coordinatorID].IdentityPublicKey
	config.SigningOperatorMap = map[string]*so.SigningOperator{
		coordinatorID: {
			Identifier:        coordinatorID,
			IdentityPublicKey: coordinatorPubKey,
			ID:                config.SigningOperatorMap[coordinatorID].ID,
		},
	}
	config.Threshold = 1

	gossip := &recordingGossipSender{}
	engine := consensus.NewTwoPCEngine(config, gossip, db.NewDefaultSessionFactory(tc.Client))
	ctx = consensus.InjectEngine(ctx, engine)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenTransactionV3Enabled: 1,
		knobs.KnobUseConsensusTokenCreate:   1,
	}))

	setup := &broadcastTokenPostgresTestSetup{
		t:        t,
		handler:  NewBroadcastTokenHandler(config),
		config:   config,
		ctx:      ctx,
		client:   dbClient,
		root:     tc.Client,
		fixtures: entfixtures.New(t, ctx, dbClient),
	}
	setup.fixtures.CreateKeyshareWithEntityDkgKey()
	return &consensusBroadcastTestSetup{broadcastTokenPostgresTestSetup: setup, gossip: gossip}
}

func TestBroadcastTokenTransaction_ConsensusCreate_Success(t *testing.T) {
	s := setUpConsensusBroadcastTest(t)
	issuerPriv := s.fixtures.GeneratePrivateKey()
	req := s.signAndBuildRequest(s.buildCreatePartial(issuerPriv), issuerPriv)

	resp, err := s.handler.BroadcastTokenTransaction(s.ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.GetCommitStatus())
	assert.NotNil(t, resp.GetFinalTokenTransaction())
	assert.NotEmpty(t, resp.GetTokenIdentifier())

	// The engine committed the request transaction, so assert on a fresh
	// session-provided client.
	dbClient, err := ent.GetDbFromContext(s.ctx)
	require.NoError(t, err)

	// The transaction is FINALIZED on the coordinator.
	tx, err := dbClient.TokenTransaction.Query().
		Where(tokentransaction.StatusEQ(st.TokenTransactionStatusFinalized)).
		WithCreate().
		Only(s.ctx)
	require.NoError(t, err)
	assert.Equal(t, resp.GetTokenIdentifier(), tx.Edges.Create.TokenIdentifier)

	// The engine recorded a COMMITTED coordinator row for the token op type
	// and dispatched commit gossip carrying the flow execution id.
	flowRow, err := dbClient.FlowExecution.Query().
		Where(flowexecution.RoleEQ(st.FlowExecutionRoleCoordinator)).
		Only(s.ctx)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusCommitted, flowRow.Status)
	assert.Equal(t, int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_TOKEN_TRANSACTION), flowRow.OpType)

	require.Len(t, s.gossip.messages, 1)
	commit := s.gossip.messages[0].GetConsensusCommit()
	require.NotNil(t, commit)
	assert.Equal(t, pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_TOKEN_TRANSACTION, commit.GetOpType())
	assert.Equal(t, flowRow.ID.String(), commit.GetFlowExecutionId())
}

// TestBroadcastTokenTransaction_ConsensusCreate_WithExecuteBefore pins the
// execute_before threading: the deadline rides the partial into the prepare
// op and the cross-SO final hash (which every operator signs), so a create
// carrying it must finalize identically to one without.
func TestBroadcastTokenTransaction_ConsensusCreate_WithExecuteBefore(t *testing.T) {
	s := setUpConsensusBroadcastTest(t)
	issuerPriv := s.fixtures.GeneratePrivateKey()

	partial := s.buildCreatePartial(issuerPriv)
	partial.ExecuteBefore = timestamppb.New(utils.ToMicrosecondPrecision(time.Now().UTC().Add(time.Hour)))
	req := s.signAndBuildRequest(partial, issuerPriv)

	resp, err := s.handler.BroadcastTokenTransaction(s.ctx, req)
	require.NoError(t, err)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.GetCommitStatus())
	assert.NotEmpty(t, resp.GetTokenIdentifier())

	dbClient, err := ent.GetDbFromContext(s.ctx)
	require.NoError(t, err)
	tx, err := dbClient.TokenTransaction.Query().
		Where(tokentransaction.StatusEQ(st.TokenTransactionStatusFinalized)).
		Only(s.ctx)
	require.NoError(t, err)
	assert.Equal(t, partial.GetExecuteBefore().AsTime().UTC(), tx.ExecuteBefore.UTC())
}

func TestBroadcastTokenTransaction_ConsensusCreate_DuplicatePartialIsIdempotent(t *testing.T) {
	s := setUpConsensusBroadcastTest(t)
	issuerPriv := s.fixtures.GeneratePrivateKey()
	req := s.signAndBuildRequest(s.buildCreatePartial(issuerPriv), issuerPriv)
	// A real client re-sends the same bytes; clone up front because the
	// server constructs the final transaction from its unmarshalled request.
	replay, ok := proto.Clone(req).(*tokenpb.BroadcastTransactionRequest)
	require.True(t, ok)

	first, err := s.handler.BroadcastTokenTransaction(s.ctx, req)
	require.NoError(t, err)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, first.GetCommitStatus())

	// Replaying the identical partial reports the existing FINALIZED
	// transaction — including its token identifier — without executing
	// another consensus round.
	second, err := s.handler.BroadcastTokenTransaction(s.ctx, replay)
	require.NoError(t, err)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, second.GetCommitStatus())
	assert.Equal(t, first.GetTokenIdentifier(), second.GetTokenIdentifier())
	assert.Len(t, s.gossip.messages, 1)

	dbClient, err := ent.GetDbFromContext(s.ctx)
	require.NoError(t, err)
	count, err := dbClient.TokenTransaction.Query().Count(s.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestBroadcastTokenTransaction_ConsensusCreate_DuplicateTokenIdentifierFails(t *testing.T) {
	s := setUpConsensusBroadcastTest(t)
	issuerPriv := s.fixtures.GeneratePrivateKey()

	first := s.signAndBuildRequest(s.buildCreatePartial(issuerPriv), issuerPriv)
	_, err := s.handler.BroadcastTokenTransaction(s.ctx, first)
	require.NoError(t, err)

	// A distinct partial (explicitly later client timestamp, so the hashes
	// deterministically differ) for the same token metadata computes the same
	// token identifier and must be rejected in Prepare.
	secondPartial := s.buildCreatePartial(issuerPriv)
	secondPartial.TokenTransactionMetadata.ClientCreatedTimestamp = timestamppb.New(
		first.GetPartialTokenTransaction().GetTokenTransactionMetadata().GetClientCreatedTimestamp().AsTime().Add(time.Second),
	)
	second := s.signAndBuildRequest(secondPartial, issuerPriv)
	_, err = s.handler.BroadcastTokenTransaction(s.ctx, second)
	require.ErrorContains(t, err, "already created")
}

// TestBroadcastTokenTransaction_ConsensusCreate_AbortFreesIdentifierForResubmit
// pins the advertised failure contract: a create whose Prepare fan-out fails
// aborts with an error (never COMMIT_PROCESSING), commits no transaction rows,
// records a ROLLED_BACK coordinator FlowExecution row, and a resubmission of
// the same signed partial succeeds once the cluster recovers.
func TestBroadcastTokenTransaction_ConsensusCreate_AbortFreesIdentifierForResubmit(t *testing.T) {
	s := setUpConsensusBroadcastTest(t)
	issuerPriv := s.fixtures.GeneratePrivateKey()

	// Add an unreachable participant so the engine's Prepare fan-out fails and
	// the coordinator aborts the attempt.
	unreachableID := so.IndexToIdentifier(1)
	if unreachableID == s.config.Identifier {
		unreachableID = so.IndexToIdentifier(2)
	}
	s.config.SigningOperatorMap[unreachableID] = &so.SigningOperator{
		Identifier:                unreachableID,
		IdentityPublicKey:         keys.GeneratePrivateKey().Public(),
		AddressRpc:                "127.0.0.1:1",
		OperatorConnectionFactory: &sparktesting.DangerousTestOperatorConnectionFactoryNoTLS{},
	}

	req := s.signAndBuildRequest(s.buildCreatePartial(issuerPriv), issuerPriv)
	retry, ok := proto.Clone(req).(*tokenpb.BroadcastTransactionRequest)
	require.True(t, ok)

	_, err := s.handler.BroadcastTokenTransaction(s.ctx, req)
	require.ErrorContains(t, err, "consensus token create failed")

	dbClient, err := ent.GetDbFromContext(s.ctx)
	require.NoError(t, err)
	count, err := dbClient.TokenTransaction.Query().Count(s.ctx)
	require.NoError(t, err)
	assert.Zero(t, count, "an aborted create must not leave committed transaction rows")
	flowRow, err := dbClient.FlowExecution.Query().
		Where(flowexecution.RoleEQ(st.FlowExecutionRoleCoordinator)).
		Only(s.ctx)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, flowRow.Status)

	// Cluster recovers (the unreachable participant leaves the operator set);
	// resubmitting the identical signed partial succeeds.
	delete(s.config.SigningOperatorMap, unreachableID)
	resp, err := s.handler.BroadcastTokenTransaction(s.ctx, retry)
	require.NoError(t, err)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.GetCommitStatus())
	assert.NotEmpty(t, resp.GetTokenIdentifier())
}

// TestBroadcastTokenTransaction_ConsensusKnobLeavesTransferOnLegacyPath proves
// the consensus gate only captures creates: with the consensus knob on, a
// transfer still completes through the legacy phase-2 pipeline (no engine is
// injected, so a consensus routing would fail loudly).
func TestBroadcastTokenTransaction_ConsensusKnobLeavesTransferOnLegacyPath(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenTransactionV3Enabled:       1,
		knobs.KnobTokenTransactionV3Phase2Enabled: 1,
		knobs.KnobUseConsensusTokenCreate:         1,
	}))

	ownerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	_, outputs := setup.fixtures.CreateMintTransaction(
		tokenCreate,
		entfixtures.OutputSpecsWithOwner(ownerPriv.Public(), big.NewInt(100)),
		st.TokenTransactionStatusFinalized,
	)
	setup.fixtures.CreateKeyshare()

	partial := setup.buildTransferPartial(ownerPriv, tokenCreate, outputs[0])
	req := setup.signAndBuildRequest(partial, ownerPriv)

	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp.GetCommitStatus())
}

// TestBroadcastTokenTransaction_ConsensusKnobLeavesMintOnLegacyPath proves the
// consensus gate only captures creates: with the consensus knob on (and no
// engine injected, so a consensus routing would fail loudly), a mint still
// completes through the legacy phase-2 pipeline.
func TestBroadcastTokenTransaction_ConsensusKnobLeavesMintOnLegacyPath(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenTransactionV3Enabled:       1,
		knobs.KnobTokenTransactionV3Phase2Enabled: 1,
		knobs.KnobUseConsensusTokenCreate:         1,
	}))

	issuerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	setup.fixtures.CreateKeyshare()
	partial := setup.buildMintPartial(issuerPriv, tokenCreate)
	req := setup.signAndBuildRequest(partial, issuerPriv)

	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.GetCommitStatus())
}

// TestBroadcastTokenTransaction_ConsensusKnobOffUsesLegacyPath proves the
// legacy path stays intact when the knob is off: with no engine injected, a
// create routes to the legacy pipeline.
func TestBroadcastTokenTransaction_ConsensusKnobOffUsesLegacyPath(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	setup.fixtures.CreateKeyshareWithEntityDkgKey()
	issuerPriv := setup.fixtures.GeneratePrivateKey()
	req := setup.signAndBuildRequest(setup.buildCreatePartial(issuerPriv), issuerPriv)
	replay, ok := proto.Clone(req).(*tokenpb.BroadcastTransactionRequest)
	require.True(t, ok)

	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.GetCommitStatus())

	// A legacy replay of the finalized create reports the token identifier,
	// matching the fresh-create and consensus-replay response shapes.
	replayResp, err := setup.handler.BroadcastTokenTransaction(ctx, replay)
	require.NoError(t, err)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, replayResp.GetCommitStatus())
	assert.Equal(t, resp.GetTokenIdentifier(), replayResp.GetTokenIdentifier())
	assert.NotEmpty(t, replayResp.GetTokenIdentifier())
}
