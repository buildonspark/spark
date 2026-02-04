# Spark Issuer SDK

For complete documentation, visit [https://docs.spark.money](https://docs.spark.money)

## Installation

```bash
npm install @buildonspark/issuer-sdk @buildonspark/spark-sdk
# or
yarn add @buildonspark/issuer-sdk @buildonspark/spark-sdk
# or
pnpm add @buildonspark/issuer-sdk @buildonspark/spark-sdk
```

## Quick Start

### Initialize an Issuer Wallet

```typescript
import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";

// Create a new issuer wallet (generates a new mnemonic)
const { wallet, mnemonic } = await IssuerSparkWallet.create({
  options: {
    network: "MAINNET", // or "REGTEST" for testing
  },
});

// Or initialize with an existing mnemonic
const wallet = await IssuerSparkWallet.initialize({
  mnemonicOrSeed: "your twelve word mnemonic phrase here ...",
  options: {
    network: "MAINNET",
  },
});
```

### Create a Token

```typescript
const token = await wallet.createToken({
  tokenName: "My Token",
  tokenTicker: "MTK",
  decimals: 8,
  maxSupply: 1000000n,
  isFreezable: true,
});

console.log(`Token created: ${token.tokenIdentifier}`);
```

### Mint Tokens

```typescript
// Mint tokens to your own wallet
const mintResult = await wallet.mintTokens({
  tokenIdentifier: "spark1...",
  tokenAmount: 10000n,
});

// To distribute tokens to others, mint first then transfer
await wallet.mintTokens({
  tokenIdentifier: "spark1...",
  tokenAmount: 5000n,
});

await wallet.transferTokens({
  tokenIdentifier: "spark1...",
  receiverSparkAddress: "sp1q...",
  tokenAmount: 5000n,
});
```

### Check Issuer Token Balance

```typescript
// Get balance for all tokens you've issued
const balances = await wallet.getIssuerTokenBalances();
for (const [tokenId, info] of balances) {
  console.log(`${info.tokenMetadata.tokenName}: ${info.balance}`);
}
```

### Freeze/Unfreeze Tokens

```typescript
// Freeze tokens at a Spark address (requires isFreezable: true during creation)
await wallet.freezeTokens({
  tokenIdentifier: "spark1...",
  sparkAddress: "sp1q...",
});

// Unfreeze tokens at a Spark address
await wallet.unfreezeTokens({
  tokenIdentifier: "spark1...",
  sparkAddress: "sp1q...",
});
```

### Burn Tokens

```typescript
const burnResult = await wallet.burnTokens({
  tokenIdentifier: "spark1...",
  tokenAmount: 1000n,
});
```

### Query Token Transactions

```typescript
const transactions = await wallet.getTokenTransactions({
  tokenIdentifier: "spark1...",
  limit: 50,
});

for (const tx of transactions) {
  console.log(`TX: ${tx.id}, Amount: ${tx.amount}`);
}
```

## Platform Support

The SDK supports multiple JavaScript runtimes:

- **Browser** - Web applications with WASM crypto
- **Node.js** - Server-side applications
- **React Native** - Mobile applications
