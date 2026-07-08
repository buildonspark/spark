package handler

import (
	"context"
	"fmt"

	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/sighash"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
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
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/staticdeposit"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// StaticDepositUtxoRefundFlowHandler — participant side (Prepare/Commit/Rollback)
// ---------------------------------------------------------------------------

// StaticDepositUtxoRefundFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_REFUND. Reached via the engine when
// InitiateStaticDepositUtxoRefund routes through it (gated on
// KnobUseConsensusStaticDepositUtxoRefund).
//
// The signing keeps the public RPC a single call: the coordinator collects FROST
// round-1 commitments before engine.Execute and carries them in the prepare op, so
// round-2 runs inside Prepare (the engine's fan-out IS the round-2 trip) and the
// coordinator builds the user-aggregated SigningResult in BuildCommitPayload.
type StaticDepositUtxoRefundFlowHandler struct {
	config *so.Config
}

var _ consensus.FlowHandler = (*StaticDepositUtxoRefundFlowHandler)(nil)

func NewStaticDepositUtxoRefundFlowHandler(config *so.Config) *StaticDepositUtxoRefundFlowHandler {
	return &StaticDepositUtxoRefundFlowHandler{config: config}
}

// staticDepositRefundJobNamespace is a fixed UUIDv4 mixed into NewSHA1 to derive a
// deterministic signing-job id from the utxo, so every SO and the coordinator
// correlate the round-2 shares without sending the job id over the wire.
var staticDepositRefundJobNamespace = uuid.MustParse("b8e4d1f0-2c6a-4e3b-9d5f-7a1c0e8b3d2a")

// txid is the raw bytes from the proto UTXO field (reversed display order). Both
// callers — buildRefundRound2Job on each SO and BuildCommitPayload on the
// coordinator — pass that same proto field, so the derived id always agrees.
//
// The id is deliberately identical across retry attempts for the same UTXO:
// round-2 shares are only ever correlated within a single Execute's results
// map (never a cross-request store), so a stale attempt's share cannot leak
// into a newer attempt's aggregation.
func staticDepositRefundJobID(txid []byte, vout uint32) uuid.UUID {
	return uuid.NewSHA1(staticDepositRefundJobNamespace, fmt.Appendf(nil, "%x:%d", txid, vout))
}

// Prepare runs on every SO. It validates the refund request and creates the
// UtxoSwap CREATED row (the work the legacy CreateStaticDepositUtxoRefund fanout
// does on each SO, minus the coordinator-ECDSA-signature check — the engine
// authenticates the coordinator). SOs whose identifier is in the coordinator-
// collected round-1 commitment set also produce their FROST round-2 share over the
// refund-tx sighash.
func (h *StaticDepositUtxoRefundFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	prepareReq, ok := op.(*pbinternal.StaticDepositUtxoRefundPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for static deposit utxo refund prepare", op)
	}
	req := prepareReq.GetOriginalRequest()
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}

	depositAddress, spendTxSighash, err := h.createRefundSwap(ctx, req)
	if err != nil {
		return nil, err
	}

	// Only SOs in the coordinator's round-1 signing set produce a round-2 share;
	// the rest return nil (collectSignatureShares skips them). The share is
	// returned as a bare FrostRound2Response so the coordinator can aggregate it
	// with the shared collectSignatureShares helper.
	if _, inSigningSet := prepareReq.GetSigningCommitments()[h.config.Identifier]; !inSigningSet {
		return nil, nil
	}
	job, err := h.buildRefundRound2Job(ctx, depositAddress, req, spendTxSighash, prepareReq.GetSigningCommitments())
	if err != nil {
		return nil, err
	}
	frostResp, err := signing_handler.NewFrostSigningHandler(h.config).FrostRound2(ctx, &pbinternal.FrostRound2Request{SigningJobs: []*pbinternal.SigningJob{job}})
	if err != nil {
		return nil, fmt.Errorf("local frost round 2 failed during prepare: %w", err)
	}
	return frostResp, nil
}

