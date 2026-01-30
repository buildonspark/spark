# @buildonspark/spark-sdk

## 0.5.8

### Patch Changes

- **Swap flow updates**
  - Swaps now use the `request_swap` API with outbound transfer IDs and multi-target amounts, and the SDK initiates swap transfers with adaptor signatures.
  - Added support for `COUNTER_SWAP_V3` transfers and updated swap processing to use adaptor-added signatures from the swap transfer response.
- **Deposit tree creation API update**
  - Uses `get_signing_commitments` + `finalize_deposit_tree_creation` and sends user signatures/commitments directly (no local signature aggregation).
- **Coop exit signing fix**
  - Added multi-input Taproot sighash support for connector-based refund transactions and include connector transaction data in coop exit requests.
- **Token transaction querying**
  - Split `queryTokenTransactions`
    - `queryTokenTransactionsByTxHashes` for confirming and querying the specific state of transactions
    - `queryTokenTransactionsWithFilters` for querying the network with filters. Includes cursor pagination
  - `queryTokenTransactions` is now deprecated.
- **Utilities and validation**
  - Added `hashstructure` tagged hash helper, validated VSS proof lengths, and skip direct refund tx creation for zero-timelock nodes.

## 0.5.7

### Patch Changes

- - Improvements to build types

## 0.5.6

### Patch Changes

- ### Direct Refund Transaction Fix for Zero Nodes

  Fixed an issue where the SDK was creating `directRefundTx` for nodes with `timelock=0` (zero nodes), which caused backend validation errors. The SDK now correctly skips parsing `directNodeTx` for zero nodes, matching the server-side validation logic.

  ### Improved Unilateral Exit Fee Estimation

  Reduced unilateral exit fees by an average of 56% by improving transaction size estimation. The previous logic was overly conservative; this change uses TX-specific fields for size calculation instead of generic estimates.

  ### Token Output Manager

  Added a dedicated `TokenOutputManager` class for fine-grained token output locking and management.

  **Key improvements:**
  - Per-output locking instead of global mutex, enabling concurrent token operations
  - Per-token output syncing with targeted sync support
  - Partial output updates: update outputs for specific tokens without replacing all data
  - Per-token pending-withdrawal tracking
  - Automatic lock expiry mechanism (default: 30 seconds, configurable via `tokenOutputLockExpiryMs`)

## 0.5.5

### Patch Changes

- - Fix: ensure direct-from-CPFP refund transactions are signed even when directTx is missing.
  - Improvement: increase embedded WASM stack size to 3MB (bindings regenerated) to reduce stack overflow failures.

## 0.5.4

### Patch Changes

- **Token transactions utilities**
  - Added `broadcastTokenTransactionDetailed()` method that returns detailed transaction information including `tokenIdentifier`, `commitStatus`, `commitProgress`, `finalTokenTransaction`, and `finalTokenTransactionHash`
  - Added `broadcastTokenTransactionV3Detailed()` method that returns the full `BroadcastTransactionResponse` including `tokenIdentifier` and `finalTokenTransaction`
  - Exported `hashFinalTokenTransaction` utility function from package utils for computing transaction hashes
  - Updated existing `broadcastTokenTransaction()` and `broadcastTokenTransactionV3()` to internally use the detailed methods and extract just the transaction hash for backward compatibility

## 0.5.3

### Patch Changes

- - Transfers: SparkWallet.getTransfers() now supports optional time filtering via createdAfter?: Date or createdBefore?: Date (mutually exclusive; providing both throws).
  - TypeScript packaging: subpath exports now include explicit types mappings (notably @buildonspark/spark-sdk/types, /test-utils, /proto/spark, /proto/spark_token) to improve TS type resolution across ESM/CJS.

## 0.5.2

### Patch Changes

- **getBalance**
  - Return extraMetadata in token metadata fields on getBalance()

  **error handling**
  - Add a public getContext() method

## 0.5.1

### Patch Changes

