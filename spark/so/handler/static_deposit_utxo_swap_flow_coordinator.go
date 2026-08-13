//go:build lightspark

package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/staticdeposit"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// staticDepositUtxoSwapCoordinatorFlow lives in a lightspark-tagged file (unlike the
// participant handler) because the coordinator entrypoint + response are SSP-internal API
// (proto/spark_ssp_internal), excluded from OSS builds. OSS participants still dispatch the
// op type via the untagged flow handler.

type staticDepositUtxoSwapCoordinatorFlow struct {
	*StaticDepositUtxoSwapFlowHandler

	req *pbinternal.InitiateStaticDepositUtxoSwapRequest
	// targetUtxo is verified once in the coordinator entrypoint (same request
	// tx) and reused in BuildCommitPayload, so the on-chain confirmation check
	// is not re-run there — avoiding a spurious rollback if a reorg lands
	// between Prepare and BuildCommitPayload.
	targetUtxo *VerifiedTargetUtxo
	// transferCoord aggregates the nested transfer's leaf signatures, applies
	// the transfer commit on the coordinator, records partner attribution, and
	// builds the transfer response. Its embedded handler is the swap flow's
	// utxo-swap-typed SendTransferFlowHandler.
	transferCoord *sendTransferCoordinatorFlow
	// Spend-tx FROST round-1 commitments the coordinator collected
	// (GetSigningCommitments) before Execute, keyed by operator id.
	spendCommitments       map[string]*pbcommon.SigningCommitment
	spendCommitmentsParsed map[string]frost.SigningCommitment

	// response is populated in BuildCommitPayload for the public handler to return.
	response *pbssp.InitiateStaticDepositUtxoSwapResponse
}

var _ consensus.CoordinatorFlow = (*staticDepositUtxoSwapCoordinatorFlow)(nil)

func (f *staticDepositUtxoSwapCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.StaticDepositUtxoSwapPrepareRequest{
		OriginalRequest:           f.req,
		SpendTxSigningCommitments: f.spendCommitments,
		SenderKeyTweakProofs:      f.transferCoord.senderKeyTweakProofs,
	}
}

// BuildCommitPayload first delegates the nested transfer to the send-transfer
// coordinator flow (leaf-signature aggregation, coordinator-local transfer
// commit → SENDER_KEY_TWEAKED, partner attribution, transfer response), then
// aggregates the spend-tx signature from the same prepare results, stores it on
// the coordinator's swap row, marks the swap COMPLETED, and builds the RPC
// response. The transfer delegation MUST run first: CompleteUtxoSwap requires
// the associated transfer to be sent.
func (f *staticDepositUtxoSwapCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	transferCommitMsg, err := f.transferCoord.BuildCommitPayload(ctx, results)
	if err != nil {
		return nil, fmt.Errorf("failed to build utxo swap transfer commit: %w", err)
	}
	transferCommit, ok := transferCommitMsg.(*pbinternal.SendTransferCommitRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected transfer commit payload type %T", transferCommitMsg)
	}

	allShares, _, err := collectSignatureShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
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
	spendTxSighash, _, err := GetTxSigningInfo(ctx, targetUtxo.inner, f.req.GetSpendTxSigningJob().GetRawTx())
	if err != nil {
		return nil, fmt.Errorf("failed to get spend tx sighash: %w", err)
	}
	userNonce := frost.SigningCommitment{}
	if err := userNonce.UnmarshalProto(f.req.GetSpendTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, fmt.Errorf("failed to parse spend tx nonce commitment: %w", err)
	}

	jobID := staticDepositSwapJobID(f.req.GetOnChainUtxo().GetTxid(), f.req.GetOnChainUtxo().GetVout())
	job := &helper.SigningJob{
		JobID:             jobID,
		SigningKeyshareID: signingKeyshare.ID,
		Message:           spendTxSighash,
		VerifyingKey:      &verifyingKey,
		UserCommitment:    &userNonce,
	}

	operatorIDs := make([]string, 0, len(f.spendCommitmentsParsed))
	for id := range f.spendCommitmentsParsed {
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
		return nil, fmt.Errorf("no round-2 shares collected for spend tx job %s", jobID)
	}
	signingResults, err := helper.BuildSigningResults(
		f.config, selection,
		[]*helper.SigningJob{job}, keyPackages,
		[]map[string]frost.SigningCommitment{f.spendCommitmentsParsed},
		map[uuid.UUID]map[string][]byte{jobID: round2},
	)
	if err != nil {
		return nil, fmt.Errorf("unable to build spend tx signing result: %w", err)
	}
	if len(signingResults) == 0 {
		return nil, fmt.Errorf("no signing result produced for spend tx job %s", jobID)
	}
	signingResultProto := signingResults[0].MarshalProto()

	// Apply on the coordinator's DB: store the signing result + mark COMPLETED.
	// The coordinator's own flow.Prepare created this row CREATED earlier in
	// this same request transaction, so the row is exclusively held by this tx —
	// no extra ForUpdate lock is needed.
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

	f.response = &pbssp.InitiateStaticDepositUtxoSwapResponse{
		SpendTxSigningResult: signingResultProto,
		Transfer:             f.transferCoord.response.GetTransfer(),
		DepositAddress: &pbspark.DepositAddressQueryResult{
			DepositAddress:       depositAddress.Address,
			UserSigningPublicKey: depositAddress.OwnerSigningPubkey.Serialize(),
			VerifyingPublicKey:   verifyingKey.Serialize(),
			LeafId:               new(depositAddress.NodeID.String()),
		},
	}
	return &pbinternal.StaticDepositUtxoSwapCommitRequest{
		OnChainUtxo:    f.req.GetOnChainUtxo(),
		TransferCommit: transferCommit,
	}, nil
}