// createRefundSwap mirrors the participant body of the legacy
// CreateStaticDepositUtxoRefund (static_deposit_internal_handler.go) — every
// validation it performs is preserved. Legacy's two coordinator checks are
// replaced as follows: the ECDSA-over-statement signature is subsumed by the
// authenticated (IP-restricted) ConsensusPrepare channel, and the
// `coordinatorIsSO` membership check is upheld by deriving the coordinator
// identity from the engine's coordinator_index (coordinatorIdentityForSwap)
// instead of trusting a self-declared payload field — so the stored
// CoordinatorIdentityPublicKey is guaranteed to be a real signing operator. The
// user signature (the real authorization) is still verified.
func (h *StaticDepositUtxoRefundFlowHandler) createRefundSwap(ctx context.Context, req *pbspark.InitiateStaticDepositUtxoRefundRequest) (*ent.DepositAddress, sighash.Hash, error) {
	if req.GetOnChainUtxo() == nil {
		return nil, sighash.Hash{}, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("on_chain_utxo is required"))
	}
	if req.GetRefundTxSigningJob() == nil {
		return nil, sighash.Hash{}, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("refund_tx_signing_job is required"))
	}
	// Validate the user nonce commitment here in Prepare (on every SO) so a missing
	// or malformed commitment fails Prepare deterministically. BuildCommitPayload
	// later parses this same commitment to assemble the SigningResult; validating it
	// up front upholds the 2PC invariant that a successful Prepare cannot be followed
	// by a commit-side parse failure that rolls back an otherwise-prepared flow.
	if req.GetRefundTxSigningJob().GetSigningNonceCommitment() == nil {
		return nil, sighash.Hash{}, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("refund_tx_signing_job.signing_nonce_commitment is required"))
	}
	if err := (&frost.SigningCommitment{}).UnmarshalProto(req.GetRefundTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, sighash.Hash{}, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid refund tx signing nonce commitment: %w", err))
	}
	network, err := btcnetwork.FromProtoNetwork(req.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, sighash.Hash{}, fmt.Errorf("unable to parse network: %w", err)
	}
	if !h.config.IsNetworkSupported(network) {
		return nil, sighash.Hash{}, fmt.Errorf("network %s not supported", network)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, sighash.Hash{}, fmt.Errorf("failed to get db: %w", err)
	}

	targetUtxo, err := VerifiedTargetUtxoFromRequest(ctx, h.config, db, network, req.GetOnChainUtxo(), nil)
	if err != nil {
		return nil, sighash.Hash{}, err
	}
	depositAddress, err := targetUtxo.inner.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, sighash.Hash{}, fmt.Errorf("unable to get utxo deposit address: %w", err)
	}
	if !depositAddress.IsStatic {
		return nil, sighash.Hash{}, fmt.Errorf("unable to refund a deposit to a non-static address")
	}

	spendTxSighash, totalAmount, err := GetTxSigningInfo(ctx, targetUtxo.inner, req.GetRefundTxSigningJob().GetRawTx())
	if err != nil {
		return nil, sighash.Hash{}, fmt.Errorf("failed to get spend tx sighash: %w", err)
	}
	if err := validateStaticDepositRefundTx(targetUtxo, req.GetRefundTxSigningJob().GetRawTx()); err != nil {
		return nil, sighash.Hash{}, err
	}

	existingSwap, err := staticdeposit.GetRegisteredUtxoSwapForUtxo(ctx, db, targetUtxo.inner)
	if err != nil {
		return nil, sighash.Hash{}, fmt.Errorf("unable to check if utxo swap is already registered: %w", err)
	}
	if existingSwap != nil {
		return nil, sighash.Hash{}, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("utxo swap is already registered"))
	}

	if err := validateUserSignature(depositAddress.OwnerIdentityPubkey, req.GetUserSignature(), spendTxSighash.Serialize(), pbspark.UtxoSwapRequestType_Refund, network, targetUtxo.Hash().String(), targetUtxo.Vout(), totalAmount, req.GetHashVariant()); err != nil {
		return nil, sighash.Hash{}, fmt.Errorf("user signature validation failed: %w", err)
	}

	coordinatorPubKey := h.coordinatorIdentityForSwap(ctx)

	utxoSwap, err := db.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetUtxo(targetUtxo.inner).
		SetUtxoValueSats(targetUtxo.inner.Amount).
		SetRequestType(st.UtxoSwapFromProtoRequestType(pbspark.UtxoSwapRequestType_Refund)).
		SetCreditAmountSats(totalAmount).
		SetSspSignature(spendTxSighash.Serialize()).
		SetSspIdentityPublicKey(depositAddress.OwnerIdentityPubkey).
		SetUserIdentityPublicKey(depositAddress.OwnerIdentityPubkey).
		SetCoordinatorIdentityPublicKey(coordinatorPubKey).
		Save(ctx)
	if err != nil {
		if sqlgraph.IsUniqueConstraintError(err) {
			return nil, sighash.Hash{}, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("utxo swap already exists: %w", err))
		}
		return nil, sighash.Hash{}, fmt.Errorf("unable to store utxo swap: %w", err)
	}
	if err := addUtxoSwapToDepositAddress(ctx, db, depositAddress.ID, utxoSwap); err != nil {
		return nil, sighash.Hash{}, err
	}
	return depositAddress, spendTxSighash, nil
}

