package handler

import (
	"context"
	"fmt"

	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	entutxoswap "github.com/lightsparkdev/spark/so/ent/utxoswap"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	transferHelper "github.com/lightsparkdev/spark/so/transfer"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// ReserveInstantStaticDepositFlowHandler — participant side
// ---------------------------------------------------------------------------

// ReserveInstantStaticDepositFlowHandler implements consensus.FlowHandler for phase one of the
// instant static deposit claim (gated on KnobUseConsensusReserveInstantStaticDepositUtxoSwap).
// Simpler than the fixed-amount swap: no spend-tx FROST signing (the UTXO may be unconfirmed;
// the spend is signed at claim time) and the swap row STAYS CREATED at commit — CREATED-with-
// transfer is the reserved state the claim phase consumes. Delegates the SSP→user transfer to
// the same embedded SendTransferFlowHandler and links the transfer edge onto its row in Prepare.
type ReserveInstantStaticDepositFlowHandler struct {
	config *so.Config
	// transfer carries the nested SSP→user transfer through every phase.
	transfer *SendTransferFlowHandler
}

var _ consensus.FlowHandler = (*ReserveInstantStaticDepositFlowHandler)(nil)

func NewReserveInstantStaticDepositFlowHandler(config *so.Config) *ReserveInstantStaticDepositFlowHandler {
	return &ReserveInstantStaticDepositFlowHandler{
		config:   config,
		transfer: NewSendTransferFlowHandlerForType(config, st.TransferTypeUtxoSwap, st.TransferPartnerTypeDeposit, false /* requireDirectRefunds */),
	}
}

// Prepare runs on every SO. It validates the reservation and creates the
// INSTANT UtxoSwap row in CREATED (the work the legacy
// CreateInstantStaticDepositUtxoSwap fanout does on each SO), delegates the
// transfer creation to the embedded send-transfer Prepare, and links the
// transfer edge onto this SO's own swap row. The prepare result is the
// transfer's FROST round-2 leaf-refund share map (there is no spend-tx job at
// reserve time).
func (h *ReserveInstantStaticDepositFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	prepareReq, ok := op.(*pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for reserve instant static deposit prepare", op)
	}
	req := prepareReq.GetOriginalRequest()
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}

	// Swap row before transfer rows, matching the fixed swap and legacy
	// ordering: the partial unique index on (deposit_address, utxo_value_sats)
	// over non-terminal rows is the cross-request race arbiter, so claim the
	// slot before doing the heavier transfer work.
	swap, err := h.createInstantReserveSwap(ctx, req)
	if err != nil {
		return nil, err
	}

	transferResult, err := h.transfer.Prepare(ctx, &pbinternal.SendTransferPrepareRequest{
		OriginalRequest: convertV2ToV3SendTransferRequest(req.GetTransfer()),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to prepare instant reserve transfer: %w", err)
	}

	// Link the transfer edge on this SO's own row now that both rows exist in
	// this tx — this replaces the legacy LinkUtxoSwapTransfer fanout (which was
	// best-effort and coordinator-driven) with an atomic, local write.
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	transferEnt, err := db.Transfer.Get(ctx, swap.RequestedTransferID)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer %s to link to instant swap: %w", swap.RequestedTransferID, err)
	}
	if _, err := swap.Update().SetTransfer(transferEnt).Save(ctx); err != nil {
		return nil, fmt.Errorf("unable to link transfer to instant swap %s: %w", swap.ID, err)
	}

	return transferResult, nil
}

