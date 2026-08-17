package handler

import (
	"context"
	"fmt"
	"maps"

	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	entutxo "github.com/lightsparkdev/spark/so/ent/utxo"
	entutxoswap "github.com/lightsparkdev/spark/so/ent/utxoswap"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"github.com/lightsparkdev/spark/so/staticdeposit"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// StaticDepositUtxoSwapFlowHandler — participant side (Prepare/Commit/Rollback)
// ---------------------------------------------------------------------------

// StaticDepositUtxoSwapFlowHandler implements consensus.FlowHandler for the fixed-amount
// static deposit claim. It delegates the
// SSP→user transfer to an embedded SendTransferFlowHandler and signs the deposit-UTXO spend
// via the refund flow's round-1-prefetch pattern. Commit sends the transfer before completing
// the swap; Rollback returns it before cancelling (CancelUtxoSwap refuses a sent transfer, SP-3261).
type StaticDepositUtxoSwapFlowHandler struct {
	config *so.Config
	// transfer carries the nested SSP→user transfer through every phase.
	transfer *SendTransferFlowHandler
}

var _ consensus.FlowHandler = (*StaticDepositUtxoSwapFlowHandler)(nil)

func NewStaticDepositUtxoSwapFlowHandler(config *so.Config) *StaticDepositUtxoSwapFlowHandler {
	return &StaticDepositUtxoSwapFlowHandler{
		config:   config,
		transfer: NewSendTransferFlowHandlerForType(config, st.TransferTypeUtxoSwap, st.TransferPartnerTypeDeposit, false /* requireDirectRefunds */),
	}
}

// staticDepositSwapJobNamespace is a fixed UUIDv4 mixed into NewSHA1 to derive a
// deterministic signing-job id for the spend tx from the utxo, so every SO and
// the coordinator correlate the round-2 shares without sending the job id over
// the wire. Distinct from staticDepositRefundJobNamespace and
// sendTransferSigningJobNamespace so job ids can never collide across flows —
// the prepare-result share map mixes this job with the transfer's leaf jobs.
var staticDepositSwapJobNamespace = uuid.MustParse("5f2a9c81-6d4b-47e3-8c0a-92e7b1d3f6a4")

// txid is the raw bytes from the proto UTXO field. Both callers — the spend-tx
// round-2 job on each SO and BuildCommitPayload on the coordinator — pass the
// same proto field, so the derived id always agrees. Identical across retry
// attempts for the same UTXO by design: shares are only correlated within a
// single Execute's results map (see staticDepositRefundJobID).
func staticDepositSwapJobID(txid []byte, vout uint32) uuid.UUID {
	return uuid.NewSHA1(staticDepositSwapJobNamespace, fmt.Appendf(nil, "%x:%d", txid, vout))
}

// Prepare runs on every SO. It validates the swap request and creates the
// UtxoSwap CREATED row (the work the legacy CreateStaticDepositUtxoSwap fanout
// does on each SO), delegates the transfer creation to the embedded
// send-transfer Prepare (Transfer + TransferLeaf rows at SenderKeyTweakPending,
// leaf-refund round-2 shares), and — for SOs in the coordinator-collected
// round-1 set — produces the FROST round-2 share over the spend-tx sighash.
// The prepare result is the merged share map (leaf jobs + spend-tx job).
func (h *StaticDepositUtxoSwapFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	prepareReq, ok := op.(*pbinternal.StaticDepositUtxoSwapPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for static deposit utxo swap prepare", op)
	}
	req := prepareReq.GetOriginalRequest()
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}

	// Swap row before transfer rows, matching legacy ordering: the unique
	// active-swap slot on the utxo is the cross-request race arbiter, so claim
	// it before doing the heavier transfer work.
	depositAddress, err := h.createFixedSwap(ctx, req)
	if err != nil {
		return nil, err
	}

	transferResult, err := h.transfer.Prepare(ctx, &pbinternal.SendTransferPrepareRequest{
		OriginalRequest:      convertV2ToV3SendTransferRequest(req.GetTransfer()),
		SenderKeyTweakProofs: prepareReq.GetSenderKeyTweakProofs(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to prepare utxo swap transfer: %w", err)
	}

	merged := make(map[string]*pbcommon.SigningResult)
	if transferResult != nil {
		transferShares, ok := transferResult.(*pbinternal.FrostRound2Response)
		if !ok {
			return nil, fmt.Errorf("unexpected transfer prepare result type %T", transferResult)
		}
		maps.Copy(merged, transferShares.GetResults())
	}

	// Only SOs in the coordinator's round-1 signing set produce a spend-tx
	// round-2 share; job ids are namespaced per flow so the merge cannot collide
	// with the transfer's leaf jobs.
	if _, inSigningSet := prepareReq.GetSpendTxSigningCommitments()[h.config.Identifier]; inSigningSet {
		job, err := h.buildSpendTxRound2Job(ctx, depositAddress, req, prepareReq.GetSpendTxSigningCommitments())
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

// createFixedSwap mirrors the participant body of the legacy
// CreateStaticDepositUtxoSwap (static_deposit_internal_handler.go) — every
// validation it performs is preserved. Legacy's two coordinator checks are
// replaced the same way the refund flow replaced them: the ECDSA-over-statement
// signature is subsumed by the authenticated ConsensusPrepare channel, and the
// coordinator identity stored on the row is derived from the engine's
// coordinator_index instead of a self-declared payload field. The user
// signature (the real authorization, binding the fixed amount to the utxo) is
// still verified on every SO.
func (h *StaticDepositUtxoSwapFlowHandler) createFixedSwap(ctx context.Context, req *pbinternal.InitiateStaticDepositUtxoSwapRequest) (*ent.DepositAddress, error) {
	if req.GetOnChainUtxo() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("on_chain_utxo is required"))
	}
	if req.GetTransfer() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer is required"))
	}
	if req.GetTransfer().GetTransferPackage() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_package is required"))
	}
	if req.GetSpendTxSigningJob() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("spend_tx_signing_job is required"))
	}
	// Validate the SSP's spend-tx nonce commitment here in Prepare (on every SO)
	// so a missing or malformed commitment fails Prepare deterministically —
	// BuildCommitPayload parses this same commitment to assemble the
	// SigningResult, and a commit-side parse failure would roll back an
	// otherwise-prepared flow.
	if req.GetSpendTxSigningJob().GetSigningNonceCommitment() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("spend_tx_signing_job.signing_nonce_commitment is required"))
	}
	if err := (&frost.SigningCommitment{}).UnmarshalProto(req.GetSpendTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid spend tx signing nonce commitment: %w", err))
	}

	network, err := btcnetwork.FromProtoNetwork(req.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, fmt.Errorf("unable to parse network: %w", err)
	}
	if !h.config.IsNetworkSupported(network) {
		return nil, fmt.Errorf("network %s not supported", network)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}

	confirmationThreshold := req.ConfirmationThreshold
	if confirmationThreshold != nil && *confirmationThreshold == 0 {
		return nil, sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("confirmation_threshold must be at least 1"))
	}
	threshold := resolveConfirmationThreshold(confirmationThreshold, h.config, network)
	targetUtxo, err := VerifiedTargetUtxoFromRequest(ctx, h.config, db, network, req.GetOnChainUtxo(), &threshold)
	if err != nil {
		return nil, err
	}
	if err := validateStaticDepositSpendTxSpendsTargetUtxo(targetUtxo, req.GetSpendTxSigningJob().GetRawTx()); err != nil {
		return nil, err
	}

	existingSwap, err := staticdeposit.GetRegisteredUtxoSwapForUtxo(ctx, db, targetUtxo.inner)
	if err != nil {
		return nil, fmt.Errorf("unable to check if utxo swap is already registered: %w", err)
	}
	if existingSwap != nil {
		return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("utxo swap is already registered"))
	}

	depositAddress, err := targetUtxo.inner.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo deposit address: %w", err)
	}
	if !depositAddress.IsStatic {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("unable to claim a deposit to a non-static address"))
	}
	receiverIdentityPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetReceiverIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse transfer receiver public key: %w", err))
	}
	if !depositAddress.OwnerIdentityPubkey.Equals(receiverIdentityPubKey) {
		return nil, fmt.Errorf("transfer is not to the recipient of the deposit")
	}
	spendTxSigningPubKey, err := keys.ParsePublicKey(req.GetSpendTxSigningJob().GetSigningPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse spend signing public key: %w", err))
	}
	if !depositAddress.OwnerSigningPubkey.Equals(spendTxSigningPubKey) {
		return nil, fmt.Errorf("deposit address owner signing pubkey does not match the signing public key")
	}

	if err := validateTransfer(req.GetTransfer()); err != nil {
		return nil, fmt.Errorf("transfer validation failed: %w", err)
	}

	ownerIdentityPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner identity public key: %w", err))
	}
	transferID, err := uuid.Parse(req.GetTransfer().GetTransferId())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse transfer_id as a uuid: %w", err))
	}

	// The fixed-amount user signature binds the exact credit amount, so it can
	// only be checked against the sum of the actual leaves. Non-locking read:
	// leaf values are immutable, and the embedded send-transfer Prepare that
	// runs right after this takes the authoritative FOR UPDATE locks +
	// availability checks in the same tx before any state change — locking here
	// would just issue a second locking query against the identical rows.
	leafRefundMap := make(map[string][]byte)
	for _, leaf := range req.GetTransfer().GetTransferPackage().GetLeavesToSend() {
		leafRefundMap[leaf.GetLeafId()] = leaf.GetRawTx()
	}
	leaves, transferNetwork, err := loadLeaves(ctx, db, leafRefundMap, false)
	if err != nil {
		return nil, fmt.Errorf("unable to load leaves: %w", err)
	}
	if len(leaves) == 0 {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no leaves found"))
	}
	if transferNetwork != network {
		return nil, fmt.Errorf("transfer network %s does not match utxo network %s", transferNetwork, network)
	}
	totalAmount := getTotalTransferValue(leaves)
	if err := validateUserSignature(receiverIdentityPubKey, req.GetUserSignature(), req.GetSspSignature(), pbspark.UtxoSwapRequestType_Fixed, network, targetUtxo.Hash().String(), targetUtxo.Vout(), totalAmount, req.GetHashVariant()); err != nil {
		return nil, fmt.Errorf("user signature validation failed: %w", err)
	}
	if totalAmount > targetUtxo.inner.Amount {
		return nil, fmt.Errorf("static deposit claim total amount %d is greater than utxo amount %d for utxo %s:%d", totalAmount, targetUtxo.inner.Amount, targetUtxo.Hash(), targetUtxo.Vout())
	}

	coordinatorPubKey := h.coordinatorIdentityForSwap(ctx)

	utxoSwap, err := db.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetUtxo(targetUtxo.inner).
		SetUtxoValueSats(targetUtxo.inner.Amount).
		SetRequestType(st.UtxoSwapFromProtoRequestType(pbspark.UtxoSwapRequestType_Fixed)).
		SetCreditAmountSats(totalAmount).
		SetSspSignature(req.GetSspSignature()).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		SetUserSignature(req.GetUserSignature()).
		SetUserIdentityPublicKey(receiverIdentityPubKey).
		SetCoordinatorIdentityPublicKey(coordinatorPubKey).
		SetRequestedTransferID(transferID).
		SetConsensusManaged(true).
		Save(ctx)
	if err != nil {
		if sqlgraph.IsUniqueConstraintError(err) {
			return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("utxo swap already exists: %w", err))
		}
		return nil, fmt.Errorf("unable to store utxo swap: %w", err)
	}
	if err := addUtxoSwapToDepositAddress(ctx, db, depositAddress.ID, utxoSwap); err != nil {
		return nil, err
	}
	return depositAddress, nil
}

