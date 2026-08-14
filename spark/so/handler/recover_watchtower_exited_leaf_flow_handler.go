package handler

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/sighash"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	enttransferreceiver "github.com/lightsparkdev/spark/so/ent/transferreceiver"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// RecoverWatchtowerExitedLeafFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_RECOVER_WATCHTOWER_EXITED_LEAF: co-signing a spend of
// the output a watchtower exit stranded, and retiring the leaf in the same
// operation so its value cannot also move off-chain.
//
// As in the static deposit refund flow, round-1 commitments are collected before
// engine.Execute and carried in the prepare op, so round 2 runs inside Prepare
// and the public RPC stays a single call.
type RecoverWatchtowerExitedLeafFlowHandler struct {
	config *so.Config
}

var (
	_ consensus.FlowHandler             = (*RecoverWatchtowerExitedLeafFlowHandler)(nil)
	_ consensus.PrepareBoundFlowHandler = (*RecoverWatchtowerExitedLeafFlowHandler)(nil)
)

func NewRecoverWatchtowerExitedLeafFlowHandler(config *so.Config) *RecoverWatchtowerExitedLeafFlowHandler {
	return &RecoverWatchtowerExitedLeafFlowHandler{config: config}
}

// recoverWatchtowerExitedLeafJobNamespace derives a deterministic signing-job id
// from the leaf, so operators correlate round-2 shares without sending it over
// the wire. Retries reuse it safely: shares are only correlated within one
// Execute.
var recoverWatchtowerExitedLeafJobNamespace = uuid.MustParse("6f2b9c14-8a37-4d5e-b0c9-3e71fd2a6b48")

func recoverWatchtowerExitedLeafJobID(leafID uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(recoverWatchtowerExitedLeafJobNamespace, []byte(leafID.String()))
}

// Prepare re-derives the recoverable output from this operator's own rows,
// checks the owner's authorisation, retires the leaf, and returns a round-2
// share if the coordinator's round-1 set includes this operator.
//
// The terminal status is written here rather than staged behind a lock because
// that direction fails closed: an abandoned flow leaves the leaf more
// restricted, and a retry signs anyway instead of deadlocking on its leftovers.
func (h *RecoverWatchtowerExitedLeafFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	prepareReq, ok := op.(*pbinternal.RecoverWatchtowerExitedLeafPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for recover watchtower exited leaf prepare", op)
	}
	req := prepareReq.GetOriginalRequest()
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("original_request is required"))
	}

	leaf, recoverable, err := authorizeRecoverWatchtowerExitedLeaf(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := retireRecoveredLeaf(ctx, leaf); err != nil {
		return nil, err
	}

	// Operators outside the coordinator's round-1 set contribute no share;
	// collectSignatureShares skips their nil result.
	if _, inSigningSet := prepareReq.GetSigningCommitments()[h.config.Identifier]; !inSigningSet {
		return nil, nil
	}
	job, err := h.buildRecoveryRound2Job(ctx, leaf, req, recoverable.sighash, prepareReq.GetSigningCommitments())
	if err != nil {
		return nil, err
	}
	frostResp, err := signing_handler.NewFrostSigningHandler(h.config).FrostRound2(ctx, &pbinternal.FrostRound2Request{SigningJobs: []*pbinternal.SigningJob{job}})
	if err != nil {
		return nil, fmt.Errorf("local frost round 2 failed during prepare: %w", err)
	}
	return frostResp, nil
}

func (h *RecoverWatchtowerExitedLeafFlowHandler) buildRecoveryRound2Job(ctx context.Context, leaf *ent.TreeNode, req *pbspark.RecoverWatchtowerExitedLeafRequest, txSighash sighash.Hash, round1 map[string]*pbcommon.SigningCommitment) (*pbinternal.SigningJob, error) {
	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare for leaf %s: %w", leaf.ID, err)
	}
	return &pbinternal.SigningJob{
		JobId:      recoverWatchtowerExitedLeafJobID(leaf.ID).String(),
		Message:    txSighash.Serialize(),
		KeyshareId: signingKeyshare.ID.String(),
		// The stranded output pays P2TR of exactly this key, which is what makes
		// the leaf's own keyshare able to spend an ancestor generation's output.
		VerifyingKey:    leaf.VerifyingPubkey.Serialize(),
		Commitments:     round1,
		UserCommitments: req.GetRecoveryTxSigningJob().GetSigningNonceCommitment(),
	}, nil
}

