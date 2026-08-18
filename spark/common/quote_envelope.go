package common

import (
	"fmt"
	"math"

	"github.com/lightsparkdev/spark/common/hashstructure"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

// QuoteReason names the flow a quote was issued for, so a quote for one flow cannot be replayed
// into another. Starts at 1 so 0 stays an unset sentinel.
type QuoteReason uint64

const (
	QuoteReasonReceive       QuoteReason = 1
	QuoteReasonSend          QuoteReason = 2
	QuoteReasonCoopExit      QuoteReason = 3
	QuoteReasonStaticDeposit QuoteReason = 4
)

// QuoteRole names which party signed a quote envelope, so one role's signature can never satisfy
// the other's check. Required even where the two roles happen to sign different targets: relying on
// that coincidence would break the first time a flow gave both roles the same target.
type QuoteRole uint64

const (
	QuoteRoleIssuer   QuoteRole = 1
	QuoteRoleAttestor QuoteRole = 2
)

var (
	quoteTag                 = []string{"spark", "quote", "v1"}
	receiveAttestorTargetTag = []string{"spark", "quote", "target", "receive", "attestor", "v1"}
)

// isSignableProtoNetwork is the one network rule the manifest hasher and this module share, so the
// two cannot drift apart on which networks are signable.
func isSignableProtoNetwork(network pb.Network) bool {
	return network != pb.Network_UNSPECIFIED && isKnownEnumValue(pb.Network_name, int32(network))
}

// isSignableNetwork additionally bounds a width the proto type cannot express. It exists only for
// the untyped entry point below, whose fixture cases cover inputs that alias a valid enum after
// truncation while hashing their untruncated bytes.
func isSignableNetwork(network uint64) bool {
	return network <= math.MaxInt32 && isSignableProtoNetwork(pb.Network(int32(network)))
}

func require32Bytes(name string, value []byte) error {
	if len(value) != 32 {
		return fmt.Errorf("%s must be 32 bytes, got %d", name, len(value))
	}
	return nil
}

// ReceiveAttestorTarget binds a receive quote to the invoice it settles. The attestor signs at
// commit time, when the payment hash exists; the issuer signs at quote time, when it does not — so
// only the attestor's target carries it.
func ReceiveAttestorTarget(paymentHash []byte) ([]byte, error) {
	if err := require32Bytes("payment_hash", paymentHash); err != nil {
		return nil, err
	}
	return hashstructure.NewHasher(receiveAttestorTargetTag).AddBytes(paymentHash).Hash(), nil
}

// QuoteEnvelopeDigest is the digest every party to a fee quote signs:
//
//	tagged_hash(["spark","quote","v1"], network, manifest_hash, reason, role, target)
//
// target carries whatever is not manifest-resident but is known to that signer when it signs, which
// is why two roles on one quote can legitimately sign different targets. An issuer signs before the
// thing it would bind exists, so its target may be empty; an attestor signs after, so its may not.
func QuoteEnvelopeDigest(network pb.Network, manifestHash []byte, reason QuoteReason, role QuoteRole, target []byte) ([]byte, error) {
	return quoteEnvelopeDigest(uint64(network), manifestHash, reason, role, target)
}

func quoteEnvelopeDigest(network uint64, manifestHash []byte, reason QuoteReason, role QuoteRole, target []byte) ([]byte, error) {
	if err := require32Bytes("manifest_hash", manifestHash); err != nil {
		return nil, err
	}
	if !isSignableNetwork(network) {
		return nil, fmt.Errorf("unsupported network %d", network)
	}
	switch reason {
	case QuoteReasonReceive, QuoteReasonSend, QuoteReasonCoopExit, QuoteReasonStaticDeposit:
	default:
		return nil, fmt.Errorf("unsupported quote reason %d", reason)
	}
	switch role {
	case QuoteRoleIssuer, QuoteRoleAttestor:
	default:
		return nil, fmt.Errorf("unsupported quote role %d", role)
	}
	if role == QuoteRoleAttestor && len(target) == 0 {
		return nil, fmt.Errorf("attestor target must be non-empty")
	}
	return hashstructure.NewHasher(quoteTag).
		AddUint64(network).
		AddBytes(manifestHash).
		AddUint64(uint64(reason)).
		AddUint64(uint64(role)).
		AddBytes(target).
		Hash(), nil
}
