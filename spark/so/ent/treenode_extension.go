package ent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MarshalSparkProto converts a TreeNode to a spark protobuf TreeNode.
func (tn *TreeNode) MarshalSparkProto(ctx context.Context) (*pbspark.TreeNode, error) {
	signingKeyshare, err := tn.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query signing keyshare for leaf %s: %w", tn.ID, err)
	}
	tree, err := tn.QueryTree().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query tree for leaf %s: %w", tn.ID, err)
	}
	networkProto, err := tree.Network.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("unable to marshal network of tree %s: %w", tree.ID, err)
	}
	treeID, err := tn.QueryTree().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query tree for leaf %s: %w", tn.ID, err)
	}
	return &pbspark.TreeNode{
		Id:                     tn.ID.String(),
		TreeId:                 treeID.ID.String(),
		Value:                  tn.Value,
		ParentNodeId:           tn.getParentNodeID(ctx),
		NodeTx:                 tn.RawTx,
		RefundTx:               tn.RawRefundTx,
		DirectTx:               tn.DirectTx,
		DirectRefundTx:         tn.DirectRefundTx,
		DirectFromCpfpRefundTx: tn.DirectFromCpfpRefundTx,
		Vout:                   uint32(tn.Vout),
		VerifyingPublicKey:     tn.VerifyingPubkey.Serialize(),
		OwnerIdentityPublicKey: tn.OwnerIdentityPubkey.Serialize(),
		OwnerSigningPublicKey:  tn.OwnerSigningPubkey.Serialize(),
		SigningKeyshare:        signingKeyshare.MarshalProto(),
		Status:                 string(tn.Status),
		Network:                networkProto,
		CreatedTime:            timestamppb.New(tn.CreateTime),
		UpdatedTime:            timestamppb.New(tn.UpdateTime),
	}, nil
}

// MarshalInternalProto converts a TreeNode to a spark internal protobuf TreeNode.
func (tn *TreeNode) MarshalInternalProto(ctx context.Context) (*pbinternal.TreeNode, error) {
	tree, err := tn.QueryTree().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query tree for leaf %s: %w", tn.ID, err)
	}
	signingKeyshare, err := tn.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query signing keyshare for leaf %s: %w", tn.ID, err)
	}
	return &pbinternal.TreeNode{
		Id:                     tn.ID.String(),
		Value:                  tn.Value,
		VerifyingPubkey:        tn.VerifyingPubkey.Serialize(),
		OwnerIdentityPubkey:    tn.OwnerIdentityPubkey.Serialize(),
		OwnerSigningPubkey:     tn.OwnerSigningPubkey.Serialize(),
		RawTx:                  tn.RawTx,
		DirectTx:               tn.DirectTx,
		RawRefundTx:            tn.RawRefundTx,
		DirectRefundTx:         tn.DirectRefundTx,
		DirectFromCpfpRefundTx: tn.DirectFromCpfpRefundTx,
		TreeId:                 tree.ID.String(),
		ParentNodeId:           tn.getParentNodeID(ctx),
		SigningKeyshareId:      signingKeyshare.ID.String(),
		Vout:                   uint32(tn.Vout),
	}, nil
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
func MarkNodeAsLocked(ctx context.Context, nodeID uuid.UUID, nodeStatus st.TreeNodeStatus) error {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	if nodeStatus != st.TreeNodeStatusSplitLocked && nodeStatus != st.TreeNodeStatusTransferLocked {
		return fmt.Errorf("not updating node status to a locked state: %s", nodeStatus)
	}

	node, err := db.TreeNode.
		Query().
		Where(enttreenode.ID(nodeID)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		return err
	}
	if node.Status != st.TreeNodeStatusAvailable {
		return fmt.Errorf("node not in a state to be locked: %s", node.Status)
	}

	return db.TreeNode.UpdateOne(node).SetStatus(nodeStatus).Exec(ctx)
}