// Commit confirms the decision. Prepare already wrote WATCHTOWER_EXIT_RECOVERED, so
// there is nothing left to apply; the leaf is re-read only to reject a decision
// for a leaf this operator never prepared.
func (h *RecoverWatchtowerExitedLeafFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.RecoverWatchtowerExitedLeafCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for recover watchtower exited leaf commit", op)
	}
	leafID, err := uuid.Parse(req.GetLeafId())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse leaf id %q: %w", req.GetLeafId(), err))
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db: %w", err)
	}
	leaf, err := db.TreeNode.Get(ctx, leafID)
	if err != nil {
		return fmt.Errorf("failed to load leaf %s on commit: %w", leafID, err)
	}
	if leaf.Status != st.TreeNodeStatusWatchtowerExitRecovered {
		return sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("leaf %s is %s on commit, expected %s", leafID, leaf.Status, st.TreeNodeStatusWatchtowerExitRecovered))
	}
	logging.GetLoggerFromContext(ctx).Sugar().Infof("recover watchtower exited leaf 2pc commit: leaf %s recovered", leafID)
	return nil
}

// Rollback returns the leaf to WATCHTOWER_EXITED. Accepts both the canonical
// rollback payload and the prepare op echoed by the participant reconciler's
// presumed-abort path — both carry the leaf id.
//
// The status guard cannot tell a leaf recovered by a *later* flow from one
// recovered by this flow; nothing bound to this handler can. Redelivery safety
// comes from the engine instead, which fences decision dispatch on the
// participant FlowExecution row (classifyConsensusOp).
func (h *RecoverWatchtowerExitedLeafFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var leafIDStr string
	switch r := op.(type) {
	case *pbinternal.RecoverWatchtowerExitedLeafRollbackRequest:
		leafIDStr = r.GetLeafId()
	case *pbinternal.RecoverWatchtowerExitedLeafPrepareRequest:
		leafIDStr = r.GetOriginalRequest().GetLeafId()
	default:
		return fmt.Errorf("unexpected operation type %T for recover watchtower exited leaf rollback", op)
	}
	leafID, err := uuid.Parse(leafIDStr)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse leaf id %q: %w", leafIDStr, err))
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db: %w", err)
	}
	logger := logging.GetLoggerFromContext(ctx)
	restored, err := db.TreeNode.Update().
		Where(enttreenode.ID(leafID), enttreenode.StatusEQ(st.TreeNodeStatusWatchtowerExitRecovered)).
		SetStatus(st.TreeNodeStatusWatchtowerExited).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to roll back leaf %s: %w", leafID, err)
	}
	if restored == 0 {
		logger.Sugar().Infof("recover watchtower exited leaf 2pc rollback: leaf %s not recovered, no-op", leafID)
		return nil
	}
	logger.Sugar().Infof("recover watchtower exited leaf 2pc rollback: leaf %s returned to %s", leafID, st.TreeNodeStatusWatchtowerExited)
	return nil
}

// ValidateDecisionAgainstPrepare binds a commit/rollback to the leaf this
// operator actually prepared. Both decision payloads take their target from a
// coordinator-supplied leaf id, so without this a coordinator could drive a
// legitimate flow to prepared and then send a decision naming another user's
// leaf.
func (h *RecoverWatchtowerExitedLeafFlowHandler) ValidateDecisionAgainstPrepare(prepareOp proto.Message, decisionOp proto.Message) error {
	prepared, ok := prepareOp.(*pbinternal.RecoverWatchtowerExitedLeafPrepareRequest)
	if !ok {
		return fmt.Errorf("unexpected prepare op type %T for recover watchtower exited leaf", prepareOp)
	}
	preparedLeafID := prepared.GetOriginalRequest().GetLeafId()

	var decisionLeafID string
	switch d := decisionOp.(type) {
	case *pbinternal.RecoverWatchtowerExitedLeafCommitRequest:
		decisionLeafID = d.GetLeafId()
	case *pbinternal.RecoverWatchtowerExitedLeafRollbackRequest:
		decisionLeafID = d.GetLeafId()
	case *pbinternal.RecoverWatchtowerExitedLeafPrepareRequest:
		decisionLeafID = d.GetOriginalRequest().GetLeafId()
	default:
		return fmt.Errorf("unexpected decision op type %T for recover watchtower exited leaf", decisionOp)
	}
	// sameTransferID rather than a string compare: UUID equality, and an
	// unparseable id on either side is a mismatch. A raw compare would accept a
	// decision with no leaf id against a prepare with no original request — both
	// read as "".
	if !sameTransferID(decisionLeafID, preparedLeafID) {
		return fmt.Errorf("decision names leaf %q but this operator prepared leaf %q", decisionLeafID, preparedLeafID)
	}
	return nil
}

