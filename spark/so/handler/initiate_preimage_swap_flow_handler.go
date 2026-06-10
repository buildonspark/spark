package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	"github.com/lightsparkdev/spark/common/uuids"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	"github.com/lightsparkdev/spark/so/ent/preimageshare"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/partner"
	decodepay "github.com/nbd-wtf/ln-decodepay"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// InitiatePreimageSwapFlowHandler — participant side (Prepare / Commit / Rollback)
// ---------------------------------------------------------------------------

// InitiatePreimageSwapFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_INITIATE_PREIMAGE_SWAP. Reached via the engine when
// LightningHandler.InitiatePreimageSwapV3 routes through it (gated on
// KnobUseConsensusInitiatePreimageSwap).
//
// Holds a LightningHandler (validation + createTransfer + storeUserSignedTransactions
// + buildHTLCRefundMaps) and a TransferHandler (ValidateTransferPackage,
// UpdateTransferLeavesSignatures, CommitSenderKeyTweaks, the cancel helpers) as
// fields rather than embedding either — both carry their own *so.Config and
// embedding both would create a promoted-field ambiguity (same pattern as
// ProvidePreimageFlowHandler). config is stored explicitly for FROST round-2.
type InitiatePreimageSwapFlowHandler struct {
	config    *so.Config
	lightning *LightningHandler
	transfer  *TransferHandler
}

var _ consensus.FlowHandler = (*InitiatePreimageSwapFlowHandler)(nil)

func NewInitiatePreimageSwapFlowHandler(config *so.Config) *InitiatePreimageSwapFlowHandler {
	return &InitiatePreimageSwapFlowHandler{
		config:    config,
		lightning: NewLightningHandler(config),
		transfer:  NewTransferHandler(config),
	}
}

// preimageSwapPreparedState carries the rows Prepare created so the FROST /
// preimage-share steps that follow can build on them without re-querying.
type preimageSwapPreparedState struct {
	transfer      *ent.Transfer
	leafMap       map[string]*ent.TreeNode
	preimageShare *ent.PreimageShare
	transferID    uuid.UUID
}

// Prepare runs on every SO (including the coordinator). It mirrors the legacy
// GetPreimageShare participant path — full preimage-swap validation
// (validateNodeOwnership=false; the coordinator's session-based ownership check
// runs once in the entrypoint) + createTransfer + storeUserSignedTransactions —
// and then diverges from legacy in two ways that the 2PC refold requires:
//
//   - REASON_SEND with a transfer package: instead of applying coordinator-signed
//     refund signatures handed down in the fanout request, every SO produces its
//     own FROST round-2 shares over the HTLC refund txs. The coordinator
//     aggregates them in BuildCommitPayload and the aggregated signatures come
//     back in the Commit op. This is the same mechanism CONSENSUS_OPERATION_TYPE_SEND_TRANSFER
//     uses; the HTLC refund txs in the transfer package carry the same
//     signing_commitments / signing_nonce_commitment shape, so the SEND signing
//     helpers (buildSendTransferLocalSigningJobs, etc.) are reused verbatim.
//   - non-HODL REASON_RECEIVE: this SO's preimage share is returned so the
//     coordinator can recover the preimage from a threshold of shares.
func (h *InitiatePreimageSwapFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	prepareReq, ok := op.(*pbinternal.InitiatePreimageSwapPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for initiate preimage swap prepare", op)
	}
	req := prepareReq.GetOriginalRequest()
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}

	state, err := h.prepareState(ctx, req)
	if err != nil {
		return nil, err
	}

	resp := &pbinternal.InitiatePreimageSwapPrepareResponse{}

	// REASON_SEND + transfer package: produce FROST round-2 shares for the HTLC
	// refund txs. The transfer package's signing jobs ARE the HTLC refund jobs
	// (buildHTLCRefundMaps reconstructed and byte-matched them against the
	// package in prepareState), so the SEND signing-job builder applies directly.
	if req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_SEND && req.GetTransferRequest() != nil {
		pkg := req.GetTransferRequest().GetTransferPackage()
		jobs, err := buildSendTransferLocalSigningJobs(ctx, state.transferID, pkg, state.leafMap)
		if err != nil {
			return nil, fmt.Errorf("failed to build local signing jobs: %w", err)
		}
		jobs = filterJobsForThisOperator(jobs, h.config.Identifier)
		if len(jobs) > 0 {
			frostHandler := signing_handler.NewFrostSigningHandler(h.config)
			frostResp, err := frostHandler.FrostRound2(ctx, &pbinternal.FrostRound2Request{SigningJobs: jobs})
			if err != nil {
				return nil, fmt.Errorf("local frost round 2 failed during prepare: %w", err)
			}
			resp.FrostResponse = frostResp
		}
	}

	// non-HODL REASON_RECEIVE: contribute this SO's preimage share.
	if state.preimageShare != nil {
		resp.PreimageShare = state.preimageShare.PreimageShare
	}

	return resp, nil
}