// coordinatorIdentityForSwap mirrors the refund flow's helper: on a participant
// the identity comes from ctx (attached by DispatchPrepare after resolving the
// engine's coordinator_index, which fails closed on an unresolvable index and
// rejects an index naming the receiving SO); a missing ctx value can only mean
// the coordinator's own self-Prepare, where this SO's own key is correct by
// definition.
func (h *StaticDepositUtxoSwapFlowHandler) coordinatorIdentityForSwap(ctx context.Context) keys.Public {
	if coordinatorPubKey, ok := consensus.CoordinatorIdentityFromContext(ctx); ok {
		return coordinatorPubKey
	}
	return h.config.IdentityPublicKey()
}

// buildSpendTxRound2Job constructs the FROST round-2 job for this SO's share
// over the spend-tx sighash. Mirrors the refund flow's buildRefundRound2Job and
// getSpendTxSigningResult's key material (verifyingKey =
// signingKeyshare.PublicKey + depositAddress.OwnerSigningPubkey).
func (h *StaticDepositUtxoSwapFlowHandler) buildSpendTxRound2Job(ctx context.Context, depositAddress *ent.DepositAddress, req *pbinternal.InitiateStaticDepositUtxoSwapRequest, round1 map[string]*pbcommon.SigningCommitment) (*pbinternal.SigningJob, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	network, err := btcnetwork.FromProtoNetwork(req.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, fmt.Errorf("unable to parse network: %w", err)
	}
	targetUtxo, err := db.Utxo.Query().
		Where(entutxo.NetworkEQ(network), entutxo.Txid(req.GetOnChainUtxo().GetTxid()), entutxo.Vout(req.GetOnChainUtxo().GetVout())).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load utxo for spend tx job: %w", err)
	}
	spendTxSighash, _, err := GetTxSigningInfo(ctx, targetUtxo, req.GetSpendTxSigningJob().GetRawTx())
	if err != nil {
		return nil, fmt.Errorf("failed to get spend tx sighash: %w", err)
	}
	signingKeyshare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}
	verifyingKey := signingKeyshare.PublicKey.Add(depositAddress.OwnerSigningPubkey)
	jobID := staticDepositSwapJobID(req.GetOnChainUtxo().GetTxid(), req.GetOnChainUtxo().GetVout())
	return &pbinternal.SigningJob{
		JobId:           jobID.String(),
		Message:         spendTxSighash.Serialize(),
		KeyshareId:      signingKeyshare.ID.String(),
		VerifyingKey:    verifyingKey.Serialize(),
		Commitments:     round1,
		UserCommitments: req.GetSpendTxSigningJob().GetSigningNonceCommitment(),
	}, nil
}

