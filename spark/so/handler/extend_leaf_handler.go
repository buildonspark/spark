package handler

import (
	"context"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authz"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/objects"
)

// ExtendLeafHandler is a handler for extending a leaf node.
type ExtendLeafHandler struct {
	config *so.Config
}

// NewExtendLeafHandler creates a new ExtendLeafHandler.
func NewExtendLeafHandler(config *so.Config) *ExtendLeafHandler {
	return &ExtendLeafHandler{
		config: config,
	}
}

func (h *ExtendLeafHandler) ExtendLeaf(ctx context.Context, req *pb.ExtendLeafRequest) (*pb.ExtendLeafResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return nil, fmt.Errorf("failed to enforce session identity public key matches: %w", err)
	}

	leafUUID, err := uuid.Parse(req.LeafId)
	if err != nil {
		return nil, fmt.Errorf("failed to parse leaf id: %w", err)
	}

	db := ent.GetDbFromContext(ctx)
	leaf, err := db.TreeNode.Get(ctx, leafUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get leaf node: %w", err)
	}

	if leaf.Status != schema.TreeNodeStatusAvailable {
		return nil, fmt.Errorf("Leaf %s is not available, status: %s", leafUUID, leaf.Status)
	}

	nodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse leaf node tx: %w", err)
	}

	refundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse leaf refund tx: %w", err)
	}

	newNodeTx, err := common.TxFromRawTxBytes(req.NodeTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse new node tx: %w", err)
	}

	newRefundTx, err := common.TxFromRawTxBytes(req.RefundTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse new refund tx: %w", err)
	}

	// Validate new transactions
	// TODO: make some shared validation across different handlers
	expectedNextSequence, err := spark.NextSequence(refundTx.TxIn[0].Sequence)
	if err != nil {
		return nil, fmt.Errorf("failed to get next sequence: %w", err)
	}
	if newNodeTx.TxIn[0].Sequence > expectedNextSequence {
		return nil, fmt.Errorf("new node tx sequence must be less than or equal to decremented refund tx sequence, expected %d, got %d", expectedNextSequence, newNodeTx.TxIn[0].Sequence)
	}

	fmt.Println("nodeTxid", nodeTx.TxHash())

	newNodeOutPoint := newNodeTx.TxIn[0].PreviousOutPoint
	refundOutPoint := refundTx.TxIn[0].PreviousOutPoint
	if !newNodeOutPoint.Hash.IsEqual(&refundOutPoint.Hash) || newNodeOutPoint.Index != refundOutPoint.Index {
		return nil, fmt.Errorf("new node tx must spend old node tx, expected %s:%d, got %s:%d", refundOutPoint.Hash, refundOutPoint.Index, newNodeOutPoint.Hash, newNodeOutPoint.Index)
	}

	if uint64(newNodeTx.TxOut[0].Value) != leaf.Value {
		return nil, fmt.Errorf("new node tx output value must match leaf value, expected %d, got %d", leaf.Value, newNodeTx.TxOut[0].Value)
	}
	if uint64(refundTx.TxOut[0].Value) != leaf.Value {
		return nil, fmt.Errorf("refund tx output value must match leaf value, expected %d, got %d", leaf.Value, refundTx.TxOut[0].Value)
	}

	newNodeSigningJob, err := createSigningJob(ctx, newNodeTx, nodeTx.TxOut[0], req.NodeTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to create new node signing job: %w", err)
	}

	refundSigningJob, err := createSigningJob(ctx, newRefundTx, newNodeTx.TxOut[0], req.RefundTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to create refund signing job: %w", err)
	}

	treeID, err := leaf.QueryTree().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get tree id: %w", err)
	}
	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
	}
	// Update the nodes in the DB
	// TODO: how to get the tree and keyshare id without a query?
	// TODO: we probably need to sync this state between the SOs
	newNode, err := db.
		TreeNode.
		Create().
		SetTreeID(treeID.ID).
		SetStatus(schema.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(req.OwnerIdentityPublicKey).
		SetOwnerSigningPubkey(leaf.OwnerSigningPubkey).
		SetValue(leaf.Value).
		SetVerifyingPubkey(leaf.VerifyingPubkey).
		SetSigningKeyshareID(signingKeyshare.ID).
		SetRawTx(req.NodeTxSigningJob.RawTx).
		SetRawRefundTx(req.RefundTxSigningJob.RawTx).
		SetVout(int16(0)).
		SetParentID(leaf.ID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create new node: %w", err)
	}

	_, err = db.
		TreeNode.
		UpdateOneID(leaf.ID).
		SetRawRefundTx(nil).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to nullify refund tx: %w", err)
	}

	// Sign frost
	signingResults, err := helper.SignFrost(ctx, h.config, []*helper.SigningJob{newNodeSigningJob, refundSigningJob})
	if err != nil {
		return nil, fmt.Errorf("failed to sign frost: %w", err)
	}
	if len(signingResults) != 2 {
		return nil, fmt.Errorf("expected 2 signing results, got %d", len(signingResults))
	}
	nodeFrostResult := signingResults[0]
	refundFrostResult := signingResults[1]

	// Prepare response
	verifyingPubkey := leaf.VerifyingPubkey
	nodeSigningResultProto, err := nodeFrostResult.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal node signing result: %w", err)
	}
	nodeSigningResult := &pb.ExtendLeafSigningResult{
		SigningResult: nodeSigningResultProto,
		VerifyingKey:  verifyingPubkey,
	}
	refundSigningResultProto, err := refundFrostResult.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal refund signing result: %w", err)
	}
	refundSigningResult := &pb.ExtendLeafSigningResult{
		SigningResult: refundSigningResultProto,
		VerifyingKey:  verifyingPubkey,
	}

	return &pb.ExtendLeafResponse{
		LeafId:                newNode.ID.String(),
		NodeTxSigningResult:   nodeSigningResult,
		RefundTxSigningResult: refundSigningResult,
	}, nil
}

func createSigningJob(
	ctx context.Context,
	tx *wire.MsgTx,
	parentTxOut *wire.TxOut,
	signingJob *pb.SigningJob,
	leaf *ent.TreeNode,
) (*helper.SigningJob, error) {
	sigHash, err := common.SigHashFromTx(tx, 0, parentTxOut)
	if err != nil {
		return nil, fmt.Errorf("failed to get sig hash for new node tx: %w", err)
	}
	newNodeUserNonceCommitment, err := objects.NewSigningCommitment(signingJob.SigningNonceCommitment.Binding, signingJob.SigningNonceCommitment.Hiding)
	if err != nil {
		return nil, fmt.Errorf("failed to create new node user nonce commitment: %w", err)
	}
	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
	}
	return &helper.SigningJob{
		JobID:             uuid.New().String(),
		SigningKeyshareID: signingKeyshare.ID,
		Message:           sigHash,
		VerifyingKey:      leaf.VerifyingPubkey,
		UserCommitment:    newNodeUserNonceCommitment,
	}, nil
}
