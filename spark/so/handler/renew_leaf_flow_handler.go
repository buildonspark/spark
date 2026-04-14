package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// RenewLeafFlowHandler — participant side (Prepare / Commit / Rollback)
// ---------------------------------------------------------------------------

var _ consensus.FlowHandler = (*RenewLeafFlowHandler)(nil)

// RenewLeafFlowHandler implements consensus.FlowHandler for the renew leaf flow.
// Each SO independently validates transactions, locks the node, and produces
// FROST signature shares during Prepare.
type RenewLeafFlowHandler struct {
	config *so.Config
}

func NewRenewLeafFlowHandler(config *so.Config) *RenewLeafFlowHandler {
	return &RenewLeafFlowHandler{config: config}
}

// Prepare validates transactions, locks the node, and performs local FROST signing.
// Each SO independently:
//  1. Loads the leaf and validates status
//  2. Validates timelocks and transaction structure
//  3. Constructs signing jobs locally with deterministic job IDs
//  4. Locks the node (Available → RenewLocked)
//  5. Performs local FROST Round 2 signing
//  6. Returns FrostRound2Response with signature shares
func (h *RenewLeafFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	req, ok := op.(*pb.RenewLeafRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for renew leaf prepare", op)
	}

	leafID, err := uuid.Parse(req.LeafId)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("invalid leaf ID %q: %w", req.LeafId, err),
		)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 1. Load leaf with lock and eager-load parent
	leaf, err := db.TreeNode.Query().
		Where(treenode.ID(leafID)).
		ForUpdate().
		WithParent().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, sparkerrors.NotFoundMissingEntity(
				fmt.Errorf("tree node %s not found: %w", leafID, err),
			)
		}
		return nil, sparkerrors.InternalDatabaseReadError(
			fmt.Errorf("failed to query tree node %s: %w", leafID, err),
		)
	}

	if leaf.Status != st.TreeNodeStatusAvailable {
		return nil, sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("tree node %s status is %s, expected Available", leafID, leaf.Status),
		)
	}

	// 2. Validate transactions using the original public request.
	// Each SO independently reconstructs expected transactions from the
	// original request — no coordinator-provided data to verify against.
	var entries []sigEntry
	switch signingJob := req.SigningJobs.(type) {
	case *pb.RenewLeafRequest_RenewNodeTimelockSigningJob:
		_, _, entries, err = validateAndConstructNodeTimelock(ctx, leaf, signingJob.RenewNodeTimelockSigningJob)
	case *pb.RenewLeafRequest_RenewRefundTimelockSigningJob:
		_, _, entries, err = validateAndConstructRefundTimelock(ctx, leaf, signingJob.RenewRefundTimelockSigningJob)
	case *pb.RenewLeafRequest_RenewNodeZeroTimelockSigningJob:
		_, entries, err = validateAndConstructNodeZeroTimelock(leaf, signingJob.RenewNodeZeroTimelockSigningJob)
	default:
		err = fmt.Errorf("unexpected signing job type %T", req.SigningJobs)
	}
	if err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// 3. Construct signing jobs locally with deterministic job IDs.
	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}
	internalJobs, err := buildLocalSigningJobs(leafID, entries, signingKeyshare, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to build local signing jobs: %w", err)
	}

	// 4. Lock the node
	_, err = leaf.Update().SetStatus(st.TreeNodeStatusRenewLocked).Save(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseWriteError(
			fmt.Errorf("failed to lock tree node %s for renew: %w", leafID, err),
		)
	}

	// 5. Local FROST signing — only if this SO is in the signing set.
	// The signing threshold (t-of-n) means only a subset of SOs have commitments.
	// SOs outside the signing set skip signing and return nil (just validation + locking).
	if len(internalJobs) > 0 && internalJobs[0].Commitments[h.config.Identifier] != nil {
		frostReq := &pbinternal.FrostRound2Request{SigningJobs: internalJobs}
		frostHandler := signing_handler.NewFrostSigningHandler(h.config)
		frostResp, err := frostHandler.FrostRound2(ctx, frostReq)
		if err != nil {
			return nil, fmt.Errorf("local frost signing failed during prepare: %w", err)
		}
		return frostResp, nil
	}

	return nil, nil
}

// Commit applies the finalized node state after successful signing.
// Both node-timelock and zero-timelock variants use FinalizeRenewNodeTimelockRequest
// because both create a split node and update the leaf — the proto carries all
// needed fields for either case.
func (h *RenewLeafFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	internalHandler := NewInternalRenewLeafHandler(h.config)

	switch req := op.(type) {
	case *pbinternal.FinalizeRenewNodeTimelockRequest:
		return internalHandler.FinalizeRenewNodeTimelock(ctx, req)
	case *pbinternal.FinalizeRenewRefundTimelockRequest:
		return internalHandler.FinalizeRenewRefundTimelock(ctx, req)
	default:
		return fmt.Errorf("unexpected operation type %T for renew leaf commit", op)
	}
}