// prepareState runs the legacy GetPreimageShare validation + row-creation
// sequence (minus the refund-signature application, which the refold moves to
// Commit). Every validation call matches the legacy participant path exactly so
// the consensus path preserves byte-for-byte parity. requireDirectTx=true on
// every SO mirrors the SEND consensus flow's choice: the legacy coordinator
// enforced the strict direct-tx check up front (initiatePreimageSwap passes
// requireDirectTx=true) so participants could trust it; under 2PC Prepare is the
// only createTransfer call site, so the check lives here on every SO.
func (h *InitiatePreimageSwapFlowHandler) prepareState(ctx context.Context, req *pbspark.InitiatePreimageSwapRequest) (*preimageSwapPreparedState, error) {
	if req.GetTransfer() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer is required"))
	}
	if req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE && req.GetFeeSats() != 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("fee is not allowed for receive preimage swap"))
	}

	// Receiver key from the top-level field, matching the legacy participant
	// (GetPreimageShare) path. When a transfer package is present,
	// validateIdenticalLeavesInTransferAndTransferRequest below enforces that
	// this equals transfer.receiver and transfer_request.receiver.
	receiverIdentityPubKey, err := keys.ParsePublicKey(req.GetReceiverIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse receiver identity public key: %w", err))
	}

	var preimageShare *ent.PreimageShare
	if req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE {
		tx, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get current tx for request: %w", err)
		}
		// HODL invoices have no preimage share yet — the user provides it later
		// via ProvidePreimage. A missing share is the HODL path, not an error.
		preimageShare, err = tx.PreimageShare.Query().Where(preimageshare.PaymentHash(req.GetPaymentHash())).First(ctx)
		if err != nil {
			if !ent.IsNotFound(err) {
				return nil, fmt.Errorf("unable to get preimage share for payment hash %x: %w", req.GetPaymentHash(), err)
			}
			preimageShare = nil
		} else if !preimageShare.OwnerIdentityPubkey.Equals(receiverIdentityPubKey) {
			return nil, sparkerrors.InvalidArgumentPublicKeyMismatch(
				fmt.Errorf("preimage share owner identity public key mismatch for payment hash %x", req.GetPaymentHash()))
		}
	}

	// Derive the invoice amount from the stored bolt11 (not the client-claimed
	// amount) whenever a preimage share exists, matching legacy.
	invoiceAmount := req.GetInvoiceAmount()
	if preimageShare != nil {
		bolt11, err := decodepay.Decodepay(preimageShare.InvoiceString)
		if err != nil {
			return nil, fmt.Errorf("unable to decode invoice: %w", err)
		}
		if bolt11.MSatoshi > 0 {
			invoiceAmount = &pbspark.InvoiceAmount{
				ValueSats: uint64(bolt11.MSatoshi / 1000),
				InvoiceAmountProof: &pbspark.InvoiceAmountProof{
					Bolt11Invoice: preimageShare.InvoiceString,
				},
			}
		}
	}

	if err := h.lightning.ValidateDuplicateLeaves(ctx, req.GetTransfer().GetLeavesToSend(), req.GetTransfer().GetDirectLeavesToSend(), req.GetTransfer().GetDirectFromCpfpLeavesToSend()); err != nil {
		return nil, err
	}

	if req.GetTransferRequest() != nil {
		if err := h.lightning.validateIdenticalLeavesInTransferAndTransferRequest(ctx, req); err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer validation failed: %w", err))
		}
	}

	if err := h.lightning.ValidateGetPreimageRequest(
		ctx,
		req.GetPaymentHash(),
		req.GetTransfer().GetLeavesToSend(),
		req.GetTransfer().GetDirectLeavesToSend(),
		req.GetTransfer().GetDirectFromCpfpLeavesToSend(),
		invoiceAmount,
		receiverIdentityPubKey,
		req.GetFeeSats(),
		req.GetReason(),
		false, // validateNodeOwnership: coordinator-only (session-based), done in the entrypoint
	); err != nil {
		return nil, fmt.Errorf("unable to validate request for payment hash %x: %w", req.GetPaymentHash(), err)
	}

	ownerIdentityPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse owner identity public key: %w", err))
	}
	transferID, err := uuid.Parse(req.GetTransfer().GetTransferId())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse transfer id: %w", err))
	}

	// Refund maps default to the client-supplied raw txs (legacy req.Transfer
	// path). When a transfer package is present, ValidateTransferPackage
	// validates + decrypts this SO's key-tweak slice and buildHTLCRefundMaps
	// reconstructs the HTLC refund txs and byte-matches them against the package.
	cpfpLeafRefundMap := make(map[string][]byte)
	directLeafRefundMap := make(map[string][]byte)
	directFromCpfpLeafRefundMap := make(map[string][]byte)
	for _, t := range req.GetTransfer().GetLeavesToSend() {
		cpfpLeafRefundMap[t.GetLeafId()] = t.GetRawTx()
	}
	for _, t := range req.GetTransfer().GetDirectLeavesToSend() {
		directLeafRefundMap[t.GetLeafId()] = t.GetRawTx()
	}
	for _, t := range req.GetTransfer().GetDirectFromCpfpLeavesToSend() {
		directFromCpfpLeafRefundMap[t.GetLeafId()] = t.GetRawTx()
	}

	var keyTweakMap map[string]*pbspark.SendLeafKeyTweak
	if req.GetTransferRequest() != nil {
		keyTweakMap, err = h.transfer.ValidateTransferPackage(ctx, transferID, req.GetTransferRequest().GetTransferPackage(), ownerIdentityPubKey, false)
		if err != nil {
			return nil, fmt.Errorf("unable to validate transfer package: %w", err)
		}
		cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap, err = h.lightning.buildHTLCRefundMaps(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("unable to build htlc refund maps: %w", err)
		}
	}

	transfer, leafMap, err := h.transfer.createTransfer(
		ctx,
		transferID,
		nil,
		st.TransferTypePreimageSwap,
		req.GetTransfer().GetExpiryTime().AsTime(),
		ownerIdentityPubKey,
		receiverIdentityPubKey,
		cpfpLeafRefundMap,
		directLeafRefundMap,
		directFromCpfpLeafRefundMap,
		keyTweakMap,
		TransferRoleParticipant,
		true, // requireDirectTx
		"",
		uuid.Nil,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create transfer for payment hash %x: %w", req.GetPaymentHash(), err)
	}

	status := st.PreimageRequestStatusWaitingForPreimage
	if req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE && preimageShare != nil {
		status = st.PreimageRequestStatusPreimageShared
	}
	if _, err := h.lightning.storeUserSignedTransactions(
		ctx,
		req.GetPaymentHash(),
		preimageShare,
		req.GetTransfer().GetLeavesToSend(),
		transfer,
		status,
		receiverIdentityPubKey,
		ownerIdentityPubKey,
	); err != nil {
		return nil, fmt.Errorf("unable to store user signed transactions for payment hash %x and transfer id %s: %w", req.GetPaymentHash(), transfer.ID, err)
	}

	return &preimageSwapPreparedState{
		transfer:      transfer,
		leafMap:       leafMap,
		preimageShare: preimageShare,
		transferID:    transferID,
	}, nil
}

