# @buildonspark/spark-sdk

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
