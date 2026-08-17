package transfer

import (
	"encoding/hex"
	"fmt"
	"maps"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/secret_sharing/curve"
	"github.com/lightsparkdev/spark/common/secret_sharing/polynomial"
	"github.com/lightsparkdev/spark/so"
)

var (
	ErrMpcInvalidProofVectorLength = fmt.Errorf("mpc commitment vector length does not match the signing threshold")
	ErrMpcInvalidSubShare          = fmt.Errorf("invalid mpc sub-share")
	ErrMpcSubShareValidationFailed = fmt.Errorf("mpc sub-share does not match its commitment vector")
	ErrMpcTweakBindingMismatch     = fmt.Errorf("combined mpc tweak commitment does not bind to the leaf key")
	ErrMpcDegenerateCommitment     = fmt.Errorf("combined mpc commitment is a degenerate point")
	ErrMpcInvalidOperatorID        = fmt.Errorf("invalid operator identifier")
	ErrMpcInvalidThreshold         = fmt.Errorf("mpc signing threshold must be positive")
)

// CombinedMpcKeyTweak is one leaf's validated, combined key-tweak material: this operator's combined secret share,
// the combined commitment vector, and the per-operator public tweak shares derived from it — everything needed to
// hand the existing key-rotation path a tweak byte-identical to a single-party sender's.
type CombinedMpcKeyTweak struct {
	secretShare  keys.Private
	proofs       []keys.Public
	pubkeyShares map[so.Identifier]keys.Public
}

func (t *CombinedMpcKeyTweak) SecretShare() keys.Private { return t.secretShare }
func (t *CombinedMpcKeyTweak) Proofs() []keys.Public     { return append([]keys.Public(nil), t.proofs...) }
func (t *CombinedMpcKeyTweak) PubkeyShares() map[so.Identifier]keys.Public {
	out := make(map[so.Identifier]keys.Public, len(t.pubkeyShares))
	maps.Copy(out, t.pubkeyShares)
	return out
}