// Commit is apply-only (no validation — all in Prepare): it applies the transfer commit
// (→ SENDER_KEY_TWEAKED) then completes the swap, the order CompleteUtxoSwap requires.
// The whole handler runs in one DB tx, so the tweak does not persist unless the swap also
// completes — a swap cancelled mid-flight rolls the tweak back too (no "cancelled + tweaked"
// state, only a recoverable IN_FLIGHT wedge; gated by the drain + SP-3495 fence). Idempotent.
func (h *StaticDepositUtxoSwapFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.StaticDepositUtxoSwapCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for static deposit utxo swap commit", op)
	}
	if req.GetTransferCommit() == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_commit is required"))
	}
	if err := h.transfer.applySendTransferCommit(ctx, req.GetTransferCommit()); err != nil {
		return fmt.Errorf("failed to apply utxo swap transfer commit: %w", err)
	}
	return h.completeFixedSwap(ctx, req.GetOnChainUtxo())
}

func (h *StaticDepositUtxoSwapFlowHandler) completeFixedSwap(ctx context.Context, utxo *pbspark.UTXO) error {
	swap, err := h.loadFixedSwapForUtxo(ctx, utxo)
	if err != nil {
		return err
	}
	if swap == nil {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("no registered utxo swap to complete for %x:%d", utxo.GetTxid(), utxo.GetVout()))
	}
	if swap.Status == st.UtxoSwapStatusCompleted {
		logging.GetLoggerFromContext(ctx).Sugar().Infof("static deposit swap 2pc commit: swap %s already COMPLETED, idempotent retry", swap.ID)
		return nil
	}
	if err := CompleteUtxoSwap(ctx, swap); err != nil {
		return fmt.Errorf("unable to complete utxo swap %s: %w", swap.ID, err)
	}
	return nil
}

