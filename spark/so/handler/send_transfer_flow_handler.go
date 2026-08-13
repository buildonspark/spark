package handler

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/sighash"
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
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/partner"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// SendTransferFlowHandler — participant side (Prepare / Commit / Rollback)
// ---------------------------------------------------------------------------

// SendTransferFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_SEND_TRANSFER (v3 send-transfer-with-transfer-package).
//
// Embeds *TransferHandler for access to ValidateTransferPackage, createTransferV3,
// UpdateTransferLeavesSignatures, commitSenderKeyTweaks and the cancel helpers.
// Reached via the engine whenever StartTransferV3 (or StartTransferV2 with a
// TransferPackage) is called.
type SendTransferFlowHandler struct {
	*TransferHandler

	// transferType is written on the Transfer rows Prepare creates; partnerType
	// is the attribution the coordinator flow records in BuildCommitPayload.
	// Plain sends use Transfer/Transfer; flows that embed a send-transfer inside
	// a larger consensus op (e.g. the static deposit utxo swap) construct the
	// handler with their own pair via NewSendTransferFlowHandlerForType.
	transferType st.TransferType
	partnerType  st.TransferPartnerType
	// requireDirectRefunds is passed to both ValidateTransferPackage
	// (requireDirectFromCpfpLeaves) and createTransferV3 (requireDirectTx).
	// Plain v3 sends require direct refund txs; the utxo-swap transfer's legacy
	// path accepts packages without them (initiateUtxoSwapTransfer →
	// startTransferInternal passes requireDirectTx=false), so embedding flows
	// choose per type to preserve their wire contract.
	requireDirectRefunds bool
}

var (
	_ consensus.FlowHandler             = (*SendTransferFlowHandler)(nil)
	_ consensus.PrepareBoundFlowHandler = (*SendTransferFlowHandler)(nil)
)

func NewSendTransferFlowHandler(config *so.Config) *SendTransferFlowHandler {
	return NewSendTransferFlowHandlerForType(config, st.TransferTypeTransfer, st.TransferPartnerTypeTransfer, true)
}

func NewSendTransferFlowHandlerForType(config *so.Config, transferType st.TransferType, partnerType st.TransferPartnerType, requireDirectRefunds bool) *SendTransferFlowHandler {
	return &SendTransferFlowHandler{
		TransferHandler:      NewTransferHandler(config),
		transferType:         transferType,
		partnerType:          partnerType,
		requireDirectRefunds: requireDirectRefunds,
	}
}

// Prepare runs on every SO. It validates the transfer package, decrypts this SO's
// slice of the sender key tweaks, persists Transfer + TransferLeaf rows in the
// pre-commit `SenderKeyTweakPending` state, and produces local FROST round-2
// signature shares for the leaves where this SO is part of the signing set.
//
// SOs outside the signing set still write rows; they return nil shares.
func (h *SendTransferFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	req, ok := op.(*pbinternal.SendTransferPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for send transfer prepare", op)
	}
	orig := req.GetOriginalRequest()
	if orig == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("original_request is required"))
	}

	parsed, err := parseSendTransferRequest(orig)
	if err != nil {
		return nil, err
	}

	keyTweakMap, err := h.ValidateTransferPackage(ctx, parsed.transferID, parsed.senderPkg.GetTransferPackage(), parsed.senderIDPK, h.requireDirectRefunds)
	if err != nil {
		return nil, fmt.Errorf("failed to validate transfer package: %w", err)
	}
	if len(keyTweakMap) == 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer package contains no key tweaks"))
	}

	// No cross-SO verifySenderKeyTweakProofsMatch call (legacy InitiateTransferV2
	// has one). V3's TransferPackage is a single user-signed blob; ValidateTransferPackage
	// above verifies that signature on every SO, which subsumes the legacy parallel-field check.

	// Per-SO transfer-size limit, enforced with raw status.Errorf so clients
	// see codes.InvalidArgument (the wire contract predates the consensus
	// path). TODO: aggregate package counts across senders at the meta level
	// when multi-sender lands.
	transferLimit := knobs.GetKnobsService(ctx).GetValue(knobs.KnobSoTransferLimit, 0)
	if transferLimit > 0 && len(keyTweakMap) > int(transferLimit) {
		return nil, status.Errorf(codes.InvalidArgument, "transfer limit reached, please send %d leaves at a time", int(transferLimit))
	}

	// Spark-invoice transfers (only reachable when a v2 request routed through
	// the engine carried one) are validated on every SO in Prepare, not just
	// the coordinator as in the legacy path. createTransferV3 below persists
	// the invoice + links it to the transfer.
	if sparkInvoice := req.GetSparkInvoice(); sparkInvoice != "" {
		if len(parsed.receivers) != 1 {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("spark invoice transfer requires exactly one receiver, got %d", len(parsed.receivers)))
		}
		leafIDsToSend := make([]uuid.UUID, len(parsed.pkg.LeavesToSend()))
		for i, job := range parsed.pkg.LeavesToSend() {
			leafIDsToSend[i] = job.LeafID()
		}
		if err := validateSatsSparkInvoice(ctx, sparkInvoice, parsed.receivers[0], parsed.senderIDPK, leafIDsToSend, true); err != nil {
			return nil, fmt.Errorf("failed to validate sats spark invoice %s for transfer %s: %w", sparkInvoice, parsed.transferID, err)
		}
	}

	cpfpMap := stringKeyedRefundMap(parsed.pkg.CPFPRefundTxByLeafID())
	directMap := stringKeyedRefundMap(parsed.pkg.DirectRefundTxByLeafID())
	dfcMap := stringKeyedRefundMap(parsed.pkg.DirectFromCPFPRefundTxByLeafID())

	// Two deliberate choices vs the legacy InitiateTransferV2 participant call:
	//
	//   - TransferRoleParticipant on every SO. The legacy distinction between
	//     SenderInitiatedCoordinator (coord) and SenderKeyTweakPending
	//     (participant) was a state-machine artifact of the two-call flow;
	//     under 2PC the engine's FlowExecution row tracks role.
	//     commitSenderKeyTweaks accepts either pre-commit status, so the
	//     unified one is harmless downstream.
	//
	//   - requireDirectTx=true on every SO (vs false on legacy participants).
	//     Legacy coord enforced the strict check up front so participants
	//     could trust the result. In 2PC there's no separate coordinator-side
	//     createTransferV3 — Prepare on every SO is the only call site, so
	//     this is where the check has to live to preserve legacy coord
	//     behavior.
	spec := &transferSpec{
		transferID:      parsed.transferID,
		senderIDPK:      parsed.senderIDPK,
		receivers:       parsed.receivers,
		leafReceiverMap: parsed.leafReceiverMap,
		expiryTime:      orig.GetExpiryTime().AsTime(),
		pkgLeafIDs:      transferPackageLeafIDLists(parsed.senderPkg.GetTransferPackage()),
		cpfpRefunds:     cpfpMap,
		directRefunds:   directMap,
		dfcRefunds:      dfcMap,
		keyTweaks:       keyTweakMap,
		sparkInvoice:    req.GetSparkInvoice(),
	}
	transferEnt, leafMap, err := h.createTransferV3(ctx, spec, h.transferType, TransferRoleParticipant, h.requireDirectRefunds)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer rows for %s: %w", parsed.transferID, err)
	}

	if err := bindManifestIfPresent(ctx, manifestBindEndpointForTransferType(h.transferType), orig, transferEnt.Network, leafMap); err != nil {
		return nil, err
	}

	jobs, err := buildSendTransferLocalSigningJobs(ctx, parsed.transferID, parsed.pkg, leafMap, TransferAdaptorPublicKeys{})
	if err != nil {
		return nil, fmt.Errorf("failed to build local signing jobs: %w", err)
	}
	jobs = filterJobsForThisOperator(jobs, h.config.Identifier)
	if len(jobs) == 0 {
		// Not in signing set for any leaf — nothing to do for FROST.
		return nil, nil
	}

	frostHandler := signing_handler.NewFrostSigningHandler(h.config)
	frostResp, err := frostHandler.FrostRound2(ctx, &pbinternal.FrostRound2Request{SigningJobs: jobs})
	if err != nil {
		return nil, fmt.Errorf("local frost round 2 failed during prepare: %w", err)
	}
	return frostResp, nil
}

