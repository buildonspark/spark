package curve

import (
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPointEqual(t *testing.T) {
	p := ScalarFromInt(123).Point()
	pAgain := ScalarFromInt(123).Point()
	notP := ScalarFromInt(456).Point()

	assert.True(t, p.Equals(p), "a point should equal itself")
	assert.True(t, p.Equals(pAgain), "a point should equal another equivalent point")

	assert.False(t, p.Equals(notP), "a point should not equal another non-equivalent point")
}

func TestPointEqualIdentity(t *testing.T) {
	idPt1 := IdentityPoint()
	idPt2 := IdentityPoint()

	assert.True(t, idPt1.Equals(idPt2), "identity points should be equal")
}

func assertPointEqualAfterSerializeParse(t *testing.T, p *Point) {
	pBytes := p.Serialize()
	pBytesParse, err := ParsePoint(pBytes)
	require.NoError(t, err, "serializing then deserializing should not cause an error")

	assert.True(t, p.Equals(pBytesParse), "serializing then deserializing should recover the original point")
}

func TestPointSerializeParseIdentity(t *testing.T) {
	idPt := IdentityPoint()
	assertPointEqualAfterSerializeParse(t, &idPt)
}

func TestPointSerializeParse(t *testing.T) {
	p := ScalarFromInt(123).Point()
	assertPointEqualAfterSerializeParse(t, &p)
}

func TestPointParseInvalidDiscriminant(t *testing.T) {
	var serial [PointBytesLen]byte
	serial[0] = 2
	_, err := ParsePoint(serial[:])
	assert.Error(t, err, "invalid discriminant should cause parsing to return an error")
}

func TestPointParseUnsupportedFormat(t *testing.T) {
	var serial [PointBytesLen]byte
	serial[0] = 1
	serial[1] = 99
	_, err := ParsePoint(serial[:])
	assert.Error(t, err, "unsupported public key format should cause parsing to return an error")
}

func TestPointJSONMarshalUnmarshal(t *testing.T) {
	p := ScalarFromInt(123).Point()
	pJSON, err := p.MarshalJSON()
	require.NoError(t, err, "marshaling point to JSON should not return an error")

	var pJSONUnmarshal Point
	err = pJSONUnmarshal.UnmarshalJSON(pJSON)
	require.NoError(t, err, "unmarshaling JSON to a point should not return an error")

	assert.True(t, p.Equals(pJSONUnmarshal), "unmarshaling a marshaled point should produce the original")
}

func TestPointParseNotOnCurve(t *testing.T) {
	p := Point{}
	// x^3 + 7 must be a quadratic non-residue modulo the prime field order
	// for this test to be correct. This can be verified using Euler's criterion.
	p.point.X.SetInt(5)
	p.point.Z.SetInt(1)

	pSerial := p.Serialize()

	_, err := ParsePoint(pSerial)
	assert.Error(t, err, "point not on curve should cause parsing to return an error")
}

func TestPointFromPublicKeyNotOnCurve(t *testing.T) {
	// The elliptic curve equation must hold for a point to be on the curve.
	x := new(secp256k1.FieldVal).SetInt(5)
	y := new(secp256k1.FieldVal).SetInt(1)
	pubKeyNotOnCurve := *secp256k1.NewPublicKey(x, y)

	_, err := NewPointFromPublicKey(pubKeyNotOnCurve)
	assert.Error(t, err, "converting an public key not on the curve to a point should return an error")
}

func TestPointToPublicKeyIdentity(t *testing.T) {
	p := IdentityPoint()

	_, err := p.ToPublicKey()
	assert.Error(t, err, "converting the identity point to a public key should return an error")
}

func TestPointPublicKeyInterchange(t *testing.T) {
	p := ScalarFromInt(123).Point()

	pPubKey, err := p.ToPublicKey()
	require.NoError(t, err, "converting a point to a public key should not return an error")

	pPubKeyPoint, err := NewPointFromPublicKey(pPubKey)
	require.NoError(t, err, "converting a public key to a point should not return an error")

	assert.True(t, p.Equals(pPubKeyPoint), "converting a point to and from a public key should not change the original")
}

func TestPointIdentityCopiesDistinct(t *testing.T) {
	idPt1 := IdentityPoint()
	idPt2 := IdentityPoint()

	assert.NotSame(t, &idPt1, &idPt2, "each identity point should have its own address")
}

func TestPointNeg(t *testing.T) {
	s := ScalarFromInt(123)
	sNegPoint := s.Neg().Point()
	sPointNeg := s.Point().Neg()

	assert.True(t, sNegPoint.Equals(sPointNeg), "the negative of a point should match the negative of its discrete log")

	// Prevent trivially satisfying algebraic check.
	assert.False(t, sNegPoint.isIdentity())
}

func TestPointNegIdentity(t *testing.T) {
	idPt := IdentityPoint()

	assert.True(t, IdentityPoint().Equals(idPt.Neg()), "the negative identity point should still be the identity")
}

func TestPointAddIdentity(t *testing.T) {
	idPt := IdentityPoint()
	p := ScalarFromInt(123).Point()

	assert.True(t, p.Equals(p.Add(idPt)), "adding the identity point should not change the original point")
	assert.True(t, p.Equals(idPt.Add(p)), "adding the identity point should not change the original point")
}

func TestPointAddCommutative(t *testing.T) {
	p := ScalarFromInt(123).Point()
	q := ScalarFromInt(456).Point()

	pQ := p.Add(q)
	qP := q.Add(p)
	assert.True(t, pQ.Equals(qP), "addition should be commutative")

	// Prevent trivially satisfying algebraic check.
	assert.False(t, pQ.isIdentity())
}

func TestPointAddAssociative(t *testing.T) {
	p := ScalarFromInt(123).Point()
	q := ScalarFromInt(456).Point()
	r := ScalarFromInt(789).Point()

	pQThenR := p.Add(q).Add(r)
	pThenQR := p.Add(q.Add(r))
	assert.True(t, pQThenR.Equals(pThenQR), "addition should be associative")

	// Prevent trivially satisfying algebraic check.
	assert.False(t, pQThenR.isIdentity())
}

func TestPointAndScalarAddCompatible(t *testing.T) {
	a := ScalarFromInt(123)
	b := ScalarFromInt(456)

	aBPt := a.Add(b).Point()
	aPtBPt := a.Point().Add(b.Point())

	assert.True(t, aBPt.Equals(aPtBPt), "base point scaling should be homomorphic over addition")

	// Prevent trivially satisfying algebraic check.
	assert.False(t, aBPt.isIdentity())
}

func TestPointSubIdentity(t *testing.T) {
	idPt := IdentityPoint()
	p := ScalarFromInt(123).Point()

	assert.True(t, p.Equals(p.Sub(idPt)), "subtracting the identity point should not change the original")

	// Prevent trivially satisfying algebraic check.
	assert.False(t, p.isIdentity())
}

func TestPointSubEqualsAddNeg(t *testing.T) {
	p := ScalarFromInt(123).Point()
	q := ScalarFromInt(456).Point()

	addNeg := p.Add(q.Neg())
	sub := p.Sub(q)

	assert.True(t, addNeg.Equals(sub), "subtracting should equal adding the negative")

	// Prevent trivially satisfying algebraic check.
	assert.False(t, addNeg.isIdentity())
}

func TestPointAndScalarScalarMulCompatible(t *testing.T) {
	a := ScalarFromInt(123)
	b := ScalarFromInt(456)
	q := b.Point()

	aBPt := a.Mul(b).Point()
	aQ := q.ScalarMul(a)

	assert.True(t, aBPt.Equals(aQ), "scalar multiplication of a point should match scalar multiplication of its discrete log")

	// Prevent trivially satisfying algebraic check.
	assert.False(t, aBPt.isIdentity())
}

func TestPointNegNil(t *testing.T) {
	var nilPoint *Point
	assert.Panics(t, func() { nilPoint.SetNeg() })
}

func TestPointAddNil(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var nilPoint *Point
		p := ScalarFromInt(123).Point()
		assert.Panics(t, func() { nilPoint.SetAdd(&p) })
	})

	t.Run("nil argument", func(t *testing.T) {
		p := ScalarFromInt(123).Point()
		assert.Panics(t, func() { p.SetAdd(nil) })
	})

	t.Run("nil receiver and argument", func(t *testing.T) {
		var nilPoint *Point
		assert.Panics(t, func() { nilPoint.SetAdd(nil) })
	})
}

func TestPointMulNil(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var nilPoint *Point
		a := ScalarFromInt(123)
		assert.Panics(t, func() { nilPoint.SetScalarMul(&a) })
	})

	t.Run("nil argument", func(t *testing.T) {
		p := ScalarFromInt(123).Point()
		assert.Panics(t, func() { p.SetScalarMul(nil) })
	})

	t.Run("nil receiver and argument", func(t *testing.T) {
		var nilPoint *Point
		assert.Panics(t, func() { nilPoint.SetScalarMul(nil) })
	})
}
