package handler

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/sighash"
	"github.com/lightsparkdev/spark/common/uuids"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/cooperativeexit"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/partner"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// CoopExitFlowHandler — participant side (Prepare / Commit / Rollback)
// ---------------------------------------------------------------------------

// CoopExitFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_COOP_EXIT (the single-call cooperative-exit-with-
// transfer-package flow). It mirrors SendTransferFlowHandler but with two
// coop-exit specifics:
//
//   - Refund txs spend two inputs (leaf output + SSP connector output), so the
//     FROST sighash and signature verification use the multi-input path.
//   - Key tweaks are NOT applied in Commit. They stay stored in
//     transfer_leaf.key_tweak; the chain watcher (tweakKeysForCoopExit) applies
//     them only after the exit tx confirms on-chain. Prepare leaves the transfer
//     at SENDER_INITIATED — a pre-commit status the watcher leaves untouched —
//     and Commit promotes it to SENDER_KEY_TWEAK_PENDING once the refund
//     signatures land.
//
// Reached via the engine when CooperativeExitV2 routes through it (gated on
// KnobUseConsensusCoopExit).
type CoopExitFlowHandler struct {
	*TransferHandler
}

var _ consensus.FlowHandler = (*CoopExitFlowHandler)(nil)

func NewCoopExitFlowHandler(config *so.Config) *CoopExitFlowHandler {
	return &CoopExitFlowHandler{TransferHandler: NewTransferHandler(config)}
}

