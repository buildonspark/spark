# @buildonspark/issuer-sdk

## 0.1.4

### Patch Changes

- ## Multi-Token Issuance Support

  Introduces support for issuer wallets to create and manage multiple tokens. Each token is uniquely identified by a bech32m token identifier derived from the token's creation parameters.

  **Deprecated Methods**

  The following methods are now marked deprecated and will throw `SparkValidationError` when multiple tokens exist for an issuer. These methods will be removed in a future major version:
  - `getIssuerTokenBalance()` - Use `getIssuerTokenBalances()` instead
  - `getIssuerTokenMetadata()` - Use `getIssuerTokensMetadata()` instead
  - `getIssuerTokenIdentifier()` - Use `getIssuerTokenIdentifiers()` instead
  - `mintTokens(amount)` - Use `mintTokens({ tokenAmount, tokenIdentifier })` instead
  - `burnTokens(amount, selectedOutputs?)` - Use `burnTokens({ tokenAmount, tokenIdentifier, selectedOutputs })` instead
  - `freezeTokens(sparkAddress)` - Use `freezeTokens({ tokenIdentifier, sparkAddress })` instead
  - `unfreezeTokens(sparkAddress)` - Use `unfreezeTokens({ tokenIdentifier, sparkAddress })` instead

  ### New Features

  **Token Creation with Identifier**

  `createToken()` now accepts an optional `returnIdentifierForCreate` parameter to return both the transaction hash and token identifier:

  ```ts
  const result = await wallet.createToken({
    tokenName: "MyToken",
    tokenTicker: "MTK",
    decimals: 8,
    maxSupply: 1000000n,
    isFreezable: false,
    returnIdentifierForCreate: true, // Returns TokenCreationDetails
  });
  ```

  **Return issuer info for all issuer tokens**
  - `getIssuerTokenBalances()` - Returns an array of token balances for all tokens owned by the issuer
  - `getIssuerTokensMetadata()` - Returns an array of metadata for all tokens created by the issuer
  - `getIssuerTokenIdentifiers()` - Returns an array of bech32m token identifiers for all issuer tokens

  **Method Overloads**

  All token operation methods now support optional `tokenIdentifier` parameters:

  ```ts
  // Mint tokens for a specific token
  await wallet.mintTokens({
      tokenAmount: 1000n,
      tokenIdentifier: "btkn1..."
  });

  // Burn tokens for a specific token
  await wallet.burnTokens({
      tokenAmount: 500n,
      tokenIdentifier: "btkn1...",
      selectedOutputs: [...] // optional
  });

  // Freeze tokens for a specific token and address
  await wallet.freezeTokens({
      tokenIdentifier: "btkn1...",
      sparkAddress: "spark1..."
  });

  // Unfreeze tokens for a specific token and address
  await wallet.unfreezeTokens({
      tokenIdentifier: "btkn1...",
      sparkAddress: "spark1..."
  });
  ```

  ### Backward Compatibility

  Legacy method signatures continue to work for issuers with a single token. When multiple tokens are detected, these methods will throw descriptive errors guiding developers to use the new multi-token methods.

  ### Migration Guide

  **For Single-Token Issuers**

  Existing code consuming older SDK versions will continue to work.

  **For Multi-Token Issuers**

  Update your code to use the new multi-token methods:

  ```ts
  // Before
  await wallet.mintTokens(100n);

  // After
  const metadata = await wallet.getIssuerTokensMetadata();
  const tokenIdentifier = metadata[1].bech32mTokenIdentifier;
  await wallet.mintTokens({
    tokenAmount: 100n,
    tokenIdentifier: tokenIdentifier,
  });
  ```

- Updated dependencies
  - @buildonspark/spark-sdk@0.5.4

## 0.1.3

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.5.3

## 0.1.2

### Patch Changes

- **getIssuerTokenMetadata()**
  - Convert extraMetadata from Buffer to Uint8Array for better cross-platform compatibility. Returns undefined when no metadata is present instead of an empty buffer on getIssuerTokenMetadata()

- Updated dependencies
  - @buildonspark/spark-sdk@0.5.2

## 0.1.1

### Patch Changes

