package ent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pbspark "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
)

// MarshalSparkProto converts a TreeNode to a spark protobuf TreeNode.
func (tn *TreeNode) MarshalSparkProto(ctx context.Context) (*pbspark.TreeNode, error) {
	signingKeyshare, err := tn.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query signing keyshare for leaf %s: %v", tn.ID.String(), err)
	}
	return &pbspark.TreeNode{
		Id:                     tn.ID.String(),
		TreeId:                 tn.QueryTree().FirstIDX(ctx).String(),
		Value:                  tn.Value,
		ParentNodeId:           tn.getParentNodeID(ctx),
		NodeTx:                 tn.RawTx,
		RefundTx:               tn.RawRefundTx,
		Vout:                   uint32(tn.Vout),
		VerifyingPublicKey:     tn.VerifyingPubkey,
		OwnerIdentityPublicKey: tn.OwnerIdentityPubkey,
		SigningKeyshare:        signingKeyshare.MarshalProto(),
	}, nil
}

// MarshalInternalProto converts a TreeNode to a spark internal protobuf TreeNode.
func (tn *TreeNode) MarshalInternalProto(ctx context.Context) *pbinternal.TreeNode {
	return &pbinternal.TreeNode{
		Id:                  tn.ID.String(),
		Value:               tn.Value,
		VerifyingPubkey:     tn.VerifyingPubkey,
		OwnerIdentityPubkey: tn.OwnerIdentityPubkey,
		OwnerSigningPubkey:  tn.OwnerSigningPubkey,
		RawTx:               tn.RawTx,
		RawRefundTx:         tn.RawRefundTx,
		TreeId:              tn.QueryTree().FirstIDX(ctx).String(),
		ParentNodeId:        tn.getParentNodeID(ctx),
		SigningKeyshareId:   tn.QuerySigningKeyshare().FirstIDX(ctx).String(),
		Vout:                uint32(tn.Vout),
	}
}

// GetRefundTxTimeLock get the time lock of the refund tx.
func (tn *TreeNode) GetRefundTxTimeLock() (*uint32, error) {
	if tn.RawRefundTx == nil {
		return nil, nil
	}
	refundTx, err := common.TxFromRawTxBytes(tn.RawRefundTx)
	if err != nil {
		return nil, err
	}
	timelock := refundTx.TxIn[0].Sequence & 0xFFFF
	return &timelock, nil
}

func (tn *TreeNode) getParentNodeID(ctx context.Context) *string {
	parentNode, err := tn.QueryParent().Only(ctx)
	if err != nil {
		return nil
	}
	parentNodeIDStr := parentNode.ID.String()
	return &parentNodeIDStr
}

// MarkNodeAsLocked marks the node as locked.
// It will only update the node status if it is in a state to be locked.
func MarkNodeAsLocked(ctx context.Context, nodeID uuid.UUID, nodeStatus schema.TreeNodeStatus) error {
	db := GetDbFromContext(ctx)
	if nodeStatus != schema.TreeNodeStatusSplitLocked && nodeStatus != schema.TreeNodeStatusTransferLocked {
		return fmt.Errorf("not updating node status to a locked state: %s", nodeStatus)
	}

	node, err := db.TreeNode.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	if node.Status != schema.TreeNodeStatusAvailable {
		return fmt.Errorf("node not in a state to be locked: %s", node.Status)
	}

	return db.TreeNode.UpdateOne(node).SetStatus(nodeStatus).Exec(ctx)
}