// coordinatorIdentityForSwap returns the identity public key recorded as the
// coordinator on this SO's UtxoSwap row. On a participant it comes from ctx,
// where DispatchPrepare attached it after resolving the engine's
// coordinator_index against the operator config — DispatchPrepare fails closed
// on an unresolvable index and rejects an index naming the receiving SO, so a
// missing ctx value here can only mean the coordinator's own self-Prepare (the
// engine calls Prepare directly, no ConsensusPrepare RPC), where falling back
// to this SO's own identity key is correct by definition. Both branches yield
// a real SO key, never a self-declared payload value; note the index proves SO
// membership, not key possession (engine-wide peer cross-check is a tracked
// follow-up).
func (h *StaticDepositUtxoRefundFlowHandler) coordinatorIdentityForSwap(ctx context.Context) keys.Public {
	if coordinatorPubKey, ok := consensus.CoordinatorIdentityFromContext(ctx); ok {
		return coordinatorPubKey
	}
	return h.config.IdentityPublicKey()
}

// buildRefundRound2Job constructs the FROST round-2 job for this SO's share over
// the refund-tx sighash, using the coordinator-collected round-1 commitments and
// the user's nonce commitment. Mirrors frostRound2's job construction
// (signing_coordinator.go) and getSpendTxSigningResult's key material
// (verifyingKey = signingKeyshare.PublicKey + depositAddress.OwnerSigningPubkey).
func (h *StaticDepositUtxoRefundFlowHandler) buildRefundRound2Job(ctx context.Context, depositAddress *ent.DepositAddress, req *pbspark.InitiateStaticDepositUtxoRefundRequest, spendTxSighash sighash.Hash, round1 map[string]*pbcommon.SigningCommitment) (*pbinternal.SigningJob, error) {
	signingKeyshare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}
	verifyingKey := signingKeyshare.PublicKey.Add(depositAddress.OwnerSigningPubkey)
	jobID := staticDepositRefundJobID(req.GetOnChainUtxo().GetTxid(), req.GetOnChainUtxo().GetVout())
	return &pbinternal.SigningJob{
		JobId:           jobID.String(),
		Message:         spendTxSighash.Serialize(),
		KeyshareId:      signingKeyshare.ID.String(),
		VerifyingKey:    verifyingKey.Serialize(),
		Commitments:     round1,
		UserCommitments: req.GetRefundTxSigningJob().GetSigningNonceCommitment(),
	}, nil
}

// Commit marks this SO's UtxoSwap COMPLETED. Idempotent against gossip
// redelivery: an already-COMPLETED swap is a no-op. A missing/cancelled row is
// an error (deliberately asymmetric with Rollback's no-op) so redelivery keeps
// retrying instead of silently dropping a committed flow. If the row is
// genuinely gone the retries never resolve — that surfaces via the
// flow_execution reconcile task (task/flow_execution_reconcile.go): the
// participant FlowExecution row stays IN_FLIGHT past the stuck threshold,
// which fires a task-level error (→ scheduler ERROR log → Slack) every tick
// and, when KnobFlowExecutionMetricsEnabled is on, emits the
// flow_execution.in_flight_count / oldest_in_flight_age_ms gauges.
func (h *StaticDepositUtxoRefundFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.StaticDepositUtxoRefundCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for static deposit utxo refund commit", op)
	}
	return h.completeRefundSwap(ctx, req.GetOnChainUtxo())
}

