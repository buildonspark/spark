package handler

import (
	"context"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"google.golang.org/grpc/codes"
)

// sigEntry holds one signing job's user job, constructed transaction, and previous output.
// Shared between coordinator flow setup, participant sighash verification, and legacy signing.
type sigEntry struct {
	UserJob *pb.UserSignedTxSigningJob
	Tx      *wire.MsgTx
	PrevOut *wire.TxOut
}

// RenewNodeTransactions encapsulates the return values from constructRenewNodeTransactions
type RenewNodeTransactions struct {
	SplitNodeTx            *wire.MsgTx
	NodeTx                 *wire.MsgTx
	RefundTx               *wire.MsgTx
	DirectSplitNodeTx      *wire.MsgTx
	DirectNodeTx           *wire.MsgTx
	DirectRefundTx         *wire.MsgTx
	DirectFromCpfpRefundTx *wire.MsgTx
}

// RenewRefundTransactions encapsulates the return values from constructRenewRefundTransactions
type RenewRefundTransactions struct {
	NodeTx                 *wire.MsgTx
	RefundTx               *wire.MsgTx
	DirectNodeTx           *wire.MsgTx
	DirectRefundTx         *wire.MsgTx
	DirectFromCpfpRefundTx *wire.MsgTx
}

// RenewZeroNodeTransactions encapsulates the return values from constructRenewZeroNodeTransactions
type RenewZeroNodeTransactions struct {
	NodeTx                 *wire.MsgTx
	RefundTx               *wire.MsgTx
	DirectNodeTx           *wire.MsgTx
	DirectFromCpfpRefundTx *wire.MsgTx
}

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

func (h *RenewLeafHandler) NodeAvailableForRenew(ctx context.Context, req *pbinternal.NodeAvailableForRenewRequest) error {
	// Read-only availability check. The consensus path uses ConsensusPrepare
	// RPC (which dispatches to FlowHandler.Prepare) instead of this endpoint,
	// so this must remain read-only to avoid locking nodes without rollback
	// during mixed rollout.
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get database from context: %w", err)
	}

	id, err := uuid.Parse(req.NodeId)
	if err != nil {
		return fmt.Errorf("failed to parse leaf id: %w", err)
	}

	leaf, err := db.TreeNode.
		Query().
		Where(enttreenode.ID(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return errors.NotFoundMissingEntity(fmt.Errorf("leaf with id %s not found", req.NodeId))
		}
		return fmt.Errorf("failed to get leaf node: %w", err)
	}

	if leaf.Status != st.TreeNodeStatusAvailable {
		return errors.FailedPreconditionInvalidState(fmt.Errorf("leaf node is not available for renewal, current status: %s", leaf.Status))
	}

	return nil
}

// RenewLeaf manages timelocks of nodes. This function validates user-sent signing jobs, signs them, aggregates them,
// and then updates internal data model with the signed transactions.
func (h *RenewLeafHandler) RenewLeaf(ctx context.Context, req *pb.RenewLeafRequest) (*pb.RenewLeafResponse, error) {
	// Get the leaf from the database
	leafUUID, err := uuid.Parse(req.LeafId)
	if err != nil {
		return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse leaf id: %w", err))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get database from context: %w", err)
	}

	leaf, err := db.TreeNode.
		Query().
		Where(enttreenode.ID(leafUUID)).
		ForUpdate().
		WithParent().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, errors.NotFoundMissingEntity(fmt.Errorf("leaf with id %s not found", req.LeafId))
		}
		return nil, fmt.Errorf("failed to get leaf node: %w", err)
	}

	if leaf.Status != st.TreeNodeStatusAvailable {
		return nil, errors.FailedPreconditionInvalidState(fmt.Errorf("leaf node is not available for renewal, current status: %s", leaf.Status))
	}

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, leaf.OwnerIdentityPubkey); err != nil {
		return nil, err
	}

	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobUseConsensusRenew, 0) > 0 {
		flow, err := buildCoordinatorFlow(ctx, h.config, req, leaf)
		if err != nil {
			return nil, err
		}
		engine, err := consensus.GetEngine(ctx)
		if err != nil {
			return nil, err
		}
		selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
		_, err = engine.Execute(ctx,
			pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_RENEW_LEAF,
			&selection,
			flow,
		)
		if err != nil {
			return nil, fmt.Errorf("consensus renew failed: %w", err)
		}
		return flow.response, nil
	}

	// Legacy path: read-only availability check on remote SOs, then
	// coordinator-only signing. To disable renew-node operations, disable the
	// consensus knob (which routes here) and use KnobShutdownRenewNode below.
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.NodeAvailableForRenew(ctx, &pbinternal.NodeAvailableForRenewRequest{NodeId: leaf.ID.String()})
	})
	if err != nil {
		return nil, fmt.Errorf("failed to check if node is available for renew: %w", err)
	}

	switch req.SigningJobs.(type) {
	case *pb.RenewLeafRequest_RenewNodeTimelockSigningJob:
		if knobs.GetKnobsService(ctx).GetValue(knobs.KnobShutdownRenewNode, 0) > 0 {
			return nil, errors.WrapErrorWithCodeAndReason(fmt.Errorf("renew node is currently disabled"), codes.Unavailable, errors.ReasonUnavailableMethodDisabled)
		}
		return h.renewNodeTimelock(ctx, req.GetRenewNodeTimelockSigningJob(), leaf)
	case *pb.RenewLeafRequest_RenewRefundTimelockSigningJob:
		return h.renewRefundTimelock(ctx, req.GetRenewRefundTimelockSigningJob(), leaf)
	case *pb.RenewLeafRequest_RenewNodeZeroTimelockSigningJob:
		return h.renewNodeZeroTimelock(ctx, req.GetRenewNodeZeroTimelockSigningJob(), leaf)
	default:
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("request must specify a signing job (RenewNodeTimelockSigningJob, RenewRefundTimelockSigningJob, or RenewNodeZeroTimelockSigningJob)"))
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
	parentLeaf, renewTxs, entries, err := validateAndConstructNodeTimelock(ctx, leaf, signingJob)
	if err != nil {
		return nil, err
	}

	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}

	var signingJobs []*helper.SigningJobWithPregeneratedNonce
	for _, e := range entries {
		j, err := helper.NewSigningJobWithPregeneratedNonce(e.UserJob, signingKeyshare, leaf.VerifyingPubkey, e.Tx, e.PrevOut)
		if err != nil {
			return nil, err
		}
		signingJobs = append(signingJobs, j)
	}

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

	// Signatures order: [node, refund, splitNode, directSplitNode, directNode, directRefund, directFromCpfpRefund]
	signatures := [][]byte{nodeSignature, refundSignature, splitNodeSignature, directSplitNodeSignature, directNodeSignature, directRefundSignature, directFromCpfpRefundSignature}

	result, err := finalizeRenewNodeTimelockDB(ctx, leaf, parentLeaf, renewTxs, signingKeyshare, signatures)
	if err != nil {
		return nil, err
	}

	err = h.sendFinalizeNodeTimelockGossipMessage(ctx, result.splitNode, result.leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to send gossip message: %w", err)
	}

	return result.response, nil
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
	parentLeaf, refundTxs, entries, err := validateAndConstructRefundTimelock(ctx, leaf, signingJob)
	if err != nil {
		return nil, err
	}

	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}

	var signingJobs []*helper.SigningJobWithPregeneratedNonce
	for _, e := range entries {
		j, err := helper.NewSigningJobWithPregeneratedNonce(e.UserJob, signingKeyshare, leaf.VerifyingPubkey, e.Tx, e.PrevOut)
		if err != nil {
			return nil, err
		}
		signingJobs = append(signingJobs, j)
	}

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

	// Signatures order: [node, refund, directNode, directRefund, directFromCpfpRefund]
	signatures := [][]byte{nodeSignature, refundSignature, directNodeSignature, directRefundSignature, directFromCpfpRefundSignature}

	result, err := finalizeRenewRefundTimelockDB(ctx, leaf, parentLeaf, refundTxs, signatures)
	if err != nil {
		return nil, err
	}

	err = h.sendFinalizeRefundTimelockGossipMessage(ctx, result.leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to send gossip message: %w", err)
	}

	return result.response, nil
}

