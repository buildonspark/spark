package frost

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/common/keys"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
)

// SigningNonce is the private part of a signing nonce. Except as specified, all methods are thread-safe.
type SigningNonce struct {
	// binding is the binding part of the nonce. 32 bytes.
	binding keys.Private
	// hiding is the hiding part of the nonce. 32 bytes.
	hiding keys.Private
}

// GenerateSigningNonce generates a random signing nonce using a CSPRNG.
func GenerateSigningNonce() SigningNonce {
	binding := keys.GeneratePrivateKey()
	hiding := keys.GeneratePrivateKey()
	return SigningNonce{binding: binding, hiding: hiding}
}

// NewSigningNonce creates a new SigningNonce from the given binding and hiding values.
// It returns an error if either is an empty key.
func NewSigningNonce(binding, hiding keys.Private) (SigningNonce, error) {
	if binding.IsZero() {
		return SigningNonce{}, errors.New("binding is zero")
	}
	if hiding.IsZero() {
		return SigningNonce{}, errors.New("hiding is zero")
	}
	return SigningNonce{binding: binding, hiding: hiding}, nil
}

// SigningCommitment returns the [SigningCommitment] for this nonce.
func (s *SigningNonce) SigningCommitment() SigningCommitment {
	return SigningCommitment{binding: s.binding.Public(), hiding: s.hiding.Public()}
}

var _ field.ValueScanner = &SigningCommitment{}

// Value implements the [field.ValueScanner] interface.
func (s SigningNonce) Value() (driver.Value, error) {
	return s.MarshalBinary(), nil
}

// Scan implements the [field.ValueScanner] interface. It fills the receiver, so must have a pointer receiver.
// Not thread-safe.
func (s *SigningNonce) Scan(src any) error {
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
		return fmt.Errorf("failed to scan SigningNonce: %w", err)
	}
	return nil
}

// MarshalBinary serializes the SigningNonce into a byte slice.
// Returns a 64-byte slice containing the concatenated binding and hiding values.
func (s SigningNonce) MarshalBinary() []byte {
	return append(s.binding.Serialize(), s.hiding.Serialize()...)
}

// UnmarshalBinary deserializes the SigningNonce from a byte slice.
// Not thread-safe.
func (s *SigningNonce) UnmarshalBinary(data []byte) error {
	if len(data) != 64 {
		return fmt.Errorf("invalid nonce length %d", len(data))
	}
	return s.unmarshalFromBytes(data[:32], data[32:])
}

// MarshalProto serializes the SigningNonce into a [pbfrost.SigningNonce]. It never returns an error.
// It's needed to implement [github.com/lightsparkdev/spark/common.ProtoConvertable].
func (s SigningNonce) MarshalProto() (*pbfrost.SigningNonce, error) {
	return &pbfrost.SigningNonce{
		Binding: s.binding.Serialize(),
		Hiding:  s.hiding.Serialize(),
	}, nil
}

// UnmarshalProto deserializes the SigningNonce from a proto.SigningNonce.
// It's needed to implement [github.com/lightsparkdev/spark/common.ProtoConvertable].
// Not thread-safe.
func (s *SigningNonce) UnmarshalProto(proto *pbfrost.SigningNonce) error {
	if proto == nil {
		return errors.New("cannot unmarshal signing nonce: nil proto")
	}
	return s.unmarshalFromBytes(proto.Binding, proto.Hiding)
}

func (s *SigningNonce) unmarshalFromBytes(bindingBytes, hidingBytes []byte) error {
	binding, err := keys.ParsePrivateKey(bindingBytes)
	if err != nil {
		return fmt.Errorf("invalid signing nonce binding: %w", err)
	}
	hiding, err := keys.ParsePrivateKey(hidingBytes)
	if err != nil {
		return fmt.Errorf("invalid signing nonce hiding: %w", err)
	}
	s.binding = binding
	s.hiding = hiding
	return nil
}

func getValue(src any) ([]byte, error) {
	switch v := src.(type) {
	case nil:
		return nil, nil
	case *sql.Null[[]byte]:
		if v == nil || !v.Valid { // It can be a nil pointer to a Null, or just a null Null.
			return nil, nil
		}
		return v.V, nil
	case []byte:
		return v, nil
	default:
		return nil, fmt.Errorf("unexpected input for Scan: %T", src)
	}
}
