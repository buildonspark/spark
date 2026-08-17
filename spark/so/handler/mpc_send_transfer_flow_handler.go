package handler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// MpcSendTransferFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_MPC_SEND_TRANSFER — the multiparty sibling of
// SendTransferFlowHandler. Prepare converts a group-signed submission into the
// single-party shape at the transfer seam: verify the authorization against
// this operator's own state, unseal this operator's sub-shares, validate and
// combine them per leaf, and synthesize the SendLeafKeyTweak that the deployed
// share validator and transfer core consume unchanged. Commit and rollback are
// the shared send-transfer decision paths; only the prepare carrier differs.
// Embeds *SendTransferFlowHandler for the shared decision machinery
// (applySendTransferCommit, rollbackSendTransfer, createTransferV3); the
// embedded handler's own Prepare/Commit/Rollback are shadowed below.
type MpcSendTransferFlowHandler struct {
	*SendTransferFlowHandler
}

var (
	_ consensus.FlowHandler             = (*MpcSendTransferFlowHandler)(nil)
	_ consensus.PrepareBoundFlowHandler = (*MpcSendTransferFlowHandler)(nil)
)

func NewMpcSendTransferFlowHandler(config *so.Config) *MpcSendTransferFlowHandler {
	return &MpcSendTransferFlowHandler{SendTransferFlowHandler: NewSendTransferFlowHandler(config)}
}

// mpcSendTransferSigningJobNamespace keeps deterministic per-leaf-per-variant
// job IDs from colliding with other 2PC flows'.
var mpcSendTransferSigningJobNamespace = uuid.MustParse("9c4a51de-2f6b-4f0e-8a3d-5b7e1c9f2a64")

func mpcSendTransferJobID(transferID uuid.UUID, leafID string, txKind string) uuid.UUID {
	return uuid.NewSHA1(mpcSendTransferSigningJobNamespace, fmt.Appendf(nil, "%s:%s:%s", transferID, leafID, txKind))
}

// Prepare runs on every SO. Beyond the single-party flow's work (persist
// Transfer + TransferLeaf rows pre-commit, produce local FROST round-2
// shares), it owns the whole multiparty check stack, so every operator — not
// just the coordinator — independently verifies the group's authorization and
// its own sealed material before any state change.
func (h *MpcSendTransferFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	req, ok := op.(*pbinternal.MpcSendTransferPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for mpc send transfer prepare", op)
	}
	orig := req.GetOriginalRequest()
	if orig == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("original_request is required"))
	}
	// A knob-disabled operator refuses to participate, exactly as its public
	// endpoint refuses to coordinate: the rollout contract is that no MPC
	// transfer commits anywhere until every operator has the flow enabled.
	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobMpcTransferEnabled, 0) == 0 {
		return nil, sparkerrors.UnimplementedMethodDisabled(fmt.Errorf("multiparty transfer send is not enabled"))
	}

	submission, err := transferpkg.ParseMpcSubmission(orig)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid mpc transfer submission: %w", err))
	}
	if leafCount, limit := len(submission.Leaves()), transferLeafLimit(ctx); leafCount > limit {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("too many leaves to send: %d (max: %d)", leafCount, limit))
	}
	if receivers := submission.Receivers(); len(receivers) != 1 {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("multiparty transfers require exactly one distinct receiver, got %d", len(receivers)))
	}

	dbLeaves, err := verifyMpcAuthorization(ctx, submission)
	if err != nil {
		return nil, err
	}

	keyTweakMap, err := h.combineAndValidateKeyTweaks(submission, dbLeaves)
	if err != nil {
		return nil, err
	}

	spec := &transferSpec{
		transferID:      submission.TransferID(),
		senderIDPK:      submission.SenderIdentityPublicKey(),
		receivers:       submission.Receivers(),
		leafReceiverMap: mpcLeafReceiverMap(submission),
		expiryTime:      submission.ExpiryTime(),
		pkgLeafIDs:      mpcTransferPackageLeafIDs(submission),
		cpfpRefunds:     mpcRefundTxMap(submission.LeavesToSend()),
		directRefunds:   mpcRefundTxMap(submission.DirectLeavesToSend()),
		dfcRefunds:      mpcRefundTxMap(submission.DirectFromCPFPLeavesToSend()),
		keyTweaks:       keyTweakMap,
	}
	_, leafMap, err := h.createTransferV3(ctx, spec, st.TransferTypeTransfer, TransferRoleParticipant, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer rows for %s: %w", submission.TransferID(), err)
	}

	jobs, err := buildMpcSendTransferLocalSigningJobs(ctx, submission, leafMap)
	if err != nil {
		return nil, fmt.Errorf("failed to build local signing jobs: %w", err)
	}
	jobs = filterJobsForThisOperator(jobs, h.config.Identifier)
	if len(jobs) == 0 {
		return nil, nil
	}

	frostHandler := signing_handler.NewFrostSigningHandler(h.config)
	frostResp, err := frostHandler.FrostRound2(ctx, &pbinternal.FrostRound2Request{SigningJobs: jobs})
	if err != nil {
		return nil, fmt.Errorf("local frost round 2 failed during prepare: %w", err)
	}
	return frostResp, nil
}