// createInstantReserveSwap mirrors the participant body of the legacy
// CreateInstantStaticDepositUtxoSwap (static_deposit_internal_handler.go) —
// every validation it performs is preserved. Legacy's two coordinator checks
// are replaced the same way the fixed swap and refund flows replaced them: the
// ECDSA-over-statement signature is subsumed by the authenticated
// ConsensusPrepare channel, and the coordinator identity stored on the row is
// derived from the engine's coordinator_index. The instant user signature (the
// real authorization, binding destination address, value, and credit amounts)
// is still verified on every SO.
func (h *ReserveInstantStaticDepositFlowHandler) createInstantReserveSwap(ctx context.Context, req *pbinternal.ReserveInstantStaticDepositUtxoSwapRequest) (*ent.UtxoSwap, error) {
	if req.GetOnChainUtxo() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("on_chain_utxo is required"))
	}
	if req.GetTransfer() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer is required"))
	}
	if req.GetTransfer().GetTransferPackage() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_package is required"))
	}

	network, err := btcnetwork.FromProtoNetwork(req.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, fmt.Errorf("unable to parse network: %w", err)
	}
	if !h.config.IsNetworkSupported(network) {
		return nil, fmt.Errorf("network %s not supported", network)
	}

	if req.GetValueSats() <= 0 || req.GetCreditAmountSats() < 0 || req.GetSecondaryCreditAmountSats() < 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("amounts must be non-negative and value_sats must be positive"))
	}
	// Compare each term against the remaining headroom rather than summing first: the two
	// caller-supplied int64s could overflow to a negative sum that slips past a `> value_sats` cap.
	credit, secondary := req.GetCreditAmountSats(), req.GetSecondaryCreditAmountSats()
	if credit > req.GetValueSats() || secondary > req.GetValueSats()-credit {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("total credit_amount_sats (%d + %d) exceeds value_sats (%d)", credit, secondary, req.GetValueSats()))
	}
	if req.GetSecondaryCreditAmountSats() == 0 && req.GetRequestedSecondaryTransferId() != "" {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("requested_secondary_transfer_id provided without secondary_credit_amount_sats"))
	}
	if req.GetSecondaryCreditAmountSats() > 0 && req.GetRequestedSecondaryTransferId() == "" {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("secondary_credit_amount_sats provided without requested_secondary_transfer_id"))
	}

	transferID, err := uuid.Parse(req.GetTransfer().GetTransferId())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse transfer_id as a uuid: %w", err))
	}
	ownerIdentityPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner identity public key: %w", err))
	}
	receiverIdentityPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetReceiverIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse transfer receiver public key: %w", err))
	}

	// Note: the transfer package is not validated here — the embedded
	// h.transfer.Prepare (invoked right after createInstantReserveSwap in
	// Prepare) runs ValidateTransferPackage itself and is the authoritative
	// check. Duplicating it (as the legacy participant did) would repeat the
	// ECDSA/key-tweak verification on every SO for no benefit; the fixed-swap
	// sibling omits it for the same reason.

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}

	// The deposit address is located by the request's address string — the UTXO
	// may be unconfirmed at reserve time, so there is no Utxo row to reach it
	// through (this is also why the swap row is created without a Utxo edge).
	depositAddress, err := db.DepositAddress.Query().
		Where(
			depositaddress.Address(req.GetDestinationAddress()),
			depositaddress.OwnerIdentityPubkey(receiverIdentityPubKey),
			depositaddress.IsStatic(true),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("deposit address %s not found", req.GetDestinationAddress()))
	}
	if err != nil {
		return nil, fmt.Errorf("unable to get deposit address: %w", err)
	}
	if depositAddress.Network != btcnetwork.Unspecified && depositAddress.Network != network {
		return nil, fmt.Errorf("deposit address network %s does not match utxo network %s", depositAddress.Network, network)
	}

	if err := validateTransfer(req.GetTransfer()); err != nil {
		return nil, fmt.Errorf("transfer validation failed: %w", err)
	}

	// Non-locking read: this load only sums immutable leaf values for the
	// instant user-signature check, and the embedded send-transfer Prepare that
	// runs right after takes the authoritative FOR UPDATE locks + availability
	// checks in the same tx before any state change.
	leafRefundMap, _, _ := loadLeafRefundMaps(req.GetTransfer())
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
	if totalAmount != uint64(req.GetCreditAmountSats()) {
		return nil, fmt.Errorf("instant static deposit total leaf amount %d does not match credit_amount_sats %d", totalAmount, req.GetCreditAmountSats())
	}
	if err := validateInstantUserSignature(
		receiverIdentityPubKey,
		req.GetUserSignature(),
		req.GetSspSignature(),
		network,
		totalAmount,
		uint64(req.GetSecondaryCreditAmountSats()),
		req.GetDestinationAddress(),
		uint64(req.GetValueSats()),
	); err != nil {
		return nil, fmt.Errorf("user signature validation failed: %w", err)
	}

	coordinatorPubKey := h.coordinatorIdentityForSwap(ctx)

	utxoSwapCreate := db.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetUtxoValueSats(uint64(req.GetValueSats())).
		SetCreditAmountSats(uint64(req.GetCreditAmountSats())).
		SetSspSignature(req.GetSspSignature()).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		SetUserSignature(req.GetUserSignature()).
		SetUserIdentityPublicKey(receiverIdentityPubKey).
		SetCoordinatorIdentityPublicKey(coordinatorPubKey).
		SetRequestedTransferID(transferID)
	if req.GetSecondaryCreditAmountSats() > 0 {
		utxoSwapCreate = utxoSwapCreate.SetSecondaryCreditAmountSats(uint64(req.GetSecondaryCreditAmountSats()))
	}
	if req.GetRequestedSecondaryTransferId() != "" {
		secondaryTransferID, err := uuid.Parse(req.GetRequestedSecondaryTransferId())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid requested_secondary_transfer_id: %w", err))
		}
		utxoSwapCreate = utxoSwapCreate.SetRequestedSecondaryTransferID(secondaryTransferID)
	}

	utxoSwap, err := utxoSwapCreate.Save(ctx)
	if err != nil {
		if sqlgraph.IsUniqueConstraintError(err) {
			return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("instant utxo swap already exists: %w", err))
		}
		return nil, fmt.Errorf("unable to store instant utxo swap: %w", err)
	}
	if err := addUtxoSwapToDepositAddress(ctx, db, depositAddress.ID, utxoSwap); err != nil {
		return nil, err
	}
	return utxoSwap, nil
}

