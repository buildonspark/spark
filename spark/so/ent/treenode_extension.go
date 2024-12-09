package ent

import (
	"encoding/hex"

	pbspark "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
)

// MarshalSparkProto converts a TreeNode to a spark protobuf TreeNode.
func (tn *TreeNode) MarshalSparkProto() *pbspark.TreeNode {
	return &pbspark.TreeNode{
		Id:             tn.ID.String(),
		TreeId:         tn.Edges.Tree.ID.String(),
		Value:          tn.Value,
		ParentNodeId:   tn.getParentNodeID(),
		RawRootTxHex:   hex.EncodeToString(tn.RawTx),
		RawRefundTxHex: hex.EncodeToString(tn.RawRefundTx),
	}
}

// MarshalInternalProto converts a TreeNode to a spark internal protobuf TreeNode.
func (tn *TreeNode) MarshalInternalProto() *pbinternal.TreeNode {
	return &pbinternal.TreeNode{
		Id:                  tn.ID.String(),
		Value:               tn.Value,
		VerifyingPubkey:     tn.VerifyingPubkey,
		OwnerIdentityPubkey: tn.OwnerIdentityPubkey,
		OwnerSigningPubkey:  tn.OwnerSigningPubkey,
		RawTx:               tn.RawTx,
		RawRefundTx:         tn.RawRefundTx,
		TreeId:              tn.Edges.Tree.ID.String(),
		ParentNodeId:        tn.getParentNodeID(),
		SigningKeyshareId:   tn.Edges.SigningKeyshare.ID.String(),
	}
}

func (tn *TreeNode) getParentNodeID() *string {
	parentNode := tn.Edges.Parent
	if parentNode != nil {
		parentNodeIDStr := parentNode.ID.String()
		return &parentNodeIDStr
	}
	return nil
}