// ValidateDecisionAgainstPrepare implements consensus.PrepareBoundFlowHandler:
// commit and rollback resolve the transfer purely from the decision payload's
// transfer_id, so without this binding a misbehaving coordinator could reuse a
// valid IN_FLIGHT SEND_TRANSFER flow id while pointing the payload at an
// unrelated pre-commit transfer. Bind the decision's transfer_id to the one
// this SO persisted in its prepare op.
func (h *SendTransferFlowHandler) ValidateDecisionAgainstPrepare(prepareOp proto.Message, decisionOp proto.Message) error {
	prepare, ok := prepareOp.(*pbinternal.SendTransferPrepareRequest)
	if !ok {
		return fmt.Errorf("unexpected prepare op type %T for send transfer", prepareOp)
	}
	preparedTransferID := prepare.GetOriginalRequest().GetTransferId()
	switch d := decisionOp.(type) {
	case *pbinternal.SendTransferCommitRequest:
		if !sameTransferID(d.GetTransferId(), preparedTransferID) {
			return fmt.Errorf("commit transfer id %s does not match the prepared transfer id %s", d.GetTransferId(), preparedTransferID)
		}
	case *pbinternal.SendTransferRollbackRequest:
		if !sameTransferID(d.GetTransferId(), preparedTransferID) {
			return fmt.Errorf("rollback transfer id %s does not match the prepared transfer id %s", d.GetTransferId(), preparedTransferID)
		}
	case *pbinternal.SendTransferPrepareRequest:
		// The reconciler's presumed-abort path echoes the prepare op itself.
		if !sameTransferID(d.GetOriginalRequest().GetTransferId(), preparedTransferID) {
			return fmt.Errorf("presumed-abort rollback transfer id %s does not match the prepared transfer id %s", d.GetOriginalRequest().GetTransferId(), preparedTransferID)
		}
	default:
		return fmt.Errorf("unexpected decision op type %T for send transfer", decisionOp)
	}
	return nil
}

// Commit runs on every participant (the coordinator's equivalent work lives in
// BuildCommitPayload). It applies the aggregated refund signatures to the
// TransferLeaf rows the participant wrote in Prepare, then applies the sender
// key tweaks and transitions the transfer to SENDER_KEY_TWEAKED.
func (h *SendTransferFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.SendTransferCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for send transfer commit", op)
	}
	return h.applySendTransferCommit(ctx, req)
}