- **Retry gateway errors**
  - Retry on 502, 503, 504 errors as these can temporarily occur when the service is autoscaling
  - Includes exponential backoff with a max of 5 retries and a base delay of 1000ms

  **Direct refund transactions**
  - Do not construct the directRefundTx if the node timelock is 0

  **Lightning**
  - Added missing lightning send status

  **Token transaction v3**
  - Default to token transaction v3

  **Query token transcations**
  - Lower the default page size to 50

  **Miscellaneous tweaks**
  - Remove unused proof map in claim transfer flow
  - Only derive the public key once when signing
  - Update sats transfer flows to use Promise.all instead of looping through sequentially
  - Update grpc HTTP/2 header parsing

## 0.5.0

### Minor Changes

- - **Error model & handling**
    - All public errors now derive from `SparkError`, with four concrete types: `SparkError`, `SparkValidationError`, `SparkRequestError`, `SparkAuthenticationError`; older classes like `NetworkError`, `RPCError`, `ValidationError`, `ConfigurationError` are no longer thrown.
    - `SparkError` accepts a flexible context object where `error` can be any `unknown`; it normalizes and stores the original error and produces concise, structured messages and `toJSON` output.
    - All public `SparkWallet` methods are now centrally wrapped, so anything thrown by internal code or third‑party libraries is surfaced as a `SparkError` (or subclass) with consistent context, making `instanceof` checks and top‑level error handling reliable and uniform.
    - OTEL tracing is now **Node‑only**: client‑side tracing is only initialized in `SparkWalletNodeJS`. Error messages in Node may include a `traceId`; browser/React Native environments no longer attempt to set up OTEL spans.
  - **Simplified error types across the SDK**
    - Service layers (GraphQL client, connection, signing, deposit, transfer, lightning, coop‑exit, token‑transactions, etc.) now consistently throw `SparkRequestError` for request/transport/API failures and `SparkValidationError` for invalid inputs or state.
    - Error context now uses strongly‑typed `operation` and `method` where applicable (e.g. RPC name and HTTP method), improving debuggability without requiring multiple bespoke error classes.
  - **Token transaction v3 support**
    - A new config flag `tokenTransactionVersion?: "V2" | "V3"` has been added (default `"V2"`).
    - Token‑related APIs that previously produced v2 `TokenTransaction` objects can now operate in a v3 mode that builds `PartialTokenTransaction` plus metadata and calls a new `broadcastTokenTransactionV3` path.
    - When `tokenTransactionVersion` is `"V3"`, token transfers/mints/creates will:
      - Construct partial token transactions including network, operator keys, withdraw bond parameters, and validity duration.
      - Sign and broadcast via `broadcast_transaction` on the token service, which finalizes the transaction on the coordinator side before returning a hash.
  - **HTLC (hash time‑locked contract) APIs**
    - `SparkWallet` now exposes a full HTLC workflow:
      - `createHTLC({ receiverSparkAddress, amountSats, preimage?, expiryTime })` to open a HTLC using your leaves, validating balance and expiry, and driving the swap flow using a Lightning service.
      - `claimHTLC(preimage: string)` to provide a preimage, complete the HTLC, and optionally auto‑claim when you are the receiver.
      - `getHTLCPreimage(transferID: string)` to deterministically compute a preimage from a transfer ID using a dedicated HTLC preimage key.
      - `createHTLCSenderSpendTx(...)` / `createHTLCReceiverSpendTx(...)` to build sender/receiver on‑chain spend transactions, including signing and final witness construction.
      - `queryHTLC({ paymentHashes?, status?, transferIds?, matchRole?, limit?, offset? })` to query HTLCs via the gRPC API.
    - `SparkSigner` now derives and stores an `htlcPreimageKey` when you call `createSparkWalletFromSeed`, and exposes `htlcHMAC(transferID)`—this underpins `getHTLCPreimage` so preimages are reproducible from wallet state.
    - HTLC timelock helpers and refund‑path helpers (`getNextHTLCTransactionSequence`, refund‑TX utilities) enforce valid timelock sequences, throwing `SparkValidationError` if the next step falls outside allowed intervals.
  - **HTLC querying validation (client‑side)**
    - `SparkWallet.queryHTLC` now validates:
      - `limit` must be between **1 and 100** if provided, otherwise a `SparkValidationError` is thrown.
      - `offset` must be **non‑negative** if provided, otherwise a `SparkValidationError` is thrown.
    - This prevents sending malformed pagination parameters to the server and surfaces clear, local validation errors instead.
  - **Proto & request validation updates**
    - In the token proto, `TokenTransaction.validity_duration_seconds` is now **optional**; the JS client continues to set it, but it no longer has to be present in all wire payloads.
    - `QueryTokenTransactionsRequest` adds validation for pagination fields:
      - `limit` must be in [0, 1000].
      - `offset` must be ≥ 0.
    - HTLC‑related proto messages and query types were updated so preimage requests can be made without always supplying sender public keys, matching the new `queryHTLC` API surface.
  - **(Rust bindings)**
    - `TransactionResult` now exposes:
      - `tx: Uint8Array`
      - `sighash: Uint8Array`
      - `inputs: TxIn[]` where `TxIn` includes a `sequence` field.
    - Allows you to inspect per‑input sequences/timelocks when calling helpers like `construct_node_tx`, `construct_refund_tx`, `construct_split_tx`, and `construct_direct_refund_tx`.
  - **Miscellaneous behavioural tweaks**
    - HTLC and refund‑sequence utilities now enforce that certain claim‑side timelocks land on specific X00/X50 boundaries, throwing `SparkValidationError` on invalid sequences.
    - Network/RPC failures in token and UTXO workflows consistently wrap unknown/third‑party errors into `SparkError` subclasses with preserved original error information and structured context.

