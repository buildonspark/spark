package objects

import (
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
)

// SigningNonce is the private part of a signing nonce.
type SigningNonce struct {
	// Binding is the binding part of the nonce. 32 bytes.
	Binding []byte
	// Hiding is the hiding part of the nonce. 32 bytes.
	Hiding []byte
}

// RandomSigningNonce generates a random signing nonce.
func RandomSigningNonce() (*SigningNonce, error) {
	binding, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	hiding, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	return NewSigningNonce(binding.Serialize(), hiding.Serialize())
}

// NewSigningNonce creates a new SigningNonce from the given binding and hiding values.
func NewSigningNonce(binding, hiding []byte) (*SigningNonce, error) {
	if len(binding) != 32 || len(hiding) != 32 {
		return nil, fmt.Errorf("invalid nonce length")
	}
	return &SigningNonce{Binding: binding, Hiding: hiding}, nil
}

// SigningCommitment returns the signing commitment for the nonce.
func (n *SigningNonce) SigningCommitment() *SigningCommitment {
	bindingPubKey := secp256k1.PrivKeyFromBytes(n.Binding).PubKey()
	hidingPubKey := secp256k1.PrivKeyFromBytes(n.Hiding).PubKey()
	return &SigningCommitment{Binding: bindingPubKey.SerializeCompressed(), Hiding: hidingPubKey.SerializeCompressed()}
}

// MarshalBinary serializes the SigningNonce into a byte slice.
// Returns a 64-byte slice containing the concatenated binding and hiding values.
func (n SigningNonce) MarshalBinary() ([]byte, error) {
	bytes := make([]byte, 64)
	copy(bytes[0:32], n.Binding)
	copy(bytes[32:64], n.Hiding)
	return bytes, nil
}

// UnmarshalBinary deserializes the SigningNonce from a byte slice.
func (n *SigningNonce) UnmarshalBinary(data []byte) error {
	if len(data) != 64 {
		return fmt.Errorf("invalid nonce length")
	}
	n.Binding = data[0:32]
	n.Hiding = data[32:64]
	return nil
}

// MarshalProto serializes the SigningNonce into a proto.SigningNonce.
func (n SigningNonce) MarshalProto() (*pbfrost.SigningNonce, error) {
	return &pbfrost.SigningNonce{
		Binding: n.Binding,
		Hiding:  n.Hiding,
	}, nil
}

// UnmarshalProto deserializes the SigningNonce from a proto.SigningNonce.
func (n *SigningNonce) UnmarshalProto(proto *pbfrost.SigningNonce) error {
	if proto == nil {
		return fmt.Errorf("nil proto")
	}

	if len(proto.Binding) != 32 || len(proto.Hiding) != 32 {
		return fmt.Errorf("invalid nonce length")
	}

	n.Binding = proto.Binding
	n.Hiding = proto.Hiding
	return nil
}

// SigningCommitment is the public part of a signing nonce.
// It is the public key of the binding and hiding parts of the nonce.
type SigningCommitment struct {
	// Binding is the public key of the binding part of the nonce. 33 bytes.
	Binding []byte
	// Hiding is the public key of the hiding part of the nonce. 33 bytes.
	Hiding []byte
}

// NewSigningCommitment creates a new SigningCommitment from the given binding and hiding values.
func NewSigningCommitment(binding, hiding []byte) (*SigningCommitment, error) {
	if len(binding) != 33 || len(hiding) != 33 {
		return nil, fmt.Errorf("invalid nonce commitment length")
	}
	return &SigningCommitment{Binding: binding, Hiding: hiding}, nil
}

// MarshalBinary serializes the SigningCommitment into a byte slice.
func (n SigningCommitment) MarshalBinary() []byte {
	bytes := make([]byte, 66)
	copy(bytes[0:33], n.Binding)
	copy(bytes[33:66], n.Hiding)
	return bytes
}

// UnmarshalBinary deserializes the SigningCommitment from a byte slice.
func (n *SigningCommitment) UnmarshalBinary(data []byte) error {
	if len(data) != 66 {
		return fmt.Errorf("invalid nonce commitment length")
	}
	n.Binding = data[0:33]
	n.Hiding = data[33:66]
	return nil
}

// Key returns the key for a map for the SigningCommitment.
func (n *SigningCommitment) Key() [66]byte {
	return [66]byte(n.MarshalBinary())
}

// MarshalProto serializes the SigningCommitment into a proto.SigningCommitment.
func (n SigningCommitment) MarshalProto() (*pbcommon.SigningCommitment, error) {
	return &pbcommon.SigningCommitment{
		Binding: n.Binding,
		Hiding:  n.Hiding,
	}, nil
}

// UnmarshalProto deserializes the SigningCommitment from a proto.SigningCommitment.
func (n *SigningCommitment) UnmarshalProto(proto *pbcommon.SigningCommitment) error {
	if proto == nil {
		return fmt.Errorf("nil proto")
	}

	if len(proto.Binding) != 33 || len(proto.Hiding) != 33 {
		return fmt.Errorf("invalid nonce commitment length")
	}

	n.Binding = proto.Binding
	n.Hiding = proto.Hiding
	return nil
}
