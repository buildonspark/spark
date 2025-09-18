#!/usr/bin/env node
/* Note lrc20-sdk will be deprecated and has been removed from Spark JS workspaces.
   This is temporarily left for testing purposes: */
import {
  TokenPubkey,
  TokenPubkeyAnnouncement,
  LRCWallet,
  NetworkType,
} from "@buildonspark/lrc20-sdk";
import fetch from "node-fetch";

Object.defineProperty(globalThis, "fetch", {
  value: fetch,
});

export const isHermeticTest = Boolean(
  typeof process !== "undefined" && process?.env?.HERMETIC_TEST === "true",
);

const regtest = {
  messagePrefix: "\x18Bitcoin Signed Message:\n",
  bech32: "bcrt",
  bip32: {
    public: 0x043587cf,
    private: 0x04358394,
  },
  pubKeyHash: 0x6f,
  scriptHash: 0xc4,
  wif: 0xef,
};

async function main() {
  const tokenName = "TestToken";
  const tokenTicker = "TEST";
  const decimals = 8;
  const maxSupply = 0n;
  const isFreezable = true;

  let wallet = new LRCWallet(
    "515c86ccb09faa2235acd0e287381bf286b37002328a8cc3c3b89738ab59dc93",
    regtest,
    NetworkType.LOCAL,
    {
      lrc20NodeUrl: "http://127.0.0.1:18332",
      electrsUrl: isHermeticTest
        ? "http://mempool.minikube.local/api"
        : "http://127.0.0.1:30000",
      electrsCredentials: {
        username: "spark-sdk",
        password: "mCMk1JqlBNtetUNy",
      },
    },
  );

  console.log(`Announcing token: ${tokenName} (${tokenTicker})`);
  const txid = await announceTokenL1(
    wallet,
    tokenName,
    tokenTicker,
    decimals,
    maxSupply,
    isFreezable,
  );
  console.log(txid);
  process.exit(0);
}

/**
 * Announces a new token on the L1 (Bitcoin) network.
 * @param tokenName - The name of the token
 * @param tokenTicker - The ticker symbol for the token
 * @param decimals - The number of decimal places for the token
 * @param maxSupply - The maximum supply of the token
 * @param isFreezable - Whether the token can be frozen
 * @param feeRateSatsPerVb - The fee rate in satoshis per virtual byte (default: 4.0)
 * @returns The transaction ID of the announcement
 * @throws {ValidationError} If decimals is not a safe integer
 * @throws {NetworkError} If the announcement transaction cannot be broadcast
 */
async function announceTokenL1(
  lrc20Wallet: LRCWallet,
  tokenName: string,
  tokenTicker: string,
  decimals: number,
  maxSupply: bigint,
  isFreezable: boolean,
  feeRateSatsPerVb: number = 4.0,
): Promise<string> {
  await lrc20Wallet!.syncWallet();

  const tokenPublicKey = new TokenPubkey(lrc20Wallet!.pubkey);

  const announcement = new TokenPubkeyAnnouncement(
    tokenPublicKey,
    tokenName,
    tokenTicker,
    decimals,
    maxSupply,
    isFreezable,
  );

  const tx = await lrc20Wallet!.prepareAnnouncement(
    announcement,
    feeRateSatsPerVb,
  );

  const txId = await lrc20Wallet!.broadcastRawBtcTransaction(
    tx.bitcoin_tx.toHex(),
  );

  return txId;
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
