package curve

import (
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// Scalar is a secp256k1 scalar.
type Scalar struct {
	// Make struct not comparable. Otherwise the operator == is available,
	// which does not run in constant time and so can leak the scalar.
	_ [0]func()

	scalar secp256k1.ModNScalar
}

// ScalarBytesLen is the number of bytes in a serialized scalar.
const ScalarBytesLen = 32

// ScalarFromInt creates a scalar that encodes the passed integer.
func ScalarFromInt(n uint32) Scalar {
	var s Scalar
	s.scalar.SetInt(n)
	return s
}

// generateScalar generates and returns a new scalar using the provided reader
// as a source of entropy.
func generateScalar(reader io.Reader) (Scalar, error) {
	key, err := secp256k1.GeneratePrivateKeyFromRand(reader)
	if err != nil {
		return Scalar{}, err
	}

	s := Scalar{
		scalar: key.Key,
	}

	return s, nil
}

// GenerateScalar generates and returns a new cryptographically secure scalar.
func GenerateScalar() (Scalar, error) {
	return generateScalar(cryptorand.Reader)
}

// GenerateScalarFromRand generates and returns a new scalar using the provided reader
// as a source of entropy.
func GenerateScalarFromRand(reader io.Reader) (Scalar, error) {
	return generateScalar(reader)
}

// ParseScalar creates a scalar from a serialization.
func ParseScalar(serial []byte) (Scalar, error) {
	if len(serial) != ScalarBytesLen {
		return Scalar{}, fmt.Errorf("scalar must be %d bytes", ScalarBytesLen)
	}

	var scalar Scalar
	overflowed := scalar.scalar.SetByteSlice(serial)
	if overflowed {
		return Scalar{}, fmt.Errorf("scalar bytes should encode an integer less than the group order")
	}
	return scalar, nil
}

// Serialize creates a serialization of this scalar.
func (s Scalar) Serialize() []byte {
	serial := s.scalar.Bytes()
	return serial[:]
}

// Point returns the point that corresponds to this scalar times the secp256k1 base point.
func (s Scalar) Point() Point {
	var p Point
	secp256k1.ScalarBaseMultNonConst(&s.scalar, &p.point)
	return p
}

// Neg returns a copy of this scalar negated.
func (s Scalar) Neg() Scalar {
	return *s.SetNeg()
}

// SetNeg negates this scalar. This scalar is modified.
//
// This scalar is returned to allow method chaining.
func (s *Scalar) SetNeg() *Scalar {
	s.scalar.Negate()
	return s
}

// Add returns a copy of this scalar with the passed scalar added to it.
func (s Scalar) Add(t Scalar) Scalar {
	return *s.SetAdd(&t)
}

// SetAdd adds the passed scalar to this scalar. This scalar is modified.
//
// This scalar is returned to allow method chaining.
func (s *Scalar) SetAdd(t *Scalar) *Scalar {
	s.scalar.Add(&t.scalar)
	return s
}

// Sub returns a copy of this scalar with the passed scalar subtracted from it.
func (s Scalar) Sub(t Scalar) Scalar {
	return *s.SetSub(&t)
}

// SetSub subtracts the passed scalar from this scalar. This scalar is modified.
//
// This scalar is returned to allow method chaining.
func (s *Scalar) SetSub(t *Scalar) *Scalar {
	// Use the fact that s - t = -(-s + t) to not modify t.
	s.SetNeg().SetAdd(t).SetNeg()
	return s
}

// Mul returns a copy of this scalar multiplied by the passed scalar.
func (s Scalar) Mul(t Scalar) Scalar {
	return *s.SetMul(&t)
}

// SetMul multplies this scalar by the passed scalar. This scalar is modified.
//
// This scalar is returned to allow method chaining.
func (s *Scalar) SetMul(t *Scalar) *Scalar {
	s.scalar.Mul(&t.scalar)
	return s
}

// InvNonConst returns the multiplicative inverse of this scalar in non-constant time.
func (s Scalar) InvNonConst() Scalar {
	return *s.SetInvNonConst()
}

// SetInvNonConst finds the multiplicative inverse of this scalar in non-constant time.
// This scalar is modified.
//
// This scalar is returned to allow method chaining.
func (s *Scalar) SetInvNonConst() *Scalar {
	// TODO: Return an error if s = zero?

	s.scalar.InverseNonConst()
	return s
}

// Equals returns true if this and the passed point represent the same scalar and false otherwise.
func (s Scalar) Equals(t Scalar) bool {
	return s.scalar.Equals(&t.scalar)
}

func (s Scalar) String() string {
	return s.scalar.String()
}

// MarshalJSON implements json.Marshaler.
func (s Scalar) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.Serialize())
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *Scalar) UnmarshalJSON(jsonData []byte) error {
	scalarSerial := make([]byte, ScalarBytesLen)
	if err := json.Unmarshal(jsonData, &scalarSerial); err != nil {
		return fmt.Errorf("failed to unmarshal scalar: %w", err)
	}

	scalar, err := ParseScalar(scalarSerial)
	if err != nil {
		return fmt.Errorf("failed to parse unmarshaled scalar: %w", err)
	}
	s.scalar = scalar.scalar

	return nil
}