// Prepare runs on every SO. It validates the exit_txid<->connector_tx binding
// and the transfer package, decrypts this SO's slice of the sender key tweaks,
// persists Transfer + TransferLeaf + CooperativeExit rows in the pre-commit
// SENDER_INITIATED state (refund txs stored UNSIGNED, key tweaks stored but not
// applied), and produces local FROST round-2 signature shares over the
// connector-spending refund transactions.
//
// SOs outside the signing set still write rows; they return nil shares.
func (h *CoopExitFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	req, ok := op.(*pbinternal.CoopExitPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for coop exit prepare", op)
	}
	orig := req.GetOriginalRequest()
	if orig == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("original_request is required"))
	}
	parsed, err := parseCoopExitRequest(orig)
	if err != nil {
		return nil, err
	}

	// exit_txid + connector binding before any DB write or FROST work.
	exitTxid, err := parseAndValidateCoopExitTxid(ctx, orig.Transfer.TransferId, orig.ExitTxid, orig.GetConnectorTx())
	if err != nil {
		return nil, err
	}

	keyTweakMap, err := h.ValidateTransferPackage(ctx, parsed.transferID, orig.Transfer.TransferPackage, parsed.senderIDPK, true)
	if err != nil {
		return nil, fmt.Errorf("failed to validate transfer package for coop exit %s: %w", parsed.transferID, err)
	}
	if len(keyTweakMap) == 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer package contains no key tweaks"))
	}

	cpfpMap, directMap, dfcMap := loadLeafRefundMaps(orig.Transfer)

	// TransferRoleParticipant on every SO (including the coordinator): the
	// engine's FlowExecution row tracks role, so we don't need the legacy
	// coordinator/participant status split. createTransfer stores the key tweaks
	// and yields SENDER_KEY_TWEAK_PENDING for Participant+tweaks; we override it
	// to SENDER_INITIATED below — a pre-commit state the chain watcher's
	// tweakKeysForCoopExit deliberately leaves untouched, so keys can't be
	// tweaked before Commit applies the refund signatures and promotes the
	// transfer to SENDER_KEY_TWEAK_PENDING.
	transfer, leafMap, err := h.createTransfer(
		ctx, parsed.transferID, nil, st.TransferTypeCooperativeExit,
		orig.Transfer.ExpiryTime.AsTime(), parsed.senderIDPK, parsed.receiverIDPK,
		cpfpMap, directMap, dfcMap,
		keyTweakMap,
		TransferRoleParticipant,
		true, /* requireDirectTx */
		"", uuid.Nil, orig.GetConnectorTx(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer rows for coop exit %s: %w", parsed.transferID, err)
	}

	// createTransferLeaves stores only the key_tweak blob. Persist the
	// secret_cipher + signature alongside it so the receiver can verify and
	// decrypt the leaf once the chain watcher applies the tweak — the watcher's
	// tweakKeysForCoopExit clears key_tweak but never sets these. The legacy
	// package path does the same via setSoCoordinatorKeyTweaks (coordinator) and
	// the InitiateCooperativeExit participant write.
	if err := h.setSoCoordinatorKeyTweaks(ctx, transfer, keyTweakMap); err != nil {
		return nil, fmt.Errorf("failed to persist key tweak cipher/signature for coop exit %s: %w", parsed.transferID, err)
	}

	if _, err := transfer.Update().SetStatus(st.TransferStatusSenderInitiated).Save(ctx); err != nil {
		return nil, fmt.Errorf("failed to set sender-initiated status for coop exit %s: %w", parsed.transferID, err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := db.CooperativeExit.Create().
		SetID(parsed.exitID).
		SetTransfer(transfer).
		SetExitTxid(exitTxid).
		Save(ctx); err != nil {
		return nil, fmt.Errorf("failed to create cooperative exit %s for transfer %s: %w", parsed.exitID, parsed.transferID, err)
	}

	connectorPrevOuts, err := parseConnectorTxOutputs(orig.GetConnectorTx())
	if err != nil {
		return nil, fmt.Errorf("unable to parse connector tx: %w", err)
	}

	jobs, err := buildCoopExitLocalSigningJobs(ctx, parsed.transferID, orig.Transfer.TransferPackage, leafMap, connectorPrevOuts)
	if err != nil {
		return nil, fmt.Errorf("failed to build local signing jobs: %w", err)
	}
	jobs = filterJobsForThisOperator(jobs, h.config.Identifier)
	if len(jobs) == 0 {
		// Not in the signing set for any leaf — nothing to do for FROST.
		return nil, nil
	}

	frostHandler := signing_handler.NewFrostSigningHandler(h.config)
	frostResp, err := frostHandler.FrostRound2(ctx, &pbinternal.FrostRound2Request{SigningJobs: jobs})
	if err != nil {
		return nil, fmt.Errorf("local frost round 2 failed during coop exit prepare: %w", err)
	}
	return frostResp, nil
}

// Commit runs on every participant (the coordinator's equivalent work lives in
// BuildCommitPayload). It applies the aggregated refund signatures to the
// TransferLeaf rows written in Prepare and promotes the transfer to
// SENDER_KEY_TWEAK_PENDING. It does NOT apply the sender key tweaks — those
// stay stored in transfer_leaf.key_tweak for the chain watcher to apply after
// the exit tx confirms on-chain.
func (h *CoopExitFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.CoopExitCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for coop exit commit", op)
	}
	return h.applyCoopExitCommit(ctx, req)
}

// Rollback runs on every participant (and on the coordinator if Prepare or
// BuildCommitPayload fails). It marks the transfer RETURNED via
// executeCancelTransfer and deletes the cooperative_exit row so the chain
// watcher no longer considers this exit pending (its unconfirmed-exit query
// would otherwise keep matching the row). Idempotent: a never-created transfer
// is a no-op, and a non-pre-commit transfer short-circuits in rollbackCoopExit.
//
// Accepts both CoopExitRollbackRequest (the normal rollback payload) and
// CoopExitPrepareRequest (the prepare op echoed back by the reconciler when the
// coordinator's row was lost).
func (h *CoopExitFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var transferIDStr string
	switch r := op.(type) {
	case *pbinternal.CoopExitRollbackRequest:
		transferIDStr = r.GetTransferId()
	case *pbinternal.CoopExitPrepareRequest:
		if r.GetOriginalRequest() != nil && r.GetOriginalRequest().GetTransfer() != nil {
			transferIDStr = r.GetOriginalRequest().GetTransfer().GetTransferId()
		}
	default:
		return fmt.Errorf("unexpected operation type %T for coop exit rollback", op)
	}
	if transferIDStr == "" {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_id is required for rollback"))
	}
	transferID, err := uuid.Parse(transferIDStr)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}
	return h.rollbackCoopExit(ctx, transferID)
}

