package handler

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// Shared swap v3 parsing + signing helpers (used by both the primary and the
// counter transfer flow handlers)
// ---------------------------------------------------------------------------

// parsedSwapTransferRequest carries the validated pieces of a swap v3 transfer
// request (either leg) that Prepare and the coordinator flows need.
type parsedSwapTransferRequest struct {
	transferID    uuid.UUID
	transferReq   *pb.StartTransferRequest
	pkg           *transferpkg.Package
	senderIDPK    keys.Public
	receiverIDPK  keys.Public
	adaptorPubKey keys.Public
}

// parseSwapTransferRequest extracts and validates the structural fields shared
// by every swap v3 call site (Prepare on each SO, the coordinator flow
// builders). Swap v3 transfers are CPFP-only — the legs are short-lived, so
// direct refund variants are rejected rather than signed — and always carry a
// TransferPackage (the consensus path never serves the legacy leaves_to_send
// shape).
func parseSwapTransferRequest(transferReq *pb.StartTransferRequest, adaptorKeys *pb.AdaptorPublicKeyPackage) (parsedSwapTransferRequest, error) {
	var empty parsedSwapTransferRequest
	if transferReq == nil {
		return empty, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer is required"))
	}
	if transferReq.GetTransferPackage() == nil {
		return empty, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_package is required"))
	}
	if len(transferReq.GetTransferPackage().GetDirectLeavesToSend()) > 0 || len(transferReq.GetTransferPackage().GetDirectFromCpfpLeavesToSend()) > 0 {
		return empty, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("direct transactions should not be provided for swap transfer %s", transferReq.GetTransferId()))
	}
	adaptorPubKey, err := keys.ParsePublicKey(adaptorKeys.GetAdaptorPublicKey())
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse adaptor public key: %w", err))
	}
	transferID, err := uuid.Parse(transferReq.GetTransferId())
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer id: %w", err))
	}
	senderIDPK, err := keys.ParsePublicKey(transferReq.GetOwnerIdentityPublicKey())
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner identity public key: %w", err))
	}
	receiverIDPK, err := keys.ParsePublicKey(transferReq.GetReceiverIdentityPublicKey())
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse receiver identity public key: %w", err))
	}
	pkg, err := transferpkg.ParsePackage(transferReq.GetTransferPackage())
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer package: %w", err))
	}
	// Swap v3 refund txs are signed by the SE via FROST at commit time — the
	// user submits them unsigned (their own share travels in UserSignature,
	// not the tx witness). Reject a pre-witnessed refund so a witness on a
	// stored refund tx unambiguously means the SE applied its signature. The
	// counter leg's requirePrimaryRefundSignaturesApplied gate relies on this
	// to distinguish an applied primary commit from a user-forged witness.
	for _, job := range pkg.LeavesToSend() {
		refundTx := job.RefundTx()
		if len(refundTx.TxIn) > 0 && len(refundTx.TxIn[0].Witness) > 0 {
			return empty, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("swap transfer refund tx for leaf %s must be submitted unsigned", job.LeafID()))
		}
	}
	return parsedSwapTransferRequest{
		transferID:    transferID,
		transferReq:   transferReq,
		pkg:           pkg,
		senderIDPK:    senderIDPK,
		receiverIDPK:  receiverIDPK,
		adaptorPubKey: adaptorPubKey,
	}, nil
}

// validateSwapTransferLimit mirrors the per-SO transfer-size limit the legacy
// startTransferInternal and the send-transfer consensus Prepare enforce, with
// the same wire contract (raw codes.InvalidArgument status, no wrapped reason).
func validateSwapTransferLimit(ctx context.Context, keyTweakMap map[string]validatedKeyTweak) error {
	transferLimit := knobs.GetKnobsService(ctx).GetValue(knobs.KnobSoTransferLimit, 0)
	if transferLimit > 0 && len(keyTweakMap) > int(transferLimit) {
		return status.Errorf(codes.InvalidArgument, "transfer limit reached, please send %d leaves at a time", int(transferLimit))
	}
	return nil
}

