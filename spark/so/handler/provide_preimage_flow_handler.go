package handler

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// ProvidePreimageFlowHandler — participant side (Prepare / Commit / Rollback)
// ---------------------------------------------------------------------------

// ProvidePreimageFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_PROVIDE_PREIMAGE. Reached via the engine when
// LightningHandler.ProvidePreimage executes the flow.
//
// Holds a LightningHandler for ValidatePreimage + StorePreimage and a
// TransferHandler for validateKeyTweakProofs + CommitSenderKeyTweaks. Holds
// them as fields (rather than embedding one
// like the send-transfer / claim-transfer handlers do) because both
// handlers carry their own *so.Config and embedding both would create a
// promoted-field ambiguity.
type ProvidePreimageFlowHandler struct {
	lightning *LightningHandler
	transfer  *TransferHandler
}

var _ consensus.FlowHandler = (*ProvidePreimageFlowHandler)(nil)

func NewProvidePreimageFlowHandler(config *so.Config) *ProvidePreimageFlowHandler {
	return &ProvidePreimageFlowHandler{
		lightning: NewLightningHandler(config),
		transfer:  NewTransferHandler(config),
	}
}

// Prepare runs on every SO (including the coordinator). Validates the preimage
// + key-tweak proofs against this SO's local state and persists the preimage
// on the preimage_request row.
//
// Mirrors the legacy ValidatePreimageInternal body: ValidatePreimage runs the
// cryptographic checks + loads the transfer; validateKeyTweakProofs cross-checks
// the coordinator-supplied proofs against the participant's own TransferLeaf
// rows; StorePreimage CASes preimage_request WAITING → PREIMAGE_SHARED. The
// StorePreimage CAS is idempotent so an engine-level Prepare retry against an
// already-shared row is a no-op.
//
// Fail-closed on transfer status BEFORE persisting the preimage: ValidatePreimage
// returns the transfer regardless of status, and validateKeyTweakProofs passes
// vacuously when the local transfer has zero leaves and the proofs map is empty.
// Without this guard a transfer at SenderInitiated (key tweaks never staged on
// this SO) would clear all three checks, StorePreimage would durably persist the
// secret, and then Commit would reject the transfer (it isn't committable) while
// Rollback is a no-op — leaving the preimage written with no path to ever apply
// the key tweaks. Requiring a committable pre-commit status here means the engine
// aborts the whole flow (every SO's Prepare must succeed) before any SO writes
// the preimage, rather than discovering the problem only at Commit time.
func (h *ProvidePreimageFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	req, ok := op.(*pbinternal.ProvidePreimagePrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for provide preimage prepare", op)
	}
	orig := req.GetOriginalRequest()
	if orig == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("original_request is required"))
	}

	preimageRequest, transfer, err := h.lightning.ValidatePreimage(ctx, orig)
	if err != nil {
		return nil, fmt.Errorf("validate preimage: %w", err)
	}

	if !isProvidePreimageCommittableStatus(transfer.Status) {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
			"provide preimage 2pc prepare: transfer %s is at status %s; refusing to persist the preimage for a non-committable transfer",
			transfer.ID, transfer.Status))
	}

	if err := h.transfer.validateKeyTweakProofs(ctx, transfer, req.GetKeyTweakProofs()); err != nil {
		return nil, fmt.Errorf("validate key tweak proofs: %w", err)
	}

	if err := h.lightning.StorePreimage(ctx, preimageRequest, orig.GetPreimage()); err != nil {
		return nil, fmt.Errorf("store preimage: %w", err)
	}

	return nil, nil
}

// isProvidePreimageCommittableStatus reports whether a transfer is in a
// pre-commit state where ProvidePreimage can legitimately stage the preimage
// and later commit the sender key tweaks. Matches the status set that
// commitSenderKeyTweaks accepts (and the set applySendTransferCommit treats as
// "apply commit work"). SenderInitiated is deliberately excluded — it means
// the staging InitiatePreimageSwap never landed key tweaks on this SO.
func isProvidePreimageCommittableStatus(status st.TransferStatus) bool {
	switch status {
	case st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusApplyingSenderKeyTweak:
		return true
	default:
		return false
	}
}

