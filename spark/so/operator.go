package so

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/lightsparkdev/spark-go/so/utils"
)

type SigningOperator struct {
	Identifier        string
	Address           string
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