// Commit runs on every participant (the coordinator's equivalent work runs in
// BuildCommitPayload). The split exists so the coordinator can apply the commit
// inside the engine's request tx — atomic with the COMMITTED decision — while
// participants apply the same payload on gossip delivery. The shared
// applyInitiatePreimageSwapCommit is idempotent against the redelivery that
// runConsensusCommit performs on every gossip delivery.
func (h *InitiatePreimageSwapFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.InitiatePreimageSwapCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for initiate preimage swap commit", op)
	}
	return h.applyInitiatePreimageSwapCommit(ctx, req)
}

// applyInitiatePreimageSwapCommit applies the commit work on a single SO. Shared
// by participant Commit and coordinator BuildCommitPayload. The three operations
// are gated by the commit op's fields, which the coordinator populates on
// mutually exclusive flow paths:
//
//   - leaf_signatures (REASON_SEND + pkg): aggregated HTLC refund signatures.
//   - preimage (non-HODL RECEIVE): the recovered preimage to persist.
//   - key_tweak_proofs (non-HODL RECEIVE + pkg): settle the sender key tweaks.
//
// Idempotent against gossip redelivery (runConsensusCommit re-invokes Commit on
// every delivery, including after the participant row is already COMMITTED):
//
//   - Preimage persistence is a SetPreimage to the same bytes — idempotent — and
//     is intentionally applied regardless of transfer status (the receiver SO has
//     already learned the secret; mirrors handlePreimageSwapGossipMessage).
//   - Signature + key-tweak settlement run only while the transfer is in a
//     pre-commit-with-tweaks status. Re-applying signatures at that status is
//     idempotent because UpdateTxWithSignature *replaces* the witness (it does
//     not append) and this flow never settles key tweaks for REASON_SEND, so the
//     leaf keys the signatures verify against don't change. Once a later
//     ProvidePreimage settles the key tweaks (status leaves the pre-commit set),
//     re-applying signatures would verify the old signature against tweaked keys,
//     so it is skipped. Key-tweak settlement is likewise skipped once applied
//     (CommitSenderKeyTweaks clears the key_tweak columns and is not re-runnable).
func (h *InitiatePreimageSwapFlowHandler) applyInitiatePreimageSwapCommit(ctx context.Context, req *pbinternal.InitiatePreimageSwapCommitRequest) error {
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}
	transferEnt, err := h.transfer.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s for commit: %w", transferID, err)
	}
	logger := logging.GetLoggerFromContext(ctx)

	// Persist the recovered preimage first (it is a precondition for the
	// CommitSenderKeyTweaks hash check below). Applied regardless of transfer
	// status — the receiver SO has already learned the secret, so this is an
	// irreversible reveal that mirrors handlePreimageSwapGossipMessage. persistPreimage
	// re-verifies sha256(preimage)==payment_hash so a tampered/Byzantine commit
	// payload cannot durably store a wrong preimage, and SetPreimage to the same
	// bytes is idempotent against gossip redelivery.
	if len(req.GetPreimage()) > 0 {
		if err := h.persistPreimage(ctx, transferID, req.GetPreimage()); err != nil {
			return fmt.Errorf("unable to persist preimage for transfer %s: %w", transferID, err)
		}
	}

	// Refund-signature application and key-tweak settlement only run while the
	// transfer is in a pre-commit-with-tweaks status. The status switch handles
	// redelivery and invariant violations explicitly, mirroring applyProvidePreimageCommit:
	if len(req.GetLeafSignatures()) > 0 || len(req.GetKeyTweakProofs()) > 0 {
		switch {
		case isPreimageSwapSettleableStatus(transferEnt.Status):
			// Pre-commit with staged tweaks — apply settlement work below.
		case transferEnt.Status == st.TransferStatusSenderInitiated:
			// A settlement payload (refund signatures or key-tweak proofs) for a
			// transfer that never staged key tweaks is an invariant violation:
			// Prepare creates +pkg transfers at SenderKeyTweakPending. Surface it
			// rather than silently marking the FlowExecution row COMMITTED (which
			// would hide a participant missing its settlement — a distributed
			// inconsistency). Mirrors applyProvidePreimageCommit's SenderInitiated
			// bucket.
			//
			// This branch is unreachable by construction: a settlement payload is
			// only built when the prepare op carried a transfer package, and every
			// SO derives the same SenderKeyTweakPending status from that identical
			// op — so a settlement Commit never lands on a SenderInitiated transfer.
			// runConsensusCommit only treats codes.AlreadyExists as success, so this
			// FailedPrecondition deliberately fails loud (stuck flow → alert) rather
			// than risk silently skipping settlement; a stuck-row alert is preferable
			// to a money-losing silent skip for a genuine invariant breach.
			return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
				"initiate preimage swap 2pc commit: transfer %s is at status %s but the commit carries settlement work; key tweaks were never staged on this SO",
				transferID, transferEnt.Status))
		default:
			// Already-committed (SenderKeyTweaked / ReceiverKeyTweaked* / Completed)
			// or terminal-cancelled (Returned / Expired): treat as an idempotent
			// retry. Settlement already ran (or the transfer was cancelled and the
			// business state — not the FlowExecution row — is the source of truth);
			// re-running UpdateTransferLeavesSignatures / CommitSenderKeyTweaks would
			// fail. Return nil so runConsensusCommit marks the row COMMITTED instead
			// of looping. Mirrors applySendTransferCommit / applyProvidePreimageCommit.
			logger.Sugar().Infof(
				"initiate preimage swap 2pc commit: transfer %s already past pre-commit (status=%s), treating as idempotent retry",
				transferID, transferEnt.Status)
			return nil
		}

		if len(req.GetLeafSignatures()) > 0 {
			cpfpSigs, directSigs, dfcSigs := splitLeafSignatures(req.GetLeafSignatures())
			if err := h.transfer.UpdateTransferLeavesSignatures(ctx, transferEnt, cpfpSigs, directSigs, dfcSigs); err != nil {
				return fmt.Errorf("unable to apply refund signatures for transfer %s: %w", transferID, err)
			}
		}
		if len(req.GetKeyTweakProofs()) > 0 {
			if _, err := h.transfer.CommitSenderKeyTweaks(ctx, transferID, req.GetKeyTweakProofs()); err != nil {
				return fmt.Errorf("unable to commit sender key tweaks for transfer %s: %w", transferID, err)
			}
		}
	}

	return nil
}