// signSwapRefundsRound2 runs this SO's local FROST round-2 over the swap
// transfer's CPFP refund txs with the adaptor point attached (the round-2
// challenge is computed against R+T). Returns nil when this SO is outside the
// signing set for every leaf.
func signSwapRefundsRound2(
	ctx context.Context,
	config *so.Config,
	transferID uuid.UUID,
	pkg *transferpkg.Package,
	leafMap map[string]*ent.TreeNode,
	adaptorPubKey keys.Public,
) (proto.Message, error) {
	jobs, err := buildSendTransferLocalSigningJobs(ctx, transferID, pkg, leafMap, TransferAdaptorPublicKeys{cpfpAdaptorPubKey: adaptorPubKey})
	if err != nil {
		return nil, fmt.Errorf("failed to build local signing jobs: %w", err)
	}
	jobs = filterJobsForThisOperator(jobs, config.Identifier)
	if len(jobs) == 0 {
		return nil, nil
	}
	frostResp, err := signing_handler.NewFrostSigningHandler(config).FrostRound2(ctx, &pbinternal.FrostRound2Request{SigningJobs: jobs})
	if err != nil {
		return nil, fmt.Errorf("local frost round 2 failed during prepare: %w", err)
	}
	return frostResp, nil
}

// aggregateSwapLeafSignatures aggregates the collected FROST round-2 shares
// into final adaptor signatures for each leaf's CPFP refund tx (the adaptor
// point rides on each signing job, attached by buildSigningJobForRefund, so
// round-2 signing and aggregation can never disagree about it). Returns the
// commit-payload leaf signatures plus the per-leaf SigningResults the public
// response publishes (the client completes the adaptor signature from them).
func aggregateSwapLeafSignatures(
	ctx context.Context,
	config *so.Config,
	signingJobsByLeaf map[string]*sendTransferLeafSigningJobs,
	results map[string]*anypb.Any,
) ([]*pbinternal.SendTransferLeafSignatures, map[string]*helper.SigningResult, map[string]*ent.TreeNode, error) {
	allShares, _, err := collectSignatureShares(results)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	leafIDs := make([]string, 0, len(signingJobsByLeaf))
	for id := range signingJobsByLeaf {
		leafIDs = append(leafIDs, id)
	}
	slices.Sort(leafIDs)

	// The swap flow signs and commits only the CPFP refund — the adaptor point
	// rides jobs.cpfp (attached by buildSigningJobForRefund). Aggregate those in
	// one batch via the shared FROST batching primitive, recording per-leaf
	// SigningResults so the public response can carry them for the client's
	// adaptor completion. (Direct/direct-from-cpfp variants are intentionally
	// not aggregated or committed by the swap flow.)
	batch := newFrostAggregationBatch(config)
	batch.recordSigningResults = true

	keyshareIDSet := make(map[uuid.UUID]struct{}, len(leafIDs))
	keyshareIDs := make([]uuid.UUID, 0, len(leafIDs))
	for _, leafID := range leafIDs {
		jobs := signingJobsByLeaf[leafID]
		if jobs.cpfp == nil {
			return nil, nil, nil, fmt.Errorf("missing cpfp signing job for leaf %s", leafID)
		}
		if _, ok := keyshareIDSet[jobs.cpfp.SigningKeyshareID]; !ok {
			keyshareIDSet[jobs.cpfp.SigningKeyshareID] = struct{}{}
			keyshareIDs = append(keyshareIDs, jobs.cpfp.SigningKeyshareID)
		}
	}
	if err := batch.prefetchKeyPackages(ctx, keyshareIDs); err != nil {
		return nil, nil, nil, err
	}
	for _, leafID := range leafIDs {
		jobs := signingJobsByLeaf[leafID]
		if err := batch.addJob(ctx, leafAggregationJobKey(leafID, txKindCPFP), jobs.cpfp, allShares, jobs.leaf, jobs.cpfpUserSig); err != nil {
			return nil, nil, nil, fmt.Errorf("build cpfp aggregation job for leaf %s: %w", leafID, err)
		}
	}
	signatures, err := batch.aggregate(ctx, frostClient)
	if err != nil {
		return nil, nil, nil, err
	}

	leafSignatures := make([]*pbinternal.SendTransferLeafSignatures, 0, len(leafIDs))
	cpfpSigningResultMap := make(map[string]*helper.SigningResult, len(leafIDs))
	leafMap := make(map[string]*ent.TreeNode, len(leafIDs))
	for _, leafID := range leafIDs {
		jobKey := leafAggregationJobKey(leafID, txKindCPFP)
		sr, err := batch.signingResult(jobKey)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("signing result for leaf %s: %w", leafID, err)
		}
		leafMap[leafID] = signingJobsByLeaf[leafID].leaf
		leafSignatures = append(leafSignatures, &pbinternal.SendTransferLeafSignatures{LeafId: leafID, RefundSignature: signatures[jobKey]})
		cpfpSigningResultMap[leafID] = sr
	}
	return leafSignatures, cpfpSigningResultMap, leafMap, nil
}

