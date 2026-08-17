package transfer

// The combine/bind math is cryptographic internals invisible at any endpoint (a wrong combination surfaces only as
// a later signing failure or a bricked leaf), so correctness is pinned here against an independently constructed
// reference producer, plus a cross-check against the deployed commitment-evaluation code the outputs must agree
// with. Endpoint-level rejection behavior is covered by the flow-handler tests.

import (
	"math/big"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	"github.com/lightsparkdev/spark/common/secret_sharing/curve"
	"github.com/lightsparkdev/spark/common/secret_sharing/polynomial"
	"github.com/lightsparkdev/spark/so"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mpcTweakFixture struct {
	leaf         *MpcLeaf
	ownerPub     keys.Public
	subShares    map[uint32][]byte // sub-shares sealed to selfOperator
	selfOperator so.Identifier
	operatorIDs  []so.Identifier
	threshold    int
	// reference values the combine must reproduce
	tweak    curve.Scalar
	subPolys []*polynomial.ScalarPolynomial
}

func scalarFromInts(t *testing.T, ints ...uint32) []curve.Scalar {
	t.Helper()
	out := make([]curve.Scalar, len(ints))
	for i, n := range ints {
		require.NotZero(t, n)
		out[i] = curve.ScalarFromInt(n)
	}
	return out
}

// newMpcTweakFixture builds an honest two-sub-user resharing for a 3-operator cluster with threshold 2, from fixed
// coefficients: mask m, tweak t, a Shamir split of t over the sub-user positions, and per-sub-user resharing
// polynomials of degree threshold−1 with Feldman commitments. The leaf's owner key is (t+m)·G so the binding
// A₀ == Pₗ − M holds by construction.
func newMpcTweakFixture(t *testing.T) *mpcTweakFixture {
	t.Helper()
	positions := []uint32{1, 3}
	threshold := 2
	mask := curve.ScalarFromInt(0xA5A5)

	// Shamir split of the tweak over the sub-user positions (degree 1: both participate).
	shamir := &polynomial.ScalarPolynomial{Coefs: scalarFromInts(t, 0xBEEF, 0x1234)}
	tweak := shamir.Eval(curve.ScalarFromInt(0))

	subPolys := []*polynomial.ScalarPolynomial{
		{Coefs: []curve.Scalar{shamir.Eval(curve.ScalarFromInt(positions[0])), curve.ScalarFromInt(0x71)}},
		{Coefs: []curve.Scalar{shamir.Eval(curve.ScalarFromInt(positions[1])), curve.ScalarFromInt(0x72)}},
	}

	vectors := make([]SubUserCommitmentVector, len(positions))
	for j, poly := range subPolys {
		proofs := make([]keys.Public, len(poly.Coefs))
		for k, coef := range poly.Coefs {
			pub, err := coef.Point().ToPublicKey()
			require.NoError(t, err)
			proofs[k] = pub
		}
		vectors[j] = SubUserCommitmentVector{position: positions[j], proofs: proofs}
	}

	selfOperator := so.IndexToIdentifier(1) // evaluation point 2
	subShares := make(map[uint32][]byte, len(positions))
	for j, poly := range subPolys {
		subShares[positions[j]] = poly.Eval(curve.ScalarFromInt(2)).Serialize()
	}

	ownerPoint := tweak.Add(mask).Point()
	ownerPub, err := ownerPoint.ToPublicKey()
	require.NoError(t, err)
	maskPub, err := mask.Point().ToPublicKey()
	require.NoError(t, err)

	return &mpcTweakFixture{
		leaf: &MpcLeaf{
			leafID:             uuid.MustParse("1af52a0d-45c1-4f5b-8c4d-000000000001"),
			maskCommitment:     maskPub,
			subUserCommitments: vectors,
		},
		ownerPub:     ownerPub,
		subShares:    subShares,
		selfOperator: selfOperator,
		operatorIDs:  []so.Identifier{so.IndexToIdentifier(0), so.IndexToIdentifier(1), so.IndexToIdentifier(2)},
		threshold:    threshold,
		tweak:        tweak,
		subPolys:     subPolys,
	}
}

func (f *mpcTweakFixture) combine(t *testing.T) (*CombinedMpcKeyTweak, error) {
	t.Helper()
	return CombineMpcLeafTweak(f.leaf, f.ownerPub, f.subShares, f.selfOperator, f.operatorIDs, f.threshold)
}

func TestCombineMpcLeafTweak_HonestResharing(t *testing.T) {
	f := newMpcTweakFixture(t)
	combined, err := f.combine(t)
	require.NoError(t, err)

	// The combined polynomial is G(x) = Σⱼ λⱼ·Fⱼ(x) with G(0) = t; the operator's share must be G(2) and the
	// constant-term commitment must be t·G.
	positionXs := scalarFromInts(t, 1, 3)
	zero := curve.ScalarFromInt(0)
	expectedShare := zero
	for j, poly := range f.subPolys {
		lambda, err := polynomial.LagrangeBasisAt(positionXs, j, zero)
		require.NoError(t, err)
		expectedShare = expectedShare.Add(lambda.Mul(poly.Eval(curve.ScalarFromInt(2))))
	}
	assert.Equal(t, expectedShare.Serialize(), combined.SecretShare().Serialize())

	tweakPub, err := f.tweak.Point().ToPublicKey()
	require.NoError(t, err)
	require.Len(t, combined.Proofs(), f.threshold)
	assert.True(t, combined.Proofs()[0].Equals(tweakPub))

	// Every operator's public tweak share must agree with the deployed commitment evaluation (index = ID+1 as a
	// big.Int) over the combined proofs — the convention the existing key-tweak path verifies against.
	proofBytes := make([][]byte, len(combined.Proofs()))
	for k, proof := range combined.Proofs() {
		proofBytes[k] = proof.Serialize()
	}
	for i, id := range f.operatorIDs {
		expected, err := secretsharing.EvaluatePolynomialCommitment(proofBytes, big.NewInt(int64(i+1)), secp256k1.S256().N)
		require.NoError(t, err)
		assert.True(t, combined.PubkeyShares()[id].Equals(expected), "operator %d", i)
	}

	// The operator's own public share commits to its combined secret share.
	assert.True(t, combined.PubkeyShares()[f.selfOperator].Equals(combined.SecretShare().Public()))
}

func TestCombineMpcLeafTweak_Rejections(t *testing.T) {
	for name, tc := range map[string]struct {
		perturb     func(t *testing.T, f *mpcTweakFixture)
		expectedErr error
	}{
		"wrong mask": {
			perturb: func(t *testing.T, f *mpcTweakFixture) {
				pub, err := curve.ScalarFromInt(0xDEAD).Point().ToPublicKey()
				require.NoError(t, err)
				f.leaf.maskCommitment = pub
			},
			expectedErr: ErrMpcTweakBindingMismatch,
		},
		"wrong-but-consistent constant term": {
			// A sub-user whose polynomial self-validates but reshared the wrong value: Feldman passes, the
			// binding check is what catches it.
			perturb: func(t *testing.T, f *mpcTweakFixture) {
				poly := &polynomial.ScalarPolynomial{Coefs: scalarFromInts(t, 0x666, 0x71)}
				proofs := make([]keys.Public, len(poly.Coefs))
				for k, coef := range poly.Coefs {
					pub, err := coef.Point().ToPublicKey()
					require.NoError(t, err)
					proofs[k] = pub
				}
				f.leaf.subUserCommitments[0] = SubUserCommitmentVector{position: 1, proofs: proofs}
				f.subShares[1] = poly.Eval(curve.ScalarFromInt(2)).Serialize()
			},
			expectedErr: ErrMpcTweakBindingMismatch,
		},
		"tampered sub-share": {
			perturb: func(t *testing.T, f *mpcTweakFixture) {
				f.subShares[1] = curve.ScalarFromInt(0x999).Serialize()
			},
			expectedErr: ErrMpcSubShareValidationFailed,
		},
		"tampered proof": {
			perturb: func(t *testing.T, f *mpcTweakFixture) {
				pub, err := curve.ScalarFromInt(0x777).Point().ToPublicKey()
				require.NoError(t, err)
				f.leaf.subUserCommitments[0].proofs[1] = pub
			},
			expectedErr: ErrMpcSubShareValidationFailed,
		},
		"missing position": {
			perturb:     func(t *testing.T, f *mpcTweakFixture) { delete(f.subShares, 3) },
			expectedErr: ErrMpcInvalidSubShare,
		},
		"zero sub-share": {
			perturb: func(t *testing.T, f *mpcTweakFixture) {
				f.subShares[1] = make([]byte, 32)
			},
			expectedErr: ErrMpcInvalidSubShare,
		},
		"non-canonical sub-share": {
			perturb: func(t *testing.T, f *mpcTweakFixture) {
				overflow := make([]byte, 32)
				for i := range overflow {
					overflow[i] = 0xFF
				}
				f.subShares[1] = overflow
			},
			expectedErr: ErrMpcInvalidSubShare,
		},
		"proof vector shorter than threshold": {
			perturb: func(t *testing.T, f *mpcTweakFixture) {
				f.leaf.subUserCommitments[0].proofs = f.leaf.subUserCommitments[0].proofs[:1]
			},
			expectedErr: ErrMpcInvalidProofVectorLength,
		},
		"malformed operator id": {
			perturb:     func(t *testing.T, f *mpcTweakFixture) { f.selfOperator = "not-hex" },
			expectedErr: ErrMpcInvalidOperatorID,
		},
		"wrong-length operator id": {
			perturb:     func(t *testing.T, f *mpcTweakFixture) { f.selfOperator = "abcd" },
			expectedErr: ErrMpcInvalidOperatorID,
		},
		"zero operator id": {
			perturb:     func(t *testing.T, f *mpcTweakFixture) { f.selfOperator = strings.Repeat("00", 32) },
			expectedErr: ErrMpcInvalidOperatorID,
		},
		"zero threshold": {
			perturb:     func(t *testing.T, f *mpcTweakFixture) { f.threshold = 0 },
			expectedErr: ErrMpcInvalidThreshold,
		},
		"negative threshold": {
			perturb:     func(t *testing.T, f *mpcTweakFixture) { f.threshold = -1 },
			expectedErr: ErrMpcInvalidThreshold,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMpcTweakFixture(t)
			tc.perturb(t, f)
			_, err := f.combine(t)
			require.ErrorIs(t, err, tc.expectedErr)
		})
	}
}