// Rollback returns this SO's transfer (→ RETURNED) then cancels the swap — the order
// CancelUtxoSwap's SP-3261 guard requires (refuses a sent transfer, fails closed when
// unreadable). The transfer id comes from the row's own requested_transfer_id, never the
// coordinator payload. Idempotent (never-created/cancelled/COMPLETED swaps are no-ops);
// accepts both the rollback payload and the reconciler-echoed prepare op.
func (h *StaticDepositUtxoSwapFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var utxo *pbspark.UTXO
	switch r := op.(type) {
	case *pbinternal.StaticDepositUtxoSwapRollbackRequest:
		utxo = r.GetOnChainUtxo()
	case *pbinternal.StaticDepositUtxoSwapPrepareRequest:
		utxo = r.GetOriginalRequest().GetOnChainUtxo()
	default:
		return fmt.Errorf("unexpected operation type %T for static deposit utxo swap rollback", op)
	}
	if utxo == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("on_chain_utxo is required for rollback"))
	}

	swap, err := h.loadFixedSwapForUtxo(ctx, utxo)
	if err != nil {
		return err
	}
	logger := logging.GetLoggerFromContext(ctx)
	if swap == nil {
		// Prepare creates the swap row strictly before the transfer, so no
		// active swap row also means no transfer of ours to roll back.
		logger.Sugar().Infof("static deposit swap 2pc rollback: no active swap for %x:%d, no-op", utxo.GetTxid(), utxo.GetVout())
		return nil
	}
	if swap.Status == st.UtxoSwapStatusCompleted {
		logger.Sugar().Infof("static deposit swap 2pc rollback: swap %s already COMPLETED, no-op", swap.ID)
		return nil
	}

	// The transfer to roll back is derived from this SO's own swap row —
	// written by its own Prepare — never from the coordinator-authored payload,
	// so a buggy or compromised coordinator cannot name an unrelated transfer
	// and have it returned.
	if swap.RequestedTransferID != uuid.Nil {
		if err := h.transfer.rollbackSendTransfer(ctx, swap.RequestedTransferID); err != nil {
			return fmt.Errorf("failed to roll back utxo swap transfer %s: %w", swap.RequestedTransferID, err)
		}
	}

	if err := CancelUtxoSwap(ctx, swap); err != nil {
		return fmt.Errorf("unable to cancel utxo swap %s during rollback: %w", swap.ID, err)
	}
	logger.Sugar().Infof("static deposit swap 2pc rollback: swap %s marked CANCELLED", swap.ID)
	return nil
}

