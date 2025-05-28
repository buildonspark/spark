package grpc

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"

	pbmock "github.com/lightsparkdev/spark/proto/mock"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	"github.com/lightsparkdev/spark/so/ent/preimageshare"
	"github.com/lightsparkdev/spark/so/ent/schema"
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
}

// NewMockServer creates a new MockServer.
func NewMockServer(config *so.Config, mockAction *common.MockAction) *MockServer {
	return &MockServer{config: config, mockAction: mockAction}
}

// CleanUpPreimageShare cleans up the preimage share for the given payment hash.
func (o *MockServer) CleanUpPreimageShare(ctx context.Context, req *pbmock.CleanUpPreimageShareRequest) (*emptypb.Empty, error) {
	db := ent.GetDbFromContext(ctx)
	_, err := db.PreimageShare.Delete().Where(preimageshare.PaymentHashEQ(req.PaymentHash)).Exec(ctx)
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

func (o *MockServer) InterruptTransfer(_ context.Context, req *pbmock.InterruptTransferRequest) (*emptypb.Empty, error) {
	switch req.Action {
	case pbmock.InterruptTransferRequest_INTERRUPT:
		o.mockAction.InterruptTransfer = true
	case pbmock.InterruptTransferRequest_RESUME:
		o.mockAction.InterruptTransfer = false
	default:
		return nil, status.Errorf(codes.InvalidArgument, "invalid interrupt transfer action: %v", req.Action)
	}
	return &emptypb.Empty{}, nil
}

func (o *MockServer) UpdateNodesStatus(ctx context.Context, req *pbmock.UpdateNodesStatusRequest) (*emptypb.Empty, error) {
	db := ent.GetDbFromContext(ctx)

	nodeUUIDs := make([]uuid.UUID, 0)
	for _, nodeID := range req.NodeIds {
		nodeUUID, err := uuid.Parse(nodeID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id %s: %v", nodeID, err)
		}
		nodeUUIDs = append(nodeUUIDs, nodeUUID)
	}

	_, err := db.TreeNode.Update().SetStatus(schema.TreeNodeStatus(req.Status)).Where(treenode.IDIn(nodeUUIDs...)).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update nodes: %v", err)
	}
	return &emptypb.Empty{}, nil
}
