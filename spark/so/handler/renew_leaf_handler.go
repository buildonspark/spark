package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
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

	ownerIDPubKey, err := keys.ParsePublicKey(leaf.OwnerIdentityPubkey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse owner identity public key: %w", err)
	}
	err = authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, ownerIDPubKey)
	if err != nil {
		return nil, err
	}

	// Determine operation type and delegate to appropriate handler
	switch req.SigningJobs.(type) {
	case *pb.RenewLeafRequest_RenewNodeTimelockSigningJob:
		return h.renewNodeTimelock(ctx, req.GetRenewNodeTimelockSigningJob(), leaf)
	case *pb.RenewLeafRequest_RenewRefundTimelockSigningJob:
		return h.renewRefundTimelock(ctx, req.GetRenewRefundTimelockSigningJob(), leaf)
	case *pb.RenewLeafRequest_RenewNodeZeroTimelockSigningJob:
		return h.renewNodeZeroTimelock(ctx, req.GetRenewNodeZeroTimelockSigningJob(), leaf)
	default:
		return nil, errors.InvalidUserInputErrorf("request must specify either RenewNodeTimelockSigningJob or RenewRefundTimelockSigningJob")
	}
}

// Resets the node and refund transaction timelocks
/*
BEFORE                                   AFTER
----------------------------             ------------------------------------------------------------------------------------------------------------
(parent_node_tx timelock: 0)             (parent_node_tx timelock: 0)         // This transaction is to invalidate all the previously signed node_tx.
|                                        |                             \      // This is a tempory solution to make sure that watchtower is able to
|                                        |                              \     // prevent attacks. But after this is broadcasted, user will need to
v                                        v                               \    // work with SOs to sign an exit transaction to claim the funds back
(node_tx: timelock: 100)                 (node_tx: timelock: 0)           \-> (direct_node_tx: timelock 50)
|                                        |                     \
|                                        |                      \
v                                        v                       \
(refund_tx: timelock:100)                (new_node_tx  )          \-> (direc_node_tx: timelock 2050)
                                         (timelock:2000)                                              \
                                         |               \                                             \
                                         |                \                                             \
                                         v                 \                                             \
                                         (refund_tx     )   \-> (direct_refund_tx_from_cpfp)              \->(direct_refund_tx)
                                         (timelock: 2000)       (timelock: 2050            )                 (timelock: 2050  )
*/
func (h *RenewLeafHandler) renewNodeTimelock(ctx context.Context, signingJob *pb.RenewNodeTimelockSigningJob, leaf *ent.TreeNode) (*pb.RenewLeafResponse, error) {
	err := h.validateRenewNodeTimelocks(leaf)
	if err != nil {
		return nil, fmt.Errorf("validating extend timelock failed: %w", err)
	}

	// Validate that all direct signing jobs are present
	if signingJob.SplitNodeDirectTxSigningJob == nil {
		return nil, errors.InvalidUserInputErrorf("split node direct tx signing job is required")
	}
	if signingJob.DirectNodeTxSigningJob == nil {
		return nil, errors.InvalidUserInputErrorf("direct node tx signing job is required")
	}
	if signingJob.DirectRefundTxSigningJob == nil {
		return nil, errors.InvalidUserInputErrorf("direct refund tx signing job is required")
	}
	if signingJob.DirectFromCpfpRefundTxSigningJob == nil {
		return nil, errors.InvalidUserInputErrorf("direct from cpfp refund tx signing job is required")
	}

	// Query the parent of the leaf to ensure it exists
	parentLeaf, err := leaf.QueryParent().Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, errors.InvalidUserInputErrorf("parent node does not exist for leaf %s", leaf.ID.String())
		}
		return nil, fmt.Errorf("failed to query parent node: %w", err)
	}
	if parentLeaf == nil {
		return nil, errors.InvalidUserInputErrorf("parent node does not exist for leaf %s", leaf.ID.String())
	}

	splitNodeTx, nodeTx, refundTx, directSplitNodeTx, directNodeTx, directRefundTx, directFromCpfpRefundTx, err := h.constructRenewNodeTransactions(leaf, parentLeaf)
	if err != nil {
		return nil, fmt.Errorf("failed to construct renew transactions: %w", err)
	}

	userRawTxs := [][]byte{signingJob.SplitNodeTxSigningJob.RawTx, signingJob.NodeTxSigningJob.RawTx, signingJob.RefundTxSigningJob.RawTx, signingJob.SplitNodeDirectTxSigningJob.RawTx, signingJob.DirectNodeTxSigningJob.RawTx, signingJob.DirectRefundTxSigningJob.RawTx, signingJob.DirectFromCpfpRefundTxSigningJob.RawTx}
	expectedTxs := []*wire.MsgTx{splitNodeTx, nodeTx, refundTx, directSplitNodeTx, directNodeTx, directRefundTx, directFromCpfpRefundTx}
	err = h.validateUserTransactions(userRawTxs, expectedTxs)
	if err != nil {
		return nil, fmt.Errorf("user transaction validation failed: %w", err)
	}

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
	parentTx, err := common.TxFromRawTxBytes(parentLeaf.RawTx)
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

	// Create direct split node transaction signing job (FOURTH)
	directSplitNodeSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.SplitNodeDirectTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		directSplitNodeTx,
		parentTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, directSplitNodeSigningJobHelper)

	// Create direct node transaction signing job (FIFTH)
	directNodeSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.DirectNodeTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		directNodeTx,
		splitNodeTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, directNodeSigningJobHelper)

	// Create direct refund transaction signing job (SIXTH)
	directRefundSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.DirectRefundTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		directRefundTx,
		directNodeTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, directRefundSigningJobHelper)

	// Create direct from CPFP refund transaction signing job (SEVENTH)
	directFromCpfpRefundSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.DirectFromCpfpRefundTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		directFromCpfpRefundTx,
		nodeTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, directFromCpfpRefundSigningJobHelper)

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

	// Aggregate direct split node transaction signature (FOURTH)
	directSplitNodeSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[3], signingJob.SplitNodeDirectTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct split node signature: %w", err)
	}

	// Aggregate direct node transaction signature (FIFTH)
	directNodeSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[4], signingJob.DirectNodeTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct node signature: %w", err)
	}

	// Aggregate direct refund transaction signature (SIXTH)
	directRefundSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[5], signingJob.DirectRefundTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct refund signature: %w", err)
	}

	// Aggregate direct from CPFP refund transaction signature (SEVENTH)
	directFromCpfpRefundSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[6], signingJob.DirectFromCpfpRefundTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct from cpfp refund signature: %w", err)
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

	// Apply and verify direct split node transaction signature
	_, directSplitNodeTxBytes, err := h.applyAndVerifySignature(directSplitNodeTx, directSplitNodeSignature, parentTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify direct split node tx signature: %w", err)
	}

	// Apply and verify direct node transaction signature
	signedDirectNodeTx, directNodeTxBytes, err := h.applyAndVerifySignature(directNodeTx, directNodeSignature, signedSplitNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify direct node tx signature: %w", err)
	}

	// Apply and verify direct refund transaction signature
	_, directRefundTxBytes, err := h.applyAndVerifySignature(directRefundTx, directRefundSignature, signedDirectNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify direct refund tx signature: %w", err)
	}

	// Apply and verify direct from CPFP refund transaction signature
	_, directFromCpfpRefundTxBytes, err := h.applyAndVerifySignature(directFromCpfpRefundTx, directFromCpfpRefundSignature, signedNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify direct from cpfp refund tx signature: %w", err)
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
		SetDirectTx(directSplitNodeTxBytes).
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
		SetDirectTx(directNodeTxBytes).
		SetDirectRefundTx(directRefundTxBytes).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTxBytes).
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

	err = h.sendFinalizeNodeTimelockGossipMessage(ctx, splitNode, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to send gossip message: %w", err)
	}

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
/*
BEFORE                                      AFTER
----------------------------                ------------------------------------------------------------------------------------------------
(parent_node_tx timelock: 0)                (parent_node_tx timelock: 0)
|                                           |                             \
|                                           |                              \
v                                           v                               \
(node_tx: timelock: 2000)                   (node_tx: timelock: 1900)        \-> (direct_refund_tx: timelock 1950)
|                                           |                       \                                             \
|                                           |                        \                                             \
v                                           v                         \                                             \
(refund_tx: timelock:100)                   (refund_tx     )           \-> (direct_refund_tx_from_cpfp)              \->(direct_refund_tx)
                                            (timelock: 2000)               (timelock: 2050            )                 (timelock: 2050  )
*/
func (h *RenewLeafHandler) renewRefundTimelock(ctx context.Context, signingJob *pb.RenewRefundTimelockSigningJob, leaf *ent.TreeNode) (*pb.RenewLeafResponse, error) {
	err := h.validateRenewRefundTimelock(leaf)
	if err != nil {
		return nil, fmt.Errorf("validating refresh timelock failed: %w", err)
	}

	// Validate that all direct signing jobs are present
	if signingJob.DirectNodeTxSigningJob == nil {
		return nil, errors.InvalidUserInputErrorf("direct node tx signing job is required")
	}
	if signingJob.DirectRefundTxSigningJob == nil {
		return nil, errors.InvalidUserInputErrorf("direct refund tx signing job is required")
	}
	if signingJob.DirectFromCpfpRefundTxSigningJob == nil {
		return nil, errors.InvalidUserInputErrorf("direct from cpfp refund tx signing job is required")
	}

	// Query the parentLeaf of the leaf to ensure it exists
	parentLeaf, err := leaf.QueryParent().Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, errors.InvalidUserInputErrorf("parent node does not exist for leaf %s", leaf.ID.String())
		}
		return nil, fmt.Errorf("failed to query parent node: %w", err)
	}
	if parentLeaf == nil {
		return nil, errors.InvalidUserInputErrorf("parent node does not exist for leaf %s", leaf.ID.String())
	}

	// Construct transactions
	nodeTx, refundTx, directNodeTx, directRefundTx, directFromCpfpRefundTx, err := h.constructRenewRefundTransactions(leaf, parentLeaf)
	if err != nil {
		return nil, fmt.Errorf("failed to construct renew transactions: %w", err)
	}

	userRawTxs := [][]byte{signingJob.NodeTxSigningJob.RawTx, signingJob.RefundTxSigningJob.RawTx, signingJob.DirectNodeTxSigningJob.RawTx, signingJob.DirectRefundTxSigningJob.RawTx, signingJob.DirectFromCpfpRefundTxSigningJob.RawTx}
	expectedTxs := []*wire.MsgTx{nodeTx, refundTx, directNodeTx, directRefundTx, directFromCpfpRefundTx}

	err = h.validateUserTransactions(userRawTxs, expectedTxs)
	if err != nil {
		return nil, fmt.Errorf("user transaction validation failed: %w", err)
	}

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
	parentTx, err := common.TxFromRawTxBytes(parentLeaf.RawTx)
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

	// Create direct node transaction signing job (THIRD)
	directNodeSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.DirectNodeTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		directNodeTx,
		parentTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, directNodeSigningJobHelper)

	// Create direct refund transaction signing job (FOURTH)
	directRefundSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.DirectRefundTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		directRefundTx,
		directNodeTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, directRefundSigningJobHelper)

	// Create direct from CPFP refund transaction signing job (FIFTH)
	directFromCpfpRefundSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.DirectFromCpfpRefundTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		directFromCpfpRefundTx,
		nodeTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, directFromCpfpRefundSigningJobHelper)

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

	// Aggregate direct node transaction signature (THIRD)
	directNodeSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[2], signingJob.DirectNodeTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct node signature: %w", err)
	}

	// Aggregate direct refund transaction signature (FOURTH)
	directRefundSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[3], signingJob.DirectRefundTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct refund signature: %w", err)
	}

	// Aggregate direct from CPFP refund transaction signature (FIFTH)
	directFromCpfpRefundSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[4], signingJob.DirectFromCpfpRefundTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct from cpfp refund signature: %w", err)
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

	// Apply and verify direct node transaction signature
	signedDirectNodeTx, directNodeTxBytes, err := h.applyAndVerifySignature(directNodeTx, directNodeSignature, parentTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify direct node tx signature: %w", err)
	}

	// Apply and verify direct refund transaction signature
	_, directRefundTxBytes, err := h.applyAndVerifySignature(directRefundTx, directRefundSignature, signedDirectNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify direct refund tx signature: %w", err)
	}

	// Apply and verify direct from CPFP refund transaction signature
	_, directFromCpfpRefundTxBytes, err := h.applyAndVerifySignature(directFromCpfpRefundTx, directFromCpfpRefundSignature, signedNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify direct from cpfp refund tx signature: %w", err)
	}

	// Update the leaf with refreshed transactions
	leaf, err = leaf.Update().
		SetRawTx(nodeTxBytes).
		SetRawRefundTx(refundTxBytes).
		SetDirectTx(directNodeTxBytes).
		SetDirectRefundTx(directRefundTxBytes).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTxBytes).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update leaf: %w", err)
	}

	// Marshal the updated leaf node into proto
	updatedLeafProto, err := leaf.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal updated leaf node %s on spark: %w", leaf.ID.String(), err)
	}

	err = h.sendFinalizeRefundTimelockGossipMessage(ctx, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to send gossip message: %w", err)
	}

	return &pb.RenewLeafResponse{
		RenewResult: &pb.RenewLeafResponse_RenewRefundTimelockResult{
			RenewRefundTimelockResult: &pb.RenewRefundTimelockResult{
				Node: updatedLeafProto,
			},
		},
	}, nil
}