// ---------------------------------------------------------------------------
// SwapPrimaryTransferFlowHandler — participant side (Prepare / Commit / Rollback)
// ---------------------------------------------------------------------------

// SwapPrimaryTransferFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_INITIATE_SWAP_PRIMARY_TRANSFER — the swap v3
// primary leg (user → SSP with adaptor-encumbered refunds). Embeds the
// send-transfer flow handler for the shared transfer/cancel machinery, but
// diverges from it in two swap-specific ways:
//
//   - Prepare creates the transfer via createTransfer (not createTransferV3):
//     swap v3 is single-receiver and the swap-specific validation — primary
//     expiry safety buffer, validateSwapV3Leaves — lives in createTransfer's
//     type switch.
//   - Commit applies the aggregated adaptor signatures ONLY. The primary's
//     sender key tweaks stay stored-but-unapplied until the counter leg's
//     commit settles both legs atomically (CommitSwapKeyTweaks).
type SwapPrimaryTransferFlowHandler struct {
	*SendTransferFlowHandler
}

var (
	_ consensus.FlowHandler             = (*SwapPrimaryTransferFlowHandler)(nil)
	_ consensus.PrepareBoundFlowHandler = (*SwapPrimaryTransferFlowHandler)(nil)
)

func NewSwapPrimaryTransferFlowHandler(config *so.Config) *SwapPrimaryTransferFlowHandler {
	// partnerType is unused: swap v3 records no transfer-partner attribution
	// (parity with the legacy startTransferInternal, whose partner switch has
	// no swap cases). requireDirectRefunds=false matches the legacy
	// ValidateTransferPackage call for swap types.
	return &SwapPrimaryTransferFlowHandler{
		SendTransferFlowHandler: NewSendTransferFlowHandlerForType(config, st.TransferTypePrimarySwapV3, st.TransferPartnerTypeTransfer, false),
	}
}

// Prepare runs on every SO. It validates the transfer package, decrypts this
// SO's slice of the sender key tweaks, persists the PRIMARY_SWAP_V3 transfer
// with tweaks stored-but-unapplied, and produces local FROST round-2 adaptor
// signature shares for the leaves where this SO is part of the signing set.
//
// The expiry time inside the original request was overridden by the
// coordinator before fan-out (now + 2x the counter safety buffer);
// createTransfer re-checks it against the 1x buffer on every SO.
func (h *SwapPrimaryTransferFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	req, ok := op.(*pbinternal.InitiateSwapPrimaryTransferPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for swap primary transfer prepare", op)
	}
	orig := req.GetOriginalRequest()
	if orig == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("original_request is required"))
	}
	parsed, err := parseSwapTransferRequest(orig.GetTransfer(), orig.GetAdaptorPublicKeys())
	if err != nil {
		return nil, err
	}
	_, leafMap, err := h.prepareSwapTransfer(ctx, parsed, st.TransferTypePrimarySwapV3, uuid.Nil)
	if err != nil {
		return nil, err
	}
	return signSwapRefundsRound2(ctx, h.config, parsed.transferID, parsed.pkg, leafMap, parsed.adaptorPubKey)
}