// combineAndValidateKeyTweaks turns the sealed multiparty tweak material into
// validated single-party tweaks: unseal this operator's blobs, validate and
// combine each leaf's sub-shares against the signed commitment vectors and the
// mask binding, synthesize the SendLeafKeyTweak carrier, and run it through
// the deployed share validator — so the synthesized material passes the exact
// checks a single-party sender's would, minted by the same sole constructor.
func (h *MpcSendTransferFlowHandler) combineAndValidateKeyTweaks(
	submission *transferpkg.MpcSubmission,
	dbLeaves map[uuid.UUID]*ent.TreeNode,
) (map[string]validatedKeyTweak, error) {
	sealedFor := make(map[so.Identifier]struct{})
	for _, id := range submission.SealedOperatorIDs() {
		sealedFor[id] = struct{}{}
	}
	for operatorID := range h.config.SigningOperatorMap {
		if _, ok := sealedFor[operatorID]; !ok {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("no sealed sub-shares for operator %s", operatorID))
		}
	}

	unsealed, err := submission.UnsealShares(h.config.Identifier, h.config.IdentityPrivateKey)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMpcSubShareUnsealable(err)
	}

	operatorIDs := slices.Sorted(maps.Keys(h.config.SigningOperatorMap))
	tweaks := make(map[string]*pb.SendLeafKeyTweak, len(submission.Leaves()))
	for _, leaf := range submission.Leaves() {
		combined, err := transferpkg.CombineMpcLeafTweak(
			leaf,
			dbLeaves[leaf.LeafID()].OwnerSigningPubkey,
			unsealed[leaf.LeafID()],
			h.config.Identifier,
			operatorIDs,
			int(h.config.Threshold),
		)
		switch {
		case errors.Is(err, transferpkg.ErrMpcTweakBindingMismatch):
			return nil, sparkerrors.InvalidArgumentMpcTweakBindingMismatch(err)
		// Reachable only from this operator's own config (threshold, operator identifiers), never from the
		// submission — blaming the client would hide a config bug behind an unpaged 4xx.
		case errors.Is(err, transferpkg.ErrMpcInvalidThreshold), errors.Is(err, transferpkg.ErrMpcInvalidOperatorID):
			return nil, sparkerrors.InternalDataInconsistency(err)
		case err != nil:
			return nil, sparkerrors.InvalidArgumentMpcSubShareInvalid(err)
		}
		tweaks[leaf.LeafID().String()] = synthesizeMpcSendLeafKeyTweak(leaf, combined)
	}

	keyTweakMap, err := h.validateKeyTweakShares(tweaks)
	if err != nil {
		return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("combined mpc key tweak failed single-party validation: %w", err))
	}
	return keyTweakMap, nil
}

