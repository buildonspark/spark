package so

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/lightsparkdev/spark-go/so/utils"
)

// SigningOperator is the information about a signing operator.
type SigningOperator struct {
	// ID is the index of the signing operator.
	ID uint64
	// Identifier is the identifier of the signing operator, which will be index + 1 in 32 bytes big endian hex string.
	// Used as shamir secret share identifier in DKG key shares.
	Identifier string
	// Address is the address of the signing operator.
	Address string
	// IdentityPublicKey is the identity public key of the signing operator.
	IdentityPublicKey []byte
}

// jsonSigningOperator is used for JSON unmarshaling
type jsonSigningOperator struct {
	ID                uint64 `json:"id"`
	Address           string `json:"address"`
	IdentityPublicKey string `json:"identity_public_key"`
}

// UnmarshalJSON implements json.Unmarshaler interface
func (s *SigningOperator) UnmarshalJSON(data []byte) error {
	var js jsonSigningOperator
	if err := json.Unmarshal(data, &js); err != nil {
		return err
	}

	// Decode hex string to bytes
	pubKey, err := hex.DecodeString(js.IdentityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to decode public key hex: %w", err)
	}

	s.Identifier = utils.IndexToIdentifier(js.ID)
	s.Address = js.Address
	s.IdentityPublicKey = pubKey
	return nil
}
