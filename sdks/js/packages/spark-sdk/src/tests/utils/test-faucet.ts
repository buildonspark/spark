import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { Address, OutScript, SigHash, Transaction } from "@scure/btc-signer";
import { TransactionInput, TransactionOutput } from "@scure/btc-signer/psbt";
import { taprootTweakPrivKey } from "@scure/btc-signer/utils";
import {
  getP2TRAddressFromPublicKey,
  getP2TRScriptFromPublicKey,
} from "../../utils/bitcoin.js";
import { getNetwork, Network } from "../../utils/network.js";
import { SparkWallet } from "../../spark-wallet.js";
import { sha256 } from "@noble/hashes/sha256";
import * as btc from "@scure/btc-signer";
import { ripemd160 } from "@noble/hashes/ripemd160";

export type FaucetCoin = {
  key: Uint8Array;
  outpoint: TransactionInput;
  txout: TransactionOutput;
};

// The amount of satoshis to put in each faucet coin to be used in tests
const COIN_AMOUNT = 10_000_000n;
const FEE_AMOUNT = 1000n;

export class BitcoinFaucet {
  private coins: FaucetCoin[] = [];
  private static instance: BitcoinFaucet | null = null;

  constructor(
    private url: string = "http://127.0.0.1:8332",
    private username: string = "testutil",
    private password: string = "testutilpassword",
  ) {
    if (BitcoinFaucet.instance) {
      return BitcoinFaucet.instance;
    }

    BitcoinFaucet.instance = this;
  }

  async fund() {
    // If no coins available, refill the faucet
    if (this.coins.length === 0) {
      await this.refill();
    }

    // Take the first coin from the faucet
    const coin = this.coins[0];
    // Remove the used coin from the array
    this.coins = this.coins.slice(1);

    return coin;
  }

  async refill() {
    // Generate key for initial block reward
    const key = secp256k1.utils.randomPrivateKey();
    const pubKey = secp256k1.getPublicKey(key);
    const address = getP2TRAddressFromPublicKey(pubKey, Network.LOCAL);

    // Mine a block to this address
    const blockHash = await this.generateToAddress(1, address);

    // Get block and funding transaction
    const block = await this.getBlock(blockHash[0]);
    const fundingTx = Transaction.fromRaw(hexToBytes(block.tx[0].hex), {
      allowUnknownOutputs: true,
    });

    // Mine 100 blocks to make funds spendable
    const randomKey = secp256k1.utils.randomPrivateKey();
    const randomPubKey = secp256k1.getPublicKey(randomKey);
    const randomAddress = getP2TRAddressFromPublicKey(
      randomPubKey,
      Network.LOCAL,
    );
    await this.generateToAddress(100, randomAddress);

    const fundingTxId = block.tx[0].txid;
    const fundingOutpoint: TransactionInput = {
      txid: fundingTxId,
      index: 0,
    };

    const splitTx = new Transaction();
    splitTx.addInput(fundingOutpoint);
    let initialValue = fundingTx.getOutput(0)!.amount!;
    const coinKeys: Uint8Array[] = [];

    while (initialValue > COIN_AMOUNT + 100_000n) {
      const coinKey = secp256k1.utils.randomPrivateKey();
      const coinPubKey = secp256k1.getPublicKey(coinKey);
      coinKeys.push(coinKey);

      const script = getP2TRScriptFromPublicKey(coinPubKey, Network.LOCAL);
      splitTx.addOutput({
        script,
        amount: COIN_AMOUNT,
      });
      initialValue -= COIN_AMOUNT;
    }
    // Sign and broadcast
    const signedSplitTx = await this.signFaucetCoin(
      splitTx,
      fundingTx.getOutput(0)!,
      key,
    );

    await this.broadcastTx(bytesToHex(signedSplitTx.extract()));

    // Create faucet coins
    const splitTxId = signedSplitTx.id;
    for (let i = 0; i < signedSplitTx.outputsLength; i++) {
      this.coins.push({
        // @ts-ignore - It's a test file
        key: coinKeys[i],
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

    // Add the output using the address directly
    sendToPubKeyTx.addOutputAddress(
      p2wpkhAddress,
      COIN_AMOUNT,
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
    // Mine 100 blocks to make funds spendable
    const randomKey = secp256k1.utils.randomPrivateKey();
    const randomPubKey = secp256k1.getPublicKey(randomKey);
    const randomAddress = getP2TRAddressFromPublicKey(
      randomPubKey,
      Network.LOCAL,
    );
    return await this.generateToAddress(numBlocks, randomAddress);
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
        throw new Error(`Bitcoin RPC error: ${data.error.message}`);
      }

      return data.result;
    } catch (error) {
      console.error("Error calling Bitcoin RPC:", error);
      throw error;
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

    tx.addOutputAddress(address, amount, getNetwork(Network.LOCAL));

    const changeAmount = availableAmount - amount;
    if (changeAmount > 0) {
      const changeKey = secp256k1.utils.randomPrivateKey();
      const changePubKey = secp256k1.getPublicKey(changeKey);
      const changeScript = getP2TRScriptFromPublicKey(changePubKey, Network.LOCAL);
      tx.addOutput({
        script: changeScript,
        amount: changeAmount,
      });
    }

    const signedTx = await this.signFaucetCoin(tx, coin.txout, coin.key);
    const txHex = bytesToHex(signedTx.extract());
    await this.broadcastTx(txHex);

    return signedTx;
  }
}
