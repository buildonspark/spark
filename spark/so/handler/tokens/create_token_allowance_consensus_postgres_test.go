package tokens_test

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenallowance"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/handler"
	tokenhandler "github.com/lightsparkdev/spark/so/handler/tokens"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/task"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type allowanceConsensusNode struct {
	ctx            context.Context
	client         *ent.Client
	session        ent.Session
	config         *so.Config
	knobs          knobs.Knobs
	prepareStarted chan struct{}
	releasePrepare <-chan struct{}
}

func (n *allowanceConsensusNode) inSession(ctx context.Context, fn func(context.Context) error) error {
	session := db.NewDefaultSessionFactory(n.client).NewSession(ctx)
	sessionCtx := ent.Inject(ctx, session)
	sessionCtx = knobs.InjectKnobsService(sessionCtx, n.knobs)
	err := fn(sessionCtx)
	tx := session.GetTxIfExists()
	if err != nil {
		if tx != nil {
			_ = tx.Rollback()
		}
		return err
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}

type allowanceConsensusServer struct {
	pbinternal.UnimplementedSparkInternalServiceServer
	node *allowanceConsensusNode
}

func (s *allowanceConsensusServer) ConsensusPrepare(ctx context.Context, req *pbinternal.ConsensusPrepareRequest) (*pbinternal.ConsensusPrepareResponse, error) {
	if s.node.prepareStarted != nil {
		close(s.node.prepareStarted)
		<-s.node.releasePrepare
	}
	var result *anypb.Any
	err := s.node.inSession(ctx, func(sessionCtx context.Context) error {
		var err error
		result, err = handler.NewConsensusHandler(s.node.config).DispatchPrepare(
			sessionCtx,
			pbgossip.ConsensusOperationType(req.GetOpType()),
			req.GetOperation(),
			req.GetFlowExecutionId(),
			uint(req.GetCoordinatorIndex()),
		)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &pbinternal.ConsensusPrepareResponse{Result: result}, nil
}

func (s *allowanceConsensusServer) ConsensusQueryOutcome(ctx context.Context, req *pbinternal.ConsensusQueryOutcomeRequest) (*pbinternal.ConsensusQueryOutcomeResponse, error) {
	var response *pbinternal.ConsensusQueryOutcomeResponse
	err := s.node.inSession(ctx, func(sessionCtx context.Context) error {
		var err error
		response, err = handler.NewConsensusQueryHandler(s.node.config).QueryOutcome(sessionCtx, req)
		return err
	})
	return response, err
}

type synchronousAllowanceGossip struct {
	nodes       map[string]*allowanceConsensusNode
	coordinator string
	skipCommit  map[string]bool
}

func (g *synchronousAllowanceGossip) CreateCommitAndSendGossipMessage(
	ctx context.Context,
	msg *pbgossip.GossipMessage,
	participants []string,
) (*ent.Gossip, error) {
	for _, identifier := range participants {
		if msg.GetConsensusCommit() != nil && g.skipCommit[identifier] {
			continue
		}
		node, ok := g.nodes[identifier]
		if !ok {
			return nil, fmt.Errorf("unknown gossip participant %s", identifier)
		}
		if err := node.inSession(ctx, func(sessionCtx context.Context) error {
			return handler.NewGossipHandler(node.config).HandleGossipMessage(sessionCtx, msg, identifier == g.coordinator)
		}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

type allowanceConsensusCluster struct {
	nodes   []*allowanceConsensusNode
	byID    map[string]*allowanceConsensusNode
	gossip  *synchronousAllowanceGossip
	tokenID []byte
}

func newAllowanceConsensusCluster(t *testing.T, tokenPresent []bool) *allowanceConsensusCluster {
	t.Helper()
	require.Len(t, tokenPresent, 3)

	operators := make(map[string]*so.SigningOperator, len(tokenPresent))
	for i := range tokenPresent {
		identifier := so.IndexToIdentifier(uint32(i))
		operators[identifier] = &so.SigningOperator{
			ID:                        uint64(i),
			Identifier:                identifier,
			IdentityPublicKey:         keys.GeneratePrivateKey().Public(),
			OperatorConnectionFactory: &sparktesting.DangerousTestOperatorConnectionFactoryNoTLS{},
		}
	}

	cluster := &allowanceConsensusCluster{
		nodes: make([]*allowanceConsensusNode, 0, len(tokenPresent)),
		byID:  make(map[string]*allowanceConsensusNode, len(tokenPresent)),
	}
	for i := range tokenPresent {
		ctx, tc := db.ConnectToTestPostgres(t)
		identifier := so.IndexToIdentifier(uint32(i))
		cfg := sparktesting.TestConfig(t)
		cfg.Index = uint64(i)
		cfg.Identifier = identifier
		cfg.SigningOperatorMap = operators
		nodeKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobTokenAllowancesEnabled: 1,
		})
		ctx = knobs.InjectKnobsService(ctx, nodeKnobs)
		node := &allowanceConsensusNode{
			ctx:     ctx,
			client:  tc.Client,
			session: tc.Session,
			config:  cfg,
			knobs:   nodeKnobs,
		}
		cluster.nodes = append(cluster.nodes, node)
		cluster.byID[identifier] = node
	}

	for _, node := range cluster.nodes {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		server := grpc.NewServer()
		pbinternal.RegisterSparkInternalServiceServer(server, &allowanceConsensusServer{node: node})
		node.config.SigningOperatorMap[node.config.Identifier].AddressRpc = listener.Addr().String()
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(server.Stop)
		t.Cleanup(func() { _ = listener.Close() })
	}

	cluster.tokenID = make([]byte, 32)
	_, err := cryptorand.Read(cluster.tokenID)
	require.NoError(t, err)
	for i, present := range tokenPresent {
		if !present {
			continue
		}
		fixtures := entfixtures.New(t, cluster.nodes[i].ctx, cluster.nodes[i].client)
		fixtures.CreateTokenCreateWithOpts(btcnetwork.Regtest, entfixtures.TokenCreateOpts{
			TokenIdentifier: cluster.tokenID,
		})
		require.NoError(t, ent.DbCommit(cluster.nodes[i].ctx))
	}

	coordinator := cluster.nodes[0].config.Identifier
	cluster.gossip = &synchronousAllowanceGossip{
		nodes:       cluster.byID,
		coordinator: coordinator,
		skipCommit:  make(map[string]bool),
	}
	return cluster
}

func allowanceU128(value uint64) []byte {
	return new(big.Int).SetUint64(value).FillBytes(make([]byte, 16))
}

func newConsensusAllowanceRequest(t *testing.T, tokenID []byte, owner keys.Private, allowanceID uuid.UUID) *tokenpb.CreateTokenAllowanceRequest {
	t.Helper()
	payload := &tokenpb.TokenAllowancePayload{
		Version:                1,
		AllowanceId:            allowanceID[:],
		OwnerPublicKey:         owner.Public().Serialize(),
		SpenderPublicKey:       keys.GeneratePrivateKey().Public().Serialize(),
		TokenIdentifier:        tokenID,
		PerTransactionCap:      allowanceU128(10_000),
		TotalLimit:             allowanceU128(100_000),
		ExpiryTime:             timestamppb.New(time.Now().Add(24 * time.Hour)),
		Network:                sparkpb.Network_REGTEST,
		OwnerProvidedTimestamp: uint64(time.Now().Add(-10 * time.Second).UnixMilli()),
	}
	hash, err := utils.HashCreateTokenAllowancePayload(payload)
	require.NoError(t, err)
	return &tokenpb.CreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   ecdsa.Sign(owner.ToBTCEC(), hash).Serialize(),
	}
}

func (c *allowanceConsensusCluster) executeCreate(req *tokenpb.CreateTokenAllowanceRequest) error {
	coordinator := c.nodes[0]
	engine := consensus.NewTwoPCEngine(coordinator.config, c.gossip, db.NewDefaultSessionFactory(coordinator.client))
	ctx := consensus.InjectEngine(coordinator.ctx, engine)
	_, err := tokenhandler.NewAllowanceTokenHandler(coordinator.config).CreateTokenAllowance(ctx, req)
	return err
}

func TestCreateTokenAllowanceConsensusPrepareFailureRollsBackEveryOperator(t *testing.T) {
	cluster := newAllowanceConsensusCluster(t, []bool{true, true, false})
	allowanceID := uuid.New()
	req := newConsensusAllowanceRequest(t, cluster.tokenID, keys.GeneratePrivateKey(), allowanceID)

	err := cluster.executeCreate(req)
	require.Error(t, err)
	if tx := cluster.nodes[0].session.GetTxIfExists(); tx != nil {
		require.NoError(t, tx.Rollback())
	}

	for _, node := range cluster.nodes {
		exists, err := node.client.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Exist(t.Context())
		require.NoError(t, err)
		assert.False(t, exists, "operator %s retained a rolled-back grant", node.config.Identifier)
	}
}

func TestCreateTokenAllowanceConsensusDoesNotAdoptForeignInFlightRow(t *testing.T) {
	cluster := newAllowanceConsensusCluster(t, []bool{true, true, true})
	allowanceID := uuid.New()
	req := newConsensusAllowanceRequest(t, cluster.tokenID, keys.GeneratePrivateKey(), allowanceID)
	prepareAny, err := anypb.New(&pbinternal.CreateTokenAllowancePrepareRequest{OriginalRequest: req})
	require.NoError(t, err)

	ownerFlowID := uuid.New()
	ownerNode := cluster.nodes[1]
	require.NoError(t, ownerNode.inSession(t.Context(), func(ctx context.Context) error {
		_, err := handler.NewConsensusHandler(ownerNode.config).DispatchPrepare(
			ctx,
			pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CREATE_TOKEN_ALLOWANCE,
			prepareAny,
			ownerFlowID.String(),
			0,
		)
		return err
	}))

	retryErr := cluster.executeCreate(req)
	if tx := cluster.nodes[0].session.GetTxIfExists(); tx != nil {
		require.NoError(t, tx.Rollback())
	}

	rollbackAny, err := anypb.New(&pbinternal.CreateTokenAllowanceRollbackRequest{AllowanceId: allowanceID[:]})
	require.NoError(t, err)
	require.NoError(t, ownerNode.inSession(t.Context(), func(ctx context.Context) error {
		return handler.NewGossipHandler(ownerNode.config).HandleGossipMessage(ctx, &pbgossip.GossipMessage{
			MessageId: uuid.NewString(),
			Message: &pbgossip.GossipMessage_ConsensusRollback{
				ConsensusRollback: &pbgossip.GossipMessageConsensusRollback{
					OpType:          pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CREATE_TOKEN_ALLOWANCE,
					Operation:       rollbackAny,
					FlowExecutionId: ownerFlowID.String(),
				},
			},
		}, false)
	}))

	require.Error(t, retryErr)
	assert.Equal(t, codes.Aborted, status.Code(retryErr))
	for _, node := range cluster.nodes {
		exists, err := node.client.TokenAllowance.Query().
			Where(tokenallowance.AllowanceID(allowanceID)).
			Exist(t.Context())
		require.NoError(t, err)
		assert.False(t, exists, "operator %s retained the allowance", node.config.Identifier)

		committed, err := node.client.FlowExecution.Query().
			Where(
				flowexecution.OpTypeEQ(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CREATE_TOKEN_ALLOWANCE)),
				flowexecution.StatusEQ(st.FlowExecutionStatusCommitted),
			).
			Exist(t.Context())
		require.NoError(t, err)
		assert.False(t, committed, "operator %s recorded a committed retry", node.config.Identifier)
	}
}

func TestCreateTokenAllowanceConsensusHappyPathWritesEveryOperator(t *testing.T) {
	cluster := newAllowanceConsensusCluster(t, []bool{true, true, true})
	allowanceID := uuid.New()
	req := newConsensusAllowanceRequest(t, cluster.tokenID, keys.GeneratePrivateKey(), allowanceID)

	require.NoError(t, cluster.executeCreate(req))

	for _, node := range cluster.nodes {
		row, err := node.client.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(t.Context())
		require.NoError(t, err)
		assert.Equal(t, st.TokenAllowanceStatusActive, row.Status)
	}
}

func TestCreateTokenAllowanceConsensusDoesNotHoldCoordinatorLocksDuringFanout(t *testing.T) {
	cluster := newAllowanceConsensusCluster(t, []bool{true, true, true})
	coordinator := cluster.nodes[0]
	owner := keys.GeneratePrivateKey()

	seedTokenID := make([]byte, 32)
	_, err := cryptorand.Read(seedTokenID)
	require.NoError(t, err)
	fixtures := entfixtures.New(t, coordinator.ctx, coordinator.client)
	_, seedToken := fixtures.CreateTokenCreateWithOpts(btcnetwork.Regtest, entfixtures.TokenCreateOpts{
		TokenIdentifier: seedTokenID,
	})
	seedAllowanceID := uuid.New()
	seedReq := newConsensusAllowanceRequest(t, seedToken.TokenIdentifier, owner, seedAllowanceID)
	require.NoError(t, tokenhandler.ValidateAndApplyCreateAllowance(
		coordinator.ctx,
		coordinator.config,
		seedReq.GetAllowancePayload(),
		seedReq.GetOwnerSignature(),
	))
	require.NoError(t, ent.DbCommit(coordinator.ctx))

	prepareStarted := make(chan struct{})
	releasePrepare := make(chan struct{})
	cluster.nodes[1].prepareStarted = prepareStarted
	cluster.nodes[1].releasePrepare = releasePrepare

	createResult := make(chan error, 1)
	targetAllowanceID := uuid.New()
	targetReq := newConsensusAllowanceRequest(t, cluster.tokenID, owner, targetAllowanceID)
	go func() {
		createResult <- cluster.executeCreate(targetReq)
	}()
	<-prepareStarted

	_, quotaLockErr := coordinator.client.TokenAllowance.Query().
		Where(tokenallowance.AllowanceID(seedAllowanceID)).
		ForUpdate(sql.WithLockAction(sql.NoWait)).
		Only(t.Context())
	_, tokenLockErr := coordinator.client.TokenCreate.Query().
		Where(tokencreate.TokenIdentifier(cluster.tokenID)).
		ForUpdate(sql.WithLockAction(sql.NoWait)).
		Only(t.Context())
	close(releasePrepare)

	require.NoError(t, quotaLockErr)
	require.NoError(t, tokenLockErr)
	require.NoError(t, <-createResult)
	_, err = coordinator.client.TokenAllowance.Query().
		Where(tokenallowance.AllowanceID(targetAllowanceID)).
		Only(t.Context())
	require.NoError(t, err)
}

func TestCreateTokenAllowanceConsensusReconcilerCommitsStrandedParticipant(t *testing.T) {
	cluster := newAllowanceConsensusCluster(t, []bool{true, true, true})
	stranded := cluster.nodes[1]
	cluster.gossip.skipCommit[stranded.config.Identifier] = true
	allowanceID := uuid.New()
	req := newConsensusAllowanceRequest(t, cluster.tokenID, keys.GeneratePrivateKey(), allowanceID)
	require.NoError(t, cluster.executeCreate(req))

	row, err := stranded.client.FlowExecution.Query().
		Where(flowexecution.RoleEQ(st.FlowExecutionRoleParticipant)).
		Only(t.Context())
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, row.Status)
	_, err = stranded.client.FlowExecution.UpdateOneID(row.ID).
		SetUpdateTime(time.Now().Add(-time.Hour)).
		Save(stranded.ctx)
	require.NoError(t, err)
	require.NoError(t, ent.DbCommit(stranded.ctx))

	reconcileKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobFlowExecutionStuckThreshold:  1,
		knobs.KnobFlowExecutionSweepBatchLimit: 10,
	})
	err = task.ReconcileStuckParticipantFlows(stranded.ctx, stranded.config, reconcileKnobs)
	require.ErrorContains(t, err, "stuck PARTICIPANT flow_execution rows")

	row, err = stranded.client.FlowExecution.Get(t.Context(), row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status)
	allowance, err := stranded.client.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(t.Context())
	require.NoError(t, err)
	assert.Nil(t, allowance.FlowExecutionID)
}
