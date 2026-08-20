package transfer

import (
	"fmt"
	"math/big"
	"time"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/proto/spark"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Round numbers bounding per-request verification and hashing work, set well above any real
// manifest so the handler's own leaf limit is what binds. Not a transfer-size policy. The routed
// leaf cap is the one tied to something real: it is the sender-package cap times the handler's
// per-sender leaf limit, so the two stay ordered.
//
// TODO(SP-1921): fold into the total-transaction size validation that owns the real limits.
const (
	MaxManifestSenderPackages = 16
	MaxManifestEdges          = 512
	MaxManifestRoutedLeaves   = 16384
	MaxManifestFees           = 256

	bpsDenominator = 10_000
)

var (
	ErrManifestAmountMismatch      = fmt.Errorf("manifest edge amount does not match executed leaves")
	ErrManifestBpsSumInvalid       = fmt.Errorf("manifest bps edges must sum to 10000")
	ErrManifestDuplicateEdge       = fmt.Errorf("manifest declares more than one edge for a sender/receiver pair")
	ErrManifestDuplicateSender     = fmt.Errorf("duplicate sender in sender packages")
	ErrManifestEdgeNotRealized     = fmt.Errorf("manifest edge has no executed leaves")
	ErrManifestEdgeOutsideWindow   = fmt.Errorf("executed leaves fall outside the manifest edge's resolved window")
	ErrManifestExpiryMismatch      = fmt.Errorf("request expiry time does not match manifest")
	ErrManifestExpiryUnsigned      = fmt.Errorf("multi-sender manifest must commit to a transfer expiry")
	ErrManifestFlatFeesExceedGross = fmt.Errorf("manifest sats edges leave nothing for the bps edges to divide")
	ErrManifestInvalidReceiver     = fmt.Errorf("invalid receiver identity public key")
	ErrManifestInvalidSender       = fmt.Errorf("invalid sender identity public key")
	ErrManifestInvalidSignature    = fmt.Errorf("invalid manifest hash signature")
	ErrManifestLeafNotRouted       = fmt.Errorf("leaf has no receiver in any sender package")
	ErrManifestLeafOwnerMismatch   = fmt.Errorf("leaf is not owned by the sender package routing it")
	ErrManifestMissing             = fmt.Errorf("transfer manifest is missing")
	ErrManifestMissingSignature    = fmt.Errorf("missing manifest hash signature")
	ErrManifestNetworkMismatch     = fmt.Errorf("manifest network does not match the transfer")
	ErrManifestUnknownNetwork      = fmt.Errorf("manifest network is unspecified or unknown")
	ErrManifestNotHashable         = fmt.Errorf("transfer manifest is not hashable")
	ErrManifestTotalOverflow       = fmt.Errorf("manifest amount total overflows")
	ErrManifestTooLarge            = fmt.Errorf("transfer manifest exceeds size limits")
	ErrManifestTransferIDMismatch  = fmt.Errorf("request transfer id does not match manifest")
	ErrManifestUnknownLeaf         = fmt.Errorf("routed leaf has no server-side record")
	ErrManifestUnlistedTransfer    = fmt.Errorf("executed leaves have no matching manifest edge")
)

// AllManifestRefusals enumerates BindManifest's refusals for callers that bucket them. A new
// ErrManifest sentinel in this file must be appended here; a test in this package fails when not.
var AllManifestRefusals = []error{
	ErrManifestAmountMismatch,
	ErrManifestBpsSumInvalid,
	ErrManifestDuplicateEdge,
	ErrManifestDuplicateSender,
	ErrManifestEdgeNotRealized,
	ErrManifestEdgeOutsideWindow,
	ErrManifestExpiryMismatch,
	ErrManifestExpiryUnsigned,
	ErrManifestFlatFeesExceedGross,
	ErrManifestInvalidReceiver,
	ErrManifestInvalidSender,
	ErrManifestInvalidSignature,
	ErrManifestLeafNotRouted,
	ErrManifestLeafOwnerMismatch,
	ErrManifestMissing,
	ErrManifestMissingSignature,
	ErrManifestNetworkMismatch,
	ErrManifestUnknownNetwork,
	ErrManifestNotHashable,
	ErrManifestTotalOverflow,
	ErrManifestTooLarge,
	ErrManifestTransferIDMismatch,
	ErrManifestUnknownLeaf,
	ErrManifestUnlistedTransfer,
	ErrDuplicateLeafID,
}

// ExecutedLeaf is the operator's own record of a leaf. Both fields must come from the locked
// rows: sourcing either from the request would make the binding restate the caller's own claim.
// Keys are leaf IDs in the operator's canonical spelling, which is what the routed IDs are
// matched against verbatim.
type ExecutedLeaf struct {
	Owner     keys.Public
	ValueSats uint64
}

// BindManifest enforces that the transfer the operator is about to execute is exactly the one
// every sender signed.
//
// leaves must hold every leaf the transfer actually moves, keyed by leaf ID; deriving it from
// the request's receiver map would make the unrouted-leaf check agree by construction. network
// must likewise be the operator's own value for these leaves, never one taken from the request.
//
// The receiver half of each edge is read from the request's per-leaf receiver map, which this
// function cannot authenticate. Callers must have validated that map against each leaf's refund
// output first, or the binding attests to routing the request merely asserted.
//
// A sole sender may omit the manifest expiry, which leaves the request's expiry unsigned. That
// exemption is only sound if the caller has enforced that the authenticated identity IS that
// sender — no identity reaches this function, so the obligation is the caller's.
//
// A nil manifest is an error, so callers branch on presence — and a manifest-less request whose
// packages still carry signature bytes is not this function's business to reject.
//
// Only edges[] is read. fees[] and quote_expiry_time describe how the SSP priced the transfer,
// which the operator neither enforces nor understands.
//
// A bps edge binds a proportion, not a quantity: it is resolved against a gross the operator
// observes but cannot certify, since the senders choose what to fund. Callers wanting a signed
// amount must declare it in sats.
func BindManifest(req *spark.StartTransferV3Request, network btcnetwork.Network, leaves map[string]ExecutedLeaf) error {
	manifest := req.GetTransferManifest()
	if manifest == nil {
		return ErrManifestMissing
	}

	if manifest.GetTransferId() != req.GetTransferId() {
		return fmt.Errorf("%w: request %q, manifest %q", ErrManifestTransferIDMismatch, req.GetTransferId(), manifest.GetTransferId())
	}

	if err := checkManifestSize(req, manifest); err != nil {
		return err
	}

	if err := bindManifestNetwork(manifest, network); err != nil {
		return err
	}

	if err := bindManifestExpiry(req, manifest); err != nil {
		return err
	}

	manifestHash, err := common.HashTransferManifest(manifest)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrManifestNotHashable, err)
	}

	signed, err := verifyManifestSignatures(req.GetSenderPackages(), manifestHash)
	if err != nil {
		return err
	}

	return bindManifestEdges(manifest.GetEdges(), signed, leaves)
}