// prepareSwapTransfer is the shared Prepare body for both swap v3 legs:
// validate the package (swap semantics: direct-from-cpfp leaves not
// required), enforce the transfer-size limit, and persist the transfer rows
// via createTransfer with TransferRoleParticipant on every SO (the engine's
// FlowExecution row tracks role; commit accepts either pre-commit status).
func (h *SendTransferFlowHandler) prepareSwapTransfer(
	ctx context.Context,
	parsed parsedSwapTransferRequest,
	transferType st.TransferType,
	primaryTransferID uuid.UUID,
) (*ent.Transfer, map[string]*ent.TreeNode, error) {
	keyTweakMap, err := h.ValidateTransferPackage(ctx, parsed.transferID, parsed.transferReq.GetTransferPackage(), parsed.senderIDPK, false)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to validate transfer package for transfer %s: %w", parsed.transferID, err)
	}
	if len(keyTweakMap) == 0 {
		return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer package contains no key tweaks"))
	}
	if err := validateSwapTransferLimit(ctx, keyTweakMap); err != nil {
		return nil, nil, err
	}

	cpfpMap, directMap, dfcMap := loadLeafRefundMapsFromTransferPackage(parsed.transferReq.GetTransferPackage())
	transfer, leafMap, err := h.createTransfer(
		ctx,
		parsed.transferID,
		parsed.transferReq.GetTransferPackage(),
		transferType,
		parsed.transferReq.GetExpiryTime().AsTime(),
		parsed.senderIDPK,
		parsed.receiverIDPK,
		cpfpMap,
		directMap,
		dfcMap,
		keyTweakMap,
		TransferRoleParticipant,
		false,
		"",
		primaryTransferID,
		nil,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create transfer rows for %s: %w", parsed.transferID, err)
	}
	return transfer, leafMap, nil
}

// ValidateDecisionAgainstPrepare implements consensus.PrepareBoundFlowHandler:
// a gossip-delivered commit/rollback must name the same transfer the persisted
// prepare op did, and a commit must carry the same adaptor point the request
// pinned. The swap commit/rollback paths resolve rows purely by the payload's
// transfer id, so without this binding a misbehaving coordinator could drive a
// legitimate flow to the prepared state and then aim its decision at an
// unrelated in-flight transfer.
func (h *SwapPrimaryTransferFlowHandler) ValidateDecisionAgainstPrepare(prepareOp proto.Message, decisionOp proto.Message) error {
	prepare, ok := prepareOp.(*pbinternal.InitiateSwapPrimaryTransferPrepareRequest)
	if !ok {
		return fmt.Errorf("unexpected prepare op type %T for swap primary transfer", prepareOp)
	}
	preparedTransferID := prepare.GetOriginalRequest().GetTransfer().GetTransferId()
	switch d := decisionOp.(type) {
	case *pbinternal.InitiateSwapPrimaryTransferCommitRequest:
		if !sameTransferID(d.GetTransferId(), preparedTransferID) {
			return fmt.Errorf("commit transfer id %s does not match the prepared transfer id %s", d.GetTransferId(), preparedTransferID)
		}
		if !samePublicKey(d.GetAdaptorPublicKey(), prepare.GetOriginalRequest().GetAdaptorPublicKeys().GetAdaptorPublicKey()) {
			return fmt.Errorf("commit adaptor public key does not match the prepared adaptor public key for transfer %s", preparedTransferID)
		}
	case *pbinternal.InitiateSwapPrimaryTransferRollbackRequest:
		if !sameTransferID(d.GetTransferId(), preparedTransferID) {
			return fmt.Errorf("rollback transfer id %s does not match the prepared transfer id %s", d.GetTransferId(), preparedTransferID)
		}
	case *pbinternal.InitiateSwapPrimaryTransferPrepareRequest:
		// The reconciler's presumed-abort path echoes the prepare op itself.
		if !sameTransferID(d.GetOriginalRequest().GetTransfer().GetTransferId(), preparedTransferID) {
			return fmt.Errorf("presumed-abort rollback transfer id %s does not match the prepared transfer id %s", d.GetOriginalRequest().GetTransfer().GetTransferId(), preparedTransferID)
		}
	default:
		return fmt.Errorf("unexpected decision op type %T for swap primary transfer", decisionOp)
	}
	return nil
}

