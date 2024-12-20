package handler

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"time"

	"github.com/btcsuite/btcd/wire"
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
	switch req.Intent {
	case pbcommon.SignatureIntent_TRANSFER:
		err := o.verifyAndUpdateTransfer(ctx, req)
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
		_, err = client.InternalFinalizeNodeSignatures(ctx, &pbinternal.InternalFinalizeNodeSignaturesRequest{Intent: req.Intent, Nodes: internalNodes})
		return nil, err
	})
	if err != nil {
		log.Printf("failed to sync with other SOs: %v", err)
		return nil, err
	}

	return &pb.FinalizeNodeSignaturesResponse{Nodes: nodes}, nil
}

func (o *FinalizeSignatureHandler) verifyAndUpdateTransfer(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) error {
	db := ent.GetDbFromContext(ctx)
	var transfer *ent.Transfer
	for _, nodeSignatures := range req.NodeSignatures {
		leafID, err := uuid.Parse(nodeSignatures.NodeId)
		if err != nil {
			return fmt.Errorf("invalid node id: %v", err)
		}
		leafTransfer, err := db.Transfer.Query().
			Where(
				enttransfer.StatusEQ(schema.TransferStatusRefundSigned),
				enttransfer.HasTransferLeavesWith(
					transferleaf.HasLeafWith(
						treenode.IDEQ(leafID),
					),
				),
			).
			Only(ctx)
		if err != nil {
			return fmt.Errorf("failed to find pending transfer for leaf %s: %v", leafID.String(), err)
		}
		if transfer == nil {
			transfer = leafTransfer
		} else if transfer.ID != leafTransfer.ID {
			return fmt.Errorf("expect all leaves to belong to the same transfer")
		}
	}
	numTransferLeaves, err := transfer.QueryTransferLeaves().Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to get the number of transfer leaves for transfer %s: %v", transfer.ID.String(), err)
	}
	if len(req.NodeSignatures) != numTransferLeaves {
		return fmt.Errorf("missing signatures for transfer %s", transfer.ID.String())
	}

	_, err = transfer.Update().SetStatus(schema.TransferStatusCompleted).SetCompletionTime(time.Now()).Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update transfer %s: %v", transfer.ID.String(), err)
	}
	return nil
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

	var rootTxBytes []byte
	if intent == pbcommon.SignatureIntent_CREATION || intent == pbcommon.SignatureIntent_SPLIT {
		rootTxBytes, err = o.verifySignatureAndUpdateTx(node.RawTx, nodeSignatures.NodeTxSignature)
		if err != nil {
			return nil, nil, err
		}
	} else {
		rootTxBytes = node.RawTx
	}
	refundTxBytes, err := o.verifySignatureAndUpdateTx(node.RawRefundTx, nodeSignatures.RefundTxSignature)
	if err != nil {
		return nil, nil, err
	}

	// Update the tree root
	node, err = node.Update().
		SetRawTx(rootTxBytes).
		SetRawRefundTx(refundTxBytes).
		SetStatus(schema.TreeNodeStatusAvailable).
		Save(ctx)
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

func (o *FinalizeSignatureHandler) verifySignatureAndUpdateTx(rawTx []byte, signature []byte) ([]byte, error) {
	tx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return nil, err
	}
	// TODO: Verify the signature

	tx.TxIn[0].Witness = wire.TxWitness{signature}
	var buf bytes.Buffer
	tx.Serialize(&buf)
	return buf.Bytes(), nil
}
