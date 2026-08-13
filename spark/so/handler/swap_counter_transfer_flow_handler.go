package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/pendingsendtransfer"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// SwapCounterTransferFlowHandler — participant side (Prepare / Commit / Rollback)
// ---------------------------------------------------------------------------

// SwapCounterTransferFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_INITIATE_COUNTER_TRANSFER — the swap v3 counter
// leg (SSP → user, settling the swap). Embeds the send-transfer flow handler
// for the shared transfer/cancel machinery. The counter leg is where the swap
// becomes atomic:
//
//   - Prepare creates the COUNTER_SWAP_V3 transfer (createTransfer validates
//     it against the primary: status, expiry safety buffer, network, amount,
//     reversed parties) and flips the primary to APPLYING_SENDER_KEY_TWEAK,
//     fencing it from cancellation and the expiry sweep on EVERY SO for the
//     in-flight window. The legacy flow only flipped the counter coordinator
//     (its flip rode the request tx); under 2PC each participant's Prepare
//     commits independently, so the fence must be explicit and per-SO — and
//     Rollback must revert it.
//   - Commit applies the aggregated adaptor signatures and then settles the
//     sender key tweaks of BOTH legs in the same DB transaction
//     (CommitSwapKeyTweaks) — replacing the legacy SettleSwapKeyTweak gossip.
type SwapCounterTransferFlowHandler struct {
	*SendTransferFlowHandler
}

var (
	_ consensus.FlowHandler             = (*SwapCounterTransferFlowHandler)(nil)
	_ consensus.PrepareBoundFlowHandler = (*SwapCounterTransferFlowHandler)(nil)
)

func NewSwapCounterTransferFlowHandler(config *so.Config) *SwapCounterTransferFlowHandler {
	// partnerType is unused (swap v3 records no partner attribution);
	// requireDirectRefunds=false matches the legacy ValidateTransferPackage
	// call for swap types.
	return &SwapCounterTransferFlowHandler{
		SendTransferFlowHandler: NewSendTransferFlowHandlerForType(config, st.TransferTypeCounterSwapV3, st.TransferPartnerTypeTransfer, false),
	}
}

// Prepare runs on every SO: validate + persist the counter transfer, fence
// the primary, and produce local FROST round-2 adaptor signature shares.
func (h *SwapCounterTransferFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	req, ok := op.(*pbinternal.InitiateCounterTransferPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for swap counter transfer prepare", op)
	}
	orig := req.GetOriginalRequest()
	if orig == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("original_request is required"))
	}
	primaryTransferID, err := uuid.Parse(orig.GetPrimaryTransferId())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid primary_transfer_id: %w", err))
	}
	parsed, err := parseSwapTransferRequest(orig.GetTransfer(), orig.GetAdaptorPublicKeys())
	if err != nil {
		return nil, err
	}

	// createTransfer's CounterSwapV3 branch loads the primary ForUpdate and
	// validates it, so the fence flip below can't race a concurrent counter
	// for the same primary: the second one fails the status check.
	counterTransfer, leafMap, err := h.prepareSwapTransfer(ctx, parsed, st.TransferTypeCounterSwapV3, primaryTransferID)
	if err != nil {
		return nil, err
	}
	if err := requirePrimaryRefundSignaturesApplied(ctx, counterTransfer); err != nil {
		return nil, err
	}
	if err := updateSwapPrimaryTransferToStatus(ctx, counterTransfer, st.TransferStatusApplyingSenderKeyTweak); err != nil {
		return nil, fmt.Errorf("unable to fence primary transfer for counter transfer %s: %w", parsed.transferID, err)
	}

	return signSwapRefundsRound2(ctx, h.config, parsed.transferID, parsed.pkg, leafMap, parsed.adaptorPubKey)
}

