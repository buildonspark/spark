package curve

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingReader struct{}

// Read implements io.Reader
func (r failingReader) Read(p []byte) (n int, err error) {
	return 0, errors.New("no bytes to give")
}

func TestScalarGenerateError(t *testing.T) {
	r := failingReader{}

	_, err := GenerateScalarFromRand(r)
	assert.Error(t, err, "a failing entropy source should cause an error")
}

func TestScalarGenerate(t *testing.T) {
	a, err := GenerateScalar()
	require.NoError(t, err)

	zero := ScalarFromInt(0)

	assert.False(t, zero.Equals(a), "a successfully generated scalar should not be zero")
}

func TestScalarSerializeSmall(t *testing.T) {
	a := ScalarFromInt(0x04_03_02_01)
	aBytes := a.Serialize()   // Big endian
	slices.Reverse(aBytes[:]) // Change to little endian

	for i, aByte := range aBytes {
		if i < 4 {
			assert.Equal(t, i+1, int(aByte), "serialized byte %d has the wrong value", i)
		} else {
			assert.Zero(t, aByte, "serialized byte %d has the wrong value", i)
		}
	}
}

func TestScalarParseErr(t *testing.T) {
	allOnes := bytes.Repeat([]byte{0xff}, ScalarBytesLen)

	_, err := ParseScalar(allOnes)
	assert.Error(t, err, "parsing an invalid serialized scalar should be an error")
}

func TestScalarSerializeParse(t *testing.T) {
	a := ScalarFromInt(123)
	aSerial := a.Serialize()
	aSerialParse, err := ParseScalar(aSerial)
	require.NoError(t, err, "parsing a serialized scalar should not cause an error")

	assert.True(t, a.Equals(aSerialParse), "parsing a serialized scalar should produce the original")
}

func TestScalarJSONMarshalUnmarshal(t *testing.T) {
	a := ScalarFromInt(123)
	aJSON, err := a.MarshalJSON()
	require.NoError(t, err, "marshaling scalar to JSON should not return an error")

	var aJSONUnmarshal Scalar
	err = aJSONUnmarshal.UnmarshalJSON(aJSON)
	require.NoError(t, err, "unmarshaling JSON to a scalar should not return an error")

	assert.True(t, a.Equals(aJSONUnmarshal), "unmarshaling a marshaled scalar should produce the original")
}

func TestScalarPoint(t *testing.T) {}

func TestScalarNeg(t *testing.T) {
	a := ScalarFromInt(123)
	aNeg := a.Neg()
	aNegNeg := aNeg.Neg()

	assert.False(t, a.Equals(aNeg), "scalar should not equal its own negative")
	assert.True(t, a.Equals(aNegNeg), "negative negative scalar should equal the original")
}

func TestScalarNegZero(t *testing.T) {
	zero := ScalarFromInt(0)
	zeroNeg := zero.Neg()

	assert.True(t, zero.Equals(zeroNeg), "zero should equal negative zero")
}

func TestScalarNegEqualsZeroSub(t *testing.T) {
	zero := ScalarFromInt(0)
	a := ScalarFromInt(123)

	zeroA := zero.Sub(a)
	aNeg := a.Neg()

	assert.True(t, zeroA.Equals(aNeg), "negative should equal subtracting from zero")
}

func TestScalarAddZero(t *testing.T) {
	zero := ScalarFromInt(0)
	a := ScalarFromInt(123)

	aZero := a.Add(zero)
	zeroA := zero.Add(a)

	assert.True(t, a.Equals(aZero), "adding zero should not change the original scalar")
	assert.True(t, a.Equals(zeroA), "adding to zero should not change the original scalar")
}

func TestScalarAddNonZero(t *testing.T) {
	a := ScalarFromInt(123)
	b := ScalarFromInt(456)

	aB := a.Add(b)

	assert.False(t, a.Equals(aB), "adding non-zero should change the original scalar")
	assert.False(t, b.Equals(aB), "adding non-zero should change the original scalar")
}

func TestScalarAddCommutative(t *testing.T) {
	a := ScalarFromInt(123)
	b := ScalarFromInt(456)

	aB := a.Add(b)
	bA := b.Add(a)

	assert.True(t, aB.Equals(bA), "adding scalars should be commutative")
}