// renewNodeZeroTimelock resets the timelock for a node that is at zero sequence and cannot be decremented further
func (h *RenewLeafHandler) renewNodeZeroTimelock(ctx context.Context, signingJob *pb.RenewNodeZeroTimelockSigningJob, leaf *ent.TreeNode) (*pb.RenewLeafResponse, error) {
	err := h.validateRenewNodeZeroTimelock(leaf)
	if err != nil {
		return nil, fmt.Errorf("validating zero timelock renewal failed: %w", err)
	}

	// Validate that all direct signing jobs are present
	if signingJob.DirectNodeTxSigningJob == nil {
		return nil, errors.InvalidUserInputErrorf("direct node tx signing job is required")
	}
	if signingJob.DirectRefundTxSigningJob == nil {
		return nil, errors.InvalidUserInputErrorf("direct refund tx signing job is required")
	}
	if signingJob.DirectFromCpfpRefundTxSigningJob == nil {
		return nil, errors.InvalidUserInputErrorf("direct from cpfp refund tx signing job is required")
	}

	// Construct transactions
	nodeTx, refundTx, directNodeTx, directRefundTx, directFromCpfpRefundTx, err := h.constructRenewZeroNodeTransactions(leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to construct renew zero timelock transactions: %w", err)
	}

	userRawTxs := [][]byte{signingJob.NodeTxSigningJob.RawTx, signingJob.RefundTxSigningJob.RawTx, signingJob.DirectNodeTxSigningJob.RawTx, signingJob.DirectRefundTxSigningJob.RawTx, signingJob.DirectFromCpfpRefundTxSigningJob.RawTx}
	expectedTxs := []*wire.MsgTx{nodeTx, refundTx, directNodeTx, directRefundTx, directFromCpfpRefundTx}
	err = h.validateUserTransactions(userRawTxs, expectedTxs)
	if err != nil {
		return nil, fmt.Errorf("user transaction validation failed: %w", err)
	}

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

	// Get the original leaf transaction for parent output
	originalTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse original transaction: %w", err)
	}

	// Create node transaction signing job (FIRST)
	nodeSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.NodeTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		nodeTx,
		originalTx.TxOut[0], // New node tx spends from original tx directly
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

	// Create direct node transaction signing job (THIRD)
	directNodeSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.DirectNodeTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		directNodeTx,
		originalTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, directNodeSigningJobHelper)

	// Create direct refund transaction signing job (FOURTH)
	directRefundSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.DirectRefundTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		directRefundTx,
		directNodeTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, directRefundSigningJobHelper)

	// Create direct from CPFP refund transaction signing job (FIFTH)
	directFromCpfpRefundSigningJobHelper, err := helper.NewSigningJobWithPregeneratedNonce(
		ctx,
		signingJob.DirectFromCpfpRefundTxSigningJob,
		signingKeyshare,
		verifyingPubKey,
		directFromCpfpRefundTx,
		nodeTx.TxOut[0],
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, directFromCpfpRefundSigningJobHelper)

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

	// Aggregate direct node transaction signature (THIRD)
	directNodeSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[2], signingJob.DirectNodeTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct node signature: %w", err)
	}

	// Aggregate direct refund transaction signature (FOURTH)
	directRefundSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[3], signingJob.DirectRefundTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct refund signature: %w", err)
	}

	// Aggregate direct from CPFP refund transaction signature (FIFTH)
	directFromCpfpRefundSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[4], signingJob.DirectFromCpfpRefundTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct from cpfp refund signature: %w", err)
	}

	// Apply signatures to transactions
	signedNodeTx, nodeTxBytes, err := h.applyAndVerifySignature(nodeTx, nodeSignature, originalTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify node tx signature: %w", err)
	}

	_, refundTxBytes, err := h.applyAndVerifySignature(refundTx, refundSignature, signedNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify refund tx signature: %w", err)
	}

	// Apply and verify direct node transaction signature
	signedDirectNodeTx, directNodeTxBytes, err := h.applyAndVerifySignature(directNodeTx, directNodeSignature, originalTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify direct node tx signature: %w", err)
	}

	// Apply and verify direct refund transaction signature
	_, directRefundTxBytes, err := h.applyAndVerifySignature(directRefundTx, directRefundSignature, signedDirectNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify direct refund tx signature: %w", err)
	}

	// Apply and verify direct from CPFP refund transaction signature
	_, directFromCpfpRefundTxBytes, err := h.applyAndVerifySignature(directFromCpfpRefundTx, directFromCpfpRefundSignature, signedNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply and verify direct from cpfp refund tx signature: %w", err)
	}

	// For zero timelock renewal, we need to create a new split node and update the leaf
	// This is similar to the renewNodeTimelock flow but uses the existing node as the split
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
		SetRawTx(leaf.RawTx).
		SetDirectTx(leaf.DirectTx).
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
		SetDirectTx(directNodeTxBytes).
		SetDirectRefundTx(directRefundTxBytes).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTxBytes).
		SetParentID(splitNode.ID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update leaf: %w", err)
	}

	// Marshal the split node into proto
	splitNodeProto, err := splitNode.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal split node %s on spark: %w", splitNode.ID.String(), err)
	}

	// Marshal the new leaf node into proto
	leafProto, err := leaf.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal new leaf node %s on spark: %w", leaf.ID.String(), err)
	}

	// Reuse finalize node timelock gossip message
	err = h.sendFinalizeNodeTimelockGossipMessage(ctx, splitNode, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to send gossip message: %w", err)
	}

	return &pb.RenewLeafResponse{
		RenewResult: &pb.RenewLeafResponse_RenewNodeZeroTimelockResult{
			RenewNodeZeroTimelockResult: &pb.RenewNodeZeroTimelockResult{
				SplitNode: splitNodeProto,
				Node:      leafProto,
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

// constructRenewNodeTransactions creates the split node, extended node, refund transactions, and all direct transactions
func (h *RenewLeafHandler) constructRenewNodeTransactions(leaf, parentLeaf *ent.TreeNode) (*wire.MsgTx, *wire.MsgTx, *wire.MsgTx, *wire.MsgTx, *wire.MsgTx, *wire.MsgTx, *wire.MsgTx, error) {
	parentTx, err := common.TxFromRawTxBytes(parentLeaf.RawTx)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to parse parent node transaction: %w", err)
	}
	parentAmount := parentTx.TxOut[0].Value

	// Construct split node transaction using parent node tx as prev outpoint
	splitNodeTx := wire.NewMsgTx(3)
	splitNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
		Sequence:         spark.ZeroSequence,
	})
	verifyingPubkey, err := keys.ParsePublicKey(leaf.VerifyingPubkey)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to parse verifying pubkey: %w", err)
	}
	outputPkScript, err := common.P2TRScriptFromPubKey(verifyingPubkey)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to construct pkscript: %w", err)
	}
	splitNodeTx.AddTxOut(wire.NewTxOut(parentAmount, outputPkScript))
	splitNodeTx.AddTxOut(common.EphemeralAnchorOutput())

	// Create extended node tx to spend the split node tx
	extendedNodeTx := wire.NewMsgTx(3)
	extendedNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: splitNodeTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence(),
	})
	extendedNodeTx.AddTxOut(wire.NewTxOut(parentAmount, outputPkScript))
	// Add ephemeral anchor output for CPFP
	extendedNodeTx.AddTxOut(common.EphemeralAnchorOutput())

	// Create refund tx to spend the extended node tx
	ownerSigningPubkey, err := keys.ParsePublicKey(leaf.OwnerSigningPubkey)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to parse owner signing pubkey: %w", err)
	}
	refundPkScript, err := common.P2TRScriptFromPubKey(ownerSigningPubkey)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to create refund script: %w", err)
	}
	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: extendedNodeTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence(),
	})
	refundTx.AddTxOut(&wire.TxOut{
		Value:    parentAmount,
		PkScript: refundPkScript,
	})
	// Add ephemeral anchor output for CPFP
	refundTx.AddTxOut(common.EphemeralAnchorOutput())

	// Direct split node tx uses parent node tx as prev outpoint with parent node value (no fee applied)
	directSplitNodeTx := wire.NewMsgTx(3)
	directSplitNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
		Sequence:         spark.DirectTimelockOffset,
	})
	directSplitNodeTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(parentAmount),
		PkScript: outputPkScript,
	})

	directNodeTx := wire.NewMsgTx(3)
	directNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: splitNodeTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence() + spark.DirectTimelockOffset,
	})
	directNodeTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(parentAmount),
		PkScript: outputPkScript,
	})

	directRefundTx := wire.NewMsgTx(3)
	directRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: directNodeTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence() + spark.DirectTimelockOffset,
	})
	directRefundTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(directNodeTx.TxOut[0].Value),
		PkScript: refundPkScript,
	})

	directFromCpfpRefundTx := wire.NewMsgTx(3)
	directFromCpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: extendedNodeTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence() + spark.DirectTimelockOffset,
	})
	directFromCpfpRefundTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(parentAmount),
		PkScript: refundPkScript,
	})

	return splitNodeTx, extendedNodeTx, refundTx, directSplitNodeTx, directNodeTx, directRefundTx, directFromCpfpRefundTx, nil
}