// Commit runs on every participant (the coordinator's equivalent work lives in
// BuildCommitPayload). It applies the aggregated adaptor signatures to the
// TransferLeaf rows written in Prepare and deliberately does NOT settle the
// sender key tweaks — the counter leg's commit settles both legs atomically.
func (h *SwapPrimaryTransferFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.InitiateSwapPrimaryTransferCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for swap primary transfer commit", op)
	}
	return h.applySwapPrimaryTransferCommit(ctx, req)
}

// applySwapPrimaryTransferCommit applies the aggregated adaptor signatures on
// a single SO. Shared by participant Commit and coordinator BuildCommitPayload.
//
// The status gate differs from the send-transfer commit's: the counter leg
// settles THIS leg's key tweaks cross-flow, so a delayed primary commit
// gossip can legitimately arrive after the transfer already reached
// SENDER_KEY_TWEAKED — the signatures must still be applied then. Re-applying
// on redelivery is idempotent: UpdateTxWithSignature replaces the witness
// with the identical aggregated signature and re-verifies it.
func (h *SwapPrimaryTransferFlowHandler) applySwapPrimaryTransferCommit(ctx context.Context, req *pbinternal.InitiateSwapPrimaryTransferCommitRequest) error {
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
		return fmt.Errorf("unable to load transfer %s for commit: %w", transferID, err)
	}
	// Defense in depth alongside ValidateDecisionAgainstPrepare: this commit
	// must never mutate a transfer of another type.
	if transferEnt.Type != st.TransferTypePrimarySwapV3 {
		return fmt.Errorf("transfer %s has type %s, expected %s for swap primary commit", transferID, transferEnt.Type, st.TransferTypePrimarySwapV3)
	}
	switch transferEnt.Status {
	case st.TransferStatusReturned, st.TransferStatusExpired:
		// The primary was cancelled/expired while this commit was in flight
		// (swap v3 primaries are cancellable until a counter exists); its leaves
		// moved on without these signatures — don't touch it.
		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"swap primary 2pc commit: transfer %s in terminal status %s, skipping signature application",
			transferID, transferEnt.Status)
		return nil
	default:
		// Apply. Normal path: SENDER_KEY_TWEAK_PENDING (fresh; consensus-created
		// primaries sit here on every SO — Prepare uses the participant role
		// everywhere), APPLYING_SENDER_KEY_TWEAK (a counter fenced this leg
		// mid-flight), or SENDER_KEY_TWEAKED (the counter's commit settled this
		// leg's tweaks before this commit arrived).
		//
		// Mixed mode: a legacy counter settles the primary via SettleSwapKeyTweak
		// with no per-SO fence, so a lagging SO can find this leg already at a
		// RECEIVER_* / COMPLETED status before this commit's first delivery lands.
		// The refund signatures must STILL be applied there — skipping would leave
		// the 2PC ledger recording the commit as applied while that SO never wrote
		// the refund-tx witness. Application is idempotent and status-independent:
		// UpdateTransferLeavesSignaturesForRefundTxOnly only rewrites this
		// transfer's own TransferLeaf refund-tx witnesses and re-verifies them.
	}

	cpfpSigs, _, _ := splitLeafSignatures(req.GetLeafSignatures())
	if err := h.UpdateTransferLeavesSignaturesForRefundTxOnly(ctx, transferEnt, cpfpSigs, adaptorPubKey); err != nil {
		return fmt.Errorf("unable to apply adaptor refund signatures for transfer %s: %w", transferID, err)
	}
	return nil
}

