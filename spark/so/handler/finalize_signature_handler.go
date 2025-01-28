package handler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	enttransfer "github.com/lightsparkdev/spark-go/so/ent/transfer"
	"github.com/lightsparkdev/spark-go/so/ent/transferleaf"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
	"github.com/lightsparkdev/spark-go/so/helper"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FinalizeSignatureHandler is the handler for the FinalizeNodeSignatures RPC.
type FinalizeSignatureHandler struct {
	config *so.Config
}

// NewFinalizeSignatureHandler creates a new FinalizeSignatureHandler.
func NewFinalizeSignatureHandler(config *so.Config) *FinalizeSignatureHandler {
	return &FinalizeSignatureHandler{config: config}
}

// FinalizeNodeSignatures verifies the node signatures and updates the node.
func (o *FinalizeSignatureHandler) FinalizeNodeSignatures(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) (*pb.FinalizeNodeSignaturesResponse, error) {
	var transfer *ent.Transfer
	switch req.Intent {
	case pbcommon.SignatureIntent_TRANSFER:
		var err error
		transfer, err = o.verifyAndUpdateTransfer(ctx, req)
		if err != nil {
			return nil, err
		}
	}

	nodes := make([]*pb.TreeNode, 0)
	internalNodes := make([]*pbinternal.TreeNode, 0)
	for _, nodeSignatures := range req.NodeSignatures {
		node, internalNode, err := o.updateNode(ctx, nodeSignatures, req.Intent)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
		internalNodes = append(internalNodes, internalNode)
	}

	// Sync with all other SOs
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, o.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)

		switch req.Intent {
		case pbcommon.SignatureIntent_CREATION:
			_, err = client.FinalizeTreeCreation(ctx, &pbinternal.FinalizeTreeCreationRequest{Nodes: internalNodes})
			return nil, err
		case pbcommon.SignatureIntent_SPLIT:
			_, err = client.FinalizeNodeSplit(ctx, &pbinternal.FinalizeNodeSplitRequest{ParentNodeId: *internalNodes[0].ParentNodeId, ChildNodes: internalNodes})
			return nil, err
		case pbcommon.SignatureIntent_AGGREGATE:
			_, err = client.FinalizeNodesAggregation(ctx, &pbinternal.FinalizeNodesAggregationRequest{Nodes: internalNodes})
			return nil, err
		case pbcommon.SignatureIntent_TRANSFER:
			_, err = client.FinalizeTransfer(ctx, &pbinternal.FinalizeTransferRequest{TransferId: transfer.ID.String(), Nodes: internalNodes, Timestamp: timestamppb.New(*transfer.CompletionTime)})
			return nil, err
		}
		return nil, err
	})
	if err != nil {
		log.Printf("failed to sync with other SOs: %v", err)
		return nil, err
	}

	return &pb.FinalizeNodeSignaturesResponse{Nodes: nodes}, nil
}

func (o *FinalizeSignatureHandler) verifyAndUpdateTransfer(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) (*ent.Transfer, error) {
	db := ent.GetDbFromContext(ctx)
	var transfer *ent.Transfer
	for _, nodeSignatures := range req.NodeSignatures {
		leafID, err := uuid.Parse(nodeSignatures.NodeId)
		if err != nil {
			return nil, fmt.Errorf("invalid node id: %v", err)
		}
		leafTransfer, err := db.Transfer.Query().
			Where(
				enttransfer.StatusEQ(schema.TransferStatusReceiverRefundSigned),
				enttransfer.HasTransferLeavesWith(
					transferleaf.HasLeafWith(
						treenode.IDEQ(leafID),
					),
				),
			).
			Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to find pending transfer for leaf %s: %v", leafID.String(), err)
		}
		if transfer == nil {
			transfer = leafTransfer
		} else if transfer.ID != leafTransfer.ID {
			return nil, fmt.Errorf("expect all leaves to belong to the same transfer")
		}
	}
	numTransferLeaves, err := transfer.QueryTransferLeaves().Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get the number of transfer leaves for transfer %s: %v", transfer.ID.String(), err)
	}
	if len(req.NodeSignatures) != numTransferLeaves {
		return nil, fmt.Errorf("missing signatures for transfer %s", transfer.ID.String())
	}

	transfer, err = transfer.Update().SetStatus(schema.TransferStatusCompleted).SetCompletionTime(time.Now()).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update transfer %s: %v", transfer.ID.String(), err)
	}
	return transfer, nil
}

