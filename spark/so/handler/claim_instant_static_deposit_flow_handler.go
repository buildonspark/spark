package handler

import (
	"context"
	"fmt"
	"maps"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	entutxoswap "github.com/lightsparkdev/spark/so/ent/utxoswap"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// ClaimInstantStaticDepositFlowHandler — participant side (Prepare/Commit/Rollback)
// ---------------------------------------------------------------------------

// ClaimInstantStaticDepositFlowHandler implements consensus.FlowHandler for phase two of the
// instant static deposit claim.
// Prepare re-verifies the now-confirmed UTXO against this SO's reserved swap row and links the
// utxo edge (the legacy SaveUtxoForInstantStaticDeposit work), prepares the optional secondary
// transfer via the embedded SendTransferFlowHandler, and — for SOs in the coordinator-collected
// round-1 set — returns the FROST round-2 share over the spend-tx sighash, so signing needs no
// extra round. Commit applies the secondary transfer commit then completes the swap; Rollback
// is a deliberate no-op, matching the legacy claim's empty rollback (see Rollback's doc).
type ClaimInstantStaticDepositFlowHandler struct {
	config *so.Config
	// transfer carries the optional secondary SSP→user transfer through every phase.
	transfer *SendTransferFlowHandler
}

var _ consensus.FlowHandler = (*ClaimInstantStaticDepositFlowHandler)(nil)

func NewClaimInstantStaticDepositFlowHandler(config *so.Config) *ClaimInstantStaticDepositFlowHandler {
	return &ClaimInstantStaticDepositFlowHandler{
		config:   config,
		transfer: NewSendTransferFlowHandlerForType(config, st.TransferTypeUtxoSwap, st.TransferPartnerTypeDeposit, false /* requireDirectRefunds */),
	}
}

// claimInstantSpendTxJobNamespace is a fixed UUIDv4 mixed into NewSHA1 to derive
// a deterministic signing-job id for the claim's spend tx from the utxo, so
// every SO and the coordinator correlate round-2 shares without sending the job
// id over the wire. Distinct from the fixed-swap, refund, and send-transfer
// namespaces so job ids can never collide across flows.
var claimInstantSpendTxJobNamespace = uuid.MustParse("3d7c50f2-91ab-4b06-a8de-64c2f0b7e915")

// txid is the raw bytes from the proto UTXO field; both the round-2 job on each
// SO and BuildCommitPayload on the coordinator derive from the same field, so
// the id always agrees (see staticDepositSwapJobID).
func claimInstantSpendTxJobID(txid []byte, vout uint32) uuid.UUID {
	return uuid.NewSHA1(claimInstantSpendTxJobNamespace, fmt.Appendf(nil, "%x:%d", txid, vout))
}

// Prepare runs on every SO. It links the confirmed UTXO to this SO's reserved
// swap row (the work the legacy SaveUtxoForInstantStaticDeposit fanout does on
// each SO), validates and — if the reservation has a secondary credit — prepares
// the secondary transfer via the embedded send-transfer Prepare and links the
// secondary_transfer edge, and produces the spend-tx FROST round-2 share for
// SOs in the coordinator's round-1 set. The prepare result is the merged share
// map (secondary leaf jobs + spend-tx job).
func (h *ClaimInstantStaticDepositFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	prepareReq, ok := op.(*pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for claim instant static deposit prepare", op)
	}
	req := prepareReq.GetOriginalRequest()
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}

	swap, targetUtxo, depositAddress, err := h.linkUtxoToReservedSwap(ctx, req)
	if err != nil {
		return nil, err
	}

	merged := make(map[string]*pbcommon.SigningResult)
	if err := h.validateSecondaryTransferAgainstReservation(ctx, req, swap, targetUtxo); err != nil {
		return nil, err
	}
	if req.GetTransfer() != nil {
		transferResult, err := h.transfer.Prepare(ctx, &pbinternal.SendTransferPrepareRequest{
			OriginalRequest:      convertV2ToV3SendTransferRequest(req.GetTransfer()),
			SenderKeyTweakProofs: prepareReq.GetSenderKeyTweakProofs(),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to prepare claim secondary transfer: %w", err)
		}
		if transferResult != nil {
			transferShares, ok := transferResult.(*pbinternal.FrostRound2Response)
			if !ok {
				return nil, fmt.Errorf("unexpected transfer prepare result type %T", transferResult)
			}
			maps.Copy(merged, transferShares.GetResults())
		}
		// Link the secondary_transfer edge on this SO's own row now that both
		// rows exist in this tx (the legacy coordinator did this only locally).
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get db: %w", err)
		}
		secondaryEnt, err := db.Transfer.Get(ctx, swap.RequestedSecondaryTransferID)
		if err != nil {
			return nil, fmt.Errorf("unable to load secondary transfer %s to link to swap: %w", swap.RequestedSecondaryTransferID, err)
		}
		if _, err := swap.Update().SetSecondaryTransfer(secondaryEnt).Save(ctx); err != nil {
			return nil, fmt.Errorf("unable to link secondary transfer to swap %s: %w", swap.ID, err)
		}
	}

	// Only SOs in the coordinator's round-1 signing set produce a spend-tx
	// round-2 share; job ids are namespaced per flow so the merge cannot collide
	// with the secondary transfer's leaf jobs.
	if _, inSigningSet := prepareReq.GetSpendTxSigningCommitments()[h.config.Identifier]; inSigningSet {
		job, err := h.buildClaimSpendTxRound2Job(ctx, depositAddress, req, targetUtxo, prepareReq.GetSpendTxSigningCommitments())
		if err != nil {
			return nil, err
		}
		frostResp, err := signing_handler.NewFrostSigningHandler(h.config).FrostRound2(ctx, &pbinternal.FrostRound2Request{SigningJobs: []*pbinternal.SigningJob{job}})
		if err != nil {
			return nil, fmt.Errorf("local frost round 2 failed during prepare: %w", err)
		}
		maps.Copy(merged, frostResp.GetResults())
	}

	if len(merged) == 0 {
		return nil, nil
	}
	return &pbinternal.FrostRound2Response{Results: merged}, nil
}

