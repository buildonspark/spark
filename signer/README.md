# Spark Signer

## Overview

The `spark/signer` directory contains **three separate Rust crates** that serve different purposes in the Lightspark signing ecosystem:

```
signer/
├── spark-frost/           # Core library (shared)
├── spark-frost-signer/    # gRPC server (backend)
└── spark-frost-uniffi/    # JS/WASM bindings (frontend)
```

---

## Multi-Target Compilation Support

The Lightspark signer crates are designed with different compilation target capabilities:

| Crate                  | WASM (wasm32) | Native (x86_64/arm64) | Notes                                                                                  |
| ---------------------- | ------------- | --------------------- | -------------------------------------------------------------------------------------- |
| **spark-frost**        | ✅ Yes        | ✅ Yes                | Conditional compilation via `build.rs` - WASM uses `prost`, native uses `tonic + gRPC` |
| **spark-frost-signer** | ❌ No         | ✅ Yes                | Server binary only - requires tokio/tonic (not WASM-compatible)                        |
| **spark-frost-uniffi** | ✅ Yes        | ✅ Yes                | Dual-target by design - `wasm-bindgen` for WASM, `uniffi` for native FFI               |

**Key Insight:** Both `spark-frost` (core library) and `spark-frost-uniffi` (bindings) support WASM compilation for browser/Node.js environments. The core library uses conditional compilation to exclude gRPC dependencies when building for WASM, while uniffi uses `wasm-bindgen` to create JavaScript-friendly bindings.

### How WASM Compilation Works

**spark-frost (core library):**

```rust
// build.rs - conditional protobuf compilation
if cfg!(target_arch = "wasm32") {
    prost_build::compile_protos(...)  // WASM: prost only (no gRPC)
} else {
    tonic_build::compile_protos(...)  // Native: tonic + gRPC support
}
```

**spark-frost-uniffi (bindings):**

```toml
[dependencies]
wasm-bindgen = "0.2.95"  # WASM JavaScript bindings
uniffi = "0.28.3"        # Native FFI (Swift, Kotlin, Python, etc.)

[lib]
crate-type = ["cdylib", "staticlib"]  # For native FFI
```

Build for WASM:

```bash
cd spark-frost-uniffi
wasm-pack build --target web  # Produces .wasm + .js
```

Build for Native:

```bash
cd spark-frost-uniffi
cargo build --release  # Produces .so/.dylib/.dll
```

---

## 1. **`spark-frost`** - Core FROST Library

**Compilation Targets:** ✅ Native | ✅ WASM

**Type:** Rust library crate (not a binary)

**Purpose:** Shared cryptographic implementation that other crates depend on

### Cargo.toml Key Details:

```toml
[package]
name = "spark-frost"

[dependencies]
prost = { workspace = true }              # Protocol Buffers
frost-secp256k1-tr = { workspace = true } # FROST implementation
tonic = { workspace = true }              # gRPC (non-WASM only)
bitcoin = "0.32.5"                        # Bitcoin utilities
ecies = { workspace = true }              # Encryption
rayon = { workspace = true }              # Parallel processing
```

### What It Contains:

**`src/lib.rs`** (3-15): Core exports

- `pub mod bridge` - Transaction operations (ECIES, tx construction)
- `pub mod proto` - Protocol buffer definitions
- `pub mod signing` - FROST signing implementation

**Key Functions:**

| Module    | Purpose                                    | File             |
| --------- | ------------------------------------------ | ---------------- |
| `signing` | FROST protocol (nonce, sign, aggregate)    | `src/signing.rs` |
| `bridge`  | Transaction construction, ECIES encryption | `src/bridge.rs`  |
| `proto`   | Protobuf message types                     | `src/proto.rs`   |

### Build Process (`build.rs`):

```rust
// Compiles Protocol Buffer definitions into Rust code
if target_arch == "wasm32" {
    prost_build::compile_protos(...)  // WASM: prost only
} else {
    tonic_build::compile_protos(...)   // Native: tonic + gRPC
}
```

**Generated Code:**

- Reads `protos/frost.proto` and `common.proto`
- Generates Rust structs: `FrostNonceRequest`, `KeyPackage`, `SigningCommitment`, etc.
- Output: `target/build/spark-frost-*/out/` (auto-included via `include!(...)`)

### What It Builds:

**Artifact:** `libspark_frost.rlib` (Rust library)

**Not a standalone binary!** - Must be used as a dependency

---

## 2. **`spark-frost-signer`** - gRPC Server

**Compilation Targets:** ✅ Native | ❌ WASM

**Type:** Binary crate (executable)

**Purpose:** Standalone server that Go backend connects to for FROST operations

### Cargo.toml Key Details:

```toml
[package]
name = "spark-frost-signer"

[dependencies]
spark-frost = { path = "../spark-frost" }  # Depends on core lib
tonic = { workspace = true }               # gRPC server
tokio = { workspace = true }               # Async runtime
clap = { workspace = true }                # CLI argument parsing
```

### What It Contains:

**`src/main.rs`** (23-33): Entry point with CLI

```rust
#[derive(Parser)]
struct Args {
    #[arg(short, long)]
    port: Option<u16>,        // TCP port (e.g., 8080)

    #[arg(short, long)]
    unix: Option<String>,     // Unix socket path
}
```

**`src/server.rs`**: `FrostServer` implementation

- Implements `FrostServiceServer` trait from tonic
- Handles gRPC requests: `dkg_round1`, `dkg_round2`, `dkg_round3`, `frost_nonce`, `sign_frost`, `aggregate_frost`

**`src/dkg.rs`**: Distributed Key Generation

- Implements DKG protocol rounds 1-3
- Uses `frost_secp256k1_tr::keys::dkg::{part1, part2, part3}`

### How It Runs:

**Starting the server:**

```bash
# Unix socket (default for Lightspark)
spark-frost-signer --unix /tmp/frost-signer.sock

# TCP port
spark-frost-signer --port 8080
```

**Server behavior** (`main.rs:80-107`):

1. Creates `FrostServer` instance
2. Binds to Unix socket or TCP port
3. Starts tonic gRPC server
4. Listens for requests from Go backend
5. Handles SIGTERM/SIGINT for graceful shutdown

### What It Builds:

**Artifact:** `spark-frost-signer` (executable binary)

**Location:** `target/release/spark-frost-signer` or `target/debug/spark-frost-signer`

**Usage:**

```bash
cargo build --release
./target/release/spark-frost-signer --unix /var/run/frost.sock
```

---

## 3. **`spark-frost-uniffi`** - Multi-Language Bindings

**Compilation Targets:** ✅ Native | ✅ WASM

**Type:** Library crate with FFI exports (`cdylib` + `staticlib`)

**Purpose:** Exposes Rust FROST signing to JavaScript/TypeScript SDK and potentially other languages

### Cargo.toml Key Details:

```toml
[package]
name = "spark-frost-uniffi"

[dependencies]
wasm-bindgen = { version = "0.2.95" }     # WASM bindings
uniffi = "0.28.3"                         # Multi-language FFI
spark-frost = { path = "../spark-frost" }

[lib]
crate-type = ["cdylib", "staticlib"]      # Shared/static library
name = "spark_frost"
```

**Dual Binding Strategy:**

1. **Uniffi**: Generates bindings for Swift, Kotlin, Python, etc.
2. **wasm-bindgen**: Generates JavaScript/WebAssembly bindings

### What It Contains:

**`src/lib.rs`** (1-3): Uniffi scaffolding

```rust
uniffi::include_scaffolding!("spark_frost");
```

**`src/spark_frost.udl`**: Interface definition (45 lines)

- Defines public API exposed to other languages
- Functions: `frost_nonce`, `sign_frost`, `aggregate_frost`, `construct_node_tx`, etc.
- Types: `KeyPackage`, `SigningCommitment`, `NonceResult`, `TransactionResult`

