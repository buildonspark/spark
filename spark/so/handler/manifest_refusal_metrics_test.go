package handler

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	msdk "go.opentelemetry.io/otel/sdk/metric"
	md "go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestClassifyManifestRefusalMapsEverySentinel(t *testing.T) {
	tests := []struct {
		sentinel     error
		expectedKind manifestRefusalKind
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

	for _, test := range tests {
		t.Run(test.sentinel.Error(), func(t *testing.T) {
			kind, ok := classifyManifestRefusal(test.sentinel)
			require.True(t, ok)
			require.Equal(t, test.expectedKind, kind)
		})
	}
}

// A sentinel that reaches `other` is invisible on the dashboard's per-kind breakdown, which is the
// resolution this counter exists to provide.
func TestClassifyManifestRefusalCoversEveryRegisteredRefusal(t *testing.T) {
	for _, refusal := range transferpkg.AllManifestRefusals {
		t.Run(refusal.Error(), func(t *testing.T) {
			kind, ok := classifyManifestRefusal(refusal)
			require.True(t, ok, "unclassified refusal would be counted as %q", manifestRefusalOther)
			require.NotEqual(t, manifestRefusalOther, kind)
		})
	}
}

func TestClassifyManifestRefusalCountsAnUnrecognizedErrorRatherThanDroppingIt(t *testing.T) {
	kind, ok := classifyManifestRefusal(fmt.Errorf("something the sentinel set has never seen"))

	require.False(t, ok)
	require.Equal(t, manifestRefusalOther, kind)
}

// The gate classifies before wrapping, but BindManifest wraps some sentinels with parse detail.
func TestClassifyManifestRefusalSeesThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("transfer manifest does not bind this transfer: %w",
		fmt.Errorf("sender 0: %w", transferpkg.ErrManifestInvalidSender))

	kind, ok := classifyManifestRefusal(wrapped)

	require.True(t, ok)
	require.Equal(t, manifestRefusalSenderKey, kind)
}

// Mappings for the types that do not reach the gate today are pinned so the label is right the
// moment they adopt a manifest.
func TestManifestBindEndpointForTransferType(t *testing.T) {
	tests := map[st.TransferType]manifestBindEndpoint{
		st.TransferTypeTransfer:        manifestEndpointStartTransferV3,
		st.TransferTypeUtxoSwap:        manifestEndpointStaticDeposit,
		st.TransferTypeCounterSwapV3:   manifestEndpointCounterSwapV3,
		st.TransferTypePrimarySwapV3:   manifestEndpointPrimarySwapV3,
		st.TransferTypeCooperativeExit: manifestEndpointOther,
	}

	for transferType, expectedEndpoint := range tests {
		t.Run(string(transferType), func(t *testing.T) {
			require.Equal(t, expectedEndpoint, manifestBindEndpointForTransferType(transferType))
		})
	}
}

// The attestor-signature gate reads the manifest, so it refuses a v4 request carrying none
// before the bind gate's own required-manifest check runs.
func TestAttestorSignatureRefusalKind(t *testing.T) {
	t.Run("no manifest is a missing manifest, not a bad signature", func(t *testing.T) {
		require.Equal(t, manifestRefusalMissingManifest,
			attestorSignatureRefusalKind(&pb.StartTransferV3Request{}, []byte{0x01}))
	})

	t.Run("a manifest present with a signature means the signature itself was refused", func(t *testing.T) {
		require.Equal(t, manifestRefusalAttestorSignature,
			attestorSignatureRefusalKind(&pb.StartTransferV3Request{
				TransferManifest: &pb.TransferManifest{},
			}, []byte{0x01}))
	})

	// A caller that has not shipped the field yet is a rollout gap, not the attestor objecting.
	t.Run("a manifest present with no signature is a missing signature, not a refused one", func(t *testing.T) {
		require.Equal(t, manifestRefusalMissingAttestorSig,
			attestorSignatureRefusalKind(&pb.StartTransferV3Request{
				TransferManifest: &pb.TransferManifest{},
			}, nil))
	})
}