// signedSenderPackage pairs a package with its already-parsed, already-verified sender so the
// edge check cannot drift out of step with the signature check.
type signedSenderPackage struct {
	sender keys.Public
	pkg    *spark.SenderTransferPackage
}

// Checked on raw list lengths before hashing or any signature verification, so a pathologically
// shaped request is rejected before it can buy the expensive work.
func checkManifestSize(req *spark.StartTransferV3Request, manifest *spark.TransferManifest) error {
	if packages := len(req.GetSenderPackages()); packages > MaxManifestSenderPackages {
		return fmt.Errorf("%w: %d sender packages, max %d", ErrManifestTooLarge, packages, MaxManifestSenderPackages)
	}
	if edges := len(manifest.GetEdges()); edges > MaxManifestEdges {
		return fmt.Errorf("%w: %d edges, max %d", ErrManifestTooLarge, edges, MaxManifestEdges)
	}
	// Unread by the binding, but every fee is sorted and hashed, which makes this the largest
	// input the digest takes.
	if fees := len(manifest.GetFees()); fees > MaxManifestFees {
		return fmt.Errorf("%w: %d fees, max %d", ErrManifestTooLarge, fees, MaxManifestFees)
	}
	routed := 0
	for _, pkg := range req.GetSenderPackages() {
		routed += len(pkg.GetReceiverIdentityPublicKeys())
	}
	if routed > MaxManifestRoutedLeaves {
		return fmt.Errorf("%w: %d routed leaves, max %d", ErrManifestTooLarge, routed, MaxManifestRoutedLeaves)
	}
	return nil
}

// Transfer IDs are caller-chosen and collide freely across networks, so without this a signature
// made on one chain is replayable against a same-id transfer on another.
func bindManifestNetwork(manifest *spark.TransferManifest, network btcnetwork.Network) error {
	manifestNetwork, err := btcnetwork.FromProtoNetwork(manifest.GetNetwork())
	if err != nil {
		return fmt.Errorf("%w: %w", ErrManifestUnknownNetwork, err)
	}
	if manifestNetwork != network {
		return fmt.Errorf("%w: transfer %s, manifest %s", ErrManifestNetworkMismatch, network, manifestNetwork)
	}
	return nil
}