func TestScalarAddAssociative(t *testing.T) {
	a := ScalarFromInt(123)
	b := ScalarFromInt(456)
	c := ScalarFromInt(789)

	aBThenC := a.Add(b).Add(c)
	aThenBC := a.Add(b.Add(c))

	assert.True(t, aBThenC.Equals(aThenBC), "adding scalars should be associative")
}

func TestScalarSubIdentity(t *testing.T) {
	zero := ScalarFromInt(0)
	a := ScalarFromInt(123)

	aZero := a.Sub(zero)

	assert.True(t, a.Equals(aZero), "subtracting zero should not change the original scalar")
}

func TestScalarSubEqualsAddNeg(t *testing.T) {
	a := ScalarFromInt(123)
	b := ScalarFromInt(456)

	aSubB := a.Sub(b)
	aAddBNeg := a.Add(b.Neg())

	assert.True(t, aAddBNeg.Equals(aSubB), "subtracting should be adding the negative")
}

func TestScalarMulOne(t *testing.T) {
	one := ScalarFromInt(1)
	a := ScalarFromInt(123)

	aOne := a.Mul(one)
	oneA := one.Mul(a)

	assert.True(t, a.Equals(aOne), "multiplying by one should not change the original")
	assert.True(t, a.Equals(oneA), "multiplying one should not change the original")
}

func TestScalarMulCommutative(t *testing.T) {
	a := ScalarFromInt(123)
	b := ScalarFromInt(456)

	aB := a.Mul(b)
	bA := b.Mul(a)

	assert.True(t, aB.Equals(bA), "multiplication should be commutative")
}

func TestScalarMulAssociative(t *testing.T) {
	a := ScalarFromInt(123)
	b := ScalarFromInt(456)
	c := ScalarFromInt(789)

	aBThenC := a.Mul(b).Mul(c)
	aThenBC := a.Mul(b.Mul(c))

	assert.True(t, aBThenC.Equals(aThenBC), "multiplication should be associative")
}

func TestScalarInvZero(t *testing.T) {}

func TestScalarInvOne(t *testing.T) {
	one := ScalarFromInt(1)
	oneInv := one.InvNonConst()

	assert.True(t, one.Equals(oneInv), "the inverse of one should be one")
}

func TestScalarInvMul(t *testing.T) {
	one := ScalarFromInt(1)
	a := ScalarFromInt(123)
	aInv := a.InvNonConst()

	aAInv := a.Mul(aInv)
	aInvA := aInv.Mul(a)

	assert.True(t, one.Equals(aAInv), "a scalar and its inverse should multiply to get one")
	assert.True(t, one.Equals(aInvA), "a scalar and its inverse should multiply to get one")
}

func TestScalarNegNil(t *testing.T) {
	var nilScalar *Scalar
	assert.Panics(t, func() { nilScalar.SetNeg() })
}

func TestScalarAddNil(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var nilScalar *Scalar
		a := ScalarFromInt(123)
		assert.Panics(t, func() { nilScalar.SetAdd(&a) })
	})

	t.Run("nil argument", func(t *testing.T) {
		a := ScalarFromInt(123)
		assert.Panics(t, func() { a.SetAdd(nil) })
	})

	t.Run("nil receiver and argument", func(t *testing.T) {
		var nilScalar *Scalar
		assert.Panics(t, func() { nilScalar.SetAdd(nil) })
	})
}

func TestScalarMulNil(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var nilScalar *Scalar
		a := ScalarFromInt(123)
		assert.Panics(t, func() { nilScalar.SetMul(&a) })
	})

	t.Run("nil argument", func(t *testing.T) {
		a := ScalarFromInt(123)
		assert.Panics(t, func() { a.SetMul(nil) })
	})

	t.Run("nil receiver and argument", func(t *testing.T) {
		var nilScalar *Scalar
		assert.Panics(t, func() { nilScalar.SetMul(nil) })
	})
}

func TestScalarInv(t *testing.T) {
	var nilScalar *Scalar
	assert.Panics(t, func() { nilScalar.SetInvNonConst() })
}