// isPreimageSwapSettleableStatus reports whether a transfer is in a pre-commit
// state where sender key tweak settlement is meaningful. Same set as the
// package-level isSwapKeyTweakCommitStatus / isProvidePreimageCommittableStatus
// (the pre-commit sender-key-tweak states CommitSenderKeyTweaks accepts); kept as
// a flow-local predicate so the enum-coverage guard test pins the classification
// for this flow. NOTE: this is the *settlement* set — distinct from the
// *cancellable* set used by Rollback (which includes SenderInitiated but not
// ApplyingSenderKeyTweak).
func isPreimageSwapSettleableStatus(status st.TransferStatus) bool {
	return isSwapKeyTweakCommitStatus(status)
}

// persistPreimage stores the recovered preimage on the preimage_request rows
// linked to this transfer and promotes WAITING_FOR_PREIMAGE → PREIMAGE_SHARED.
// Scoped to the transfer (rather than payment hash, as handlePreimageSwapGossipMessage
// does) because a consensus flow settles exactly one transfer. Idempotent.
//
// Re-verifies sha256(preimage)==payment_hash before writing — the coordinator
// verifies the recovered preimage in BuildCommitPayload, but participants accept
// the preimage from the commit payload on faith, so re-checking here closes the
// Byzantine-coordinator / tampered-gossip gap (matching handlePreimageSwapGossipMessage).
func (h *InitiatePreimageSwapFlowHandler) persistPreimage(ctx context.Context, transferID uuid.UUID, preimage []byte) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get db context: %w", err)
	}
	preimageRequests, err := db.PreimageRequest.Query().
		Where(preimagerequest.HasTransfersWith(enttransfer.IDEQ(transferID))).
		ForUpdate().
		All(ctx)
	if err != nil {
		return fmt.Errorf("unable to load preimage requests for transfer %s: %w", transferID, err)
	}
	if len(preimageRequests) == 0 {
		return sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("no preimage request linked to transfer %s; cannot persist preimage", transferID))
	}
	hash := sha256.Sum256(preimage)
	for _, pr := range preimageRequests {
		if !bytes.Equal(hash[:], pr.PaymentHash) {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
				"preimage does not match payment hash for preimage request %s", pr.ID))
		}
		update := pr.Update().SetPreimage(preimage)
		if pr.Status == st.PreimageRequestStatusWaitingForPreimage {
			update = update.SetStatus(st.PreimageRequestStatusPreimageShared)
		}
		if _, err := update.Save(ctx); err != nil {
			return fmt.Errorf("unable to update preimage request %s: %w", pr.ID, err)
		}
	}
	return nil
}

// Rollback cancels the transfer this SO wrote in Prepare — transfer → RETURNED,
// leaves unlocked — via executeCancelTransfer, the same mechanism the legacy
// CancelTransfer / RollbackTransferGossip path uses. Idempotent: a never-created
// transfer (NotFound) or an already-terminal one is a no-op.
//
// The preimage is NOT un-revealed on rollback. For the non-HODL receive path the
// preimage is only persisted in Commit (after the commit decision), so a rollback
// — which fires before any commit gossip — means no SO ever persisted it. For
// REASON_SEND there is no preimage at this stage. This is consistent with the
// engine's roll-back-only recovery model (see ProvidePreimageFlowHandler.Rollback
// and SP-3195): a crash before the engine's atomic decision commit rolls every SO
// back; a crash after leaves a COMMITTED row the reconciler drives forward.
//
// Accepts both InitiatePreimageSwapRollbackRequest (the canonical payload) and
// InitiatePreimageSwapPrepareRequest (the prepare op echoed by the participant
// reconciler when the coordinator's row is missing).
func (h *InitiatePreimageSwapFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var transferIDStr string
	switch r := op.(type) {
	case *pbinternal.InitiatePreimageSwapRollbackRequest:
		transferIDStr = r.GetTransferId()
	case *pbinternal.InitiatePreimageSwapPrepareRequest:
		if req := r.GetOriginalRequest(); req != nil {
			transferIDStr = req.GetTransfer().GetTransferId()
		}
	default:
		return fmt.Errorf("unexpected operation type %T for initiate preimage swap rollback", op)
	}
	if transferIDStr == "" {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_id is required for rollback"))
	}
	transferID, err := uuid.Parse(transferIDStr)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}

	transferEnt, err := h.transfer.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("unable to load transfer %s for rollback: %w", transferID, err)
	}

	// Only pre-commit transfers are cancellable (the set executeCancelTransfer
	// accepts). In correct 2PC a rollback is only ever dispatched pre-commit — the
	// participant reconciler synthesizes one only when the coordinator did NOT
	// commit (SP-3195's atomic decision closes the committed-crash window). But a
	// stray/redelivered rollback could still arrive after a successful Commit
	// advanced the transfer to SenderKeyTweaked (non-HODL receive). Absorb those
	// (and terminal statuses) as a logged no-op rather than letting
	// executeCancelTransfer return a plain error — runConsensusRollback only treats
	// AlreadyExists/nil as success, so a plain error would loop the reconciler
	// forever. Mirrors CoopExitFlowHandler.rollbackCoopExit's guard.
	switch transferEnt.Status {
	case st.TransferStatusSenderInitiated,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusSenderInitiatedCoordinator:
		// Cancellable pre-commit — fall through to cancel.
	default:
		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"initiate preimage swap 2pc rollback: transfer %s is at non-cancellable status %s; treating rollback as a no-op",
			transferID, transferEnt.Status)
		return nil
	}

	if err := h.transfer.executeCancelTransfer(ctx, transferEnt); err != nil {
		return fmt.Errorf("unable to cancel transfer %s during rollback: %w", transferID, err)
	}
	logging.GetLoggerFromContext(ctx).Sugar().Infof("initiate preimage swap 2pc rollback: transfer %s marked RETURNED", transferID)
	return nil
}

