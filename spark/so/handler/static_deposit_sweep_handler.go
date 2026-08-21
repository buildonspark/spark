//go:build lightspark

package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
)

// SignStaticDepositSweepTx co-signs every input of a transaction the SSP has
// built to sweep static deposit UTXOs it has already bought into its own wallet.
//
// Unlike per-claim spend signing, this signs whatever transaction the owning SSP
// presents, as many times as it asks. The eligibility check in resolveSweepInputs
// is the whole of what holds that line, and it runs on every operator: the signing
// goes through the 2PC engine, so a share is only ever produced by an SO that
// independently agrees the UTXO is the caller's to spend.
//
// This coordinator pass runs that check first so a caller holding stale inputs
// gets them named back and can rebuild, rather than burning a cluster-wide round
// trip on a sweep that was always going to abort.
func (h *SspRequestHandler) SignStaticDepositSweepTx(ctx context.Context, req *pbssp.SignStaticDepositSweepTxRequest) (*pbssp.SignStaticDepositSweepTxResponse, error) {
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}
	network, err := btcnetwork.FromProtoNetwork(req.GetNetwork())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentNetworkNotSupported(fmt.Errorf("invalid network: %w", err))
	}
	if len(req.GetInputs()) == 0 {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("inputs are required"))
	}

	// The caller's identity is the sole authorization for spending someone else's
	// deposit address, so it comes from the session and not from anything the
	// request can assert.
	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	sspIdentityPubKey := session.IdentityPublicKey()
	if err := authz.EnforceWalletNotKillSwitched(ctx, sspIdentityPubKey); err != nil {
		return nil, err
	}

	inputs := make([]*pbinternal.StaticDepositSweepInput, 0, len(req.GetInputs()))
	for i, in := range req.GetInputs() {
		if in == nil {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("inputs[%d] is required", i))
		}
		inputs = append(inputs, &pbinternal.StaticDepositSweepInput{
			OnChainUtxo:           in.GetOnChainUtxo(),
			Vin:                   in.GetVin(),
			UserSigningCommitment: in.GetUserSigningCommitment(),
		})
	}

	prepared, refusals, err := PrepareSweep(ctx, network, req.GetRawTx(), inputs, sspIdentityPubKey, req.GetSspSignature())
	if err != nil {
		return nil, err
	}
	// A partially signed batch is useless, so refuse the whole set at once and let
	// the caller rebuild without the bad inputs.
	if len(refusals) > 0 {
		return &pbssp.SignStaticDepositSweepTxResponse{
			Result: &pbssp.SignStaticDepositSweepTxResponse_Ineligible{
				Ineligible: &pbssp.IneligibleSweepInputs{Inputs: ineligibleSweepInputProtos(refusals)},
			},
		}, nil
	}

	// One round-1 commitment per input, collected before Execute so round-2 can run
	// inside the engine's Prepare and this stays a single RPC.
	round1, err := helper.GetSigningCommitments(ctx, h.config, uint32(len(prepared.inputs)), 1)
	if err != nil {
		return nil, fmt.Errorf("failed to collect round-1 signing commitments: %w", err)
	}
	flow, err := buildStaticDepositSweepCoordinatorFlow(h.config, req.GetNetwork(), req.GetRawTx(), sspIdentityPubKey, req.GetSspSignature(), prepared, round1)
	if err != nil {
		return nil, fmt.Errorf("unable to build coordinator flow: %w", err)
	}
	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	if _, err := engine.Execute(ctx, pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_SWEEP, &selection, flow); err != nil {
		return nil, fmt.Errorf("consensus static deposit sweep failed: %w", err)
	}
	if len(flow.results) != len(prepared.inputs) {
		return nil, sparkerrors.InternalDataInconsistency(
			fmt.Errorf("static deposit sweep consensus produced %d of %d signing results", len(flow.results), len(prepared.inputs)),
		)
	}

	results := make([]*pbssp.SweepInputSigningResult, 0, len(flow.results))
	for _, result := range flow.results {
		results = append(results, &pbssp.SweepInputSigningResult{
			Vin:           result.Vin,
			SigningResult: result.SigningResult,
			VerifyingKey:  result.VerifyingKey,
		})
	}
	return &pbssp.SignStaticDepositSweepTxResponse{
		Result: &pbssp.SignStaticDepositSweepTxResponse_Signed{
			Signed: &pbssp.SweptInputs{Results: results},
		},
	}, nil
}

func ineligibleSweepInputProtos(refusals []sweepRefusal) []*pbssp.IneligibleSweepInput {
	out := make([]*pbssp.IneligibleSweepInput, 0, len(refusals))
	for _, refusal := range refusals {
		out = append(out, &pbssp.IneligibleSweepInput{
			OnChainUtxo: refusal.utxo,
			Reason:      sweepIneligibleReasonProto(refusal.reason),
		})
	}
	return out
}

func sweepIneligibleReasonProto(reason sweepIneligibleReason) pbssp.SweepIneligibleReason {
	switch reason {
	case sweepUnknownUtxo:
		return pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_UNKNOWN_UTXO
	case sweepNoSwap:
		return pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_NO_SWAP
	case sweepSwapNotCompleted:
		return pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_SWAP_NOT_COMPLETED
	case sweepNotOwnedByCaller:
		return pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_NOT_OWNED_BY_CALLER
	case sweepRefundSwap:
		return pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_REFUND_SWAP
	default:
		return pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_UNSPECIFIED
	}
}