// authorizeRecoverWatchtowerExitedLeaf loads and locks the leaf, checks the
// caller may recover it, and re-derives the recoverable output. Run unchanged in
// Prepare and on the coordinator's re-sign, so the two cannot drift.
//
// The owner's signature is the load-bearing check, not the session: the
// ConsensusPrepare channel reaching the other operators carries none. Without a
// statement each verifies itself, one coordinator could retire any user's leaf —
// useless to it, but enough to strand the value for good.
func authorizeRecoverWatchtowerExitedLeaf(ctx context.Context, req *pbspark.RecoverWatchtowerExitedLeafRequest) (*ent.TreeNode, *recoverableOutput, error) {
	if req.GetRecoveryTxSigningJob() == nil {
		return nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("recovery_tx_signing_job is required"))
	}
	// Validated here rather than where it is consumed, so a malformed commitment
	// fails Prepare everywhere instead of surfacing as a commit-side parse error.
	if req.GetRecoveryTxSigningJob().GetSigningNonceCommitment() == nil {
		return nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("recovery_tx_signing_job.signing_nonce_commitment is required"))
	}
	if err := (&frost.SigningCommitment{}).UnmarshalProto(req.GetRecoveryTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid recovery tx signing nonce commitment: %w", err))
	}
	leafID, err := uuid.Parse(req.GetLeafId())
	if err != nil {
		return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse leaf id %q: %w", req.GetLeafId(), err))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get db: %w", err)
	}
	leaf, err := db.TreeNode.Query().Where(enttreenode.ID(leafID)).ForUpdate().Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("leaf %s not found", leafID))
		}
		return nil, nil, fmt.Errorf("failed to load leaf %s: %w", leafID, err)
	}

	// A renewal split node shares its child's verifying key and keyshare but keeps
	// the owner it was created with, since transfers rotate the leaf only. Every
	// check below would then pass against that stale owner.
	hasChildren, err := leaf.QueryChildren().Exist(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to check whether node %s is a leaf: %w", leafID, err)
	}
	if hasChildren {
		return nil, nil, sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("node %s is not a leaf", leafID))
	}

	// Once a transfer's sender key tweaks are applied the value is the
	// receiver's — claimLeafTweakKey says so, and lets them claim a leaf that
	// exited to L1 mid-transfer for exactly that reason. But owner_identity_pubkey
	// still names the sender until SettleReceiverKeyTweak rotates it at claim
	// time, so every check below would pass for a sender recovering value the SE
	// already treats as spent. The leaf's own status cannot stand in for this:
	// the watchtower sweep overwrites TRANSFER_LOCKED.
	//
	// Asked of the leaf's own receiver rather than of the transfer, which under
	// MIMO stays non-terminal until every receiver has claimed — blocking one
	// receiver's recovery behind another's unrelated claim. The receiver status is
	// written whichever shape the transfer has, so it answers for both; only rows
	// predating the receiver edge fall back to the transfer.
	pendingTransfer, err := db.TransferLeaf.Query().
		Where(
			enttransferleaf.HasLeafWith(enttreenode.ID(leafID)),
			enttransferleaf.Or(
				enttransferleaf.HasTransferReceiverWith(
					enttransferreceiver.StatusIn(st.NonTerminalTransferReceiverStatuses()...)),
				enttransferleaf.And(
					enttransferleaf.Not(enttransferleaf.HasTransferReceiver()),
					enttransferleaf.HasTransferWith(
						enttransfer.StatusIn(st.NonTerminalTransferStatuses()...))),
			),
		).
		Exist(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to check leaf %s for a transfer in flight: %w", leafID, err)
	}
	if pendingTransfer {
		return nil, nil, sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("leaf %s has a transfer in flight; recover it once that transfer settles", leafID))
	}

	// WATCHTOWER_EXIT_RECOVERED is accepted so an owner can re-sign — to raise the fee,
	// or because they named the wrong one of a renewal chain's near-identical
	// transactions. Every recovery transaction spends the same output, so at most
	// one can confirm; the re-signer is whoever owns the leaf now, which a claim
	// can rotate (setAvailableUnlessExitedToL1 preserves this status).
	if !(leaf.Status == st.TreeNodeStatusWatchtowerExited || leaf.Status == st.TreeNodeStatusWatchtowerExitRecovered) {
		return nil, nil, sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("leaf %s is %s, expected %s or %s", leafID, leaf.Status,
				st.TreeNodeStatusWatchtowerExited, st.TreeNodeStatusWatchtowerExitRecovered))
	}

	recoverable, err := resolveRecoverableOutput(ctx, db, leaf, req.GetRecoveryTxSigningJob().GetRawTx())
	if err != nil {
		return nil, nil, err
	}

	if err := validateRecoverWatchtowerExitedLeafSignature(leaf, leaf.Network, req.GetUserSignature(), recoverable.sighash); err != nil {
		return nil, nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, leaf.OwnerIdentityPubkey); err != nil {
		return nil, nil, err
	}
	return leaf, recoverable, nil
}