// ---------------------------------------------------------------------------
// initiatePreimageSwapCoordinatorFlow — coordinator side
// ---------------------------------------------------------------------------

// initiatePreimageSwapCoordinatorFlow drives the coordinator side of
// InitiatePreimageSwapV3 through the 2PC engine. The engine calls Prepare on the
// coordinator too (delegated to the embedded handler); BuildCommitPayload is
// where coordinator-only work lives — aggregating FROST shares (REASON_SEND),
// recovering the preimage (non-HODL RECEIVE), applying the commit locally, and
// building the response.
type initiatePreimageSwapCoordinatorFlow struct {
	*InitiatePreimageSwapFlowHandler

	req              *pbspark.InitiatePreimageSwapRequest
	transferID       uuid.UUID
	isNonHodlReceive bool

	// signingJobsByLeaf is populated only for REASON_SEND with a transfer
	// package — the per-leaf aggregation helpers BuildCommitPayload feeds to
	// AggregateFrost. nil for receive flows (no SO refund signing).
	signingJobsByLeaf map[string]*sendTransferLeafSigningJobs

	// response is populated during BuildCommitPayload so the public
	// InitiatePreimageSwapV3 handler can return it after engine.Execute completes.
	response *pbspark.InitiatePreimageSwapResponse
}

var _ consensus.CoordinatorFlow = (*initiatePreimageSwapCoordinatorFlow)(nil)

// PrepareOp returns the prepare payload fanned out to every SO. The request was
// already mutated by the entrypoint (expiry nulled for non-HODL receive) so
// every SO agrees on the same transfer state.
func (f *initiatePreimageSwapCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.InitiatePreimageSwapPrepareRequest{OriginalRequest: f.req}
}

// BuildCommitPayload aggregates FROST shares (REASON_SEND), recovers the preimage
// (non-HODL RECEIVE), applies the commit on the coordinator's DB so the engine's
// request-tx commit carries the final state, and builds the response.
func (f *initiatePreimageSwapCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	commitReq := &pbinternal.InitiatePreimageSwapCommitRequest{TransferId: f.transferID.String()}

	if f.req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_SEND && f.req.GetTransferRequest() != nil {
		leafSignatures, err := f.aggregateRefundSignatures(ctx, results)
		if err != nil {
			return nil, err
		}
		commitReq.LeafSignatures = leafSignatures
	}

	if f.isNonHodlReceive {
		preimage, err := f.recoverPreimage(results)
		if err != nil {
			return nil, err
		}
		commitReq.Preimage = preimage
		if f.req.GetTransferRequest() != nil {
			proofs, err := f.deriveKeyTweakProofs(ctx)
			if err != nil {
				return nil, err
			}
			commitReq.KeyTweakProofs = proofs
		}
	}

	if err := f.applyInitiatePreimageSwapCommit(ctx, commitReq); err != nil {
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
	f.response = &pbspark.InitiatePreimageSwapResponse{Transfer: transferProto}
	if f.isNonHodlReceive {
		f.response.Preimage = commitReq.GetPreimage()
	}

	return commitReq, nil
}

// RollbackPayload returns the minimal payload sent to participants on rollback.
func (f *initiatePreimageSwapCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: f.transferID.String()}
}

// aggregateRefundSignatures aggregates the per-leaf FROST shares collected from
// every SO's Prepare into the HTLC refund signatures, reusing the SEND flow's
// aggregateLeafSignature. Mirrors sendTransferCoordinatorFlow.BuildCommitPayload.
func (f *initiatePreimageSwapCoordinatorFlow) aggregateRefundSignatures(ctx context.Context, results map[string]*anypb.Any) ([]*pbinternal.SendTransferLeafSignatures, error) {
	allShares, err := collectPreimageSwapFrostShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	frostConn, err := f.config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	leafIDs := slices.Sorted(maps.Keys(f.signingJobsByLeaf))
	leafSignatures := make([]*pbinternal.SendTransferLeafSignatures, 0, len(leafIDs))
	for _, leafID := range leafIDs {
		jobs := f.signingJobsByLeaf[leafID]
		sigs := &pbinternal.SendTransferLeafSignatures{LeafId: leafID}
		if jobs.cpfp != nil {
			sig, _, err := aggregateLeafSignature(ctx, f.config, frostClient, jobs.cpfp, allShares, jobs.leaf, jobs.cpfpUserSig)
			if err != nil {
				return nil, fmt.Errorf("aggregate cpfp signature for leaf %s: %w", leafID, err)
			}
			sigs.RefundSignature = sig
		}
		if jobs.direct != nil {
			sig, _, err := aggregateLeafSignature(ctx, f.config, frostClient, jobs.direct, allShares, jobs.leaf, jobs.directUserSig)
			if err != nil {
				return nil, fmt.Errorf("aggregate direct signature for leaf %s: %w", leafID, err)
			}
			sigs.DirectRefundSignature = sig
		}
		if jobs.dfc != nil {
			sig, _, err := aggregateLeafSignature(ctx, f.config, frostClient, jobs.dfc, allShares, jobs.leaf, jobs.dfcUserSig)
			if err != nil {
				return nil, fmt.Errorf("aggregate direct-from-cpfp signature for leaf %s: %w", leafID, err)
			}
			sigs.DirectFromCpfpRefundSignature = sig
		}
		leafSignatures = append(leafSignatures, sigs)
	}
	return leafSignatures, nil
}