// applyCoopExitCommit applies aggregated refund signatures on a single SO and
// promotes the transfer to SENDER_KEY_TWEAK_PENDING. Shared by participant
// Commit and coordinator BuildCommitPayload.
//
// Idempotent against replayed commit gossip: it proceeds only while the
// transfer is still SENDER_INITIATED (the pre-commit state set in Prepare). A
// replayed delivery finds the status already promoted (SENDER_KEY_TWEAK_PENDING,
// or SENDER_KEY_TWEAKED once the chain watcher ran) and short-circuits before
// UpdateTransferLeavesSignatures runs over already-signed refund tx bytes.
// The gate is intentionally exact (only SENDER_INITIATED) rather than the
// multi-status set applySendTransferCommit uses: every consensus coop exit is
// written by Prepare at SENDER_INITIATED, so any other status — including a
// legacy SENDER_INITIATED_COORDINATOR that somehow reached this commit path — is
// treated as already-committed and skipped rather than re-applied.
func (h *CoopExitFlowHandler) applyCoopExitCommit(ctx context.Context, req *pbinternal.CoopExitCommitRequest) error {
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}

	transferEnt, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		// NotFound is surfaced as an error (not a no-op like rollback): the engine
		// only dispatches Commit after every SO's Prepare succeeded, and Prepare
		// writes the transfer row + the participant FlowExecution row in the same
		// tx. A missing transfer here means a committed flow lost this SO's
		// prepared state — a real invariant violation worth surfacing so the
		// reconciler keeps retrying rather than silently marking it terminal.
		return fmt.Errorf("unable to load transfer %s for coop exit commit: %w", transferID, err)
	}

	if transferEnt.Status != st.TransferStatusSenderInitiated {
		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"coop exit 2pc commit: transfer %s already past pre-commit (status=%s), treating as idempotent retry",
			transferID, transferEnt.Status)
		return nil
	}

	cpfpSigs, directSigs, dfcSigs := splitLeafSignatures(req.GetLeafSignatures())
	if err := h.UpdateTransferLeavesSignatures(ctx, transferEnt, cpfpSigs, directSigs, dfcSigs, req.GetConnectorTx()); err != nil {
		return fmt.Errorf("unable to apply refund signatures for coop exit %s: %w", transferID, err)
	}

	if _, err := transferEnt.Update().SetStatus(st.TransferStatusSenderKeyTweakPending).Save(ctx); err != nil {
		return fmt.Errorf("unable to promote coop exit %s to key-tweak-pending: %w", transferID, err)
	}
	return nil
}

// rollbackCoopExit transitions the transfer to RETURNED, unlocks the leaves
// (executeCancelTransfer — the same mechanism the legacy cancel path uses), and
// deletes the cooperative_exit row.
//
// It only acts while the transfer is still SENDER_INITIATED (the pre-commit
// state Prepare wrote). Any other status is a no-op, which keeps it safe and
// idempotent against:
//   - Prepare never ran on this SO (NotFound)
//   - a replayed/stale rollback after this flow already committed
//     (SENDER_KEY_TWEAK_PENDING) — we must NOT cancel it, even though
//     executeCancelTransfer would accept that status, since that would undo a
//     committed exit and unlock leaves the SSP is about to claim
//   - the chain watcher having advanced a committed exit to SENDER_KEY_TWEAKED,
//     or a prior rollback having reached RETURNED
//
// In correct operation the engine sends a participant either Commit or Rollback
// (never both), so a ROLLED_BACK flow is always still SENDER_INITIATED here; the
// guard is defense against reconciler/sweep races.
func (h *CoopExitFlowHandler) rollbackCoopExit(ctx context.Context, transferID uuid.UUID) error {
	transferEnt, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("unable to load transfer %s for coop exit rollback: %w", transferID, err)
	}
	if transferEnt.Status != st.TransferStatusSenderInitiated {
		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"coop exit 2pc rollback: transfer %s in non-pre-commit status %s, skipping", transferID, transferEnt.Status)
		return nil
	}
	if err := h.executeCancelTransfer(ctx, transferEnt); err != nil {
		return fmt.Errorf("unable to cancel transfer %s during coop exit rollback: %w", transferID, err)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	if _, err := db.CooperativeExit.Delete().
		Where(cooperativeexit.HasTransferWith(enttransfer.ID(transferID))).
		Exec(ctx); err != nil {
		return fmt.Errorf("unable to delete cooperative exit for transfer %s during rollback: %w", transferID, err)
	}
	logging.GetLoggerFromContext(ctx).Sugar().Infof("coop exit 2pc rollback: transfer %s marked RETURNED", transferID)
	return nil
}