func (h *StaticDepositUtxoRefundFlowHandler) completeRefundSwap(ctx context.Context, utxo *pbspark.UTXO) error {
	swap, err := h.loadSwapForUtxo(ctx, utxo)
	if err != nil {
		return err
	}
	if swap == nil {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("no registered utxo swap to complete for %x:%d", utxo.GetTxid(), utxo.GetVout()))
	}
	if swap.Status == st.UtxoSwapStatusCompleted {
		logging.GetLoggerFromContext(ctx).Sugar().Infof("static deposit refund 2pc commit: swap %s already COMPLETED, idempotent retry", swap.ID)
		return nil
	}
	if err := CompleteUtxoSwap(ctx, swap); err != nil {
		return fmt.Errorf("unable to complete utxo swap %s: %w", swap.ID, err)
	}
	return nil
}

// Rollback cancels this SO's UtxoSwap (CANCELLED). REFUND swaps have no transfer
// edge, so CancelUtxoSwap skips the SP-3261 "never cancel a sent transfer" guard.
// Idempotent: a never-created / already-cancelled swap is a no-op, and a
// post-commit COMPLETED swap is absorbed as a no-op (CancelUtxoSwap would error on
// COMPLETED, which runConsensusRollback would loop on). Accepts both the canonical
// rollback payload and the prepare op echoed by the participant reconciler.
//
// on_chain_utxo does not discriminate between attempts, so this handler must
// never see a redelivered rollback for a flow that already applied: once a
// cancelled swap frees the unique (utxo, status != CANCELLED) slot, a retry's
// fresh swap for the same UTXO is exactly what a stale rollback would find here.
// classifyConsensusOp's terminal-row fence guarantees that (redelivery for a
// terminal participant FlowExecution row is skipped before dispatch).
func (h *StaticDepositUtxoRefundFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var utxo *pbspark.UTXO
	switch r := op.(type) {
	case *pbinternal.StaticDepositUtxoRefundRollbackRequest:
		utxo = r.GetOnChainUtxo()
	case *pbinternal.StaticDepositUtxoRefundPrepareRequest:
		utxo = r.GetOriginalRequest().GetOnChainUtxo()
	default:
		return fmt.Errorf("unexpected operation type %T for static deposit utxo refund rollback", op)
	}
	if utxo == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("on_chain_utxo is required for rollback"))
	}

	swap, err := h.loadSwapForUtxo(ctx, utxo)
	if err != nil {
		return err
	}
	logger := logging.GetLoggerFromContext(ctx)
	if swap == nil {
		// Prepare never created the swap on this SO (or it is already cancelled).
		logger.Sugar().Infof("static deposit refund 2pc rollback: no active swap for %x:%d, no-op", utxo.GetTxid(), utxo.GetVout())
		return nil
	}
	if swap.Status == st.UtxoSwapStatusCompleted {
		// Post-commit. In correct 2PC a rollback never lands here (the engine's
		// atomic decision means a committed flow drives forward), but absorb a
		// stray/redelivered rollback as a no-op rather than letting CancelUtxoSwap
		// error and loop runConsensusRollback.
		logger.Sugar().Infof("static deposit refund 2pc rollback: swap %s already COMPLETED, no-op", swap.ID)
		return nil
	}
	if err := CancelUtxoSwap(ctx, swap); err != nil {
		return fmt.Errorf("unable to cancel utxo swap %s during rollback: %w", swap.ID, err)
	}
	logger.Sugar().Infof("static deposit refund 2pc rollback: swap %s marked CANCELLED", swap.ID)
	return nil
}

// loadSwapForUtxo finds the active (CREATED/COMPLETED) REFUND UtxoSwap for the
// utxo, or nil if the utxo or an active refund swap doesn't exist (a
// CANCELLED-only utxo returns nil). Used by Commit and Rollback to locate the
// row to complete / cancel. It queries the utxo row directly — unlike Prepare
// it does NOT re-check on-chain confirmation, since completing or cancelling an
// already-prepared swap must not depend on the current confirmation state.
//
// Filtering to RequestType REFUND (unlike Prepare's conflict check, which must
// see any active swap) means Commit/Rollback can only ever touch a row this
// flow type created: even if this flow's refund row is cancelled out from under
// it (e.g. a stray legacy utxo-keyed rollback) and a different-type swap claims
// the freed active slot before a redelivered decision lands, that swap is
// invisible here rather than completed/cancelled by mistake.
func (h *StaticDepositUtxoRefundFlowHandler) loadSwapForUtxo(ctx context.Context, utxo *pbspark.UTXO) (*ent.UtxoSwap, error) {
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
	swap, err := db.UtxoSwap.Query().
		Where(
			entutxoswap.HasUtxoWith(entutxo.IDEQ(targetUtxo.ID)),
			entutxoswap.StatusIn(st.UtxoSwapStatusCreated, st.UtxoSwapStatusCompleted),
			entutxoswap.RequestTypeEQ(st.UtxoSwapRequestTypeRefund),
		).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	return swap, nil
}