// Rollback resets the tree node status from RenewLocked back to Available.
// Idempotent — if the node is not RenewLocked, this is a no-op.
func (h *RenewLeafFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pb.RenewLeafRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for renew leaf rollback", op)
	}
	nodeIDStr := req.LeafId

	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("invalid node ID %q: %w", nodeIDStr, err),
		)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	node, err := db.TreeNode.Query().
		Where(treenode.ID(nodeID)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return sparkerrors.InternalDatabaseReadError(
			fmt.Errorf("failed to query tree node %s: %w", nodeID, err),
		)
	}

	if node.Status != st.TreeNodeStatusRenewLocked {
		return nil
	}

	_, err = node.Update().SetStatus(st.TreeNodeStatusAvailable).Save(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseWriteError(
			fmt.Errorf("failed to rollback tree node %s: %w", nodeID, err),
		)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Shared validation helpers
// ---------------------------------------------------------------------------

func getParentLeaf(ctx context.Context, leaf *ent.TreeNode) (*ent.TreeNode, error) {
	if leaf.Edges.Parent != nil {
		return leaf.Edges.Parent, nil
	}
	parentLeaf, err := leaf.QueryParent().Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("parent node does not exist for leaf %s", leaf.ID.String()))
		}
		return nil, fmt.Errorf("failed to query parent node: %w", err)
	}
	if parentLeaf == nil {
		return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("parent node does not exist for leaf %s", leaf.ID.String()))
	}
	return parentLeaf, nil
}

// renewLeafSigningJobNamespace is a fixed UUID v5 namespace for generating
// deterministic signing job IDs. Each SO uses the same namespace + leaf ID +
// entry index to produce identical job IDs independently.
var renewLeafSigningJobNamespace = uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890")

// deterministicJobID produces a deterministic UUID from a leaf ID and entry index.
// All SOs compute the same ID for a given (leafID, index) pair, enabling the
// coordinator to correlate FROST signature shares without distributing job IDs.
func deterministicJobID(leafID uuid.UUID, index int) uuid.UUID {
	return uuid.NewSHA1(renewLeafSigningJobNamespace, fmt.Appendf(nil, "%s-%d", leafID.String(), index))
}

// buildLocalSigningJobs constructs internal SigningJob protos locally from
// validated sigEntry data, using deterministic job IDs. This is used by both
// the coordinator and participant SOs during Prepare.
func buildLocalSigningJobs(leafID uuid.UUID, entries []sigEntry, signingKeyshare *ent.SigningKeyshare, leaf *ent.TreeNode) ([]*pbinternal.SigningJob, error) {
	result := make([]*pbinternal.SigningJob, len(entries))
	for i, e := range entries {
		jobHelper, err := helper.NewSigningJobWithDeterministicID(
			deterministicJobID(leafID, i),
			e.UserJob, signingKeyshare, leaf.VerifyingPubkey, e.Tx, e.PrevOut,
		)
		if err != nil {
			return nil, fmt.Errorf("signing job construction failed at index %d: %w", i, err)
		}
		result[i], err = marshalSigningJobHelper(jobHelper)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal signing job at index %d: %w", i, err)
		}
	}
	return result, nil
}

// marshalSigningJobHelper converts a SigningJobWithPregeneratedNonce into
// the internal SigningJob proto used for FrostRound2.
func marshalSigningJobHelper(job *helper.SigningJobWithPregeneratedNonce) (*pbinternal.SigningJob, error) {
	commitments := make(map[string]*pbcommon.SigningCommitment, len(job.Round1Packages))
	for id, c := range job.Round1Packages {
		cp, err := c.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal commitment for %s: %w", id, err)
		}
		commitments[id] = cp
	}
	var userCommitments *pbcommon.SigningCommitment
	if job.UserCommitment != nil {
		uc, err := job.UserCommitment.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal user commitment: %w", err)
		}
		userCommitments = uc
	}
	return &pbinternal.SigningJob{
		JobId:           job.JobID.String(),
		Message:         job.Message,
		KeyshareId:      job.SigningKeyshareID.String(),
		VerifyingKey:    job.VerifyingKey.Serialize(),
		Commitments:     commitments,
		UserCommitments: userCommitments,
	}, nil
}