// linkUtxoToReservedSwap mirrors the participant body of the legacy
// SaveUtxoForInstantStaticDeposit (static_deposit_internal_handler.go) — every
// validation it performs is preserved: the UTXO must be confirmed (threshold 1),
// its amount must match the reservation's utxo_value_sats, and its deposit
// address must be the reservation's. Legacy's coordinator ECDSA-over-statement
// check is subsumed by the authenticated ConsensusPrepare channel, the same way
// the sibling flows replaced it. Additionally validates the spend tx this SO is
// about to sign (nonce commitment + spends-the-target-utxo), which legacy only
// checked on the coordinator.
func (h *ClaimInstantStaticDepositFlowHandler) linkUtxoToReservedSwap(ctx context.Context, req *pbinternal.ClaimInstantStaticDepositUtxoSwapRequest) (*ent.UtxoSwap, *VerifiedTargetUtxo, *ent.DepositAddress, error) {
	if req.GetOnChainUtxo() == nil {
		return nil, nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("on_chain_utxo is required"))
	}
	if req.GetSpendTxSigningJob() == nil {
		return nil, nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("spend_tx_signing_job is required"))
	}
	if req.GetSpendTxSigningJob().GetSigningNonceCommitment() == nil {
		return nil, nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("spend_tx_signing_job.signing_nonce_commitment is required"))
	}
	if err := (&frost.SigningCommitment{}).UnmarshalProto(req.GetSpendTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid spend tx signing nonce commitment: %w", err))
	}
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return nil, nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}
	network, err := btcnetwork.FromProtoNetwork(req.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to parse network: %w", err)
	}
	if !h.config.IsNetworkSupported(network) {
		return nil, nil, nil, fmt.Errorf("network %s not supported", network)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get db: %w", err)
	}

	targetUtxo, err := VerifiedTargetUtxoFromRequestWithThreshold(ctx, db, network, req.GetOnChainUtxo(), 1)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to verify on-chain utxo: %w", err)
	}
	if targetUtxo == nil {
		return nil, nil, nil, sparkerrors.FailedPreconditionInsufficientConfirmations(fmt.Errorf("on-chain utxo not found or not confirmed"))
	}
	if err := validateStaticDepositSpendTxSpendsTargetUtxo(targetUtxo, req.GetSpendTxSigningJob().GetRawTx()); err != nil {
		return nil, nil, nil, err
	}

	swap, err := loadInstantSwapForClaim(ctx, db, transferID, true /* forUpdate */, st.UtxoSwapStatusCreated)
	if err != nil {
		return nil, nil, nil, err
	}
	if targetUtxo.inner.Amount != swap.UtxoValueSats {
		return nil, nil, nil, fmt.Errorf("utxo amount %d does not match swap utxo_value_sats %d", targetUtxo.inner.Amount, swap.UtxoValueSats)
	}

	utxoDepositAddress, err := targetUtxo.inner.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get deposit address for utxo: %w", err)
	}
	swapDepositAddress, err := db.DepositAddress.Query().
		Where(depositaddress.HasUtxoswapsWith(entutxoswap.IDEQ(swap.ID))).
		Only(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get deposit address for swap %s: %w", swap.ID, err)
	}
	if utxoDepositAddress.ID != swapDepositAddress.ID {
		return nil, nil, nil, fmt.Errorf("utxo deposit address %s does not match swap deposit address %s", utxoDepositAddress.ID, swapDepositAddress.ID)
	}
	if swapDepositAddress.Network != btcnetwork.Unspecified && swapDepositAddress.Network != network {
		return nil, nil, nil, fmt.Errorf("swap deposit address network %s does not match utxo network %s", swapDepositAddress.Network, network)
	}

	if _, err := swap.Update().SetUtxo(targetUtxo.inner).Save(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to save utxo on swap %s: %w", swap.ID, err)
	}
	return swap, targetUtxo, utxoDepositAddress, nil
}