// ---------------------------------------------------------------------------
// coopExitCoordinatorFlow — coordinator side
// ---------------------------------------------------------------------------

// coopExitCoordinatorFlow drives the coordinator-side of a cooperative exit
// through the 2PC engine. Prepare/Commit/Rollback are delegated to the
// participant-side CoopExitFlowHandler (the engine calls Prepare on the
// coordinator too); BuildCommitPayload is where coordinator-only work lives —
// aggregating FROST shares, applying signatures locally, and returning the
// commit payload gossiped to participants.
type coopExitCoordinatorFlow struct {
	*CoopExitFlowHandler

	req               *pb.CooperativeExitRequest
	parsed            parsedCoopExitRequest
	connectorTx       []byte
	signingJobsByLeaf map[string]*sendTransferLeafSigningJobs

	// response is populated during BuildCommitPayload so the public
	// CooperativeExitV2 handler can return it after engine.Execute completes.
	response *pb.CooperativeExitResponse
}

var _ consensus.CoordinatorFlow = (*coopExitCoordinatorFlow)(nil)

// PrepareOp returns the prepare request sent to every SO (engine fans this out).
func (f *coopExitCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.CoopExitPrepareRequest{OriginalRequest: f.req}
}

// BuildCommitPayload aggregates FROST shares from all SOs, applies the resulting
// refund signatures on the coordinator (promoting it to SENDER_KEY_TWEAK_PENDING),
// builds the response, and returns the commit payload (aggregated signatures +
// connector_tx) for the engine to gossip to participants.
func (f *coopExitCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	allShares, _, err := collectSignatureShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	leafSignatures := make([]*pbinternal.SendTransferLeafSignatures, 0, len(f.signingJobsByLeaf))

	frostConn, err := f.config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	leafIDs := make([]string, 0, len(f.signingJobsByLeaf))
	for id := range f.signingJobsByLeaf {
		leafIDs = append(leafIDs, id)
	}
	slices.Sort(leafIDs)

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

	// Apply on the coordinator's DB now so the request tx the engine commits
	// next carries the final transfer state. Participants do the same work via
	// FlowHandler.Commit after receiving the commit gossip.
	commitReq := &pbinternal.CoopExitCommitRequest{
		TransferId:     f.parsed.transferID.String(),
		LeafSignatures: leafSignatures,
		ConnectorTx:    f.connectorTx,
	}
	if err := f.applyCoopExitCommit(ctx, commitReq); err != nil {
		return nil, fmt.Errorf("failed to apply commit on coordinator: %w", err)
	}

	// Coordinator-only partner attribution, matching the legacy package path
	// (cooperativeExitWithTransferPackage). Runs here — in the request ctx (which
	// carries the partner JWT) and the request tx — before the engine's DbCommit,
	// since SaveTransferPartner reads partner info from context and is a no-op on
	// participants. Fire-and-forget: it logs and never blocks.
	partner.SaveTransferPartner(ctx, f.parsed.transferID, st.TransferPartnerTypeCooperativeExit)

	transferEnt, err := f.loadTransferForUpdate(ctx, f.parsed.transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to reload transfer %s after commit: %w", f.parsed.transferID, err)
	}
	transferProto, err := transferEnt.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer %s for response: %w", f.parsed.transferID, err)
	}
	// SigningResults is nil to match the legacy single-call package path
	// (cooperativeExitWithTransferPackage): the SDK's package flow does not
	// consume per-leaf signing results from the response.
	f.response = &pb.CooperativeExitResponse{Transfer: transferProto, SigningResults: nil}

	return commitReq, nil
}