// ---------------------------------------------------------------------------
// staticDepositUtxoRefundCoordinatorFlow — coordinator side
// ---------------------------------------------------------------------------

type staticDepositUtxoRefundCoordinatorFlow struct {
	*StaticDepositUtxoRefundFlowHandler

	req *pbspark.InitiateStaticDepositUtxoRefundRequest
	// targetUtxo is verified once in the coordinator entrypoint (same request tx)
	// and reused in BuildCommitPayload, so the on-chain confirmation check is not
	// re-run there — avoiding a spurious rollback if a reorg lands between Prepare
	// and BuildCommitPayload.
	targetUtxo *VerifiedTargetUtxo
	// signingCommitments are the FROST round-1 commitments the coordinator
	// collected (GetSigningCommitments) before Execute, keyed by operator id.
	signingCommitments       map[string]*pbcommon.SigningCommitment
	signingCommitmentsParsed map[string]frost.SigningCommitment

	// response is populated in BuildCommitPayload for the public handler to return.
	response *pbspark.InitiateStaticDepositUtxoRefundResponse
}

var _ consensus.CoordinatorFlow = (*staticDepositUtxoRefundCoordinatorFlow)(nil)

func (f *staticDepositUtxoRefundCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.StaticDepositUtxoRefundPrepareRequest{
		OriginalRequest:    f.req,
		SigningCommitments: f.signingCommitments,
	}
}

