//go:build lightspark

package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// claimInstantStaticDepositCoordinatorFlow lives in a lightspark-tagged file (unlike the
// participant handler) because the coordinator entrypoint + response are SSP-internal API
// (proto/spark_ssp_internal), excluded from OSS builds. OSS participants still dispatch the
// op type via the untagged flow handler.

type claimInstantStaticDepositCoordinatorFlow struct {
	*ClaimInstantStaticDepositFlowHandler

	req *pbinternal.ClaimInstantStaticDepositUtxoSwapRequest
	// transferCoord aggregates the optional secondary transfer's leaf
	// signatures, applies the transfer commit on the coordinator, and builds the
	// transfer response. nil when the reservation has no secondary credit.
	transferCoord *sendTransferCoordinatorFlow
	// Spend-tx FROST round-1 commitments the coordinator collected
	// (GetSigningCommitments) before Execute, keyed by operator id.
	spendCommitments       map[string]*pbcommon.SigningCommitment
	spendCommitmentsParsed map[string]frost.SigningCommitment

	// response is populated in BuildCommitPayload for the public handler to return.
	response *pbssp.ClaimInstantStaticDepositUtxoSwapResponse
}

var _ consensus.CoordinatorFlow = (*claimInstantStaticDepositCoordinatorFlow)(nil)

func (f *claimInstantStaticDepositCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest:           f.req,
		SpendTxSigningCommitments: f.spendCommitments,
	}
}

// BuildCommitPayload first delegates the optional secondary transfer to the
// send-transfer coordinator flow (leaf-signature aggregation, coordinator-local
// transfer commit, transfer response), then aggregates the spend-tx signature
// from the same prepare results, stores it on the coordinator's swap row, marks
// the swap COMPLETED, and builds the RPC response. The transfer delegation MUST
// run first: CompleteUtxoSwap requires every linked transfer to be sent.
func (f *claimInstantStaticDepositCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	var transferCommit *pbinternal.SendTransferCommitRequest
	if f.transferCoord != nil {
		transferCommitMsg, err := f.transferCoord.BuildCommitPayload(ctx, results)
		if err != nil {
			return nil, fmt.Errorf("failed to build claim secondary transfer commit: %w", err)
		}
		var ok bool
		transferCommit, ok = transferCommitMsg.(*pbinternal.SendTransferCommitRequest)
		if !ok {
			return nil, fmt.Errorf("unexpected transfer commit payload type %T", transferCommitMsg)
		}
	}

	allShares, _, err := collectSignatureShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	// The coordinator's own flow.Prepare linked the utxo edge onto this row
	// earlier in this same request transaction, so both rows are exclusively
	// held by this tx — no extra ForUpdate is needed.
	transferID, err := uuid.Parse(f.req.GetTransferId())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}
	swap, err := loadInstantSwapForClaim(ctx, db, transferID, false /* forUpdate */, st.UtxoSwapStatusCreated)
	if err != nil {
		return nil, err
	}
	targetUtxo, err := swap.QueryUtxo().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load utxo linked by prepare for swap %s: %w", swap.ID, err)
	}
	depositAddress, err := targetUtxo.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo deposit address: %w", err)
	}
	signingKeyshare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}
	verifyingKey := signingKeyshare.PublicKey.Add(depositAddress.OwnerSigningPubkey)
	spendTxSighash, _, err := GetTxSigningInfo(ctx, targetUtxo, f.req.GetSpendTxSigningJob().GetRawTx())
	if err != nil {
		return nil, fmt.Errorf("failed to get spend tx sighash: %w", err)
	}
	userNonce := frost.SigningCommitment{}
	if err := userNonce.UnmarshalProto(f.req.GetSpendTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, fmt.Errorf("failed to parse spend tx nonce commitment: %w", err)
	}

	jobID := claimInstantSpendTxJobID(f.req.GetOnChainUtxo().GetTxid(), f.req.GetOnChainUtxo().GetVout())
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

	var transferProto *pbspark.Transfer
	if f.transferCoord != nil {
		transferProto = f.transferCoord.response.GetTransfer()
	}
	f.response = &pbssp.ClaimInstantStaticDepositUtxoSwapResponse{
		Transfer:             transferProto,
		SpendTxSigningResult: signingResultProto,
		DepositAddress: &pbspark.DepositAddressQueryResult{
			DepositAddress:       depositAddress.Address,
			UserSigningPublicKey: depositAddress.OwnerSigningPubkey.Serialize(),
			VerifyingPublicKey:   verifyingKey.Serialize(),
			LeafId:               new(depositAddress.NodeID.String()),
		},
	}
	return &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{
		TransferId:     f.req.GetTransferId(),
		TransferCommit: transferCommit,
	}, nil
}

// RollbackPayload carries the primary transfer id for observability only —
// the participant Rollback is a deliberate no-op and shape-validates the
// payload without acting on it (see the handler Rollback doc).
func (f *claimInstantStaticDepositCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.ClaimInstantStaticDepositUtxoSwapRollbackRequest{
		TransferId: f.req.GetTransferId(),
	}
}

// ---------------------------------------------------------------------------
// Coordinator entrypoint
// ---------------------------------------------------------------------------

