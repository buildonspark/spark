package handler

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"github.com/lightsparkdev/spark/so/helper"
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
// Reached via the engine when StartTransferV3 routes through it (gated on
// KnobUseConsensusTransfer).
type SendTransferFlowHandler struct {
	*TransferHandler
}

var _ consensus.FlowHandler = (*SendTransferFlowHandler)(nil)

func NewSendTransferFlowHandler(config *so.Config) *SendTransferFlowHandler {
	return &SendTransferFlowHandler{TransferHandler: NewTransferHandler(config)}
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

	keyTweakMap, err := h.ValidateTransferPackage(ctx, parsed.transferID, parsed.senderPkg.TransferPackage, parsed.senderIDPK, true)
	if err != nil {
		return nil, fmt.Errorf("failed to validate transfer package: %w", err)
	}
	if len(keyTweakMap) == 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer package contains no key tweaks"))
	}

	cpfpMap, directMap, dfcMap := loadLeafRefundMapsFromTransferPackage(parsed.senderPkg.TransferPackage)

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
	_, leafMap, err := h.createTransferV3(
		ctx, parsed.transferID, parsed.senderPkg.TransferPackage, orig.GetExpiryTime().AsTime(),
		parsed.senderIDPK, parsed.receivers, parsed.leafReceiverMap,
		cpfpMap, directMap, dfcMap,
		keyTweakMap,
		TransferRoleParticipant,
		true, /* requireDirectTx */
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer rows for %s: %w", parsed.transferID, err)
	}

	jobs, err := buildSendTransferLocalSigningJobs(ctx, parsed.transferID, parsed.senderPkg.TransferPackage, leafMap)
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
// no-op, and an already-RETURNED transfer short-circuits inside
// executeCancelTransfer.
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
//   - if rollback already ran (status == RETURNED, or any other terminal
//     status) → no-op via executeCancelTransfer's own idempotent check.
func (h *SendTransferFlowHandler) rollbackSendTransfer(ctx context.Context, transferID uuid.UUID) error {
	transferEnt, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("unable to load transfer %s for rollback: %w", transferID, err)
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

	// response is populated during BuildCommitPayload so the public
	// StartTransferV3 handler can return it after engine.Execute completes.
	response *pb.StartTransferResponse
}

var _ consensus.CoordinatorFlow = (*sendTransferCoordinatorFlow)(nil)

// PrepareOp returns the prepare request sent to every SO (engine fans this out).
func (f *sendTransferCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.SendTransferPrepareRequest{OriginalRequest: f.req}
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

	leafSignatures := make([]*pbinternal.SendTransferLeafSignatures, 0, len(f.signingJobsByLeaf))

	// Sort leaves for deterministic iteration (helps tests + log reproducibility).
	leafIDs := make([]string, 0, len(f.signingJobsByLeaf))
	for id := range f.signingJobsByLeaf {
		leafIDs = append(leafIDs, id)
	}
	slices.Sort(leafIDs)

	for _, leafID := range leafIDs {
		jobs := f.signingJobsByLeaf[leafID]
		sigs := &pbinternal.SendTransferLeafSignatures{LeafId: leafID}

		if jobs.cpfp != nil {
			sig, err := f.aggregateLeafSignature(ctx, jobs.cpfp, allShares, jobs.leaf, jobs.cpfpUserSig)
			if err != nil {
				return nil, fmt.Errorf("aggregate cpfp signature for leaf %s: %w", leafID, err)
			}
			sigs.RefundSignature = sig
		}
		if jobs.direct != nil {
			sig, err := f.aggregateLeafSignature(ctx, jobs.direct, allShares, jobs.leaf, jobs.directUserSig)
			if err != nil {
				return nil, fmt.Errorf("aggregate direct signature for leaf %s: %w", leafID, err)
			}
			sigs.DirectRefundSignature = sig
		}
		if jobs.dfc != nil {
			sig, err := f.aggregateLeafSignature(ctx, jobs.dfc, allShares, jobs.leaf, jobs.dfcUserSig)
			if err != nil {
				return nil, fmt.Errorf("aggregate direct-from-cpfp signature for leaf %s: %w", leafID, err)
			}
			sigs.DirectFromCpfpRefundSignature = sig
		}
		leafSignatures = append(leafSignatures, sigs)
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

	// Build the response StartTransferV3 returns to the client.
	transferEnt, err := f.loadTransferForUpdate(ctx, f.parsed.transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to reload transfer %s after commit: %w", f.parsed.transferID, err)
	}
	transferProto, err := transferEnt.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer %s for response: %w", f.parsed.transferID, err)
	}
	f.response = &pb.StartTransferResponse{Transfer: transferProto}

	return commitReq, nil
}

