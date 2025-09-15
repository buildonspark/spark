package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"go.uber.org/zap"
)

// RenewLeafHandler is a handler for renewing a leaf node.
type RenewLeafHandler struct {
	config *so.Config
}

// NewRenewLeafHandler creates a new RenewLeafHandler.
func NewRenewLeafHandler(config *so.Config) *RenewLeafHandler {
	return &RenewLeafHandler{
		config: config,
	}
}

/**
 *	RenewLeaf manages timelocks of nodes. This function will validate user
 * 	sent signing jobs, sign them, aggregate them, and then update internal
 * 	data model with the signed transactions.
 */
func (h *RenewLeafHandler) RenewLeaf(ctx context.Context, req *pb.RenewLeafRequest) (*pb.RenewLeafResponse, error) {
	// Get the leaf from the database
	leafUUID, err := uuid.Parse(req.LeafId)
	if err != nil {
		return nil, errors.InvalidUserInputErrorf("failed to parse leaf id: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get database from context: %w", err)
	}

	leaf, err := db.TreeNode.
		Query().
		Where(enttreenode.ID(leafUUID)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		return nil, errors.InvalidUserInputErrorf("failed to get leaf node: %w", err)
	}

	if leaf.Status != st.TreeNodeStatusAvailable {
		return nil, errors.InvalidUserInputErrorf("leaf node is not available for renewal, current status: %s", leaf.Status)
	}

	// Determine operation type and delegate to appropriate handler
	switch req.SigningJobs.(type) {
	case *pb.RenewLeafRequest_RenewNodeTimelockSigningJob:
		return h.renewNodeTimelock(ctx, req.GetRenewNodeTimelockSigningJob(), leaf)
	case *pb.RenewLeafRequest_RenewRefundTimelockSigningJob:
		return h.renewRefundTimelock(ctx, req.GetRenewRefundTimelockSigningJob(), leaf)
	default:
		return nil, errors.InvalidUserInputErrorf("request must specify either RenewNodeTimelockSigningJob or RenewRefundTimelockSigningJob")
	}
}