// coordinatorIdentityForSwap mirrors the fixed swap and refund flows' helper:
// on a participant the identity comes from ctx (attached by DispatchPrepare
// after resolving the engine's coordinator_index, which fails closed on an
// unresolvable index and rejects an index naming the receiving SO); a missing
// ctx value can only mean the coordinator's own self-Prepare, where this SO's
// own key is correct by definition.
func (h *ReserveInstantStaticDepositFlowHandler) coordinatorIdentityForSwap(ctx context.Context) keys.Public {
	if coordinatorPubKey, ok := consensus.CoordinatorIdentityFromContext(ctx); ok {
		return coordinatorPubKey
	}
	return h.config.IdentityPublicKey()
}

// Commit applies the aggregated transfer commit (transfer →
// SENDER_KEY_TWEAKED). The swap row deliberately stays CREATED —
// CREATED-with-transfer is the reserved state the claim phase consumes; only
// claim completes the swap. Idempotent against gossip redelivery:
// applySendTransferCommit short-circuits an already-committed transfer.
func (h *ReserveInstantStaticDepositFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.ReserveInstantStaticDepositUtxoSwapCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for reserve instant static deposit commit", op)
	}
	if req.GetTransferCommit() == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_commit is required"))
	}
	return h.transfer.applySendTransferCommit(ctx, req.GetTransferCommit())
}