// Rollback runs on every participant (and on the coordinator if Prepare or
// BuildCommitPayload fails). Identical semantics to the send-transfer
// rollback: cancel the transfer rows Prepare wrote and unlock the leaves.
//
// Accepts both InitiateSwapPrimaryTransferRollbackRequest (the normal
// rollback payload) and InitiateSwapPrimaryTransferPrepareRequest (the
// prepare op echoed back by the reconciler when the coordinator's row was
// lost).
func (h *SwapPrimaryTransferFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var transferIDStr string
	switch r := op.(type) {
	case *pbinternal.InitiateSwapPrimaryTransferRollbackRequest:
		transferIDStr = r.GetTransferId()
	case *pbinternal.InitiateSwapPrimaryTransferPrepareRequest:
		transferIDStr = r.GetOriginalRequest().GetTransfer().GetTransferId()
	default:
		return fmt.Errorf("unexpected operation type %T for swap primary transfer rollback", op)
	}
	if transferIDStr == "" {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_id is required for rollback"))
	}
	transferID, err := uuid.Parse(transferIDStr)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}
	// Defense in depth alongside ValidateDecisionAgainstPrepare: this rollback
	// must never cancel a transfer of another type.
	transferEnt, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		if ent.IsNotFound(err) {
			// Prepare never created rows on this SO — nothing to undo.
			return nil
		}
		return fmt.Errorf("unable to load transfer %s for rollback: %w", transferID, err)
	}
	if transferEnt.Type != st.TransferTypePrimarySwapV3 {
		return fmt.Errorf("transfer %s has type %s, expected %s for swap primary rollback", transferID, transferEnt.Type, st.TransferTypePrimarySwapV3)
	}
	return h.rollbackSendTransfer(ctx, transferID)
}

// ---------------------------------------------------------------------------
// swapPrimaryTransferCoordinatorFlow — coordinator side
// ---------------------------------------------------------------------------

// swapPrimaryTransferCoordinatorFlow drives the coordinator side of the swap
// v3 primary leg through the 2PC engine. BuildCommitPayload aggregates the
// FROST shares into adaptor signatures, applies them locally, and builds the
// public response.
type swapPrimaryTransferCoordinatorFlow struct {
	*SwapPrimaryTransferFlowHandler

	req               *pb.InitiateSwapPrimaryTransferRequest
	parsed            parsedSwapTransferRequest
	signingJobsByLeaf map[string]*sendTransferLeafSigningJobs

	// response is populated during BuildCommitPayload so the public
	// InitiateSwapPrimaryTransfer handler can return it after engine.Execute.
	response *pb.StartTransferResponse
}

var _ consensus.CoordinatorFlow = (*swapPrimaryTransferCoordinatorFlow)(nil)

func (f *swapPrimaryTransferCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.InitiateSwapPrimaryTransferPrepareRequest{OriginalRequest: f.req}
}

func (f *swapPrimaryTransferCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	leafSignatures, cpfpSigningResultMap, leafMap, err := aggregateSwapLeafSignatures(ctx, f.config, f.signingJobsByLeaf, results)
	if err != nil {
		return nil, err
	}

	commitReq := &pbinternal.InitiateSwapPrimaryTransferCommitRequest{
		TransferId:       f.parsed.transferID.String(),
		LeafSignatures:   leafSignatures,
		AdaptorPublicKey: f.parsed.adaptorPubKey.Serialize(),
	}
	// Apply on the coordinator's DB now so the request tx the engine commits
	// next carries the final transfer state. Participants do the same work via
	// FlowHandler.Commit after receiving the commit gossip. No partner
	// attribution: parity with the legacy path, whose partner switch has no
	// swap cases.
	if err := f.applySwapPrimaryTransferCommit(ctx, commitReq); err != nil {
		return nil, fmt.Errorf("failed to apply commit on coordinator: %w", err)
	}

	transferEnt, err := f.loadTransferForUpdate(ctx, f.parsed.transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to reload transfer %s after commit: %w", f.parsed.transferID, err)
	}
	transferProto, err := transferEnt.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer %s for response: %w", f.parsed.transferID, err)
	}
	signingResultProtos, err := buildSigningResultProtos(leafMap, cpfpSigningResultMap, map[string]*helper.SigningResult{}, map[string]*helper.SigningResult{})
	if err != nil {
		return nil, fmt.Errorf("unable to build signing result protos: %w", err)
	}
	f.response = &pb.StartTransferResponse{Transfer: transferProto, SigningResults: signingResultProtos}

	return commitReq, nil
}