// recoverPreimage reconstructs the payment preimage from the threshold of shares
// returned in the SOs' prepare results and verifies it against the payment hash.
// The share index for each SO is its operator identifier parsed as hex, matching
// the legacy initiatePreimageSwap recovery.
func (f *initiatePreimageSwapCoordinatorFlow) recoverPreimage(results map[string]*anypb.Any) ([]byte, error) {
	var shares []*secretsharing.SecretShare
	for identifier, anyResult := range results {
		if anyResult == nil {
			continue
		}
		resp := &pbinternal.InitiatePreimageSwapPrepareResponse{}
		if err := anyResult.UnmarshalTo(resp); err != nil {
			return nil, fmt.Errorf("unable to unmarshal prepare result from %s: %w", identifier, err)
		}
		if len(resp.GetPreimageShare()) == 0 {
			continue
		}
		index, ok := new(big.Int).SetString(identifier, 16)
		if !ok {
			return nil, fmt.Errorf("unable to parse operator index %q", identifier)
		}
		shares = append(shares, &secretsharing.SecretShare{
			FieldModulus: secp256k1.S256().N,
			Threshold:    int(f.config.Threshold),
			Index:        index,
			Share:        new(big.Int).SetBytes(resp.GetPreimageShare()),
		})
	}

	// Guard the threshold explicitly: if fewer than Threshold SOs contributed a
	// share (e.g., gossip lag, or a participant outside the share set), Lagrange
	// interpolation would still "recover" a wrong secret and only the hash check
	// below would catch it — with a misleading "preimage did not match" error.
	// Surface the real cause here instead.
	if len(shares) < int(f.config.Threshold) {
		return nil, fmt.Errorf("insufficient preimage shares for payment hash %x: got %d, need threshold %d",
			f.req.GetPaymentHash(), len(shares), f.config.Threshold)
	}

	secret, err := secretsharing.RecoverSecret(shares)
	if err != nil {
		return nil, fmt.Errorf("unable to recover preimage for payment hash %x: %w", f.req.GetPaymentHash(), err)
	}
	secretBytes := secret.Bytes()
	if len(secretBytes) < 32 {
		secretBytes = append(make([]byte, 32-len(secretBytes)), secretBytes...)
	}
	hash := sha256.Sum256(secretBytes)
	if !bytes.Equal(hash[:], f.req.GetPaymentHash()) {
		return nil, fmt.Errorf("recovered preimage did not match payment hash %x", f.req.GetPaymentHash())
	}
	return secretBytes, nil
}

// deriveKeyTweakProofs extracts the per-leaf sender-key-tweak proofs from the
// coordinator's transfer_leaf rows (written in self-Prepare), mirroring
// buildProvidePreimageCoordinatorFlow. The proofs are public polynomial
// commitments identical across SOs; every participant re-validates them against
// its own stored tweaks inside CommitSenderKeyTweaks.
func (f *initiatePreimageSwapCoordinatorFlow) deriveKeyTweakProofs(ctx context.Context) (map[string]*pbspark.SecretProof, error) {
	transferEnt, err := f.transfer.loadTransferForUpdate(ctx, f.transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer %s for key tweak proofs: %w", f.transferID, err)
	}
	transferLeaves, err := transferEnt.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer leaves for transfer %s: %w", f.transferID, err)
	}
	proofs := make(map[string]*pbspark.SecretProof, len(transferLeaves))
	for _, leaf := range transferLeaves {
		keyTweakProto := &pbspark.SendLeafKeyTweak{}
		if err := proto.Unmarshal(leaf.KeyTweak, keyTweakProto); err != nil {
			return nil, fmt.Errorf("unable to unmarshal key tweak: %w", err)
		}
		if keyTweakProto.GetSecretShareTweak() == nil {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("secret share tweak missing for leaf %s", keyTweakProto.GetLeafId()))
		}
		proofs[keyTweakProto.GetLeafId()] = &pbspark.SecretProof{
			Proofs: keyTweakProto.GetSecretShareTweak().GetProofs(),
		}
	}
	return proofs, nil
}

// collectPreimageSwapFrostShares unwraps each SO's InitiatePreimageSwapPrepareResponse
// and collects the FROST signature shares keyed jobID → operatorID → share.
// Mirrors collectSignatureShares but for the wrapped response type.
func collectPreimageSwapFrostShares(results map[string]*anypb.Any) (map[string]map[string][]byte, error) {
	allShares := make(map[string]map[string][]byte)
	for opID, anyResult := range results {
		if anyResult == nil {
			continue
		}
		resp := &pbinternal.InitiatePreimageSwapPrepareResponse{}
		if err := anyResult.UnmarshalTo(resp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal prepare result from %s: %w", opID, err)
		}
		frostResp := resp.GetFrostResponse()
		if frostResp == nil {
			continue
		}
		for jobID, sigResult := range frostResp.GetResults() {
			if allShares[jobID] == nil {
				allShares[jobID] = make(map[string][]byte)
			}
			allShares[jobID][opID] = sigResult.GetSignatureShare()
		}
	}
	return allShares, nil
}

// ---------------------------------------------------------------------------
// Coordinator-flow builder
// ---------------------------------------------------------------------------