// RollbackPayload returns the minimal payload sent to participants on rollback.
func (f *coopExitCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.CoopExitRollbackRequest{TransferId: f.parsed.transferID.String()}
}

// buildCoopExitCoordinatorFlow validates the request and pre-computes the
// connector-aware signing-job helpers the coordinator needs during
// BuildCommitPayload's aggregation. The coordinator's own DB writes
// (createTransfer, CooperativeExit row, FROST round-2) happen inside
// engine.Execute via the engine-driven Prepare phase.
func buildCoopExitCoordinatorFlow(ctx context.Context, config *so.Config, req *pb.CooperativeExitRequest) (*coopExitCoordinatorFlow, error) {
	parsed, err := parseCoopExitRequest(req)
	if err != nil {
		return nil, err
	}

	// Fast-fail the exit_txid<->connector_tx binding on the coordinator before
	// the engine fans Prepare out to every SO. Prepare re-runs this on each SO
	// (it's the authoritative gate, before any DB write), but rejecting a
	// malformed/malicious binding here avoids a wasted RPC round-trip across the
	// cluster. Mirrors send transfer's fast-fail structural validation.
	if _, err := parseAndValidateCoopExitTxid(ctx, req.Transfer.TransferId, req.ExitTxid, req.GetConnectorTx()); err != nil {
		return nil, err
	}

	pkg := req.Transfer.TransferPackage
	cpfpMap, directMap, dfcMap := loadLeafRefundMapsFromTransferPackage(pkg)
	// Union of all three refund maps so direct/dfc-only leaves are still loaded;
	// values are last-writer-wins, but only the keys are consumed downstream.
	leafRefundUnion := make(map[string][]byte, len(cpfpMap))
	maps.Copy(leafRefundUnion, cpfpMap)
	maps.Copy(leafRefundUnion, directMap)
	maps.Copy(leafRefundUnion, dfcMap)
	leafUUIDs, err := uuids.ParseSeq(maps.Keys(leafRefundUnion))
	if err != nil {
		return nil, fmt.Errorf("unable to parse leaf IDs for coop exit coordinator flow: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	// Non-locking preload (mirrors buildSendTransferCoordinatorFlow): the
	// engine's Prepare phase re-loads these under FOR UPDATE before mutating.
	leaves, err := db.TreeNode.Query().
		Where(treenode.IDIn(leafUUIDs...)).
		WithTree().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to preload leaves for coop exit coordinator flow: %w", err)
	}
	if len(leaves) != len(leafRefundUnion) {
		return nil, fmt.Errorf("preload missed leaves: got %d, want %d", len(leaves), len(leafRefundUnion))
	}
	leafMap := make(map[string]*ent.TreeNode, len(leaves))
	for _, leaf := range leaves {
		leafMap[leaf.ID.String()] = leaf
	}

	connectorPrevOuts, err := parseConnectorTxOutputs(req.GetConnectorTx())
	if err != nil {
		return nil, fmt.Errorf("unable to parse connector tx: %w", err)
	}

	jobsByLeaf, err := buildCoopExitAggregationJobs(ctx, parsed.transferID, pkg, leafMap, connectorPrevOuts)
	if err != nil {
		return nil, fmt.Errorf("unable to build coop exit signing-job helpers: %w", err)
	}

	return &coopExitCoordinatorFlow{
		CoopExitFlowHandler: NewCoopExitFlowHandler(config),
		req:                 req,
		parsed:              parsed,
		connectorTx:         req.GetConnectorTx(),
		signingJobsByLeaf:   jobsByLeaf,
	}, nil
}

// ---------------------------------------------------------------------------
// Parsing + validation
// ---------------------------------------------------------------------------

type parsedCoopExitRequest struct {
	transferID   uuid.UUID
	exitID       uuid.UUID
	senderIDPK   keys.Public
	receiverIDPK keys.Public
}

// parseCoopExitRequest extracts and validates the structural fields shared by
// every call site (Prepare on each SO, buildCoopExitCoordinatorFlow). It only
// handles the TransferPackage (single-call) path — the consensus route is gated
// to that path in CooperativeExitV2.
func parseCoopExitRequest(req *pb.CooperativeExitRequest) (parsedCoopExitRequest, error) {
	var empty parsedCoopExitRequest
	if req == nil {
		return empty, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}
	if req.Transfer == nil {
		return empty, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer is required"))
	}
	if req.Transfer.TransferPackage == nil {
		return empty, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_package is required for consensus coop exit"))
	}
	if len(req.GetConnectorTx()) == 0 {
		return empty, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("connector_tx is required for cooperative exit"))
	}
	transferID, err := uuid.Parse(req.Transfer.TransferId)
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer id: %w", err))
	}
	exitID, err := uuid.Parse(req.ExitId)
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid exit id: %w", err))
	}
	if req.Transfer.ExpiryTime == nil {
		return empty, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("expiry_time is required for transfer %s", transferID))
	}
	senderIDPK, err := keys.ParsePublicKey(req.Transfer.OwnerIdentityPublicKey)
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid owner identity public key: %w", err))
	}
	receiverIDPK, err := keys.ParsePublicKey(req.Transfer.ReceiverIdentityPublicKey)
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid receiver identity public key: %w", err))
	}
	return parsedCoopExitRequest{
		transferID:   transferID,
		exitID:       exitID,
		senderIDPK:   senderIDPK,
		receiverIDPK: receiverIDPK,
	}, nil
}