// Rollback runs on every participant (and on the coordinator if Prepare or
// BuildCommitPayload fails). It marks the transfer RETURNED via
// executeCancelTransfer — same path the legacy CancelTransfer /
// RollbackTransferGossip uses. Idempotent: a never-created transfer is a
// no-op, and a transfer already RETURNED or past the rollbackable pre-commit
// states short-circuits in rollbackSendTransfer.
//
// Accepts both SendTransferRollbackRequest (the normal rollback payload) and
// SendTransferPrepareRequest (the prepare op echoed back by the reconciler
// when the coordinator's row was lost).
func (h *SendTransferFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var transferIDStr string
	switch r := op.(type) {
	case *pbinternal.SendTransferRollbackRequest:
		transferIDStr = r.GetTransferId()
	case *pbinternal.SendTransferPrepareRequest:
		if r.GetOriginalRequest() != nil {
			transferIDStr = r.GetOriginalRequest().GetTransferId()
		}
	default:
		return fmt.Errorf("unexpected operation type %T for send transfer rollback", op)
	}
	if transferIDStr == "" {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_id is required for rollback"))
	}
	transferID, err := uuid.Parse(transferIDStr)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}
	return h.rollbackSendTransfer(ctx, transferID)
}

// applySendTransferCommit applies aggregated signatures and key tweaks on a
// single SO. Shared by participant Commit and coordinator BuildCommitPayload.
//
// Idempotent against replayed commit gossip: the engine's gossip layer can
// deliver the same Commit more than once. A second delivery against a
// transfer whose status is already past the pre-commit states finds
// UpdateTransferLeavesSignatures running over already-signed refund tx
// bytes (which UpdateTxWithSignature treats as a malformed input) and
// commitSenderKeyTweaks's already-cleared key_tweak columns (which it
// raises as an error). Short-circuit before either runs.
func (h *SendTransferFlowHandler) applySendTransferCommit(ctx context.Context, req *pbinternal.SendTransferCommitRequest) error {
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}

	cpfpSigs, directSigs, dfcSigs := splitLeafSignatures(req.GetLeafSignatures())

	transferEnt, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s for commit: %w", transferID, err)
	}

	// Pre-commit states are the only ones where commit work is meaningful.
	// commitSenderKeyTweaks itself accepts these three statuses and treats
	// anything else as already-committed; we mirror that condition here so
	// the UpdateTransferLeavesSignatures call upstream of it doesn't trip
	// on a replayed delivery. SenderInitiated is excluded — it means the
	// transfer never reached this SO's prepare phase, which is a real
	// invariant violation we want to surface.
	switch transferEnt.Status {
	case st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusApplyingSenderKeyTweak:
		// Fall through to apply commit work.
	default:
		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"send transfer 2pc commit: transfer %s already past pre-commit (status=%s), treating as idempotent retry",
			transferID, transferEnt.Status)
		return nil
	}

	if err := h.UpdateTransferLeavesSignatures(ctx, transferEnt, cpfpSigs, directSigs, dfcSigs); err != nil {
		return fmt.Errorf("unable to apply refund signatures for transfer %s: %w", transferID, err)
	}

	if _, err := h.commitSenderKeyTweaks(ctx, transferEnt); err != nil {
		return fmt.Errorf("unable to commit sender key tweaks for transfer %s: %w", transferID, err)
	}
	return nil
}

// rollbackSendTransfer transitions the transfer (and its TransferReceivers) to
// the terminal RETURNED state and unlocks the underlying leaves — the same
// mechanism the legacy CancelTransfer / RollbackTransferGossip path uses. The
// Transfer + TransferLeaf rows are kept so history (and any in-flight reads)
// remains consistent; only the status moves to RETURNED.
//
// Idempotent in two directions:
//   - if Prepare never created a row on this SO (NotFound) → no-op
//   - if rollback already ran, or the transfer is already past the rollbackable
//     pre-commit states, the late rollback is a no-op.
func (h *SendTransferFlowHandler) rollbackSendTransfer(ctx context.Context, transferID uuid.UUID) error {
	transferEnt, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("unable to load transfer %s for rollback: %w", transferID, err)
	}

	switch transferEnt.Status {
	case st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusSenderKeyTweakPending:
		// These are the only states where rollback can safely return the leaf to
		// the sender. Later states may already have applied key tweaks or receiver
		// claim progress, so a rollback delivered after commit must be idempotent.
	case st.TransferStatusReturned:
		logging.GetLoggerFromContext(ctx).Sugar().Infof("send transfer 2pc rollback: transfer %s already RETURNED", transferID)
		return nil
	default:
		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"send transfer 2pc rollback: transfer %s already past rollbackable status %s",
			transferID,
			transferEnt.Status,
		)
		return nil
	}

	if err := h.executeCancelTransfer(ctx, transferEnt); err != nil {
		return fmt.Errorf("unable to cancel transfer %s during rollback: %w", transferID, err)
	}
	logging.GetLoggerFromContext(ctx).Sugar().Infof("send transfer 2pc rollback: transfer %s marked RETURNED", transferID)
	return nil
}

// ---------------------------------------------------------------------------
// sendTransferCoordinatorFlow — coordinator side
// ---------------------------------------------------------------------------

// sendTransferCoordinatorFlow drives the coordinator-side of a v3 send-transfer
// through the 2PC engine. The handler delegates Prepare/Commit/Rollback to the
// participant-side SendTransferFlowHandler (the engine calls Prepare on the
// coordinator too); BuildCommitPayload is where coordinator-only work lives —
// aggregating FROST shares, applying signatures + key tweaks locally, and
// returning the commit payload that gets gossiped to participants.
type sendTransferCoordinatorFlow struct {
	*SendTransferFlowHandler

	req               *pb.StartTransferV3Request
	parsed            parsedSendTransferRequest
	signingJobsByLeaf map[string]*sendTransferLeafSigningJobs

	// sparkInvoice is carried out-of-band from req because the public
	// StartTransferV3Request has no invoice field; it is non-empty only when a
	// StartTransferV2 request routed through the engine paid an invoice.
	sparkInvoice string

	senderKeyTweakProofs map[string]*pb.SecretProof

	// response is populated during BuildCommitPayload so the public
	// StartTransferV3 handler can return it after engine.Execute completes.
	response *pb.StartTransferResponse
}