// Commit applies the sender key tweaks on this participant by calling the same
// CommitSenderKeyTweaks helper the legacy SettleSenderKeyTweak gossip dispatch
// uses.
//
// Idempotent against gossip redelivery and reconciler replay: applyProvidePreimageCommit
// loads the transfer and short-circuits if it has already moved past the
// pre-commit states. Without that short-circuit, a redelivered Commit would
// re-enter CommitSenderKeyTweaks → validateKeyTweakProofs, which fails after
// the first Commit cleared each leaf's KeyTweak column (the unmarshalled blob
// becomes the zero-value SendLeafKeyTweak with nil SecretShareTweak).
func (h *ProvidePreimageFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.ProvidePreimageCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for provide preimage commit", op)
	}
	return h.applyProvidePreimageCommit(ctx, req)
}

// Rollback is a no-op for PROVIDE_PREIMAGE. The preimage write in Prepare is
// intentionally irreversible — the receiver-side SO has already learned the
// secret, and erasing the preimage_request row would not un-reveal it.
// CommitSenderKeyTweaks only runs in Commit, so a rollback before commit
// gossip is dispatched means no participant ever moved sender key tweaks.
//
// No coordinator commit/rollback divergence (engine-level): the engine records
// the COMMITTED decision in the request tx and commits it atomically with the
// coordinator's domain work (the SenderKeyTweaked transition lands in the same
// tx), so there is no durable state where the transfer is committed but the
// FlowExecution row is still IN_FLIGHT for the self-sweep to roll back. A crash
// before that atomic commit leaves the transfer untweaked and the row IN_FLIGHT
// (sweep → ROLLED_BACK, consistent); a crash after it leaves the row COMMITTED
// (reconciler drives participants forward, consistent). See SP-3195 and
// consensus/twopc.go's ErrCoordinatorRowPreempted docstring.
//
// Accepts both the canonical ProvidePreimageRollbackRequest and the
// ProvidePreimagePrepareRequest echoed back by the participant reconciler
// when the coordinator's row is missing.
func (h *ProvidePreimageFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var transferIDStr, paymentHashHex string
	switch r := op.(type) {
	case *pbinternal.ProvidePreimageRollbackRequest:
		transferIDStr = r.GetTransferId()
	case *pbinternal.ProvidePreimagePrepareRequest:
		// The reconciler-synthesized rollback echoes the prepare op, which
		// doesn't carry a transfer_id directly — surface the payment hash so
		// an oncall correlating logs at 2am has a handle.
		if orig := r.GetOriginalRequest(); orig != nil {
			paymentHashHex = hex.EncodeToString(orig.GetPaymentHash())
		}
	default:
		return fmt.Errorf("unexpected operation type %T for provide preimage rollback", op)
	}

	logging.GetLoggerFromContext(ctx).Sugar().Infof(
		"provide preimage 2pc rollback: no-op (transfer_id=%q payment_hash=%q) — preimage write in Prepare is intentionally irreversible",
		transferIDStr, paymentHashHex)
	return nil
}