// loadFixedSwapForUtxo finds the active (CREATED/COMPLETED) FIXED UtxoSwap for
// the utxo, or nil if the utxo or an active fixed swap doesn't exist. Filtering
// to RequestType Fixed means Commit/Rollback can only ever touch a row this
// flow type created — a different-type swap that claims the freed active slot
// after a cancellation is invisible here rather than completed/cancelled by
// mistake (see the refund flow's loadSwapForUtxo). It does NOT re-check
// on-chain confirmation: completing or cancelling an already-prepared swap must
// not depend on the current confirmation state.
func (h *StaticDepositUtxoSwapFlowHandler) loadFixedSwapForUtxo(ctx context.Context, utxo *pbspark.UTXO) (*ent.UtxoSwap, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	network, err := btcnetwork.FromProtoNetwork(utxo.GetNetwork())
	if err != nil {
		return nil, fmt.Errorf("unable to parse network: %w", err)
	}
	targetUtxo, err := db.Utxo.Query().
		Where(entutxo.NetworkEQ(network), entutxo.Txid(utxo.GetTxid()), entutxo.Vout(utxo.GetVout())).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("unable to load utxo %x:%d: %w", utxo.GetTxid(), utxo.GetVout(), err)
	}
	// Not scoped by consensus_managed: during a rolling deploy a consensus row prepared by an older
	// binary predates the flag (false), and this SO's own Commit/Rollback must still find it. The
	// legacy rollback fence handles the reverse (a stray legacy rollback off a consensus row).
	swap, err := db.UtxoSwap.Query().
		Where(
			entutxoswap.HasUtxoWith(entutxo.IDEQ(targetUtxo.ID)),
			entutxoswap.StatusIn(st.UtxoSwapStatusCreated, st.UtxoSwapStatusCompleted),
			entutxoswap.RequestTypeEQ(st.UtxoSwapRequestTypeFixedAmount),
		).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	return swap, nil
}