// synthesizeMpcSendLeafKeyTweak assembles the single-party tweak carrier from
// one leaf's combined material. This proto is the persisted encoding on
// TransferLeaf rows and the input every downstream consumer (key rotation,
// receiver-facing marshalling) already understands; an ECDSA per-leaf
// signature travels as the bare-bytes arm so the receiver-facing surface
// matches a deployed single-party sender's exactly.
func synthesizeMpcSendLeafKeyTweak(leaf *transferpkg.MpcLeaf, combined *transferpkg.CombinedMpcKeyTweak) *pb.SendLeafKeyTweak {
	proofs := combined.Proofs()
	proofBytes := make([][]byte, len(proofs))
	for k, proof := range proofs {
		proofBytes[k] = proof.Serialize()
	}
	pubkeyShares := make(map[string][]byte)
	for operatorID, share := range combined.PubkeyShares() {
		pubkeyShares[operatorID] = share.Serialize()
	}

	out := &pb.SendLeafKeyTweak{
		LeafId: leaf.LeafID().String(),
		SecretShareTweak: &pb.SecretShare{
			SecretShare: combined.SecretShare().Serialize(),
			Proofs:      proofBytes,
		},
		PubkeySharesTweak:             pubkeyShares,
		SecretCipher:                  leaf.SecretCipher(),
		RefundSignature:               leaf.RefundSignature(),
		DirectRefundSignature:         leaf.DirectRefundSignature(),
		DirectFromCpfpRefundSignature: leaf.DirectFromCPFPRefundSignature(),
	}
	if leaf.SignatureScheme() == pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA {
		out.Sig = &pb.SendLeafKeyTweak_Signature{Signature: leaf.Signature()}
	} else {
		out.Sig = &pb.SendLeafKeyTweak_TypedSignature{TypedSignature: &pbcommon.Signature{
			Scheme:    leaf.SignatureScheme(),
			Signature: leaf.Signature(),
		}}
	}
	return out
}

func mpcLeafReceiverMap(submission *transferpkg.MpcSubmission) map[string]keys.Public {
	out := make(map[string]keys.Public, len(submission.Leaves()))
	for _, leaf := range submission.Leaves() {
		out[leaf.LeafID().String()] = leaf.ReceiverIdentityPubKey()
	}
	return out
}

func mpcTransferPackageLeafIDs(submission *transferpkg.MpcSubmission) *transferPackageLeafIDs {
	ids := func(jobs []*transferpkg.MpcRefundSigningJob) []string {
		out := make([]string, len(jobs))
		for i, job := range jobs {
			out[i] = job.LeafID().String()
		}
		return out
	}
	return &transferPackageLeafIDs{
		leavesToSend:         ids(submission.LeavesToSend()),
		directLeavesToSend:   ids(submission.DirectLeavesToSend()),
		directFromCpfpLeaves: ids(submission.DirectFromCPFPLeavesToSend()),
	}
}

func mpcRefundTxMap(jobs []*transferpkg.MpcRefundSigningJob) map[string][]byte {
	out := make(map[string][]byte, len(jobs))
	for _, job := range jobs {
		out[job.LeafID().String()] = job.RawTx()
	}
	return out
}

