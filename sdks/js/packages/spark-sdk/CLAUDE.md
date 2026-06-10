# Spark SDK (JavaScript/TypeScript)

TypeScript/JavaScript client library for Spark. Supports browser, Node.js, React Native, and Bare runtimes.

## Architecture

### Multi-Platform Support

- **Browser** (`index.browser.ts`) - Web applications with WASM crypto
- **Node.js** (`index.node.ts`) - Server-side with WASM crypto (base64-inlined)
- **React Native** (`index.react-native.ts`) - Mobile via native modules (Kotlin/Swift)
- **Bare** (`bare/index.ts`) - Minimal server runtime

Each platform has specialized crypto bindings in `spark-bindings/`. The `package.json` `"exports"` field routes imports to the correct platform entry point based on the runtime condition (`react-native`, `node`, `import`, `require`).

### Core Components

- **SparkWallet** (`spark-wallet/`) - Main wallet interface
- **Services** (`services/`) - High-level operations (transfers, deposits, tokens)
- **gRPC Client** - Communication with Spark operators
- **WASM/Native Bindings** (`spark-bindings/`) - Platform-specific crypto

## Key Services

- **transfer.ts** - Off-chain Spark transfers, FROST signature coordination
- **deposit.ts** - Bitcoin deposits into Spark
- **lightning.ts** - Lightning Network integration
- **token-transactions.ts** - BTKN token operations
- **signing.ts** - Cryptographic signing operations

## Rust → SDK Binding Pipeline

All FROST cryptographic operations are implemented in Rust and exposed to the SDK through two compilation paths: **WASM** (for Node.js/Browser) and **native bindings via UniFFI** (for React Native iOS/Android).

### Source of Truth

The single Rust crate `signer/spark-frost-uniffi/` contains both `#[wasm_bindgen]` and UniFFI-annotated functions. Core crypto logic lives in `signer/spark-frost/`; the `spark-frost-uniffi` crate is a thin adapter that exposes it to both compilation targets.

```
signer/
├── spark-frost/                    # Core FROST crypto logic (pure Rust)
└── spark-frost-uniffi/
    ├── src/
    │   ├── lib.rs                  # Dual-annotated: #[wasm_bindgen] + uniffi functions
    │   └── spark_frost.udl         # UniFFI interface definition (drives Swift/Kotlin codegen)
    ├── build.rs                    # Calls uniffi::generate_scaffolding() at compile time
    ├── uniffi-bindgen.rs           # CLI entry point for generating Swift/Kotlin bindings
    ├── Cargo.toml                  # crate-type = ["cdylib", "staticlib"], depends on wasm-bindgen + uniffi
    ├── build-bindings.sh           # Builds WASM (nodejs + browser targets)
    ├── build-rn-bindings.sh        # Builds native libs + generates Kotlin/Swift bindings
    └── build-swift.sh              # iOS-only native build
```

### Compilation Path 1: WASM (Node.js + Browser)

**Script:** `signer/spark-frost-uniffi/build-bindings.sh`

```
Rust source (lib.rs with #[wasm_bindgen])
  │
  ├─ wasm-pack --target nodejs  →  wasm/nodejs/wasm_nodejs.{js,wasm,d.ts}
  └─ wasm-pack --target web     →  wasm/browser/wasm_browser.{js,wasm,d.ts}
  │
  └─ yarn patch-wasm  (post-processing)
       ├─ patch-wasm-nodejs.mjs  →  src/spark-bindings/wasm/wasm-nodejs.js
       │   • Converts CJS → ESM
       │   • Inlines .wasm binary as base64 (self-contained, no external file)
       │   • Renames classes to avoid collisions
       │
       └─ patch-wasm-browser.mjs →  src/spark-bindings/wasm/wasm-browser.{js,wasm,d.ts}
           • Fixes WASM file path for SDK directory structure
           • Removes import.meta.url reliance (forces explicit WASM byte loading)
```

**Steps to regenerate WASM bindings:**

```bash
cd signer/spark-frost-uniffi
./build-bindings.sh
```

### Compilation Path 2: Native (React Native iOS + Android)