// constructRenewRefundTransactions creates the node and refund transactions
func (h *RenewLeafHandler) constructRenewRefundTransactions(leaf, parentLeaf *ent.TreeNode) (*wire.MsgTx, *wire.MsgTx, *wire.MsgTx, *wire.MsgTx, *wire.MsgTx, error) {
	parentTx, err := common.TxFromRawTxBytes(parentLeaf.RawTx)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to parse parent node transaction: %w", err)
	}
	parentAmount := parentTx.TxOut[0].Value
	oldNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to parse parent node transaction: %w", err)
	}
	newNodeSequence, err := spark.NextSequence(oldNodeTx.TxIn[0].Sequence)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to get next sequence: %w", err)
	}

	nodeTx := wire.NewMsgTx(3)
	nodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
		Sequence:         newNodeSequence,
	})

	verifyingPubkey, err := keys.ParsePublicKey(leaf.VerifyingPubkey)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to parse verifying pubkey: %w", err)
	}
	nodePkScript, err := common.P2TRScriptFromPubKey(verifyingPubkey)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to construct pkscript: %w", err)
	}
	nodeTx.AddTxOut(&wire.TxOut{
		PkScript: nodePkScript,
		Value:    parentAmount,
	})
	nodeTx.AddTxOut(common.EphemeralAnchorOutput())

	// Create refund tx to spend the extended node tx
	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence(),
	})

	ownerSigningPubkey, err := keys.ParsePublicKey(leaf.OwnerSigningPubkey)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to parse owner signing pubkey: %w", err)
	}
	refundPkScript, err := common.P2TRScriptFromPubKey(ownerSigningPubkey)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to create refund script: %w", err)
	}
	refundTx.AddTxOut(&wire.TxOut{
		Value:    parentAmount,
		PkScript: refundPkScript,
	})
	// Add ephemeral anchor output for CPFP
	refundTx.AddTxOut(common.EphemeralAnchorOutput())

	directNodeTx := wire.NewMsgTx(3)
	directNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
		Sequence:         newNodeSequence + spark.DirectTimelockOffset,
	})
	directNodeTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(parentAmount),
		PkScript: nodePkScript,
	})

	directRefundTx := wire.NewMsgTx(3)
	directRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: directNodeTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence() + spark.DirectTimelockOffset,
	})
	directRefundTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(directNodeTx.TxOut[0].Value),
		PkScript: refundPkScript,
	})

	directFromCpfpRefundTx := wire.NewMsgTx(3)
	directFromCpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence() + spark.DirectTimelockOffset,
	})
	directFromCpfpRefundTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(parentAmount),
		PkScript: refundPkScript,
	})

	return nodeTx, refundTx, directNodeTx, directRefundTx, directFromCpfpRefundTx, nil
}