// buildMpcSendTransferLocalSigningJobs is buildSendTransferLocalSigningJobs'
// multiparty form, feeding buildMpcSigningJobForRefund per leaf per variant.
func buildMpcSendTransferLocalSigningJobs(
	ctx context.Context,
	submission *transferpkg.MpcSubmission,
	leafMap map[string]*ent.TreeNode,
) ([]*pbinternal.SigningJob, error) {
	jobs := make([]*pbinternal.SigningJob, 0, 3*len(leafMap))
	add := func(job *transferpkg.MpcRefundSigningJob, txKind string, parentTxBytes func(*ent.TreeNode) []byte) error {
		leafID := job.LeafID().String()
		leaf, ok := leafMap[leafID]
		if !ok {
			return fmt.Errorf("%s leaf %s not found", txKind, leafID)
		}
		keyshareID, err := leafSigningKeyshareID(ctx, leaf)
		if err != nil {
			return err
		}
		helperJob, err := buildMpcSigningJobForRefund(job, leaf.VerifyingPubkey, keyshareID, parentTxBytes(leaf),
			mpcSendTransferJobID(submission.TransferID(), leafID, txKind))
		if err != nil {
			return fmt.Errorf("build %s signing job for leaf %s: %w", txKind, leafID, err)
		}
		marshalled, err := marshalSigningJobHelper(helperJob)
		if err != nil {
			return err
		}
		jobs = append(jobs, marshalled)
		return nil
	}
	rawTx := func(leaf *ent.TreeNode) []byte { return leaf.RawTx }
	directTx := func(leaf *ent.TreeNode) []byte { return leaf.DirectTx }
	for _, job := range submission.LeavesToSend() {
		if err := add(job, txKindCPFP, rawTx); err != nil {
			return nil, err
		}
	}
	for _, job := range submission.DirectLeavesToSend() {
		if err := add(job, txKindDirect, directTx); err != nil {
			return nil, err
		}
	}
	for _, job := range submission.DirectFromCPFPLeavesToSend() {
		if err := add(job, txKindDirectFromCPFP, rawTx); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

func leafSigningKeyshareID(ctx context.Context, leaf *ent.TreeNode) (uuid.UUID, error) {
	if ks := leaf.Edges.SigningKeyshare; ks != nil {
		return ks.ID, nil
	}
	id, err := leaf.QuerySigningKeyshare().OnlyID(ctx)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("unable to load signing keyshare for leaf %s: %w", leaf.ID, err)
	}
	return id, nil
}

// ValidateDecisionAgainstPrepare binds commit/rollback decisions to the
// transfer this SO prepared, exactly as the single-party flow does; without it
// a misbehaving coordinator could reuse a valid IN_FLIGHT flow id while
// pointing the payload at an unrelated pre-commit transfer.
func (h *MpcSendTransferFlowHandler) ValidateDecisionAgainstPrepare(prepareOp proto.Message, decisionOp proto.Message) error {
	prepare, ok := prepareOp.(*pbinternal.MpcSendTransferPrepareRequest)
	if !ok {
		return fmt.Errorf("unexpected prepare op type %T for mpc send transfer", prepareOp)
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
	case *pbinternal.MpcSendTransferPrepareRequest:
		// The reconciler's presumed-abort path echoes the prepare op itself.
		if !sameTransferID(d.GetOriginalRequest().GetTransferId(), preparedTransferID) {
			return fmt.Errorf("presumed-abort rollback transfer id %s does not match the prepared transfer id %s", d.GetOriginalRequest().GetTransferId(), preparedTransferID)
		}
	default:
		return fmt.Errorf("unexpected decision op type %T for mpc send transfer", decisionOp)
	}
	return nil
}

// Commit and Rollback are the shared send-transfer decision paths: after
// Prepare's synthesis the persisted rows are byte-identical to a single-party
// transfer's, so the decision semantics are too.
func (h *MpcSendTransferFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.SendTransferCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for mpc send transfer commit", op)
	}
	return h.applySendTransferCommit(ctx, req)
}