// renewNodeZeroTimelock resets the timelock for a node that is at zero sequence and cannot be decremented further
/*
BEFORE                                AFTER
---------------------------           ------------------------------------------------------------------------------------------------------------
(node_tx timelock: 0)                 (node_tx timelock: 0)                // This transaction is to invalidate all the previously signed node_tx.
|                                     |                             \      // This is a tempory solution to make sure that watchtower is able to
|                                     |                              \     // prevent attacks. But after this is broadcasted, user will need to
|                                     v                               \    // work with SOs to sign an exit transaction to claim the funds back
|                                     (new_node_tx: timelock: 0)       \-> (direct_node_tx: timelock 50)
|                                     |                       \
|                                     |                        \
v                                     v                         \
(refund_tx: timelock:100)             (refund_tx  )              \-> (direct_refund_tx_from_cpfp)
                                      (timelock:2000)                (timelock 2050             )
*/
func (h *RenewLeafHandler) renewNodeZeroTimelock(ctx context.Context, signingJob *pb.RenewNodeZeroTimelockSigningJob, leaf *ent.TreeNode) (*pb.RenewLeafResponse, error) {
	zeroTxs, entries, err := validateAndConstructNodeZeroTimelock(leaf, signingJob)
	if err != nil {
		return nil, err
	}

	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}

	var signingJobs []*helper.SigningJobWithPregeneratedNonce
	for _, e := range entries {
		j, err := helper.NewSigningJobWithPregeneratedNonce(e.UserJob, signingKeyshare, leaf.VerifyingPubkey, e.Tx, e.PrevOut)
		if err != nil {
			return nil, err
		}
		signingJobs = append(signingJobs, j)
	}

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

	// Aggregate direct from CPFP refund transaction signature (FOURTH)
	directFromCpfpRefundSignature, err := h.aggregateRenewLeafSignature(ctx, signingResults[3], signingJob.DirectFromCpfpRefundTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct from cpfp refund signature: %w", err)
	}

	// Signatures order: [node, refund, directNode, directFromCpfpRefund]
	signatures := [][]byte{nodeSignature, refundSignature, directNodeSignature, directFromCpfpRefundSignature}

	result, err := finalizeRenewNodeZeroTimelockDB(ctx, leaf, zeroTxs, signingKeyshare, signatures)
	if err != nil {
		return nil, err
	}

	err = h.sendFinalizeNodeTimelockGossipMessage(ctx, result.splitNode, result.leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to send gossip message: %w", err)
	}

	return result.response, nil
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
		VerifyingKey:       leaf.VerifyingPubkey.Serialize(),
		Commitments:        userSigningJob.SigningCommitments.SigningCommitments,
		UserCommitments:    userSigningJob.SigningNonceCommitment,
		UserPublicKey:      leaf.OwnerSigningPubkey.Serialize(),
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
			return nil, fmt.Errorf("signing job %s has empty round1Packages (message: %x)", job.JobID, job.Message)
		}
		for key, commitment := range job.Round1Packages {
			if commitment.IsZero() {
				return nil, fmt.Errorf("signing job %s has invalid commitment for key %s: hiding or binding is empty (message: %x)", job.JobID, key, job.Message)
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

// validateAndConstructNodeTimelock validates timelocks, required fields, loads
// the parent leaf, constructs transactions, validates user-provided raw bytes,
// and returns the parent leaf, constructed transactions, and ordered signing entries.
func validateAndConstructNodeTimelock(ctx context.Context, leaf *ent.TreeNode, signingJob *pb.RenewNodeTimelockSigningJob) (*ent.TreeNode, *RenewNodeTransactions, []sigEntry, error) {
	if err := validateRenewNodeTimelocks(leaf); err != nil {
		return nil, nil, nil, fmt.Errorf("validating extend timelock failed: %w", err)
	}

	if signingJob.SplitNodeDirectTxSigningJob == nil {
		return nil, nil, nil, errors.InvalidArgumentMissingField(fmt.Errorf("split node direct tx signing job is required"))
	}
	if signingJob.DirectNodeTxSigningJob == nil {
		return nil, nil, nil, errors.InvalidArgumentMissingField(fmt.Errorf("direct node tx signing job is required"))
	}
	if signingJob.DirectRefundTxSigningJob == nil {
		return nil, nil, nil, errors.InvalidArgumentMissingField(fmt.Errorf("direct refund tx signing job is required"))
	}
	if signingJob.DirectFromCpfpRefundTxSigningJob == nil {
		return nil, nil, nil, errors.InvalidArgumentMissingField(fmt.Errorf("direct from cpfp refund tx signing job is required"))
	}

	parentLeaf, err := getParentLeaf(ctx, leaf)
	if err != nil {
		return nil, nil, nil, err
	}

	renewTxs, err := constructRenewNodeTransactions(leaf, parentLeaf, signingJob)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to construct renew transactions: %w", err)
	}

	userRawTxs := [][]byte{signingJob.SplitNodeTxSigningJob.RawTx, signingJob.NodeTxSigningJob.RawTx, signingJob.RefundTxSigningJob.RawTx, signingJob.SplitNodeDirectTxSigningJob.RawTx, signingJob.DirectNodeTxSigningJob.RawTx, signingJob.DirectRefundTxSigningJob.RawTx, signingJob.DirectFromCpfpRefundTxSigningJob.RawTx}
	expectedTxs := []*wire.MsgTx{renewTxs.SplitNodeTx, renewTxs.NodeTx, renewTxs.RefundTx, renewTxs.DirectSplitNodeTx, renewTxs.DirectNodeTx, renewTxs.DirectRefundTx, renewTxs.DirectFromCpfpRefundTx}
	if err := validateUserTransactions(userRawTxs, expectedTxs); err != nil {
		return nil, nil, nil, fmt.Errorf("user transaction validation failed: %w", err)
	}

	parentTx, err := common.TxFromRawTxBytes(parentLeaf.RawTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse parent transaction: %w", err)
	}

	entries := []sigEntry{
		{signingJob.NodeTxSigningJob, renewTxs.NodeTx, renewTxs.SplitNodeTx.TxOut[0]},
		{signingJob.RefundTxSigningJob, renewTxs.RefundTx, renewTxs.NodeTx.TxOut[0]},
		{signingJob.SplitNodeTxSigningJob, renewTxs.SplitNodeTx, parentTx.TxOut[0]},
		{signingJob.SplitNodeDirectTxSigningJob, renewTxs.DirectSplitNodeTx, parentTx.TxOut[0]},
		{signingJob.DirectNodeTxSigningJob, renewTxs.DirectNodeTx, renewTxs.SplitNodeTx.TxOut[0]},
		{signingJob.DirectRefundTxSigningJob, renewTxs.DirectRefundTx, renewTxs.DirectNodeTx.TxOut[0]},
		{signingJob.DirectFromCpfpRefundTxSigningJob, renewTxs.DirectFromCpfpRefundTx, renewTxs.NodeTx.TxOut[0]},
	}

	return parentLeaf, renewTxs, entries, nil
}

// validateAndConstructRefundTimelock validates timelocks, required fields, loads
// the parent leaf, constructs transactions, validates user-provided raw bytes,
// and returns the parent leaf, constructed transactions, and ordered signing entries.
func validateAndConstructRefundTimelock(ctx context.Context, leaf *ent.TreeNode, signingJob *pb.RenewRefundTimelockSigningJob) (*ent.TreeNode, *RenewRefundTransactions, []sigEntry, error) {
	if err := validateRenewRefundTimelock(leaf); err != nil {
		return nil, nil, nil, fmt.Errorf("validating refresh timelock failed: %w", err)
	}

	if signingJob.DirectNodeTxSigningJob == nil {
		return nil, nil, nil, errors.InvalidArgumentMissingField(fmt.Errorf("direct node tx signing job is required"))
	}
	if signingJob.DirectRefundTxSigningJob == nil {
		return nil, nil, nil, errors.InvalidArgumentMissingField(fmt.Errorf("direct refund tx signing job is required"))
	}
	if signingJob.DirectFromCpfpRefundTxSigningJob == nil {
		return nil, nil, nil, errors.InvalidArgumentMissingField(fmt.Errorf("direct from cpfp refund tx signing job is required"))
	}

	parentLeaf, err := getParentLeaf(ctx, leaf)
	if err != nil {
		return nil, nil, nil, err
	}

	refundTxs, err := constructRenewRefundTransactions(leaf, parentLeaf, signingJob)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to construct renew transactions: %w", err)
	}

	userRawTxs := [][]byte{signingJob.NodeTxSigningJob.RawTx, signingJob.RefundTxSigningJob.RawTx, signingJob.DirectNodeTxSigningJob.RawTx, signingJob.DirectRefundTxSigningJob.RawTx, signingJob.DirectFromCpfpRefundTxSigningJob.RawTx}
	expectedTxs := []*wire.MsgTx{refundTxs.NodeTx, refundTxs.RefundTx, refundTxs.DirectNodeTx, refundTxs.DirectRefundTx, refundTxs.DirectFromCpfpRefundTx}
	if err := validateUserTransactions(userRawTxs, expectedTxs); err != nil {
		return nil, nil, nil, fmt.Errorf("user transaction validation failed: %w", err)
	}

	parentTx, err := common.TxFromRawTxBytes(parentLeaf.RawTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse parent transaction: %w", err)
	}

	entries := []sigEntry{
		{signingJob.NodeTxSigningJob, refundTxs.NodeTx, parentTx.TxOut[0]},
		{signingJob.RefundTxSigningJob, refundTxs.RefundTx, refundTxs.NodeTx.TxOut[0]},
		{signingJob.DirectNodeTxSigningJob, refundTxs.DirectNodeTx, parentTx.TxOut[0]},
		{signingJob.DirectRefundTxSigningJob, refundTxs.DirectRefundTx, refundTxs.DirectNodeTx.TxOut[0]},
		{signingJob.DirectFromCpfpRefundTxSigningJob, refundTxs.DirectFromCpfpRefundTx, refundTxs.NodeTx.TxOut[0]},
	}

	return parentLeaf, refundTxs, entries, nil
}

