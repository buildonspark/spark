package handler

import (
	"context"
	"errors"

	pb "github.com/lightsparkdev/spark/proto/spark"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// manifestRefusalKind buckets a refusal coarsely enough to alert on while keeping a cryptographic
// failure distinct from an arithmetic one.
type manifestRefusalKind string

const (
	manifestRefusalEdgeCover          manifestRefusalKind = "edge_cover"
	manifestRefusalAmountOverflow     manifestRefusalKind = "amount_overflow"
	manifestRefusalDuplicate          manifestRefusalKind = "duplicate"
	manifestRefusalExpiry             manifestRefusalKind = "expiry"
	manifestRefusalNetwork            manifestRefusalKind = "network"
	manifestRefusalSizeCap            manifestRefusalKind = "size_cap"
	manifestRefusalSignature          manifestRefusalKind = "signature"
	manifestRefusalStraySignature     manifestRefusalKind = "stray_signature"
	manifestRefusalSenderKey          manifestRefusalKind = "sender_key"
	manifestRefusalReceiverKey        manifestRefusalKind = "receiver_key"
	manifestRefusalLeafOwner          manifestRefusalKind = "leaf_owner"
	manifestRefusalTransferID         manifestRefusalKind = "transfer_id"
	manifestRefusalUnknownLeaf        manifestRefusalKind = "unknown_leaf"
	manifestRefusalMissingManifest    manifestRefusalKind = "missing_manifest"
	manifestRefusalAttestorSignature  manifestRefusalKind = "attestor_signature"
	manifestRefusalMissingAttestorSig manifestRefusalKind = "missing_attestor_signature"
	manifestRefusalReason             manifestRefusalKind = "reason"
	manifestRefusalOther              manifestRefusalKind = "other"
)

// allManifestRefusalKinds ties the const block above to the test that pins each scraped label, so a
// new kind fails the suite until its label string is stated.
var allManifestRefusalKinds = []manifestRefusalKind{
	manifestRefusalEdgeCover,
	manifestRefusalAmountOverflow,
	manifestRefusalDuplicate,
	manifestRefusalExpiry,
	manifestRefusalNetwork,
	manifestRefusalSizeCap,
	manifestRefusalSignature,
	manifestRefusalStraySignature,
	manifestRefusalSenderKey,
	manifestRefusalReceiverKey,
	manifestRefusalLeafOwner,
	manifestRefusalTransferID,
	manifestRefusalUnknownLeaf,
	manifestRefusalMissingManifest,
	manifestRefusalAttestorSignature,
	manifestRefusalMissingAttestorSig,
	manifestRefusalReason,
	manifestRefusalOther,
}

// manifestBindEndpoint names the endpoint whose contract the refused manifest was bound under;
// the shared gate cannot infer it. Not the `flow` label key, which carries a different vocabulary.
type manifestBindEndpoint string

const (
	manifestEndpointStartTransferV3        manifestBindEndpoint = "start_transfer_v3"
	manifestEndpointStaticDeposit          manifestBindEndpoint = "static_deposit"
	manifestEndpointCounterSwapV3          manifestBindEndpoint = "counter_swap_v3"
	manifestEndpointPrimarySwapV3          manifestBindEndpoint = "primary_swap_v3"
	manifestEndpointInitiatePreimageSwapV4 manifestBindEndpoint = "initiate_preimage_swap_v4"
	manifestEndpointOther                  manifestBindEndpoint = "other"
)

var manifestRefusalKindsBySentinel = []struct {
	sentinel error
	kind     manifestRefusalKind
}{
	{transferpkg.ErrManifestAmountMismatch, manifestRefusalEdgeCover},
	{transferpkg.ErrManifestEdgeNotRealized, manifestRefusalEdgeCover},
	{transferpkg.ErrManifestLeafNotRouted, manifestRefusalEdgeCover},
	{transferpkg.ErrManifestUnlistedTransfer, manifestRefusalEdgeCover},
	{transferpkg.ErrManifestNonSatsEdge, manifestRefusalEdgeCover},
	{transferpkg.ErrManifestTotalOverflow, manifestRefusalAmountOverflow},
	{transferpkg.ErrManifestDuplicateEdge, manifestRefusalDuplicate},
	{transferpkg.ErrManifestDuplicateSender, manifestRefusalDuplicate},
	{transferpkg.ErrDuplicateLeafID, manifestRefusalDuplicate},
	{transferpkg.ErrManifestExpiryMismatch, manifestRefusalExpiry},
	{transferpkg.ErrManifestExpiryUnsigned, manifestRefusalExpiry},
	{transferpkg.ErrManifestNetworkMismatch, manifestRefusalNetwork},
	{transferpkg.ErrManifestUnknownNetwork, manifestRefusalNetwork},
	{transferpkg.ErrManifestTooLarge, manifestRefusalSizeCap},
	{transferpkg.ErrManifestInvalidSignature, manifestRefusalSignature},
	{transferpkg.ErrManifestMissingSignature, manifestRefusalSignature},
	{transferpkg.ErrManifestNotHashable, manifestRefusalSignature},
	{transferpkg.ErrManifestInvalidSender, manifestRefusalSenderKey},
	{transferpkg.ErrManifestInvalidReceiver, manifestRefusalReceiverKey},
	{transferpkg.ErrManifestLeafOwnerMismatch, manifestRefusalLeafOwner},
	{transferpkg.ErrManifestTransferIDMismatch, manifestRefusalTransferID},
	{transferpkg.ErrManifestUnknownLeaf, manifestRefusalUnknownLeaf},
	{transferpkg.ErrManifestMissing, manifestRefusalMissingManifest},
}

var manifestRefusals metric.Int64Counter

func init() {
	manifestRefusals = newManifestRefusalCounter()
}

// The name carries no unit: the OTel-to-Prometheus exporter renders unit "1" as a `_ratio` infix
// and appends `_total` itself, so this scrapes as `spark_manifest_refusals_total`.
func newManifestRefusalCounter() metric.Int64Counter {
	counter, err := otel.GetMeterProvider().Meter("handler.manifest").Int64Counter(
		"spark_manifest_refusals",
		metric.WithDescription(
			"Manifest-binding and fee-contract refusals, by refusal class and originating endpoint. "+
				"Counted per operator, so a coordinator-entry refusal increments once while a Prepare-path "+
				"refusal increments once per operator"),
	)
	if err != nil {
		otel.Handle(err)
		if counter == nil {
			counter = noop.Int64Counter{}
		}
	}
	return counter
}

// classifyManifestRefusal reports ok=false outside the sentinel set; callers still count that as
// manifestRefusalOther, so a rising `other` means the sentinel set drifted.
func classifyManifestRefusal(err error) (manifestRefusalKind, bool) {
	for _, classification := range manifestRefusalKindsBySentinel {
		if errors.Is(err, classification.sentinel) {
			return classification.kind, true
		}
	}
	return manifestRefusalOther, false
}

// manifestBindEndpointForTransferType uses the only axis the shared Prepare can distinguish. Only
// start_transfer_v3 binds a manifest today, so an absent series means the gate never ran.
func manifestBindEndpointForTransferType(transferType st.TransferType) manifestBindEndpoint {
	switch transferType {
	case st.TransferTypeTransfer:
		return manifestEndpointStartTransferV3
	case st.TransferTypeUtxoSwap:
		return manifestEndpointStaticDeposit
	case st.TransferTypeCounterSwapV3:
		return manifestEndpointCounterSwapV3
	case st.TransferTypePrimarySwapV3:
		return manifestEndpointPrimarySwapV3
	default:
		return manifestEndpointOther
	}
}

// attestorSignatureRefusalKind splits the gate's refusals three ways: no manifest (it reads one,
// so it refuses first), no signature (a caller yet to ship the field), or one that does not verify.
func attestorSignatureRefusalKind(req *pb.StartTransferV3Request, signature []byte) manifestRefusalKind {
	if req.GetTransferManifest() == nil {
		return manifestRefusalMissingManifest
	}
	if len(signature) == 0 {
		return manifestRefusalMissingAttestorSig
	}
	return manifestRefusalAttestorSignature
}

func recordManifestRefusal(ctx context.Context, endpoint manifestBindEndpoint, kind manifestRefusalKind) {
	manifestRefusals.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kind", string(kind)),
		attribute.String("endpoint", string(endpoint)),
	))
}