// CombineMpcLeafTweak validates one leaf's sub-user tweak contributions and combines them into the single-party
// shape. Order matters: every sub-share is validated against its own signed commitment vector before any
// combination, so a bad contribution is rejected with its position rather than poisoning the sum; then the combined
// constant-term commitment must equal (leaf owner pubkey − mask commitment), which is what turns a wrong or
// inconsistent mask into a pre-commit rejection instead of an unrecoverable leaf. The mask commitment is trustworthy
// here because it rides in the verified authorization; the leaf owner pubkey must come from the operator's own
// state, never the submission.
//
// subShares is this operator's unsealed sub-share per participant position; operatorID names this operator (its
// identifier is the resharing evaluation point); operatorIDs spans the cluster, for deriving every operator's
// public tweak share from the combined commitment vector; threshold is the SO signing threshold, which every
// commitment vector's length must equal — a shorter vector reshares a lower-degree polynomial.
func CombineMpcLeafTweak(
	leaf *MpcLeaf,
	ownerSigningPubKey keys.Public,
	subShares map[uint32][]byte,
	operatorID so.Identifier,
	operatorIDs []so.Identifier,
	threshold int,
) (*CombinedMpcKeyTweak, error) {
	if threshold <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrMpcInvalidThreshold, threshold)
	}
	vectors := leaf.subUserCommitments
	if len(subShares) != len(vectors) {
		return nil, fmt.Errorf("%w: %d sub-shares for %d participants (leaf %s)", ErrMpcInvalidSubShare, len(subShares), len(vectors), leaf.leafID)
	}

	selfX, err := operatorIdentifierScalar(operatorID)
	if err != nil {
		return nil, err
	}
	positionXs := make([]curve.Scalar, len(vectors))
	for j, vector := range vectors {
		positionXs[j] = curve.ScalarFromInt(vector.position)
	}

	zero := curve.ScalarFromInt(0)
	combinedShare := zero
	combined := &polynomial.PointPolynomial{Coefs: make([]curve.Point, threshold)}
	for k := range combined.Coefs {
		combined.Coefs[k] = curve.IdentityPoint()
	}

	for j, vector := range vectors {
		if len(vector.proofs) != threshold {
			return nil, fmt.Errorf("%w: %d proofs for position %d, leaf %s (threshold %d)",
				ErrMpcInvalidProofVectorLength, len(vector.proofs), vector.position, leaf.leafID, threshold)
		}
		shareBytes, ok := subShares[vector.position]
		if !ok {
			return nil, fmt.Errorf("%w: no sub-share for position %d (leaf %s)", ErrMpcInvalidSubShare, vector.position, leaf.leafID)
		}
		share, err := curve.ParseScalar(shareBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: position %d, leaf %s: %w", ErrMpcInvalidSubShare, vector.position, leaf.leafID, err)
		}
		if share.Equals(zero) {
			return nil, fmt.Errorf("%w: zero sub-share for position %d, leaf %s", ErrMpcInvalidSubShare, vector.position, leaf.leafID)
		}

		vectorPoly := &polynomial.PointPolynomial{Coefs: make([]curve.Point, len(vector.proofs))}
		for k, proof := range vector.proofs {
			vectorPoly.Coefs[k] = curve.NewPointFromPublicKey(proof)
		}
		if !share.Point().Equals(vectorPoly.Eval(selfX)) {
			return nil, fmt.Errorf("%w: position %d, leaf %s", ErrMpcSubShareValidationFailed, vector.position, leaf.leafID)
		}

		lambda, err := polynomial.LagrangeBasisAt(positionXs, j, zero)
		if err != nil {
			return nil, fmt.Errorf("%w: position %d, leaf %s: %w", ErrMpcInvalidSubShare, vector.position, leaf.leafID, err)
		}
		combinedShare = combinedShare.Add(lambda.Mul(share))
		for k := range combined.Coefs {
			combined.Coefs[k] = combined.Coefs[k].Add(vectorPoly.Coefs[k].ScalarMul(lambda))
		}
	}

	ownerPoint := curve.NewPointFromPublicKey(ownerSigningPubKey)
	maskPoint := curve.NewPointFromPublicKey(leaf.maskCommitment)
	if !combined.Coefs[0].Equals(ownerPoint.Sub(maskPoint)) {
		return nil, fmt.Errorf("%w: leaf %s", ErrMpcTweakBindingMismatch, leaf.leafID)
	}

	if combinedShare.Equals(zero) {
		return nil, fmt.Errorf("%w: combined share is zero (leaf %s)", ErrMpcInvalidSubShare, leaf.leafID)
	}
	secretShare, err := keys.ParsePrivateKey(combinedShare.Serialize())
	if err != nil {
		return nil, fmt.Errorf("%w: leaf %s: %w", ErrMpcInvalidSubShare, leaf.leafID, err)
	}

	proofs := make([]keys.Public, threshold)
	for k, coef := range combined.Coefs {
		if proofs[k], err = coef.ToPublicKey(); err != nil {
			return nil, fmt.Errorf("%w: combined coefficient %d (leaf %s): %w", ErrMpcDegenerateCommitment, k, leaf.leafID, err)
		}
	}

	pubkeyShares := make(map[so.Identifier]keys.Public, len(operatorIDs))
	for _, id := range operatorIDs {
		x, err := operatorIdentifierScalar(id)
		if err != nil {
			return nil, err
		}
		if pubkeyShares[id], err = combined.Eval(x).ToPublicKey(); err != nil {
			return nil, fmt.Errorf("%w: public tweak share for operator %s (leaf %s): %w", ErrMpcDegenerateCommitment, id, leaf.leafID, err)
		}
	}

	return &CombinedMpcKeyTweak{secretShare: secretShare, proofs: proofs, pubkeyShares: pubkeyShares}, nil
}

// operatorIdentifierScalar decodes an operator identifier (32 bytes hex-encoded) into the scalar the resharing
// polynomials are evaluated at.
func operatorIdentifierScalar(id so.Identifier) (curve.Scalar, error) {
	raw, err := hex.DecodeString(id)
	if err != nil || len(raw) != 32 {
		return curve.Scalar{}, fmt.Errorf("%w: %q", ErrMpcInvalidOperatorID, id)
	}
	x, err := curve.ParseScalar(raw)
	if err != nil {
		return curve.Scalar{}, fmt.Errorf("%w: %q: %w", ErrMpcInvalidOperatorID, id, err)
	}
	zero := curve.ScalarFromInt(0)
	if x.Equals(zero) {
		return curve.Scalar{}, fmt.Errorf("%w: zero evaluation point %q", ErrMpcInvalidOperatorID, id)
	}
	return x, nil
}