// The manifest's expiry is optional — sole-sender flows omit it — but a manifest that does commit
// to one must match the expiry the transfer actually gets, or the signed timelock means nothing.
// Compared at millisecond precision because finer nanos are floored before hashing, so rejecting
// on them would refuse pairs the signature authorizes.
func bindManifestExpiry(req *spark.StartTransferV3Request, manifest *spark.TransferManifest) error {
	manifestExpiry := manifest.GetTransferExpiryTime()
	if manifestExpiry == nil {
		// Omitting it leaves the request's expiry unsigned, which is only safe when the lone
		// sender is the requester — otherwise an intermediary picks the cancellation timing.
		// Callers own that equality; this function cannot see the authenticated identity.
		if len(req.GetSenderPackages()) > 1 {
			return fmt.Errorf("%w: %d senders", ErrManifestExpiryUnsigned, len(req.GetSenderPackages()))
		}
		return nil
	}
	requestExpiry := req.GetExpiryTime()
	if requestExpiry == nil {
		return fmt.Errorf("%w: manifest sets an expiry, request does not", ErrManifestExpiryMismatch)
	}
	if !signedExpiryInstant(requestExpiry).Equal(signedExpiryInstant(manifestExpiry)) {
		return fmt.Errorf("%w: request %v, manifest %v", ErrManifestExpiryMismatch, requestExpiry.AsTime(), manifestExpiry.AsTime())
	}
	return nil
}

func signedExpiryInstant(ts *timestamppb.Timestamp) time.Time {
	return ts.AsTime().Truncate(time.Millisecond)
}

// Every sender package must carry its own signature over the manifest hash: a sender that did not
// sign has not agreed to the split, and the manifest binds the transfer for all of them jointly.
func verifyManifestSignatures(pkgs []*spark.SenderTransferPackage, manifestHash []byte) ([]signedSenderPackage, error) {
	signed := make([]signedSenderPackage, 0, len(pkgs))
	seen := make(map[keys.Public]struct{}, len(pkgs))

	for i, pkg := range pkgs {
		sender, err := keys.ParsePublicKey(pkg.GetOwnerIdentityPublicKey())
		if err != nil {
			return nil, fmt.Errorf("%w: sender_packages[%d]: %w", ErrManifestInvalidSender, i, err)
		}
		if _, dup := seen[sender]; dup {
			return nil, fmt.Errorf("%w: %x", ErrManifestDuplicateSender, sender.Serialize())
		}
		seen[sender] = struct{}{}

		signature := pkg.GetManifestHashSignature()
		if len(signature) == 0 {
			return nil, fmt.Errorf("%w: sender %x", ErrManifestMissingSignature, sender.Serialize())
		}
		// Strict-DER and low-S are enforced inside: a high-S signature re-serializes to different
		// bytes and fails the canonical-encoding check, so the same signature cannot be replayed
		// in a second malleated form.
		if err := common.VerifyECDSASignature(sender, signature, manifestHash); err != nil {
			return nil, fmt.Errorf("%w: sender %x: %w", ErrManifestInvalidSignature, sender.Serialize(), err)
		}

		signed = append(signed, signedSenderPackage{sender: sender, pkg: pkg})
	}

	return signed, nil
}

type manifestEdgeKey struct {
	sender   keys.Public
	receiver keys.Public
}