- **Token transaction v3**
  - Updated test configs and expanded test coverage to include both v2 and v3 token transactions

  **Extra metadata field available when creating new tokens**
  - Added a field to accept additional bytes data to attach to token creates

- Updated dependencies
  - @buildonspark/spark-sdk@0.5.1

## 0.1.0

### Minor Changes

- - **Entry points & module resolution**
    - The package now provides distinct entrypoints:
      - `index.browser` (default `module`/`import` for browser/bundlers).
      - `index.node` for Node environments.
      - `index.react-native` for React Native.
    - The `exports` map was simplified and modernized so:
      - `import { IssuerSparkWallet } from "@buildonspark/issuer-sdk"` resolves correctly in Node ESM, bundlers, and React Native without manual path selection.
      - Type definitions for browser/Node/React Native are correctly wired.
  - **Shared API surface**
    - A new `index-shared` consolidates type and helper re‑exports:
      - Re‑exports signer types (`DefaultSparkSigner`, `UnsafeStatelessSparkSigner`, `SparkSigner` and related Frost types).
      - Re‑exports `WalletConfig`, `ConfigOptions`, and issuer‑specific types (`IssuerTokenMetadata`, etc.).
    - Public API usage for typical consumers (importing `IssuerSparkWallet` and issuer types) remains the same; only the internal file layout and `exports` map changed.
  - **Token transaction v3 support**
    - `IssuerTokenTransactionService` gained:
      - `constructPartialCreateTokenTransaction(...)` and `constructPartialMintTokenTransaction(...)` that build v3 `PartialTokenTransaction` objects with metadata and withdraw parameters.
      - These complement the existing v2 `constructCreateTokenTransaction` and `constructMintTokenTransaction`.
    - `IssuerSparkWallet.createToken` and `mintTokens` now:
      - Check `config.getTokenTransactionVersion()`.
      - Use v2 full transactions and `broadcastTokenTransaction(...)` when `"V2"`.
      - Use v3 partial transactions and `broadcastTokenTransactionV3(...)` when `"V3"`.
  - **Error and method wrapping**
    - Issuer services and `IssuerSparkWallet` now emit the same simplified `SparkError` family used in `spark-sdk`, aligning error handling semantics for issuers with the core wallet.
    - Public issuer wallet methods are wrapped via `SparkWallet.wrapMethod`, so they benefit from the centralized error‑wrapping and consistent error surface.

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.5.0

## 0.0.110

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.4.7

## 0.0.109

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.4.6

## 0.0.108

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.4.5

## 0.0.107

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.4.4

## 0.0.106

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.4.3

## 0.0.105

### Patch Changes

- - readability changes to test files
  - loadtest CLI
  - enable spark invoices
  - return spark1 address
  - update lightning flow to use v3 endpoint
  - match rust-toolchain.toml with signer
- Updated dependencies
  - @buildonspark/spark-sdk@0.4.2

## 0.0.104

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.4.1

## 0.0.103

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.4.0

## 0.0.102

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.3.9

## 0.0.101

### Patch Changes

- - Lower target for RN to es2020
- Updated dependencies
  - @buildonspark/spark-sdk@0.3.8

## 0.0.100

### Patch Changes

- - Fix: replacement of lossy comparison for sorting token outputs
  - Added React Native support and export directly from @buildonspark/issuer-sdk in RN
- Updated dependencies
  - @buildonspark/spark-sdk@0.3.7

## 0.0.99

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.3.6

## 0.0.98

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.3.5

## 0.0.97

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.3.4

## 0.0.96

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.3.3

## 0.0.95

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.3.2

## 0.0.94

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.3.1

## 0.0.93

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.3.0

## 0.0.92

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.2.13

## 0.0.91

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.2.12

## 0.0.90

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.2.11

## 0.0.89

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.2.10

## 0.0.88

### Patch Changes

- -- return offset from queryTokenTransactions
- Updated dependencies
  - @buildonspark/spark-sdk@0.2.9

## 0.0.87

### Patch Changes

- -- Added spark invoice support for token transfers
  -- Added support for initialization SparkWallet with pre-existing keys
  -- Return bare info in x-client-env
  -- Improved test coverage for multiple coordinators
  -- Improved retry mechanism for transfer claim
  -- Improved error handling for alreaday exists