// retireRecoveredLeaf moves the leaf out of the transferable pool, leaving an
// already-recovered leaf untouched so a re-sign is a no-op and a retry can
// finish a half-applied flow.
func retireRecoveredLeaf(ctx context.Context, leaf *ent.TreeNode) error {
	if leaf.Status == st.TreeNodeStatusWatchtowerExitRecovered {
		return nil
	}
	if _, err := leaf.Update().SetStatus(st.TreeNodeStatusWatchtowerExitRecovered).Save(ctx); err != nil {
		return fmt.Errorf("failed to mark leaf %s as %s: %w", leaf.ID, st.TreeNodeStatusWatchtowerExitRecovered, err)
	}
	return nil
}

// createRecoverWatchtowerExitedLeafStatement builds the message the owner signs
// to authorise a recovery. See the user_signature field in spark.proto for the
// wire-visible definition.
func createRecoverWatchtowerExitedLeafStatement(leafID uuid.UUID, network btcnetwork.Network, txSighash sighash.Hash) []byte {
	h := sha256.New()
	h.Write([]byte("recover_watchtower_exited_leaf"))
	h.Write([]byte(network.String()))
	h.Write([]byte(leafID.String()))
	h.Write(txSighash.Serialize())
	return h.Sum(nil)
}

func validateRecoverWatchtowerExitedLeafSignature(leaf *ent.TreeNode, network btcnetwork.Network, userSignature []byte, txSighash sighash.Hash) error {
	if len(userSignature) == 0 {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("user_signature is required"))
	}
	statement := createRecoverWatchtowerExitedLeafStatement(leaf.ID, network, txSighash)
	if err := common.VerifyECDSASignature(leaf.OwnerIdentityPubkey, userSignature, statement); err != nil {
		return sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("user signature does not authorise recovering leaf %s: %w", leaf.ID, err))
	}
	return nil
}

type recoverWatchtowerExitedLeafCoordinatorFlow struct {
	*RecoverWatchtowerExitedLeafFlowHandler

	req    *pbspark.RecoverWatchtowerExitedLeafRequest
	leafID uuid.UUID
	// signingCommitments are the coordinator-collected FROST round-1 commitments,
	// keyed by operator id, in both wire and parsed form.
	signingCommitments       map[string]*pbcommon.SigningCommitment
	signingCommitmentsParsed map[string]frost.SigningCommitment

	// response is populated in BuildCommitPayload for the public handler to return.
	response *pbspark.RecoverWatchtowerExitedLeafResponse
}

var _ consensus.CoordinatorFlow = (*recoverWatchtowerExitedLeafCoordinatorFlow)(nil)

func (f *recoverWatchtowerExitedLeafCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.RecoverWatchtowerExitedLeafPrepareRequest{
		OriginalRequest:    f.req,
		SigningCommitments: f.signingCommitments,
	}
}

func (f *recoverWatchtowerExitedLeafCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.RecoverWatchtowerExitedLeafRollbackRequest{LeafId: f.req.GetLeafId()}
}