// bindManifestEdges requires a cover between the manifest's declared edges and the value the
// request actually moves: every edge realized, no unlisted (sender, receiver) pair executed, no
// leaf left unrouted. Anything less lets a caller move value the manifest never described. Sats
// edges bind exactly; a bps edge binds to its resolved window, so the rounding dust may land on
// any of the parties the manifest already names.
func bindManifestEdges(
	edges []*spark.ManifestEdge,
	signed []signedSenderPackage,
	leaves map[string]ExecutedLeaf,
) error {
	executed, err := executedEdgeTotals(signed, leaves)
	if err != nil {
		return err
	}

	parsed, err := declaredEdgeKeys(edges)
	if err != nil {
		return err
	}
	declaredPairs := make(map[manifestEdgeKey]struct{}, len(parsed))
	for _, key := range parsed {
		declaredPairs[key] = struct{}{}
	}

	// Ahead of the gross: value routed to a pair no edge names would otherwise inflate what every
	// bps edge divides, and the refusal would name whichever check that skew tripped first.
	for key, executedSats := range executed {
		if _, ok := declaredPairs[key]; !ok {
			return fmt.Errorf("%w: %x -> %x for %d sats", ErrManifestUnlistedTransfer, key.sender.Serialize(), key.receiver.Serialize(), executedSats)
		}
	}

	// The remainder is derived from the DECLARED flat total, so a short flat edge left for later
	// moves every bps edge's window and is refused as one of theirs.
	for i, edge := range edges {
		amount, isSats := edge.GetAmount().GetAmount().(*spark.ManifestAmount_Sats)
		if !isSats {
			continue
		}
		executedSats, realized := executed[parsed[i]]
		if !realized {
			return fmt.Errorf("%w: %x -> %x for %d sats", ErrManifestEdgeNotRealized, parsed[i].sender.Serialize(), parsed[i].receiver.Serialize(), amount.Sats)
		}
		if executedSats != amount.Sats {
			return fmt.Errorf("%w: %x -> %x declared %d sats, executed %d sats", ErrManifestAmountMismatch, parsed[i].sender.Serialize(), parsed[i].receiver.Serialize(), amount.Sats, executedSats)
		}
	}

	var gross uint64
	for _, executedSats := range executed {
		total, err := addSats(gross, executedSats)
		if err != nil {
			return fmt.Errorf("executed gross: %w", err)
		}
		gross = total
	}

	declared, err := declaredEdgeTotals(edges, parsed, gross)
	if err != nil {
		return err
	}

	// An edge nobody funded shrinks the gross every other edge resolved against, so its absence
	// pushes them out of their windows too. Naming it first keeps the cause ahead of its effects.
	for key, resolved := range declared {
		if _, realized := executed[key]; realized {
			continue
		}
		// A bps edge flooring to zero is not levied, so nothing realizes it.
		if resolved.sats == 0 {
			continue
		}
		return fmt.Errorf("%w: %x -> %x for %d sats", ErrManifestEdgeNotRealized, key.sender.Serialize(), key.receiver.Serialize(), resolved.sats)
	}

	for key, resolved := range declared {
		executedSats, ok := executed[key]
		if !ok {
			continue
		}
		if executedSats < resolved.sats || executedSats > resolved.allowed {
			if resolved.allowed == resolved.sats {
				return fmt.Errorf("%w: %x -> %x declared %d sats, executed %d sats", ErrManifestAmountMismatch, key.sender.Serialize(), key.receiver.Serialize(), resolved.sats, executedSats)
			}
			return fmt.Errorf("%w: %x -> %x resolved %d sats, executed %d sats", ErrManifestEdgeOutsideWindow, key.sender.Serialize(), key.receiver.Serialize(), resolved.sats, executedSats)
		}
	}

	return nil
}

// allowed exceeds sats by one for a bps edge: independent flooring leaves the total short of the
// gross, and edges[] names no payee, so the operator cannot say which party absorbs that dust.
// An edge flooring to zero keeps that allowance: at a small gross the deficit can exceed the edges
// that floored above zero, leaving no split that binds at all.
type resolvedEdge struct {
	sats    uint64
	allowed uint64
}

// edges[] carries at most one entry per (sender, receiver): a key that is both a destination and
// a fee receiver gets a single edge for its total, with fees[] annotating the slice. Summing
// duplicates instead would let one movement have several valid signed forms, since the digest
// covers the list as written.
//
// Refusing a repeat here, ahead of every sum, is what keeps a duplicate named for repeating rather
// than for the rate table it corrupts.
func declaredEdgeKeys(edges []*spark.ManifestEdge) ([]manifestEdgeKey, error) {
	parsed := make([]manifestEdgeKey, len(edges))
	seen := make(map[manifestEdgeKey]struct{}, len(edges))
	for i, edge := range edges {
		sender, err := keys.ParsePublicKey(edge.GetSenderIdentityPublicKey())
		if err != nil {
			return nil, fmt.Errorf("%w: edges[%d] sender: %w", ErrManifestInvalidSender, i, err)
		}
		receiver, err := keys.ParsePublicKey(edge.GetReceiverIdentityPublicKey())
		if err != nil {
			return nil, fmt.Errorf("%w: edges[%d] receiver: %w", ErrManifestInvalidReceiver, i, err)
		}

		key := manifestEdgeKey{sender: sender, receiver: receiver}
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("%w: edges[%d]: %x -> %x", ErrManifestDuplicateEdge, i, sender.Serialize(), receiver.Serialize())
		}
		seen[key] = struct{}{}
		parsed[i] = key
	}
	return parsed, nil
}

