package grpc

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/knobs"

	"github.com/lightsparkdev/spark/so/task"

	pbmock "github.com/lightsparkdev/spark/proto/mock"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	"github.com/lightsparkdev/spark/so/ent/preimageshare"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/ent/usersignedtransaction"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// MockServer is a mock server for the Spark protocol.
type MockServer struct {
	config *so.Config
	pbmock.UnimplementedMockServiceServer
	mockAction *common.MockAction
	rootClient *ent.Client
}

// NewMockServer creates a new MockServer.
func NewMockServer(config *so.Config, mockAction *common.MockAction, rootClient *ent.Client) *MockServer {
	return &MockServer{config: config, mockAction: mockAction, rootClient: rootClient}
}

// CleanUpPreimageShare cleans up the preimage share for the given payment hash.
func (o *MockServer) CleanUpPreimageShare(ctx context.Context, req *pbmock.CleanUpPreimageShareRequest) (*emptypb.Empty, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	_, err = db.PreimageShare.Delete().Where(preimageshare.PaymentHashEQ(req.PaymentHash)).Exec(ctx)
	if err != nil {
		return nil, err
	}
	preimageRequestQuery := db.PreimageRequest.Query().Where(preimagerequest.PaymentHashEQ(req.PaymentHash))
	if preimageRequestQuery.CountX(ctx) == 0 {
		return nil, nil
	}
	preimageRequests, err := preimageRequestQuery.All(ctx)
	if err != nil {
		return nil, err
	}
	for _, preimageRequest := range preimageRequests {
		txs, err := preimageRequest.QueryTransactions().All(ctx)
		if err != nil {
			return nil, err
		}
		for _, tx := range txs {
			_, err = db.UserSignedTransaction.Delete().Where(usersignedtransaction.IDEQ(tx.ID)).Exec(ctx)
			if err != nil {
				return nil, err
			}
		}
	}
	_, err = db.PreimageRequest.Delete().Where(preimagerequest.PaymentHashEQ(req.PaymentHash)).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (o *MockServer) UpdateNodesStatus(ctx context.Context, req *pbmock.UpdateNodesStatusRequest) (*emptypb.Empty, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	nodeUUIDs := make([]uuid.UUID, 0)
	for _, nodeID := range req.NodeIds {
		nodeUUID, err := uuid.Parse(nodeID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id %s: %w", nodeID, err)
		}
		nodeUUIDs = append(nodeUUIDs, nodeUUID)
	}

	_, err = db.TreeNode.Update().SetStatus(st.TreeNodeStatus(req.Status)).Where(treenode.IDIn(nodeUUIDs...)).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update nodes: %w", err)
	}
	return &emptypb.Empty{}, nil
}

// TriggerTask executes a scheduled task immediately. Primarily used from hermetic tests to avoid relying on gocron timing.
func (o *MockServer) TriggerTask(_ context.Context, req *pbmock.TriggerTaskRequest) (*emptypb.Empty, error) {
	taskName := req.GetTaskName()
	var selected *task.ScheduledTaskSpec
	for _, t := range task.AllScheduledTasks() {
		if t.Name == taskName {
			selected = &t
			break
		}
	}
	if selected == nil {
		return nil, status.Errorf(codes.NotFound, "unknown task: %s", taskName)
	}
	// Use the operator's root *ent.Client instead of the transactional one because RunOnce expects *ent.Client.
	dbClient := o.rootClient
	if err := selected.RunOnce(o.config, dbClient, knobs.NewFixedKnobs(map[string]float64{})); err != nil {
		return nil, status.Errorf(codes.Internal, "task %s failed: %v", taskName, err)
	}

	return &emptypb.Empty{}, nil
}

func (o *MockServer) InterruptCoopExit(_ context.Context, req *pbmock.InterruptCoopExitRequest) (*emptypb.Empty, error) {
	switch req.Action {
	case pbmock.InterruptCoopExitRequest_INTERRUPT:
		o.mockAction.InterruptCoopExit = true
		if req.TargetOperator != "" {
			o.mockAction.TargetOperatorID = req.TargetOperator
		}
	case pbmock.InterruptCoopExitRequest_RESUME:
		o.mockAction.InterruptCoopExit = false
	default:
		return nil, status.Errorf(codes.InvalidArgument, "invalid interrupt coop-exit action: %v", req.Action)
	}
	return &emptypb.Empty{}, nil
}