var _ consensus.CoordinatorFlow = (*sendTransferCoordinatorFlow)(nil)

// PrepareOp returns the prepare request sent to every SO (engine fans this out).
func (f *sendTransferCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.SendTransferPrepareRequest{
		OriginalRequest:      f.req,
		SparkInvoice:         f.sparkInvoice,
		SenderKeyTweakProofs: f.senderKeyTweakProofs,
	}
}

// BuildCommitPayload aggregates FROST shares from all SOs, applies the resulting
// signatures + sender key tweaks on the coordinator, builds the response, and
// returns the commit payload (aggregated signatures keyed by leaf) for the
// engine to gossip to participants.
func (f *sendTransferCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	// Discard the participantIDs return: send-transfer filters public shares
	// per-job from the actual share contributors (different leaves can carry
	// different round1 commitment sets), not from the global participant set
	// that renew_leaf uses.
	allShares, _, err := collectSignatureShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	// One FROST gRPC connection for all per-leaf aggregation jobs. Up to
	// 3 jobs/leaf × n leaves would otherwise pay the dial cost per call.
	frostConn, err := f.config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	leafSignatures, batch, err := aggregateSendTransferLeafSignatures(ctx, f.config, frostClient, f.signingJobsByLeaf, allShares, true)
	if err != nil {
		return nil, err
	}

	// Per-leaf per-variant SigningResults preserve parity with the legacy v3
	// response (built via buildSigningResultProtos). The SDK helper for v3
	// doesn't consume this field today, but the RPC contract publishes it.
	cpfpSigningResultMap := make(map[string]*helper.SigningResult, len(f.signingJobsByLeaf))
	directSigningResultMap := make(map[string]*helper.SigningResult)
	dfcSigningResultMap := make(map[string]*helper.SigningResult)
	leafMap := make(map[string]*ent.TreeNode, len(f.signingJobsByLeaf))
	for leafID, jobs := range f.signingJobsByLeaf {
		leafMap[leafID] = jobs.leaf
		if jobs.cpfp != nil {
			if cpfpSigningResultMap[leafID], err = batch.signingResult(leafAggregationJobKey(leafID, txKindCPFP)); err != nil {
				return nil, err
			}
		}
		if jobs.direct != nil {
			if directSigningResultMap[leafID], err = batch.signingResult(leafAggregationJobKey(leafID, txKindDirect)); err != nil {
				return nil, err
			}
		}
		if jobs.dfc != nil {
			if dfcSigningResultMap[leafID], err = batch.signingResult(leafAggregationJobKey(leafID, txKindDirectFromCPFP)); err != nil {
				return nil, err
			}
		}
	}

	// Apply on the coordinator's DB now so the request tx that the engine
	// commits next carries the final transfer state. Participants do the
	// same work via FlowHandler.Commit after receiving the commit gossip.
	commitReq := &pbinternal.SendTransferCommitRequest{
		TransferId:     f.parsed.transferID.String(),
		LeafSignatures: leafSignatures,
	}
	if err := f.applySendTransferCommit(ctx, commitReq); err != nil {
		return nil, fmt.Errorf("failed to apply commit on coordinator: %w", err)
	}

	// Coordinator-only partner attribution, mirroring the coop-exit consensus
	// flow: runs in the request ctx (carries the partner JWT) and tx before the
	// engine's DbCommit; a no-op on participants.
	partner.SaveTransferPartner(ctx, f.parsed.transferID, f.partnerType)

	// Build the response StartTransferV3 returns to the client.
	transferEnt, err := f.loadTransferForUpdate(ctx, f.parsed.transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to reload transfer %s after commit: %w", f.parsed.transferID, err)
	}
	transferProto, err := transferEnt.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer %s for response: %w", f.parsed.transferID, err)
	}
	signingResultProtos, err := buildSigningResultProtos(leafMap, cpfpSigningResultMap, directSigningResultMap, dfcSigningResultMap)
	if err != nil {
		return nil, fmt.Errorf("unable to build signing result protos: %w", err)
	}
	f.response = &pb.StartTransferResponse{Transfer: transferProto, SigningResults: signingResultProtos}

	return commitReq, nil
}

// RollbackPayload returns the minimal payload sent to participants on rollback.
func (f *sendTransferCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.SendTransferRollbackRequest{TransferId: f.parsed.transferID.String()}
}

