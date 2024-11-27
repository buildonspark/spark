package objects

import (
	"fmt"

	pb "github.com/lightsparkdev/spark-go/proto"
)

// SigningNonce is the private part of a signing nonce.
type SigningNonce struct {
	// Binding is the binding part of the nonce. 32 bytes.
	Binding []byte
	// Hiding is the hiding part of the nonce. 32 bytes.
	Hiding []byte
}

// NewSigningNonce creates a new SigningNonce from the given binding and hiding values.
func NewSigningNonce(binding, hiding []byte) (*SigningNonce, error) {
	if len(binding) != 32 || len(hiding) != 32 {
		return nil, fmt.Errorf("invalid nonce length")
	}
	return &SigningNonce{Binding: binding, Hiding: hiding}, nil
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
func (n SigningNonce) MarshalProto() (*pb.SigningNonce, error) {
	return &pb.SigningNonce{
		Binding: n.Binding,
		Hiding:  n.Hiding,
	}, nil
}

// UnmarshalProto deserializes the SigningNonce from a proto.SigningNonce.
func (n *SigningNonce) UnmarshalProto(proto *pb.SigningNonce) error {
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

func (n *SigningCommitment) Key() [66]byte {
	return [66]byte(n.MarshalBinary())
}

// MarshalProto serializes the SigningCommitment into a proto.SigningCommitment.
func (n SigningCommitment) MarshalProto() (*pb.SigningCommitment, error) {
	return &pb.SigningCommitment{
		Binding: n.Binding,
		Hiding:  n.Hiding,
	}, nil
}

// UnmarshalProto deserializes the SigningCommitment from a proto.SigningCommitment.
func (n *SigningCommitment) UnmarshalProto(proto *pb.SigningCommitment) error {
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