// validateAndConstructNodeZeroTimelock validates timelocks, required fields,
// constructs transactions, validates user-provided raw bytes,
// and returns the constructed transactions and ordered signing entries.
func validateAndConstructNodeZeroTimelock(leaf *ent.TreeNode, signingJob *pb.RenewNodeZeroTimelockSigningJob) (*RenewZeroNodeTransactions, []sigEntry, error) {
	if err := validateRenewNodeZeroTimelock(leaf); err != nil {
		return nil, nil, fmt.Errorf("validating zero timelock renewal failed: %w", err)
	}

	if signingJob.DirectNodeTxSigningJob == nil {
		return nil, nil, errors.InvalidArgumentMissingField(fmt.Errorf("direct node tx signing job is required"))
	}
	if signingJob.DirectFromCpfpRefundTxSigningJob == nil {
		return nil, nil, errors.InvalidArgumentMissingField(fmt.Errorf("direct from cpfp refund tx signing job is required"))
	}

	zeroTxs, err := constructRenewZeroNodeTransactions(leaf, signingJob)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to construct renew zero timelock transactions: %w", err)
	}

	userRawTxs := [][]byte{signingJob.NodeTxSigningJob.RawTx, signingJob.RefundTxSigningJob.RawTx, signingJob.DirectNodeTxSigningJob.RawTx, signingJob.DirectFromCpfpRefundTxSigningJob.RawTx}
	expectedTxs := []*wire.MsgTx{zeroTxs.NodeTx, zeroTxs.RefundTx, zeroTxs.DirectNodeTx, zeroTxs.DirectFromCpfpRefundTx}
	if err := validateUserTransactions(userRawTxs, expectedTxs); err != nil {
		return nil, nil, fmt.Errorf("user transaction validation failed: %w", err)
	}

	originalTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse original transaction: %w", err)
	}

	entries := []sigEntry{
		{signingJob.NodeTxSigningJob, zeroTxs.NodeTx, originalTx.TxOut[0]},
		{signingJob.RefundTxSigningJob, zeroTxs.RefundTx, zeroTxs.NodeTx.TxOut[0]},
		{signingJob.DirectNodeTxSigningJob, zeroTxs.DirectNodeTx, originalTx.TxOut[0]},
		{signingJob.DirectFromCpfpRefundTxSigningJob, zeroTxs.DirectFromCpfpRefundTx, zeroTxs.NodeTx.TxOut[0]},
	}

	return zeroTxs, entries, nil
}