// BuildCommitPayload aggregates the round-2 shares the operators returned from
// Prepare into the SigningResult the caller needs, and builds the response.
//
// The leaf is re-read and the sighash recomputed rather than carried from the
// entrypoint, so neither depends on state captured before the fan-out.
func (f *recoverWatchtowerExitedLeafCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	// The second return (participant ids) is unneeded: the signing set is already
	// tracked in f.signingCommitmentsParsed.
	allShares, _, err := collectSignatureShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	leaf, err := db.TreeNode.Get(ctx, f.leafID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload leaf %s: %w", f.leafID, err)
	}
	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare for leaf %s: %w", leaf.ID, err)
	}
	recoverable, err := resolveRecoverableOutput(ctx, db, leaf, f.req.GetRecoveryTxSigningJob().GetRawTx())
	if err != nil {
		return nil, err
	}
	userCommitment := frost.SigningCommitment{}
	if err := userCommitment.UnmarshalProto(f.req.GetRecoveryTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, fmt.Errorf("failed to parse user nonce commitment: %w", err)
	}

	verifyingKey := leaf.VerifyingPubkey
	jobID := recoverWatchtowerExitedLeafJobID(leaf.ID)
	job := &helper.SigningJob{
		JobID:             jobID,
		SigningKeyshareID: signingKeyshare.ID,
		Message:           recoverable.sighash,
		VerifyingKey:      &verifyingKey,
		UserCommitment:    &userCommitment,
	}

	operatorIDs := make([]string, 0, len(f.signingCommitmentsParsed))
	for id := range f.signingCommitmentsParsed {
		operatorIDs = append(operatorIDs, id)
	}
	selection, err := helper.NewPreSelectedOperatorSelection(f.config, operatorIDs)
	if err != nil {
		return nil, fmt.Errorf("unable to build signing operator selection: %w", err)
	}
	keyPackages, err := ent.GetKeyPackages(ctx, f.config, []uuid.UUID{signingKeyshare.ID})
	if err != nil {
		return nil, fmt.Errorf("unable to get key packages: %w", err)
	}
	round2, ok := allShares[jobID.String()]
	if !ok {
		return nil, fmt.Errorf("no round-2 shares collected for recovery job %s", jobID)
	}
	signingResults, err := helper.BuildSigningResults(
		f.config, selection,
		[]*helper.SigningJob{job}, keyPackages,
		[]map[string]frost.SigningCommitment{f.signingCommitmentsParsed},
		map[uuid.UUID]map[string][]byte{jobID: round2},
	)
	if err != nil {
		return nil, fmt.Errorf("unable to build signing result: %w", err)
	}
	// One job in, one result out — guarded before indexing so a future change
	// cannot panic the commit-build path, where a panic would crash the request
	// instead of flowing through the engine's rollback-on-error.
	if len(signingResults) == 0 {
		return nil, fmt.Errorf("no signing result produced for recovery job %s", jobID)
	}

	f.response = &pbspark.RecoverWatchtowerExitedLeafResponse{
		RecoveryTxSigningResult: signingResults[0].MarshalProto(),
		VerifyingKey:            verifyingKey.Serialize(),
	}
	return &pbinternal.RecoverWatchtowerExitedLeafCommitRequest{LeafId: f.req.GetLeafId()}, nil
}

func buildRecoverWatchtowerExitedLeafCoordinatorFlow(config *so.Config, req *pbspark.RecoverWatchtowerExitedLeafRequest, leafID uuid.UUID, round1 map[string][]frost.SigningCommitment) (*recoverWatchtowerExitedLeafCoordinatorFlow, error) {
	signingCommitments := make(map[string]*pbcommon.SigningCommitment, len(round1))
	signingCommitmentsParsed := make(map[string]frost.SigningCommitment, len(round1))
	for opID, commitments := range round1 {
		// Collected with count=1, so exactly one per operator; guarding here stops a
		// future count change from silently dropping the extras and desyncing
		// round-1 from round-2 aggregation.
		if len(commitments) != 1 {
			return nil, fmt.Errorf("expected exactly 1 round-1 commitment for operator %s, got %d", opID, len(commitments))
		}
		signingCommitments[opID] = commitments[0].MarshalProto()
		signingCommitmentsParsed[opID] = commitments[0]
	}
	return &recoverWatchtowerExitedLeafCoordinatorFlow{
		RecoverWatchtowerExitedLeafFlowHandler: NewRecoverWatchtowerExitedLeafFlowHandler(config),
		req:                                    req,
		leafID:                                 leafID,
		signingCommitments:                     signingCommitments,
		signingCommitmentsParsed:               signingCommitmentsParsed,
	}, nil
}