// ---------------------------------------------------------------------------
// Connector-aware signing-job construction
// ---------------------------------------------------------------------------

// coopExitSigningJobNamespace is a fixed UUIDv4 mixed into NewSHA1 to produce
// deterministic per-leaf-per-tx-variant job IDs that don't collide with the
// send-transfer namespace.
var coopExitSigningJobNamespace = uuid.MustParse("8f2a1d4c-3b6e-4c9a-8d7f-1e0b2c5a6d3e")

func coopExitJobID(transferID uuid.UUID, leafID string, txKind string) uuid.UUID {
	return uuid.NewSHA1(coopExitSigningJobNamespace, fmt.Appendf(nil, "%s:%s:%s", transferID.String(), leafID, txKind))
}

// buildCoopExitAggregationJobs constructs the per-leaf signing-job helpers the
// coordinator uses for FROST aggregation. Mirrors buildSendTransferAggregationJobs
// but builds connector-aware (multi-input) sighashes.
func buildCoopExitAggregationJobs(
	ctx context.Context,
	transferID uuid.UUID,
	pkg *pb.TransferPackage,
	leafMap map[string]*ent.TreeNode,
	connectorPrevOuts map[wire.OutPoint]*wire.TxOut,
) (map[string]*sendTransferLeafSigningJobs, error) {
	out := make(map[string]*sendTransferLeafSigningJobs, len(leafMap))
	for _, leaf := range leafMap {
		out[leaf.ID.String()] = &sendTransferLeafSigningJobs{leaf: leaf}
	}
	for _, req := range pkg.GetLeavesToSend() {
		leaf, ok := leafMap[req.LeafId]
		if !ok {
			return nil, fmt.Errorf("cpfp leaf %s not found in leaf map", req.LeafId)
		}
		job, err := buildCoopExitSigningJobForRefund(ctx, req, leaf, leaf.RawTx, coopExitJobID(transferID, leaf.ID.String(), "cpfp"), connectorPrevOuts)
		if err != nil {
			return nil, fmt.Errorf("build cpfp signing job for leaf %s: %w", req.LeafId, err)
		}
		out[req.LeafId].cpfp = job
		out[req.LeafId].cpfpUserSig = req.UserSignature
	}
	for _, req := range pkg.GetDirectLeavesToSend() {
		leaf, ok := leafMap[req.LeafId]
		if !ok {
			return nil, fmt.Errorf("direct leaf %s not found in leaf map", req.LeafId)
		}
		job, err := buildCoopExitSigningJobForRefund(ctx, req, leaf, leaf.DirectTx, coopExitJobID(transferID, leaf.ID.String(), "direct"), connectorPrevOuts)
		if err != nil {
			return nil, fmt.Errorf("build direct signing job for leaf %s: %w", req.LeafId, err)
		}
		out[req.LeafId].direct = job
		out[req.LeafId].directUserSig = req.UserSignature
	}
	for _, req := range pkg.GetDirectFromCpfpLeavesToSend() {
		leaf, ok := leafMap[req.LeafId]
		if !ok {
			return nil, fmt.Errorf("direct-from-cpfp leaf %s not found in leaf map", req.LeafId)
		}
		job, err := buildCoopExitSigningJobForRefund(ctx, req, leaf, leaf.RawTx, coopExitJobID(transferID, leaf.ID.String(), "directFromCpfp"), connectorPrevOuts)
		if err != nil {
			return nil, fmt.Errorf("build direct-from-cpfp signing job for leaf %s: %w", req.LeafId, err)
		}
		out[req.LeafId].dfc = job
		out[req.LeafId].dfcUserSig = req.UserSignature
	}
	return out, nil
}