**Script:** `signer/spark-frost-uniffi/build-rn-bindings.sh`

```
Rust source (lib.rs) + spark_frost.udl
  │
  ├─ uniffi-bindgen generate --language kotlin
  │   → sdks/js/packages/spark-sdk/android/src/main/java/uniffi/uniffi/spark_frost/spark_frost.kt
  │
  ├─ uniffi-bindgen generate --language swift
  │   → sdks/js/packages/spark-sdk/ios/spark_frost.swift
  │
  ├─ cargo build (Android targets: aarch64, armv7, i686, x86_64)
  │   → android/src/main/jniLibs/{arm64-v8a,armeabi-v7a,x86,x86_64}/libuniffi_spark_frost.so
  │     (build-rn-bindings.sh renames libspark_frost.so → libuniffi_spark_frost.so during copy)
  │
  └─ cargo build (iOS targets: arm64, x86_64, arm64-sim) + lipo (universal libs)
      → ios/spark_frostFFI.xcframework/ (ios-arm64, ios-sim, macos slices)
```

**Steps to regenerate native bindings:**

```bash
cd signer/spark-frost-uniffi
./build-rn-bindings.sh    # Full Android + iOS build
./build-swift.sh          # iOS-only shortcut
```

### TypeScript Binding Layer

The `spark-bindings/` directory bridges the compiled artifacts to the SDK:

```
src/spark-bindings/
├── spark-bindings.ts               # SparkFrostBase (abstract class defining the interface)
├── spark-bindings.node.ts          # SparkFrostNodeJS → calls wasm-nodejs.js (sync)
├── spark-bindings.browser.ts       # SparkFrostBrowser → async-initializes WASM, then calls it
├── spark-bindings.react-native.ts  # SparkFrostReactNative → delegates to NativeModules.SparkFrostModule
├── types.ts                        # Shared TypeScript types for the bindings layer
└── wasm/
    ├── wasm-nodejs.js              # Patched WASM module (base64-inlined, ESM — self-contained)
    ├── wasm-nodejs.d.ts            # TypeScript declarations for Node.js WASM
    ├── wasm-nodejs-bg.wasm         # Node.js WASM binary (build artifact only — inlined into wasm-nodejs.js at patch time)
    ├── wasm-nodejs-bg.wasm.d.ts    # TypeScript declarations for Node.js WASM internals
    ├── wasm-browser.js             # Patched WASM loader (ESM)
    ├── wasm-browser.d.ts           # TypeScript declarations for browser WASM
    ├── wasm-browser-bg.js          # Browser WASM glue code
    ├── wasm-browser-bg.wasm        # Browser WASM binary (loaded at runtime)
    └── wasm-browser-bg.wasm.d.ts   # TypeScript declarations for browser WASM internals
```

**Singleton pattern:** Each `index.{platform}.ts` entry point creates the platform-specific `SparkFrost` subclass and registers it via `setSparkFrostOnce()`. All SDK code accesses bindings through `getSparkFrost()`.

**React Native data marshaling:** The RN bridge doesn't support `Uint8Array` or `bigint`. `SparkFrostReactNative` converts `Uint8Array` → `number[]` and `bigint` → `string` before crossing the bridge, then converts back on return.

### React Native Native Modules

The native module bridges connect React Native JS to the UniFFI-generated bindings:

**iOS:**

```
ios/
├── SparkFrostModule.swift          # RCT bridge: unpacks JS params → calls UniFFI Swift functions
├── SparkFrostModule.m              # ObjC RCT_EXTERN_MODULE/METHOD macros
├── SparkFrostModule.h              # ObjC header for SparkFrostModule
├── SparkFrost-Bridging-Header.h    # Swift/ObjC interop header
├── spark_frost.swift               # UniFFI-generated Swift bindings (auto-generated, do not edit)
└── spark_frostFFI.xcframework/     # Pre-compiled Rust static libs
    ├── ios-arm64/                  # Physical iPhones
    ├── ios-arm64_x86_64-simulator/ # iOS Simulator (Apple Silicon + Intel)
    └── macos-arm64_x86_64/         # macOS
```