// Resets the node and refund transaction timelocks
func (h *RenewLeafHandler) renewNodeTimelock(ctx context.Context, signingJob *pb.RenewNodeTimelockSigningJob, leaf *ent.TreeNode) (*pb.RenewLeafResponse, error) {
	err := h.validateRenewNodeTimelocks(ctx, leaf)
	if err != nil {
		return nil, fmt.Errorf("validating extend timelock failed: %w", err)
	}

	splitNodeTx, nodeTx, refundTx, err := h.constructRenewNodeTransactions(leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to construct renew transactions: %w", err)
	}

	// TODO: add direct txes
	// Create signing jobs with pregenerated nonces
	var signingJobs []*helper.SigningJobWithPregeneratedNonce

	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}

	verifyingPubKey, err := keys.ParsePublicKey(leaf.VerifyingPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse verifying public key: %w", err)
	}

	// Get the parent transaction output for the node transaction
	parentTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse parent transaction: %w", err)
	}

	// Create node transaction signing job (FIRST)
	nodeSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.NodeTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		nodeTx,
		splitNodeTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, nodeSigningJobHelper)

	// Create refund transaction signing job (SECOND)
	refundSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.RefundTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		refundTx,
		nodeTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, refundSigningJobHelper)

	// Create split node transaction signing job (THIRD) - for extend flow
	splitNodeSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.SplitNodeTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		splitNodeTx,
		parentTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, splitNodeSigningJobHelper)

	// Sign the renew refunds
	signingResults, err := h.signRenewRefunds(ctx, signingJobs)
	if err != nil {
		return nil, fmt.Errorf("failed to sign renew refunds: %w", err)
	}

	// Aggregate signatures
	// Aggregate node transaction signature (FIRST)
	nodeSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[0], signingJob.NodeTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate node signature: %w", err)
	}

	// Aggregate refund transaction signature (SECOND)
	refundSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[1], signingJob.RefundTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate refund signature: %w", err)
	}

	// Aggregate split node transaction signature (THIRD) - for extend flow
	splitNodeSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[2], signingJob.SplitNodeTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate split node signature: %w", err)
	}

	// Apply signatures to transactions
	signedSplitNodeTx, splitNodeTxBytes, err := h.applyAndVerifySignature(splitNodeTx, splitNodeSignature, parentTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify split node tx signature: %w", err)
	}

	signedNodeTx, nodeTxBytes, err := h.applyAndVerifySignature(nodeTx, nodeSignature, signedSplitNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify node tx signature: %w", err)
	}

	_, refundTxBytes, err := h.applyAndVerifySignature(refundTx, refundSignature, signedNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify refund tx signature: %w", err)
	}

	// Create new tree node and split the old one
	treeID, err := leaf.QueryTree().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get tree id: %w", err)
	}

	// Get database context
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get database from context: %w", err)
	}

	// Create new split node
	mut := db.
		TreeNode.
		Create().
		SetTreeID(treeID.ID).
		SetStatus(st.TreeNodeStatusSplitLocked).
		SetOwnerIdentityPubkey(leaf.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(leaf.OwnerSigningPubkey).
		SetValue(leaf.Value).
		SetVerifyingPubkey(leaf.VerifyingPubkey).
		SetSigningKeyshareID(signingKeyshare.ID).
		SetRawTx(splitNodeTxBytes).
		SetVout(int16(0))
	if leaf.Edges.Parent != nil {
		mut.SetParentID(leaf.Edges.Parent.ID)
	}
	splitNode, err := mut.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create new node: %w", err)
	}

	// Update the old leaf with extended transactions
	leaf, err = leaf.Update().
		SetRawTx(nodeTxBytes).
		SetRawRefundTx(refundTxBytes).
		SetParentID(splitNode.ID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update leaf: %w", err)
	}

	// Marshal the split node into proto
	splitNodeProto, err := splitNode.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal newly created node %s on spark: %w", splitNode.ID.String(), err)
	}

	// Marshal the extended leaf node into proto
	updatedLeafProto, err := leaf.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal updated leaf node %s on spark: %w", leaf.ID.String(), err)
	}

	// TODO: Send gossip message to other SOs with leaves
	// This PR is getting too big, moving this
	// err = h.sendFinalizeRenewLeafGossipMessage(ctx, leaf, splitNode)

	return &pb.RenewLeafResponse{
		RenewResult: &pb.RenewLeafResponse_RenewNodeTimelockResult{
			RenewNodeTimelockResult: &pb.RenewNodeTimelockResult{
				SplitNode: splitNodeProto,
				Node:      updatedLeafProto,
			},
		},
	}, nil
}