## 0.4.7

### Patch Changes

- - Fix variable name in android module

## 0.4.6

### Patch Changes

- - Use generated bindings for ecies encryption/decryption

## 0.4.5

### Patch Changes

- - Display more readable error client side when concurrency limit is reached
  - Update retryable gRPC statuses
  - Remove unnecessary fields from GQL operations (preferred currency options)
  - Use web optimized bindings in web and browser extension contexts
  - Add additional info to top level error messages

## 0.4.4

### Patch Changes

- - Regenerate bindings for transaction construction to use standard spark transaction v3 version.

## 0.4.3

### Patch Changes

- - Enable Spark invoices.
  - Address APIs now return spark1 addresses.
  - Lightning flow updated to v3 endpoint
  - Improve leaf optimization

## 0.4.2

### Patch Changes

- - readability changes to test files
  - loadtest CLI
  - enable spark invoices
  - return spark1 address
  - update lightning flow to use v3 endpoint
  - match rust-toolchain.toml with signer

## 0.4.1

### Patch Changes

- - Prevent duplicate otel headers in requests
  - Update default leaf optimization mode for faster transfers
  - Added token output optimization functionality to consolidate token
    outputs when they exceed a configurable threshold.
  - Added tokenOptimizationOptions to SparkWallet config:

  ```js
  const wallet = await SparkWallet.initialize({
    options: {
      tokenOptimizationOptions: {
        enabled: true,
        intervalMs: 300000,
        minOutputsThreshold: 50,
      },
    },
  });
  ```

  - Add BareHttpTransport to unref sockets for responseStream RPCs. Enable process to exit after abort signal received from cleanupConnections.

## 0.4.0

### Minor Changes

- - Export taproot signer for react-native
  - Improvements to internal leaf optimization
    - Enables manual leaf optimization
    - Make optimization strategy configurable
  - Upgrade Android library to support 16kb page size
  - Fix for incorrectly parsed binary headers for react-native

## 0.3.9

### Patch Changes

- - Update transaction construction
  - Replace refresh/extend with improved renew leaf flow

## 0.3.8

### Patch Changes

- - Update lighting invoice payment to support new refund transactions
  - Lower target for RN to es2020
  - Reuse gRPC channels across all ConnectionManager instances for better performance in Node.js

## 0.3.7

### Patch Changes

- - Direct exports from @buildonspark/spark-sdk support in React Native
  - Default to ReactNativeSparkSigner in React Native if not provided to SparkWallet.initialize
  - Add leaf optimization strategies