// renewNodeTimelockResult holds the results of finalizing a node timelock renewal.
type renewNodeTimelockResult struct {
	response  *pb.RenewLeafResponse
	splitNode *ent.TreeNode
	leaf      *ent.TreeNode
}

// finalizeRenewNodeTimelockDB applies aggregated signatures, creates a split node,
// updates the leaf, and returns the response proto plus the DB entities needed for
// gossip or commit proto construction. Used by both the legacy and consensus paths.
//
// Signatures order: [node, refund, splitNode, directSplitNode, directNode, directRefund, directFromCpfpRefund]
func finalizeRenewNodeTimelockDB(
	ctx context.Context,
	leaf *ent.TreeNode,
	parentLeaf *ent.TreeNode,
	renewTxs *RenewNodeTransactions,
	signingKeyshare *ent.SigningKeyshare,
	signatures [][]byte,
) (*renewNodeTimelockResult, error) {
	parentTx, err := common.TxFromRawTxBytes(parentLeaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse parent transaction: %w", err)
	}

	signedSplitNodeTx, splitNodeTxBytes, err := applyAndVerifySignature(renewTxs.SplitNodeTx, signatures[2], parentTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply split node tx signature: %w", err)
	}
	signedNodeTx, nodeTxBytes, err := applyAndVerifySignature(renewTxs.NodeTx, signatures[0], signedSplitNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply node tx signature: %w", err)
	}
	_, refundTxBytes, err := applyAndVerifySignature(renewTxs.RefundTx, signatures[1], signedNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply refund tx signature: %w", err)
	}
	_, directSplitNodeTxBytes, err := applyAndVerifySignature(renewTxs.DirectSplitNodeTx, signatures[3], parentTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply direct split node tx signature: %w", err)
	}
	signedDirectNodeTx, directNodeTxBytes, err := applyAndVerifySignature(renewTxs.DirectNodeTx, signatures[4], signedSplitNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply direct node tx signature: %w", err)
	}
	_, directRefundTxBytes, err := applyAndVerifySignature(renewTxs.DirectRefundTx, signatures[5], signedDirectNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply direct refund tx signature: %w", err)
	}
	_, directFromCpfpRefundTxBytes, err := applyAndVerifySignature(renewTxs.DirectFromCpfpRefundTx, signatures[6], signedNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply direct from cpfp refund tx signature: %w", err)
	}

	tree, err := leaf.QueryTree().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get database: %w", err)
	}

	splitNode, err := db.TreeNode.Create().
		SetTreeID(tree.ID).
		SetNetwork(tree.Network).
		SetStatus(st.TreeNodeStatusSplitLocked).
		SetOwnerIdentityPubkey(leaf.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(leaf.OwnerSigningPubkey).
		SetValue(leaf.Value).
		SetVerifyingPubkey(leaf.VerifyingPubkey).
		SetSigningKeyshareID(signingKeyshare.ID).
		SetRawTx(splitNodeTxBytes).
		SetDirectTx(directSplitNodeTxBytes).
		SetVout(leaf.Vout).
		SetParentID(parentLeaf.ID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create split node: %w", err)
	}

	leaf, err = leaf.Update().
		SetRawTx(nodeTxBytes).
		SetRawRefundTx(refundTxBytes).
		SetDirectTx(directNodeTxBytes).
		SetDirectRefundTx(directRefundTxBytes).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTxBytes).
		SetParentID(splitNode.ID).
		SetStatus(st.TreeNodeStatusAvailable).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update leaf: %w", err)
	}

	splitNodeProto, err := splitNode.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal split node: %w", err)
	}
	updatedLeafProto, err := leaf.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal updated leaf: %w", err)
	}

	return &renewNodeTimelockResult{
		response: &pb.RenewLeafResponse{
			RenewResult: &pb.RenewLeafResponse_RenewNodeTimelockResult{
				RenewNodeTimelockResult: &pb.RenewNodeTimelockResult{
					SplitNode: splitNodeProto,
					Node:      updatedLeafProto,
				},
			},
		},
		splitNode: splitNode,
		leaf:      leaf,
	}, nil
}