// renewRefundTimelock resets the timelock of a refund transaction
func (h *RenewLeafHandler) renewRefundTimelock(ctx context.Context, signingJob *pb.RenewRefundTimelockSigningJob, leaf *ent.TreeNode) (*pb.RenewLeafResponse, error) {
	err := h.validateRenewRefundTimelock(ctx, leaf)
	if err != nil {
		return nil, fmt.Errorf("validating refresh timelock failed: %w", err)
	}

	// Construct transactions
	nodeTx, refundTx, err := h.constructRenewRefundTransactions(leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to construct renew transactions: %w", err)
	}

	// TODO: add direct txes
	// Create signing jobs with pregenerated nonces
	var signingJobs []*helper.SigningJobWithPregeneratedNonce

	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}

	verifyingPubKey, err := keys.ParsePublicKey(leaf.VerifyingPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse verifying public key: %w", err)
	}

	// Get the parent transaction output for the node transaction
	parentTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse parent transaction: %w", err)
	}

	// Create node transaction signing job (FIRST)
	nodeSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.NodeTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		nodeTx,
		parentTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, nodeSigningJobHelper)

	// Create refund transaction signing job (SECOND)
	refundSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.RefundTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		refundTx,
		nodeTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, refundSigningJobHelper)

	// Sign the renew refunds
	signingResults, err := h.signRenewRefunds(ctx, signingJobs)
	if err != nil {
		return nil, fmt.Errorf("failed to sign renew refunds: %w", err)
	}

	// Aggregate signatures
	// Aggregate node transaction signature (FIRST)
	nodeSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[0], signingJob.NodeTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate node signature: %w", err)
	}

	// Aggregate refund transaction signature (SECOND)
	refundSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[1], signingJob.RefundTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate refund signature: %w", err)
	}

	// Apply signatures to transactions
	signedNodeTx, nodeTxBytes, err := h.applyAndVerifySignature(nodeTx, nodeSignature, parentTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify node tx signature: %w", err)
	}

	_, refundTxBytes, err := h.applyAndVerifySignature(refundTx, refundSignature, signedNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify refund tx signature: %w", err)
	}

	// Update the leaf with refreshed transactions
	leaf, err = leaf.Update().
		SetRawTx(nodeTxBytes).
		SetRawRefundTx(refundTxBytes).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update leaf: %w", err)
	}

	// Marshal the updated leaf node into proto
	updatedLeafProto, err := leaf.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal updated leaf node %s on spark: %w", leaf.ID.String(), err)
	}

	// TODO: Send gossip message to other SOs with leaves
	// This PR is getting too big, moving this
	// err = h.sendFinalizeRenewLeafGossipMessage(ctx, leaf, nil)

	return &pb.RenewLeafResponse{
		RenewResult: &pb.RenewLeafResponse_RenewRefundTimelockResult{
			RenewRefundTimelockResult: &pb.RenewRefundTimelockResult{
				Node: updatedLeafProto,
			},
		},
	}, nil
}

/**
 * 	aggregateRenewLeafSignature performs frost aggregation on a single signing
 *	result and user signing job. After signing in signRenewRefunds,
 * 	aggregation combines the user signature with the SO signature.
 */
func (h *RenewLeafHandler) aggregateRenewLeafSignature(
	ctx context.Context,
	signingResult *helper.SigningResult,
	userSigningJob *pb.UserSignedTxSigningJob,
	leaf *ent.TreeNode,
) ([]byte, error) {
	if userSigningJob == nil {
		return nil, fmt.Errorf("userSigningJob is required but not present")
	}

	frostConn, err := h.config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	signatureResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            signingResult.Message,
		SignatureShares:    signingResult.SignatureShares,
		PublicShares:       signingResult.PublicKeys,
		VerifyingKey:       leaf.VerifyingPubkey,
		Commitments:        userSigningJob.SigningCommitments.SigningCommitments,
		UserCommitments:    userSigningJob.SigningNonceCommitment,
		UserPublicKey:      leaf.OwnerSigningPubkey,
		UserSignatureShare: userSigningJob.UserSignature,
		// Note: No adaptor public key as requested
	})
	if err != nil {
		return nil, fmt.Errorf("unable to aggregate frost signature: %w", err)
	}

	return signatureResult.Signature, nil
}

/**
 * signRenewRefunds performs the SO's job of signing the transactions passed by
 * the user.
 */
func (h *RenewLeafHandler) signRenewRefunds(
	ctx context.Context,
	signingJobs []*helper.SigningJobWithPregeneratedNonce,
) ([]*helper.SigningResult, error) {
	// Validate that no signing jobs have empty round1Packages
	for _, job := range signingJobs {
		if len(job.Round1Packages) == 0 {
			return nil, fmt.Errorf("signing job %s has empty round1Packages (message: %x)", job.SigningJob.JobID, job.SigningJob.Message)
		}
		for key, commitment := range job.Round1Packages {
			if len(commitment.Hiding) == 0 || len(commitment.Binding) == 0 {
				return nil, fmt.Errorf("signing job %s has invalid commitment for key %s: hiding or binding is empty (message: %x)", job.SigningJob.JobID, key, job.SigningJob.Message)
			}
		}
	}

	// Use FROST signing with pregenerated nonces
	signingResults, err := helper.SignFrostWithPregeneratedNonce(ctx, h.config, signingJobs)
	if err != nil {
		return nil, fmt.Errorf("failed to sign frost: %w", err)
	}

	if len(signingResults) != len(signingJobs) {
		return nil, fmt.Errorf("expected %d signing results, got %d", len(signingJobs), len(signingResults))
	}

	return signingResults, nil
}

