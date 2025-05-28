import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import * as btc from "@scure/btc-signer";
import { Address, OutScript, SigHash, Transaction } from "@scure/btc-signer";
import { TransactionInput, TransactionOutput } from "@scure/btc-signer/psbt";
import { taprootTweakPrivKey } from "@scure/btc-signer/utils";
import { RPCError } from "../../errors/index.js";
import {
  getP2TRAddressFromPublicKey,
  getP2TRScriptFromPublicKey,
} from "../../utils/bitcoin.js";
import { getNetwork, Network } from "../../utils/network.js";

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

export class BitcoinFaucet {
  private coins: FaucetCoin[] = [];
  private static instance: BitcoinFaucet | null = null;
  private miningAddress: string;
  private lock: Promise<void> = Promise.resolve();

  private constructor(
    private url: string = "http://127.0.0.1:8332",
    private username: string = "testutil",
    private password: string = "testutilpassword",
  ) {
    this.miningAddress = getP2TRAddressFromPublicKey(
      secp256k1.getPublicKey(STATIC_MINING_KEY),
      Network.LOCAL,
    );
  }

  static getInstance(
    url: string = "http://127.0.0.1:8332",
    username: string = "testutil",
    password: string = "testutilpassword",
  ): BitcoinFaucet {
    if (!BitcoinFaucet.instance) {
      BitcoinFaucet.instance = new BitcoinFaucet(url, username, password);
    }
    return BitcoinFaucet.instance;
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
      if (this.coins.length === 0) {
        await this.refill();
      }

      const coin = this.coins[0];
      if (!coin) {
        throw new Error("Failed to get coin from faucet");
      }
      this.coins = this.coins.slice(1);
      return coin;
    });
  }

  private async refill(): Promise<void> {
    const minerPubKey = secp256k1.getPublicKey(STATIC_MINING_KEY);
    const address = getP2TRAddressFromPublicKey(minerPubKey, Network.LOCAL);

    // Use scantxoutset to find UTXOs
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
        amount: fundingTx.getOutput(0)!.amount!, // Already in sats
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
    const script = getP2TRScriptFromPublicKey(faucetPubKey, Network.LOCAL);
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
    const minerScript = getP2TRScriptFromPublicKey(minerPubKey, Network.LOCAL);
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

    await this.broadcastTx(bytesToHex(signedSplitTx.extract()));

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

    // For P2WPKH, we need to hash the public key

    // Create a P2WPKH address
    const p2wpkhAddress = btc.p2wpkh(pubKey, getNetwork(Network.LOCAL)).address;
    if (!p2wpkhAddress) {
      throw new Error("Invalid P2WPKH address");
    }

    // Get the coin to spend
    const coinToSend = await this.fund();
    if (!coinToSend) {
      throw new Error("No coins available");
    }

    // Add the input
    sendToPubKeyTx.addInput(coinToSend.outpoint);

    // Add the output using the address directly, but subtract FEE_AMOUNT to ensure there's a fee
    sendToPubKeyTx.addOutputAddress(
      p2wpkhAddress,
      COIN_AMOUNT - FEE_AMOUNT,
      getNetwork(Network.LOCAL),
    );

    // Sign the transaction and get the signed result
    const signedTx = await this.signFaucetCoin(
      sendToPubKeyTx,
      coinToSend.txout,
      coinToSend.key,
    );

    // Broadcast the signed transaction
    await this.broadcastTx(bytesToHex(signedTx.extract()));
  }

  async signFaucetCoin(
    unsignedTx: Transaction,
    fundingTxOut: TransactionOutput,
    key: Uint8Array,
  ): Promise<Transaction> {
    const pubKey = secp256k1.getPublicKey(key);
    const internalKey = pubKey.slice(1); // Remove the 0x02/0x03 prefix

    const script = getP2TRScriptFromPublicKey(pubKey, Network.LOCAL);

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

  // MineBlocks mines the specified number of blocks to a random address
  // and returns the block hashes.
  async mineBlocks(numBlocks: number) {
    return await this.generateToAddress(numBlocks, this.miningAddress);
  }

  private async call(method: string, params: any[]) {
    try {
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
        throw new RPCError(`Bitcoin RPC error: ${data.error.message}`, {
          method,
          params,
          code: data.error.code,
        });
      }

      return data.result;
    } catch (error) {
      if (error instanceof RPCError) {
        throw error;
      }
      throw new RPCError(
        "Failed to call Bitcoin RPC",
        {
          method,
          params,
        },
        error as Error,
      );
    }
  }

  async generateToAddress(numBlocks: number, address: string) {
    return await this.call("generatetoaddress", [numBlocks, address]);
  }

  async getBlock(blockHash: string) {
    return await this.call("getblock", [blockHash, 2]);
  }

  async broadcastTx(txHex: string) {
    let response = await this.call("sendrawtransaction", [txHex, 0]);
    return response;
  }

  async getNewAddress(): Promise<string> {
    const key = secp256k1.utils.randomPrivateKey();
    const pubKey = secp256k1.getPublicKey(key);
    return getP2TRAddressFromPublicKey(pubKey, Network.LOCAL);
  }

  async sendToAddress(address: string, amount: bigint): Promise<Transaction> {
    const coin = await this.fund();
    if (!coin) {
      throw new Error("No coins available");
    }

    const tx = new Transaction();
    tx.addInput(coin.outpoint);

    const availableAmount = COIN_AMOUNT - FEE_AMOUNT;

    const destinationAddress = Address(getNetwork(Network.LOCAL)).decode(
      address,
    );
    const destinationScript = OutScript.encode(destinationAddress);
    tx.addOutput({
      script: destinationScript,
      amount: amount,
    });

    const changeAmount = availableAmount - amount;
    if (changeAmount > 0) {
      const changeKey = secp256k1.utils.randomPrivateKey();
      const changePubKey = secp256k1.getPublicKey(changeKey);
      const changeScript = getP2TRScriptFromPublicKey(
        changePubKey,
        Network.LOCAL,
      );
      tx.addOutput({
        script: changeScript,
        amount: changeAmount,
      });
    }

    const signedTx = await this.signFaucetCoin(tx, coin.txout, coin.key);
    const txHex = bytesToHex(signedTx.extract());
    await this.broadcastTx(txHex);

    const randomKey = secp256k1.utils.randomPrivateKey();
    const randomPubKey = secp256k1.getPublicKey(randomKey);
    const randomAddress = getP2TRAddressFromPublicKey(
      randomPubKey,
      Network.LOCAL,
    );

    await this.generateToAddress(1, randomAddress);

    return signedTx;
  }

  async getRawTransaction(txid: string) {
    return await this.call("getrawtransaction", [txid, 2]);
  }
}