// claimInstantStaticDepositUtxoSwapConsensus is the knob-gated 2PC entrypoint for phase two
// of the instant static deposit claim. The caller (ClaimInstantStaticDepositUtxoSwap) has
// already run the legacy validations — instant-enabled knob, the ForUpdate load of the
// CREATED reservation, session-identity auth, kill switch, and the secondary-transfer
// cross-field checks — so this validates the spend-tx job, collects FROST round-1
// commitments (keeping the public RPC a single call), then drives the engine.
func (o *StaticDepositHandler) claimInstantStaticDepositUtxoSwapConsensus(ctx context.Context, config *so.Config, req *pbssp.ClaimInstantStaticDepositUtxoSwapRequest, utxoSwap *ent.UtxoSwap) (*pbssp.ClaimInstantStaticDepositUtxoSwapResponse, error) {
	// Reject a missing/malformed spend-tx job before any cross-operator work:
	// round-1 collection consumes a persisted FROST nonce on every selected
	// operator, and every participant's Prepare re-validates this same field.
	if req.GetSpendTxSigningJob() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("spend_tx_signing_job is required"))
	}
	if req.GetSpendTxSigningJob().GetSigningNonceCommitment() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("spend_tx_signing_job.signing_nonce_commitment is required"))
	}
	if err := (&frost.SigningCommitment{}).UnmarshalProto(req.GetSpendTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid spend tx signing nonce commitment: %w", err))
	}

	// Fast-fail the UTXO confirmation and spend-tx checks against this SO's own
	// state before any cross-operator work, mirroring the fixed-swap consensus
	// entrypoint: round-1 collection consumes a persisted FROST nonce on every
	// selected operator and Execute fans Prepare out to all of them. Additive
	// only — every participant re-runs these same checks in Prepare
	// (linkUtxoToReservedSwap); a coordinator-side check is never a substitute.
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	network, err := btcnetwork.FromProtoNetwork(req.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, fmt.Errorf("unable to parse network: %w", err)
	}
	targetUtxo, err := VerifiedTargetUtxoFromRequestWithThreshold(ctx, db, network, req.GetOnChainUtxo(), 1)
	if err != nil {
		return nil, fmt.Errorf("failed to verify on-chain utxo: %w", err)
	}
	if targetUtxo == nil {
		return nil, sparkerrors.FailedPreconditionInsufficientConfirmations(fmt.Errorf("on-chain utxo not found or not confirmed"))
	}
	if targetUtxo.inner.Amount != utxoSwap.UtxoValueSats {
		return nil, fmt.Errorf("utxo amount %d does not match swap utxo_value_sats %d", targetUtxo.inner.Amount, utxoSwap.UtxoValueSats)
	}
	if err := validateStaticDepositSpendTxSpendsTargetUtxo(targetUtxo, req.GetSpendTxSigningJob().GetRawTx()); err != nil {
		return nil, err
	}

	reqInternal := &pbinternal.ClaimInstantStaticDepositUtxoSwapRequest{
		OnChainUtxo:       req.GetOnChainUtxo(),
		Transfer:          req.GetTransfer(),
		SpendTxSigningJob: req.GetSpendTxSigningJob(),
		// Every SO locates its row by the primary transfer id from the
		// coordinator's ForUpdate-locked row — exactly what the legacy
		// SaveUtxoForInstantStaticDeposit fanout carried.
		TransferId: utxoSwap.RequestedTransferID.String(),
	}

	// Collect spend-tx FROST round-1 commitments server-side so round-2 can run
	// inside the engine's Prepare while the public RPC stays a single call.
	round1, err := helper.GetSigningCommitments(ctx, config, 1, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to collect round-1 signing commitments: %w", err)
	}

	flow, err := buildClaimInstantStaticDepositCoordinatorFlow(ctx, config, reqInternal, round1)
	if err != nil {
		return nil, fmt.Errorf("unable to build coordinator flow: %w", err)
	}
	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	if _, err := engine.Execute(ctx, pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CLAIM_INSTANT_STATIC_DEPOSIT_UTXO_SWAP, &selection, flow); err != nil {
		return nil, fmt.Errorf("consensus claim instant static deposit utxo swap failed: %w", err)
	}
	if flow.response == nil {
		return nil, fmt.Errorf("claim instant static deposit consensus completed without building a response")
	}
	return flow.response, nil
}

// buildClaimInstantStaticDepositCoordinatorFlow parses the coordinator-collected
// round-1 commitments and pre-builds the delegated send-transfer coordinator
// flow for the optional secondary transfer.
func buildClaimInstantStaticDepositCoordinatorFlow(ctx context.Context, config *so.Config, req *pbinternal.ClaimInstantStaticDepositUtxoSwapRequest, round1 map[string][]frost.SigningCommitment) (*claimInstantStaticDepositCoordinatorFlow, error) {
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

	handler := NewClaimInstantStaticDepositFlowHandler(config)
	var transferCoord *sendTransferCoordinatorFlow
	if req.GetTransfer() != nil {
		var err error
		// Built on the claim's typed transfer handler so the secondary carries
		// the swap's transfer semantics from construction.
		transferCoord, err = buildSendTransferCoordinatorFlow(ctx, config, convertV2ToV3SendTransferRequest(req.GetTransfer()), "", handler.transfer)
		if err != nil {
			return nil, fmt.Errorf("unable to build secondary transfer coordinator flow: %w", err)
		}
	}

	return &claimInstantStaticDepositCoordinatorFlow{
		ClaimInstantStaticDepositFlowHandler: handler,
		req:                                  req,
		transferCoord:                        transferCoord,
		spendCommitments:                     spendCommitments,
		spendCommitmentsParsed:               spendCommitmentsParsed,
	}, nil
}