// constructRenewNodeTransactions creates the split node, extended node, and refund transactions
func (h *RenewLeafHandler) constructRenewNodeTransactions(leaf *ent.TreeNode) (*wire.MsgTx, *wire.MsgTx, *wire.MsgTx, error) {
	leafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse leaf node transaction: %w", err)
	}
	// Construct split node transaction from leaf's node transaction
	splitNodeTx := leafNodeTx.Copy()
	// Clear witness and signature script
	for i := range splitNodeTx.TxIn {
		splitNodeTx.TxIn[i].Witness = wire.TxWitness{}
		splitNodeTx.TxIn[i].SignatureScript = []byte{}
		splitNodeTx.TxIn[i].Sequence = spark.ZeroSequence
	}
	// Create extended node tx to spend the split node tx
	oldRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to deserialize leaf refund tx: %w", err)
	}
	// Create extended node tx to spend the split node tx
	extendedNodeTx := wire.NewMsgTx(3)
	extendedNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: splitNodeTx.TxHash(), Index: 0},
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         spark.InitialSequence(),
	})
	verifyingPubkey, err := keys.ParsePublicKey(leaf.VerifyingPubkey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse verifying pubkey: %w", err)
	}
	outputPkScript, err := common.P2TRScriptFromPubKey(verifyingPubkey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to construct pkscript: %w", err)
	}
	extendedNodeTx.AddTxOut(wire.NewTxOut(leafNodeTx.TxOut[0].Value, outputPkScript))
	// Add ephemeral anchor output for CPFP
	extendedNodeTx.AddTxOut(wire.NewTxOut(0, []byte{0x51, 0x02, 0x4e, 0x73}))

	// Create refund tx to spend the extended node tx
	ownerSigningPubkey, err := keys.ParsePublicKey(leaf.OwnerSigningPubkey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse owner signing pubkey: %w", err)
	}
	refundPkScript, err := common.P2TRScriptFromPubKey(ownerSigningPubkey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create refund script: %w", err)
	}
	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: extendedNodeTx.TxHash(), Index: 0},
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         spark.InitialSequence(),
	})
	refundTx.AddTxOut(&wire.TxOut{
		Value:    oldRefundTx.TxOut[0].Value,
		PkScript: refundPkScript,
	})
	// Add ephemeral anchor output for CPFP
	refundTx.AddTxOut(wire.NewTxOut(0, []byte{0x51, 0x02, 0x4e, 0x73}))
	return splitNodeTx, extendedNodeTx, refundTx, nil
}

// constructRenewRefundTransactions creates the node and refund transactions
func (h *RenewLeafHandler) constructRenewRefundTransactions(leaf *ent.TreeNode) (*wire.MsgTx, *wire.MsgTx, error) {
	leafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse leaf node transaction: %w", err)
	}
	nodeTx := leafNodeTx.Copy()
	// Clear witness and signature script
	for i := range nodeTx.TxIn {
		nodeTx.TxIn[i].Witness = wire.TxWitness{}
		nodeTx.TxIn[i].SignatureScript = []byte{}
		nodeTx.TxIn[i].Sequence, err = spark.NextSequence(nodeTx.TxIn[i].Sequence)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get next sequence of node transaction: %w", err)
		}
	}

	// Create refund tx to spend the extended node tx
	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0},
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         spark.InitialSequence(),
	})

	ownerSigningPubkey, err := keys.ParsePublicKey(leaf.OwnerSigningPubkey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse owner signing pubkey: %w", err)
	}
	refundPkScript, err := common.P2TRScriptFromPubKey(ownerSigningPubkey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create refund script: %w", err)
	}
	oldRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to deserialize leaf refund tx: %w", err)
	}
	refundTx.AddTxOut(&wire.TxOut{
		Value:    oldRefundTx.TxOut[0].Value,
		PkScript: refundPkScript,
	})
	// Add ephemeral anchor output for CPFP
	refundTx.AddTxOut(wire.NewTxOut(0, []byte{0x51, 0x02, 0x4e, 0x73}))

	return nodeTx, refundTx, nil
}