// renewRefundTimelockResult holds the results of finalizing a refund timelock renewal.
type renewRefundTimelockResult struct {
	response *pb.RenewLeafResponse
	leaf     *ent.TreeNode
}

// finalizeRenewRefundTimelockDB applies aggregated signatures, updates the leaf,
// and returns the response proto. Used by both the legacy and consensus paths.
//
// Signatures order: [node, refund, directNode, directRefund, directFromCpfpRefund]
func finalizeRenewRefundTimelockDB(
	ctx context.Context,
	leaf *ent.TreeNode,
	parentLeaf *ent.TreeNode,
	refundTxs *RenewRefundTransactions,
	signatures [][]byte,
) (*renewRefundTimelockResult, error) {
	parentTx, err := common.TxFromRawTxBytes(parentLeaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse parent transaction: %w", err)
	}

	signedNodeTx, nodeTxBytes, err := applyAndVerifySignature(refundTxs.NodeTx, signatures[0], parentTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply node tx signature: %w", err)
	}
	_, refundTxBytes, err := applyAndVerifySignature(refundTxs.RefundTx, signatures[1], signedNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply refund tx signature: %w", err)
	}
	signedDirectNodeTx, directNodeTxBytes, err := applyAndVerifySignature(refundTxs.DirectNodeTx, signatures[2], parentTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply direct node tx signature: %w", err)
	}
	_, directRefundTxBytes, err := applyAndVerifySignature(refundTxs.DirectRefundTx, signatures[3], signedDirectNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply direct refund tx signature: %w", err)
	}
	_, directFromCpfpRefundTxBytes, err := applyAndVerifySignature(refundTxs.DirectFromCpfpRefundTx, signatures[4], signedNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply direct from cpfp refund tx signature: %w", err)
	}

	leaf, err = leaf.Update().
		SetRawTx(nodeTxBytes).
		SetRawRefundTx(refundTxBytes).
		SetDirectTx(directNodeTxBytes).
		SetDirectRefundTx(directRefundTxBytes).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTxBytes).
		SetStatus(st.TreeNodeStatusAvailable).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update leaf: %w", err)
	}

	updatedLeafProto, err := leaf.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal updated leaf: %w", err)
	}

	return &renewRefundTimelockResult{
		response: &pb.RenewLeafResponse{
			RenewResult: &pb.RenewLeafResponse_RenewRefundTimelockResult{
				RenewRefundTimelockResult: &pb.RenewRefundTimelockResult{
					Node: updatedLeafProto,
				},
			},
		},
		leaf: leaf,
	}, nil
}

// renewNodeZeroTimelockResult holds the results of finalizing a zero-timelock node renewal.
type renewNodeZeroTimelockResult struct {
	response  *pb.RenewLeafResponse
	splitNode *ent.TreeNode
	leaf      *ent.TreeNode
}

// finalizeRenewNodeZeroTimelockDB applies aggregated signatures, creates a split node,
// updates the leaf (clearing DirectRefundTx), and returns the response proto.
// Used by both the legacy and consensus paths.
//
// Signatures order: [node, refund, directNode, directFromCpfpRefund]
func finalizeRenewNodeZeroTimelockDB(
	ctx context.Context,
	leaf *ent.TreeNode,
	zeroTxs *RenewZeroNodeTransactions,
	signingKeyshare *ent.SigningKeyshare,
	signatures [][]byte,
) (*renewNodeZeroTimelockResult, error) {
	originalTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse original transaction: %w", err)
	}

	signedNodeTx, nodeTxBytes, err := applyAndVerifySignature(zeroTxs.NodeTx, signatures[0], originalTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply node tx signature: %w", err)
	}
	_, refundTxBytes, err := applyAndVerifySignature(zeroTxs.RefundTx, signatures[1], signedNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply refund tx signature: %w", err)
	}
	_, directNodeTxBytes, err := applyAndVerifySignature(zeroTxs.DirectNodeTx, signatures[2], originalTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply direct node tx signature: %w", err)
	}
	_, directFromCpfpRefundTxBytes, err := applyAndVerifySignature(zeroTxs.DirectFromCpfpRefundTx, signatures[3], signedNodeTx.TxOut[0], 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply direct from cpfp refund tx signature: %w", err)
	}

	tree, err := leaf.QueryTree().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get database: %w", err)
	}

	mut := db.TreeNode.Create().
		SetTreeID(tree.ID).
		SetNetwork(tree.Network).
		SetStatus(st.TreeNodeStatusSplitLocked).
		SetOwnerIdentityPubkey(leaf.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(leaf.OwnerSigningPubkey).
		SetValue(leaf.Value).
		SetVerifyingPubkey(leaf.VerifyingPubkey).
		SetSigningKeyshareID(signingKeyshare.ID).
		SetRawTx(leaf.RawTx).
		SetDirectTx(leaf.DirectTx).
		SetVout(leaf.Vout)
	if leaf.Edges.Parent != nil {
		mut.SetParentID(leaf.Edges.Parent.ID)
	}
	splitNode, err := mut.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create split node: %w", err)
	}

	leaf, err = leaf.Update().
		SetRawTx(nodeTxBytes).
		SetRawRefundTx(refundTxBytes).
		SetDirectTx(directNodeTxBytes).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTxBytes).
		ClearDirectRefundTx().
		SetParentID(splitNode.ID).
		SetStatus(st.TreeNodeStatusAvailable).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update leaf: %w", err)
	}

	splitNodeProto, err := splitNode.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal split node: %w", err)
	}
	leafProto, err := leaf.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal leaf: %w", err)
	}

	return &renewNodeZeroTimelockResult{
		response: &pb.RenewLeafResponse{
			RenewResult: &pb.RenewLeafResponse_RenewNodeZeroTimelockResult{
				RenewNodeZeroTimelockResult: &pb.RenewNodeZeroTimelockResult{
					SplitNode: splitNodeProto,
					Node:      leafProto,
				},
			},
		},
		splitNode: splitNode,
		leaf:      leaf,
	}, nil
}