// validateSecondaryTransferAgainstReservation mirrors the legacy coordinator's
// cross-field checks between the claim request and the reservation, run on
// every SO since they are deterministic against this SO's own row: a transfer
// is required iff the reservation has a secondary credit amount, must target
// the reservation's user, and must carry the pinned secondary transfer id and
// the exact secondary credit amount. ValidateTransferPackage itself is not
// duplicated here — the embedded send-transfer Prepare is the authoritative
// check, as in the reserve flow.
func (h *ClaimInstantStaticDepositFlowHandler) validateSecondaryTransferAgainstReservation(ctx context.Context, req *pbinternal.ClaimInstantStaticDepositUtxoSwapRequest, swap *ent.UtxoSwap, targetUtxo *VerifiedTargetUtxo) error {
	hasSecondaryCredit := swap.SecondaryCreditAmountSats != nil && *swap.SecondaryCreditAmountSats > 0
	if !hasSecondaryCredit {
		if req.GetTransfer() != nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer must not be provided when there is no secondary credit amount"))
		}
		return nil
	}
	if req.GetTransfer() == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer is required when secondary credit amount is set"))
	}
	receiverIdentityPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetReceiverIdentityPublicKey())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid receiver identity public key: %w", err))
	}
	if !receiverIdentityPubKey.Equals(swap.UserIdentityPublicKey) {
		return sparkerrors.InvalidArgumentPublicKeyMismatch(fmt.Errorf("transfer receiver identity public key does not match utxo swap user identity public key"))
	}
	if req.GetTransfer().GetTransferId() != swap.RequestedSecondaryTransferID.String() {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer id does not match requested secondary transfer id"))
	}
	if req.GetTransfer().GetTransferPackage() == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer package is required when secondary credit amount is set"))
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db: %w", err)
	}
	leaves, err := loadUtxoSwapTransferLeavesOnNetwork(ctx, db, req.GetTransfer(), targetUtxo.inner.Network)
	if err != nil {
		return err
	}
	return validateInstantSecondaryTransferLeavesAmount(leaves, *swap.SecondaryCreditAmountSats)
}

// validateInstantSecondaryTransferLeavesAmount lives here (untagged) rather
// than the lightspark-tagged SSP handler so the OSS participant Prepare can
// share it with the lightspark-tagged claim entrypoint.
func validateInstantSecondaryTransferLeavesAmount(leaves []*ent.TreeNode, expectedAmount uint64) error {
	actualAmount := getTotalTransferValue(leaves)
	if actualAmount != expectedAmount {
		return fmt.Errorf("secondary transfer amount %d does not match secondary credit amount %d", actualAmount, expectedAmount)
	}
	return nil
}

// buildClaimSpendTxRound2Job constructs the FROST round-2 job for this SO's
// share over the spend-tx sighash, with the same key material as the legacy
// getSpendTxSigningResult (verifyingKey = signingKeyshare.PublicKey +
// depositAddress.OwnerSigningPubkey).
func (h *ClaimInstantStaticDepositFlowHandler) buildClaimSpendTxRound2Job(ctx context.Context, depositAddress *ent.DepositAddress, req *pbinternal.ClaimInstantStaticDepositUtxoSwapRequest, targetUtxo *VerifiedTargetUtxo, round1 map[string]*pbcommon.SigningCommitment) (*pbinternal.SigningJob, error) {
	spendTxSighash, _, err := GetTxSigningInfo(ctx, targetUtxo.inner, req.GetSpendTxSigningJob().GetRawTx())
	if err != nil {
		return nil, fmt.Errorf("failed to get spend tx sighash: %w", err)
	}
	signingKeyshare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}
	verifyingKey := signingKeyshare.PublicKey.Add(depositAddress.OwnerSigningPubkey)
	jobID := claimInstantSpendTxJobID(req.GetOnChainUtxo().GetTxid(), req.GetOnChainUtxo().GetVout())
	return &pbinternal.SigningJob{
		JobId:           jobID.String(),
		Message:         spendTxSighash.Serialize(),
		KeyshareId:      signingKeyshare.ID.String(),
		VerifyingKey:    verifyingKey.Serialize(),
		Commitments:     round1,
		UserCommitments: req.GetSpendTxSigningJob().GetSigningNonceCommitment(),
	}, nil
}