// The label string is what a dashboard query and its runbook are written against, while every other
// assertion here compares Go constants — so a rename is otherwise invisible to this suite.
func TestManifestRefusalKindLabelStrings(t *testing.T) {
	labels := map[manifestRefusalKind]string{
		manifestRefusalEdgeCover:          "edge_cover",
		manifestRefusalAmountOverflow:     "amount_overflow",
		manifestRefusalDuplicate:          "duplicate",
		manifestRefusalExpiry:             "expiry",
		manifestRefusalNetwork:            "network",
		manifestRefusalSizeCap:            "size_cap",
		manifestRefusalSignature:          "signature",
		manifestRefusalStraySignature:     "stray_signature",
		manifestRefusalSenderKey:          "sender_key",
		manifestRefusalReceiverKey:        "receiver_key",
		manifestRefusalLeafOwner:          "leaf_owner",
		manifestRefusalTransferID:         "transfer_id",
		manifestRefusalUnknownLeaf:        "unknown_leaf",
		manifestRefusalMissingManifest:    "missing_manifest",
		manifestRefusalAttestorSignature:  "attestor_signature",
		manifestRefusalMissingAttestorSig: "missing_attestor_signature",
		manifestRefusalReason:             "reason",
		manifestRefusalOther:              "other",
	}

	require.Len(t, labels, len(allManifestRefusalKinds),
		"every refusal kind needs its scraped label stated here")
	for _, kind := range allManifestRefusalKinds {
		scrapedLabel, pinned := labels[kind]
		require.True(t, pinned, "refusal kind %q has no pinned label", string(kind))
		assert.Equal(t, scrapedLabel, string(kind))
	}
}

type manifestRefusalLabels struct {
	kind     manifestRefusalKind
	endpoint manifestBindEndpoint
}

// The classifier cannot show which endpoint a refusal is attributed to, nor that the required-
// manifest gate counts once rather than again through the bind gate it delegates to. Asserting the
// whole count map also catches a stray extra increment anywhere.
func TestManifestRefusalCounterAttributesRefusalsToTheRefusingGate(t *testing.T) {
	reader := installManifestRefusalTestMeter(t)
	ctx := t.Context()

	require.Error(t, rejectStrayManifestSignature(ctx, manifestEndpointStartTransferV3,
		&pb.StartTransferV3Request{
			SenderPackages: []*pb.SenderTransferPackage{{ManifestHashSignature: []byte{0x01}}},
		}))

	require.Error(t, requireAndBindManifest(ctx, manifestEndpointInitiatePreimageSwapV4,
		&pb.StartTransferV3Request{}, btcnetwork.Regtest, nil))

	require.Equal(t, map[manifestRefusalLabels]int64{
		{kind: manifestRefusalStraySignature, endpoint: manifestEndpointStartTransferV3}:         1,
		{kind: manifestRefusalMissingManifest, endpoint: manifestEndpointInitiatePreimageSwapV4}: 1,
	}, collectManifestRefusalCounts(ctx, t, reader))
}

// A refusal the gates accept must not touch the counter at all, or every panel reads noise.
func TestManifestRefusalCounterStaysSilentWhenNothingIsRefused(t *testing.T) {
	reader := installManifestRefusalTestMeter(t)
	ctx := t.Context()

	require.NoError(t, rejectStrayManifestSignature(ctx, manifestEndpointStartTransferV3,
		&pb.StartTransferV3Request{SenderPackages: []*pb.SenderTransferPackage{{}}}))
	require.NoError(t, bindManifestIfPresent(ctx, manifestEndpointStartTransferV3,
		&pb.StartTransferV3Request{}, btcnetwork.Regtest, nil))

	// The manifest-less calls above never reach BindManifest, so a covering manifest is what
	// actually pins the binding's success path silent.
	covering := newManifestBindFixture(t, 41)
	require.NoError(t, bindManifestIfPresent(ctx, manifestEndpointStartTransferV3,
		covering.request, btcnetwork.Regtest, lockedLeaf(covering.sender, manifestFixtureLeafSats)))
	required := newManifestBindFixture(t, 42)
	require.NoError(t, requireAndBindManifest(ctx, manifestEndpointInitiatePreimageSwapV4,
		required.request, btcnetwork.Regtest, lockedLeaf(required.sender, manifestFixtureLeafSats)))

	require.Empty(t, collectManifestRefusalCounts(ctx, t, reader))
}

func installManifestRefusalTestMeter(t *testing.T) *msdk.ManualReader {
	t.Helper()

	reader := msdk.NewManualReader()
	prevProvider := otel.GetMeterProvider()
	testProvider := msdk.NewMeterProvider(msdk.WithReader(reader))
	otel.SetMeterProvider(testProvider)
	prevCounter := manifestRefusals
	manifestRefusals = newManifestRefusalCounter()
	t.Cleanup(func() {
		manifestRefusals = prevCounter
		otel.SetMeterProvider(prevProvider)
		// Deliberately not t.Context(): it is cancelled before t.Cleanup runs, so
		// Shutdown needs a live context.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second) //nolint:usetesting // see comment above
		defer cancel()
		require.NoError(t, testProvider.Shutdown(shutdownCtx))
	})
	return reader
}

