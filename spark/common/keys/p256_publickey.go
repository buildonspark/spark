package keys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"entgo.io/ent/schema/field"
)

// P256Public is a NIST P-256 public key, with additional methods supporting its use as a DB field.
// It stores the 33-byte compressed form directly, so == compares by value.
type P256Public struct {
	b [33]byte
}

// GenerateP256PublicKey generates a P-256 key pair and returns the public key. The reader is not
// configurable because Go's P-256 implementation routes through BoringSSL and ignores caller-supplied entropy.
func GenerateP256PublicKey() P256Public {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		panic(fmt.Sprintf("this should be impossible: %v", err))
	}
	// P256 key from P256 generation cannot fail the curve check.
	k, err := P256PublicFromECDSA(&priv.PublicKey)
	if err != nil {
		panic(fmt.Sprintf("this should be impossible: %v", err))
	}
	return k
}

// P256PublicFromECDSA converts an [*ecdsa.PublicKey] on the P-256 curve into a [P256Public].
// Returns an error if the key is not on the P-256 curve.
func P256PublicFromECDSA(key *ecdsa.PublicKey) (P256Public, error) {
	if key.Curve != elliptic.P256() {
		return P256Public{}, fmt.Errorf("P256PublicFromECDSA: expected P-256 curve, got %s", key.Curve.Params().Name)
	}
	var k P256Public
	copy(k.b[:], elliptic.MarshalCompressed(elliptic.P256(), key.X, key.Y))
	return k, nil
}

// ParseP256PublicKey parses a P-256 public key from compressed (33 bytes) or uncompressed (65 bytes) form.
func ParseP256PublicKey(b []byte) (P256Public, error) {
	curve := elliptic.P256()

	switch len(b) {
	case 33:
		// Validate by decompressing.
		if x, _ := elliptic.UnmarshalCompressed(curve, b); x == nil {
			return P256Public{}, fmt.Errorf("malformed public key: invalid P-256 compressed public key")
		}
		var k P256Public
		copy(k.b[:], b)
		return k, nil
	case 65:
		key, err := ecdsa.ParseUncompressedPublicKey(curve, b)
		if err != nil {
			return P256Public{}, err
		}
		var k P256Public
		copy(k.b[:], elliptic.MarshalCompressed(curve, key.X, key.Y))
		return k, nil
	default:
		return P256Public{}, fmt.Errorf("malformed public key: invalid length: %d", len(b))
	}
}

// ParseP256PublicKeyHex parses a hex-encoded P-256 public key.
func ParseP256PublicKeyHex(s string) (P256Public, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return P256Public{}, err
	}
	return ParseP256PublicKey(b)
}

// MustParseP256PublicKeyHex parses a hex-encoded P-256 public key. Panics on error.
// Meant for use in tests and static initialization.
func MustParseP256PublicKeyHex(s string) P256Public {
	k, err := ParseP256PublicKeyHex(s)
	if err != nil {
		panic(err)
	}
	return k
}

// ToECDSA returns the key as an [*ecdsa.PublicKey] by decompressing the stored form.
// Returns nil for the zero value.
func (p P256Public) ToECDSA() *ecdsa.PublicKey {
	if p.IsZero() {
		return nil
	}
	curve := elliptic.P256()
	x, y := elliptic.UnmarshalCompressed(curve, p.b[:])
	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
}

// IsZero returns true if this public key is the empty key, and false otherwise.
func (p P256Public) IsZero() bool {
	return p == P256Public{}
}

// Equals returns true if p and other represent equivalent P-256 public keys.
func (p P256Public) Equals(other P256Public) bool {
	return p == other
}

// Serialize serializes the key as 33-byte compressed form. Returns nil for the zero value.
func (p P256Public) Serialize() []byte {
	if p.IsZero() {
		return nil
	}
	out := make([]byte, 33)
	copy(out, p.b[:])
	return out
}

// ToHex returns the key as a hex-encoded compressed representation.
func (p P256Public) ToHex() string {
	return hex.EncodeToString(p.Serialize())
}

// String returns the key as a hex-encoded compressed representation. Implements [fmt.Stringer].
func (p P256Public) String() string {
	return p.ToHex()
}

// Value implements the [field.ValueScanner] interface.
func (p P256Public) Value() (driver.Value, error) {
	return p.Serialize(), nil
}

var _ field.ValueScanner = &P256Public{}

// Scan implements the [field.ValueScanner] interface.
func (p *P256Public) Scan(src any) error {
	*p = P256Public{}
	b, err := getValue(src)
	if err != nil {
		return err
	}
	if b == nil {
		return nil
	}

	parsed, err := ParseP256PublicKey(b)
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}

// MarshalJSON implements [json.Marshaler].
func (p P256Public) MarshalJSON() ([]byte, error) {
	if p.IsZero() {
		return json.Marshal(nil)
	}
	return json.Marshal(p.Serialize())
}

// UnmarshalJSON implements [json.Unmarshaler].
func (p *P256Public) UnmarshalJSON(data []byte) error {
	var b []byte
	if err := json.Unmarshal(data, &b); err != nil {
		return err
	}
	if b == nil {
		*p = P256Public{}
		return nil
	}

	parsed, err := ParseP256PublicKey(b)
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}
