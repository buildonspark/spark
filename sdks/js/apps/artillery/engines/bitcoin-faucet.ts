import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import * as btc from "@scure/btc-signer";
import { SigHash, Transaction } from "@scure/btc-signer";
import { TransactionInput, TransactionOutput } from "@scure/btc-signer/psbt";
import { taprootTweakPrivKey } from "@scure/btc-signer/utils";

// Static keys for deterministic testing
// P2TRAddress: bcrt1p2uy9zw5ltayucsuzl4tet6ckelzawp08qrtunacscsszflye907q62uqhl
const STATIC_FAUCET_KEY = hexToBytes(
  "deadbeef1337cafe4242424242424242deadbeef1337cafe4242424242424242",
);

// P2TRAddress: bcrt1pwr5k38p68ceyrnm2tvrp50dvmg3grh6uvayjl3urwtxejhd3dw4swz6p58
const STATIC_MINING_KEY = hexToBytes(
  "1337cafe4242deadbeef4242424242421337cafe4242deadbeef424242424242",
);
const SATS_PER_BTC = 100_000_000;

export type FaucetCoin = {
  key: Uint8Array;
  outpoint: TransactionInput;
  txout: TransactionOutput;
};

// The amount of satoshis to put in each faucet coin to be used in tests
const COIN_AMOUNT = 10_000_000n;
const FEE_AMOUNT = 1000n;
const TARGET_NUM_COINS = 20;

// Simplified network config for LOCAL/REGTEST
const NETWORK_CONFIG = {
  bech32: "bcrt",
  pubKeyHash: 0x6f,
  scriptHash: 0xc4,
  wif: 0xef,
};

function getP2TRAddressFromPublicKey(pubKey: Uint8Array): string {
  // Convert compressed pubkey to x-only pubkey (remove the first byte)
  const xOnlyPubkey = pubKey.slice(1);
  const p2tr = btc.p2tr(xOnlyPubkey, undefined, NETWORK_CONFIG);
  if (!p2tr.address) {
    throw new Error("Failed to generate P2TR address");
  }
  return p2tr.address;
}

function getP2TRScriptFromPublicKey(pubKey: Uint8Array): Uint8Array {
  // Convert compressed pubkey to x-only pubkey (remove the first byte)
  const xOnlyPubkey = pubKey.slice(1);
  const p2tr = btc.p2tr(xOnlyPubkey, undefined, NETWORK_CONFIG);
  if (!p2tr.script) {
    throw new Error("Failed to generate P2TR script");
  }
  return p2tr.script;
}

export class BitcoinFaucet {
  private coins: FaucetCoin[] = [];
  private static instance: BitcoinFaucet | null = null;
  private static instanceLock = false;
  private miningAddress: string;
  private lock: Promise<void> = Promise.resolve();
  private url: string;
  private username: string;
  private password: string;

  private constructor(
    url: string = "http://127.0.0.1:8332",
    username: string = "testutil",
    password: string = "testutilpassword",
  ) {
    this.url = url;
    this.username = username;
    this.password = password;
    this.miningAddress = getP2TRAddressFromPublicKey(
      secp256k1.getPublicKey(STATIC_MINING_KEY),
    );
    console.log(
      `BitcoinFaucet initialized with URL: ${this.url} in process ${process.pid}`,
    );
  }

  static getInstance(
    url?: string,
    username?: string,
    password?: string,
  ): BitcoinFaucet {
    const faucetUrl =
      url || process.env.BITCOIN_RPC_URL || "http://127.0.0.1:8332";
    const faucetUsername =
      username || process.env.BITCOIN_RPC_USER || "testutil";
    const faucetPassword =
      password || process.env.BITCOIN_RPC_PASSWORD || "testutilpassword";

    return new BitcoinFaucet(faucetUrl, faucetUsername, faucetPassword);
  }

  private async withLock<T>(operation: () => Promise<T>): Promise<T> {
    const current = this.lock;
    let resolve: () => void;
    this.lock = new Promise<void>((r) => (resolve = r));
    await current;
    try {
      return await operation();
    } finally {
      resolve!();
    }
  }

  async fund(): Promise<FaucetCoin> {
    return this.withLock(async () => {
      let retries = 3;
      while (retries > 0) {
        if (this.coins.length === 0) {
          try {
            await this.refill();
          } catch (error: any) {
            if (
              error.message?.includes(
                "Transaction outputs already in utxo set",
              ) &&
              retries > 1
            ) {
              console.log(
                "[BitcoinFaucet] Refill failed due to race condition, retrying...",
              );
              retries--;
              await new Promise((resolve) => setTimeout(resolve, 1000)); // Wait 1 second
              continue;
            }
            throw error;
          }
        }

        const coin = this.coins[0];
        if (coin) {
          this.coins = this.coins.slice(1);
          return coin;
        }

        retries--;
        if (retries > 0) {
          console.log("[BitcoinFaucet] No coins available, retrying...");
          await new Promise((resolve) => setTimeout(resolve, 1000));
        }
      }

      throw new Error("Failed to get coin from faucet after retries");
    });
  }