## 0.3.6

### Patch Changes

- Bug fixes

## 0.3.5

### Patch Changes

- - Fix 0 amount invoice validation

## 0.3.4

### Patch Changes

- - Remove v0 token transactions in favor of v1
  - Add method to query spark invoices
  - Support decoding spark1 addresses
  - Validate invoice details returned from SSP in lightning receive flow

## 0.3.3

### Patch Changes

- - Revert "Fix timelock value in SDK" temporarily

## 0.3.2

### Patch Changes

- - Ensure android/build folder excluded from publish

## 0.3.1

### Patch Changes

- - Temporarily revert address prefix change

## 0.3.0

### Minor Changes

- - Update the spark address prefix to spark1
  - Breaking: SparkWallet.fulfillSparkInvoice return type and multi-invoice support: returns FulfillSparkInvoiceResponse; supports sats and multiple token assets.
    - Extends the functionality of fulfillSparkInvoice to support multiple concurrent sats transfers and add support for multiple fulfilling invoices for multiple assets. A user can pass as many invoices to fulfillSparkInvoice as they want and the function will attempt to fulfill all of them.
    - For token transactions, it will batch the token transactions by token identifier.
    - For sats transactions, it will pre-select the leaves, build the transfers, and send them all off to the SO concurrently.
  - Create Spark invoices from the wallet
    SparkWallet.createSatsInvoice(...), SparkWallet.createTokensInvoice(...)
  - transfer(...) now throws if given a Spark invoice address, with guidance to use fulfillSparkInvoice
  - Fix: Recover leaves if a transfer was already claimed
  - Timelock sequence fix. Removed setting the 30th bit in sequence values; corrected locktime behavior
  - Browser extension fixes: globalThis.crypto reference fix; globalThis.fetch now bound correctly

## 0.2.13

### Patch Changes

- - Add create and broadcast static deposit refund tx
  - Update tests to be less flaky

## 0.2.12

### Patch Changes

- - Update static deposit address generation rpc method
  - Add exclude claimed input for get utxo for address query
  - Bug fixes

## 0.2.11

### Patch Changes

- - Update integration tests
  - Add logging class to SDK
  - Bug fixes

## 0.2.10

### Patch Changes

- -- Bug fix for queryNodes

## 0.2.9

### Patch Changes

- -- return offset from queryTokenTransactions

## 0.2.8

### Patch Changes

- -- Added spark invoice support for token transfers
  -- Added support for initialization SparkWallet with pre-existing keys
  -- Return bare info in x-client-env
  -- Improved test coverage for multiple coordinators
  -- Improved retry mechanism for transfer claim
  -- Improved error handling for alreaday exists

## 0.2.7

### Patch Changes

- - Removed TokenSigner from top-level exports (index.ts/index.node.ts).
  - Replaced SparkWallet.createSparkPaymentIntent(...) with createSatsInvoice(...) and createTokensInvoice(...).
  - utils/address invoice schema changed:
    - PaymentIntentFields → SparkInvoiceFields with versioned structure and union paymentType (tokens/sats), optional senderPublicKey, expiryTime, and optional signature.
    - encodeSparkAddress now takes sparkInvoiceFields (was paymentIntentFields).
    - decodeSparkAddress now returns sparkInvoiceFields (was paymentIntentFields) with the new shape.
    - New exported helper: validateSparkInvoiceFields(...).
  - Removed ./proto/lrc20 export and dropped LRC20-specific re-exports (e.g., MultisigReceiptInput), along with the @buildonspark/lrc20-sdk dependency.
  - New @buildonspark/spark-sdk/bare entrypoint for the Bare runtime (exports SparkWallet, utils, signer, and getLatestDepositTxId).
  - Added top-level export of IKeyPackage type.

## 0.2.6

### Patch Changes

- -- Opentelemetry improvements
  -- Utility function to decode bech32mtokenidentifiers to raw token identifiers
  -- Add userRequest to transfer in getTransfer() if it exists
  -- Fixes to getIssuerTokenIdentifier() types
  -- Migrates some internal filtering logic to key on token identifiers
  -- Testing improvements
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.61