// buildInitiatePreimageSwapCoordinatorFlow constructs the coordinator flow. For
// REASON_SEND with a transfer package it pre-loads the leaves and builds the
// per-leaf aggregation helpers (non-locking, like buildSendTransferCoordinatorFlow —
// the engine's Prepare re-loads them FOR UPDATE before mutating). isNonHodlReceive
// is resolved by the entrypoint (it already looked up the preimage share to apply
// the expiry mutation).
func buildInitiatePreimageSwapCoordinatorFlow(ctx context.Context, config *so.Config, req *pbspark.InitiatePreimageSwapRequest, isNonHodlReceive bool) (*initiatePreimageSwapCoordinatorFlow, error) {
	transferID, err := uuid.Parse(req.GetTransfer().GetTransferId())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse transfer id: %w", err))
	}

	flow := &initiatePreimageSwapCoordinatorFlow{
		InitiatePreimageSwapFlowHandler: NewInitiatePreimageSwapFlowHandler(config),
		req:                             req,
		transferID:                      transferID,
		isNonHodlReceive:                isNonHodlReceive,
	}

	if req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_SEND && req.GetTransferRequest() != nil {
		pkg := req.GetTransferRequest().GetTransferPackage()
		leafMap, err := preloadLeavesForTransferPackage(ctx, pkg)
		if err != nil {
			return nil, err
		}
		jobsByLeaf, err := buildSendTransferAggregationJobs(ctx, transferID, pkg, leafMap)
		if err != nil {
			return nil, fmt.Errorf("unable to build signing-job helpers: %w", err)
		}
		flow.signingJobsByLeaf = jobsByLeaf
	}

	return flow, nil
}

// preloadLeavesForTransferPackage loads (non-locking) the TreeNode rows for every
// leaf referenced by a transfer package's three refund-tx lists. Mirrors the
// pre-load in buildSendTransferCoordinatorFlow.
//
// The load is intentionally non-locking: the engine's Prepare phase re-loads
// these FOR UPDATE before mutating them, and Prepare's leafAvailableStatus check
// rejects any leaf whose status changed under us. So the worst case here is a
// wasted job-builder pass that the locked re-read in Prepare aborts cleanly — not
// a sighash divergence reaching signing. Locking here would hold row locks on
// every leaf for the entire Prepare RPC fan-out plus FROST aggregation, blocking
// concurrent transfers/claims/exits on the same leaves.
func preloadLeavesForTransferPackage(ctx context.Context, pkg *pbspark.TransferPackage) (map[string]*ent.TreeNode, error) {
	cpfpMap, directMap, dfcMap := loadLeafRefundMapsFromTransferPackage(pkg)
	leafRefundUnion := make(map[string][]byte, len(cpfpMap))
	maps.Copy(leafRefundUnion, cpfpMap)
	maps.Copy(leafRefundUnion, directMap)
	maps.Copy(leafRefundUnion, dfcMap)
	leafUUIDs, err := uuids.ParseSeq(maps.Keys(leafRefundUnion))
	if err != nil {
		return nil, fmt.Errorf("unable to parse leaf IDs for coordinator flow: %w", err)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	leaves, err := db.TreeNode.Query().Where(treenode.IDIn(leafUUIDs...)).WithTree().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to preload leaves for coordinator flow: %w", err)
	}
	if len(leaves) != len(leafRefundUnion) {
		return nil, fmt.Errorf("preload missed leaves: got %d, want %d", len(leaves), len(leafRefundUnion))
	}
	leafMap := make(map[string]*ent.TreeNode, len(leaves))
	for _, leaf := range leaves {
		leafMap[leaf.ID.String()] = leaf
	}
	return leafMap, nil
}

// ---------------------------------------------------------------------------
// Coordinator entrypoint
// ---------------------------------------------------------------------------

