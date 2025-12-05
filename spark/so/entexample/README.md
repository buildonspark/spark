# Ent Example Extension

This package provides an [Ent](https://entgo.io) code generation extension that automatically generates test fixture builders for your Ent schemas.

## Overview

The `entexample` extension generates builder patterns for creating test fixtures, making it easy to set up database entities for testing without repetitive boilerplate code.

## Features

- **Fluent builder API** for setting fields and edges
- **Automatic default values** via annotations
- **Smart required field handling** - auto-creates required edges, errors on missing required fields
- **Two execution modes**:
  - `MustExec()` - panics on failure (for simple test cases)
  - `Exec()` - returns errors (for tests that need error handling)
- **Type-safe** - uses generated Ent types throughout

## Installation

The extension is already integrated into the Ent code generation. It's registered in `so/ent/entc.go`:

```go
ext := &entexample.Extension{}
err := entc.Generate("./schema", &gen.Config{
    // ... features
}, entc.Extensions(ext))
```

When you run `make ent`, the extension automatically generates test fixtures in `so/ent/entexample/`.

## Usage

### Basic Example

```go
import (
    "context"
    "testing"

    "github.com/lightsparkdev/spark/so/db"
    "github.com/lightsparkdev/spark/so/ent/entexample"
)

func TestMyFeature(t *testing.T) {
    ctx := db.NewTestSQLiteContext(t)
    client := ent.FromContext(ctx)

    // Create a deposit address with all defaults
    addr := entexample.NewDepositAddressExample(t, client).
        MustExec(ctx)

    // Create with custom fields
    addr2 := entexample.NewDepositAddressExample(t, client).
        SetAddress("bcrt1p...custom").
        SetNetwork(st.NetworkMainnet).
        MustExec(ctx)
}
```

### Setting Fields

```go
example := entexample.NewBlockHeightExample(t, client).
    SetHeight(100).
    SetHash("blockhash123")
```

### Setting Edges

```go
// For unique edges (one-to-one, many-to-one)
keyshare := entexample.NewSigningKeyshareExample(t, client).MustExec(ctx)
addr := entexample.NewDepositAddressExample(t, client).
    SetSigningKeyshare(keyshare).
    MustExec(ctx)

// For non-unique edges (one-to-many, many-to-many)
utxo1 := entexample.NewUtxoExample(t, client).MustExec(ctx)
utxo2 := entexample.NewUtxoExample(t, client).MustExec(ctx)
addr := entexample.NewDepositAddressExample(t, client).
    AddUtxo(utxo1).
    AddUtxo(utxo2).
    MustExec(ctx)
```

### Error Handling

```go
// Use Exec() instead of MustExec() when you need to handle errors
addr, err := entexample.NewDepositAddressExample(t, client).
    SetAddress("invalid").
    Exec(ctx)
if err != nil {
    // Handle error
}
```

## Adding Default Values to Schemas

To provide default values for your test fixtures, use the `entexample.Default()` annotation in your schema definitions:

```go
import "github.com/lightsparkdev/spark/so/entexample"

func (DepositAddress) Fields() []ent.Field {
    return []ent.Field{
        field.String("address").
            Annotations(entexample.Default(
                "bcrt1pkvkqsq52a8uprpdzlwj2m8r3lhp2zqtp08h7sp5skfydqxkytkeqp0mxzf",
            )),
        field.Bytes("owner_identity_pubkey").
            GoType(keys.Public{}).
            Annotations(entexample.Default(
                "037f699d5b77668b847d92a3d4ad199af4d11ebc2069cf78d7694b08be0a6b381d",
            )),
        field.Int64("confirmation_height").
            Optional().
            Annotations(entexample.Default(2630707)),
        field.JSON("address_signatures", map[string][]byte{}).
            Optional().
            Annotations(entexample.Default(map[string]string{
                "key1": "value1",
                "key2": "value2",
            })),
    }
}
```

### Supported Types

The extension supports custom rendering for:
- **Basic types**: string, bool, int variants, uint variants, time.Time, []byte
- **Custom types**: `keys.Public`, `keys.Private`, `uint128.Uint128`, `uuid.UUID`
- **JSON fields**: maps and slices with proper type handling
- **Enums**: GoType enums

See `extension.go` for the complete type registry and rendering logic.

## How It Works

### Code Generation Flow

1. **Extension Registration**: `entc.go` registers the extension with Ent's code generator
2. **Template Execution**: The extension provides `templates/entexample.tmpl` which iterates over all schemas
3. **Builder Generation**: For each schema, generates a builder struct with:
   - Field setters for non-default fields
   - Edge setters for relationships
   - `MustExec()` and `Exec()` methods that create the entity

### Required Fields

- **Required fields without defaults**: Must be set explicitly or will error at runtime
- **Required fields with defaults**: Automatically use the annotation value
- **Optional fields without defaults**: Can be omitted (will be NULL/zero value)
- **Optional fields with defaults**: Use the annotation value if not explicitly set

### Required Edges

Required edges are **automatically created** if not provided:

```go
// DepositAddress requires a SigningKeyshare edge
// This automatically creates a default SigningKeyshare:
addr := entexample.NewDepositAddressExample(t, client).MustExec(ctx)

// Or provide your own:
keyshare := entexample.NewSigningKeyshareExample(t, client).
    SetPubkey("custom").
    MustExec(ctx)
addr := entexample.NewDepositAddressExample(t, client).
    SetSigningKeyshare(keyshare).
    MustExec(ctx)
```

## Template Customization

The template is located at `so/entexample/templates/entexample.tmpl`. It uses Go's `text/template` syntax with Ent's template functions.

Key template features:
- `{{ range $n := $.Nodes }}` - iterates over all schemas
- `{{ $f.Optional }}` - checks if a field is optional
- `{{ $f.Annotations }}` - accesses field annotations
- `formatDefault` custom function - renders default values based on field type

## Generated File

The extension generates a single file: `so/ent/entexample/entexample.go`

This file contains builder types for all schemas in your `so/ent/schema/` directory.

## TODO
- Ensure all non-optional fields have an `entexample.Default` annotation at code generation time
  through a hook.
- Dynamic fixtures values (i.e. for a time field, allow the example to generate 
  `time.Now() + 2 * time.Minute` as the time rather than a fixed value).
- Everything is generated in `entexample.go`. Ideally we would get separate files for each entity
  (i.e. `blockheight_example.go`) but I couldn't find a way to make it work with the template
  system.