// BuildCommitPayload collects the round-2 shares from the SO prepare results,
// builds the user-aggregated SigningResult (via helper.BuildSigningResults — the
// same prepareResults the non-consensus path uses, so the bytes are identical),
// stores it on the coordinator's swap row and marks it COMPLETED, and builds the
// response. Returns the commit op carrying the utxo so participants complete too.
func (f *staticDepositUtxoRefundCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	// The second return (participant ids) is unneeded — the signing set is
	// already tracked in f.signingCommitmentsParsed.
	allShares, _, err := collectSignatureShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	// Reuse the UTXO verified in the entrypoint (same request tx) rather than
	// re-running the on-chain confirmation check, which could spuriously fail on a
	// reorg between Prepare and here and roll back an otherwise-prepared flow.
	targetUtxo := f.targetUtxo
	depositAddress, err := targetUtxo.inner.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo deposit address: %w", err)
	}
	signingKeyshare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}
	verifyingKey := signingKeyshare.PublicKey.Add(depositAddress.OwnerSigningPubkey)
	spendTxSighash, _, err := GetTxSigningInfo(ctx, targetUtxo.inner, f.req.GetRefundTxSigningJob().GetRawTx())
	if err != nil {
		return nil, fmt.Errorf("failed to get spend tx sighash: %w", err)
	}
	userNonce := frost.SigningCommitment{}
	if err := userNonce.UnmarshalProto(f.req.GetRefundTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, fmt.Errorf("failed to parse user nonce commitment: %w", err)
	}

	jobID := staticDepositRefundJobID(f.req.GetOnChainUtxo().GetTxid(), f.req.GetOnChainUtxo().GetVout())
	job := &helper.SigningJob{
		JobID:             jobID,
		SigningKeyshareID: signingKeyshare.ID,
		Message:           spendTxSighash,
		VerifyingKey:      &verifyingKey,
		UserCommitment:    &userNonce,
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
		return nil, fmt.Errorf("no round-2 shares collected for refund job %s", jobID)
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
	// One job in => one result out, but guard before indexing so a future change can
	// never panic the commit-build path (a panic crashes the request instead of
	// flowing through the engine's rollback-on-error). Matches legacy getSpendTxSigningResult.
	if len(signingResults) == 0 {
		return nil, fmt.Errorf("no signing result produced for refund job %s", jobID)
	}
	signingResultProto := signingResults[0].MarshalProto()

	// Apply on the coordinator's DB: store the signing result + mark COMPLETED.
	// The coordinator's own flow.Prepare created this row CREATED earlier in this
	// same request transaction (twopc.go runs the coordinator's Prepare and
	// BuildCommitPayload against the request session, committed atomically), so the
	// row is exclusively held by this tx and invisible to concurrent gossip — no
	// extra ForUpdate lock is needed, and CompleteUtxoSwap on a REFUND swap is
	// idempotent on an already-COMPLETED status.
	swap, err := staticdeposit.GetRegisteredUtxoSwapForUtxo(ctx, db, targetUtxo.inner)
	if err != nil {
		return nil, fmt.Errorf("unable to load coordinator utxo swap for %x:%d: %w", f.req.GetOnChainUtxo().GetTxid(), f.req.GetOnChainUtxo().GetVout(), err)
	}
	if swap == nil {
		return nil, fmt.Errorf("coordinator utxo swap not found for %x:%d after prepare", f.req.GetOnChainUtxo().GetTxid(), f.req.GetOnChainUtxo().GetVout())
	}
	signingResultBytes, err := proto.Marshal(signingResultProto)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal signing result bytes: %w", err)
	}
	if _, err := swap.Update().SetSpendTxSigningResult(signingResultBytes).Save(ctx); err != nil {
		return nil, fmt.Errorf("unable to store spend tx signing result: %w", err)
	}
	if err := CompleteUtxoSwap(ctx, swap); err != nil {
		return nil, fmt.Errorf("unable to complete coordinator utxo swap: %w", err)
	}

	f.response = &pbspark.InitiateStaticDepositUtxoRefundResponse{
		RefundTxSigningResult: signingResultProto,
		DepositAddress: &pbspark.DepositAddressQueryResult{
			DepositAddress:       depositAddress.Address,
			UserSigningPublicKey: depositAddress.OwnerSigningPubkey.Serialize(),
			VerifyingPublicKey:   verifyingKey.Serialize(),
			LeafId:               new(depositAddress.NodeID.String()),
		},
	}
	return &pbinternal.StaticDepositUtxoRefundCommitRequest{OnChainUtxo: f.req.GetOnChainUtxo()}, nil
}

func (f *staticDepositUtxoRefundCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.StaticDepositUtxoRefundRollbackRequest{OnChainUtxo: f.req.GetOnChainUtxo()}
}

// buildStaticDepositUtxoRefundCoordinatorFlow parses the coordinator-collected
// round-1 commitments into the proto + frost representations the flow needs.
func buildStaticDepositUtxoRefundCoordinatorFlow(config *so.Config, req *pbspark.InitiateStaticDepositUtxoRefundRequest, targetUtxo *VerifiedTargetUtxo, round1 map[string][]frost.SigningCommitment) (*staticDepositUtxoRefundCoordinatorFlow, error) {
	signingCommitments := make(map[string]*pbcommon.SigningCommitment, len(round1))
	signingCommitmentsParsed := make(map[string]frost.SigningCommitment, len(round1))
	for opID, commitments := range round1 {
		// GetSigningCommitments is called with count=1, so exactly one commitment
		// per operator is expected; guard against a future count change silently
		// dropping the extras (which would desync round-1 from round-2 aggregation).
		if len(commitments) != 1 {
			return nil, fmt.Errorf("expected exactly 1 round-1 commitment for operator %s, got %d", opID, len(commitments))
		}
		signingCommitments[opID] = commitments[0].MarshalProto()
		signingCommitmentsParsed[opID] = commitments[0]
	}
	return &staticDepositUtxoRefundCoordinatorFlow{
		StaticDepositUtxoRefundFlowHandler: NewStaticDepositUtxoRefundFlowHandler(config),
		req:                                req,
		targetUtxo:                         targetUtxo,
		signingCommitments:                 signingCommitments,
		signingCommitmentsParsed:           signingCommitmentsParsed,
	}, nil
}

// ---------------------------------------------------------------------------
// Coordinator entrypoint
// ---------------------------------------------------------------------------

