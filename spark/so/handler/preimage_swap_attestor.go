package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// A manifest is signed only by its senders, and on a receive the sender is the SSP — so nothing in
// it attests that anyone outside the SSP agreed to these terms. The attestor need not be a payee:
// on a delegated receive it holds the preimage share and receives nothing.
func verifyAttestorSignature(ctx context.Context, req *pbspark.InitiatePreimageSwapV4Request, attestor keys.Public) (retErr error) {
	signature := req.GetAttestorSignature()
	manifest := req.GetTransferV3Request().GetTransferManifest()

	// Set once up front so no refusal path can forget to count, and reassigned by the branch whose
	// class differs from the gate's own.
	kind := attestorSignatureRefusalKind(req.GetTransferV3Request(), signature)
	defer func() {
		if retErr != nil {
			recordManifestRefusal(ctx, manifestEndpointInitiatePreimageSwapV4, kind)
		}
	}()

	// The digest is built under QuoteReasonReceive, so the reason is a precondition of this gate
	// rather than a branch inside it: another flow's attestation carries its own reason, and
	// checking it against this one is the cross-flow replay the reason component exists to stop.
	if req.GetReason() != pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE {
		kind = manifestRefusalReason
		return sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("attestor_signature is defined only for REASON_RECEIVE, got %s", req.GetReason()))
	}
	if len(signature) == 0 {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("attestor_signature is required"))
	}
	// A signature over nothing covers nothing, and whoever supplied it believes something was bound.
	if manifest == nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("attestor_signature set without a transfer_manifest"))
	}
	// Checked here so an unknown network counts as a network refusal: the digest's own guard would
	// otherwise attribute the same manifest to the signature class.
	if _, err := btcnetwork.FromProtoNetwork(manifest.GetNetwork()); err != nil {
		kind = manifestRefusalNetwork
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("manifest network: %w", err))
	}

	manifestHash, err := common.HashTransferManifest(manifest)
	if err != nil {
		kind = manifestRefusalSignature
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer manifest is not hashable: %w", err))
	}
	target, err := common.ReceiveAttestorTarget(req.GetPaymentHash())
	if err != nil {
		kind = manifestRefusalSignature
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("attestation target: %w", err))
	}
	digest, err := common.QuoteEnvelopeDigest(
		manifest.GetNetwork(), manifestHash,
		common.QuoteReasonReceive, common.QuoteRoleAttestor, target)
	if err != nil {
		kind = manifestRefusalSignature
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("quote envelope digest: %w", err))
	}
	if err := common.VerifyECDSASignature(attestor, signature, digest); err != nil {
		return sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("attestor %x signature is invalid: %w", attestor.Serialize(), err))
	}
	return nil
}

// v4LeafDestinations keeps a v4-specific label out of the generic perLeafDestinations. Its kind is
// asserted, not classified: that helper's only refusal is a duplicated leaf id.
func v4LeafDestinations(ctx context.Context, receiverByLeafID map[string]keys.Public) (leafDestinations, error) {
	destinations, err := perLeafDestinations(receiverByLeafID)
	if err != nil {
		recordManifestRefusal(ctx, manifestEndpointInitiatePreimageSwapV4, manifestRefusalDuplicate)
		return leafDestinations{}, err
	}
	return destinations, nil
}