// ValidateDecisionAgainstPrepare implements consensus.PrepareBoundFlowHandler:
// a gossip-delivered commit/rollback must name the same counter transfer the
// persisted prepare op did, and a commit must carry the same adaptor point the
// request pinned. The swap commit/rollback paths resolve rows purely by the
// payload's transfer id, so without this binding a misbehaving coordinator
// could drive a legitimate flow to the prepared state and then aim its
// decision at an unrelated in-flight transfer.
func (h *SwapCounterTransferFlowHandler) ValidateDecisionAgainstPrepare(prepareOp proto.Message, decisionOp proto.Message) error {
	prepare, ok := prepareOp.(*pbinternal.InitiateCounterTransferPrepareRequest)
	if !ok {
		return fmt.Errorf("unexpected prepare op type %T for swap counter transfer", prepareOp)
	}
	preparedTransferID := prepare.GetOriginalRequest().GetTransfer().GetTransferId()
	switch d := decisionOp.(type) {
	case *pbinternal.InitiateCounterTransferCommitRequest:
		if !sameTransferID(d.GetTransferId(), preparedTransferID) {
			return fmt.Errorf("commit transfer id %s does not match the prepared transfer id %s", d.GetTransferId(), preparedTransferID)
		}
		if !samePublicKey(d.GetAdaptorPublicKey(), prepare.GetOriginalRequest().GetAdaptorPublicKeys().GetAdaptorPublicKey()) {
			return fmt.Errorf("commit adaptor public key does not match the prepared adaptor public key for transfer %s", preparedTransferID)
		}
	case *pbinternal.InitiateCounterTransferRollbackRequest:
		if !sameTransferID(d.GetTransferId(), preparedTransferID) {
			return fmt.Errorf("rollback transfer id %s does not match the prepared transfer id %s", d.GetTransferId(), preparedTransferID)
		}
	case *pbinternal.InitiateCounterTransferPrepareRequest:
		// The reconciler's presumed-abort path echoes the prepare op itself.
		if !sameTransferID(d.GetOriginalRequest().GetTransfer().GetTransferId(), preparedTransferID) {
			return fmt.Errorf("presumed-abort rollback transfer id %s does not match the prepared transfer id %s", d.GetOriginalRequest().GetTransfer().GetTransferId(), preparedTransferID)
		}
	default:
		return fmt.Errorf("unexpected decision op type %T for swap counter transfer", decisionOp)
	}
	return nil
}

// Commit runs on every participant (the coordinator's equivalent work lives
// in BuildCommitPayload). It applies the aggregated adaptor signatures to the
// counter transfer's leaves, then settles the sender key tweaks of both the
// primary and the counter transfer atomically.
func (h *SwapCounterTransferFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.InitiateCounterTransferCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for swap counter transfer commit", op)
	}
	return h.applySwapCounterTransferCommit(ctx, req)
}