**Android:**

```
android/src/main/
├── java/com/sparkfrost/
│   ├── SparkFrostModule.kt         # RN bridge: unpacks ReadableMap → calls UniFFI Kotlin functions
│   └── SparkFrostPackage.kt        # Registers module with RN module registry
├── java/uniffi/uniffi/spark_frost/
│   └── spark_frost.kt              # UniFFI-generated Kotlin bindings (auto-generated, do not edit)
└── jniLibs/                        # Pre-compiled Rust shared libs
    ├── arm64-v8a/libuniffi_spark_frost.so
    ├── armeabi-v7a/libuniffi_spark_frost.so
    ├── x86/libuniffi_spark_frost.so
    └── x86_64/libuniffi_spark_frost.so
```

### Adding a New Rust Function to the SDK

To expose a new Rust function across all platforms:

1. **Implement in Rust** (`signer/spark-frost/src/`) — add the core logic
2. **Add wasm_bindgen export** (`signer/spark-frost-uniffi/src/lib.rs`) — add `#[wasm_bindgen]` function
3. **Add UniFFI export** (`signer/spark-frost-uniffi/src/lib.rs`) — add `*_uniffi` variant function
4. **Update UDL** (`signer/spark-frost-uniffi/src/spark_frost.udl`) — declare the function signature and any new types
5. **Regenerate WASM** — run `./build-bindings.sh` from `signer/spark-frost-uniffi/`
6. **Regenerate native bindings** — run `./build-rn-bindings.sh` from `signer/spark-frost-uniffi/`
7. **Add to SparkFrostBase** (`src/spark-bindings/spark-bindings.ts`) — add abstract method
8. **Implement per platform:**
   - `spark-bindings.node.ts` — call the WASM function
   - `spark-bindings.browser.ts` — call the WASM function (with `await this.init()` guard)
   - `spark-bindings.react-native.ts` — call `SparkFrostModule.methodName()` with marshaled params
9. **Update native bridge modules:**
   - `ios/SparkFrostModule.swift` — add `@objc` method that unpacks params and calls UniFFI function
   - `ios/SparkFrostModule.m` — add `RCT_EXTERN_METHOD` declaration
   - `android/.../SparkFrostModule.kt` — add `@ReactMethod` that unpacks params and calls UniFFI function

## Common Workflows

### Making a Transfer

1. Create payment intent
2. Construct HTLC
3. Request operator signatures using FROST
4. Collect threshold signatures
5. Finalize transfer

### Handling Deposits

1. Request deposit address from operator
2. User sends Bitcoin to address
3. SDK monitors for confirmations
4. Operator credits Spark balance

### Working with Tokens

1. Create token
2. Mint tokens to recipients
3. Transfer tokens between users
4. Query token balances

## Platform-Specific Code

When adding platform-specific code:

1. Define common interface in base file
2. Implement for each platform (`.browser.ts`, `.node.ts`, `.react-native.ts`)
3. Export through `index.*.ts` entry points
4. Update build config if needed

For crypto operations specifically, follow the "Adding a New Rust Function" guide above. For non-crypto platform differences (e.g., gRPC transport, storage), use the same `.{platform}.ts` file convention but implement directly in TypeScript.

## Build Commands

```bash
yarn build          # Full production build
yarn build:watch    # Watch mode
yarn test           # Run tests
yarn generate:proto # Regenerate proto types
yarn patch-wasm     # Re-run WASM post-processing (after build-bindings.sh)
```

## Cross-Language Compatibility

The SDK must maintain compatibility with the Go backend:

- Proto hash calculations must match Go implementation
- Signature formats must be compatible
- Byte ordering matters

## React Native Notes

Requires:

- `react-native-get-random-values` for crypto
- Native modules for FROST operations (see native module structure above)
- Pre-compiled native libraries vendored in `ios/` and `android/`
- React Native does NOT use WASM — it bridges directly to Rust via UniFFI-generated Kotlin/Swift

## Browser Notes

- Private keys stored in memory only
- Be mindful of CORS for gRPC-Web
- WASM is loaded async on first crypto operation (lazy initialization)