// ---------------------------------------------------------------------------
// renewLeafCoordinatorFlow — coordinator side (CoordinatorFlow)
// ---------------------------------------------------------------------------

var _ consensus.CoordinatorFlow = (*renewLeafCoordinatorFlow)(nil)

type renewLeafCoordinatorFlow struct {
	config   *so.Config
	req      *pb.RenewLeafRequest
	leaf     *ent.TreeNode
	response *pb.RenewLeafResponse

	// Pre-computed by coordinator before engine.Execute
	signingJobHelpers []*helper.SigningJobWithPregeneratedNonce
	userSigningJobs   []*pb.UserSignedTxSigningJob
	signingKeyshare   *ent.SigningKeyshare
	parentLeaf        *ent.TreeNode

	// Transaction data (one set based on variant)
	renewNodeTxs   *RenewNodeTransactions
	renewRefundTxs *RenewRefundTransactions
	renewZeroTxs   *RenewZeroNodeTransactions
}

// Prepare delegates to RenewLeafFlowHandler.Prepare.
func (f *renewLeafCoordinatorFlow) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	return NewRenewLeafFlowHandler(f.config).Prepare(ctx, op)
}

// Commit delegates to RenewLeafFlowHandler.Commit.
func (f *renewLeafCoordinatorFlow) Commit(ctx context.Context, op proto.Message) error {
	return NewRenewLeafFlowHandler(f.config).Commit(ctx, op)
}

// Rollback delegates to RenewLeafFlowHandler.Rollback.
func (f *renewLeafCoordinatorFlow) Rollback(ctx context.Context, op proto.Message) error {
	return NewRenewLeafFlowHandler(f.config).Rollback(ctx, op)
}

// PrepareOp returns the prepare request for FlowHandler.Prepare.
func (f *renewLeafCoordinatorFlow) PrepareOp() proto.Message {
	return f.req
}

// BuildCommitPayload aggregates signature shares from all SOs, applies/verifies
// signatures, does coordinator DB writes, and returns the commit message.
func (f *renewLeafCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	// 1. Collect signature shares from all SOs' prepare results
	allShares, participantIDs, err := collectSignatureShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	// 2. Load key package for public shares needed in aggregation
	keyPackage, err := ent.GetKeyPackage(ctx, f.config, f.signingKeyshare.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load key package: %w", err)
	}

	// 3. Filter public shares to participating operators
	publicKeys := make(map[string][]byte, len(participantIDs))
	for _, id := range participantIDs {
		pk, ok := keyPackage.PublicShares[id]
		if !ok {
			return nil, fmt.Errorf("missing public share for operator %s", id)
		}
		publicKeys[id] = pk
	}

	// 4. Aggregate each signing job's signature
	signatures := make([][]byte, len(f.signingJobHelpers))
	for i, jobHelper := range f.signingJobHelpers {
		jobID := jobHelper.JobID.String()
		shares, ok := allShares[jobID]
		if !ok {
			return nil, fmt.Errorf("missing signature shares for job %s", jobID)
		}

		sig, err := aggregateSignature(ctx, f.config, jobHelper, shares, publicKeys, f.userSigningJobs[i], f.leaf)
		if err != nil {
			return nil, fmt.Errorf("failed to aggregate signature for job %d: %w", i, err)
		}
		signatures[i] = sig
	}

	// 5. Dispatch to variant-specific finalize
	switch f.req.SigningJobs.(type) {
	case *pb.RenewLeafRequest_RenewNodeTimelockSigningJob:
		return f.finalizeNodeTimelock(ctx, signatures)
	case *pb.RenewLeafRequest_RenewRefundTimelockSigningJob:
		return f.finalizeRefundTimelock(ctx, signatures)
	case *pb.RenewLeafRequest_RenewNodeZeroTimelockSigningJob:
		return f.finalizeNodeZeroTimelock(ctx, signatures)
	default:
		return nil, fmt.Errorf("unexpected signing job type %T", f.req.SigningJobs)
	}
}

// RollbackPayload returns the rollback gossip payload carrying the leaf ID.
func (f *renewLeafCoordinatorFlow) RollbackPayload() proto.Message {
	return &pb.RenewLeafRequest{LeafId: f.leaf.ID.String()}
}

// ---------------------------------------------------------------------------
// BuildCommitPayload helpers
// ---------------------------------------------------------------------------