// applySwapCounterTransferCommit applies signatures + settles both legs' key
// tweaks on a single SO. Shared by participant Commit and coordinator
// BuildCommitPayload.
//
// Idempotent against replayed commit gossip: re-applying the identical
// aggregated signature replaces the witness with the same bytes, and
// CommitSwapKeyTweaks short-circuits when both legs already reached
// SENDER_KEY_TWEAKED.
func (h *SwapCounterTransferFlowHandler) applySwapCounterTransferCommit(ctx context.Context, req *pbinternal.InitiateCounterTransferCommitRequest) error {
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}
	adaptorPubKey, err := keys.ParsePublicKey(req.GetAdaptorPublicKey())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse adaptor public key: %w", err))
	}

	transferEnt, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to load counter transfer %s for commit: %w", transferID, err)
	}
	// Defense in depth alongside ValidateDecisionAgainstPrepare: this commit
	// must never mutate a transfer of another type.
	if transferEnt.Type != st.TransferTypeCounterSwapV3 {
		return fmt.Errorf("transfer %s has type %s, expected %s for swap counter commit", transferID, transferEnt.Type, st.TransferTypeCounterSwapV3)
	}
	switch transferEnt.Status {
	case st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusSenderKeyTweaked:
		// Apply. Consensus-created counters sit in SENDER_KEY_TWEAK_PENDING on
		// every SO (Prepare uses the participant role everywhere, and the
		// flow-execution fence guarantees this commit only ever targets a
		// consensus-created row); SENDER_KEY_TWEAKED is a replayed delivery —
		// the signature application below is idempotent and
		// CommitSwapKeyTweaks no-ops.
	default:
		// A consensus-created counter can't legitimately be RETURNED or in a
		// receiver-side state before its commit applied: the CancelTransfer
		// RPC rejects its pre-commit status, the expiry sweeps only cancel
		// primaries, its own rollback is fenced off once the flow decided
		// commit, and receivers can't advance a leg whose tweaks were never
		// settled. Surface it instead of skipping.
		return fmt.Errorf("counter transfer %s in unexpected status %s for commit", transferID, transferEnt.Status)
	}

	cpfpSigs, _, _ := splitLeafSignatures(req.GetLeafSignatures())
	if err := h.UpdateTransferLeavesSignaturesForRefundTxOnly(ctx, transferEnt, cpfpSigs, adaptorPubKey); err != nil {
		return fmt.Errorf("unable to apply adaptor refund signatures for counter transfer %s: %w", transferID, err)
	}

	if err := h.CommitSwapKeyTweaks(ctx, transferID); err != nil {
		return fmt.Errorf("unable to settle swap key tweaks for counter transfer %s: %w", transferID, err)
	}
	return nil
}

// Rollback runs on every participant (and on the coordinator if Prepare or
// BuildCommitPayload fails). It reverts the primary transfer's
// APPLYING_SENDER_KEY_TWEAK fence back to its pre-flip pending status, then
// cancels the counter transfer rows Prepare wrote — atomically in one tx, so
// a RETURNED counter always implies an already-reverted fence.
//
// Accepts both InitiateCounterTransferRollbackRequest (the normal rollback
// payload) and InitiateCounterTransferPrepareRequest (the prepare op echoed
// back by the reconciler when the coordinator's row was lost).
func (h *SwapCounterTransferFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var transferIDStr string
	switch r := op.(type) {
	case *pbinternal.InitiateCounterTransferRollbackRequest:
		transferIDStr = r.GetTransferId()
	case *pbinternal.InitiateCounterTransferPrepareRequest:
		transferIDStr = r.GetOriginalRequest().GetTransfer().GetTransferId()
	default:
		return fmt.Errorf("unexpected operation type %T for swap counter transfer rollback", op)
	}
	if transferIDStr == "" {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_id is required for rollback"))
	}
	transferID, err := uuid.Parse(transferIDStr)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}

	logger := logging.GetLoggerFromContext(ctx)
	counterTransfer, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		if ent.IsNotFound(err) {
			// Prepare never created rows on this SO (and therefore never
			// flipped the primary here) — nothing to undo.
			return nil
		}
		return fmt.Errorf("unable to load counter transfer %s for rollback: %w", transferID, err)
	}
	// Defense in depth alongside ValidateDecisionAgainstPrepare: this rollback
	// must never cancel a transfer of another type (or revert a fence through
	// a foreign transfer's primary_swap_transfer edge).
	if counterTransfer.Type != st.TransferTypeCounterSwapV3 {
		return fmt.Errorf("transfer %s has type %s, expected %s for swap counter rollback", transferID, counterTransfer.Type, st.TransferTypeCounterSwapV3)
	}

	switch counterTransfer.Status {
	case st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusSenderKeyTweakPending:
		// Pre-commit: revert the fence, then cancel below.
	case st.TransferStatusReturned:
		// Redelivered rollback. The fence revert and the cancel run in one tx,
		// so RETURNED implies the fence was already reverted — and the primary
		// may since have been fenced again by a NEWER counter transfer, which
		// this stale rollback must not touch.
		logger.Sugar().Infof("swap counter 2pc rollback: transfer %s already RETURNED", transferID)
		return nil
	default:
		// SENDER_KEY_TWEAKED and beyond: the flow committed; a late rollback is
		// a no-op. APPLYING_SENDER_KEY_TWEAK also lands here — unreachable for
		// a counter (only primaries get fenced) but deliberately a logged no-op
		// rather than an error: a rollback error loops gossip redelivery on a
		// message that can never become valid.
		logger.Sugar().Infof("swap counter 2pc rollback: transfer %s already past rollbackable status %s", transferID, counterTransfer.Status)
		return nil
	}

	if err := h.revertSwapPrimaryFence(ctx, counterTransfer); err != nil {
		return fmt.Errorf("unable to revert primary fence for counter transfer %s: %w", transferID, err)
	}
	if err := h.executeCancelTransfer(ctx, counterTransfer); err != nil {
		return fmt.Errorf("unable to cancel counter transfer %s during rollback: %w", transferID, err)
	}
	logger.Sugar().Infof("swap counter 2pc rollback: transfer %s marked RETURNED", transferID)
	return nil
}