// buildSendTransferCoordinatorFlow pre-computes the signing-job helpers the
// coordinator needs during BuildCommitPayload's aggregation. Callers parse the
// request via parseSendTransferRequest and thread the result in, so the
// coordinator runs the expensive body parse (transferpkg.ParsePackage) only
// once. (The cheap envelope/receiver parses still run twice — at the entry
// gate and here — which isn't worth avoiding.) The coordinator's own DB
// writes (createTransferV3, FROST round-2) happen inside engine.Execute via
// the engine-driven Prepare phase.
//
// handler supplies the transfer semantics (transferType/partnerType/
// requireDirectRefunds) the flow applies — plain sends pass
// NewSendTransferFlowHandler; embedding flows (e.g. the static deposit utxo
// swap) pass their own typed handler so the flow is correctly typed from
// construction rather than patched afterwards.
//
// Contract for embedding flows: remote participants and commit/rollback
// redelivery construct their handler from the OP TYPE (consensusFlowHandler),
// not from this coordinator-side value — SEND_TRANSFER always dispatches the
// plain-send defaults. A flow that needs non-default transfer semantics must
// therefore register its own op type whose dispatcher builds the same typed
// handler on every SO (the static deposit utxo swap does exactly this via
// NewStaticDepositUtxoSwapFlowHandler), never reuse SEND_TRANSFER with a
// customized handler.
func buildSendTransferCoordinatorFlow(ctx context.Context, config *so.Config, req *pb.StartTransferV3Request, parsed parsedSendTransferRequest, sparkInvoice string, handler *SendTransferFlowHandler) (*sendTransferCoordinatorFlow, error) {
	// Pre-load leaves for signing-job construction. The pre-load is
	// intentionally non-locking: createTransferV3 inside the engine's Prepare
	// phase re-loads these under FOR UPDATE before mutating them, and
	// Prepare's leafAvailableStatus check rejects any leaf whose status
	// changed under us (e.g., a concurrent renew_leaf flipped Available →
	// RenewLocked, which is the only Spark flow that mutates leaf.RawTx).
	// So the worst case here is a wasted job-builder pass that the locked
	// re-read in Prepare aborts cleanly — not a sighash divergence reaching
	// signing. Locking at this layer would hold row locks on every leaf for
	// the entire Prepare RPC fan-out plus FROST aggregation (~seconds),
	// blocking concurrent transfers/claims/exits touching the same leaves.
	//
	// pkg.LeafIDs() is the distinct union across all three refund variants, so a
	// direct/dfc-only leaf (possible under multi-sender) is still loaded and
	// doesn't fail the per-leaf lookup in buildSendTransferAggregationJobs.
	leafUUIDs := parsed.pkg.LeafIDs()
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	leaves, err := db.TreeNode.Query().
		Where(treenode.IDIn(leafUUIDs...)).
		WithTree().
		WithSigningKeyshare().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to preload leaves for coordinator flow: %w", err)
	}
	if len(leaves) != len(leafUUIDs) {
		return nil, fmt.Errorf("preload missed leaves: got %d, want %d", len(leaves), len(leafUUIDs))
	}
	leafMap := make(map[string]*ent.TreeNode, len(leaves))
	for _, leaf := range leaves {
		leafMap[leaf.ID.String()] = leaf
	}

	jobsByLeaf, err := buildSendTransferAggregationJobs(ctx, parsed.transferID, parsed.pkg, leafMap, TransferAdaptorPublicKeys{})
	if err != nil {
		return nil, fmt.Errorf("unable to build signing-job helpers: %w", err)
	}

	senderKeyTweakProofs, err := handler.coordinatorSenderKeyTweakProofs(ctx, parsed.senderPkg.GetTransferPackage())
	if err != nil {
		return nil, fmt.Errorf("unable to read sender key tweak proofs from the coordinator's slice: %w", err)
	}

	return &sendTransferCoordinatorFlow{
		SendTransferFlowHandler: handler,
		req:                     req,
		parsed:                  parsed,
		signingJobsByLeaf:       jobsByLeaf,
		sparkInvoice:            sparkInvoice,
		senderKeyTweakProofs:    senderKeyTweakProofs,
	}, nil
}

// ---------------------------------------------------------------------------
// Parsing + validation helpers
// ---------------------------------------------------------------------------

// stringKeyedRefundMap re-keys a leaf-ID-keyed refund map to string keys.
// Deprecated: This is a temporary shim until callers are migrated to operate in terms of UUIDs.
func stringKeyedRefundMap(m map[uuid.UUID][]byte) map[string][]byte {
	out := make(map[string][]byte, len(m))
	for id, rawTx := range m {
		out[id.String()] = rawTx
	}
	return out
}

type parsedSendTransferRequest struct {
	transferID      uuid.UUID
	senderPkg       *pb.SenderTransferPackage
	pkg             *transferpkg.Package
	senderIDPK      keys.Public
	leafReceiverMap map[string]keys.Public
	receivers       []keys.Public
}

// parseSendTransferEnvelope validates the request envelope — the cheap checks
// the public entry point runs before session auth and knob gates — and
// returns the sender package with its parsed identity key.
func parseSendTransferEnvelope(req *pb.StartTransferV3Request) (*pb.SenderTransferPackage, keys.Public, error) {
	if req == nil {
		return nil, keys.Public{}, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}
	if len(req.GetSenderPackages()) != 1 {
		return nil, keys.Public{}, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("expected exactly 1 sender package, got %d", len(req.GetSenderPackages())))
	}
	senderPkg := req.GetSenderPackages()[0]
	if senderPkg == nil {
		return nil, keys.Public{}, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("sender_package is required"))
	}
	if senderPkg.GetTransferPackage() == nil {
		return nil, keys.Public{}, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_package is required"))
	}
	senderIDPK, err := keys.ParsePublicKey(senderPkg.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, keys.Public{}, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid owner identity public key: %w", err))
	}
	return senderPkg, senderIDPK, nil
}

