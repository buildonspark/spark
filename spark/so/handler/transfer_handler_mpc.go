package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/authz"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
)

// StartTransferMpc is the multiparty (user-side MPC) sibling of StartTransferV3: a dedicated front for group-signed
// submissions, so the single-party fronts stay free of MPC branches. It currently accepts a submission only as far as
// the gates below, then fails closed with Unimplemented — the flow that executes through the consensus engine
// (SP-3506) lands behind the same knob. Nothing here can commit a transfer.
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
//  6. Authorization: verifyMpcAuthorization — the group signature over the recomputed whole-submission payload,
//     then every authorized fact against this operator's own state. This runs on the coordinator here; the
//     consensus flow (SP-3506) runs the same verification on every operator during prepare.
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

	if err := verifyMpcAuthorization(ctx, submission); err != nil {
		return nil, err
	}

	// Fail-closed tail: the submission is verified as far as the coordinator can take it, but the consensus flow
	// for CONSENSUS_OPERATION_TYPE_MPC_SEND_TRANSFER is not implemented yet. Refusing here (rather than not
	// registering the RPC) gives clients a stable target whose acceptance criteria tighten as the flow lands. The
	// FEATURE_NOT_IMPLEMENTED reason distinguishes this from the knob-off gate's METHOD_DISABLED, so a client can
	// tell "accepted as far as the code goes" from "MPC is off on this cluster" without parsing message strings.
	return nil, sparkerrors.UnimplementedFeatureIncomplete(fmt.Errorf("multiparty transfer send is not yet implemented for transfer %s", submission.TransferID()))
}