// collectSignatureShares transposes prepare results from per-operator to per-job.
func collectSignatureShares(results map[string]*anypb.Any) (map[string]map[string][]byte, []string, error) {
	allShares := make(map[string]map[string][]byte)
	participantIDs := make([]string, 0, len(results))

	for opID, anyResult := range results {
		if anyResult == nil {
			// Non-signing participant (outside threshold set) — skip
			continue
		}
		participantIDs = append(participantIDs, opID)
		resp := &pbinternal.FrostRound2Response{}
		if err := anyResult.UnmarshalTo(resp); err != nil {
			return nil, nil, fmt.Errorf("failed to unmarshal prepare result from %s: %w", opID, err)
		}
		for jobID, sigResult := range resp.Results {
			if allShares[jobID] == nil {
				allShares[jobID] = make(map[string][]byte)
			}
			allShares[jobID][opID] = sigResult.SignatureShare
		}
	}
	return allShares, participantIDs, nil
}

func aggregateSignature(
	ctx context.Context,
	config *so.Config,
	jobHelper *helper.SigningJobWithPregeneratedNonce,
	signatureShares map[string][]byte,
	publicKeys map[string][]byte,
	userSigningJob *pb.UserSignedTxSigningJob,
	leaf *ent.TreeNode,
) ([]byte, error) {
	conn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer conn.Close()
	frostClient := pbfrost.NewFrostServiceClient(conn)

	resp, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            jobHelper.Message,
		SignatureShares:    signatureShares,
		PublicShares:       publicKeys,
		VerifyingKey:       leaf.VerifyingPubkey.Serialize(),
		Commitments:        userSigningJob.SigningCommitments.SigningCommitments,
		UserCommitments:    userSigningJob.SigningNonceCommitment,
		UserPublicKey:      leaf.OwnerSigningPubkey.Serialize(),
		UserSignatureShare: userSigningJob.UserSignature,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to aggregate frost signature: %w", err)
	}
	return resp.Signature, nil
}

// ---------------------------------------------------------------------------
// Variant-specific finalize methods (coordinator DB writes + response)
// ---------------------------------------------------------------------------

func (f *renewLeafCoordinatorFlow) finalizeNodeTimelock(ctx context.Context, signatures [][]byte) (proto.Message, error) {
	result, err := finalizeRenewNodeTimelockDB(ctx, f.leaf, f.parentLeaf, f.renewNodeTxs, f.signingKeyshare, signatures)
	if err != nil {
		return nil, err
	}
	f.response = result.response
	f.leaf = result.leaf

	splitNodeInternal, err := result.splitNode.MarshalInternalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal split node internal: %w", err)
	}
	extendedNodeInternal, err := result.leaf.MarshalInternalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal extended node internal: %w", err)
	}
	return &pbinternal.FinalizeRenewNodeTimelockRequest{
		SplitNode: splitNodeInternal,
		Node:      extendedNodeInternal,
	}, nil
}

func (f *renewLeafCoordinatorFlow) finalizeRefundTimelock(ctx context.Context, signatures [][]byte) (proto.Message, error) {
	result, err := finalizeRenewRefundTimelockDB(ctx, f.leaf, f.parentLeaf, f.renewRefundTxs, signatures)
	if err != nil {
		return nil, err
	}
	f.response = result.response
	f.leaf = result.leaf

	nodeInternal, err := result.leaf.MarshalInternalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal node internal: %w", err)
	}
	return &pbinternal.FinalizeRenewRefundTimelockRequest{
		Node: nodeInternal,
	}, nil
}

func (f *renewLeafCoordinatorFlow) finalizeNodeZeroTimelock(ctx context.Context, signatures [][]byte) (proto.Message, error) {
	result, err := finalizeRenewNodeZeroTimelockDB(ctx, f.leaf, f.renewZeroTxs, f.signingKeyshare, signatures)
	if err != nil {
		return nil, err
	}
	f.response = result.response
	f.leaf = result.leaf

	splitNodeInternal, err := result.splitNode.MarshalInternalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal split node internal: %w", err)
	}
	extendedNodeInternal, err := result.leaf.MarshalInternalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal leaf internal: %w", err)
	}
	return &pbinternal.FinalizeRenewNodeTimelockRequest{
		SplitNode: splitNodeInternal,
		Node:      extendedNodeInternal,
	}, nil
}

// ---------------------------------------------------------------------------
// Coordinator flow construction (called from RenewLeaf before engine.Execute)
// ---------------------------------------------------------------------------