// ---------------------------------------------------------------------------
// Coordinator entrypoint
// ---------------------------------------------------------------------------

// RecoverWatchtowerExitedLeaf co-signs a spend of the output a watchtower exit
// stranded, for the leaf that exit cut off.
//
// A WATCHTOWER_EXITED leaf goes through the consensus engine, because retiring
// it has to land on every operator. An already-recovered leaf skips the engine
// and just signs — the state change happened on the first call — which is what
// makes a fee bump cheap.
func (h *RecoverWatchtowerExitedLeafFlowHandler) RecoverWatchtowerExitedLeaf(ctx context.Context, req *pbspark.RecoverWatchtowerExitedLeafRequest) (*pbspark.RecoverWatchtowerExitedLeafResponse, error) {
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}
	leaf, recoverable, err := authorizeRecoverWatchtowerExitedLeaf(ctx, req)
	if err != nil {
		return nil, err
	}
	// Only reachable on the coordinator, which is the only operator the caller's
	// session reaches. Additive to the owner signature checked above, never a
	// substitute for it — that signature is what the other operators verify.
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, leaf.OwnerIdentityPubkey); err != nil {
		return nil, err
	}

	if leaf.Status == st.TreeNodeStatusWatchtowerExitRecovered {
		return h.resignRecoveredLeaf(ctx, leaf, req, recoverable)
	}

	// Collected on the coordinator so the public RPC stays a single call: the
	// client supplies only its own nonce, and round 2 runs inside the engine's
	// prepare fan-out.
	round1, err := helper.GetSigningCommitments(ctx, h.config, 1, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to collect round-1 signing commitments: %w", err)
	}
	flow, err := buildRecoverWatchtowerExitedLeafCoordinatorFlow(h.config, req, leaf.ID, round1)
	if err != nil {
		return nil, fmt.Errorf("unable to build coordinator flow: %w", err)
	}
	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	if _, err := engine.Execute(ctx, pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_RECOVER_WATCHTOWER_EXITED_LEAF, &selection, flow); err != nil {
		return nil, fmt.Errorf("consensus recover watchtower exited leaf failed: %w", err)
	}
	if flow.response == nil {
		return nil, fmt.Errorf("recover watchtower exited leaf consensus completed without building a response")
	}
	return flow.response, nil
}

// resignRecoveredLeaf signs another recovery transaction for a leaf that is
// already retired. No consensus round: nothing is written anywhere, and every
// transaction signed this way spends an output under the same key, so at most one
// of them can ever confirm no matter how many exist.
func (h *RecoverWatchtowerExitedLeafFlowHandler) resignRecoveredLeaf(ctx context.Context, leaf *ent.TreeNode, req *pbspark.RecoverWatchtowerExitedLeafRequest, recoverable *recoverableOutput) (*pbspark.RecoverWatchtowerExitedLeafResponse, error) {
	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare for leaf %s: %w", leaf.ID, err)
	}
	userCommitment := frost.SigningCommitment{}
	if err := userCommitment.UnmarshalProto(req.GetRecoveryTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse user nonce commitment: %w", err))
	}
	verifyingKey := leaf.VerifyingPubkey
	signingResults, err := helper.SignFrost(ctx, h.config, []*helper.SigningJob{{
		JobID:             recoverWatchtowerExitedLeafJobID(leaf.ID),
		SigningKeyshareID: signingKeyshare.ID,
		Message:           recoverable.sighash,
		VerifyingKey:      &verifyingKey,
		UserCommitment:    &userCommitment,
	}})
	if err != nil {
		return nil, fmt.Errorf("failed to re-sign recovery tx for leaf %s: %w", leaf.ID, err)
	}
	if len(signingResults) == 0 {
		return nil, fmt.Errorf("no signing result produced re-signing recovery tx for leaf %s", leaf.ID)
	}
	logging.GetLoggerFromContext(ctx).Sugar().Infof(
		"re-signed a recovery tx for already-recovered leaf %s spending the output of node %s", leaf.ID, recoverable.sourceNodeID)
	return &pbspark.RecoverWatchtowerExitedLeafResponse{
		RecoveryTxSigningResult: signingResults[0].MarshalProto(),
		VerifyingKey:            verifyingKey.Serialize(),
	}, nil
}
