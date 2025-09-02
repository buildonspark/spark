package keys

import (
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"

	"entgo.io/ent/schema/field"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// Private is an secp256k1 private key, with additional methods supporting its use as a DB field.
type Private struct {
	key secp256k1.PrivateKey
}

// GeneratePrivateKey securely generates an secp256k1 private key.
func GeneratePrivateKey() (Private, error) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return Private{}, err
	}
	return Private{key: *priv}, nil
}

// MustGeneratePrivateKeyFromRand generates an secp256k1 private key using reader.
// Meant for testing, it panics if the key cannot be generated.
func MustGeneratePrivateKeyFromRand(reader io.Reader) Private {
	priv, err := secp256k1.GeneratePrivateKeyFromRand(reader)
	if err != nil {
		panic(err)
	}
	return Private{key: *priv}
}

// ParsePrivateKey creates an secp256k1 private key from a byte slice. The byte slice must be 32 bytes.
// This is intended for use in deserialization, not for key generation. If you need to generate a key, use
// [GeneratePrivateKey] or, in tests, [MustGeneratePrivateKeyFromRand], since they properly handle generation of appropriate values.
func ParsePrivateKey(privKeyBytes []byte) (Private, error) {
	if len(privKeyBytes) != 32 {
		return Private{}, fmt.Errorf("private key must be 32 bytes")
	}
	pk := Private{key: *secp256k1.PrivKeyFromBytes(privKeyBytes)}
	if pk.key.Key.IsZero() {
		return Private{}, fmt.Errorf("private key must not be zero")
	}
	return pk, nil
}

// PrivateKeyFromBigInt creates an secp256k1 private key from a big integer.
func PrivateKeyFromBigInt(privKeyInt *big.Int) (Private, error) {
	if privKeyInt == nil || len(privKeyInt.Bits()) == 0 {
		return Private{}, fmt.Errorf("private key must not be zero")
	}
	if privKeyInt.BitLen() > 256 {
		return Private{}, fmt.Errorf("private key must not be represented by an Int larger than 32 bytes")
	}

	bytes := make([]byte, 32)
	privKeyInt.FillBytes(bytes)
	return Private{key: *secp256k1.PrivKeyFromBytes(bytes)}, nil
}

// PrivateKeyFromScalar creates an secp256k1 private key from a scalar.
func PrivateKeyFromScalar(scalar *secp256k1.ModNScalar) (Private, error) {
	if scalar == nil {
		return Private{}, fmt.Errorf("private key must not be nil")
	}
	pk := Private{key: *secp256k1.NewPrivateKey(scalar)}
	if pk.key.Key.IsZero() {
		return Private{}, fmt.Errorf("private key must not be zero")
	}
	return pk, nil
}

// PrivateFromKey creates an secp256k1 private key from an [secp256k1.PrivateKey].
func PrivateFromKey(key secp256k1.PrivateKey) Private {
	return Private{key: key}
}

// Public returns the public key corresponding to this private key.
func (p Private) Public() Public {
	return Public{key: *p.key.PubKey()}
}

// Add adds two private keys using field addition.
func (p Private) Add(b Private) Private {
	var sum secp256k1.ModNScalar
	sum.Add2(&p.key.Key, &b.key.Key)
	return Private{key: *secp256k1.NewPrivateKey(&sum)}
}

// Sub subtracts two private keys using field subtraction.
func (p Private) Sub(b Private) Private {
	var sum secp256k1.ModNScalar
	sum.Set(&b.key.Key).Negate().Add(&p.key.Key)
	return Private{key: *secp256k1.NewPrivateKey(&sum)}
}

// ToBTCEC converts this [Private] into a [secp256k1.PrivateKey].
func (p Private) ToBTCEC() *secp256k1.PrivateKey {
	return &p.key
}

// Value implements the [field.ValueScanner] interface.
func (p Private) Value() (driver.Value, error) {
	return p.Serialize(), nil
}

// Equals returns true if p and other represent the equivalent private keys, and false otherwise.
func (p Private) Equals(other Private) bool {
	return p.key.Key.Equals(&other.key.Key)
}

// IsZero returns true if p is the empty key.
func (p Private) IsZero() bool {
	return p.key.Key.IsZero()
}

// ToHex returns the key as a hex-encoded, 256-bit big-endian binary number.
func (p Private) ToHex() string {
	return hex.EncodeToString(p.Serialize())
}

// String returns the key as a hex-encoded, 256-bit big-endian binary number. It's equivalent to ToHex, but implements fmt.Stringer.
func (p Private) String() string {
	return p.ToHex()
}

// Serialize returns the key as a 256-bit big-endian binary-encoded number.
func (p Private) Serialize() []byte {
	if p.IsZero() {
		return nil
	}
	return p.key.Serialize()
}

var _ field.ValueScanner = &Private{}

// Scan implements the [field.ValueScanner] interface.
func (p *Private) Scan(src any) error {
	p.key = secp256k1.PrivateKey{}
	value, err := getValue(src)
	if err != nil {
		return err
	}
	if value == nil {
		return nil
	}
	asBytes := make([]byte, len(value))
	copy(asBytes, value)
	key, err := ParsePrivateKey(asBytes)
	if err != nil {
		return err
	}
	p.key = key.key
	return nil
}

// MarshalJSON implements json.Marshaler interface.
func (p Private) MarshalJSON() ([]byte, error) {
	if p.IsZero() {
		return json.Marshal(nil)
	}
	return json.Marshal(p.Serialize())
}

// UnmarshalJSON implements json.Unmarshaler interface.
func (p *Private) UnmarshalJSON(data []byte) error {
	var bytes []byte
	if err := json.Unmarshal(data, &bytes); err != nil {
		return err
	}

	key, err := ParsePrivateKey(bytes)
	if err != nil {
		return err
	}
	p.key = key.key
	return nil
}

func getValue(src any) ([]byte, error) {
	switch v := src.(type) {
	case *sql.Null[[]byte]:
		if !v.Valid { // It's NULL
			return nil, nil
		}
		return v.V, nil
	case []byte:
		return v, nil
	default:
		return nil, fmt.Errorf("unexpected input for Scan: %T", src)
	}
}
