#!/usr/bin/env node
import {
  TokenPubkey,
  TokenPubkeyAnnouncement,
  Lrc20AnnouncementWallet,
  NetworkType,
} from "./lib/index.js";
import fetch from "node-fetch";

Object.defineProperty(globalThis, "fetch", {
  value: fetch,
});

const MAX_BROADCAST_RETRIES = 3;
const RETRY_DELAY_MS = 1_000;

async function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export const isHermeticTest = Boolean(
  typeof process !== "undefined" && process?.env?.MINIKUBE_IP,
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

  const wallet = new Lrc20AnnouncementWallet(
    "515c86ccb09faa2235acd0e287381bf286b37002328a8cc3c3b89738ab59dc93",
    regtest,
    NetworkType.LOCAL,
    {
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
 * @param wallet - The wallet to use for the announcement
 * @param tokenName - The name of the token
 * @param tokenTicker - The ticker symbol for the token
 * @param decimals - The number of decimal places for the token
 * @param maxSupply - The maximum supply of the token
 * @param isFreezable - Whether the token can be frozen
 * @param feeRateSatsPerVb - The fee rate in satoshis per virtual byte (default: 4.0)
 * @returns The transaction ID of the announcement
 */
async function announceTokenL1(
  wallet: Lrc20AnnouncementWallet,
  tokenName: string,
  tokenTicker: string,
  decimals: number,
  maxSupply: bigint,
  isFreezable: boolean,
  feeRateSatsPerVb: number = 4.0,
): Promise<string> {
  await wallet.syncWallet();

  const tokenPublicKey = new TokenPubkey(wallet.pubkey);

  const announcement = new TokenPubkeyAnnouncement(
    tokenPublicKey,
    tokenName,
    tokenTicker,
    decimals,
    maxSupply,
    isFreezable,
  );

  const tx = await wallet.prepareAnnouncement(announcement, feeRateSatsPerVb);

  let lastError: unknown;

  for (let attempt = 0; attempt < MAX_BROADCAST_RETRIES; attempt++) {
    try {
      const txId = await wallet.broadcastRawBtcTransaction(
        tx.bitcoin_tx.toHex(),
      );

      return txId;
    } catch (err) {
      lastError = err;
      console.warn(
        `broadcastRawBtcTransaction failed (attempt ${
          attempt + 1
        }/${MAX_BROADCAST_RETRIES})`,
        err,
      );

      if (attempt < MAX_BROADCAST_RETRIES - 1) {
        await sleep(RETRY_DELAY_MS * (attempt + 1));
      }
    }
  }

  throw lastError;
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