// Commit is apply-only: it applies the aggregated secondary transfer commit (if
// the reservation has one) then marks this SO's swap COMPLETED — the order
// CompleteUtxoSwap requires, since it refuses to complete while any linked
// transfer is unsent (the primary has been sent since reserve). Idempotent:
// applySendTransferCommit short-circuits an already-committed transfer and an
// already-COMPLETED swap is a no-op; a missing/cancelled row is an error so
// redelivery keeps retrying instead of silently dropping a committed flow.
func (h *ClaimInstantStaticDepositFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for claim instant static deposit commit", op)
	}
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db: %w", err)
	}
	swap, err := loadInstantSwapForClaim(ctx, db, transferID, false /* forUpdate */, st.UtxoSwapStatusCreated, st.UtxoSwapStatusCompleted)
	if err != nil {
		return err
	}
	if req.GetTransferCommit() != nil {
		// Bind the coordinator-authored transfer commit to this SO's own pinned
		// secondary transfer id so a forged or mismatched commit payload cannot
		// apply signatures and key tweaks to an unrelated pre-commit transfer.
		if req.GetTransferCommit().GetTransferId() != swap.RequestedSecondaryTransferID.String() {
			return fmt.Errorf("transfer commit transfer id %s does not match the reservation's pinned secondary transfer id for swap %s", req.GetTransferCommit().GetTransferId(), swap.ID)
		}
		if err := h.transfer.applySendTransferCommit(ctx, req.GetTransferCommit()); err != nil {
			return fmt.Errorf("failed to apply claim secondary transfer commit: %w", err)
		}
	}
	if swap.Status == st.UtxoSwapStatusCompleted {
		logging.GetLoggerFromContext(ctx).Sugar().Infof("claim instant static deposit 2pc commit: swap %s already COMPLETED, idempotent retry", swap.ID)
		return nil
	}
	if err := CompleteUtxoSwap(ctx, swap); err != nil {
		return fmt.Errorf("unable to complete utxo swap %s: %w", swap.ID, err)
	}
	return nil
}

// loadInstantSwapForClaim loads this SO's INSTANT reservation for the given
// primary transfer id, shared by Prepare, Commit, and the coordinator's
// BuildCommitPayload so none of them can drop the RequestType filter — a
// transfer id is client-supplied input and must never resolve to a row created
// by a different swap flow.
func loadInstantSwapForClaim(ctx context.Context, db *ent.Client, transferID uuid.UUID, forUpdate bool, statuses ...st.UtxoSwapStatus) (*ent.UtxoSwap, error) {
	query := db.UtxoSwap.Query().
		Where(
			entutxoswap.RequestedTransferIDEQ(transferID),
			entutxoswap.RequestTypeEQ(st.UtxoSwapRequestTypeInstant),
			entutxoswap.StatusIn(statuses...),
		)
	if forUpdate {
		query = query.ForUpdate()
	}
	swap, err := query.Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load instant swap for transfer %s: %w", transferID, err)
	}
	return swap, nil
}

// Rollback deliberately mutates NOTHING — parity with the legacy claim, whose
// failure path never undoes claim-side state (its rollback callback is empty):
// the swap stays CREATED with the utxo edge Prepare linked, and the sent
// primary is untouched (cancelling would orphan it, SP-3261). A secondary
// transfer Prepare created stays SENDER_KEY_TWEAK_PENDING; the
// cancel_expired_transfers sweep returns it — unlocking its leaves — once it
// expires, the same recovery that owns stranded PREIMAGE_SWAP rounds.
// Returning it here instead would free the leaves sooner but equally burns the
// reservation's pinned secondary transfer id (a RETURNED row blocks
// re-creating it just as a pending one does), and would give a forged rollback
// payload a cross-flow transfer to corrupt; the no-op leaves nothing to
// attack. Either way a reservation that is never successfully re-claimed is
// the tracked SP-3495 stranded-reservation class. The op is still
// shape-validated so an engine dispatch bug surfaces rather than being
// absorbed; both the canonical payload and the reconciler-echoed prepare op
// are accepted.
func (h *ClaimInstantStaticDepositFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var transferIDStr string
	switch r := op.(type) {
	case *pbinternal.ClaimInstantStaticDepositUtxoSwapRollbackRequest:
		transferIDStr = r.GetTransferId()
	case *pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest:
		transferIDStr = r.GetOriginalRequest().GetTransferId()
	default:
		return fmt.Errorf("unexpected operation type %T for claim instant static deposit rollback", op)
	}
	logging.GetLoggerFromContext(ctx).Sugar().Infof(
		"claim instant static deposit 2pc rollback: no-op by design for transfer %s — reservation kept, transfer expiry owns any prepared secondary",
		transferIDStr,
	)
	return nil
}
