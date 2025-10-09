package frost

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
)

// SigningCommitment is the public part of a [SigningNonce]. It is the public keys of the binding and hiding parts
// of the nonce. Except as specified, all methods are thread-safe.
type SigningCommitment struct {
	// binding is the public key of the binding part of the nonce.
	binding keys.Public
	// hiding is the public key of the hiding part of the nonce.
	hiding keys.Public
}

// NewSigningCommitment creates a new SigningCommitment from the given binding and hiding values.
func NewSigningCommitment(binding, hiding keys.Public) (SigningCommitment, error) {
	if binding.IsZero() {
		return SigningCommitment{}, errors.New("binding must not be zero")
	}
	if hiding.IsZero() {
		return SigningCommitment{}, errors.New("hiding must not be zero")
	}
	return SigningCommitment{binding: binding, hiding: hiding}, nil
}

var _ field.ValueScanner = &SigningCommitment{}

// Value implements the [field.ValueScanner] interface.
func (s SigningCommitment) Value() (driver.Value, error) {
	return s.MarshalBinary(), nil
}

// Scan implements the [field.ValueScanner] interface. It fills the receiver, so must have a pointer receiver.
func (s *SigningCommitment) Scan(src any) error {
	value, err := getValue(src)
	if err != nil {
		return err
	}
	if value == nil {
		return nil
	}

	asBytes := make([]byte, len(value))
	copy(asBytes, value)

	if err := s.UnmarshalBinary(asBytes); err != nil {
		return fmt.Errorf("failed to scan SigningCommitment: %w", err)
	}
	return nil
}

// MarshalBinary serializes the SigningCommitment into a 66-byte slice.
func (s SigningCommitment) MarshalBinary() []byte {
	return append(s.binding.Serialize(), s.hiding.Serialize()...)
}

// UnmarshalBinary deserializes the SigningCommitment from a byte slice.
func (s *SigningCommitment) UnmarshalBinary(data []byte) error {
	if len(data) != 66 {
		return fmt.Errorf("invalid nonce commitment length %d", len(data))
	}
	return s.unmarshalFromBytes(data[:33], data[33:])
}

// MarshalProto serializes the SigningCommitment into a proto.SigningCommitment. It never returns an error.
// It's needed to implement [github.com/lightsparkdev/spark/common.ProtoConvertable].
func (s SigningCommitment) MarshalProto() (*pbcommon.SigningCommitment, error) {
	return &pbcommon.SigningCommitment{
		Binding: s.binding.Serialize(),
		Hiding:  s.hiding.Serialize(),
	}, nil
}

// UnmarshalProto deserializes the SigningCommitment from a proto.SigningCommitment.
// It's needed to implement [github.com/lightsparkdev/spark/common.ProtoConvertable].
func (s *SigningCommitment) UnmarshalProto(proto *pbcommon.SigningCommitment) error {
	if proto == nil {
		return errors.New("cannot unmarshal signing commitment: nil proto")
	}
	return s.unmarshalFromBytes(proto.Binding, proto.Hiding)
}

func (s *SigningCommitment) unmarshalFromBytes(bindingBytes, hidingBytes []byte) error {
	binding, err := keys.ParsePublicKey(bindingBytes)
	if err != nil {
		return fmt.Errorf("invalid signing commitment binding: %w", err)
	}
	hiding, err := keys.ParsePublicKey(hidingBytes)
	if err != nil {
		return fmt.Errorf("invalid signing commitment hiding: %w", err)
	}
	s.binding = binding
	s.hiding = hiding
	return nil
}