// bindManifestIfPresent holds the executed transfer to the manifest every sender signed, when one
// is supplied, and rejects manifest material that cannot be bound when one is not.
// start_transfer_v3 is the generic transfer path and never demands a manifest; the flows that
// treat one as part of their contract require it on their own endpoint instead.
//
// This is the single manifest gate on the Prepare path, and it runs after createTransferV3
// because the cover check needs the locked rows' owners and amounts — which is also what keeps
// those two facts out of the requester's hands. The coordinator entry additionally runs the
// signature half up front, where it can reject before any of that work is done.
func bindManifestIfPresent(ctx context.Context, endpoint manifestBindEndpoint, req *pb.StartTransferV3Request, network btcnetwork.Network, leafMap map[string]*ent.TreeNode) error {
	if req.GetTransferManifest() == nil {
		return rejectStrayManifestSignature(ctx, endpoint, req)
	}

	leaves := make(map[string]transferpkg.ExecutedLeaf, len(leafMap))
	for leafID, leaf := range leafMap {
		leaves[leafID] = transferpkg.ExecutedLeaf{Owner: leaf.OwnerIdentityPubkey, ValueSats: leaf.Value}
	}

	if err := transferpkg.BindManifest(req, network, leaves); err != nil {
		kind, _ := classifyManifestRefusal(err)
		recordManifestRefusal(ctx, endpoint, kind)
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer manifest does not bind this transfer: %w", err))
	}
	return nil
}

// requireAndBindManifest is bindManifestIfPresent for the flows whose contract includes a manifest:
// a missing one is refused rather than skipped. Two named entry points rather than a boolean
// parameter, because a bare true at a call site says nothing about which precondition is in force.
func requireAndBindManifest(ctx context.Context, endpoint manifestBindEndpoint, req *pb.StartTransferV3Request, network btcnetwork.Network, leafMap map[string]*ent.TreeNode) error {
	if req.GetTransferManifest() == nil {
		// Counted here rather than around the delegated call, which counts its own refusals.
		recordManifestRefusal(ctx, endpoint, manifestRefusalMissingManifest)
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_manifest is required"))
	}
	return bindManifestIfPresent(ctx, endpoint, req, network, leafMap)
}

// parseSendTransferReceivers parses the sender package's leaf→receiver map and
// returns it along with the distinct receivers in canonical (sorted) order.
func parseSendTransferReceivers(senderPkg *pb.SenderTransferPackage) (map[string]keys.Public, []keys.Public, error) {
	leafReceiverMap := make(map[string]keys.Public, len(senderPkg.GetReceiverIdentityPublicKeys()))
	receiverSet := make(map[string]keys.Public)
	for leafID, recvBytes := range senderPkg.GetReceiverIdentityPublicKeys() {
		recvPK, err := keys.ParsePublicKey(recvBytes)
		if err != nil {
			return nil, nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse receiver public key for leaf %s: %w", leafID, err))
		}
		leafReceiverMap[leafID] = recvPK
		receiverSet[string(recvPK.Serialize())] = recvPK
	}
	if len(receiverSet) == 0 {
		return nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("at least one receiver required"))
	}
	receivers := slices.SortedFunc(maps.Values(receiverSet), func(a, b keys.Public) int {
		return bytes.Compare(a.Serialize(), b.Serialize())
	})
	return leafReceiverMap, receivers, nil
}

// parseSendTransferRequest extracts and validates the structural fields shared
// by every call site (Prepare on each SO, buildSendTransferCoordinatorFlow).
// MVP: single sender, any number of receivers.
func parseSendTransferRequest(req *pb.StartTransferV3Request) (parsedSendTransferRequest, error) {
	var empty parsedSendTransferRequest
	senderPkg, senderIDPK, err := parseSendTransferEnvelope(req)
	if err != nil {
		return empty, err
	}
	pkg, err := transferpkg.ParsePackage(senderPkg.GetTransferPackage())
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer package: %w", err))
	}
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer id: %w", err))
	}
	leafReceiverMap, receivers, err := parseSendTransferReceivers(senderPkg)
	if err != nil {
		return empty, err
	}

	return parsedSendTransferRequest{
		transferID:      transferID,
		senderPkg:       senderPkg,
		pkg:             pkg,
		senderIDPK:      senderIDPK,
		leafReceiverMap: leafReceiverMap,
		receivers:       receivers,
	}, nil
}

// ---------------------------------------------------------------------------
// Deterministic signing-job construction
// ---------------------------------------------------------------------------

// sendTransferSigningJobNamespace is a fixed UUIDv4 mixed into NewSHA1 to
// produce deterministic per-leaf-per-tx-variant job IDs that don't collide
// with other 2PC flows.
var sendTransferSigningJobNamespace = uuid.MustParse("3d6b8c2a-7a9e-4a8d-9b1c-0c2c1b6d4e9f")

// sendTransferJobID returns a deterministic UUID identifying the FROST signing
// job for (transferID, leafID, txKind). All SOs derive the same ID, which lets
// the coordinator correlate shares without sending job IDs over the wire.
//
// txKind values: "cpfp", "direct", "directFromCpfp".
func sendTransferJobID(transferID uuid.UUID, leafID string, txKind string) uuid.UUID {
	return uuid.NewSHA1(sendTransferSigningJobNamespace, fmt.Appendf(nil, "%s:%s:%s", transferID, leafID, txKind))
}

// sendTransferLeafSigningJobs holds the pre-built signing-job helpers for one
// leaf's three refund tx variants, plus the user's signature shares (needed
// for FROST aggregation). Used by the coordinator only — BuildCommitPayload
// needs helpers (not just job IDs) to call AggregateFrost.
type sendTransferLeafSigningJobs struct {
	leaf          *ent.TreeNode
	cpfp          *helper.SigningJobWithPregeneratedNonce
	cpfpUserSig   []byte
	direct        *helper.SigningJobWithPregeneratedNonce
	directUserSig []byte
	dfc           *helper.SigningJobWithPregeneratedNonce
	dfcUserSig    []byte
}