func collectManifestRefusalCounts(ctx context.Context, t *testing.T, reader *msdk.ManualReader) map[manifestRefusalLabels]int64 {
	t.Helper()

	var rm md.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	counts := make(map[manifestRefusalLabels]int64)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "spark_manifest_refusals" {
				continue
			}
			sums, isSum := m.Data.(md.Sum[int64])
			require.True(t, isSum, "manifest refusals must be an int64 counter")
			// A unit here would have the exporter infix it, renaming the scraped series.
			require.Empty(t, m.Unit, "manifest refusals must carry no unit")
			for _, dp := range sums.DataPoints {
				kind, _ := dp.Attributes.Value(attribute.Key("kind"))
				endpoint, _ := dp.Attributes.Value(attribute.Key("endpoint"))
				counts[manifestRefusalLabels{
					kind:     manifestRefusalKind(kind.AsString()),
					endpoint: manifestBindEndpoint(endpoint.AsString()),
				}] += dp.Value
			}
		}
	}
	return counts
}

// manifestBindFixture builds a request whose manifest covers exactly one locked leaf, so a caller
// can move the locked row out from under it to provoke a specific sentinel.
type manifestBindFixture struct {
	request *pb.StartTransferV3Request
	sender  keys.Public
}

func newManifestBindFixture(t *testing.T, seed byte) manifestBindFixture {
	t.Helper()

	rng := rand.NewChaCha8([32]byte{seed})
	senderKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	transferID := uuid.New().String()

	manifest := &pb.TransferManifest{
		Version:    1,
		TransferId: transferID,
		Network:    pb.Network_REGTEST,
		Edges: []*pb.ManifestEdge{{
			SenderIdentityPublicKey:   senderKey.Public().Serialize(),
			ReceiverIdentityPublicKey: receiver.Serialize(),
			Amount:                    &pb.ManifestAmount{Amount: &pb.ManifestAmount_Sats{Sats: manifestFixtureLeafSats}},
		}},
	}
	hash, err := common.HashTransferManifest(manifest)
	require.NoError(t, err)

	return manifestBindFixture{
		sender: senderKey.Public(),
		request: &pb.StartTransferV3Request{
			TransferId: transferID,
			SenderPackages: []*pb.SenderTransferPackage{{
				OwnerIdentityPublicKey:     senderKey.Public().Serialize(),
				ReceiverIdentityPublicKeys: map[string][]byte{manifestFixtureLeafID: receiver.Serialize()},
				ManifestHashSignature:      ecdsa.Sign(senderKey.ToBTCEC(), hash).Serialize(),
			}},
			TransferManifest: manifest,
		},
	}
}

const (
	manifestFixtureLeafID   = "leaf-a"
	manifestFixtureLeafSats = 1000
)

func lockedLeaf(owner keys.Public, valueSats uint64) map[string]*ent.TreeNode {
	return map[string]*ent.TreeNode{manifestFixtureLeafID: {OwnerIdentityPubkey: owner, Value: valueSats}}
}

// The classifier-driven emit in the bind gate is the only site that turns a BindManifest sentinel
// into a kind, and it serves every endpoint — so it needs the sentinels driven through it for real
// rather than the classifier tested in isolation.
func TestManifestRefusalCounterCountsRealBindRefusals(t *testing.T) {
	t.Run("a manifest disagreeing with the locked value is an edge_cover refusal", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)
		fixture := newManifestBindFixture(t, 11)

		require.Error(t, bindManifestIfPresent(t.Context(), manifestEndpointStartTransferV3,
			fixture.request, btcnetwork.Regtest, lockedLeaf(fixture.sender, manifestFixtureLeafSats-1)))

		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalEdgeCover, endpoint: manifestEndpointStartTransferV3}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})

	t.Run("a manifest disagreeing with the locked owner is a leaf_owner refusal", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)
		fixture := newManifestBindFixture(t, 12)
		otherOwner := keys.MustGeneratePrivateKeyFromRand(rand.NewChaCha8([32]byte{99})).Public()

		require.Error(t, bindManifestIfPresent(t.Context(), manifestEndpointStartTransferV3,
			fixture.request, btcnetwork.Regtest, lockedLeaf(otherOwner, manifestFixtureLeafSats)))

		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalLeafOwner, endpoint: manifestEndpointStartTransferV3}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})

	// The required-manifest gate delegates, so its endpoint has to survive the hand-off.
	t.Run("the endpoint survives delegation from the required-manifest gate", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)
		fixture := newManifestBindFixture(t, 13)

		require.Error(t, requireAndBindManifest(t.Context(), manifestEndpointInitiatePreimageSwapV4,
			fixture.request, btcnetwork.Regtest, lockedLeaf(fixture.sender, manifestFixtureLeafSats-1)))

		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalEdgeCover, endpoint: manifestEndpointInitiatePreimageSwapV4}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})

	t.Run("a mismatched network is a network refusal", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)
		fixture := newManifestBindFixture(t, 14)

		require.Error(t, bindManifestIfPresent(t.Context(), manifestEndpointStartTransferV3,
			fixture.request, btcnetwork.Mainnet, lockedLeaf(fixture.sender, manifestFixtureLeafSats)))

		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalNetwork, endpoint: manifestEndpointStartTransferV3}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})
}