func (f *swapPrimaryTransferCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.InitiateSwapPrimaryTransferRollbackRequest{TransferId: f.parsed.transferID.String()}
}

// buildSwapCoordinatorSigningJobs pre-loads the swap transfer's leaves
// (non-locking — Prepare re-loads them under FOR UPDATE and its
// leafAvailableStatus check rejects any leaf whose status changed under us,
// same trade-off as buildSendTransferCoordinatorFlow) and builds the per-leaf
// aggregation-job helpers with the adaptor point attached.
func buildSwapCoordinatorSigningJobs(ctx context.Context, parsed parsedSwapTransferRequest) (map[string]*sendTransferLeafSigningJobs, error) {
	leafUUIDs := parsed.pkg.LeafIDs()
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	// Filter by sender ownership too: Prepare re-validates ownership under
	// FOR UPDATE, but adding the predicate here fails a crafted request for
	// someone else's leaf fast (as a count mismatch below) rather than after a
	// pointless cross-SO fan-out.
	leaves, err := db.TreeNode.Query().
		Where(treenode.IDIn(leafUUIDs...), treenode.OwnerIdentityPubkeyEQ(parsed.senderIDPK)).
		WithTree().
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
	return buildSendTransferAggregationJobs(ctx, parsed.transferID, parsed.pkg, leafMap, TransferAdaptorPublicKeys{cpfpAdaptorPubKey: parsed.adaptorPubKey})
}

// initiateSwapPrimaryTransferConsensus runs the swap v3 primary leg through
// the 2PC consensus engine instead of the legacy startTransferInternal
// fanout. Gated by KnobUseConsensusInitiateSwapPrimaryTransfer at the public
// InitiateSwapPrimaryTransfer entry point.
func (h *TransferHandler) initiateSwapPrimaryTransferConsensus(ctx context.Context, req *pb.InitiateSwapPrimaryTransferRequest) (*pb.StartTransferResponse, error) {
	if req.GetTransfer() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer is required"))
	}
	// Override the expiry time before parsing/building the prepare op so every
	// SO persists the identical value (the legacy path overrode it
	// coordinator-side and fanned the result out). Clone first so the override
	// never leaks into the caller's request.
	req = proto.Clone(req).(*pb.InitiateSwapPrimaryTransferRequest)
	req.GetTransfer().ExpiryTime = swapPrimaryTransferExpiryOverride()

	parsed, err := parseSwapTransferRequest(req.GetTransfer(), req.GetAdaptorPublicKeys())
	if err != nil {
		return nil, err
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, parsed.senderIDPK); err != nil {
		return nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, parsed.senderIDPK); err != nil {
		return nil, err
	}

	// No PendingSendTransfer guard on the consensus path — the engine's
	// FlowExecution row plus createTransfer's unique constraint on Transfer.id
	// already provide mutual exclusivity and recovery (same pattern as the
	// send-transfer consensus flow). This is load-bearing beyond convention:
	// revertSwapPrimaryFence in the counter flow uses the ABSENCE of a
	// PendingSendTransfer row to recognize consensus-created primaries when
	// restoring the fence, so this flow must never write one.

	signingJobsByLeaf, err := buildSwapCoordinatorSigningJobs(ctx, parsed)
	if err != nil {
		return nil, err
	}
	flow := &swapPrimaryTransferCoordinatorFlow{
		SwapPrimaryTransferFlowHandler: NewSwapPrimaryTransferFlowHandler(h.config),
		req:                            req,
		parsed:                         parsed,
		signingJobsByLeaf:              signingJobsByLeaf,
	}
	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	if _, err := engine.Execute(ctx,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_INITIATE_SWAP_PRIMARY_TRANSFER,
		&selection,
		flow,
	); err != nil {
		return nil, fmt.Errorf("consensus swap primary transfer failed: %w", err)
	}
	if flow.response == nil {
		return nil, fmt.Errorf("internal: consensus swap primary transfer for %s succeeded but produced no response", parsed.transferID)
	}
	return flow.response, nil
}