// Rollback locates this SO's INSTANT swap row by requested_transfer_id (scoped
// to CREATED — a reservation the claim phase already completed is untouchable
// by a stale rollback), returns the transfer the row names (derived from the
// row's own requested_transfer_id, never a free-standing coordinator-named
// id), then cancels the swap. The transfer-first order satisfies
// CancelUtxoSwap's SP-3261 guard. Idempotent: a never-created / cancelled /
// completed row is a no-op. Accepts both the canonical rollback payload and
// the prepare op echoed by the participant reconciler.
func (h *ReserveInstantStaticDepositFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	// Both rollback sources are coordinator-scoped. The (transferID, INSTANT, CREATED) lookup
	// resolves against ANY local reservation, so it MUST be constrained to the coordinator that
	// drove this flow — resolved from THIS SO's own FlowExecution row by dispatchConsensusRollback,
	// never from the payload. This includes the PrepareRequest (reconciler-echo) arm: gossip
	// dispatch derives the Go type from the payload's Any URL, so a Byzantine coordinator could
	// gossip a PrepareRequest naming another honest SO's transfer id under its own in-flight flow;
	// scoping to the flow's coordinator makes that lookup miss. Fail closed if the identity is
	// unavailable — an unscoped rollback must never cancel anything.
	var transferIDStr string
	switch r := op.(type) {
	case *pbinternal.ReserveInstantStaticDepositUtxoSwapRollbackRequest:
		transferIDStr = r.GetRequestedTransferId()
	case *pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest:
		transferIDStr = r.GetOriginalRequest().GetTransfer().GetTransferId()
	default:
		return fmt.Errorf("unexpected operation type %T for reserve instant static deposit rollback", op)
	}
	if transferIDStr == "" {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("requested_transfer_id is required for rollback"))
	}
	transferID, err := uuid.Parse(transferIDStr)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid requested_transfer_id: %w", err))
	}
	coordinatorPubKey, ok := consensus.CoordinatorIdentityFromContext(ctx)
	if !ok {
		// Fail closed: never run the lookup unscoped, for either arm.
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("cannot scope instant reserve rollback: coordinator identity unavailable for transfer %s", transferID))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db: %w", err)
	}
	logger := logging.GetLoggerFromContext(ctx)
	swap, err := db.UtxoSwap.Query().
		Where(
			entutxoswap.RequestedTransferIDEQ(transferID),
			entutxoswap.RequestTypeEQ(st.UtxoSwapRequestTypeInstant),
			entutxoswap.StatusEQ(st.UtxoSwapStatusCreated),
			entutxoswap.CoordinatorIdentityPublicKeyEQ(coordinatorPubKey),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			logger.Sugar().Infof("reserve instant static deposit 2pc rollback: no CREATED instant swap for transfer %s, no-op", transferID)
			return nil
		}
		return fmt.Errorf("unable to load instant swap for transfer %s: %w", transferID, err)
	}

	// A committed reservation stays CREATED with a sent transfer (unlike the fixed swap, whose
	// COMPLETED row is status-filtered out), so a stale/redelivered rollback still matches the
	// (requested_transfer_id, INSTANT, CREATED) query. Absorb as an idempotent no-op instead of
	// letting CancelUtxoSwap's SP-3261 guard error and loop runConsensusRollback. The terminal-row
	// fence prevents this in correct 2PC; this is the reconciler-replay fallback.
	if swap.Edges.Transfer == nil {
		if t, err := swap.QueryTransfer().Only(ctx); err == nil {
			swap.Edges.Transfer = t
		} else if !ent.IsNotFound(err) {
			return fmt.Errorf("unable to load transfer for instant swap %s during rollback: %w", swap.ID, err)
		}
	}
	if swap.Edges.Transfer != nil && transferHelper.IsTransferSent(swap.Edges.Transfer) {
		logger.Sugar().Infof("reserve instant static deposit 2pc rollback: swap %s transfer already sent, committed reservation, no-op", swap.ID)
		return nil
	}

	if err := h.transfer.rollbackSendTransfer(ctx, swap.RequestedTransferID); err != nil {
		return fmt.Errorf("failed to roll back instant reserve transfer %s: %w", swap.RequestedTransferID, err)
	}
	if err := CancelUtxoSwap(ctx, swap); err != nil {
		return fmt.Errorf("unable to cancel instant utxo swap %s: %w", swap.ID, err)
	}
	logger.Sugar().Infof("reserve instant static deposit 2pc rollback: swap %s marked CANCELLED", swap.ID)
	return nil
}