// requirePrimaryRefundSignaturesApplied rejects the counter Prepare when this
// SO has not yet applied the primary leg's adaptor refund signatures. Under
// the legacy flow, a primary's rows only exist on an SO with their signatures
// already applied; under the consensus flow the signatures land via the
// primary's commit gossip, which a lagging SO may not have processed yet.
// Settling the swap here anyway would make the primary claimable on this SO
// with unsigned refund txs — so fail the Prepare instead (the SSP retries;
// the primary commit is durably retried gossip, so the gap closes).
//
// A witness on the stored refund tx unambiguously means the SE applied its
// signature: swap-v3 refunds are rejected at ingest unless submitted unsigned
// (parseSwapTransferRequest), so a user cannot forge a witness to slip past
// this gate.
func requirePrimaryRefundSignaturesApplied(ctx context.Context, counterTransfer *ent.Transfer) error {
	primaryTransfer, err := counterTransfer.QueryPrimarySwapTransfer().Only(ctx)
	if err != nil {
		return fmt.Errorf("unable to load primary transfer: %w", err)
	}
	return primaryRefundSignaturesApplied(ctx, primaryTransfer)
}

// primaryRefundSignaturesApplied returns a FailedPrecondition error when the
// swap primary's per-leaf refund-tx witnesses have not yet been written on this
// operator — i.e. the primary's refund signatures (applied inline by the legacy
// primary path, or by the consensus primary's commit gossip) have not landed
// here yet. It guards a counter leg from settling the swap before the primary's
// refund signatures exist locally: because commit gossip is asynchronous, a
// counter (legacy or consensus) could otherwise finalize a consensus primary on
// a lagging SO that never wrote the refund-tx witness.
func primaryRefundSignaturesApplied(ctx context.Context, primaryTransfer *ent.Transfer) error {
	leaves, err := primaryTransfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to load primary transfer leaves: %w", err)
	}
	for _, leaf := range leaves {
		refundTx, err := common.TxFromRawTxBytes(leaf.IntermediateRefundTx)
		if err != nil {
			return fmt.Errorf("unable to parse primary refund tx for transfer leaf %s: %w", leaf.ID, err)
		}
		if len(refundTx.TxIn) == 0 || len(refundTx.TxIn[0].Witness) == 0 {
			return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
				"primary transfer %s refund signatures are not yet applied on this operator; retry after the primary commit lands", primaryTransfer.ID))
		}
	}
	return nil
}