// applyProvidePreimageCommit applies sender key tweaks on a single SO. Shared
// by participant Commit and coordinator BuildCommitPayload.
//
// The status switch handles four buckets explicitly:
//
//  1. Committable pre-commit (SenderInitiatedCoordinator / SenderKeyTweakPending
//     / ApplyingSenderKeyTweak): fall through to CommitSenderKeyTweaks. The
//     normal happy path.
//
//  2. SenderInitiated: surface as an error. This status means the staging
//     InitiatePreimageSwap never landed key tweaks on this SO — Commit cannot
//     legitimately succeed. Returning nil would mislead runConsensusCommit into
//     marking this participant's FlowExecution row COMMITTED while
//     CommitSenderKeyTweaks never ran, a distributed inconsistency where the
//     engine reports success but the SO is missing the sender key tweaks.
//     (Prepare's fail-closed status check should prevent this flow from ever
//     reaching Commit in SenderInitiated, but Commit guards independently.)
//
//  3. True post-commit (SenderKeyTweaked / ReceiverKeyTweaked* / Completed):
//     idempotent retry — the sender key tweaks are already applied, so return
//     nil and let runConsensusCommit mark the row COMMITTED. Without this, a
//     redelivered Commit would re-enter CommitSenderKeyTweaks →
//     validateKeyTweakProofs, which fails after the first Commit cleared each
//     leaf's KeyTweak column.
//
//  4. Cancelled-terminal (Returned / Expired): a concurrent cancel/expiry won
//     the race after Prepare but before this Commit. The sender key tweaks were
//     NOT applied on this SO, yet returning nil marks the FlowExecution row
//     COMMITTED — a genuine divergence (engine row COMMITTED vs transfer
//     cancelled). We still return nil rather than erroring because erroring
//     would make runConsensusCommit loop forever (the coordinator already
//     recorded COMMITTED, so reconciler replays of Commit would re-fail). This
//     matches applySendTransferCommit's behavior; the transfer business state —
//     not the FlowExecution row — is the source of truth for settlement.
//     TODO(SPARK): once the engine grows a "committed-but-locally-cancelled"
//     outcome, surface this divergence explicitly instead of absorbing it.
func (h *ProvidePreimageFlowHandler) applyProvidePreimageCommit(ctx context.Context, req *pbinternal.ProvidePreimageCommitRequest) error {
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}
	transferEnt, err := h.transfer.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s for commit: %w", transferID, err)
	}
	logger := logging.GetLoggerFromContext(ctx)
	switch transferEnt.Status {
	case st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusApplyingSenderKeyTweak:
		// Fall through to apply commit work.
	case st.TransferStatusSenderInitiated:
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
			"provide preimage 2pc commit: transfer %s is at status %s — key tweaks were never prepared on this SO; refusing to mark the participant FlowExecution COMMITTED",
			transferID, transferEnt.Status))
	case st.TransferStatusSenderKeyTweaked,
		st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverRefundSigned,
		st.TransferStatusCompleted:
		logger.Sugar().Infof(
			"provide preimage 2pc commit: transfer %s already past pre-commit (status=%s), treating as idempotent retry",
			transferID, transferEnt.Status)
		return nil
	case st.TransferStatusReturned, st.TransferStatusExpired:
		// See bucket 4 in the docstring: known engine-level divergence.
		logger.Sugar().Warnf(
			"provide preimage 2pc commit: transfer %s is terminal-cancelled (status=%s) but Commit arrived — sender key tweaks were NOT applied on this SO; marking FlowExecution COMMITTED anyway to avoid a reconcile loop (transfer state is the source of truth)",
			transferID, transferEnt.Status)
		return nil
	default:
		// Defensive: an unrecognized status is safer to surface than to
		// silently absorb. Should be unreachable given the enum is enumerated above.
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
			"provide preimage 2pc commit: transfer %s has unexpected status %s", transferID, transferEnt.Status))
	}
	if _, err := h.transfer.CommitSenderKeyTweaks(ctx, transferID, req.GetKeyTweakProofs()); err != nil {
		return fmt.Errorf("commit sender key tweaks for transfer %s: %w", transferID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// providePreimageCoordinatorFlow — coordinator side
// ---------------------------------------------------------------------------

// providePreimageCoordinatorFlow drives the coordinator-side of ProvidePreimage
// through the 2PC engine. BuildCommitPayload runs CommitSenderKeyTweaks
// locally on the coordinator (mirroring CONSENSUS_OPERATION_TYPE_SEND_TRANSFER's
// applySendTransferCommit pattern) so the coordinator's state advances
// deterministically rather than depending on the gossip-loopback in
// postSendingGossipMessage filling the participant bitmap before firing the
// coordinator-local handler. The 2PC path is strictly stronger than the
// legacy fanout, which strands the coordinator at SenderKeyTweakPending
// while waiting for the gossip bitmap to fill if any participant ACK lags.
//
// response is populated during BuildCommitPayload so the public
// ProvidePreimage handler can return the freshly-marshaled transfer to the
// caller after engine.Execute completes.
type providePreimageCoordinatorFlow struct {
	*ProvidePreimageFlowHandler

	req            *pbspark.ProvidePreimageRequest
	transferID     uuid.UUID
	keyTweakProofs map[string]*pbspark.SecretProof

	response *pbspark.ProvidePreimageResponse
}

var _ consensus.CoordinatorFlow = (*providePreimageCoordinatorFlow)(nil)

// PrepareOp returns the prepare payload fanned out to every SO.
func (f *providePreimageCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.ProvidePreimagePrepareRequest{
		OriginalRequest: f.req,
		KeyTweakProofs:  f.keyTweakProofs,
	}
}

// BuildCommitPayload commits sender key tweaks on the coordinator's DB so the
// engine's request-tx commit carries the final transfer state, then builds the
// commit payload (transfer_id + proofs) gossiped to participants. Also
// populates the response the public handler returns to the caller.
//
// Coordinator Commit fires exactly once via this call. The ConsensusCommit
// gossip dispatch in gossip_handler.go is gated with !forCoordinator (see
// dispatchConsensusCommit), so the gossip-loopback does NOT re-run Commit on
// the coordinator — applyProvidePreimageCommit's idempotency guard exists for
// participants, not for a coord self-replay.
//
// The results parameter is unused: Prepare returns nil on every SO (no FROST
// shares to aggregate for this flow), unlike send-transfer / claim-transfer
// where BuildCommitPayload aggregates per-leaf signature shares.
func (f *providePreimageCoordinatorFlow) BuildCommitPayload(ctx context.Context, _ map[string]*anypb.Any) (proto.Message, error) {
	commitReq := &pbinternal.ProvidePreimageCommitRequest{
		TransferId:     f.transferID.String(),
		KeyTweakProofs: f.keyTweakProofs,
	}
	if err := f.applyProvidePreimageCommit(ctx, commitReq); err != nil {
		return nil, fmt.Errorf("failed to apply commit on coordinator: %w", err)
	}

	transferEnt, err := f.transfer.loadTransferForUpdate(ctx, f.transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to reload transfer %s after commit: %w", f.transferID, err)
	}
	transferProto, err := transferEnt.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer %s for response: %w", f.transferID, err)
	}
	f.response = &pbspark.ProvidePreimageResponse{Transfer: transferProto}

	return commitReq, nil
}

// RollbackPayload returns the minimal payload sent to participants on rollback.
func (f *providePreimageCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.ProvidePreimageRollbackRequest{TransferId: f.transferID.String()}
}

// ---------------------------------------------------------------------------
// Coordinator-flow builder
// ---------------------------------------------------------------------------

// buildProvidePreimageCoordinatorFlow constructs the coordinator-side flow
// from a pre-loaded transfer's TransferLeaf rows. The caller (the public
// ProvidePreimage handler) is responsible for running ValidatePreimage first
// so it can short-circuit when the transfer has already advanced past the
// pre-commit states without paying the engine's bookkeeping cost.
//
// transferLeaves must be the leaves owned by transfer and must be non-empty —
// a transfer with no leaves has no sender key tweaks to commit and reaching
// this builder with zero leaves indicates a data-integrity bug upstream
// (rather than letting the engine fan out a no-op flow).
func buildProvidePreimageCoordinatorFlow(
	config *so.Config,
	req *pbspark.ProvidePreimageRequest,
	transfer *ent.Transfer,
	transferLeaves []*ent.TransferLeaf,
) (*providePreimageCoordinatorFlow, error) {
	if len(transferLeaves) == 0 {
		return nil, sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("transfer %s has no transfer_leaves; cannot derive sender key tweak proofs", transfer.ID))
	}
	proofs := make(map[string]*pbspark.SecretProof, len(transferLeaves))
	for _, leaf := range transferLeaves {
		keyTweakProto := &pbspark.SendLeafKeyTweak{}
		if err := proto.Unmarshal(leaf.KeyTweak, keyTweakProto); err != nil {
			return nil, fmt.Errorf("unable to unmarshal key tweak: %w", err)
		}
		if keyTweakProto.GetSecretShareTweak() == nil {
			return nil, sparkerrors.InvalidArgumentMissingField(
				fmt.Errorf("secret share tweak missing for leaf %s", keyTweakProto.GetLeafId()))
		}
		proofs[keyTweakProto.GetLeafId()] = &pbspark.SecretProof{
			Proofs: keyTweakProto.GetSecretShareTweak().GetProofs(),
		}
	}

	return &providePreimageCoordinatorFlow{
		ProvidePreimageFlowHandler: NewProvidePreimageFlowHandler(config),
		req:                        req,
		transferID:                 transfer.ID,
		keyTweakProofs:             proofs,
	}, nil
}
