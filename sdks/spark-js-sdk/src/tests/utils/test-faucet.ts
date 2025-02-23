import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { SigHash, Transaction } from "@scure/btc-signer";
import { TransactionInput, TransactionOutput } from "@scure/btc-signer/psbt";
import { taprootTweakPrivKey } from "@scure/btc-signer/utils";
import {
  getP2TRAddressFromPublicKey,
  getP2TRScriptFromPublicKey,
} from "../../utils/bitcoin.js";
import { Network } from "../../utils/network.js";

export type FaucetCoin = {
  key: Uint8Array;
  outpoint: TransactionInput;
  txout: TransactionOutput;
};

export class BitcoinFaucet {
  private coins: FaucetCoin[] = [];
  private static instance: BitcoinFaucet | null = null;

  constructor(
    private url: string,
    private username: string,
    private password: string
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
      Network.LOCAL
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
    const coinAmount = 10_000_000n;
    const coinKeys: Uint8Array[] = [];

    while (initialValue > coinAmount + 100_000n) {
      const coinKey = secp256k1.utils.randomPrivateKey();
      const coinPubKey = secp256k1.getPublicKey(coinKey);
      coinKeys.push(coinKey);

      const script = getP2TRScriptFromPublicKey(coinPubKey, Network.LOCAL);
      splitTx.addOutput({
        script,
        amount: coinAmount,
      });
      initialValue -= coinAmount;
    }
    // Sign and broadcast
    const signedSplitTx = await this.signFaucetCoin(
      splitTx,
      fundingTx.getOutput(0)!,
      key
    );

    await this.broadcastTx(bytesToHex(signedSplitTx.extract()));

    // Create faucet coins
    const splitTxId = signedSplitTx.id;
    for (let i = 0; i < signedSplitTx.outputsLength; i++) {
      this.coins.push({
        key: coinKeys[i],
        outpoint: {
          txid: hexToBytes(splitTxId),
          index: i,
        },
        txout: signedSplitTx.getOutput(i)!,
      });
    }
  }

  async signFaucetCoin(
    unsignedTx: Transaction,
    fundingTxOut: TransactionOutput,
    key: Uint8Array
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
      new Array(unsignedTx.inputsLength).fill(fundingTxOut.amount!)
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
    return await this.call("sendrawtransaction", [txHex, 0]);
  }
}