func TestManifestRefusalCounterCountsTheAttestorSignatureGate(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{21})
	attestorKey := keys.MustGeneratePrivateKeyFromRand(rng)

	attestedV4 := func(t *testing.T, manifest *pb.TransferManifest, signWith keys.Private) *pb.InitiatePreimageSwapV4Request {
		t.Helper()
		paymentHash := make([]byte, 32)
		hash, err := common.HashTransferManifest(manifest)
		require.NoError(t, err)
		target, err := common.ReceiveAttestorTarget(paymentHash)
		require.NoError(t, err)
		digest, err := common.QuoteEnvelopeDigest(
			manifest.GetNetwork(), hash, common.QuoteReasonReceive, common.QuoteRoleAttestor, target)
		require.NoError(t, err)
		return &pb.InitiatePreimageSwapV4Request{
			Reason:            pb.InitiatePreimageSwapRequest_REASON_RECEIVE,
			PaymentHash:       paymentHash,
			AttestorSignature: ecdsa.Sign(signWith.ToBTCEC(), digest).Serialize(),
			TransferV3Request: &pb.StartTransferV3Request{TransferManifest: manifest},
		}
	}

	t.Run("a signature with no manifest is a missing_manifest refusal", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)

		require.Error(t, verifyAttestorSignature(t.Context(), &pb.InitiatePreimageSwapV4Request{
			Reason:            pb.InitiatePreimageSwapRequest_REASON_RECEIVE,
			AttestorSignature: []byte{0x01},
			TransferV3Request: &pb.StartTransferV3Request{},
		}, attestorKey.Public()))

		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalMissingManifest, endpoint: manifestEndpointInitiatePreimageSwapV4}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})

	t.Run("no signature at all on a receive is a missing_manifest refusal without a manifest", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)

		require.Error(t, verifyAttestorSignature(t.Context(), &pb.InitiatePreimageSwapV4Request{
			Reason:            pb.InitiatePreimageSwapRequest_REASON_RECEIVE,
			TransferV3Request: &pb.StartTransferV3Request{},
		}, attestorKey.Public()))

		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalMissingManifest, endpoint: manifestEndpointInitiatePreimageSwapV4}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})

	t.Run("no signature with a manifest present is a missing_attestor_signature refusal", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)

		require.Error(t, verifyAttestorSignature(t.Context(), &pb.InitiatePreimageSwapV4Request{
			Reason:            pb.InitiatePreimageSwapRequest_REASON_RECEIVE,
			TransferV3Request: &pb.StartTransferV3Request{TransferManifest: hashableManifest()},
		}, attestorKey.Public()))

		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalMissingAttestorSig, endpoint: manifestEndpointInitiatePreimageSwapV4}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})

	t.Run("a wrong signer over a real manifest is an attestor_signature refusal", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)
		impostor := keys.MustGeneratePrivateKeyFromRand(rand.NewChaCha8([32]byte{22}))

		require.Error(t, verifyAttestorSignature(t.Context(),
			attestedV4(t, hashableManifest(), impostor),
			attestorKey.Public()))

		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalAttestorSignature, endpoint: manifestEndpointInitiatePreimageSwapV4}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})

	// Hashing is a precondition of verifying, so a manifest that will not hash is a malformed
	// manifest, not evidence about the attestor's signature.
	t.Run("a manifest that will not hash is a signature refusal, not an attestor one", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)

		unhashable := &pb.TransferManifest{Version: 1, Network: pb.Network_REGTEST}

		require.Error(t, verifyAttestorSignature(t.Context(), &pb.InitiatePreimageSwapV4Request{
			Reason:            pb.InitiatePreimageSwapRequest_REASON_RECEIVE,
			AttestorSignature: []byte{0x01},
			TransferV3Request: &pb.StartTransferV3Request{TransferManifest: unhashable},
		}, attestorKey.Public()))

		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalSignature, endpoint: manifestEndpointInitiatePreimageSwapV4}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})

	// The digest carries the network, so without an explicit check the same unknown network refuses
	// here as a signature rather than joining the network series it belongs to.
	t.Run("an unknown manifest network is a network refusal, not a signature one", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)

		unnetworked := hashableManifest()
		unnetworked.Network = pb.Network_UNSPECIFIED

		// Signed bytes are arbitrary here: an unsignable network has no envelope digest to sign, which
		// is the whole reason this refuses before verification.
		require.Error(t, verifyAttestorSignature(t.Context(), &pb.InitiatePreimageSwapV4Request{
			Reason:            pb.InitiatePreimageSwapRequest_REASON_RECEIVE,
			PaymentHash:       make([]byte, 32),
			AttestorSignature: []byte{0x01},
			TransferV3Request: &pb.StartTransferV3Request{TransferManifest: unnetworked},
		}, attestorKey.Public()))

		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalNetwork, endpoint: manifestEndpointInitiatePreimageSwapV4}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})

	// v4 refuses a non-RECEIVE request upstream, so this gate treats the reason as a precondition;
	// verifying a SEND signature against the RECEIVE digest would be a cross-flow replay.
	t.Run("a non-receive reason is refused rather than verified", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)

		sendReq := attestedV4(t, hashableManifest(), attestorKey)
		sendReq.Reason = pb.InitiatePreimageSwapRequest_REASON_SEND

		require.Error(t, verifyAttestorSignature(t.Context(), sendReq, attestorKey.Public()))

		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalReason, endpoint: manifestEndpointInitiatePreimageSwapV4}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})

	t.Run("a payment hash with no derivable target is a signature refusal", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)

		shortHash := attestedV4(t, hashableManifest(), attestorKey)
		shortHash.PaymentHash = make([]byte, 31)

		require.Error(t, verifyAttestorSignature(t.Context(), shortHash, attestorKey.Public()))

		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalSignature, endpoint: manifestEndpointInitiatePreimageSwapV4}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})

	t.Run("the attestor's own signature is not counted", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)

		require.NoError(t, verifyAttestorSignature(t.Context(),
			attestedV4(t, hashableManifest(), attestorKey),
			attestorKey.Public()))

		require.Empty(t, collectManifestRefusalCounts(t.Context(), t, reader))
	})
}