// validateRenewNodeTimelocks validates the timelock requirements for a renew
// node timelock operation. Both the node transaction and the refund transaction
// must have a timelock of 300 or less.
func (h *RenewLeafHandler) validateRenewNodeTimelocks(ctx context.Context, leaf *ent.TreeNode) error {
	// Check the leaf's node transaction sequence
	leafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return fmt.Errorf("failed to parse leaf node transaction: %w", err)
	}
	if len(leafNodeTx.TxIn) == 0 {
		return fmt.Errorf("found no tx inputs for leaf node tx %v", leafNodeTx)
	}
	timelock := leafNodeTx.TxIn[0].Sequence & 0xffff

	// TODO: Throw an error here
	// Leaving in to make unit testing easier
	if timelock > 300 {
		logger := logging.GetLoggerFromContext(ctx)
		logger.Error("leaf node transaction sequence must be less than or equal to 300",
			zap.Uint32("sequence", leafNodeTx.TxIn[0].Sequence),
			zap.String("leaf_id", leaf.ID.String()))
	}

	leafRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("failed to parse leaf refund transaction: %w", err)
	}
	if len(leafRefundTx.TxIn) == 0 {
		return fmt.Errorf("found no tx inputs for leaf refund tx %v", leafRefundTx)
	}

	timelock = leafRefundTx.TxIn[0].Sequence & 0xffff
	if timelock > 300 {
		logger := logging.GetLoggerFromContext(ctx)
		logger.Error("leaf refund transaction sequence must be less than or equal to 300",
			zap.Uint32("sequence", leafRefundTx.TxIn[0].Sequence),
			zap.String("leaf_id", leaf.ID.String()))
	}

	return nil
}

// validateRenewRefundTimelock validates the timelock requirements for a renew
// refund timelock operation. Refund timelock must be <= 300
func (h *RenewLeafHandler) validateRenewRefundTimelock(ctx context.Context, leaf *ent.TreeNode) error {
	// Check the leaf's refund transaction sequence
	leafRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("failed to parse leaf refund transaction: %w", err)
	}
	if len(leafRefundTx.TxIn) == 0 {
		return fmt.Errorf("found no tx inputs for leaf refund tx %v", leafRefundTx)
	}
	timelock := leafRefundTx.TxIn[0].Sequence & 0xffff

	// TODO: Throw an error here
	// Leaving in to make unit testing easier
	if timelock > 300 {
		logger := logging.GetLoggerFromContext(ctx)
		logger.Error("leaf refund transaction sequence must be less than or equal to 300",
			zap.Uint32("sequence", leafRefundTx.TxIn[0].Sequence),
			zap.String("leaf_id", leaf.ID.String()))
	}

	return nil
}

// applyAndVerifySignature applies a signature to a transaction and verifies it
func (h *RenewLeafHandler) applyAndVerifySignature(tx *wire.MsgTx, signature []byte, prevOutput *wire.TxOut, inputIndex int) (*wire.MsgTx, []byte, error) {
	txBytes, err := common.SerializeTx(tx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to serialize transaction: %w", err)
	}

	txBytes, err = common.UpdateTxWithSignature(txBytes, inputIndex, signature)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to update transaction with signature: %w", err)
	}

	signedTx, err := common.TxFromRawTxBytes(txBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to deserialize signed transaction: %w", err)
	}

	err = common.VerifySignatureSingleInput(signedTx, inputIndex, prevOutput)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to verify transaction signature: %w", err)
	}

	return signedTx, txBytes, nil
}