// constructRenewNodeTransactions creates the split node, extended node, refund transactions, and all direct transactions
func constructRenewNodeTransactions(leaf, parentLeaf *ent.TreeNode, signingJob *pb.RenewNodeTimelockSigningJob) (*RenewNodeTransactions, error) {
	parentTx, err := common.TxFromRawTxBytes(parentLeaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse parent node transaction: %w", err)
	}
	parentAmount := parentTx.TxOut[0].Value

	// Construct split node transaction using parent node tx as prev outpoint
	splitNodeTx := wire.NewMsgTx(3)
	userSplitNodeSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.SplitNodeTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided split node tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userSplitNodeSequence, spark.ZeroTimelock); err != nil {
		return nil, fmt.Errorf("failed to validate user provided split node tx timelock: %w", err)
	}
	splitNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
		Sequence:         userSplitNodeSequence,
	})
	outputPkScript, err := common.P2TRScriptFromPubKey(leaf.VerifyingPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to construct pkscript: %w", err)
	}
	splitNodeTx.AddTxOut(wire.NewTxOut(parentAmount, outputPkScript))
	splitNodeTx.AddTxOut(common.EphemeralAnchorOutput())

	// Create extended node tx to spend the split node tx
	extendedNodeTx := wire.NewMsgTx(3)
	userExtendedNodeSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.NodeTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided extended node tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userExtendedNodeSequence, spark.InitialTimeLock); err != nil {
		return nil, fmt.Errorf("failed to validate user provided extended node tx timelock: %w", err)
	}
	extendedNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: splitNodeTx.TxHash(), Index: 0},
		Sequence:         userExtendedNodeSequence,
	})
	extendedNodeTx.AddTxOut(wire.NewTxOut(parentAmount, outputPkScript))
	// Add ephemeral anchor output for CPFP
	extendedNodeTx.AddTxOut(common.EphemeralAnchorOutput())

	// Create refund tx to spend the extended node tx
	refundPkScript, err := common.P2TRScriptFromPubKey(leaf.OwnerSigningPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to create refund script: %w", err)
	}
	refundTx := wire.NewMsgTx(3)
	userRefundSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.RefundTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided refund tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userRefundSequence, spark.InitialTimeLock); err != nil {
		return nil, fmt.Errorf("failed to validate user provided refund tx timelock: %w", err)
	}
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: extendedNodeTx.TxHash(), Index: 0},
		Sequence:         userRefundSequence,
	})
	refundTx.AddTxOut(&wire.TxOut{
		Value:    parentAmount,
		PkScript: refundPkScript,
	})
	// Add ephemeral anchor output for CPFP
	refundTx.AddTxOut(common.EphemeralAnchorOutput())

	// Direct split node tx uses parent node tx as prev outpoint with parent node value (no fee applied)
	directSplitNodeTx := wire.NewMsgTx(3)
	userDirectSplitNodeSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.SplitNodeDirectTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct split node tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userDirectSplitNodeSequence, spark.DirectTimelockOffset); err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct split node tx timelock: %w", err)
	}

	directSplitNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
		Sequence:         userDirectSplitNodeSequence,
	})
	directSplitNodeTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(parentAmount),
		PkScript: outputPkScript,
	})

	directNodeTx := wire.NewMsgTx(3)
	// Timelock is not changed in this case
	userDirectNodeSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.DirectNodeTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct node tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userDirectNodeSequence, spark.InitialTimeLock+spark.DirectTimelockOffset); err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct node tx timelock: %w", err)
	}
	directNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: splitNodeTx.TxHash(), Index: 0},
		Sequence:         userDirectNodeSequence,
	})
	directNodeTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(parentAmount),
		PkScript: outputPkScript,
	})

	directRefundTx := wire.NewMsgTx(3)
	// Timelock is not changed in this case
	userDirectRefundSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.DirectRefundTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct refund tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userDirectRefundSequence, spark.InitialTimeLock+spark.DirectTimelockOffset); err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct refund tx timelock: %w", err)
	}
	directRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: directNodeTx.TxHash(), Index: 0},
		Sequence:         userDirectRefundSequence,
	})
	directRefundTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(directNodeTx.TxOut[0].Value),
		PkScript: refundPkScript,
	})

	directFromCpfpRefundTx := wire.NewMsgTx(3)
	// Timelock is not changed in this case
	userDirectFromCpfpRefundSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.DirectFromCpfpRefundTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct from cpfp refund tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userDirectFromCpfpRefundSequence, spark.InitialTimeLock+spark.DirectTimelockOffset); err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct from cpfp tx timelock: %w", err)
	}
	directFromCpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: extendedNodeTx.TxHash(), Index: 0},
		Sequence:         userDirectFromCpfpRefundSequence,
	})
	directFromCpfpRefundTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(parentAmount),
		PkScript: refundPkScript,
	})

	return &RenewNodeTransactions{
		SplitNodeTx:            splitNodeTx,
		NodeTx:                 extendedNodeTx,
		RefundTx:               refundTx,
		DirectSplitNodeTx:      directSplitNodeTx,
		DirectNodeTx:           directNodeTx,
		DirectRefundTx:         directRefundTx,
		DirectFromCpfpRefundTx: directFromCpfpRefundTx,
	}, nil
}

