package keys

import (
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"

	"entgo.io/ent/schema/field"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// Public is an secp256k1 public key, with additional methods supporting its use as a DB field.
type Public struct {
	key secp256k1.PublicKey
}

// ParsePublicKey parses an secp256k1 public key encoded according to the format specified by ANSI X9.62-1998.
// For more information, see secp256k1.ParsePubKey.
func ParsePublicKey(bytes []byte) (Public, error) {
	key, err := secp256k1.ParsePubKey(bytes)
	if err != nil {
		return Public{}, err
	}
	return Public{key: *key}, nil
}

// ParsePublicKeyMap creates secp256k1 public keys from a map of byte slices.
func ParsePublicKeyMap[k comparable](asBytes map[k][]byte) (map[k]Public, error) {
	asKeys := make(map[k]Public, len(asBytes))
	for id, bytes := range asBytes {
		key, err := ParsePublicKey(bytes)
		if err != nil {
			return nil, err
		}
		asKeys[id] = key
	}
	return asKeys, nil
}

// publicKeyFromInts creates an secp256k1 public key from x and y big integers. x and y must not be nil, and must be
// on the secp256k1 curve.
func publicKeyFromInts(x, y *big.Int) Public {
	xFieldVal := secp256k1.FieldVal{}
	xFieldVal.SetByteSlice(x.Bytes())
	yFieldVal := secp256k1.FieldVal{}
	yFieldVal.SetByteSlice(y.Bytes())

	// Normalize both values, because the cost is low, and having non-normalized values will break everything.
	// See the comments on FieldVal for more information.
	xFieldVal.Normalize()
	yFieldVal.Normalize()
	return Public{key: *secp256k1.NewPublicKey(&xFieldVal, &yFieldVal)}
}

// PublicKeyFromKey creates a Public from an [secp256k1.PublicKey].
func PublicKeyFromKey(key secp256k1.PublicKey) Public {
	return Public{key: key}
}

// Add returns the sum of p and b using field addition.
func (p Public) Add(b Public) Public {
	curve := secp256k1.S256()
	sumX, sumY := curve.Add(p.key.X(), p.key.Y(), b.key.X(), b.key.Y())
	return publicKeyFromInts(sumX, sumY)
}

// AddTweak applies a tweak to a public key. The result key is pubkey + tweak * G.
func (p Public) AddTweak(tweak Private) Public {
	return p.Add(tweak.Public())
}

// Sub subtracts b from p using field subtraction.
func (p Public) Sub(b Public) Public {
	negBY := new(big.Int).Sub(secp256k1.S256().P, b.key.Y())
	negB := publicKeyFromInts(b.key.X(), negBY)
	return p.Add(negB)
}

// ToBTCEC converts this [Public] into a [*secp256k1.PublicKey].
func (p Public) ToBTCEC() *secp256k1.PublicKey {
	return &p.key
}

// Equals returns true if p and other represent equivalent public keys, and false otherwise.
func (p Public) Equals(other Public) bool {
	return p.key.IsEqual(&other.key)
}

// IsZero returns true if p is the empty key.
func (p Public) IsZero() bool {
	return p == Public{}
}

// ToHex returns the key as a hex-encoded, 256-bit big-endian binary number.
func (p Public) ToHex() string {
	return hex.EncodeToString(p.Serialize())
}

// String returns the key as a hex-encoded, 256-bit big-endian binary number. It's equivalent to ToHex, but implements fmt.Stringer.
func (p Public) String() string {
	return p.ToHex()
}

// SumPublicKeys sums a list of secp256k1 public keys using group addition. It errors if the list is empty.
func SumPublicKeys(keys []Public) (Public, error) {
	if len(keys) == 0 {
		return Public{}, fmt.Errorf("no keys to add")
	}
	if len(keys) == 1 {
		return keys[0], nil
	}

	sum := keys[0].Add(keys[1])
	for _, key := range keys[2:] {
		sum = sum.Add(key)
	}

	return sum, nil
}

// Serialize serializes this key into the 33-byte compressed format. It is equivalent to [secp256k1.PublicKey.SerializeCompressed].
func (p Public) Serialize() []byte {
	if p.key == (secp256k1.PublicKey{}) {
		return nil
	}
	return p.key.SerializeCompressed()
}

// Value implements the [field.ValueScanner] interface.
func (p Public) Value() (driver.Value, error) {
	return p.Serialize(), nil
}

var _ field.ValueScanner = &Public{}

// Scan implements the [field.ValueScanner] interface. It fills the receiver, so must have a pointer receiver.
func (p *Public) Scan(src any) error {
	p.key = secp256k1.PublicKey{}
	value, err := getValue(src)
	if err != nil {
		return err
	}
	if value == nil {
		return nil
	}

	asBytes := make([]byte, len(value))
	copy(asBytes, value)
	pubKey, err := secp256k1.ParsePubKey(asBytes)
	if err != nil {
		return err
	}
	p.key = *pubKey
	return nil
}

// MarshalJSON implements json.Marshaler interface.
func (p Public) MarshalJSON() ([]byte, error) {
	if (p.key == secp256k1.PublicKey{}) {
		return json.Marshal(nil)
	}
	return json.Marshal(p.Serialize())
}

// UnmarshalJSON implements json.Unmarshaler interface.
func (p *Public) UnmarshalJSON(data []byte) error {
	var bytes []byte
	if err := json.Unmarshal(data, &bytes); err != nil {
		return err
	}

	key, err := ParsePublicKey(bytes)
	if err != nil {
		return err
	}
	p.key = key.key
	return nil
}

// LogValue makes Public loggable with [slog].
func (p Public) LogValue() slog.Value {
	return slog.AnyValue(p.String())
}

type key interface {
	Private | Public
	Serialize() []byte
}

// ToBytesMap converts a map[k]v, where v is a Public or a Private, to a map[k][]byte, using each type's Serialize.
func ToBytesMap[k comparable, v key](m map[k]v) map[k][]byte {
	if len(m) == 0 {
		return nil
	}
	out := make(map[k][]byte, len(m))
	for k, v := range m {
		out[k] = v.Serialize()
	}
	return out
}
