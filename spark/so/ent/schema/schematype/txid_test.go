package schematype

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTxID(t *testing.T) {
	hash := chainhash.HashH([]byte("test transaction"))

	txid := NewTxID(hash)

	assert.Equal(t, hash, txid.Hash())
	assert.Equal(t, hash[:], txid.Bytes())
}

func TestNewTxIDFromBytes(t *testing.T) {
	hash := chainhash.HashH([]byte("test transaction"))
	hashBytes := hash[:]

	txid, err := NewTxIDFromBytes(hashBytes)

	require.NoError(t, err)
	assert.Equal(t, hashBytes, txid.Bytes())
	assert.Equal(t, hash, txid.Hash())
}

func TestNewTxIDFromBytes_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name        string
		input       []byte
		expectedErr string
	}{
		{
			name:        "nil",
			input:       nil,
			expectedErr: "invalid txid length: expected 32, got 0",
		},
		{
			name:        "empty",
			input:       []byte{},
			expectedErr: "invalid txid length: expected 32, got 0",
		},
		{
			name:        "too short",
			input:       make([]byte, 16),
			expectedErr: "invalid txid length: expected 32, got 16",
		},
		{
			name:        "too long",
			input:       make([]byte, 64),
			expectedErr: "invalid txid length: expected 32, got 64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewTxIDFromBytes(tt.input)
			require.ErrorContains(t, err, tt.expectedErr)
		})
	}
}

func TestNewTxIDFromString(t *testing.T) {
	hash := chainhash.HashH([]byte("test transaction"))
	hashStr := hash.String()

	txid, err := NewTxIDFromString(hashStr)

	require.NoError(t, err)
	assert.Equal(t, hash, txid.Hash())
	assert.Equal(t, hashStr, txid.String())
}

func TestNewTxIDFromString_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectedErr string
	}{
		{
			name:        "empty",
			input:       "",
			expectedErr: "invalid txid hex length: expected 64 hex chars, got 0",
		},
		{
			name:        "invalid hex",
			input:       "not a valid hex string" + strings.Repeat("0", 42),
			expectedErr: "failed to parse txid string",
		},
		{
			name:        "too short",
			input:       "abc123",
			expectedErr: "invalid txid hex length: expected 64 hex chars, got 6",
		},
		{
			name:        "too long",
			input:       strings.Repeat("0", 90),
			expectedErr: "invalid txid hex length: expected 64 hex chars, got 90",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewTxIDFromString(tt.input)
			require.ErrorContains(t, err, tt.expectedErr)
		})
	}
}

func TestTxID_Hash(t *testing.T) {
	hash := chainhash.HashH([]byte("test transaction"))
	txid := NewTxID(hash)

	result := txid.Hash()

	assert.Equal(t, hash, result)
}

func TestTxID_Bytes(t *testing.T) {
	hash := chainhash.HashH([]byte("test transaction"))
	txid := NewTxID(hash)

	result := txid.Bytes()

	assert.Equal(t, hash[:], result)
	// Verify it returns a copy
	result[0] = ^result[0]
	assert.NotEqual(t, result, txid.Bytes())
}

func TestTxID_String(t *testing.T) {
	hash := chainhash.HashH([]byte("test transaction"))
	txid := NewTxID(hash)

	result := txid.String()

	assert.Equal(t, hash.String(), result)
}

func TestTxID_IsZero(t *testing.T) {
	hash := chainhash.HashH([]byte("test transaction"))

	tests := []struct {
		name           string
		txid           TxID
		expectedIsZero bool
	}{
		{
			name:           "zero value",
			txid:           TxID{},
			expectedIsZero: true,
		},
		{
			name:           "non-zero value",
			txid:           NewTxID(hash),
			expectedIsZero: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedIsZero, tt.txid.IsZero())
		})
	}
}