  private async refill(): Promise<void> {
    const minerPubKey = secp256k1.getPublicKey(STATIC_MINING_KEY);
    const address = getP2TRAddressFromPublicKey(minerPubKey);

    const scanResult = await this.call("scantxoutset", [
      "start",
      [`addr(${address})`],
    ]);

    let selectedUtxo;
    let selectedUtxoAmountSats;
    if (!scanResult.success || scanResult.unspents.length === 0) {
      const blockHash = await this.generateToAddress(1, address);
      const block = await this.getBlock(blockHash[0]);
      const fundingTx = Transaction.fromRaw(hexToBytes(block.tx[0].hex), {
        allowUnknownOutputs: true,
      });

      await this.generateToAddress(100, this.miningAddress);

      selectedUtxo = {
        txid: block.tx[0].txid,
        vout: 0,
        amount: fundingTx.getOutput(0)!.amount!,
      };
      selectedUtxoAmountSats = BigInt(selectedUtxo.amount);
    } else {
      selectedUtxo = scanResult.unspents.find((utxo) => {
        const isValueEnough =
          BigInt(Math.floor(utxo.amount * SATS_PER_BTC)) >=
          COIN_AMOUNT + FEE_AMOUNT;
        const isMature = scanResult.height - utxo.height >= 100;
        return isValueEnough && isMature;
      });

      if (!selectedUtxo) {
        throw new Error("No UTXO large enough to create even one faucet coin");
      }
      selectedUtxoAmountSats = BigInt(
        Math.floor(selectedUtxo.amount * SATS_PER_BTC),
      );
    }

    const maxPossibleCoins = Number(
      (selectedUtxoAmountSats - FEE_AMOUNT) / COIN_AMOUNT,
    );
    const numCoinsToCreate = Math.min(maxPossibleCoins, TARGET_NUM_COINS);

    if (numCoinsToCreate < 1) {
      throw new Error(
        `Selected UTXO (${selectedUtxoAmountSats} sats) is too small to create even one faucet coin of ${COIN_AMOUNT} sats`,
      );
    }

    const splitTx = new Transaction();
    splitTx.addInput({
      txid: selectedUtxo.txid,
      index: selectedUtxo.vout,
    });

    const faucetPubKey = secp256k1.getPublicKey(STATIC_FAUCET_KEY);
    const script = getP2TRScriptFromPublicKey(faucetPubKey);
    for (let i = 0; i < numCoinsToCreate; i++) {
      splitTx.addOutput({
        script,
        amount: COIN_AMOUNT,
      });
    }

    const remainingValue =
      selectedUtxoAmountSats -
      COIN_AMOUNT * BigInt(numCoinsToCreate) -
      FEE_AMOUNT;
    const minerScript = getP2TRScriptFromPublicKey(minerPubKey);
    if (remainingValue > 0n) {
      splitTx.addOutput({
        script: minerScript,
        amount: remainingValue,
      });
    }

    const signedSplitTx = await this.signFaucetCoin(
      splitTx,
      {
        amount: selectedUtxoAmountSats,
        script: minerScript,
      },
      STATIC_MINING_KEY,
    );

    try {
      await this.broadcastTx(bytesToHex(signedSplitTx.extract()));
    } catch (error: any) {
      if (error.message?.includes("Transaction outputs already in utxo set")) {
        console.log(
          "[BitcoinFaucet] Transaction already broadcast, likely by another process. Continuing...",
        );
        return;
      }
      throw error;
    }

    const splitTxId = signedSplitTx.id;
    for (let i = 0; i < numCoinsToCreate; i++) {
      this.coins.push({
        key: STATIC_FAUCET_KEY,
        outpoint: {
          txid: hexToBytes(splitTxId),
          index: i,
        },
        txout: signedSplitTx.getOutput(i)!,
      });
    }
  }

  async sendFaucetCoinToP2WPKHAddress(pubKey: Uint8Array) {
    const sendToPubKeyTx = new Transaction();

    const p2wpkhAddress = btc.p2wpkh(pubKey, NETWORK_CONFIG).address;
    if (!p2wpkhAddress) {
      throw new Error("Invalid P2WPKH address");
    }

    const coinToSend = await this.fund();
    if (!coinToSend) {
      throw new Error("No coins available");
    }

    sendToPubKeyTx.addInput(coinToSend.outpoint);

    sendToPubKeyTx.addOutputAddress(
      p2wpkhAddress,
      COIN_AMOUNT - FEE_AMOUNT,
      NETWORK_CONFIG,
    );

    const signedTx = await this.signFaucetCoin(
      sendToPubKeyTx,
      coinToSend.txout,
      coinToSend.key,
    );

    await this.broadcastTx(bytesToHex(signedTx.extract()));
  }