// constructRenewZeroNodeTransactions creates the node and refund transactions for zero timelock renewal
func (h *RenewLeafHandler) constructRenewZeroNodeTransactions(leaf *ent.TreeNode) (*wire.MsgTx, *wire.MsgTx, *wire.MsgTx, *wire.MsgTx, *wire.MsgTx, error) {
	leafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to parse leaf node transaction: %w", err)
	}
	leafAmount := leafNodeTx.TxOut[0].Value

	// Create new node tx with zero sequence (timelock = 0)
	newNodeTx := wire.NewMsgTx(3)
	newNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leafNodeTx.TxHash(), Index: 0},
		Sequence:         spark.ZeroSequence,
	})

	// Use same output value and script as original node tx
	verifyingPubkey, err := keys.ParsePublicKey(leaf.VerifyingPubkey)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to parse verifying pubkey: %w", err)
	}
	nodePkScript, err := common.P2TRScriptFromPubKey(verifyingPubkey)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to construct pkscript: %w", err)
	}
	newNodeTx.AddTxOut(wire.NewTxOut(leafAmount, nodePkScript))
	// Add ephemeral anchor output for CPFP
	newNodeTx.AddTxOut(common.EphemeralAnchorOutput())

	// Create refund tx to spend the new node tx with initial sequence (timelock = 2000)
	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: newNodeTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence(),
	})

	ownerSigningPubkey, err := keys.ParsePublicKey(leaf.OwnerSigningPubkey)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to parse owner signing pubkey: %w", err)
	}
	refundPkScript, err := common.P2TRScriptFromPubKey(ownerSigningPubkey)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to create refund script: %w", err)
	}
	refundTx.AddTxOut(&wire.TxOut{
		Value:    leafAmount,
		PkScript: refundPkScript,
	})
	// Add ephemeral anchor output for CPFP
	refundTx.AddTxOut(common.EphemeralAnchorOutput())

	directNodeTx := wire.NewMsgTx(3)
	directNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leafNodeTx.TxHash(), Index: 0},
		Sequence:         spark.DirectTimelockOffset,
	})
	directNodeTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(leafAmount),
		PkScript: nodePkScript,
	})

	directRefundTx := wire.NewMsgTx(3)
	directRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: directNodeTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence() + spark.DirectTimelockOffset,
	})
	directRefundTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(directNodeTx.TxOut[0].Value),
		PkScript: refundPkScript,
	})

	directFromCpfpRefundTx := wire.NewMsgTx(3)
	directFromCpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: newNodeTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence() + spark.DirectTimelockOffset,
	})
	directFromCpfpRefundTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(leafAmount),
		PkScript: refundPkScript,
	})

	return newNodeTx, refundTx, directNodeTx, directRefundTx, directFromCpfpRefundTx, nil
}

