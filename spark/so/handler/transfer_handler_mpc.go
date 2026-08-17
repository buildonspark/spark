package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
)

// StartTransferMpc is the multiparty (user-side MPC) sibling of StartTransferV3: a dedicated front for group-signed
// submissions, so the single-party fronts stay free of MPC branches. Like startTransferV3Consensus, the entry point
// is deliberately thin: the authoritative multiparty verification — group signature, authorized facts, sealed
// sub-shares, tweak binding — runs inside the consensus flow's Prepare on every operator, this one included.
//
// Gate order, pinned by tests. The interceptor chain runs first — the method's AuthSession rpcpolicy means an
// unauthenticated call is rejected there and never reaches this handler, exactly as for the deprecated methods in
// spark_server.go. Within the handler:
//
//  1. Knob (KnobMpcTransferEnabled, default off): disabled means the handler behaves as if the RPC were absent —
//     Unimplemented before any request inspection beyond the interceptor chain's session authentication.
//  2. Envelope: parse the sender identity key, then enforce session identity and the kill switch, so nothing below
//     runs for an unauthenticated caller.
//  3. Leaf limit: the same per-transfer cap every single-party front applies (KnobSoTransferLimit, falling back to
//     MaxLeavesToSend), checked on the raw list length before any parsing work.
//  4. Structure: ParseMpcSubmission; malformed submissions are InvalidArgument.
//  5. Receiver shape: the wire carries a per-leaf receiver map so multi-receiver MPC sends are a later validation
//     relaxation, but the MVP accepts exactly one distinct receiver (FailedPrecondition).
//  6. Authorization signature: verifyMpcAuthorizationSignature — pure crypto, before any leaf state is read
//     (the flow builder below preloads leaf rows), so a caller without the group signature cannot probe
//     state-dependent errors.
//  7. Execution: the consensus engine fans the submission out under
//     CONSENSUS_OPERATION_TYPE_MPC_SEND_TRANSFER; the full authorization — signature and facts against each
//     operator's own state — runs in MpcSendTransferFlowHandler.Prepare on every operator.
func (h *TransferHandler) StartTransferMpc(ctx context.Context, req *pb.StartTransferMpcRequest) (*pb.StartTransferResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.StartTransferMpc")
	defer span.End()

	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobMpcTransferEnabled, 0) == 0 {
		return nil, sparkerrors.UnimplementedMethodDisabled(fmt.Errorf("multiparty transfer send is not enabled"))
	}

	senderIDPub, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner identity public key: %w", err))
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, senderIDPub); err != nil {
		return nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, senderIDPub); err != nil {
		return nil, err
	}

	if leafCount, limit := len(req.GetMpcTransferPackage().GetLeaves()), transferLeafLimit(ctx); leafCount > limit {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("too many leaves to send: %d (max: %d)", leafCount, limit))
	}

	submission, err := transferpkg.ParseMpcSubmission(req)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid mpc transfer submission: %w", err))
	}

	if receivers := submission.Receivers(); len(receivers) != 1 {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("multiparty transfers require exactly one distinct receiver, got %d", len(receivers)))
	}

	if err := verifyMpcAuthorizationSignature(submission); err != nil {
		return nil, err
	}

	flow, err := buildMpcSendTransferCoordinatorFlow(ctx, req, submission, NewMpcSendTransferFlowHandler(h.config))
	if err != nil {
		return nil, err
	}
	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	if _, err := engine.Execute(ctx,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_MPC_SEND_TRANSFER,
		&selection,
		flow,
	); err != nil {
		// Failing the request is correct whether the engine rolled back (the
		// transfer never committed) or a post-commit gossip dispatch failed
		// (the reconciler drives participants forward from the persisted
		// FlowExecution row); retries are one-shot per transfer id either way.
		return nil, fmt.Errorf("consensus mpc send transfer failed: %w", err)
	}
	if flow.response == nil {
		return nil, fmt.Errorf("internal: consensus mpc send transfer for %s succeeded but produced no response", req.GetTransferId())
	}
	return flow.response, nil
}