// revertSwapPrimaryFence restores the primary transfer from the
// APPLYING_SENDER_KEY_TWEAK fence Prepare applied back to its pre-flip
// pending status. The pre-flip status was SENDER_INITIATED_COORDINATOR only
// on the SO that coordinated the primary through the LEGACY path — the legacy
// coordinator is the only writer of a PendingSendTransfer row for the
// transfer (the consensus primary flow creates none and lands every SO on
// SENDER_KEY_TWEAK_PENDING) — so that row's presence recovers the bit the
// flip destroyed.
func (h *SwapCounterTransferFlowHandler) revertSwapPrimaryFence(ctx context.Context, counterTransfer *ent.Transfer) error {
	primaryTransfer, err := counterTransfer.QueryPrimarySwapTransfer().ForUpdate().Only(ctx)
	if err != nil {
		return fmt.Errorf("unable to load primary transfer: %w", err)
	}
	// createTransfer's counter branch only ever sets the edge to a
	// PRIMARY_SWAP_V3 transfer; assert it anyway so every mutator in this flow
	// carries a uniform type check.
	if primaryTransfer.Type != st.TransferTypePrimarySwapV3 {
		return fmt.Errorf("transfer %s has type %s, expected %s for swap primary fence revert", primaryTransfer.ID, primaryTransfer.Type, st.TransferTypePrimarySwapV3)
	}
	if primaryTransfer.Status != st.TransferStatusApplyingSenderKeyTweak {
		// Never fenced on this SO, or something else owns the primary's state
		// now — either way there is nothing to revert.
		return nil
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get database transaction: %w", err)
	}
	restored := st.TransferStatusSenderKeyTweakPending
	// Filter on the STARTED status, not mere existence: the legacy coordinator's
	// row is STARTED while the primary is mid-swap (the counter block marks it
	// FINISHED only on the settle path, never for a primary), whereas a losing
	// concurrent legacy attempt that reused this transfer id — or any prior
	// failed attempt — leaves its row FINISHED. Keying on STARTED avoids
	// mis-restoring a consensus-created primary (which owns no STARTED row) to
	// SENDER_INITIATED_COORDINATOR off a stale FINISHED row.
	primaryCoordinatedHere, err := db.PendingSendTransfer.Query().
		Where(
			pendingsendtransfer.TransferID(primaryTransfer.ID),
			pendingsendtransfer.StatusEQ(st.PendingSendTransferStatusPending),
		).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("unable to query pending send transfer for primary %s: %w", primaryTransfer.ID, err)
	}
	if primaryCoordinatedHere {
		restored = st.TransferStatusSenderInitiatedCoordinator
	}
	if _, err := primaryTransfer.Update().SetStatus(restored).Save(ctx); err != nil {
		return fmt.Errorf("unable to restore primary transfer %s to %s: %w", primaryTransfer.ID, restored, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// swapCounterTransferCoordinatorFlow — coordinator side
// ---------------------------------------------------------------------------

// swapCounterTransferCoordinatorFlow drives the coordinator side of the swap
// v3 counter leg through the 2PC engine. It lives untagged (the internal
// mirror request and pb.StartTransferResponse are OSS types); the
// lightspark-tagged SSP handler converts the response to the SSP proto shape.
type swapCounterTransferCoordinatorFlow struct {
	*SwapCounterTransferFlowHandler

	req               *pbinternal.InitiateCounterTransferRequest
	parsed            parsedSwapTransferRequest
	signingJobsByLeaf map[string]*sendTransferLeafSigningJobs

	senderKeyTweakProofs map[string]*pb.SecretProof

	// response is populated during BuildCommitPayload so the SSP-facing
	// handler can return it after engine.Execute completes.
	response *pb.StartTransferResponse
}

var _ consensus.CoordinatorFlow = (*swapCounterTransferCoordinatorFlow)(nil)

func (f *swapCounterTransferCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.InitiateCounterTransferPrepareRequest{
		OriginalRequest:      f.req,
		SenderKeyTweakProofs: f.senderKeyTweakProofs,
	}
}

func (f *swapCounterTransferCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	leafSignatures, cpfpSigningResultMap, leafMap, err := aggregateSwapLeafSignatures(ctx, f.config, f.signingJobsByLeaf, results)
	if err != nil {
		return nil, err
	}

	commitReq := &pbinternal.InitiateCounterTransferCommitRequest{
		TransferId:       f.parsed.transferID.String(),
		LeafSignatures:   leafSignatures,
		AdaptorPublicKey: f.parsed.adaptorPubKey.Serialize(),
	}
	// Apply on the coordinator's DB now — signatures plus the atomic two-leg
	// key tweak settlement — so the request tx the engine commits next carries
	// the final swap state. Participants do the same work via
	// FlowHandler.Commit after receiving the commit gossip.
	if err := f.applySwapCounterTransferCommit(ctx, commitReq); err != nil {
		return nil, fmt.Errorf("failed to apply commit on coordinator: %w", err)
	}

	transferEnt, err := f.loadTransferForUpdate(ctx, f.parsed.transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to reload counter transfer %s after commit: %w", f.parsed.transferID, err)
	}
	transferProto, err := transferEnt.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal counter transfer %s for response: %w", f.parsed.transferID, err)
	}
	signingResultProtos, err := buildSigningResultProtos(leafMap, cpfpSigningResultMap, map[string]*helper.SigningResult{}, map[string]*helper.SigningResult{})
	if err != nil {
		return nil, fmt.Errorf("unable to build signing result protos: %w", err)
	}
	f.response = &pb.StartTransferResponse{Transfer: transferProto, SigningResults: signingResultProtos}

	return commitReq, nil
}

func (f *swapCounterTransferCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.InitiateCounterTransferRollbackRequest{TransferId: f.parsed.transferID.String()}
}

// initiateCounterTransferConsensus runs the swap v3 counter leg through the
// 2PC consensus engine instead of the legacy StartCounterTransferInternal
// fanout + SettleSwapKeyTweak gossip. Gated by
// KnobUseConsensusInitiateCounterTransfer at the SSP-facing
// InitiateCounterTransfer entry point, which performs the SSP-proto nil
// checks and the kill-switch check on the primary sender (the user) before
// building the internal mirror request.
func initiateCounterTransferConsensus(ctx context.Context, config *so.Config, req *pbinternal.InitiateCounterTransferRequest) (*pb.StartTransferResponse, error) {
	parsed, err := parseSwapTransferRequest(req.GetTransfer(), req.GetAdaptorPublicKeys())
	if err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(req.GetPrimaryTransferId()); err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid primary_transfer_id: %w", err))
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, config, parsed.senderIDPK); err != nil {
		return nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, parsed.senderIDPK); err != nil {
		return nil, err
	}

	signingJobsByLeaf, err := buildSwapCoordinatorSigningJobs(ctx, parsed)
	if err != nil {
		return nil, err
	}
	handler := NewSwapCounterTransferFlowHandler(config)
	senderKeyTweakProofs, err := handler.coordinatorSenderKeyTweakProofs(ctx, parsed.transferReq.GetTransferPackage())
	if err != nil {
		return nil, fmt.Errorf("unable to read sender key tweak proofs from the coordinator's slice: %w", err)
	}
	flow := &swapCounterTransferCoordinatorFlow{
		SwapCounterTransferFlowHandler: handler,
		req:                            req,
		parsed:                         parsed,
		signingJobsByLeaf:              signingJobsByLeaf,
		senderKeyTweakProofs:           senderKeyTweakProofs,
	}
	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	if _, err := engine.Execute(ctx,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_INITIATE_COUNTER_TRANSFER,
		&selection,
		flow,
	); err != nil {
		return nil, fmt.Errorf("consensus swap counter transfer failed: %w", err)
	}
	if flow.response == nil {
		return nil, fmt.Errorf("internal: consensus swap counter transfer for %s succeeded but produced no response", parsed.transferID)
	}
	return flow.response, nil
}