// buildCoordinatorFlow extracts validation, transaction construction, and
// signing job creation from the old renew functions. This runs on the
// coordinator before engine.Execute to pre-compute data for both the prepare
// request (sent to all SOs) and BuildCommitPayload (aggregation + finalize).
func buildCoordinatorFlow(ctx context.Context, config *so.Config, req *pb.RenewLeafRequest, leaf *ent.TreeNode) (*renewLeafCoordinatorFlow, error) {
	flow := &renewLeafCoordinatorFlow{
		config: config,
		req:    req,
		leaf:   leaf,
	}

	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}
	flow.signingKeyshare = signingKeyshare

	switch job := req.SigningJobs.(type) {
	case *pb.RenewLeafRequest_RenewNodeTimelockSigningJob:
		err = prepareNodeTimelockFlow(ctx, flow, leaf, signingKeyshare, job.RenewNodeTimelockSigningJob)
	case *pb.RenewLeafRequest_RenewRefundTimelockSigningJob:
		err = prepareRefundTimelockFlow(ctx, flow, leaf, signingKeyshare, job.RenewRefundTimelockSigningJob)
	case *pb.RenewLeafRequest_RenewNodeZeroTimelockSigningJob:
		err = prepareNodeZeroTimelockFlow(ctx, flow, leaf, signingKeyshare, job.RenewNodeZeroTimelockSigningJob)
	default:
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request must specify a signing job"))
	}
	if err != nil {
		return nil, err
	}

	return flow, nil
}

func prepareNodeTimelockFlow(ctx context.Context, flow *renewLeafCoordinatorFlow, leaf *ent.TreeNode, signingKeyshare *ent.SigningKeyshare, signingJob *pb.RenewNodeTimelockSigningJob) error {
	parentLeaf, renewTxs, entries, err := validateAndConstructNodeTimelock(ctx, leaf, signingJob)
	if err != nil {
		return err
	}
	flow.parentLeaf = parentLeaf
	flow.renewNodeTxs = renewTxs

	jobs := make([]*helper.SigningJobWithPregeneratedNonce, 0, len(entries))
	userJobs := make([]*pb.UserSignedTxSigningJob, 0, len(entries))
	for i, e := range entries {
		j, err := helper.NewSigningJobWithDeterministicID(
			deterministicJobID(leaf.ID, i),
			e.UserJob, signingKeyshare, leaf.VerifyingPubkey, e.Tx, e.PrevOut,
		)
		if err != nil {
			return err
		}
		jobs = append(jobs, j)
		userJobs = append(userJobs, e.UserJob)
	}

	flow.signingJobHelpers = jobs
	flow.userSigningJobs = userJobs
	return nil
}

func prepareRefundTimelockFlow(ctx context.Context, flow *renewLeafCoordinatorFlow, leaf *ent.TreeNode, signingKeyshare *ent.SigningKeyshare, signingJob *pb.RenewRefundTimelockSigningJob) error {
	parentLeaf, refundTxs, entries, err := validateAndConstructRefundTimelock(ctx, leaf, signingJob)
	if err != nil {
		return err
	}
	flow.parentLeaf = parentLeaf
	flow.renewRefundTxs = refundTxs

	jobs := make([]*helper.SigningJobWithPregeneratedNonce, 0, len(entries))
	userJobs := make([]*pb.UserSignedTxSigningJob, 0, len(entries))
	for i, e := range entries {
		j, err := helper.NewSigningJobWithDeterministicID(
			deterministicJobID(leaf.ID, i),
			e.UserJob, signingKeyshare, leaf.VerifyingPubkey, e.Tx, e.PrevOut,
		)
		if err != nil {
			return err
		}
		jobs = append(jobs, j)
		userJobs = append(userJobs, e.UserJob)
	}

	flow.signingJobHelpers = jobs
	flow.userSigningJobs = userJobs
	return nil
}

func prepareNodeZeroTimelockFlow(ctx context.Context, flow *renewLeafCoordinatorFlow, leaf *ent.TreeNode, signingKeyshare *ent.SigningKeyshare, signingJob *pb.RenewNodeZeroTimelockSigningJob) error {
	zeroTxs, entries, err := validateAndConstructNodeZeroTimelock(leaf, signingJob)
	if err != nil {
		return err
	}
	flow.renewZeroTxs = zeroTxs

	jobs := make([]*helper.SigningJobWithPregeneratedNonce, 0, len(entries))
	userJobs := make([]*pb.UserSignedTxSigningJob, 0, len(entries))
	for i, e := range entries {
		j, err := helper.NewSigningJobWithDeterministicID(
			deterministicJobID(leaf.ID, i),
			e.UserJob, signingKeyshare, leaf.VerifyingPubkey, e.Tx, e.PrevOut,
		)
		if err != nil {
			return err
		}
		jobs = append(jobs, j)
		userJobs = append(userJobs, e.UserJob)
	}

	flow.signingJobHelpers = jobs
	flow.userSigningJobs = userJobs
	return nil
}