// validateRenewNodeTimelocks validates the timelock requirements for a renew
// node timelock operation. Both the node transaction and the refund transaction
// must have a timelock of 300 or less.
func (h *RenewLeafHandler) validateRenewNodeTimelocks(leaf *ent.TreeNode) error {
	// Check the leaf's node transaction sequence
	leafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return fmt.Errorf("failed to parse leaf node transaction: %w", err)
	}
	if len(leafNodeTx.TxIn) == 0 {
		return fmt.Errorf("found no tx inputs for leaf node tx %v", leafNodeTx)
	}
	nodeTimelock := leafNodeTx.TxIn[0].Sequence & 0xffff

	if nodeTimelock > 300 {
		return errors.FailedPreconditionErrorf("leaf %s node transaction sequence must be less than or equal to 300, got %d", leaf.ID, nodeTimelock)
	}

	leafRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("failed to parse leaf refund transaction: %w", err)
	}
	if len(leafRefundTx.TxIn) == 0 {
		return fmt.Errorf("found no tx inputs for leaf refund tx %v", leafRefundTx)
	}
	refundTimelock := leafRefundTx.TxIn[0].Sequence & 0xffff
	if refundTimelock > 300 {
		return errors.FailedPreconditionErrorf("leaf %s refund transaction sequence must be less than or equal to 300, got %d", leaf.ID, nodeTimelock)
	}

	return nil
}

