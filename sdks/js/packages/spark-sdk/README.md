# Spark SDK

Spark is the fastest, cheapest, and most UX-friendly way to build financial apps and launch assets natively on Bitcoin. It’s a Bitcoin L2 that lets developers move Bitcoin and Bitcoin-native assets (including stablecoins) instantly, at near-zero cost, while staying fully connected to Bitcoin’s infrastructure.

For complete documentation, visit [https://docs.spark.money](https://docs.spark.money)

## Installation

```bash
npm install @buildonspark/spark-sdk
# or
yarn add @buildonspark/spark-sdk
# or
pnpm add @buildonspark/spark-sdk
```

## Quick Start

### Initialize a Wallet

```typescript
import { SparkWallet } from "@buildonspark/spark-sdk";

// Create a new wallet (generates a new mnemonic)
const { wallet, mnemonic } = await SparkWallet.create({
  options: {
    network: "MAINNET", // or "REGTEST" for testing
  },
});

// Or initialize with an existing mnemonic
const wallet = await SparkWallet.initialize({
  mnemonicOrSeed: "your twelve word mnemonic phrase here ...",
  options: {
    network: "MAINNET",
  },
});
```

### Check Balance

```typescript
const balance = await wallet.getBalance();
console.log(`Bitcoin balance: ${balance.balance} sats`);
console.log(`Token balances:`, balance.tokenBalances);
```

### Get Deposit Address

```typescript
// Single-use deposit address (one-time use)
const address = await wallet.getSingleUseDepositAddress();
console.log(`Deposit Bitcoin to: ${address}`);
```

### Static Deposit Address

Static deposit addresses are reusable and allow you to receive multiple deposits to the same address.

```typescript
// Get a reusable static deposit address
const staticAddress = await wallet.getStaticDepositAddress();
console.log(`Static deposit address: ${staticAddress}`);

// After sending Bitcoin to the address from an external wallet,
// get a quote for claiming the deposit
const quote = await wallet.getClaimStaticDepositQuote(transactionId);
console.log(`Credit amount: ${quote.creditAmountSats} sats`);
console.log(`Fee: ${quote.feeSats} sats`);

// Claim the deposit with the quote
await wallet.claimStaticDeposit({
  transactionId,
  creditAmountSats: quote.creditAmountSats,
  sspSignature: quote.signature,
});

// Or use the convenience method with a max fee
await wallet.claimStaticDepositWithMaxFee({
  transactionId,
  maxFee: 500, // Will throw if fee exceeds this amount
});

// Query all your static deposit addresses
const addresses = await wallet.queryStaticDepositAddresses();

// Get confirmed UTXOs for a deposit address
const utxos = await wallet.getUtxosForDepositAddress(staticAddress);
for (const utxo of utxos) {
  console.log(`UTXO: ${utxo.txid}:${utxo.vout}`);
}
```

### Send Bitcoin

```typescript
// Transfer to another Spark address
const transfer = await wallet.transfer({
  receiverSparkAddress: "sp1q...",
  amountSats: 10000,
});

// Withdraw to on-chain Bitcoin address
const withdrawal = await wallet.withdraw({
  onchainAddress: "bc1q...",
  amountSats: 50000,
  exitSpeed: "FAST", // or "MEDIUM", "SLOW"
});
```

### Lightning Payments

```typescript
// Create a Lightning invoice
const invoice = await wallet.createLightningInvoice({
  amountSats: 1000,
  memo: "Payment for services",
});
console.log(`Invoice: ${invoice.invoice.encodedInvoice}`);

// Pay a Lightning invoice
const payment = await wallet.payLightningInvoice({
  invoice: "lnbc...",
  maxFeeSats: 100,
});
```

### Token Operations

```typescript
// Get token balances
const { tokenBalances } = await wallet.getBalance();
for (const [tokenId, info] of tokenBalances) {
  console.log(`${info.tokenMetadata.tokenName}: ${info.balance}`);
}

// Transfer tokens
const tokenTransfer = await wallet.transferTokens({
  tokenIdentifier: "spark1...",
  receiverSparkAddress: "sp1q...",
  tokenAmount: 100n,
});
```

### Event Handling

```typescript
import { SparkWalletEvent } from "@buildonspark/spark-sdk";

// Listen for incoming transfers
wallet.on(SparkWalletEvent.TransferClaimed, (transferId, newBalance) => {
  console.log(`Received transfer ${transferId}, new balance: ${newBalance}`);
});

// Listen for confirmed deposits
wallet.on(SparkWalletEvent.DepositConfirmed, (depositId, newBalance) => {
  console.log(`Deposit ${depositId} confirmed, new balance: ${newBalance}`);
});
```

## Platform Support

The SDK supports multiple JavaScript runtimes:

- **Browser**
- **Node.js**
- **React Native**