// RollbackPayload returns the minimal payload sent to participants on rollback.
func (f *sendTransferCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.SendTransferRollbackRequest{TransferId: f.parsed.transferID.String()}
}

// aggregateLeafSignature drives a single FROST AggregateFrost RPC for one job.
func (f *sendTransferCoordinatorFlow) aggregateLeafSignature(
	ctx context.Context,
	job *helper.SigningJobWithPregeneratedNonce,
	allShares map[string]map[string][]byte,
	leaf *ent.TreeNode,
	userSignatureShare []byte,
) ([]byte, error) {
	keyPackage, err := ent.GetKeyPackage(ctx, f.config, job.SigningKeyshareID)
	if err != nil {
		return nil, fmt.Errorf("unable to get key package: %w", err)
	}
	shares, ok := allShares[job.JobID.String()]
	if !ok {
		return nil, fmt.Errorf("missing signature shares for job %s", job.JobID)
	}
	// Public shares must match the signing set per job, not the global
	// participant set. Different leaves can carry different round1
	// commitment sets (the user picks the t-of-n SOs per leaf), so we filter
	// from the actual contributors to this job's shares.
	publicShares := make(map[string][]byte, len(shares))
	for id := range shares {
		share, ok := keyPackage.PublicShares[id]
		if !ok {
			return nil, fmt.Errorf("missing public share for operator %s", id)
		}
		publicShares[id] = share
	}

	conn, err := f.config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer conn.Close()
	frostClient := pbfrost.NewFrostServiceClient(conn)

	userCommitment, err := job.UserCommitment.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("unable to marshal user commitment: %w", err)
	}
	roundCommitments, err := marshalRoundCommitments(job.Round1Packages)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal round1 commitments: %w", err)
	}
	resp, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            job.Message,
		SignatureShares:    shares,
		PublicShares:       publicShares,
		VerifyingKey:       leaf.VerifyingPubkey.Serialize(),
		Commitments:        roundCommitments,
		UserCommitments:    userCommitment,
		UserPublicKey:      leaf.OwnerSigningPubkey.Serialize(),
		UserSignatureShare: userSignatureShare,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to aggregate frost signature: %w", err)
	}
	return resp.Signature, nil
}

// buildSendTransferCoordinatorFlow validates the request and pre-computes the
// signing-job helpers the coordinator needs during BuildCommitPayload's
// aggregation. The coordinator's own DB writes (createTransferV3, FROST round-2)
// happen inside engine.Execute via the engine-driven Prepare phase.
//
//nolint:unused // wired in PR 3 when StartTransferV3 routes to the engine.
func buildSendTransferCoordinatorFlow(ctx context.Context, config *so.Config, req *pb.StartTransferV3Request) (*sendTransferCoordinatorFlow, error) {
	parsed, err := parseSendTransferRequest(req)
	if err != nil {
		return nil, err
	}

	// Pre-load leaves for signing-job construction. The engine's Prepare phase
	// will re-load these under FOR UPDATE before mutating them — these reads
	// are only to derive deterministic job IDs and sighashes for later
	// aggregation.
	cpfpMap, _, _ := loadLeafRefundMapsFromTransferPackage(parsed.senderPkg.TransferPackage)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	leaves, _, err := loadLeavesWithLock(ctx, db, cpfpMap)
	if err != nil {
		return nil, fmt.Errorf("unable to preload leaves for coordinator flow: %w", err)
	}
	leafMap := make(map[string]*ent.TreeNode, len(leaves))
	for _, leaf := range leaves {
		leafMap[leaf.ID.String()] = leaf
	}

	jobsByLeaf, err := buildSendTransferAggregationJobs(ctx, parsed.transferID, parsed.senderPkg.TransferPackage, leafMap)
	if err != nil {
		return nil, fmt.Errorf("unable to build signing-job helpers: %w", err)
	}

	handler := NewSendTransferFlowHandler(config)
	return &sendTransferCoordinatorFlow{
		SendTransferFlowHandler: handler,
		req:                     req,
		parsed:                  parsed,
		signingJobsByLeaf:       jobsByLeaf,
	}, nil
}