## 0.2.5

### Patch Changes

- Attach the SSP request object to spark transfer if it exists
- Update static deposit refund flow to take sats per vbyte
- Allow the creation of multiple refunds in static deposit refund flow
- Add new function to claim a static deposit while specifying a max fee

## 0.2.4

### Patch Changes

- Add watchtower supported transactions on leaves
- Improvements to otel wrapping
- Fix resoluation of SparkWallet for Node.js

## 0.2.3

### Patch Changes

- -leaf key improvements
  -token improvements

## 0.2.2

### Patch Changes

- Export stateless signer from signer.ts

## 0.2.1

### Patch Changes

- tokens changes
  - Bech32mTokenIdentifier prefix change from "btk" -> "btkn"

- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.60

## 0.2.0

### Minor Changes

- Remove key map state from signer interface.
  - The SDK now passes around the derivation details regarding the signing key instead of forcing the signer to maintain a pubkey to privkey map
- Parameter changes to transferTokens() and batchTransferTokens()
- Parameter changes to queryTokenTransactions()
- Replaced getIssuerTokenInfo() with getIssuerTokenMetadata()
- Rename HumanReadableTokenIdentifier to Bech32mTokenIdentifier
  - Bech32mTokenIdentifier must now be passed as tokenIdentifier in transferTokens() batchTransferTokens

## 0.1.47

### Patch Changes

- - Move some less common imports to root. If you were using these import paths please update them to import the same objects from @buildonspark/spark-sdk instead:
    - @buildonspark/spark-sdk/address
    - @buildonspark/spark-sdk/signer
    - @buildonspark/spark-sdk/services/wallet-config
    - @buildonspark/spark-sdk/utils
    - @buildonspark/spark-sdk/token-transactions
    - @buildonspark/spark-sdk/config
    - @buildonspark/spark-sdk/lrc-connection
    - @buildonspark/spark-sdk/connection

## 0.1.46

### Patch Changes

- Upgrades to token transfers
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.59

## 0.1.45

### Patch Changes

- - Update parsing of spark address from fallback_adress to route_hints
  - Update sdk checks on transactions
  - Add token features
  - Improve stability and cleanup

## 0.1.44

### Patch Changes

- Add fee estimate quote for coop exit requests
- Allow coop exit fees to be taken from wallet balance instead of withdrawal amount if specified

## 0.1.43

### Patch Changes

- - Improve serialization for some error context values (be15609)
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.58

## 0.1.42

### Patch Changes

- - Add X-Client-Env with SDK and env information
  - Make use of Swap V2 endpoints in coop exit + lightning sends

## 0.1.41

### Patch Changes

- Add a method to fetch a single transfer
- Add a method to fetch transfers from the SSP
- Add TaprootOutputKeysGenerator in signer

## 0.1.40

### Patch Changes

- Improved support for unilateral exits

## 0.1.39

### Patch Changes

- - Update leaves swap to v2

## 0.1.38

### Patch Changes

- - Export errors and utils from /native

## 0.1.37

### Patch Changes

- - Return static deposit address instead of throwing error when trying to create after first time.
  - Handle window undefined in buffer polyfill.
  - Add static deposit transactions to get all transaction request.

## 0.1.36

### Patch Changes

- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.57

## 0.1.35

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.56

## 0.1.34

### Patch Changes

- Add ability to create invoice for another spark user
- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.55

## 0.1.33

### Patch Changes

- - Remove some unneeded files to reduce package size
  - Include Android binding libs

## 0.1.32

### Patch Changes

- - Added HDKeyGenerator interface and default implementation to allow for easy custom derivation path changes

## 0.1.31

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.54

## 0.1.30

### Patch Changes

- Remove LRC20 Proto Generation
- Update to leaf optimizations
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.53

## 0.1.29

### Patch Changes

- - react-native moved to peerDependencies
  - Error messages now include more context and the original error message.
  - Fix self transfers with query to pending transactions.
  - For RN Android, improved typings and resolve issue where calls to SparkFrostModule were hanging.
  - Export getLatestDepositTxId from /native
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.52

