package keys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"entgo.io/ent/schema/field"
)

// JwtKeyType identifies the elliptic curve of a JwtPubKey.
type JwtKeyType byte

const (
	// JwtKeyTypeSecp256k1 identifies a secp256k1 public key (used with ES256K JWTs).
	JwtKeyTypeSecp256k1 JwtKeyType = 0x01
	// JwtKeyTypeP256 identifies a P-256 public key (used with ES256 JWTs).
	JwtKeyTypeP256 JwtKeyType = 0x02
)

// JwtPubKey is a public key used for JWT verification, supporting both secp256k1 (ES256K)
// and P-256 (ES256) curves. It implements [field.ValueScanner] for use as an Ent field type.
//
// Serialization format: 1-byte curve discriminator + 33-byte compressed key = 34 bytes total.
// This ensures the unique index reliably enforces one-key-per-partner across both curves and
// encoding variants.
type JwtPubKey struct {
	keyType JwtKeyType
	ecKey   *ecdsa.PublicKey
}

// MustParseJwtPubKeyHex parses a hex-encoded JwtPubKey (34 bytes: 1-byte curve discriminator +
// 33-byte compressed key). Panics on error. Meant for use in tests and static initialization.
func MustParseJwtPubKeyHex(s string) JwtPubKey {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(fmt.Errorf("MustParseJwtPubKeyHex: invalid hex: %w", err))
	}
	var k JwtPubKey
	if err := k.Scan(b); err != nil {
		panic(fmt.Errorf("MustParseJwtPubKeyHex: %w", err))
	}
	return k
}

// ParseJwtPubKeySecp256k1 parses a secp256k1 public key from compressed or uncompressed bytes.
func ParseJwtPubKeySecp256k1(b []byte) (JwtPubKey, error) {
	pub, err := ParsePublicKey(b)
	if err != nil {
		return JwtPubKey{}, fmt.Errorf("invalid secp256k1 key: %w", err)
	}
	return JwtPubKey{
		keyType: JwtKeyTypeSecp256k1,
		ecKey:   pub.ToBTCEC().ToECDSA(),
	}, nil
}

// ParseJwtPubKeyP256 parses a P-256 public key from compressed or uncompressed bytes.
func ParseJwtPubKeyP256(b []byte) (JwtPubKey, error) {
	// Try compressed form first (33 bytes, prefix 0x02/0x03).
	xInt, yInt := elliptic.UnmarshalCompressed(elliptic.P256(), b)
	if xInt == nil {
		// Fall back to uncompressed form (65 bytes, prefix 0x04).
		xInt, yInt = elliptic.Unmarshal(elliptic.P256(), b)
	}
	if xInt == nil {
		return JwtPubKey{}, fmt.Errorf("invalid P-256 key")
	}
	return JwtPubKey{
		keyType: JwtKeyTypeP256,
		ecKey:   &ecdsa.PublicKey{Curve: elliptic.P256(), X: xInt, Y: yInt},
	}, nil
}

// KeyType returns the curve type of this key.
func (k JwtPubKey) KeyType() JwtKeyType {
	return k.keyType
}

// ToECDSA returns the underlying [*ecdsa.PublicKey] for use in JWT verification.
func (k JwtPubKey) ToECDSA() *ecdsa.PublicKey {
	return k.ecKey
}

// IsZero reports whether this key is the zero value (unset).
func (k JwtPubKey) IsZero() bool {
	return k.ecKey == nil
}

// Serialize serializes the key as 34 bytes: 1-byte curve discriminator + 33-byte compressed key.
func (k JwtPubKey) Serialize() []byte {
	if k.IsZero() {
		return nil
	}
	compressed := elliptic.MarshalCompressed(k.ecKey.Curve, k.ecKey.X, k.ecKey.Y)
	out := make([]byte, 1+len(compressed))
	out[0] = byte(k.keyType)
	copy(out[1:], compressed)
	return out
}

// MarshalJSON implements [json.Marshaler].
func (k JwtPubKey) MarshalJSON() ([]byte, error) {
	if k.IsZero() {
		return json.Marshal(nil)
	}
	return json.Marshal(k.Serialize())
}

// UnmarshalJSON implements [json.Unmarshaler].
func (k *JwtPubKey) UnmarshalJSON(data []byte) error {
	var b []byte
	if err := json.Unmarshal(data, &b); err != nil {
		return err
	}
	return k.Scan(b)
}

// Value implements the [field.ValueScanner] interface.
func (k JwtPubKey) Value() (driver.Value, error) {
	return k.Serialize(), nil
}

var _ field.ValueScanner = &JwtPubKey{}

// Scan implements the [field.ValueScanner] interface.
func (k *JwtPubKey) Scan(src any) error {
	*k = JwtPubKey{}
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
	switch JwtKeyType(b[0]) {
	case JwtKeyTypeSecp256k1:
		parsed, err := ParseJwtPubKeySecp256k1(keyBytes)
		if err != nil {
			return err
		}
		*k = parsed
	case JwtKeyTypeP256:
		parsed, err := ParseJwtPubKeyP256(keyBytes)
		if err != nil {
			return err
		}
		*k = parsed
	default:
		return fmt.Errorf("jwt_public_key: unknown curve discriminator 0x%02x", b[0])
	}
	return nil
}