// Create Tree Node transactions that reset the Refunx Tx timelock.
//   - Node Tx timelock is decreased by one step.
//   - Refund Tx timelock is set to Zero.
//   - Direct Txs (node, refund, cpfp) timelock is set to Refund Tx timelock plus one step.
func constructRenewRefundTransactions(leaf, parentLeaf *ent.TreeNode, signingJob *pb.RenewRefundTimelockSigningJob) (*RenewRefundTransactions, error) {
	parentTx, err := common.TxFromRawTxBytes(parentLeaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse parent node transaction: %w", err)
	}
	if len(parentTx.TxOut) == 0 {
		return nil, fmt.Errorf("parent node transaction has zero outputs")
	}
	parentAmount := parentTx.TxOut[0].Value

	// ******************************************************************
	// NODE TX
	// ******************************************************************
	userNodeSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.NodeTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided extended node tx sequence: %w", err)
	}
	oldNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse leaf node transaction: %w", err)
	}
	if len(oldNodeTx.TxIn) == 0 {
		return nil, fmt.Errorf("leaf node transaction has no inputs")
	}
	newNodeSequenceExpected, newDirectNodeSequenceExpected, err := bitcointransaction.NextSequence(oldNodeTx.TxIn[0].Sequence)
	if err != nil {
		return nil, fmt.Errorf("failed to produce new node tx timelock: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userNodeSequence, bitcointransaction.GetTimelockFromSequence(newNodeSequenceExpected)); err != nil {
		return nil, fmt.Errorf("failed to validate user provided node tx timelock: %w", err)
	}

	nodeTx := wire.NewMsgTx(3)
	nodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
		Sequence:         userNodeSequence,
	})

	nodePkScript, err := common.P2TRScriptFromPubKey(leaf.VerifyingPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to construct pkscript: %w", err)
	}
	nodeTx.AddTxOut(&wire.TxOut{
		PkScript: nodePkScript,
		Value:    parentAmount,
	})
	nodeTx.AddTxOut(common.EphemeralAnchorOutput())

	// ******************************************************************
	// REFUND TX
	// Create refund tx to spend the extended node tx
	// ******************************************************************
	userRefundSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.RefundTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided refund tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userRefundSequence, spark.InitialTimeLock); err != nil {
		return nil, fmt.Errorf("failed to validate user provided refund tx timelock: %w", err)
	}

	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0},
		Sequence:         userRefundSequence,
	})

	refundPkScript, err := common.P2TRScriptFromPubKey(leaf.OwnerSigningPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to create refund script: %w", err)
	}
	refundTx.AddTxOut(&wire.TxOut{
		Value:    parentAmount,
		PkScript: refundPkScript,
	})
	// Add ephemeral anchor output for CPFP
	refundTx.AddTxOut(common.EphemeralAnchorOutput())

	// ******************************************************************
	// DIRECT NODE TX
	// ******************************************************************
	userDirectNodeSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.DirectNodeTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct node tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userDirectNodeSequence, bitcointransaction.GetTimelockFromSequence(newDirectNodeSequenceExpected)); err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct node tx timelock: %w", err)
	}

	directNodeTx := wire.NewMsgTx(3)
	directNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
		Sequence:         userDirectNodeSequence,
	})
	directNodeTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(parentAmount),
		PkScript: nodePkScript,
	})

	// ******************************************************************
	// DIRECT REFUND TX
	// ******************************************************************
	userDirectRefundSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.DirectRefundTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct refund tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userDirectRefundSequence, spark.InitialTimeLock+spark.DirectTimelockOffset); err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct refund tx timelock: %w", err)
	}

	directRefundTx := wire.NewMsgTx(3)
	directRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: directNodeTx.TxHash(), Index: 0},
		Sequence:         userDirectRefundSequence,
	})
	directRefundTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(directNodeTx.TxOut[0].Value),
		PkScript: refundPkScript,
	})

	// ******************************************************************
	// DIRECT FROM CPFP REFUND TX
	// ******************************************************************
	userDirectFromCpfpRefundSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.DirectFromCpfpRefundTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct from cpfp refund tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userDirectFromCpfpRefundSequence, spark.InitialTimeLock+spark.DirectTimelockOffset); err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct from cpfp refund tx timelock: %w", err)
	}

	directFromCpfpRefundTx := wire.NewMsgTx(3)
	directFromCpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0},
		Sequence:         userDirectFromCpfpRefundSequence,
	})
	directFromCpfpRefundTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(parentAmount),
		PkScript: refundPkScript,
	})

	return &RenewRefundTransactions{
		NodeTx:                 nodeTx,
		RefundTx:               refundTx,
		DirectNodeTx:           directNodeTx,
		DirectRefundTx:         directRefundTx,
		DirectFromCpfpRefundTx: directFromCpfpRefundTx,
	}, nil
}

// constructRenewZeroNodeTransactions creates the node and refund transactions for zero timelock renewal
func constructRenewZeroNodeTransactions(leaf *ent.TreeNode, signingJob *pb.RenewNodeZeroTimelockSigningJob) (*RenewZeroNodeTransactions, error) {
	leafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse leaf node transaction: %w", err)
	}
	if len(leafNodeTx.TxOut) == 0 {
		return nil, fmt.Errorf("tree node node transaction has zero outputs")
	}
	leafAmount := leafNodeTx.TxOut[0].Value

	userNodeSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.NodeTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate old leaf node tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userNodeSequence, spark.ZeroTimelock); err != nil {
		return nil, fmt.Errorf("failed to validate user provided node tx timelock: %w", err)
	}

	// ******************************************************************
	// NODE TX
	// ******************************************************************
	newNodeTx := wire.NewMsgTx(3)
	newNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leafNodeTx.TxHash(), Index: 0},
		Sequence:         userNodeSequence,
	})

	// Use same output value and script as original node tx
	nodePkScript, err := common.P2TRScriptFromPubKey(leaf.VerifyingPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to construct pkscript: %w", err)
	}
	newNodeTx.AddTxOut(wire.NewTxOut(leafAmount, nodePkScript))
	// Add ephemeral anchor output for CPFP
	newNodeTx.AddTxOut(common.EphemeralAnchorOutput())

	// ******************************************************************
	// REFUND TX
	// ******************************************************************
	userRefundSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.RefundTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate old leaf node tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userRefundSequence, spark.InitialTimeLock); err != nil {
		return nil, fmt.Errorf("failed to validate user provided refund tx timelock: %w", err)
	}
	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: newNodeTx.TxHash(), Index: 0},
		Sequence:         userRefundSequence,
	})

	refundPkScript, err := common.P2TRScriptFromPubKey(leaf.OwnerSigningPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to create refund script: %w", err)
	}
	refundTx.AddTxOut(&wire.TxOut{
		Value:    leafAmount,
		PkScript: refundPkScript,
	})
	// Add ephemeral anchor output for CPFP
	refundTx.AddTxOut(common.EphemeralAnchorOutput())

	// ******************************************************************
	// DIRECT NODE TX
	// ******************************************************************
	userDirectNodeSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.DirectNodeTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct split node tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userDirectNodeSequence, spark.DirectTimelockOffset); err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct node tx timelock: %w", err)
	}
	directNodeTx := wire.NewMsgTx(3)
	directNodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leafNodeTx.TxHash(), Index: 0},
		Sequence:         userDirectNodeSequence,
	})
	directNodeTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(leafAmount),
		PkScript: nodePkScript,
	})

	// ******************************************************************
	// DIRECT FROM CPFP REFUND TX
	// ******************************************************************
	userDirectFromCpfpRefundSequence, err := bitcointransaction.GetAndValidateUserSequence(signingJob.DirectFromCpfpRefundTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct from cpfp refund tx sequence: %w", err)
	}
	if err := bitcointransaction.ValidateSequenceTimelock(userDirectFromCpfpRefundSequence, spark.InitialTimeLock+spark.DirectTimelockOffset); err != nil {
		return nil, fmt.Errorf("failed to validate user provided direct from cpfp tx timelock: %w", err)
	}
	directFromCpfpRefundTx := wire.NewMsgTx(3)
	directFromCpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: newNodeTx.TxHash(), Index: 0},
		Sequence:         userDirectFromCpfpRefundSequence,
	})
	directFromCpfpRefundTx.AddTxOut(&wire.TxOut{
		Value:    common.MaybeApplyFee(leafAmount),
		PkScript: refundPkScript,
	})

	return &RenewZeroNodeTransactions{
		NodeTx:                 newNodeTx,
		RefundTx:               refundTx,
		DirectNodeTx:           directNodeTx,
		DirectFromCpfpRefundTx: directFromCpfpRefundTx,
	}, nil
}