// RollbackPayload carries only the utxo: each participant derives the transfer
// to roll back from its own swap row rather than trusting a coordinator-named
// transfer id (see StaticDepositUtxoSwapRollbackRequest's doc).
func (f *staticDepositUtxoSwapCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.StaticDepositUtxoSwapRollbackRequest{
		OnChainUtxo: f.req.GetOnChainUtxo(),
	}
}

// ---------------------------------------------------------------------------
// Coordinator entrypoint
// ---------------------------------------------------------------------------

// initiateStaticDepositUtxoSwapConsensus is the knob-gated 2PC entrypoint for the fixed-amount
// claim. The caller has already done structural checks, auth, and kill-switch; this runs the
// coordinator-only validation + idempotency short-circuit, fast-fails the user signature before
// any cross-operator work, collects spend-tx FROST round-1 commitments, then drives the engine.
func (o *StaticDepositHandler) initiateStaticDepositUtxoSwapConsensus(ctx context.Context, config *so.Config, req *pbssp.InitiateStaticDepositUtxoSwapRequest) (*pbssp.InitiateStaticDepositUtxoSwapResponse, error) {
	if req.GetTransfer().GetTransferPackage() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_package is required"))
	}
	if req.GetSpendTxSigningJob() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("spend_tx_signing_job is required"))
	}
	// Reject a missing/malformed spend-tx nonce commitment before any
	// cross-operator work: round-1 collection below consumes a persisted FROST
	// nonce on every selected operator, and without this check a malformed
	// request would burn that cluster-wide work only to be rejected by every
	// participant's Prepare (createFixedSwap re-validates this same field).
	if req.GetSpendTxSigningJob().GetSigningNonceCommitment() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("spend_tx_signing_job.signing_nonce_commitment is required"))
	}
	if err := (&frost.SigningCommitment{}).UnmarshalProto(req.GetSpendTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid spend tx signing nonce commitment: %w", err))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	schemaNetwork, err := btcnetwork.FromProtoNetwork(req.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, err
	}
	confirmationThreshold := req.ConfirmationThreshold
	if confirmationThreshold != nil && *confirmationThreshold == 0 {
		return nil, sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("confirmation_threshold must be at least 1"))
	}
	threshold := resolveConfirmationThreshold(confirmationThreshold, config, schemaNetwork)
	targetUtxo, err := VerifiedTargetUtxoFromRequest(ctx, config, db, schemaNetwork, req.GetOnChainUtxo(), &threshold)
	if err != nil {
		return nil, err
	}

	// Idempotency/duplicate short-circuit, matching legacy: fixed swaps have no
	// re-sign path (unlike refunds), so an already-registered swap is a plain
	// AlreadyExists.
	existingSwap, err := staticdeposit.GetRegisteredUtxoSwapForUtxo(ctx, db, targetUtxo.inner)
	if err != nil {
		return nil, fmt.Errorf("unable to check if utxo swap is already registered: %w", err)
	}
	if existingSwap != nil {
		return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("utxo swap is already registered"))
	}

	// Fast-fail the user signature (and the cheap address checks it depends on)
	// before any cross-operator work: round-1 collection consumes a persisted
	// FROST nonce on every selected operator and Execute fans Prepare out to all
	// of them. Additive only — every participant re-verifies in Prepare
	// (createFixedSwap); a coordinator-side check is never a substitute.
	depositAddress, err := targetUtxo.inner.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo deposit address: %w", err)
	}
	if !depositAddress.IsStatic {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("unable to claim a deposit to a non-static address"))
	}
	receiverIdentityPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetReceiverIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid receiver identity public key: %w", err))
	}
	if !depositAddress.OwnerIdentityPubkey.Equals(receiverIdentityPubKey) {
		return nil, fmt.Errorf("transfer is not to the recipient of the deposit")
	}
	if err := validateStaticDepositSpendTxSpendsTargetUtxo(targetUtxo, req.GetSpendTxSigningJob().GetRawTx()); err != nil {
		return nil, err
	}
	leafRefundMap := make(map[string][]byte)
	for _, leaf := range req.GetTransfer().GetTransferPackage().GetLeavesToSend() {
		leafRefundMap[leaf.GetLeafId()] = leaf.GetRawTx()
	}
	// Non-locking read: this load only sums immutable leaf values for the
	// signature fast-fail, and FOR UPDATE locks taken here would be held across
	// the round-1 collection RPCs and the entire engine Execute fan-out,
	// blocking concurrent flows touching the same leaves. The engine-driven
	// Prepare (createTransferV3) takes the authoritative locks + availability
	// checks before any state change.
	leaves, _, err := loadLeaves(ctx, db, leafRefundMap, false)
	if err != nil {
		return nil, fmt.Errorf("unable to load leaves: %w", err)
	}
	if len(leaves) == 0 {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no leaves found"))
	}
	totalAmount := getTotalTransferValue(leaves)
	if err := validateUserSignature(receiverIdentityPubKey, req.GetUserSignature(), req.GetSspSignature(), pbspark.UtxoSwapRequestType_Fixed, schemaNetwork, targetUtxo.Hash().String(), targetUtxo.Vout(), totalAmount, req.GetHashVariant()); err != nil {
		return nil, fmt.Errorf("user signature validation failed: %w", err)
	}

	reqInternal := &pbinternal.InitiateStaticDepositUtxoSwapRequest{
		OnChainUtxo:           req.GetOnChainUtxo(),
		SspSignature:          req.GetSspSignature(),
		UserSignature:         req.GetUserSignature(),
		Transfer:              req.GetTransfer(),
		SpendTxSigningJob:     req.GetSpendTxSigningJob(),
		HashVariant:           req.GetHashVariant(),
		ConfirmationThreshold: req.ConfirmationThreshold,
	}

	// Collect spend-tx FROST round-1 commitments server-side so round-2 can run
	// inside the engine's Prepare while the public RPC stays a single call.
	round1, err := helper.GetSigningCommitments(ctx, config, 1, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to collect round-1 signing commitments: %w", err)
	}

	flow, err := buildStaticDepositUtxoSwapCoordinatorFlow(ctx, config, reqInternal, targetUtxo, round1)
	if err != nil {
		return nil, fmt.Errorf("unable to build coordinator flow: %w", err)
	}
	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	if _, err := engine.Execute(ctx, pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_SWAP, &selection, flow); err != nil {
		return nil, fmt.Errorf("consensus static deposit utxo swap failed: %w", err)
	}
	if flow.response == nil {
		return nil, fmt.Errorf("static deposit utxo swap consensus completed without building a response")
	}
	return flow.response, nil
}

