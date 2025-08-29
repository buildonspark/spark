### Protohash (Go) — Deterministic canonical hashing for Protobuf messages

This library computes a stable, deterministic hash for Protobuf values. It is designed to be cross-language compatible with the JavaScript implementation used in Spark.

The hash is computed by SHA-256 over a type-identifier prefix and a canonical byte representation of the value. Primitive values, lists, maps, and messages all have well-defined rules described below.

### Type identifiers

- **bool**: `b`
- **int/uint (all integer kinds)**: `i`
- **float/double**: `f` (with canonical float normalization)
- **string (unicode)**: `u`
- **bytes**: `r`
- **list/array**: `l`
- **map/object**: `d`

Hashing is always `SHA256(type_identifier || canonical_bytes)`.

### How field IDs are used (messages)

- Messages are treated like canonical maps from field ID → value.
- For each present field:
  - The field key is hashed as the field tag number (`hashInt64(field_number)`) by default.
  - The field value is hashed according to its type (see below).
- Field entries are sorted by their numeric field number before combining.
- The message’s concatenated `(key_hash || value_hash)` entries are then wrapped with the map identifier `d` and hashed.

### How nesting works

- **Messages**: Values of message fields are hashed recursively using the same rules.
- **Lists**: Elements are hashed in order; the concatenation is wrapped with `l` and hashed. Order matters.
- **Maps**: Each entry hashes its key and value; entries are sorted by the key hash bytes, concatenated, then wrapped with `d` and hashed. Order does not matter.
- **Well-known types**: `google.protobuf.{BoolValue,Int32Value,Int64Value,UInt32Value,UInt64Value,FloatValue,DoubleValue,StringValue,Timestamp,Duration,Struct,ListValue,Value}` are handled explicitly to hash their logical value consistently.

### Optional, presence, and null handling (primitives)

- Only fields with non-default values are hashed. A field is skipped if it contains:
  - `0` for any numeric type (integers, floats, enums).
  - `false` for booleans.
  - An empty string (`""`).
  - Empty `bytes`.
  - An empty list (see list semantics below).
  - An empty map (or a map whose entries are all skipped; see map semantics below).
  - For `google.protobuf.Value` message fields: a `null_value` is treated as default and skipped.

**⚠️ Warning**: This hasher should not be used on protobuf messages that define or rely on non-zero default values. Since only non-default values are included in the hash, fields with custom default values will be excluded from the hash when they contain their default value, potentially leading to unexpected hash collisions or inconsistencies.

### Primitive value hashing details

- **bool**: if true, `b || "1"`; false values are omitted (default)
- **int/uint**: `i || 64-bit big-endian binary representation` (signed/unsigned as appropriate)
- **float/double**: `f || IEEE 754 binary64 (big-endian) of normalized value` (normalizes `-0.0` to `+0.0`; collapses all `NaN` to a single quiet NaN; encodes `±Infinity` and finite values using the 64-bit IEEE 754 bit pattern). Note: 32-bit `float` values are promoted to 64-bit before encoding.
- **string**: `u || UTF-8 bytes`
- **bytes**: `r || raw bytes`

### Top-level wrapper policy

Top-level scalar/wrapper types are not hashable. Attempting to hash any of the following at the top level returns an error:

- `google.protobuf.Value`
- `google.protobuf.ListValue`
- `google.protobuf.{BoolValue,Int32Value,Int64Value,UInt32Value,UInt64Value,FloatValue,DoubleValue,StringValue,BytesValue}`

Rationale: avoid confusing hashes of "falsey" values and enforce hashing of structured messages.

### Lists, Maps, and Struct semantics

- **Lists**

  - Order matters (elements are hashed in sequence).
  - Empty lists are default-equivalent and are skipped (the field is omitted from the hash).
  - Null elements are not allowed: a list element that is a `google.protobuf.Value` with `null_value` triggers an error. Skipping ordered elements would be malleable.

- **Maps**

  - Entries are hashed as `(key_hash, value_hash)` pairs and sorted by `key_hash` bytes.
  - Map entries whose value is `google.protobuf.Value` set to `null_value` are skipped.
  - If all entries are skipped, the map field itself is skipped (default-equivalent), so an explicit map of only-null values equals an empty/absent map.

- **google.protobuf.Struct**
  - Internally a `map<string, Value>`.
  - Fields set to `null_value` are skipped.
  - Fields set to `list_value: []` are skipped (default-equivalent) to match list semantics above.

### Usage

```go
hasher := protohash.NewHasher()

hash, err := hasher.HashProto(myMessage.ProtoReflect())
if err != nil {
    // handle error
}
// hash is a 32-byte SHA-256 digest
```

### Testing (cross-language verification)

Before adding or relying on a new proto shape for cross-language hashing, add or update a test case in `spark/testdata/cross_language_hash_cases.json` and validate both Go and JS implementations.

1. Add a test case

- Append a case to `spark/testdata/cross_language_hash_cases.json` under `test_cases`.
- If you do not yet know the expected hash, set `expected_hash` to `"TBD"` or an empty string.

2. Compute expected hash in Go

- Run the JSON-driven test; it will print `COMPUTED_HASH <name>: <hex>` for cases with missing `expected_hash`.

```bash
cd spark
go test ./common -run TestSparkInvoiceFieldsJSONCases -v | cat
```

- Copy the printed hex into the case’s `expected_hash` field.

3. Verify in JavaScript

- Run the JS test that consumes the same JSON and compares against the JS hasher.

```bash
cd sdks/js/packages/spark-sdk
yarn test src/tests/cross-language-hash-cases.test.ts
```

Notes

- The current JSON harness and JS test construct `spark.SparkInvoiceFields` messages. If you introduce new message types for cross-language hashing, update the Go JSON→proto conversion in `spark/common/hash_cross_language_json_test.go` and the JS constructor in `sdks/js/packages/spark-sdk/src/tests/cross-language-hash-cases.test.ts` accordingly, then add corresponding JSON cases.
- Keep cases small and targeted (one aspect per case) to make discrepancies easy to diagnose.

### Notes

- `google.protobuf.Any` is not supported and will return an error.
- The algorithm sorts message fields by numeric field number, independent of the key-hash option, to ensure stable ordering across languages.