// validateRenewRefundTimelock validates the timelock requirements for a renew
// refund timelock operation. Refund timelock must be <= 300, and the node
// timelock must not go below 100 following a decrement.
func (h *RenewLeafHandler) validateRenewRefundTimelock(leaf *ent.TreeNode) error {
	// Check the leaf's refund transaction sequence
	leafRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("failed to parse leaf refund transaction: %w", err)
	}
	if len(leafRefundTx.TxIn) == 0 {
		return fmt.Errorf("found no tx inputs for leaf refund tx %v", leafRefundTx)
	}
	refundTimelock := leafRefundTx.TxIn[0].Sequence & 0xffff

	if refundTimelock > 300 {
		return errors.FailedPreconditionErrorf("leaf %s refund transaction sequence must be less than or equal to 300, got %d", leaf.ID, refundTimelock)
	}

	// Check the next sequence of the leaf's node transaction
	leafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return fmt.Errorf("failed to parse leaf node transaction: %w", err)
	}
	if len(leafNodeTx.TxIn) == 0 {
		return fmt.Errorf("found no tx inputs for leaf node tx %v", leafNodeTx)
	}
	nextNodeSequence, err := spark.NextSequence(leafNodeTx.TxIn[0].Sequence)
	if err != nil {
		return fmt.Errorf("failed to decrement node tx timelock: %w", err)
	}
	nextNodeTimelock := nextNodeSequence & 0xffff

	if nextNodeTimelock < 100 {
		return errors.FailedPreconditionErrorf("next leaf %s node transaction sequence must be 100 or greater, got %d", leaf.ID, nextNodeTimelock)
	}

	return nil
}

