package schematype

import (
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"testing"

	"entgo.io/ent/schema/field"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/stretchr/testify/require"
)

// TxID is a wrapper around chainhash.Hash that implements field.ValueScanner
// for use in Ent schemas.
type TxID struct {
	hash chainhash.Hash
}

var _ field.ValueScanner = &TxID{}

// NewTxID creates a new TxID from a chainhash.Hash.
func NewTxID(hash chainhash.Hash) TxID {
	return TxID{hash: hash}
}

// NewTxIDFromBytes creates a new TxID from a byte slice.
func NewTxIDFromBytes(b []byte) (TxID, error) {
	if len(b) != chainhash.HashSize {
		return TxID{}, fmt.Errorf("invalid txid length: expected %d, got %d", chainhash.HashSize, len(b))
	}
	hash, err := chainhash.NewHash(b)
	if err != nil {
		return TxID{}, fmt.Errorf("failed to parse txid: %w", err)
	}
	return TxID{hash: *hash}, nil
}

// NewTxIDFromString creates a new TxID from a hex-encoded string.
func NewTxIDFromString(s string) (TxID, error) {
	if len(s) != chainhash.HashSize*2 {
		return TxID{}, fmt.Errorf("invalid txid hex length: expected %d hex chars, got %d", chainhash.HashSize*2, len(s))
	}
	hash, err := chainhash.NewHashFromStr(s)
	if err != nil {
		return TxID{}, fmt.Errorf("failed to parse txid string: %w", err)
	}
	return TxID{hash: *hash}, nil
}

// Hash returns the underlying chainhash.Hash.
func (t TxID) Hash() chainhash.Hash {
	return t.hash
}

// Bytes returns the byte representation of the transaction ID.
func (t TxID) Bytes() []byte {
	return t.hash.CloneBytes()
}

// String returns the byte-reversed hex-encoded string representation of the transaction ID.
func (t TxID) String() string {
	return t.hash.String()
}

// IsZero returns true if this TxID is the zero value.
func (t TxID) IsZero() bool {
	return t == TxID{}
}

// Value implements the driver.Valuer interface for database serialization.
func (t TxID) Value() (driver.Value, error) {
	if t.IsZero() {
		return nil, nil
	}
	return t.hash[:], nil
}

// Scan implements the sql.Scanner interface for database deserialization.
func (t *TxID) Scan(src any) error {
	switch v := src.(type) {
	case []byte:
		if len(v) == 0 {
			t.hash = chainhash.Hash{}
			return nil
		}
		hash, err := chainhash.NewHash(v)
		if err != nil {
			return fmt.Errorf("failed to deserialize txid bytes %x: %w", v, err)
		}
		t.hash = *hash
		return nil
	case string:
		if err := chainhash.Decode(&t.hash, v); err != nil {
			return fmt.Errorf("failed to deserialize txid string %s: %w", v, err)
		}
		return nil
	case nil:
		t.hash = chainhash.Hash{}
		return nil
	case *sql.Null[[]byte]:
		if v != nil && v.Valid && len(v.V) > 0 {
			hash, err := chainhash.NewHash(v.V)
			if err != nil {
				return fmt.Errorf("failed to deserialize txid bytes %x: %w", v.V, err)
			}
			t.hash = *hash
			return nil
		}
		t.hash = chainhash.Hash{}
		return nil
	default:
		return fmt.Errorf("unexpected type for TxID: %T", src)
	}
}

// NewRandomTxID generates a random TxID for testing purposes.
func NewRandomTxIDForTesting(t *testing.T) TxID {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err, "failed to generate random bytes")
	txid, err := NewTxIDFromBytes(b)
	require.NoError(t, err, "failed to create TxID")
	return txid
}