// buildCoopExitLocalSigningJobs constructs the *pbinternal.SigningJob list each
// SO feeds into its local FrostRound2 handler during Prepare. Mirrors
// buildCoopExitAggregationJobs but produces the internal proto shape.
func buildCoopExitLocalSigningJobs(
	ctx context.Context,
	transferID uuid.UUID,
	pkg *pb.TransferPackage,
	leafMap map[string]*ent.TreeNode,
	connectorPrevOuts map[wire.OutPoint]*wire.TxOut,
) ([]*pbinternal.SigningJob, error) {
	jobs := make([]*pbinternal.SigningJob, 0)
	// addJob takes the already-resolved leaf and the parent tx bytes whose vout 0
	// the refund spends (leaf.RawTx for cpfp/direct-from-cpfp, leaf.DirectTx for
	// direct) — the caller selects parentTxBytes per variant.
	addJob := func(req *pb.UserSignedTxSigningJob, txKind string, leaf *ent.TreeNode, parentTxBytes []byte) error {
		helperJob, err := buildCoopExitSigningJobForRefund(ctx, req, leaf, parentTxBytes, coopExitJobID(transferID, leaf.ID.String(), txKind), connectorPrevOuts)
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
	for _, req := range pkg.GetLeavesToSend() {
		leaf, ok := leafMap[req.LeafId]
		if !ok {
			return nil, fmt.Errorf("cpfp leaf %s not found", req.LeafId)
		}
		if err := addJob(req, "cpfp", leaf, leaf.RawTx); err != nil {
			return nil, fmt.Errorf("build cpfp signing job for leaf %s: %w", req.LeafId, err)
		}
	}
	for _, req := range pkg.GetDirectLeavesToSend() {
		leaf, ok := leafMap[req.LeafId]
		if !ok {
			return nil, fmt.Errorf("direct leaf %s not found", req.LeafId)
		}
		if err := addJob(req, "direct", leaf, leaf.DirectTx); err != nil {
			return nil, fmt.Errorf("build direct signing job for leaf %s: %w", req.LeafId, err)
		}
	}
	for _, req := range pkg.GetDirectFromCpfpLeavesToSend() {
		leaf, ok := leafMap[req.LeafId]
		if !ok {
			return nil, fmt.Errorf("direct-from-cpfp leaf %s not found", req.LeafId)
		}
		if err := addJob(req, "directFromCpfp", leaf, leaf.RawTx); err != nil {
			return nil, fmt.Errorf("build direct-from-cpfp signing job for leaf %s: %w", req.LeafId, err)
		}
	}
	return jobs, nil
}

// buildCoopExitSigningJobForRefund builds a single FROST signing-job helper for
// one coop-exit refund variant. Unlike buildSigningJobForRefund (send transfer),
// it computes a multi-input sighash when the refund spends the connector output
// as a second input — mirroring the prevOuts construction in
// SignRefundsWithPregeneratedNonce. parentTxBytes is the tx whose vout 0 is
// being spent by the refund's input 0 (leaf.RawTx for cpfp + direct-from-cpfp;
// leaf.DirectTx for direct).
func buildCoopExitSigningJobForRefund(
	ctx context.Context,
	req *pb.UserSignedTxSigningJob,
	leaf *ent.TreeNode,
	parentTxBytes []byte,
	jobID uuid.UUID,
	connectorPrevOuts map[wire.OutPoint]*wire.TxOut,
) (*helper.SigningJobWithPregeneratedNonce, error) {
	refundTx, err := common.TxFromRawTxBytes(req.GetRawTx())
	if err != nil {
		return nil, fmt.Errorf("unable to parse refund tx: %w", err)
	}
	parentTx, err := common.TxFromRawTxBytes(parentTxBytes)
	if err != nil {
		return nil, fmt.Errorf("unable to parse parent tx: %w", err)
	}
	if len(parentTx.TxOut) == 0 {
		return nil, fmt.Errorf("parent tx has no outputs")
	}
	if err := validateRefundInputCountForConnector(refundTx, connectorPrevOuts, "coop-exit"); err != nil {
		return nil, err
	}
	expectedOutPoint := wire.OutPoint{Hash: parentTx.TxHash(), Index: 0}
	if refundTx.TxIn[0].PreviousOutPoint != expectedOutPoint {
		return nil, fmt.Errorf("refund tx input 0 must spend parent tx output 0")
	}

	var sigHash sighash.Hash
	if len(refundTx.TxIn) > 1 && connectorPrevOuts != nil {
		prevOuts := make(map[wire.OutPoint]*wire.TxOut, 2)
		prevOuts[expectedOutPoint] = parentTx.TxOut[0]
		connectorOutpoint := refundTx.TxIn[1].PreviousOutPoint
		connectorTxOut, ok := connectorPrevOuts[connectorOutpoint]
		if !ok {
			return nil, fmt.Errorf("refund tx input 1 does not reference a valid connector output: %v", connectorOutpoint)
		}
		prevOuts[connectorOutpoint] = connectorTxOut
		sigHash, err = sighash.FromMultiPrevOutTx(refundTx, 0, prevOuts)
	} else {
		sigHash, err = sighash.FromTx(refundTx, 0, parentTx.TxOut[0])
	}
	if err != nil {
		return nil, fmt.Errorf("compute sighash: %w", err)
	}

	userCommitment := frost.SigningCommitment{}
	if err := userCommitment.UnmarshalProto(req.GetSigningNonceCommitment()); err != nil {
		return nil, fmt.Errorf("unmarshal user nonce commitment: %w", err)
	}

	round1 := make(map[string]frost.SigningCommitment)
	signingCommitments := req.GetSigningCommitments()
	if signingCommitments == nil {
		return nil, fmt.Errorf("missing signing_commitments")
	}
	for opID, commitment := range signingCommitments.GetSigningCommitments() {
		c := frost.SigningCommitment{}
		if err := c.UnmarshalProto(commitment); err != nil {
			return nil, fmt.Errorf("unmarshal round1 commitment for %s: %w", opID, err)
		}
		if c.IsZero() {
			return nil, fmt.Errorf("round1 commitment for %s is zero", opID)
		}
		round1[opID] = c
	}

	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load signing keyshare for leaf %s: %w", leaf.ID, err)
	}
	return &helper.SigningJobWithPregeneratedNonce{
		SigningJob: helper.SigningJob{
			JobID:             jobID,
			SigningKeyshareID: signingKeyshare.ID,
			Message:           sigHash,
			VerifyingKey:      new(leaf.VerifyingPubkey),
			UserCommitment:    &userCommitment,
		},
		Round1Packages: round1,
	}, nil
}
