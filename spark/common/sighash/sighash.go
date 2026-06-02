// Package sighash defines a typed wrapper for a 32-byte Bitcoin signing digest, plus helpers for computing one from
// a transaction.
package sighash

import (
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"entgo.io/ent/schema/field"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// Size is the length of a Bitcoin taproot signing digest in bytes.
const Size = 32

// Hash is a 32-byte Bitcoin signing digest (BIP-341 taproot sighash). It stores the bytes directly, so == compares by value.
type Hash [Size]byte

// Parse parses a Hash from a 32-byte slice.
func Parse(b []byte) (Hash, error) {
	if len(b) != Size {
		return Hash{}, fmt.Errorf("invalid sighash length: %d (want %d)", len(b), Size)
	}
	var h Hash
	copy(h[:], b)
	return h, nil
}

// ParseHex parses a Hash from a 64-character hex string.
func ParseHex(s string) (Hash, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return Hash{}, err
	}
	return Parse(b)
}

// MustParseHex parses a hex-encoded sighash. Panics on error. Meant for use in tests and static initialization.
func MustParseHex(s string) Hash {
	h, err := ParseHex(s)
	if err != nil {
		panic(err)
	}
	return h
}

// Serialize returns a copy of the 32 bytes. Returns nil for the zero value.
func (h Hash) Serialize() []byte {
	if h.IsZero() {
		return nil
	}
	return append([]byte{}, h[:]...)
}

// ToHex returns the sighash as a hex-encoded string.
func (h Hash) ToHex() string {
	return hex.EncodeToString(h[:])
}

// String returns the sighash as a hex-encoded string. Implements [fmt.Stringer].
func (h Hash) String() string {
	return h.ToHex()
}

// IsZero returns true if this is the zero-value Hash.
func (h Hash) IsZero() bool {
	return h == Hash{}
}

// Equals returns true if h and other are byte-identical. Equivalent to ==.
func (h Hash) Equals(other Hash) bool {
	return h == other
}

// Value implements the [field.ValueScanner] interface.
func (h Hash) Value() (driver.Value, error) {
	return h.Serialize(), nil
}

var _ field.ValueScanner = &Hash{}

// Scan implements the [field.ValueScanner] interface.
func (h *Hash) Scan(src any) error {
	*h = Hash{}
	b, err := getValue(src)
	if err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	parsed, err := Parse(b)
	if err != nil {
		return err
	}
	*h = parsed
	return nil
}

// MarshalJSON implements [json.Marshaler]. The zero value marshals as JSON null;
// otherwise the 32 bytes are encoded as base64 (Go's default for []byte).
func (h Hash) MarshalJSON() ([]byte, error) {
	if h.IsZero() {
		return json.Marshal(nil)
	}
	return json.Marshal(h.Serialize())
}

// UnmarshalJSON implements [json.Unmarshaler].
func (h *Hash) UnmarshalJSON(data []byte) error {
	var b []byte
	if err := json.Unmarshal(data, &b); err != nil {
		return err
	}
	if b == nil {
		*h = Hash{}
		return nil
	}
	parsed, err := Parse(b)
	if err != nil {
		return err
	}
	*h = parsed
	return nil
}

func getValue(src any) ([]byte, error) {
	switch v := src.(type) {
	case nil:
		return nil, nil
	case *sql.Null[[]byte]:
		if v == nil || !v.Valid {
			return nil, nil
		}
		return v.V, nil
	case []byte:
		return v, nil
	default:
		return nil, fmt.Errorf("unexpected input for Scan: %T", src)
	}
}

// FromTx computes the BIP-341 taproot sighash for the given input of a single-prevout transaction.
func FromTx(tx *wire.MsgTx, inputIndex int, prevOutput *wire.TxOut) (Hash, error) {
	prevOutputFetcher := txscript.NewCannedPrevOutputFetcher(prevOutput.PkScript, prevOutput.Value)
	sighashes := txscript.NewTxSigHashes(tx, prevOutputFetcher)

	raw, err := txscript.CalcTaprootSignatureHash(sighashes, txscript.SigHashDefault, tx, inputIndex, prevOutputFetcher)
	if err != nil {
		return Hash{}, err
	}
	return Parse(raw)
}

// FromMultiPrevOutTx computes the BIP-341 taproot sighash for the given input of a multi-prevout transaction.
func FromMultiPrevOutTx(tx *wire.MsgTx, inputIndex int, prevOutputs map[wire.OutPoint]*wire.TxOut) (Hash, error) {
	prevOutFetcher := txscript.NewMultiPrevOutFetcher(prevOutputs)
	sighashes := txscript.NewTxSigHashes(tx, prevOutFetcher)

	raw, err := txscript.CalcTaprootSignatureHash(sighashes, txscript.SigHashDefault, tx, inputIndex, prevOutFetcher)
	if err != nil {
		return Hash{}, err
	}
	return Parse(raw)
}