// buildSendTransferAggregationJobs constructs the per-leaf signing-job helpers
// the coordinator uses for FROST aggregation. Mirrors the iteration logic in
// SignRefundsWithPregeneratedNonce but with deterministic job IDs.
// adaptorKeys carries the per-variant adaptor public keys (zero values for
// flows without adaptor signatures; set by the swap v3 flows).
func buildSendTransferAggregationJobs(
	ctx context.Context,
	transferID uuid.UUID,
	pkg *transferpkg.Package,
	leafMap map[string]*ent.TreeNode,
	adaptorKeys TransferAdaptorPublicKeys,
) (map[string]*sendTransferLeafSigningJobs, error) {
	out := make(map[string]*sendTransferLeafSigningJobs, len(leafMap))
	for _, leaf := range leafMap {
		out[leaf.ID.String()] = &sendTransferLeafSigningJobs{leaf: leaf}
	}
	for _, job := range pkg.LeavesToSend() {
		leafID := job.LeafID().String()
		leaf, ok := leafMap[leafID]
		if !ok {
			return nil, fmt.Errorf("cpfp leaf %s not found in leaf map", leafID)
		}
		signingJob, err := buildSigningJobForRefund(ctx, job, leaf, leaf.RawTx, sendTransferJobID(transferID, leafID, "cpfp"), adaptorKeys.cpfpAdaptorPubKey)
		if err != nil {
			return nil, fmt.Errorf("build cpfp signing job for leaf %s: %w", leafID, err)
		}
		out[leafID].cpfp = signingJob
		out[leafID].cpfpUserSig = job.Inputs()[0].UserSignature()
	}
	for _, job := range pkg.DirectLeavesToSend() {
		leafID := job.LeafID().String()
		leaf, ok := leafMap[leafID]
		if !ok {
			return nil, fmt.Errorf("direct leaf %s not found in leaf map", leafID)
		}
		signingJob, err := buildSigningJobForRefund(ctx, job, leaf, leaf.DirectTx, sendTransferJobID(transferID, leafID, "direct"), adaptorKeys.directAdaptorPubKey)
		if err != nil {
			return nil, fmt.Errorf("build direct signing job for leaf %s: %w", leafID, err)
		}
		out[leafID].direct = signingJob
		out[leafID].directUserSig = job.Inputs()[0].UserSignature()
	}
	for _, job := range pkg.DirectFromCPFPLeavesToSend() {
		leafID := job.LeafID().String()
		leaf, ok := leafMap[leafID]
		if !ok {
			return nil, fmt.Errorf("direct-from-cpfp leaf %s not found in leaf map", leafID)
		}
		signingJob, err := buildSigningJobForRefund(ctx, job, leaf, leaf.RawTx, sendTransferJobID(transferID, leafID, "directFromCpfp"), adaptorKeys.directFromCpfpAdaptorPubKey)
		if err != nil {
			return nil, fmt.Errorf("build direct-from-cpfp signing job for leaf %s: %w", leafID, err)
		}
		out[leafID].dfc = signingJob
		out[leafID].dfcUserSig = job.Inputs()[0].UserSignature()
	}
	return out, nil
}

// buildSigningJobForRefund builds a single FROST signing-job helper for one
// refund variant. parentTxBytes is the tx whose vout 0 is being spent
// (leaf.RawTx for cpfp + direct-from-cpfp; leaf.DirectTx for direct). The
// refund tx and the user/operator commitments are already parsed on job (by
// transfer.ParsePackage); this only computes the sighash and assembles the
// job. A non-zero adaptorPubKey is attached to the job so FROST round-2
// shares embed the adaptor point (swap v3); the zero value leaves the job a
// normal signing job.
func buildSigningJobForRefund(
	ctx context.Context,
	job *transferpkg.RefundSigningJob,
	leaf *ent.TreeNode,
	parentTxBytes []byte,
	jobID uuid.UUID,
	adaptorPubKey keys.Public,
) (*helper.SigningJobWithPregeneratedNonce, error) {
	refundTx := job.RefundTx()
	parentTx, err := common.TxFromRawTxBytes(parentTxBytes)
	if err != nil {
		return nil, fmt.Errorf("unable to parse parent tx: %w", err)
	}
	if len(parentTx.TxOut) == 0 {
		return nil, fmt.Errorf("parent tx has no outputs")
	}
	if len(refundTx.TxIn) != 1 {
		return nil, fmt.Errorf("refund tx must have exactly 1 input, got %d", len(refundTx.TxIn))
	}
	expectedOutPoint := wire.OutPoint{Hash: parentTx.TxHash(), Index: 0}
	if refundTx.TxIn[0].PreviousOutPoint != expectedOutPoint {
		return nil, fmt.Errorf("refund tx input 0 must spend parent tx output 0")
	}
	sigHash, err := sighash.FromTx(refundTx, 0, parentTx.TxOut[0])
	if err != nil {
		return nil, fmt.Errorf("compute sighash: %w", err)
	}

	input := job.Inputs()[0]
	userCommitment := input.SigningNonceCommitment()

	round1 := input.SigningCommitments()
	if len(round1) == 0 {
		return nil, fmt.Errorf("missing signing_commitments")
	}
	for opID, c := range round1 {
		if c.IsZero() {
			return nil, fmt.Errorf("round1 commitment for %s is zero", opID)
		}
	}

	// This runs three times per leaf, so callers batch-load the
	// SigningKeyshare edge; the query is only a fallback.
	var signingKeyshareID uuid.UUID
	if ks := leaf.Edges.SigningKeyshare; ks != nil {
		signingKeyshareID = ks.ID
	} else if signingKeyshareID, err = leaf.QuerySigningKeyshare().OnlyID(ctx); err != nil {
		return nil, fmt.Errorf("unable to load signing keyshare for leaf %s: %w", leaf.ID, err)
	}
	var adaptorPubKeyRef *keys.Public
	if !adaptorPubKey.IsZero() {
		adaptorPubKeyRef = &adaptorPubKey
	}
	return &helper.SigningJobWithPregeneratedNonce{
		SigningJob: helper.SigningJob{
			JobID:             jobID,
			SigningKeyshareID: signingKeyshareID,
			Message:           sigHash,
			VerifyingKey:      new(leaf.VerifyingPubkey),
			UserCommitment:    &userCommitment,
			AdaptorPublicKey:  adaptorPubKeyRef,
		},
		Round1Packages: round1,
	}, nil
}

