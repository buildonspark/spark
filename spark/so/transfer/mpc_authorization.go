package transfer

import (
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/hashstructure"
)

// Domain-separation tags for the multiparty send constructions. The version suffix pins the value schema: any
// change to a payload's value list gets a new version string, so a signature can never verify across schema
// revisions.
var (
	mpcSendAuthorizationTag = []string{"spark", "transfer mpc", "send authorization v1"}
	mpcRefundSighashesTag   = []string{"spark", "transfer mpc", "refund sighashes v1"}
)

// AuthorizationPayload computes the digest the sending group threshold-signs: the authorization's own facts plus
// the public material an untrusted relay routes alongside them — the participant positions, each leaf's sub-user
// commitment vectors, each leaf's secret_cipher, and each leaf's scheme-tagged identity signature (which the
// receiver verifies at claim) — so none of it can be substituted between signing and operator verification. The
// sealed sub-shares are deliberately not hashed: the signed commitment vectors are perfectly binding, so a
// tampered sealed blob can only decrypt to the committed share value or fail validation. Leaves enter sorted by
// leaf id, making the digest independent of wire order.
func (s *MpcSubmission) AuthorizationPayload() []byte {
	hasher := hashstructure.NewHasher(mpcSendAuthorizationTag).
		AddBytes(s.transferID[:]).
		AddUint64(uint64(s.expiryTime.Unix())).
		AddUint64(uint64(len(s.positions)))
	for _, position := range s.positions {
		hasher.AddUint64(uint64(position))
	}
	hasher.AddUint64(uint64(len(s.leaves)))
	sorted := slices.Clone(s.leaves)
	slices.SortFunc(sorted, func(a, b *MpcLeaf) int { return strings.Compare(a.leafID.String(), b.leafID.String()) })
	for _, leaf := range sorted {
		hasher.AddString(leaf.leafID.String()).
			AddUint64(leaf.amountSats).
			AddBytes(leaf.ownerSigningPubKey.Serialize()).
			AddBytes(leaf.maskCommitment.Serialize()).
			AddBytes(leaf.receiverIDPub.Serialize()).
			AddBytes(leaf.secretCipher).
			AddUint32(uint32(leaf.signatureScheme)).
			AddBytes(leaf.signature)
		for _, vector := range leaf.subUserCommitments {
			hasher.AddUint64(uint64(len(vector.proofs)))
			for _, proof := range vector.proofs {
				hasher.AddBytes(proof.Serialize())
			}
		}
	}
	return hasher.AddBytes(s.refundSighashesDigest).Hash()
}

// MpcLeafRefundSighashes carries one leaf's BIP-341 refund sighashes for MpcRefundSighashesDigest. A refund
// flavour the leaf does not carry enters as empty bytes.
type MpcLeafRefundSighashes struct {
	LeafID         uuid.UUID
	CPFP           []byte
	Direct         []byte
	DirectFromCPFP []byte
}

// MpcRefundSighashesDigest commits to the exact refund transactions a submission authorizes, through their BIP-341
// SIGHASH_DEFAULT sighashes — the same messages the sub-users' signing contributions sign, and which an operator
// recomputes from transactions it has validated against its own state. Sighashes rather than raw transaction bytes
// keep the commitment invariant to non-consensus serialization choices, so an honest client with a different
// unsigned-tx serializer is not rejected. Leaves enter sorted by leaf id, making the digest independent of input
// order.
func MpcRefundSighashesDigest(leaves []MpcLeafRefundSighashes) []byte {
	sorted := slices.Clone(leaves)
	slices.SortFunc(sorted, func(a, b MpcLeafRefundSighashes) int {
		return strings.Compare(a.LeafID.String(), b.LeafID.String())
	})
	hasher := hashstructure.NewHasher(mpcRefundSighashesTag).AddUint64(uint64(len(sorted)))
	for _, leaf := range sorted {
		hasher.AddString(leaf.LeafID.String()).
			AddBytes(leaf.CPFP).
			AddBytes(leaf.Direct).
			AddBytes(leaf.DirectFromCPFP)
	}
	return hasher.Hash()
}
