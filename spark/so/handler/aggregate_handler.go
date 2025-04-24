package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authz"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/logging"
	"google.golang.org/protobuf/types/known/emptypb"
)

// AggregateHandler is the handler for the aggregate nodes request.
type AggregateHandler struct {
	config *so.Config
}

// NewAggregateHandler creates a new AggregateHandler.
func NewAggregateHandler(config *so.Config) *AggregateHandler {
	return &AggregateHandler{config: config}
}

func (h *AggregateHandler) validateAggregateNodesRequest(ctx context.Context, req *pb.AggregateNodesRequest) (parentNode *ent.TreeNode, nodes []*ent.TreeNode, err error) {
	db := ent.GetDbFromContext(ctx)

	nodeIDs := make([]uuid.UUID, len(req.NodeIds))
	nodesIDMap := make(map[uuid.UUID]bool)
	for i, nodeID := range req.NodeIds {
		nodeIDs[i], err = uuid.Parse(nodeID)
		if err != nil {
			return nil, nil, err
		}
		nodesIDMap[nodeIDs[i]] = true
	}

	nodes, err = db.TreeNode.Query().Where(treenode.IDIn(nodeIDs...)).All(ctx)
	if err != nil {
		return nil, nil, err
	}

	if len(nodes) == 0 || len(nodes) != len(nodeIDs) {
		return nil, nil, errors.New("invalid node ids")
	}

	parentNode, err = nodes[0].QueryParent().First(ctx)
	if err != nil {
		return nil, nil, err
	}

	children, err := parentNode.QueryChildren().All(ctx)
	if err != nil {
		return nil, nil, err
	}

	for _, child := range children {
		if !nodesIDMap[child.ID] {
			return nil, nil, errors.New("invalid node ids")
		}
		if !bytes.Equal(child.OwnerIdentityPubkey, req.OwnerIdentityPublicKey) {
			return nil, nil, errors.New("invalid owner identity public key")
		}
	}

	return parentNode, nodes, nil
}

func (h *AggregateHandler) prepareSigningJob(_ context.Context, parentNode *ent.TreeNode, keyshare *ent.SigningKeyshare, req *pb.AggregateNodesRequest) (*helper.SigningJob, error) {
	parentNodeTx, err := common.TxFromRawTxBytes(parentNode.RawTx)
	if err != nil {
		return nil, err
	}
	signingJob, _, err := helper.NewSigningJob(keyshare, req.SigningJob, parentNodeTx.TxOut[parentNode.Vout], nil)
	if err != nil {
		return nil, err
	}
	return signingJob, nil
}

func (h *AggregateHandler) markNodesAggregatedStatus(ctx context.Context, parentNode *ent.TreeNode, nodes []*ent.TreeNode) error {
	for _, node := range nodes {
		_, err := node.Update().SetStatus(schema.TreeNodeStatusAggregated).Save(ctx)
		if err != nil {
			return err
		}
	}

	_, err := parentNode.Update().SetStatus(schema.TreeNodeStatusAggregateLock).Save(ctx)
	if err != nil {
		return err
	}
	return nil
}

func (h *AggregateHandler) validateAndAggregateKeyshares(ctx context.Context, req *pb.AggregateNodesRequest) (parentNode *ent.TreeNode, nodes []*ent.TreeNode, parentKeyshare *ent.SigningKeyshare, err error) {
	parentNode, nodes, err = h.validateAggregateNodesRequest(ctx, req)
	if err != nil {
		return nil, nil, nil, err
	}

	err = h.markNodesAggregatedStatus(ctx, parentNode, nodes)
	if err != nil {
		return nil, nil, nil, err
	}

	parentKeyshare, err = parentNode.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	keyshares := make([]*ent.SigningKeyshare, len(nodes))
	for i, node := range nodes {
		keyshare, err := node.QuerySigningKeyshare().First(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		keyshares[i] = keyshare
	}

	updatedKeyshare, err := ent.AggregateKeyshares(ctx, h.config, keyshares, parentKeyshare.ID)
	if err != nil {
		return nil, nil, nil, err
	}

	return parentNode, nodes, updatedKeyshare, nil
}

// InternalAggregateNodes is the internal handler for the aggregate nodes request.
func (h *AggregateHandler) InternalAggregateNodes(ctx context.Context, req *pb.AggregateNodesRequest) (*emptypb.Empty, error) {
	_, _, _, err := h.validateAndAggregateKeyshares(ctx, req)
	if err != nil {
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

// AggregateNodes is the handler for the aggregate nodes request.
func (h *AggregateHandler) AggregateNodes(ctx context.Context, req *pb.AggregateNodesRequest) (*pb.AggregateNodesResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	parentNode, _, updatedKeyshare, err := h.validateAndAggregateKeyshares(ctx, req)
	if err != nil {
		return nil, err
	}

	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			logger.Error("Failed to connect to operator", "error", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.AggregateNodes(ctx, req)
	})
	if err != nil {
		return nil, err
	}

	signingJob, err := h.prepareSigningJob(ctx, parentNode, updatedKeyshare, req)
	if err != nil {
		return nil, err
	}

	signingResult, err := helper.SignFrost(ctx, h.config, []*helper.SigningJob{
		signingJob,
	})
	if err != nil {
		return nil, err
	}

	verifyingKey, err := common.AddPublicKeys(req.SigningJob.SigningPublicKey, updatedKeyshare.PublicKey)
	if err != nil {
		return nil, err
	}

	signingResultProto, err := signingResult[0].MarshalProto()
	if err != nil {
		return nil, err
	}

	return &pb.AggregateNodesResponse{
		AggregateSignature: signingResultProto,
		VerifyingKey:       verifyingKey,
		ParentNodeTx:       parentNode.RawTx,
		ParentNodeVout:     uint32(parentNode.Vout),
	}, nil
}

// InternalFinalizeNodesAggregation syncs final nodes aggregation.
func (h *AggregateHandler) InternalFinalizeNodesAggregation(ctx context.Context, req *pbinternal.FinalizeNodesAggregationRequest) error {
	logger := logging.GetLoggerFromContext(ctx)
	db := ent.GetDbFromContext(ctx)
	for _, node := range req.Nodes {
		nodeID, err := uuid.Parse(node.Id)
		if err != nil {
			return err
		}
		dbNode, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return err
		}
		if dbNode == nil {
			return fmt.Errorf("node not found")
		}
		_, err = dbNode.Update().
			SetRawTx(node.RawTx).
			SetRawRefundTx(node.RawRefundTx).
			SetStatus(schema.TreeNodeStatusAvailable).
			Save(ctx)
		if err != nil {
			logger.Error("Failed to update node", "error", err)
			return err
		}
	}
	return nil
}
