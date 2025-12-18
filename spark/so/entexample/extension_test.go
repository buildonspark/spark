package entexample

import (
	"reflect"
	"testing"

	"entgo.io/ent/entc/gen"
	schemafield "entgo.io/ent/schema/field"
	"github.com/stretchr/testify/require"
)

func TestRenderValueForField(t *testing.T) {
	tests := []struct {
		name     string
		field    *gen.Field
		value    any
		expected string
	}{
		// String types
		{
			name:     "string",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeString}},
			value:    "hello",
			expected: `"hello"`,
		},
		{
			name:     "string with quotes",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeString}},
			value:    `say "hello"`,
			expected: `"say \"hello\""`,
		},

		// Boolean types
		{
			name:     "bool true",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeBool}},
			value:    true,
			expected: "true",
		},
		{
			name:     "bool false",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeBool}},
			value:    false,
			expected: "false",
		},

		// Integer types with float64 input (from JSON unmarshaling)
		{
			name:     "int from float64",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeInt}},
			value:    float64(42),
			expected: "int(42)",
		},
		{
			name:     "int8 from float64",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeInt8}},
			value:    float64(127),
			expected: "int8(127)",
		},
		{
			name:     "int16 from float64",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeInt16}},
			value:    float64(1000),
			expected: "int16(1000)",
		},
		{
			name:     "int32 from float64",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeInt32}},
			value:    float64(100000),
			expected: "int32(100000)",
		},
		{
			name:     "int64 from float64",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeInt64}},
			value:    float64(9999999),
			expected: "int64(9999999)",
		},

		// Unsigned integer types
		{
			name:     "uint from float64",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeUint}},
			value:    float64(42),
			expected: "uint(42)",
		},
		{
			name:     "uint8 from float64",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeUint8}},
			value:    float64(255),
			expected: "uint8(255)",
		},
		{
			name:     "uint16 from float64",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeUint16}},
			value:    float64(65535),
			expected: "uint16(65535)",
		},
		{
			name:     "uint32 from float64",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeUint32}},
			value:    float64(4294967295),
			expected: "uint32(4294967295)",
		},
		{
			name:     "uint64 from float64",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeUint64}},
			value:    float64(1234567890),
			expected: "uint64(1234567890)",
		},

		// Integer types with non-float64 input (fallback path)
		{
			name:     "int from int",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeInt}},
			value:    42,
			expected: "int(42)",
		},

		// Time type
		{
			name:     "time from RFC3339 string",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeTime}},
			value:    "2024-01-15T10:30:00Z",
			expected: `func() time.Time { t, _ := time.Parse(time.RFC3339, "2024-01-15T10:30:00Z"); return t }()`,
		},
		{
			name:     "time with timezone converts to UTC",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeTime}},
			value:    "2024-01-15T10:30:00+05:00",
			expected: `func() time.Time { t, _ := time.Parse(time.RFC3339, "2024-01-15T05:30:00Z"); return t }()`,
		},
		{
			name:     "time with invalid format",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeTime}},
			value:    "not-a-time",
			expected: `"not-a-time"`,
		},

		// Bytes type
		{
			name:     "bytes from hex string",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeBytes}},
			value:    "deadbeef",
			expected: `func() []byte { b, _ := hex.DecodeString("deadbeef"); return b }()`,
		},
		{
			name:     "bytes from non-hex string",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeBytes}},
			value:    "hello",
			expected: `[]byte("hello")`,
		},
		{
			name:     "bytes from empty string",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeBytes}},
			value:    "",
			expected: `[]byte("")`,
		},

		// JSON type - []string
		{
			name: "json []string",
			field: &gen.Field{Type: &schemafield.TypeInfo{
				Type:  schemafield.TypeJSON,
				RType: &schemafield.RType{Kind: reflect.Slice, Ident: "[]string"},
			}},
			value:    []any{"a", "b", "c"},
			expected: `[]string{"a", "b", "c", }`,
		},

		// JSON type - map[string][]byte
		{
			name: "json map[string][]byte",
			field: &gen.Field{Type: &schemafield.TypeInfo{
				Type:  schemafield.TypeJSON,
				RType: &schemafield.RType{Kind: reflect.Map, Ident: "map[string][]uint8"},
			}},
			value:    map[string]any{"key1": "deadbeef"},
			expected: `map[string][]byte{"key1": func() []byte { b, _ := hex.DecodeString("deadbeef"); return b }(), }`,
		},

		// JSON type - map[string]keys.Public
		{
			name: "json map[string]keys.Public",
			field: &gen.Field{Type: &schemafield.TypeInfo{
				Type:  schemafield.TypeJSON,
				RType: &schemafield.RType{Kind: reflect.Map, Ident: "map[string]keys.Public"},
			}},
			value:    map[string]any{"op1": "02abcd"},
			expected: `map[string]keys.Public{"op1": keys.MustParsePublicKeyHex("02abcd"), }`,
		},

		// Enum type
		{
			name:     "enum",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeEnum}},
			value:    "ACTIVE",
			expected: `"ACTIVE"`,
		},

		// Custom types via typeRegistry
		{
			name: "keys.Public via registry",
			field: &gen.Field{Type: &schemafield.TypeInfo{
				Type:  schemafield.TypeOther,
				RType: &schemafield.RType{Ident: "keys.Public"},
				Ident: "keys.Public",
			}},
			value:    "02abcdef",
			expected: `keys.MustParsePublicKeyHex("02abcdef")`,
		},
		{
			name: "keys.Private via registry",
			field: &gen.Field{Type: &schemafield.TypeInfo{
				Type:  schemafield.TypeOther,
				RType: &schemafield.RType{Ident: "keys.Private"},
				Ident: "keys.Private",
			}},
			value:    "abcdef01",
			expected: `keys.MustParsePrivateKeyHex("abcdef01")`,
		},
		{
			name: "uuid.UUID via registry",
			field: &gen.Field{Type: &schemafield.TypeInfo{
				Type:  schemafield.TypeOther,
				RType: &schemafield.RType{Ident: "uuid.UUID"},
				Ident: "uuid.UUID",
			}},
			value:    "550e8400-e29b-41d4-a716-446655440000",
			expected: `uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")`,
		},
		{
			name: "uint128.Uint128 via registry from float64",
			field: &gen.Field{Type: &schemafield.TypeInfo{
				Type:  schemafield.TypeOther,
				RType: &schemafield.RType{Ident: "uint128.Uint128"},
				Ident: "uint128.Uint128",
			}},
			value:    float64(12345),
			expected: "uint128.FromUint64(uint64(12345))",
		},
		{
			name: "schematype.TxID via registry",
			field: &gen.Field{Type: &schemafield.TypeInfo{
				Type:  schemafield.TypeOther,
				RType: &schemafield.RType{Ident: "schematype.TxID"},
				Ident: "schematype.TxID",
			}},
			value:    "abc123",
			expected: `schematype.MustParseTxID("abc123")`,
		},

		// Default fallback
		{
			name:     "unknown type uses %#v",
			field:    &gen.Field{Type: &schemafield.TypeInfo{Type: schemafield.TypeOther}},
			value:    "fallback",
			expected: `"fallback"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := renderValueForField(tt.field, tt.value)
			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestTypeRegistry(t *testing.T) {
	tests := []struct {
		name     string
		typeKey  string
		value    any
		expected string
	}{
		{
			name:     "keys.Public",
			typeKey:  "keys.Public",
			value:    "02abcdef1234567890",
			expected: `keys.MustParsePublicKeyHex("02abcdef1234567890")`,
		},
		{
			name:     "keys.Private",
			typeKey:  "keys.Private",
			value:    "abcdef0123456789",
			expected: `keys.MustParsePrivateKeyHex("abcdef0123456789")`,
		},
		{
			name:     "frost.SigningCommitment",
			typeKey:  "frost.SigningCommitment",
			value:    "commitment123",
			expected: `frost.MustParseSigningCommitment("commitment123")`,
		},
		{
			name:     "frost.SigningNonce",
			typeKey:  "frost.SigningNonce",
			value:    "nonce456",
			expected: `frost.MustParseSigningNonce("nonce456")`,
		},
		{
			name:     "schematype.TxID",
			typeKey:  "schematype.TxID",
			value:    "txid789",
			expected: `schematype.MustParseTxID("txid789")`,
		},
		{
			name:     "uint128.Uint128",
			typeKey:  "uint128.Uint128",
			value:    float64(12345),
			expected: "uint128.FromUint64(uint64(12345))",
		},
		{
			name:     "uuid.UUID",
			typeKey:  "uuid.UUID",
			value:    "550e8400-e29b-41d4-a716-446655440000",
			expected: `uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			renderFunc, ok := typeRegistry[tt.typeKey]
			require.True(t, ok, "typeRegistry should contain key %q", tt.typeKey)
			result, err := renderFunc(tt.value)
			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestTypeRegistryErrors(t *testing.T) {
	tests := []struct {
		name    string
		typeKey string
		value   any
	}{
		{
			name:    "keys.Public with non-string",
			typeKey: "keys.Public",
			value:   12345,
		},
		{
			name:    "keys.Private with non-string",
			typeKey: "keys.Private",
			value:   67890,
		},
		{
			name:    "frost.SigningCommitment with non-string",
			typeKey: "frost.SigningCommitment",
			value:   999,
		},
		{
			name:    "frost.SigningNonce with non-string",
			typeKey: "frost.SigningNonce",
			value:   111,
		},
		{
			name:    "schematype.TxID with non-string",
			typeKey: "schematype.TxID",
			value:   222,
		},
		{
			name:    "uint128.Uint128 with non-float64",
			typeKey: "uint128.Uint128",
			value:   "67890",
		},
		{
			name:    "uuid.UUID with non-string",
			typeKey: "uuid.UUID",
			value:   123,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Contains(t, typeRegistry, tt.typeKey)
			_, err := typeRegistry[tt.typeKey](tt.value)
			require.Error(t, err)
		})
	}
}