## 0.1.28

### Patch Changes

- - Separate entry point for NodeJS environments and refactor some NodeJS dependencies out
  - Added `LEAVES_LOCKED` status to `SparkLeavesSwapRequestStatus` enum.
  - Added support for `GetTransferPackageSigningPayload` in `SparkTransferToLeavesConnection`.
  - Added GraphQL for managing static deposit addresses.
  - Begin adding "Transfer V2", a new mechanism for handling transfers.
    - A new method `sendTransferWithKeyTweaks` added to `TransferService`.
    - SparkWallet primary transfer initiation now utilizes this V2 flow.
  - Export the `createDummyTx` function from WASM bindings. Primarily for testing or example purposes.
  - The `swapLeaves` method in `SparkWallet` now processes leaves in batches of 100, potentially improving performance and reliability for operations involving a large number of leaves.
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.51

## 0.1.27

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.50

## 0.1.26

### Patch Changes

- - Export ReactNativeSigner as DefaultSparkSigner from /native

## 0.1.25

### Patch Changes

- - Only import @opentelemetry in NodeJS

## 0.1.24

### Patch Changes

- - Add tracer
  - Token transfer with multiple outputs

## 0.1.23

### Patch Changes

- Use browser module override for nice-grpc
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.49

## 0.1.22

### Patch Changes

- Update homepage URL
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.48

## 0.1.21

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.47

## 0.1.20

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.46

## 0.1.19

### Patch Changes

- React Native support
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.45

## 0.1.18

### Patch Changes

- - Polyfill crypto for React Native support

## 0.1.17

### Patch Changes

- - Removed the nice-grpc-web alias from bundling configuration
  - Refactored ConnectionManager and gRPC client code in src/services/connection.ts to support Node vs Web channels uniformly
  - Changed rawTx serialization to toBytes(true) for script sig in DepositService
  - Moved isHermeticTest helper from src/tests/test-util.ts to src/tests/isHermeticTest.ts
  - Wrapped claimTransfers in SparkWallet (src/spark-wallet.ts) with a try/catch, improved retry logic, and updated return type to an array of claimed-ID strings
  - Updated utils in src/utils/bitcoin.ts and src/utils/network.ts to use the new serialization methods and constants paths
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.44

## 0.1.16

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.43

## 0.1.15

### Patch Changes

- - Fixed secret splitting by passing threshold (instead of threshold - 1) to the polynomial generator.

## 0.1.14

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.42

## 0.1.13

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.41

## 0.1.12

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.40

## 0.1.11

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.39

## 0.1.10

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.38

## 0.1.9

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.37

## 0.1.8

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.36

## 0.1.7

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.35

## 0.1.6

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.34

## 0.1.5

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.33

## 0.1.4

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.32

## 0.1.3

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.31

## 0.1.2

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.30

## 0.1.1

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.29

## 0.1.0

### Minor Changes

- - SparkServiceClient.query_all_transfers request format has changed to TransferFilter type

## 0.0.30

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.28

## 0.0.29

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.27

## 0.0.28

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.26

## 0.0.27

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.25

## 0.0.26

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.24

## 0.0.25

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.23

## 0.0.24

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.22

## 0.0.23

### Patch Changes

- CJS support and package improvements
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.21

## 0.0.22

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.20

## 0.0.21

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.19

## 0.0.20

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.18

## 0.0.19

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.17

## 0.0.18

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.16

## 0.0.17

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.15

## 0.0.16

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.14

## 0.0.15

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.13

## 0.0.14

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.12

## 0.0.13

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.11

## 0.0.12

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.10

## 0.0.11

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.9

## 0.0.10

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.8

## 0.0.9

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.7

## 0.0.8

### Patch Changes

- Fixes

## 0.0.7

### Patch Changes

- Fixes

## 0.0.6

### Patch Changes

- Fixes

## 0.0.4

### Patch Changes

- Fixes

## 0.0.3

### Patch Changes

- Fixes

## 0.0.2

### Patch Changes

- Fixes

## 0.0.1

### Patch Changes

- Fixes