  async signFaucetCoin(
    unsignedTx: Transaction,
    fundingTxOut: TransactionOutput,
    key: Uint8Array,
  ): Promise<Transaction> {
    const pubKey = secp256k1.getPublicKey(key);
    const internalKey = pubKey.slice(1);

    const script = getP2TRScriptFromPublicKey(pubKey);

    unsignedTx.updateInput(0, {
      tapInternalKey: internalKey,
      witnessUtxo: {
        script,
        amount: fundingTxOut.amount!,
      },
    });

    const sighash = unsignedTx.preimageWitnessV1(
      0,
      new Array(unsignedTx.inputsLength).fill(script),
      SigHash.DEFAULT,
      new Array(unsignedTx.inputsLength).fill(fundingTxOut.amount!),
    );

    const merkleRoot = new Uint8Array();
    const tweakedKey = taprootTweakPrivKey(key, merkleRoot);
    if (!tweakedKey)
      throw new Error("Invalid private key for taproot tweaking");

    const signature = schnorr.sign(sighash, tweakedKey);

    unsignedTx.updateInput(0, {
      tapKeySig: signature,
    });

    unsignedTx.finalize();

    return unsignedTx;
  }

  async mineBlocks(numBlocks: number) {
    return await this.generateToAddress(numBlocks, this.miningAddress);
  }

  private async call(method: string, params: any[]) {
    try {
      if (!this || !this.url) {
        throw new Error(
          `BitcoinFaucet not properly initialized. this.url is undefined`,
        );
      }
      const response = await fetch(this.url, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: "Basic " + btoa(`${this.username}:${this.password}`),
        },
        body: JSON.stringify({
          jsonrpc: "1.0",
          id: "spark-js",
          method,
          params,
        }),
      });

      const data = await response.json();
      if (data.error) {
        console.error(`RPC Error for method ${method}:`, data.error);
        throw new Error(`Bitcoin RPC error: ${data.error.message}`);
      }

      return data.result;
    } catch (error) {
      if (error instanceof Error) {
        throw error;
      }
      throw new Error(`Failed to call Bitcoin RPC: ${method}`);
    }
  }

  async generateToAddress(numBlocks: number, address: string) {
    return await this.call("generatetoaddress", [numBlocks, address]);
  }

  async getBlock(blockHash: string) {
    return await this.call("getblock", [blockHash, 2]);
  }

  async broadcastTx(txHex: string) {
    return await this.call("sendrawtransaction", [txHex, 0]);
  }

  async getNewAddress(): Promise<string> {
    const key = secp256k1.utils.randomSecretKey();
    const pubKey = secp256k1.getPublicKey(key);
    return getP2TRAddressFromPublicKey(pubKey);
  }

  async sendToAddress(address: string, amount: bigint): Promise<string> {
    console.log(
      `[BitcoinFaucet.sendToAddress] Called with address: ${address}, amount: ${amount}`,
    );
    console.log(
      `[BitcoinFaucet.sendToAddress] this.url: ${this.url}, process: ${process.pid}`,
    );

    const coin = await this.fund();
    if (!coin) {
      throw new Error("No coins available");
    }

    const tx = new Transaction();
    tx.addInput(coin.outpoint);

    const availableAmount = COIN_AMOUNT - FEE_AMOUNT;

    tx.addOutputAddress(address, amount, NETWORK_CONFIG);

    const changeAmount = availableAmount - amount;
    if (changeAmount > 0) {
      const changeKey = secp256k1.utils.randomSecretKey();
      const changePubKey = secp256k1.getPublicKey(changeKey);
      const changeScript = getP2TRScriptFromPublicKey(changePubKey);
      tx.addOutput({
        script: changeScript,
        amount: changeAmount,
      });
    }

    const signedTx = await this.signFaucetCoin(tx, coin.txout, coin.key);
    const txHex = bytesToHex(signedTx.extract());
    await this.broadcastTx(txHex);

    const randomKey = secp256k1.utils.randomSecretKey();
    const randomPubKey = secp256k1.getPublicKey(randomKey);
    const randomAddress = getP2TRAddressFromPublicKey(randomPubKey);

    await this.generateToAddress(1, randomAddress);

    return signedTx.id;
  }

  async getRawTransaction(txid: string) {
    return await this.call("getrawtransaction", [txid, 2]);
  }
}