**WASM-specific exports** (`lib.rs:53-243):

```rust
#[wasm_bindgen]
pub fn wasm_sign_frost(...) -> Result<Vec<u8>, Error> {
    // Wraps core sign_frost with JS-friendly types
}

#[wasm_bindgen]
pub fn wasm_aggregate_frost(...) -> Result<Vec<u8>, Error> {
    // Wraps core aggregate_frost
}
```

**Key Functions Exposed:**

| Function                                   | Purpose                 | Line      |
| ------------------------------------------ | ----------------------- | --------- |
| `frost_nonce`                              | Generate signing nonces | 183       |
| `sign_frost` / `wasm_sign_frost`           | USER role signing       | 197 / 222 |
| `aggregate_frost` / `wasm_aggregate_frost` | Aggregate signatures    | 246 / 307 |
| `construct_node_tx`                        | Build Bitcoin tx        | 386       |
| `construct_refund_tx`                      | Build refund tx         | 458       |
| `encrypt_ecies` / `decrypt_ecies`          | ECIES encryption        | 732 / 740 |
| `get_taproot_pubkey`                       | Taproot key derivation  | 748       |

### Build Process (`build.rs`):

```rust
fn main() {
    uniffi::generate_scaffolding("src/spark_frost.udl").unwrap();
}
```

**Generated Artifacts:**

1. **WASM Module:** `spark_frost.wasm` (for browsers)
2. **JS Bindings:** `spark_frost.js` (TypeScript compatible)
3. **Uniffi Bindings:** Swift/Kotlin/Python bindings (if requested)

### What It Builds:

**For WASM (JavaScript/TypeScript):**

```bash
wasm-pack build --target web
```

**Output:**

- `pkg/spark_frost_bg.wasm` - WebAssembly binary
- `pkg/spark_frost.js` - JavaScript wrapper
- `pkg/spark_frost.d.ts` - TypeScript definitions

**For Native Libraries:**

```bash
cargo build --release
```

**Output:**

- `target/release/libspark_frost.dylib` (macOS)
- `target/release/libspark_frost.so` (Linux)
- `target/release/libspark_frost.dll` (Windows)
- `target/release/libspark_frost.a` (static library)

---

## Build Artifacts Comparison

| Crate                  | Target | Build Command                                 | Primary Artifact                | Secondary Artifacts |
| ---------------------- | ------ | --------------------------------------------- | ------------------------------- | ------------------- |
| **spark-frost**        | Native | `cargo build`                                 | `libspark_frost.rlib`           | None (library only) |
| **spark-frost**        | WASM   | `cargo build --target wasm32-unknown-unknown` | `libspark_frost.rlib`           | None (library only) |
| **spark-frost-signer** | Native | `cargo build --release`                       | `spark-frost-signer` (binary)   | None                |
| **spark-frost-uniffi** | WASM   | `wasm-pack build --target web`                | `spark_frost.wasm` + `.js`      | TypeScript `.d.ts`  |
| **spark-frost-uniffi** | Native | `cargo build --release`                       | `libspark_frost.{so,dylib,dll}` | `.a` static lib     |

---

## Usage in Lightspark Ecosystem

### Architecture Flow:

```
┌────────────────────────────────────────────────────────────┐
│                     Lightspark Platform                    │
├────────────────────────────────────────────────────────────┤
│                                                            │
│  Go Backend (spark/so/)                                    │
│  ├─ Config with IdentityPrivateKey                         │
│  ├─ frost_connection.go                                    │
│  │  └─ gRPC Client ───────────────┐                        │
│  └─ signing_handler.go            │                        │
│                                   │                        │
│                                   v                        │
│  Rust Server (spark-frost-signer) │                        │
│  ├─ Listens on Unix socket        │                        │
│  ├─ Uses spark-frost library      │                        │
│  └─ FrostServiceServer ◄──────────┘                        │
│                                                            │
├────────────────────────────────────────────────────────────┤
│                                                            │
│  TypeScript SDK (sdks/js/)                                 │
│  ├─ Signer class                                           │
│  ├─ Imports spark_frost.js/wasm                            │
│  └─ Calls frost_nonce(), sign_frost(), aggregate_frost()   │
│                                                            │
│  Browser/Node.js                                           │
│  └─ Loads spark_frost.wasm ◄─── spark-frost-uniffi         │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

### Dependency Graph:

```
spark-frost (core library)
    ├──> spark-frost-signer (Go backend)
    │    └─ Builds: spark-frost-signer binary
    │    └─ Runs as: gRPC server process
    │
    └──> spark-frost-uniffi (JS SDK)
         └─ Builds: spark_frost.wasm + .js
         └─ Runs in: Browser/Node.js
```

---

## Key Differences Summary

| Aspect               | spark-frost                   | spark-frost-signer      | spark-frost-uniffi                   |
| -------------------- | ----------------------------- | ----------------------- | ------------------------------------ |
| **Type**             | Library                       | Binary (server)         | Library (FFI)                        |
| **WASM Support**     | ✅ Yes (conditional)          | ❌ No                   | ✅ Yes (wasm-bindgen)                |
| **Native Support**   | ✅ Yes                        | ✅ Yes (only)           | ✅ Yes (uniffi)                      |
| **Consumers**        | Other Rust crates             | Go backend via gRPC     | JS/TS (WASM) + Swift/Kotlin (native) |
| **Exports**          | Rust functions                | gRPC service            | WASM + JS bindings / FFI             |
| **Runtime**          | N/A (compile-time)            | Standalone process      | Browser/Node.js or native apps       |
| **Interface**        | Rust API                      | Protocol Buffers        | UDL + wasm-bindgen                   |
| **Build Output**     | `.rlib`                       | Binary executable       | `.wasm` + `.js` or `.so/.dylib/.dll` |
| **Purpose**          | Shared crypto logic           | Backend signing service | Client-side signing (all platforms)  |
| **Key Dependencies** | prost (WASM) / tonic (native) | tonic, tokio            | wasm-bindgen, uniffi                 |

---

## Conclusion

This architecture demonstrates Rust best practices: modular design, clear separation between library and application code, and support for multiple deployment targets from a single cryptographic core. The three-crate structure allows Lightspark to:

- Share cryptographic logic across platforms
- Deploy backend signing as an isolated service
- Provide client-side signing capabilities in web browsers
- Maintain clear boundaries between infrastructure and application code
