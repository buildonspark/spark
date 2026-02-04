# Spark SDK for Bare Runtime

Spark SDK optimized for the [Bare](https://bare.pears.com/) JavaScript runtime environment.

## Documentation

For complete documentation, visit [https://docs.spark.money](https://docs.spark.money)

## Overview

This package provides Spark SDK support for Bare, a lightweight JavaScript runtime. It uses native addons for FROST cryptographic operations.

## Installation

```bash
npm install @buildonspark/bare bare
```

## Quick Start

Create a JavaScript file and run it with the `bare` CLI:

**get-balance.js**

```javascript
import { SparkWallet } from "@buildonspark/bare";
import process from "bare-process";

async function main() {
  const { wallet, mnemonic } = await SparkWallet.initialize({
    mnemonicOrSeed: undefined, // Or provide existing mnemonic
    options: {
      network: "MAINNET", // or "REGTEST" for testing
    },
  });

  const balance = await wallet.getBalance();
  const sparkAddress = await wallet.getSparkAddress();

  console.log("Mnemonic:", mnemonic);
  console.log("Balance:", balance);
  console.log("Spark Address:", sparkAddress);

  await wallet.cleanupConnections();
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
```

Run with:

```bash
bare get-balance.js
```

## Examples

### Get Static Deposit Address

```javascript
import { SparkWallet } from "@buildonspark/bare";

const { wallet } = await SparkWallet.initialize({
  mnemonicOrSeed: "your mnemonic phrase here...",
  options: { network: "MAINNET" },
});

const address = await wallet.getStaticDepositAddress();
console.log("Deposit address:", address);

await wallet.cleanupConnections();
```

### Transfer Bitcoin

```javascript
import { SparkWallet } from "@buildonspark/bare";

const { wallet } = await SparkWallet.initialize({
  mnemonicOrSeed: "your mnemonic phrase here...",
  options: { network: "MAINNET" },
});

const transfer = await wallet.transfer({
  receiverSparkAddress: "sp1q...",
  amountSats: 1000,
});

console.log("Transfer result:", transfer);
```

### Create Lightning Invoice

```javascript
import { SparkWallet } from "@buildonspark/bare";

const { wallet } = await SparkWallet.initialize({
  mnemonicOrSeed: "your mnemonic phrase here...",
  options: { network: "MAINNET" },
});

const invoice = await wallet.createLightningInvoice({
  amountSats: 1000,
  memo: "Payment",
});

console.log("Invoice:", invoice.invoice.encodedInvoice);
```

## When to Use This Package

This package is intended for use with Bare runtime only. Use @buildonspark/spark-sdk for other platforms.