- Updated dependencies
  - @buildonspark/spark-sdk@0.2.8

## 0.0.86

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.2.7

## 0.0.85

### Patch Changes

- -- Opentelemetry improvements
  -- Utility function to decode bech32mtokenidentifiers to raw token identifiers
  -- Add userRequest to transfer in getTransfer() if it exists
  -- Fixes to getIssuerTokenIdentifier() types
  -- Migrates some internal filtering logic to key on token identifiers
  -- Testing improvements
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.61
  - @buildonspark/spark-sdk@0.2.6

## 0.0.84

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.2.5

## 0.0.83

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.2.4

## 0.0.82

### Patch Changes

- -leaf key improvements
  -token improvements
- Updated dependencies
  - @buildonspark/spark-sdk@0.2.3

## 0.0.81

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.2.2

## 0.0.80

### Patch Changes

- tokens changes
  - Bech32mTokenIdentifier prefix change from "btk" -> "btkn"

- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.60
  - @buildonspark/spark-sdk@0.2.1

## 0.0.79

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.2.0

## 0.0.78

### Patch Changes

- - Renamed getIssuerTokenInfo() to getIssuerTokenMetadata() to better reflect its purpose
  - Renamed fields to match the new API response (e.g., tokenSymbol → tokenTicker, tokenDecimals → decimals)
- Updated dependencies
  - @buildonspark/spark-sdk@0.1.47

## 0.0.77

### Patch Changes

- Upgrades to token transfers
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.59
  - @buildonspark/spark-sdk@0.1.46

## 0.0.76

### Patch Changes

- - Update parsing of spark address from fallback_adress to route_hints
  - Update sdk checks on transactions
  - Add token features
  - Improve stability and cleanup
- Updated dependencies
  - @buildonspark/spark-sdk@0.1.45

## 0.0.75

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.44

## 0.0.74

### Patch Changes

- Updated dependencies
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.58
  - @buildonspark/spark-sdk@0.1.43

## 0.0.73

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.42

## 0.0.72

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.41

## 0.0.71

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.40

## 0.0.70

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.39

## 0.0.69

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.38

## 0.0.68

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.37

## 0.0.67

### Patch Changes

- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.57
  - @buildonspark/spark-sdk@0.1.36

## 0.0.66

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.56
  - @buildonspark/spark-sdk@0.1.35

## 0.0.65

### Patch Changes

- Add signer interface
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.55
  - @buildonspark/spark-sdk@0.1.34

## 0.0.64

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.33

## 0.0.63

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.32

## 0.0.62

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.54
  - @buildonspark/spark-sdk@0.1.31

## 0.0.61

### Patch Changes

- Update issuer-wallet directory
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.53
  - @buildonspark/spark-sdk@0.1.30

## 0.0.60

### Patch Changes

- - Proto files for lrc20 monitoring operations have been moved to @buildonspark/issuer-sdk/proto/lrc20
  - The monitoring operations have been modified to accept either strings or enums, generated from protos
- Updated dependencies
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.52
  - @buildonspark/spark-sdk@0.1.29

## 0.0.59

### Patch Changes

- - Fixes
- Updated dependencies
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.51
  - @buildonspark/spark-sdk@0.1.28

## 0.0.58

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.50
  - @buildonspark/spark-sdk@0.1.27

## 0.0.57

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.26

## 0.0.56

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.25

## 0.0.55

### Patch Changes

- - Add tracer
  - Token transfer with multiple outputs
- Updated dependencies
  - @buildonspark/spark-sdk@0.1.24

## 0.0.54

### Patch Changes

- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.49
  - @buildonspark/spark-sdk@0.1.23

## 0.0.53

### Patch Changes

- Update homepage URL
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.48
  - @buildonspark/spark-sdk@0.1.22

## 0.0.52

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.47
  - @buildonspark/spark-sdk@0.1.21

## 0.0.51

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.46
  - @buildonspark/spark-sdk@0.1.20

## 0.0.50

### Patch Changes

- React Native support
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.45
  - @buildonspark/spark-sdk@0.1.19