// validateRenewNodeZeroTimelock validates the timelock requirements for a renew
// node zero timelock operation. The node transaction must have a timelock of 0
// and the refund transaction must have a timelock of 300 or less.
func (h *RenewLeafHandler) validateRenewNodeZeroTimelock(leaf *ent.TreeNode) error {
	// Check the leaf's node transaction sequence
	leafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return fmt.Errorf("failed to parse leaf node transaction: %w", err)
	}
	if len(leafNodeTx.TxIn) == 0 {
		return fmt.Errorf("found no tx inputs for leaf node tx %v", leafNodeTx)
	}
	nodeTimelock := leafNodeTx.TxIn[0].Sequence & 0xffff

	if nodeTimelock != 0 {
		return errors.FailedPreconditionErrorf("leaf %s node transaction sequence must be 0 for zero timelock renewal, got %d", leaf.ID, nodeTimelock)
	}

	// Check the leaf's refund transaction sequence
	leafRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("failed to parse leaf refund transaction: %w", err)
	}
	if len(leafRefundTx.TxIn) == 0 {
		return fmt.Errorf("found no tx inputs for leaf refund tx %v", leafRefundTx)
	}
	refundTimelock := leafRefundTx.TxIn[0].Sequence & 0xffff

	if refundTimelock > 300 {
		return errors.FailedPreconditionErrorf("leaf %s refund transaction sequence must be less than or equal to 300, got %d", leaf.ID, refundTimelock)
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

// validateUserTransactions validates that user-provided raw transaction bytes match expected wire transactions
func (h *RenewLeafHandler) validateUserTransactions(userRawTxs [][]byte, expectedTxs []*wire.MsgTx) error {
	if len(userRawTxs) != len(expectedTxs) {
		return fmt.Errorf("mismatch between number of raw transactions (%d) and wire transactions (%d)", len(userRawTxs), len(expectedTxs))
	}

	for i, rawTx := range userRawTxs {
		userTx, err := common.TxFromRawTxBytes(rawTx)
		if err != nil {
			return fmt.Errorf("failed to deserialize user tx at index %d: %w", i, err)
		}

		err = common.CompareTransactions(expectedTxs[i], userTx)
		if err != nil {
			return fmt.Errorf("user signed tx validation failed at index %d: %w", i, err)
		}
	}

	return nil
}

func (h *RenewLeafHandler) sendFinalizeNodeTimelockGossipMessage(ctx context.Context, splitNode *ent.TreeNode, extendedNode *ent.TreeNode) error {
	// Create internal nodes for the gossip message
	splitNodeInternal, err := splitNode.MarshalInternalProto(ctx)
	if err != nil {
		return fmt.Errorf("unable to marshal split node to internal proto: %w", err)
	}
	extendedNodeInternal, err := extendedNode.MarshalInternalProto(ctx)
	if err != nil {
		return fmt.Errorf("unable to marshal extended node to internal proto: %w", err)
	}
	// Get operator selection to exclude self
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	participants, err := selection.OperatorIdentifierList(h.config)
	if err != nil {
		return fmt.Errorf("unable to get operator list: %w", err)
	}
	// Create and send gossip message
	sendGossipHandler := NewSendGossipHandler(h.config)
	_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_FinalizeNodeTimelock{
			FinalizeNodeTimelock: &pbgossip.GossipMessageFinalizeRenewNodeTimelock{
				SplitNode: splitNodeInternal,
				Node:      extendedNodeInternal,
			},
		},
	}, participants)
	if err != nil {
		return fmt.Errorf("unable to create and send gossip message: %w", err)
	}
	return nil
}

func (h *RenewLeafHandler) sendFinalizeRefundTimelockGossipMessage(ctx context.Context, node *ent.TreeNode) error {
	// Create internal node for the gossip message
	nodeInternal, err := node.MarshalInternalProto(ctx)
	if err != nil {
		return fmt.Errorf("unable to marshal node to internal proto: %w", err)
	}

	// Get operator selection to exclude self
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	participants, err := selection.OperatorIdentifierList(h.config)
	if err != nil {
		return fmt.Errorf("unable to get operator list: %w", err)
	}

	// Create and send gossip message
	sendGossipHandler := NewSendGossipHandler(h.config)
	_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_FinalizeRefundTimelock{
			FinalizeRefundTimelock: &pbgossip.GossipMessageFinalizeRenewRefundTimelock{
				Node: nodeInternal,
			},
		},
	}, participants)
	if err != nil {
		return fmt.Errorf("unable to create and send gossip message: %w", err)
	}
	return nil
}