func TestManifestRefusalCounterCountsDuplicateLeafDestinations(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{31})
	alice := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	bob := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	canonical := uuid.New().String()

	t.Run("one leaf named twice is a duplicate refusal", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)

		_, err := v4LeafDestinations(t.Context(), map[string]keys.Public{
			canonical: alice, strings.ToUpper(canonical): bob,
		})

		require.Error(t, err)
		require.Equal(t, map[manifestRefusalLabels]int64{
			{kind: manifestRefusalDuplicate, endpoint: manifestEndpointInitiatePreimageSwapV4}: 1,
		}, collectManifestRefusalCounts(t.Context(), t, reader))
	})

	t.Run("distinct leaves are not counted", func(t *testing.T) {
		reader := installManifestRefusalTestMeter(t)

		_, err := v4LeafDestinations(t.Context(), map[string]keys.Public{
			canonical: alice, uuid.New().String(): bob,
		})

		require.NoError(t, err)
		require.Empty(t, collectManifestRefusalCounts(t.Context(), t, reader))
	})
}

// The attestor-signature gate only hashes and verifies, so any well-formed edge suffices.
func hashableManifest() *pb.TransferManifest {
	rng := rand.NewChaCha8([32]byte{51})
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	return &pb.TransferManifest{
		Version:    1,
		Network:    pb.Network_REGTEST,
		TransferId: uuid.New().String(),
		Edges: []*pb.ManifestEdge{{
			SenderIdentityPublicKey:   sender.Serialize(),
			ReceiverIdentityPublicKey: receiver.Serialize(),
			Amount:                    &pb.ManifestAmount{Amount: &pb.ManifestAmount_Sats{Sats: manifestFixtureLeafSats}},
		}},
	}
}
