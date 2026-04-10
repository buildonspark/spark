package jwt

import (
	"crypto/ecdsa"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/common/keys"
)

// KeyType identifies the elliptic curve of a [Public].
type KeyType byte

const (
	// KeyTypeUnset identifies the zero value. It is never actually serialized.
	KeyTypeUnset KeyType = 0x00
	// KeyTypeSecp256k1 identifies a secp256k1 public key (used with ES256K JWTs).
	KeyTypeSecp256k1 KeyType = 0x01
	// KeyTypeP256 identifies a P-256 public key (used with ES256 JWTs).
	KeyTypeP256 KeyType = 0x02
)

// Public is a sum type over [keys.Public] (secp256k1) and [keys.P256Public] (P-256), used for JWT
// verification. It implements [field.ValueScanner] for use as an Ent field type.
//
// Exactly one of secp256k1 or p256 is set for non-zero values.
//
// Serialization format: 1-byte curve discriminator + 33-byte compressed key = 34 bytes total.
type Public struct {
	secp256k1 keys.Public
	p256      keys.P256Public
}

// PublicFromSecp256k1 wraps a [keys.Public] (secp256k1) key as a [Public].
func PublicFromSecp256k1(key keys.Public) Public {
	return Public{secp256k1: key}
}

// PublicFromP256 wraps a [keys.P256Public] key as a [Public].
func PublicFromP256(key keys.P256Public) Public {
	return Public{p256: key}
}

// ParsePublicHex parses a hex-encoded Public (34 bytes: 1-byte curve discriminator + 33-byte compressed key).
func ParsePublicHex(s string) (Public, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return Public{}, fmt.Errorf("invalid hex: %w", err)
	}
	var p Public
	if err := p.Scan(b); err != nil {
		return Public{}, fmt.Errorf("ParsePublicHex: %w", err)
	}
	return p, nil
}

// MustParsePublicHex parses a hex-encoded Public (34 bytes: 1-byte curve discriminator +
// 33-byte compressed key). Panics on error. Meant for use in tests and static initialization.
func MustParsePublicHex(s string) Public {
	key, err := ParsePublicHex(s)
	if err != nil {
		panic(fmt.Sprintf("MustParsePublicHex: %v", err))
	}
	return key
}

// KeyType returns the curve type of this key.
func (p Public) KeyType() KeyType {
	switch {
	case !p.secp256k1.IsZero():
		return KeyTypeSecp256k1
	case !p.p256.IsZero():
		return KeyTypeP256
	default:
		return KeyTypeUnset
	}
}

// Secp256k1 returns the underlying [keys.Public] key, or a zero value if this is not a secp256k1 key.
func (p Public) Secp256k1() keys.Public {
	return p.secp256k1
}

// P256 returns the underlying [keys.P256Public] key, or a zero value if this is not a P256 key.
func (p Public) P256() keys.P256Public {
	return p.p256
}

// ToECDSA converts the key to an [*ecdsa.PublicKey], dispatching to the underlying key type.
// Returns nil for the zero value.
func (p Public) ToECDSA() *ecdsa.PublicKey {
	switch p.KeyType() {
	case KeyTypeSecp256k1:
		return p.secp256k1.ToBTCEC().ToECDSA()
	case KeyTypeP256:
		return p.p256.ToECDSA()
	default:
		return nil
	}
}

// IsZero reports whether this key is the zero value (unset).
func (p Public) IsZero() bool {
	return p == Public{}
}

// Equals returns true if k and other represent the same key type and value.
func (p Public) Equals(other Public) bool {
	return p.secp256k1.Equals(other.secp256k1) && p.p256.Equals(other.p256)
}

// Serialize serializes the key as 34 bytes: 1-byte curve discriminator + 33-byte compressed key.
func (p Public) Serialize() []byte {
	var inner []byte
	var discriminator byte

	switch p.KeyType() {
	case KeyTypeUnset:
		return nil
	case KeyTypeSecp256k1:
		discriminator = byte(KeyTypeSecp256k1)
		inner = p.secp256k1.Serialize()
	case KeyTypeP256:
		discriminator = byte(KeyTypeP256)
		inner = p.p256.Serialize()
	}
	out := make([]byte, 1+len(inner))
	out[0] = discriminator
	copy(out[1:], inner)
	return out
}

// ToHex returns the key as a hex-encoded serialized representation.
func (p Public) ToHex() string {
	return hex.EncodeToString(p.Serialize())
}

// String returns the key as a hex-encoded serialized representation. Implements [fmt.Stringer].
func (p Public) String() string {
	return p.ToHex()
}

// Value implements the [field.ValueScanner] interface.
func (p Public) Value() (driver.Value, error) {
	return p.Serialize(), nil
}

var _ field.ValueScanner = &Public{}

// Scan implements the [field.ValueScanner] interface.
func (p *Public) Scan(src any) error {
	*p = Public{}
	b, err := getValue(src)
	if err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	if len(b) != 34 {
		return fmt.Errorf("jwt_public_key: expected 34 bytes (1 curve discriminator + 33 compressed key), got %d", len(b))
	}
	keyBytes := b[1:]
	switch KeyType(b[0]) {
	case KeyTypeSecp256k1:
		pub, err := keys.ParsePublicKey(keyBytes)
		if err != nil {
			return fmt.Errorf("invalid secp256k1 key: %w", err)
		}
		p.secp256k1 = pub
	case KeyTypeP256:
		pub, err := keys.ParseP256PublicKey(keyBytes)
		if err != nil {
			return fmt.Errorf("invalid P-256 key: %w", err)
		}
		p.p256 = pub
	default:
		return fmt.Errorf("jwt_public_key: unknown curve discriminator 0x%02x", b[0])
	}
	return nil
}

// MarshalJSON implements [json.Marshaler].
func (p Public) MarshalJSON() ([]byte, error) {
	if p.IsZero() {
		return json.Marshal(nil)
	}
	return json.Marshal(p.Serialize())
}

// UnmarshalJSON implements [json.Unmarshaler].
func (p *Public) UnmarshalJSON(data []byte) error {
	var b []byte
	if err := json.Unmarshal(data, &b); err != nil {
		return err
	}
	return p.Scan(b)
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