// initiateStaticDepositUtxoRefundConsensus is the knob-gated 2PC entrypoint. It
// runs the coordinator-only validation + idempotency short-circuit (identical to
// the legacy InitiateStaticDepositUtxoRefund), collects FROST round-1 commitments
// (keeping the public RPC a single call), then drives the flow through the engine.
func (o *StaticDepositHandler) initiateStaticDepositUtxoRefundConsensus(ctx context.Context, config *so.Config, req *pbspark.InitiateStaticDepositUtxoRefundRequest) (*pbspark.InitiateStaticDepositUtxoRefundResponse, error) {
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}
	if req.GetOnChainUtxo() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("on_chain_utxo is required"))
	}
	if req.GetRefundTxSigningJob() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("refund_tx_signing_job is required"))
	}
	if req.GetRefundTxSigningJob().GetSigningNonceCommitment() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("refund_tx_signing_job.signing_nonce_commitment is required"))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	schemaNetwork, err := btcnetwork.FromProtoNetwork(req.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, err
	}
	targetUtxo, err := VerifiedTargetUtxoFromRequest(ctx, config, db, schemaNetwork, req.GetOnChainUtxo(), nil)
	if err != nil {
		return nil, err
	}
	if err := validateStaticDepositRefundTx(targetUtxo, req.GetRefundTxSigningJob().GetRawTx()); err != nil {
		return nil, err
	}

	// Idempotency: a refund swap that is already COMPLETED can no longer be swapped;
	// the owner may sign additional refund txs, so re-sign and return without the
	// engine. Shares the legacy re-sign helper so the semantics cannot drift.
	existingSwap, err := staticdeposit.GetRegisteredUtxoSwapForUtxo(ctx, db, targetUtxo.inner)
	if err != nil {
		return nil, err
	}
	if existingSwap != nil {
		return o.handleAlreadyRegisteredSwapOnRefund(ctx, config, existingSwap, targetUtxo, schemaNetwork, req)
	}

	// Fast-fail the user signature before any cross-operator work: the round-1
	// collection below consumes a persisted FROST nonce on every selected
	// operator and Execute fans Prepare out to all of them, and a static
	// deposit's UTXO is public — without this check, any caller could burn that
	// cluster-wide work with a bogus signature. Additive only: every participant
	// still verifies the signature itself in Prepare (createRefundSwap); a
	// coordinator-side check is never a substitute for that.
	depositAddress, err := targetUtxo.inner.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo deposit address: %w", err)
	}
	// Reject a non-static address before fanning out: each participant re-checks
	// this in Prepare (createRefundSwap), but catching it here turns a doomed
	// Execute-then-rollback into an immediate, clearer rejection.
	if !depositAddress.IsStatic {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("unable to refund a deposit to a non-static address"))
	}
	spendTxSighash, totalAmount, err := GetTxSigningInfo(ctx, targetUtxo.inner, req.GetRefundTxSigningJob().GetRawTx())
	if err != nil {
		return nil, fmt.Errorf("failed to get spend tx sighash: %w", err)
	}
	if err := validateUserSignature(depositAddress.OwnerIdentityPubkey, req.GetUserSignature(), spendTxSighash.Serialize(), pbspark.UtxoSwapRequestType_Refund, schemaNetwork, targetUtxo.Hash().String(), targetUtxo.Vout(), totalAmount, req.GetHashVariant()); err != nil {
		return nil, fmt.Errorf("user signature validation failed: %w", err)
	}

	// Collect FROST round-1 commitments server-side so round-2 can run inside the
	// engine's Prepare while the public RPC stays a single call.
	round1, err := helper.GetSigningCommitments(ctx, config, 1, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to collect round-1 signing commitments: %w", err)
	}

	flow, err := buildStaticDepositUtxoRefundCoordinatorFlow(config, req, targetUtxo, round1)
	if err != nil {
		return nil, fmt.Errorf("unable to build coordinator flow: %w", err)
	}
	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	if _, err := engine.Execute(ctx, pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_REFUND, &selection, flow); err != nil {
		return nil, fmt.Errorf("consensus static deposit utxo refund failed: %w", err)
	}
	if flow.response == nil {
		return nil, fmt.Errorf("static deposit utxo refund consensus completed without building a response")
	}
	return flow.response, nil
}