// initiatePreimageSwapV3Consensus is the knob-gated 2PC entrypoint for
// InitiatePreimageSwapV3. It performs the coordinator-only checks that need the
// user session (session-identity match, kill-switch, node ownership), applies the
// V3 expiry mutation (non-HODL receive transfers carry no expiry so the
// cancel_expired_transfers task leaves them alone), then drives the flow through
// the engine. Per-SO validation + state creation happens inside Prepare.
func (h *LightningHandler) initiatePreimageSwapV3Consensus(ctx context.Context, req *pbspark.InitiatePreimageSwapRequest) (resp *pbspark.InitiatePreimageSwapResponse, retErr error) {
	if req == nil || req.GetTransfer() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer is required"))
	}

	// Observability parity with the legacy initiatePreimageSwap and the
	// ProvidePreimage consensus path: emit the same lightningFlowInitiatePreimage
	// flow/phase metrics + tracing spans so the existing dashboards keep working
	// when this knob ramps. flowPath is refined during validation once the
	// HODL vs non-HODL receive distinction is known.
	flowStart := time.Now()
	flowPath := lightningFlowPathUnknown
	if req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_SEND {
		flowPath = lightningFlowPathSend
	}
	spanOpt := lightningPaymentHashSpanOption(req.GetPaymentHash())
	ctx, span := tracer.Start(ctx, "LightningHandler.initiatePreimageSwapV3Consensus", spanOpt)
	defer func() {
		endSpanWithError(span, retErr)
		observeLightningFlow(ctx, lightningFlowInitiatePreimage, flowPath, flowStart, retErr)
	}()

	var (
		preimageShare    *ent.PreimageShare
		isNonHodlReceive bool
	)
	phaseStart := time.Now()
	validateCtx, validateSpan := tracer.Start(ctx, "LightningHandler.initiatePreimageSwapV3Consensus.validate", spanOpt)
	validateErr := func() error {
		ownerIdentityPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetOwnerIdentityPublicKey())
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse owner identity public key: %w", err))
		}
		if err := authz.EnforceSessionIdentityPublicKeyMatches(validateCtx, h.config, ownerIdentityPubKey); err != nil {
			return err
		}
		if err := authz.EnforceWalletNotKillSwitched(validateCtx, ownerIdentityPubKey); err != nil {
			return err
		}
		if len(req.GetTransfer().GetLeavesToSend()) == 0 {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("at least one cpfp leaf tx must be provided"))
		}
		if req.GetTransfer().GetReceiverIdentityPublicKey() == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("receiver identity public key is required"))
		}

		// Resolve the preimage share once (non-HODL vs HODL receive) and apply the
		// expiry mutation, mirroring legacy initiatePreimageSwap. For V3 there is no
		// positive expiry override; only the non-HODL-receive "no expiry" rule applies.
		if req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE {
			db, err := ent.GetDbFromContext(validateCtx)
			if err != nil {
				return fmt.Errorf("unable to get db context: %w", err)
			}
			preimageShare, err = db.PreimageShare.Query().Where(preimageshare.PaymentHash(req.GetPaymentHash())).First(validateCtx)
			if err != nil {
				if !ent.IsNotFound(err) {
					return fmt.Errorf("unable to get preimage share for payment hash %x: %w", req.GetPaymentHash(), err)
				}
				preimageShare = nil
				flowPath = lightningFlowPathReceiveHodl
				if knobs.GetKnobsService(validateCtx).GetValue(knobs.KnobShutdownHodlInvoices, 0) > 0 {
					return sparkerrors.UnavailableMethodDisabled(fmt.Errorf("hodl invoices are currently disabled"))
				}
			} else {
				flowPath = lightningFlowPathReceiveNonHodl
				// non-HODL receive: strip expiry so the transfer is not cancelled by
				// the cancel_expired_transfers task.
				req.Transfer.ExpiryTime = nil
				if req.GetTransferRequest() != nil {
					req.TransferRequest.ExpiryTime = nil
				}
			}
		}
		isNonHodlReceive = req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE && preimageShare != nil

		// Reject an already-expired expiry up front, matching legacy initiatePreimageSwap.
		// createTransfer re-checks this inside Prepare, but failing here gives the
		// client the same early InvalidArgument and avoids fanning out a doomed flow.
		expiryTime := req.GetTransfer().GetExpiryTime().AsTime()
		if expiryTime.Unix() != 0 && expiryTime.Before(time.Now()) {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("expiry time is before current time"))
		}

		// Coordinator-only node ownership check (session-based; participants run
		// validateNodeOwnership=false in Prepare). Loads the cpfp leaves non-locking
		// — Prepare re-loads them FOR UPDATE before mutating.
		if err := h.validateLeafOwnershipForPreimageSwap(validateCtx, req.GetTransfer().GetLeavesToSend()); err != nil {
			return err
		}
		return nil
	}()
	endSpanWithError(validateSpan, validateErr)
	observeLightningPhase(ctx, lightningFlowInitiatePreimage, lightningPhaseValidate, phaseStart, validateErr)
	if validateErr != nil {
		return nil, validateErr
	}

	flow, err := buildInitiatePreimageSwapCoordinatorFlow(ctx, h.config, req, isNonHodlReceive)
	if err != nil {
		return nil, fmt.Errorf("unable to build coordinator flow: %w", err)
	}

	phaseStart = time.Now()
	consensusCtx, consensusSpan := tracer.Start(ctx, "LightningHandler.initiatePreimageSwapV3Consensus.execute", spanOpt)
	execErr := func() error {
		engine, err := consensus.GetEngine(consensusCtx)
		if err != nil {
			return err
		}
		selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
		if _, err := engine.Execute(consensusCtx,
			pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_INITIATE_PREIMAGE_SWAP,
			&selection, flow); err != nil {
			return fmt.Errorf("consensus initiate preimage swap failed: %w", err)
		}
		return nil
	}()
	endSpanWithError(consensusSpan, execErr)
	observeLightningPhase(ctx, lightningFlowInitiatePreimage, lightningPhaseConsensusExecute, phaseStart, execErr)
	if execErr != nil {
		return nil, execErr
	}

	if flow.response == nil {
		return nil, fmt.Errorf("initiate preimage swap consensus completed without building a response for transfer %s", flow.transferID)
	}

	// Partner attribution, mirroring legacy. Best-effort; failures are logged.
	if req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_SEND {
		partner.SaveTransferPartner(ctx, flow.transferID, st.TransferPartnerTypeLightningSend)
	} else if isNonHodlReceive {
		baseHandler := NewBaseTransferHandler(h.config)
		transferEnt, err := baseHandler.loadTransferNoUpdate(ctx, flow.transferID)
		if err != nil {
			logging.GetLoggerFromContext(ctx).Sugar().Warnf("failed to load transfer %s for partner attribution: %v", flow.transferID, err)
		} else if err := saveTransferPartnerFromPreimageShare(ctx, preimageShare, transferEnt); err != nil {
			logging.GetLoggerFromContext(ctx).Sugar().Warnf("failed to save transfer partner attribution for transfer %s: %v", flow.transferID, err)
		}
	}

	return flow.response, nil
}

// validateLeafOwnershipForPreimageSwap loads the cpfp leaves and runs the
// session-based node-ownership check on the coordinator. This is the one
// validation that cannot move into Prepare (it compares against the user session,
// which participants don't have).
func (h *LightningHandler) validateLeafOwnershipForPreimageSwap(ctx context.Context, cpfpLeaves []*pbspark.UserSignedTxSigningJob) error {
	leafIDs := make([]uuid.UUID, 0, len(cpfpLeaves))
	for _, leaf := range cpfpLeaves {
		id, err := uuid.Parse(leaf.GetLeafId())
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid leaf id %q: %w", leaf.GetLeafId(), err))
		}
		leafIDs = append(leafIDs, id)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get db context: %w", err)
	}
	nodes, err := db.TreeNode.Query().Where(treenode.IDIn(leafIDs...)).All(ctx)
	if err != nil {
		return fmt.Errorf("unable to load leaves for ownership check: %w", err)
	}
	// IDIn silently drops ids that don't exist, so a request referencing a
	// nonexistent (or duplicate) leaf would otherwise slip past this gate and
	// only fail later inside Prepare — after the engine flow has started. Surface
	// it up front, matching the legacy path where missing nodes failed early in
	// ValidateGetPreimageRequest. (Duplicate leaf ids also trip this; they are
	// invalid anyway, and ValidateDuplicateLeaves only runs later in Prepare.)
	if len(nodes) != len(leafIDs) {
		return sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("preimage swap references %d leaves but only %d exist (nonexistent or duplicate leaf id)", len(leafIDs), len(nodes)))
	}
	return h.validateNodeOwnership(ctx, nodes)
}