// buildSendTransferLocalSigningJobs constructs the *pbinternal.SigningJob list
// each SO feeds into its local FrostRound2 handler during Prepare. Mirrors
// buildSendTransferAggregationJobs but produces the internal proto shape.
// adaptorKeys carries the per-variant adaptor public keys (zero values for
// flows without adaptor signatures; set by the swap v3 flows).
func buildSendTransferLocalSigningJobs(
	ctx context.Context,
	transferID uuid.UUID,
	pkg *transferpkg.Package,
	leafMap map[string]*ent.TreeNode,
	adaptorKeys TransferAdaptorPublicKeys,
) ([]*pbinternal.SigningJob, error) {
	jobs := make([]*pbinternal.SigningJob, 0)
	addJob := func(job *transferpkg.RefundSigningJob, txKind string, parentTxBytes []byte, adaptorPubKey keys.Public) error {
		leafID := job.LeafID().String()
		leaf, ok := leafMap[leafID]
		if !ok {
			return fmt.Errorf("leaf %s not found in leaf map", leafID)
		}
		helperJob, err := buildSigningJobForRefund(ctx, job, leaf, parentTxBytes, sendTransferJobID(transferID, leafID, txKind), adaptorPubKey)
		if err != nil {
			return err
		}
		marshalled, err := marshalSigningJobHelper(helperJob)
		if err != nil {
			return err
		}
		jobs = append(jobs, marshalled)
		return nil
	}
	for _, job := range pkg.LeavesToSend() {
		leafID := job.LeafID().String()
		leaf, ok := leafMap[leafID]
		if !ok {
			return nil, fmt.Errorf("cpfp leaf %s not found", leafID)
		}
		if err := addJob(job, "cpfp", leaf.RawTx, adaptorKeys.cpfpAdaptorPubKey); err != nil {
			return nil, fmt.Errorf("build cpfp signing job for leaf %s: %w", leafID, err)
		}
	}
	for _, job := range pkg.DirectLeavesToSend() {
		leafID := job.LeafID().String()
		leaf, ok := leafMap[leafID]
		if !ok {
			return nil, fmt.Errorf("direct leaf %s not found", leafID)
		}
		if err := addJob(job, "direct", leaf.DirectTx, adaptorKeys.directAdaptorPubKey); err != nil {
			return nil, fmt.Errorf("build direct signing job for leaf %s: %w", leafID, err)
		}
	}
	for _, job := range pkg.DirectFromCPFPLeavesToSend() {
		leafID := job.LeafID().String()
		leaf, ok := leafMap[leafID]
		if !ok {
			return nil, fmt.Errorf("direct-from-cpfp leaf %s not found", leafID)
		}
		if err := addJob(job, "directFromCpfp", leaf.RawTx, adaptorKeys.directFromCpfpAdaptorPubKey); err != nil {
			return nil, fmt.Errorf("build direct-from-cpfp signing job for leaf %s: %w", leafID, err)
		}
	}
	return jobs, nil
}

// filterJobsForThisOperator drops jobs whose round1 commitments don't include
// this SO's identifier. Threshold signing only requires t-of-n SOs to
// participate; the rest skip local FROST round-2 and contribute nil to the
// engine's collected results.
func filterJobsForThisOperator(jobs []*pbinternal.SigningJob, identifier string) []*pbinternal.SigningJob {
	filtered := make([]*pbinternal.SigningJob, 0, len(jobs))
	for _, job := range jobs {
		if _, ok := job.GetCommitments()[identifier]; ok {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

// splitLeafSignatures fans out the commit payload's per-leaf signatures into
// the three maps UpdateTransferLeavesSignatures expects.
func splitLeafSignatures(sigs []*pbinternal.SendTransferLeafSignatures) (cpfp, direct, dfc map[string][]byte) {
	cpfp = make(map[string][]byte, len(sigs))
	direct = make(map[string][]byte, len(sigs))
	dfc = make(map[string][]byte, len(sigs))
	for _, s := range sigs {
		if len(s.GetRefundSignature()) > 0 {
			cpfp[s.GetLeafId()] = s.GetRefundSignature()
		}
		if len(s.GetDirectRefundSignature()) > 0 {
			direct[s.GetLeafId()] = s.GetDirectRefundSignature()
		}
		if len(s.GetDirectFromCpfpRefundSignature()) > 0 {
			dfc[s.GetLeafId()] = s.GetDirectFromCpfpRefundSignature()
		}
	}
	return cpfp, direct, dfc
}