// ---------------------------------------------------------------------------
// Parsing + validation helpers
// ---------------------------------------------------------------------------

type parsedSendTransferRequest struct {
	transferID      uuid.UUID
	senderPkg       *pb.SenderTransferPackage
	senderIDPK      keys.Public
	leafReceiverMap map[string]keys.Public
	receivers       []keys.Public
}

// parseSendTransferRequest extracts and validates the structural fields shared
// by every call site (Prepare on each SO, buildSendTransferCoordinatorFlow).
// MVP: single sender; multi-receiver is supported but gated behind
// KnobMimoTransferMultiReceiverEnabled in the public StartTransferV3 handler.
func parseSendTransferRequest(req *pb.StartTransferV3Request) (parsedSendTransferRequest, error) {
	var empty parsedSendTransferRequest
	if len(req.SenderPackages) != 1 {
		return empty, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("expected exactly 1 sender package, got %d", len(req.SenderPackages)))
	}
	senderPkg := req.SenderPackages[0]
	if senderPkg.TransferPackage == nil {
		return empty, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_package is required"))
	}
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer id: %w", err))
	}
	senderIDPK, err := keys.ParsePublicKey(senderPkg.OwnerIdentityPublicKey)
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid owner identity public key: %w", err))
	}
	leafReceiverMap := make(map[string]keys.Public, len(senderPkg.ReceiverIdentityPublicKeys))
	receiverSet := make(map[string]keys.Public)
	for leafID, recvBytes := range senderPkg.ReceiverIdentityPublicKeys {
		recvPK, err := keys.ParsePublicKey(recvBytes)
		if err != nil {
			return empty, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid receiver pubkey for leaf %s: %w", leafID, err))
		}
		leafReceiverMap[leafID] = recvPK
		receiverSet[string(recvPK.Serialize())] = recvPK
	}
	if len(receiverSet) == 0 {
		return empty, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("at least one receiver required"))
	}
	receivers := make([]keys.Public, 0, len(receiverSet))
	for _, pk := range receiverSet {
		receivers = append(receivers, pk)
	}
	slices.SortFunc(receivers, func(a, b keys.Public) int {
		return bytes.Compare(a.Serialize(), b.Serialize())
	})

	return parsedSendTransferRequest{
		transferID:      transferID,
		senderPkg:       senderPkg,
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
	return uuid.NewSHA1(sendTransferSigningJobNamespace, fmt.Appendf(nil, "%s:%s:%s", transferID.String(), leafID, txKind))
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
// SignRefundsWithPregeneratedNonce but with deterministic job IDs and no
// adaptor public keys (v3 doesn't use adaptor signatures).
func buildSendTransferAggregationJobs(
	ctx context.Context,
	transferID uuid.UUID,
	pkg *pb.TransferPackage,
	leafMap map[string]*ent.TreeNode,
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
		job, err := buildSigningJobForRefund(ctx, req, leaf, leaf.RawTx, sendTransferJobID(transferID, leaf.ID.String(), "cpfp"))
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
		job, err := buildSigningJobForRefund(ctx, req, leaf, leaf.DirectTx, sendTransferJobID(transferID, leaf.ID.String(), "direct"))
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
		job, err := buildSigningJobForRefund(ctx, req, leaf, leaf.RawTx, sendTransferJobID(transferID, leaf.ID.String(), "directFromCpfp"))
		if err != nil {
			return nil, fmt.Errorf("build direct-from-cpfp signing job for leaf %s: %w", req.LeafId, err)
		}
		out[req.LeafId].dfc = job
		out[req.LeafId].dfcUserSig = req.UserSignature
	}
	return out, nil
}