func TestTxID_Value(t *testing.T) {
	hash := chainhash.HashH([]byte("test transaction"))

	tests := []struct {
		name          string
		txid          TxID
		expectedValue any
	}{
		{
			name:          "non-zero value",
			txid:          NewTxID(hash),
			expectedValue: hash[:],
		},
		{
			name:          "zero value",
			txid:          TxID{},
			expectedValue: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, err := tt.txid.Value()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedValue, value)
		})
	}
}

func TestTxID_Scan(t *testing.T) {
	hash := chainhash.HashH([]byte("test transaction"))
	hashBytes := hash[:]
	hashStr := hash.String()

	tests := []struct {
		name         string
		input        any
		expectedHash chainhash.Hash
	}{
		{
			name:         "valid bytes",
			input:        hashBytes,
			expectedHash: hash,
		},
		{
			name:         "valid string",
			input:        hashStr,
			expectedHash: hash,
		},
		{
			name:         "nil value",
			input:        nil,
			expectedHash: chainhash.Hash{},
		},
		{
			name:         "empty bytes",
			input:        []byte{},
			expectedHash: chainhash.Hash{},
		},
		{
			name:         "nil sql.Null",
			input:        (*sql.Null[[]byte])(nil),
			expectedHash: chainhash.Hash{},
		},
		{
			name:         "null value",
			input:        &sql.Null[[]byte]{Valid: false},
			expectedHash: chainhash.Hash{},
		},
		{
			name:         "valid sql.Null",
			input:        &sql.Null[[]byte]{V: hashBytes, Valid: true},
			expectedHash: hash,
		},
		{
			name:         "empty sql.Null",
			input:        &sql.Null[[]byte]{V: []byte{}, Valid: true},
			expectedHash: chainhash.Hash{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var txid TxID
			err := txid.Scan(tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.expectedHash, txid.hash)
		})
	}
}

func TestTxID_Scan_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name        string
		input       any
		expectedErr string
	}{
		{
			name:        "invalid type",
			input:       123,
			expectedErr: "unexpected type for TxID: int",
		},
		{
			name:        "invalid type struct",
			input:       struct{}{},
			expectedErr: "unexpected type for TxID: struct {}",
		},
		{
			name:        "invalid bytes length",
			input:       make([]byte, 16),
			expectedErr: "failed to deserialize txid bytes",
		},
		{
			name:        "invalid string",
			input:       "not a valid hash",
			expectedErr: "failed to deserialize txid string",
		},
		{
			name:        "invalid sql.Null bytes",
			input:       &sql.Null[[]byte]{V: make([]byte, 16), Valid: true},
			expectedErr: "failed to deserialize txid bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var txid TxID
			err := txid.Scan(tt.input)
			require.ErrorContains(t, err, tt.expectedErr)
		})
	}
}

func TestTxID_RoundTrip(t *testing.T) {
	hash := chainhash.HashH([]byte("test transaction"))
	original := NewTxID(hash)

	value, err := original.Value()
	require.NoError(t, err)

	var scanned TxID
	err = scanned.Scan(value)
	require.NoError(t, err)

	assert.Equal(t, original.Hash(), scanned.Hash())
	assert.Equal(t, original.Bytes(), scanned.Bytes())
	assert.Equal(t, original.String(), scanned.String())
}

func TestTxID_RoundTrip_String(t *testing.T) {
	hash := chainhash.HashH([]byte("test transaction"))
	original := NewTxID(hash)

	str := original.String()
	parsed, err := NewTxIDFromString(str)
	require.NoError(t, err)

	assert.Equal(t, original.Hash(), parsed.Hash())
	assert.Equal(t, original.Bytes(), parsed.Bytes())
	assert.Equal(t, original.String(), parsed.String())
}

func TestTxID_RoundTrip_Bytes(t *testing.T) {
	hash := chainhash.HashH([]byte("test transaction"))
	original := NewTxID(hash)

	bytes := original.Bytes()
	parsed, err := NewTxIDFromBytes(bytes)
	require.NoError(t, err)

	assert.Equal(t, original.Hash(), parsed.Hash())
	assert.Equal(t, original.Bytes(), parsed.Bytes())
	assert.Equal(t, original.String(), parsed.String())
}
