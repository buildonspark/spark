//go:build lightspark

package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
)

// AggregateLeaves is the SSP-facing coordinator entrypoint for the
// AggregateLeaves 2PC flow. It runs the session-bound checks that cannot move
// into Prepare, short-circuits idempotent retries, and drives the flow
// through the consensus engine.
func (h *SspRequestHandler) AggregateLeaves(ctx context.Context, req *pbssp.AggregateLeavesRequest) (*pbssp.AggregateLeavesResponse, error) {
	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobEnableLeafAggregation, 0) == 0 {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("leaf aggregation is not enabled"))
	}
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}
	targetID, leafIDs, ownerKey, err := parseAggregateLeavesIdentifiers(req.GetTargetNodeId(), req.GetLeafIds(), req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, err
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, ownerKey); err != nil {
		return nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, ownerKey); err != nil {
		return nil, err
	}

	// This structural walk runs before any ownership check, so its errors
	// describe another user's topology (node existence, branching shape, the
	// node ids making up a subtree's leaf set) to whoever asks. That is
	// acceptable only because this RPC is InternalOnly (see rpcpolicy): the
	// caller is the SSP, which already knows the topology it is asking about.
	// Exposing leaf aggregation on spark.proto to wallets would make this an
	// information-disclosure oracle, so that change must first move the
	// ownership check ahead of the walk, or make every pre-ownership failure
	// return one generic error.
	subtree, err := loadAggregateLeavesSubtree(ctx, targetID, leafIDs, false)
	if err != nil {
		return nil, err
	}

	// Idempotency: a retry of an already-committed aggregation returns the
	// stored package. The unsigned request txid equals the signed txid
	// (witnesses are excluded from the txid), so matching both slots proves
	// the stored package is the one this request asked for. The ownership
	// check comes first — the short-circuit hands back signed transactions,
	// so it must never answer a caller who does not own the node.
	if subtree.target.Status == st.TreeNodeStatusConsolidated {
		if !subtree.target.OwnerIdentityPubkey.Equals(ownerKey) {
			return nil, sparkerrors.PermissionDeniedNoReadAccess(fmt.Errorf("target node %s is not owned by the caller", subtree.target.ID))
		}
		requestRefundTx, err := common.TxFromRawTxBytes(req.GetRefundTxSigningJob().GetRawTx())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid refund tx: %w", err))
		}
		requestWatchtowerTx, err := common.TxFromRawTxBytes(req.GetWatchtowerRefundTxSigningJob().GetRawTx())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid watchtower refund tx: %w", err))
		}
		if subtree.target.RawRefundTxid.Hash() != requestRefundTx.TxHash() ||
			subtree.target.DirectFromCpfpRefundTxid.Hash() != requestWatchtowerTx.TxHash() {
			return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("target %s is already consolidated with a different exit package", subtree.target.ID))
		}
		return buildAggregateLeavesResponse(ctx, subtree.target.ID, subtree.target.RawRefundTx, subtree.target.DirectFromCpfpRefundTx)
	}

	aggregatedUserKey, err := validateAggregateLeavesSubtree(ctx, subtree, ownerKey)
	if err != nil {
		return nil, err
	}
	if err := validateAggregateLeavesUserJobs(aggregatedUserKey, req.GetRefundTxSigningJob(), req.GetWatchtowerRefundTxSigningJob()); err != nil {
		return nil, err
	}
	txs, err := constructAggregateLeavesTransactions(ctx, subtree.target, aggregatedUserKey, req.GetRefundTxSigningJob(), req.GetWatchtowerRefundTxSigningJob())
	if err != nil {
		return nil, err
	}

	flow := &aggregateLeavesCoordinatorFlow{
		AggregateLeavesFlowHandler: NewAggregateLeavesFlowHandler(h.config),
		prepareOp: &pbinternal.AggregateLeavesPrepareRequest{
			TargetNodeId:                 req.GetTargetNodeId(),
			LeafIds:                      req.GetLeafIds(),
			OwnerIdentityPublicKey:       req.GetOwnerIdentityPublicKey(),
			RefundTxSigningJob:           req.GetRefundTxSigningJob(),
			WatchtowerRefundTxSigningJob: req.GetWatchtowerRefundTxSigningJob(),
		},
		subtree:           subtree,
		aggregatedUserKey: aggregatedUserKey,
		txs:               txs,
	}

	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	if _, err := engine.Execute(ctx, pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_AGGREGATE_LEAVES, &selection, flow); err != nil {
		return nil, fmt.Errorf("consensus aggregate leaves failed: %w", err)
	}
	if len(flow.signedRefundTx) == 0 || len(flow.signedWatchtowerTx) == 0 {
		return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("aggregate leaves consensus completed without a signed package"))
	}

	return buildAggregateLeavesResponse(ctx, subtree.target.ID, flow.signedRefundTx, flow.signedWatchtowerTx)
}

// buildAggregateLeavesResponse reloads the target (its row was updated inside
// the engine's request transaction) and marshals the response.
func buildAggregateLeavesResponse(ctx context.Context, targetID uuid.UUID, signedRefundTx, signedWatchtowerTx []byte) (*pbssp.AggregateLeavesResponse, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	target, err := db.TreeNode.Query().Where(enttreenode.ID(targetID)).Only(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to reload target node %s: %w", targetID, err))
	}
	targetProto, err := target.MarshalSparkProto(ctx)
	if err != nil {
		return nil, sparkerrors.InternalTypeConversionError(fmt.Errorf("failed to marshal target node %s: %w", targetID, err))
	}
	return &pbssp.AggregateLeavesResponse{
		TargetNode:         targetProto,
		RefundTx:           signedRefundTx,
		WatchtowerRefundTx: signedWatchtowerTx,
	}, nil
}