// buildSigningJobForRefund builds a single FROST signing-job helper for one
// refund variant. parentTxBytes is the tx whose vout 0 is being spent
// (leaf.RawTx for cpfp + direct-from-cpfp; leaf.DirectTx for direct).
func buildSigningJobForRefund(
	ctx context.Context,
	req *pb.UserSignedTxSigningJob,
	leaf *ent.TreeNode,
	parentTxBytes []byte,
	jobID uuid.UUID,
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
	if len(refundTx.TxIn) != 1 {
		return nil, fmt.Errorf("refund tx must have exactly 1 input, got %d", len(refundTx.TxIn))
	}
	expectedOutPoint := wire.OutPoint{Hash: parentTx.TxHash(), Index: 0}
	if refundTx.TxIn[0].PreviousOutPoint != expectedOutPoint {
		return nil, fmt.Errorf("refund tx input 0 must spend parent tx output 0")
	}
	sigHash, err := common.SigHashFromTx(refundTx, 0, parentTx.TxOut[0])
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
	verifyingKey := leaf.VerifyingPubkey
	return &helper.SigningJobWithPregeneratedNonce{
		SigningJob: helper.SigningJob{
			JobID:             jobID,
			SigningKeyshareID: signingKeyshare.ID,
			Message:           sigHash,
			VerifyingKey:      &verifyingKey,
			UserCommitment:    &userCommitment,
		},
		Round1Packages: round1,
	}, nil
}

// buildSendTransferLocalSigningJobs constructs the *pbinternal.SigningJob list
// each SO feeds into its local FrostRound2 handler during Prepare. Mirrors
// buildSendTransferAggregationJobs but produces the internal proto shape.
func buildSendTransferLocalSigningJobs(
	ctx context.Context,
	transferID uuid.UUID,
	pkg *pb.TransferPackage,
	leafMap map[string]*ent.TreeNode,
) ([]*pbinternal.SigningJob, error) {
	jobs := make([]*pbinternal.SigningJob, 0)
	addJob := func(req *pb.UserSignedTxSigningJob, txKind string, parentTxBytes []byte) error {
		leaf, ok := leafMap[req.LeafId]
		if !ok {
			return fmt.Errorf("leaf %s not found in leaf map", req.LeafId)
		}
		helperJob, err := buildSigningJobForRefund(ctx, req, leaf, parentTxBytes, sendTransferJobID(transferID, leaf.ID.String(), txKind))
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
		if err := addJob(req, "cpfp", leaf.RawTx); err != nil {
			return nil, fmt.Errorf("build cpfp signing job for leaf %s: %w", req.LeafId, err)
		}
	}
	for _, req := range pkg.GetDirectLeavesToSend() {
		leaf, ok := leafMap[req.LeafId]
		if !ok {
			return nil, fmt.Errorf("direct leaf %s not found", req.LeafId)
		}
		if err := addJob(req, "direct", leaf.DirectTx); err != nil {
			return nil, fmt.Errorf("build direct signing job for leaf %s: %w", req.LeafId, err)
		}
	}
	for _, req := range pkg.GetDirectFromCpfpLeavesToSend() {
		leaf, ok := leafMap[req.LeafId]
		if !ok {
			return nil, fmt.Errorf("direct-from-cpfp leaf %s not found", req.LeafId)
		}
		if err := addJob(req, "directFromCpfp", leaf.RawTx); err != nil {
			return nil, fmt.Errorf("build direct-from-cpfp signing job for leaf %s: %w", req.LeafId, err)
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
		if _, ok := job.Commitments[identifier]; ok {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

// marshalRoundCommitments converts the in-memory round1 commitments map to
// the AggregateFrost RPC's proto shape. Errors are returned rather than
// silently dropped — a missing entry would produce a malformed Commitments
// map and a cryptic FROST-side failure later.
func marshalRoundCommitments(round1 map[string]frost.SigningCommitment) (map[string]*pbcommon.SigningCommitment, error) {
	out := make(map[string]*pbcommon.SigningCommitment, len(round1))
	for id, c := range round1 {
		m, err := c.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("marshal round1 commitment for %s: %w", id, err)
		}
		out[id] = m
	}
	return out, nil
}

// splitLeafSignatures fans out the commit payload's per-leaf signatures into
// the three maps UpdateTransferLeavesSignatures expects.
func splitLeafSignatures(sigs []*pbinternal.SendTransferLeafSignatures) (cpfp, direct, dfc map[string][]byte) {
	cpfp = make(map[string][]byte, len(sigs))
	direct = make(map[string][]byte, len(sigs))
	dfc = make(map[string][]byte, len(sigs))
	for _, s := range sigs {
		if len(s.RefundSignature) > 0 {
			cpfp[s.LeafId] = s.RefundSignature
		}
		if len(s.DirectRefundSignature) > 0 {
			direct[s.LeafId] = s.DirectRefundSignature
		}
		if len(s.DirectFromCpfpRefundSignature) > 0 {
			dfc[s.LeafId] = s.DirectFromCpfpRefundSignature
		}
	}
	return cpfp, direct, dfc
}