// validateRenewNodeTimelocks validates the timelock requirements for a renew
// node timelock operation. Both the node transaction and the refund transaction
// must have a timelock at or below spark.RenewTimelockThreshold, and the refund
// transaction must still have a nonzero watchtower window.
func validateRenewNodeTimelocks(leaf *ent.TreeNode) error {
	// Check the leaf's node transaction sequence
	leafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return fmt.Errorf("failed to parse leaf node transaction: %w", err)
	}
	if len(leafNodeTx.TxIn) == 0 {
		return fmt.Errorf("found no tx inputs for leaf node tx %v", leafNodeTx)
	}
	nodeTimelock := leafNodeTx.TxIn[0].Sequence & 0xffff

	if nodeTimelock > spark.RenewTimelockThreshold {
		return errors.FailedPreconditionInvalidState(fmt.Errorf("leaf %s node transaction sequence must be less than or equal to %d, got %d", leaf.ID, spark.RenewTimelockThreshold, nodeTimelock))
	}

	leafRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("failed to parse leaf refund transaction: %w", err)
	}
	if len(leafRefundTx.TxIn) == 0 {
		return fmt.Errorf("found no tx inputs for leaf refund tx %v", leafRefundTx)
	}
	refundTimelock := leafRefundTx.TxIn[0].Sequence & 0xffff
	if refundTimelock > spark.RenewTimelockThreshold {
		return errors.FailedPreconditionInvalidState(fmt.Errorf("leaf %s refund transaction sequence must be less than or equal to %d, got %d", leaf.ID, spark.RenewTimelockThreshold, refundTimelock))
	}
	if err := validateRenewRefundTimelockMinimum(leaf, refundTimelock); err != nil {
		return err
	}

	return nil
}

// validateRenewRefundTimelock validates the timelock requirements for a renew
// refund timelock operation. Refund timelock must be at or below
// spark.RenewTimelockThreshold and must still have a nonzero watchtower window.
// The node timelock must not go below 100 following a decrement.
func validateRenewRefundTimelock(leaf *ent.TreeNode) error {
	// Check the leaf's refund transaction sequence
	leafRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("failed to parse leaf refund transaction: %w", err)
	}
	if len(leafRefundTx.TxIn) == 0 {
		return fmt.Errorf("found no tx inputs for leaf refund tx %v", leafRefundTx)
	}
	refundTimelock := leafRefundTx.TxIn[0].Sequence & 0xffff

	if refundTimelock > spark.RenewTimelockThreshold {
		return errors.FailedPreconditionInvalidState(fmt.Errorf("leaf %s refund transaction sequence must be less than or equal to %d, got %d", leaf.ID, spark.RenewTimelockThreshold, refundTimelock))
	}
	if err := validateRenewRefundTimelockMinimum(leaf, refundTimelock); err != nil {
		return err
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
		return errors.FailedPreconditionInvalidState(fmt.Errorf("next leaf %s node transaction sequence must be 100 or greater, got %d", leaf.ID, nextNodeTimelock))
	}

	return nil
}

// validateRenewNodeZeroTimelock validates the timelock requirements for a renew
// node zero timelock operation. The node transaction must have a timelock of 0
// and the refund transaction must have a timelock at or below
// spark.RenewTimelockThreshold with a nonzero watchtower window.
func validateRenewNodeZeroTimelock(leaf *ent.TreeNode) error {
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
		return errors.FailedPreconditionInvalidState(fmt.Errorf("leaf %s node transaction sequence must be 0 for zero timelock renewal, got %d", leaf.ID, nodeTimelock))
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

	if refundTimelock > spark.RenewTimelockThreshold {
		return errors.FailedPreconditionInvalidState(fmt.Errorf("leaf %s refund transaction sequence must be less than or equal to %d, got %d", leaf.ID, spark.RenewTimelockThreshold, refundTimelock))
	}
	if err := validateRenewRefundTimelockMinimum(leaf, refundTimelock); err != nil {
		return err
	}

	return nil
}

func validateRenewRefundTimelockMinimum(leaf *ent.TreeNode, refundTimelock uint32) error {
	if refundTimelock < spark.TimeLockInterval {
		return errors.FailedPreconditionInvalidState(fmt.Errorf("leaf %s refund transaction sequence must be at least %d for renewal, got %d", leaf.ID, spark.TimeLockInterval, refundTimelock))
	}
	return nil
}

// applyAndVerifySignature applies a signature to a transaction and verifies it
func applyAndVerifySignature(tx *wire.MsgTx, signature []byte, prevOutput *wire.TxOut, inputIndex int) (*wire.MsgTx, []byte, error) {
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
func validateUserTransactions(userRawTxs [][]byte, expectedTxs []*wire.MsgTx) error {
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

	sendGossipHandler := NewSendGossipHandler(h.config)
	_, err = sendGossipHandler.CreateCommitAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
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

	sendGossipHandler := NewSendGossipHandler(h.config)
	_, err = sendGossipHandler.CreateCommitAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
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
