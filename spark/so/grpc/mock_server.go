package grpc

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/uuids"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/entephemeral"
	"github.com/lightsparkdev/spark/so/knobs"

	"github.com/lightsparkdev/spark/so/task"

	pbmock "github.com/lightsparkdev/spark/proto/mock"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	"github.com/lightsparkdev/spark/so/ent/preimageshare"
	"github.com/lightsparkdev/spark/so/ent/preimagesharepartner"
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
	rootClient          *ent.Client
	ephemeralRootClient *entephemeral.Client
}

// NewMockServer creates a new MockServer.
func NewMockServer(config *so.Config, rootClient *ent.Client, ephemeralRootClient *entephemeral.Client) *MockServer {
	return &MockServer{config: config, rootClient: rootClient, ephemeralRootClient: ephemeralRootClient}
}

// CleanUpPreimageShare cleans up the preimage share for the given payment hash.
func (o *MockServer) CleanUpPreimageShare(ctx context.Context, req *pbmock.CleanUpPreimageShareRequest) (*emptypb.Empty, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// Delete preimage_share_partners before preimage_shares (FK constraint).
	shares, _ := db.PreimageShare.Query().Where(preimageshare.PaymentHashEQ(req.PaymentHash)).All(ctx)
	for _, s := range shares {
		db.PreimageSharePartner.Delete().Where(preimagesharepartner.HasPreimageShareWith(preimageshare.IDEQ(s.ID))).Exec(ctx) //nolint:errcheck // best-effort
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

	nodeUUIDs, err := uuids.ParseSlice(req.GetNodeIds())
	if err != nil {
		return nil, fmt.Errorf("unable to parse node id: %w", err)
	}

	_, err = db.TreeNode.Update().SetStatus(st.TreeNodeStatus(req.Status)).Where(treenode.IDIn(nodeUUIDs...)).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update nodes: %w", err)
	}
	return &emptypb.Empty{}, nil
}

// TriggerTask executes a scheduled task immediately. Primarily used from hermetic tests to avoid relying on gocron timing.
func (o *MockServer) TriggerTask(ctx context.Context, req *pbmock.TriggerTaskRequest) (*emptypb.Empty, error) {
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
	ephemeralDBClient := o.ephemeralRootClient
	if ephemeralDBClient == nil {
		logging.GetLoggerFromContext(ctx).Sugar().Warnf("Mock TriggerTask running without ephemeral DB client for task %s", taskName)
	}
	// Use the knobs service from context (injected by gRPC interceptor) to respect test-configured knob values
	if err := selected.RunOnce(ctx, o.config, dbClient, ephemeralDBClient, knobs.GetKnobsService(ctx)); err != nil {
		return nil, status.Errorf(codes.Internal, "task %s failed: %v", taskName, err)
	}

	return &emptypb.Empty{}, nil
}

func (o *MockServer) QueryPreimageShare(ctx context.Context, req *pbmock.QueryPreimageShareRequest) (*pbmock.QueryPreimageShareResponse, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	share, err := db.PreimageShare.Query().Where(preimageshare.PaymentHashEQ(req.PaymentHash)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "preimage share not found for payment hash")
		}
		return nil, err
	}
	return &pbmock.QueryPreimageShareResponse{
		PreimageShare: share.PreimageShare,
		Threshold:     share.Threshold,
		InvoiceString: share.InvoiceString,
	}, nil
}

func (o *MockServer) ModifyNodeTimelock(ctx context.Context, req *pbmock.ModifyNodeTimelockRequest) (*emptypb.Empty, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	nodeUUID, err := uuid.Parse(req.GetNodeId())
	if err != nil {
		return nil, fmt.Errorf("unable to parse node id: %w", err)
	}

	node, err := db.TreeNode.Query().
		Where(treenode.IDEQ(nodeUUID)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get node: %w", err)
	}

	updatedNodeTx, err := modifyTxSequence(node.RawTx, req.NodeTimelock)
	if err != nil {
		return nil, fmt.Errorf("failed to modify node tx sequence: %w", err)
	}

	updatedRefundTx, err := modifyTxSequence(node.RawRefundTx, req.RefundTimelock)
	if err != nil {
		return nil, fmt.Errorf("failed to modify refund tx sequence: %w", err)
	}

	_, err = db.TreeNode.UpdateOneID(nodeUUID).
		SetRawTx(updatedNodeTx).
		SetRawRefundTx(updatedRefundTx).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update node: %w", err)
	}

	return &emptypb.Empty{}, nil
}

// modifyTxSequence parses a serialized transaction, modifies the first input's
// sequence to encode the desired timelock (preserving existing upper bits),
// and re-serializes it.
func modifyTxSequence(rawTx []byte, timelock uint32) ([]byte, error) {
	tx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse transaction: %w", err)
	}
	if len(tx.TxIn) == 0 {
		return nil, fmt.Errorf("transaction has no inputs")
	}
	if len(tx.TxIn) != 1 {
		return nil, fmt.Errorf("expected single-input transaction, got %d inputs", len(tx.TxIn))
	}

	oldSequence := tx.TxIn[0].Sequence
	tx.TxIn[0].Sequence = (oldSequence & 0xFFFF0000) | (timelock & 0xFFFF)

	serialized, err := common.SerializeTx(tx)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize modified transaction: %w", err)
	}
	return serialized, nil
}