// Sats edges are flat charges and come off the top, so a bps rate means "of the amount being
// split" rather than of the gross once a flat fee exists.
//
// gross pools every sender's leaves, so a rate is of the whole transfer rather than of the
// signer's own contribution. Every sender signs the same manifest, so all of them commit to that
// reading.
func declaredEdgeTotals(edges []*spark.ManifestEdge, parsed []manifestEdgeKey, gross uint64) (map[manifestEdgeKey]resolvedEdge, error) {
	var flatSats uint64
	var bpsTotal uint64
	anyBps := false
	for i, edge := range edges {
		switch amount := edge.GetAmount().GetAmount().(type) {
		case *spark.ManifestAmount_Sats:
			total, err := addSats(flatSats, amount.Sats)
			if err != nil {
				return nil, fmt.Errorf("edges[%d]: %w", i, err)
			}
			flatSats = total
		case *spark.ManifestAmount_Bps:
			anyBps = true
			bpsTotal += uint64(amount.Bps)
		default:
			return nil, fmt.Errorf("%w: edges[%d]", ErrManifestNotHashable, i)
		}
	}

	remainder := gross
	if anyBps {
		// Rates that do not partition the remainder leave the windows wide enough to admit a
		// manifest declaring only part of the gross.
		if bpsTotal != bpsDenominator {
			return nil, fmt.Errorf("%w: edges sum to %d", ErrManifestBpsSumInvalid, bpsTotal)
		}
		if gross == 0 {
			return nil, fmt.Errorf("%w: no executed value to divide", ErrManifestFlatFeesExceedGross)
		}
		if flatSats >= gross {
			return nil, fmt.Errorf("%w: %d sats of %d", ErrManifestFlatFeesExceedGross, flatSats, gross)
		}
		remainder = gross - flatSats
	}

	totals := make(map[manifestEdgeKey]resolvedEdge, len(edges))
	for i, edge := range edges {
		switch amount := edge.GetAmount().GetAmount().(type) {
		case *spark.ManifestAmount_Sats:
			totals[parsed[i]] = resolvedEdge{sats: amount.Sats, allowed: amount.Sats}
		case *spark.ManifestAmount_Bps:
			// big.Int because remainder*bpsDenominator overflows uint64 near the total bitcoin supply.
			product := new(big.Int).Mul(new(big.Int).SetUint64(remainder), new(big.Int).SetUint64(uint64(amount.Bps)))
			resolved := product.Div(product, big.NewInt(bpsDenominator)).Uint64()
			totals[parsed[i]] = resolvedEdge{sats: resolved, allowed: resolved + 1}
		}
	}

	return totals, nil
}

func executedEdgeTotals(
	signed []signedSenderPackage,
	leaves map[string]ExecutedLeaf,
) (map[manifestEdgeKey]uint64, error) {
	totals := make(map[manifestEdgeKey]uint64)
	routed := make(map[string]struct{}, len(leaves))

	for i, entry := range signed {
		for leafID, receiverBytes := range entry.pkg.GetReceiverIdentityPublicKeys() {
			receiver, err := keys.ParsePublicKey(receiverBytes)
			if err != nil {
				return nil, fmt.Errorf("%w: sender_packages[%d] leaf %s receiver: %w", ErrManifestInvalidReceiver, i, leafID, err)
			}
			if _, dup := routed[leafID]; dup {
				return nil, fmt.Errorf("%w: %s", ErrDuplicateLeafID, leafID)
			}
			routed[leafID] = struct{}{}

			leaf, ok := leaves[leafID]
			if !ok {
				return nil, fmt.Errorf("%w: %s", ErrManifestUnknownLeaf, leafID)
			}
			if leaf.Owner != entry.sender {
				return nil, fmt.Errorf("%w: leaf %s routed by %x", ErrManifestLeafOwnerMismatch, leafID, entry.sender.Serialize())
			}

			key := manifestEdgeKey{sender: leaf.Owner, receiver: receiver}
			total, err := addSats(totals[key], leaf.ValueSats)
			if err != nil {
				return nil, fmt.Errorf("sender_packages[%d]: %w", i, err)
			}
			totals[key] = total
		}
	}

	// A leaf the request moves but no package routes would leave value unaccounted for by any edge.
	for leafID := range leaves {
		if _, ok := routed[leafID]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrManifestLeafNotRouted, leafID)
		}
	}

	return totals, nil
}

func addSats(total, delta uint64) (uint64, error) {
	sum := total + delta
	if sum < total {
		return 0, ErrManifestTotalOverflow
	}
	return sum, nil
}