func (h *MpcSendTransferFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var transferIDStr string
	switch r := op.(type) {
	case *pbinternal.SendTransferRollbackRequest:
		transferIDStr = r.GetTransferId()
	case *pbinternal.MpcSendTransferPrepareRequest:
		if r.GetOriginalRequest() != nil {
			transferIDStr = r.GetOriginalRequest().GetTransferId()
		}
	default:
		return fmt.Errorf("unexpected operation type %T for mpc send transfer rollback", op)
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

// ---------------------------------------------------------------------------
// mpcSendTransferCoordinatorFlow — coordinator side
// ---------------------------------------------------------------------------

type mpcSendTransferLeafSigningJobs struct {
	leaf              *ent.TreeNode
	cpfp, direct, dfc *helper.SigningJobWithPregeneratedNonce
	cpfpShares        []*pbfrost.SubUserSignatureShare
	directShares      []*pbfrost.SubUserSignatureShare
	dfcShares         []*pbfrost.SubUserSignatureShare
}

// mpcSendTransferCoordinatorFlow drives the coordinator side through the 2PC
// engine, mirroring sendTransferCoordinatorFlow: Prepare/Commit/Rollback
// delegate to the participant handler; BuildCommitPayload aggregates the FROST
// shares (in MPC user-group mode, folding the sub-users' contributions in),
// applies the commit locally, and assembles the client response.
type mpcSendTransferCoordinatorFlow struct {
	*MpcSendTransferFlowHandler

	req               *pb.StartTransferMpcRequest
	transferID        uuid.UUID
	signingJobsByLeaf map[string]*mpcSendTransferLeafSigningJobs

	// response is populated during BuildCommitPayload so the public
	// StartTransferMpc handler can return it after engine.Execute completes.
	response *pb.StartTransferResponse
}

var _ consensus.CoordinatorFlow = (*mpcSendTransferCoordinatorFlow)(nil)

func (f *mpcSendTransferCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.MpcSendTransferPrepareRequest{OriginalRequest: f.req}
}

func (f *mpcSendTransferCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.SendTransferRollbackRequest{TransferId: f.transferID.String()}
}

func (f *mpcSendTransferCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	allShares, _, err := collectSignatureShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	frostConn, err := f.config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	leafSignatures, batch, err := aggregateMpcSendTransferLeafSignatures(ctx, f.config, frostClient, f.signingJobsByLeaf, allShares)
	if err != nil {
		return nil, err
	}

	cpfpSigningResultMap := make(map[string]*helper.SigningResult, len(f.signingJobsByLeaf))
	directSigningResultMap := make(map[string]*helper.SigningResult, len(f.signingJobsByLeaf))
	dfcSigningResultMap := make(map[string]*helper.SigningResult, len(f.signingJobsByLeaf))
	leafMap := make(map[string]*ent.TreeNode, len(f.signingJobsByLeaf))
	for leafID, jobs := range f.signingJobsByLeaf {
		leafMap[leafID] = jobs.leaf
		if cpfpSigningResultMap[leafID], err = batch.signingResult(leafAggregationJobKey(leafID, txKindCPFP)); err != nil {
			return nil, err
		}
		if directSigningResultMap[leafID], err = batch.signingResult(leafAggregationJobKey(leafID, txKindDirect)); err != nil {
			return nil, err
		}
		if dfcSigningResultMap[leafID], err = batch.signingResult(leafAggregationJobKey(leafID, txKindDirectFromCPFP)); err != nil {
			return nil, err
		}
	}

	commitReq := &pbinternal.SendTransferCommitRequest{
		TransferId:     f.transferID.String(),
		LeafSignatures: leafSignatures,
	}
	if err := f.applySendTransferCommit(ctx, commitReq); err != nil {
		return nil, fmt.Errorf("failed to apply commit on coordinator: %w", err)
	}

	partner.SaveTransferPartner(ctx, f.transferID, st.TransferPartnerTypeTransfer)

	transferEnt, err := f.loadTransferForUpdate(ctx, f.transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to reload transfer %s after commit: %w", f.transferID, err)
	}
	transferProto, err := transferEnt.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer %s for response: %w", f.transferID, err)
	}
	signingResultProtos, err := buildSigningResultProtos(leafMap, cpfpSigningResultMap, directSigningResultMap, dfcSigningResultMap)
	if err != nil {
		return nil, fmt.Errorf("unable to build signing result protos: %w", err)
	}
	f.response = &pb.StartTransferResponse{Transfer: transferProto, SigningResults: signingResultProtos}

	return commitReq, nil
}

// buildMpcSendTransferCoordinatorFlow pre-computes the aggregation helpers the
// coordinator needs during BuildCommitPayload. The leaf pre-load is
// non-locking for the same reason the single-party coordinator flow's is: the
// engine-driven Prepare re-loads FOR UPDATE and rejects any leaf whose status
// changed, so the worst case is a wasted job-builder pass.
//
//nolint:unused // The caller is the public endpoint, wired one PR up this stack; the directive is deleted there.
func buildMpcSendTransferCoordinatorFlow(
	ctx context.Context,
	req *pb.StartTransferMpcRequest,
	submission *transferpkg.MpcSubmission,
	handler *MpcSendTransferFlowHandler,
) (*mpcSendTransferCoordinatorFlow, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	leafUUIDs := make([]uuid.UUID, 0, len(submission.Leaves()))
	for _, leaf := range submission.Leaves() {
		leafUUIDs = append(leafUUIDs, leaf.LeafID())
	}
	leaves, err := db.TreeNode.Query().
		Where(treenode.IDIn(leafUUIDs...)).
		WithTree().
		WithSigningKeyshare().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to preload leaves for mpc coordinator flow: %w", err)
	}
	if len(leaves) != len(leafUUIDs) {
		return nil, fmt.Errorf("preload missed leaves: got %d, want %d", len(leaves), len(leafUUIDs))
	}
	leafMap := make(map[string]*ent.TreeNode, len(leaves))
	for _, leaf := range leaves {
		leafMap[leaf.ID.String()] = leaf
	}

	jobsByLeaf := make(map[string]*mpcSendTransferLeafSigningJobs, len(leafMap))
	for _, leaf := range leafMap {
		jobsByLeaf[leaf.ID.String()] = &mpcSendTransferLeafSigningJobs{leaf: leaf}
	}
	add := func(job *transferpkg.MpcRefundSigningJob, txKind string, parentTxBytes func(*ent.TreeNode) []byte,
		set func(*mpcSendTransferLeafSigningJobs, *helper.SigningJobWithPregeneratedNonce, []*pbfrost.SubUserSignatureShare)) error {
		leafID := job.LeafID().String()
		leaf, ok := leafMap[leafID]
		if !ok {
			return fmt.Errorf("%s leaf %s not found in leaf map", txKind, leafID)
		}
		keyshareID, err := leafSigningKeyshareID(ctx, leaf)
		if err != nil {
			return err
		}
		helperJob, err := buildMpcSigningJobForRefund(job, leaf.VerifyingPubkey, keyshareID, parentTxBytes(leaf),
			mpcSendTransferJobID(submission.TransferID(), leafID, txKind))
		if err != nil {
			return fmt.Errorf("build %s signing job for leaf %s: %w", txKind, leafID, err)
		}
		set(jobsByLeaf[leafID], helperJob, mpcSubUserShares(job.Contributions()))
		return nil
	}
	rawTx := func(leaf *ent.TreeNode) []byte { return leaf.RawTx }
	directTx := func(leaf *ent.TreeNode) []byte { return leaf.DirectTx }
	for _, job := range submission.LeavesToSend() {
		if err := add(job, txKindCPFP, rawTx, func(j *mpcSendTransferLeafSigningJobs, h *helper.SigningJobWithPregeneratedNonce, s []*pbfrost.SubUserSignatureShare) {
			j.cpfp, j.cpfpShares = h, s
		}); err != nil {
			return nil, err
		}
	}
	for _, job := range submission.DirectLeavesToSend() {
		if err := add(job, txKindDirect, directTx, func(j *mpcSendTransferLeafSigningJobs, h *helper.SigningJobWithPregeneratedNonce, s []*pbfrost.SubUserSignatureShare) {
			j.direct, j.directShares = h, s
		}); err != nil {
			return nil, err
		}
	}
	for _, job := range submission.DirectFromCPFPLeavesToSend() {
		if err := add(job, txKindDirectFromCPFP, rawTx, func(j *mpcSendTransferLeafSigningJobs, h *helper.SigningJobWithPregeneratedNonce, s []*pbfrost.SubUserSignatureShare) {
			j.dfc, j.dfcShares = h, s
		}); err != nil {
			return nil, err
		}
	}

	return &mpcSendTransferCoordinatorFlow{
		MpcSendTransferFlowHandler: handler,
		req:                        req,
		transferID:                 submission.TransferID(),
		signingJobsByLeaf:          jobsByLeaf,
	}, nil
}
