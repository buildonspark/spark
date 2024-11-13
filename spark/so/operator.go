package so

import (
	"encoding/json"

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
	IdentityPublicKey []byte `json:"identity_public_key"`
}

// UnmarshalJSON implements json.Unmarshaler interface
func (s *SigningOperator) UnmarshalJSON(data []byte) error {
	var js jsonSigningOperator
	if err := json.Unmarshal(data, &js); err != nil {
		return err
	}

	s.Identifier = utils.IndexToIdentifier(js.ID)
	s.Address = js.Address
	s.IdentityPublicKey = js.IdentityPublicKey
	return nil
}