## 0.0.49

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.18

## 0.0.48

### Patch Changes

- Updated dependencies
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.44
  - @buildonspark/spark-sdk@0.1.17

## 0.0.47

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.43
  - @buildonspark/spark-sdk@0.1.16

## 0.0.46

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.15

## 0.0.45

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.42
  - @buildonspark/spark-sdk@0.1.14

## 0.0.44

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.41
  - @buildonspark/spark-sdk@0.1.13

## 0.0.43

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.40
  - @buildonspark/spark-sdk@0.1.12

## 0.0.42

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.39
  - @buildonspark/spark-sdk@0.1.11

## 0.0.41

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.38
  - @buildonspark/spark-sdk@0.1.10

## 0.0.40

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.37
  - @buildonspark/spark-sdk@0.1.9

## 0.0.39

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.36
  - @buildonspark/spark-sdk@0.1.8

## 0.0.38

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.35
  - @buildonspark/spark-sdk@0.1.7

## 0.0.37

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.34
  - @buildonspark/spark-sdk@0.1.6

## 0.0.36

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.33
  - @buildonspark/spark-sdk@0.1.5

## 0.0.35

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.32
  - @buildonspark/spark-sdk@0.1.4

## 0.0.34

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.31
  - @buildonspark/spark-sdk@0.1.3

## 0.0.33

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.30
  - @buildonspark/spark-sdk@0.1.2

## 0.0.32

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.29
  - @buildonspark/spark-sdk@0.1.1

## 0.0.31

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.1.0

## 0.0.30

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.28
  - @buildonspark/spark-sdk@0.0.30

## 0.0.29

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.27
  - @buildonspark/spark-sdk@0.0.29

## 0.0.28

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.26
  - @buildonspark/spark-sdk@0.0.28

## 0.0.27

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.25
  - @buildonspark/spark-sdk@0.0.27

## 0.0.26

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.24
  - @buildonspark/spark-sdk@0.0.26

## 0.0.25

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.23
  - @buildonspark/spark-sdk@0.0.25

## 0.0.24

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.22
  - @buildonspark/spark-sdk@0.0.24

## 0.0.23

### Patch Changes

- CJS support and package improvements
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.21
  - @buildonspark/spark-sdk@0.0.23

## 0.0.22

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.20
  - @buildonspark/spark-sdk@0.0.22

## 0.0.21

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.19
  - @buildonspark/spark-sdk@0.0.21

## 0.0.20

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.18
  - @buildonspark/spark-sdk@0.0.20

## 0.0.19

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.17
  - @buildonspark/spark-sdk@0.0.19

## 0.0.18

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.16
  - @buildonspark/spark-sdk@0.0.18

## 0.0.17

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.15
  - @buildonspark/spark-sdk@0.0.17

## 0.0.16

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.14
  - @buildonspark/spark-sdk@0.0.16

## 0.0.15

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.13
  - @buildonspark/spark-sdk@0.0.15

## 0.0.14

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.12
  - @buildonspark/spark-sdk@0.0.14

## 0.0.13

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.11
  - @buildonspark/spark-sdk@0.0.13

## 0.0.12

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.10
  - @buildonspark/spark-sdk@0.0.12

## 0.0.11

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.9
  - @buildonspark/spark-sdk@0.0.11

## 0.0.10

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.8
  - @buildonspark/spark-sdk@0.0.10

## 0.0.9

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.7
  - @buildonspark/spark-sdk@0.0.9

## 0.0.8

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.6
  - @buildonspark/spark-sdk@0.0.8

## 0.0.7

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.5
  - @buildonspark/spark-sdk@0.0.7

## 0.0.6

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.4
  - @buildonspark/spark-sdk@0.0.6

## 0.0.4

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.2
  - @buildonspark/spark-sdk@0.0.4

## 0.0.3

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/lrc20-sdk@0.0.1
  - @buildonspark/spark-sdk@0.0.3

## 0.0.2

### Patch Changes

- Updated dependencies
  - @buildonspark/spark-sdk@0.0.2

## 0.0.1

### Patch Changes

- Fixes
- Updated dependencies
  - @buildonspark/spark-sdk@0.0.1