func (o *FinalizeSignatureHandler) updateNode(ctx context.Context, nodeSignatures *pb.NodeSignatures, intent pbcommon.SignatureIntent) (*pb.TreeNode, *pbinternal.TreeNode, error) {
	log.Printf("finalizing node signatures for node %s", nodeSignatures.NodeId)
	db := ent.GetDbFromContext(ctx)

	nodeID, err := uuid.Parse(nodeSignatures.NodeId)
	if err != nil {
		return nil, nil, err
	}

	// Read the tree node
	node, err := db.TreeNode.Get(ctx, nodeID)
	if err != nil {
		return nil, nil, err
	}
	if node == nil {
		return nil, nil, fmt.Errorf("node not found")
	}

	var nodeTxBytes []byte
	if intent == pbcommon.SignatureIntent_CREATION || intent == pbcommon.SignatureIntent_SPLIT {
		nodeTxBytes, err = common.UpdateTxWithSignature(node.RawTx, 0, nodeSignatures.NodeTxSignature)
		if err != nil {
			return nil, nil, err
		}
		// Node may not have parent if it is the root node
		nodeParent, err := node.QueryParent().Only(ctx)
		if err == nil && nodeParent != nil {
			treeNodeTx, err := common.TxFromRawTxBytes(nodeTxBytes)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to deserialize node tx: %v", err)
			}
			treeNodeParentTx, err := common.TxFromRawTxBytes(nodeParent.RawTx)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to deserialize parent tx: %v", err)
			}
			err = common.VerifySignature(treeNodeTx, 0, treeNodeParentTx.TxOut[node.Vout])
			if err != nil {
				return nil, nil, fmt.Errorf("unable to verify node tx signature: %v", err)
			}
		}
	} else {
		nodeTxBytes = node.RawTx
	}
	var refundTxBytes []byte
	if nodeSignatures.RefundTxSignature != nil {
		refundTxBytes, err = common.UpdateTxWithSignature(node.RawRefundTx, 0, nodeSignatures.RefundTxSignature)
		if err != nil {
			return nil, nil, err
		}

		refundTx, err := common.TxFromRawTxBytes(refundTxBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to deserialize refund tx: %v", err)
		}
		treeNodeTx, err := common.TxFromRawTxBytes(nodeTxBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to deserialize leaf tx: %v", err)
		}
		err = common.VerifySignature(refundTx, 0, treeNodeTx.TxOut[node.Vout])
		if err != nil {
			return nil, nil, fmt.Errorf("unable to verify refund tx signature: %v", err)
		}
	}

	tree, err := node.QueryTree().Only(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Update the tree node
	nodeMutator := node.Update().
		SetRawTx(nodeTxBytes).
		SetRawRefundTx(refundTxBytes)
	if tree.Status == schema.TreeStatusAvailable {
		nodeMutator.SetStatus(schema.TreeNodeStatusAvailable)
	}
	node, err = nodeMutator.Save(ctx)
	if err != nil {
		return nil, nil, err
	}

	if intent == pbcommon.SignatureIntent_SPLIT {
		parent, err := node.QueryParent().Only(ctx)
		if err != nil {
			log.Printf("failed to get parent node: %v", err)
			return nil, nil, err
		}
		_, err = parent.Update().SetStatus(schema.TreeNodeStatusSplitted).Save(ctx)
		if err != nil {
			log.Printf("failed to update parent node: %v", err)
			return nil, nil, err
		}
	}

	return node.MarshalSparkProto(ctx), node.MarshalInternalProto(ctx), nil
}
