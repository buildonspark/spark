# Spark SDK (JavaScript/TypeScript)

TypeScript/JavaScript client library for Spark. Supports browser, Node.js, React Native, and Bare runtimes.

## Architecture

### Multi-Platform Support

- **Browser** (`index.browser.ts`) - Web applications with WASM crypto
- **Node.js** (`index.node.ts`) - Server-side with native crypto
- **React Native** (`index.react-native.ts`) - Mobile applications
- **Bare** (`bare/index.ts`) - Minimal server runtime

Each platform has specialized crypto bindings in `spark-bindings/`.

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
4. Update build config in `tsup.config.ts`

## Build Commands

```bash
yarn build          # Full production build
yarn build:watch    # Watch mode
yarn test           # Run tests
yarn generate:proto # Regenerate proto types
```

## Cross-Language Compatibility

The SDK must maintain compatibility with the Go backend:

- Proto hash calculations must match Go implementation
- Signature formats must be compatible
- Byte ordering matters

## React Native Notes

Requires:

- `react-native-get-random-values` for crypto
- Native modules for FROST operations
- Special build configuration

## Browser Notes

- Private keys stored in memory only
- Be mindful of CORS for gRPC-Web