// buildStaticDepositUtxoSwapCoordinatorFlow parses the coordinator-collected
// round-1 commitments and pre-builds the delegated send-transfer coordinator
// flow (leaf preload + aggregation-job helpers) from the converted v3 request.
func buildStaticDepositUtxoSwapCoordinatorFlow(ctx context.Context, config *so.Config, req *pbinternal.InitiateStaticDepositUtxoSwapRequest, targetUtxo *VerifiedTargetUtxo, round1 map[string][]frost.SigningCommitment) (*staticDepositUtxoSwapCoordinatorFlow, error) {
	spendCommitments := make(map[string]*pbcommon.SigningCommitment, len(round1))
	spendCommitmentsParsed := make(map[string]frost.SigningCommitment, len(round1))
	for opID, commitments := range round1 {
		// GetSigningCommitments is called with count=1, so exactly one
		// commitment per operator is expected; guard against a future count
		// change silently desyncing round-1 from round-2 aggregation.
		if len(commitments) != 1 {
			return nil, fmt.Errorf("expected exactly 1 round-1 commitment for operator %s, got %d", opID, len(commitments))
		}
		spendCommitments[opID] = commitments[0].MarshalProto()
		spendCommitmentsParsed[opID] = commitments[0]
	}

	handler := NewStaticDepositUtxoSwapFlowHandler(config)
	// The delegated coordinator flow is built directly on the swap's typed
	// transfer handler (TransferTypeUtxoSwap rows, TransferPartnerTypeDeposit
	// attribution, no direct-refund requirement) so it carries the swap's
	// transfer semantics from construction.
	transferReq := convertV2ToV3SendTransferRequest(req.GetTransfer())
	parsedTransfer, err := parseSendTransferRequest(transferReq)
	if err != nil {
		return nil, fmt.Errorf("unable to parse swap transfer request: %w", err)
	}
	transferCoord, err := buildSendTransferCoordinatorFlow(ctx, config, transferReq, parsedTransfer, "", handler.transfer)
	if err != nil {
		return nil, fmt.Errorf("unable to build transfer coordinator flow: %w", err)
	}

	return &staticDepositUtxoSwapCoordinatorFlow{
		StaticDepositUtxoSwapFlowHandler: handler,
		req:                              req,
		targetUtxo:                       targetUtxo,
		transferCoord:                    transferCoord,
		spendCommitments:                 spendCommitments,
		spendCommitmentsParsed:           spendCommitmentsParsed,
	}, nil
}
