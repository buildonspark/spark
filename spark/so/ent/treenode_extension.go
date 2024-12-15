package ent

import (
	"context"

	pbspark "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
)

// MarshalSparkProto converts a TreeNode to a spark protobuf TreeNode.
func (tn *TreeNode) MarshalSparkProto(ctx context.Context) *pbspark.TreeNode {
	return &pbspark.TreeNode{
		Id:           tn.ID.String(),
		TreeId:       tn.QueryTree().FirstIDX(ctx).String(),
		Value:        tn.Value,
		ParentNodeId: tn.getParentNodeID(ctx),
		NodeTx:       tn.RawTx,
		RefundTx:     tn.RawRefundTx,
		Vout:         uint32(tn.Vout),
		VerifyingKey: tn.VerifyingPubkey,
	}
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

func (tn *TreeNode) getParentNodeID(ctx context.Context) *string {
	parentNode, err := tn.QueryParent().Only(ctx)
	if err != nil {
		return nil
	}
	parentNodeIDStr := parentNode.ID.String()
	return &parentNodeIDStr
}
