#!/usr/bin/env node
import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import fetch from "node-fetch";

Object.defineProperty(globalThis, "fetch", {
  value: fetch,
});

async function main() {
  const mnemonicOrSeed =
    "table apology decrease custom deny client retire genius uniform find eager fish";
  const tokenName = "TestToken";
  const tokenTicker = "TEST";
  const decimals = 8;
  const maxSupply = 0n;
  const isFreezable = true;

  const { wallet } = await IssuerSparkWallet.initialize({
    mnemonicOrSeed,
    options: {
      network: "LOCAL",
    },
  });

  console.log(`Announcing token: ${tokenName} (${tokenTicker})`);
  const txid = await wallet.announceTokenL1(
    tokenName,
    tokenTicker,
    decimals,
    maxSupply,
    isFreezable,
  );
  console.log(txid);
  process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
