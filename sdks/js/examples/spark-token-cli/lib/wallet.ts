import { Buffer } from "buffer";
import { initEccLib, payments, Psbt, type Network } from "bitcoinjs-lib";
import * as ecc from "@bitcoinerlab/secp256k1";
import { ECPairFactory, type ECPairInterface } from "ecpair";

import { NetworkType } from "./types.js";
import { TokenPubkeyAnnouncement } from "./announcement.js";
import { ElectrsApi, type BasicAuth, type BitcoinUtxo } from "./electrs-api.js";

initEccLib(ecc);
const ECPair = ECPairFactory(ecc);

const DUST_AMOUNT = 354;

// OP_RETURN prefix for LRC20 token pubkey announcement
const OP_RETURN_ANNOUNCEMENT_PREFIX = Buffer.from([76, 82, 67, 50, 48, 0, 0]);

export interface Lrc20AnnouncementWalletConfig {
  electrsUrl: string;
  electrsCredentials?: BasicAuth;
}

export interface Lrc20Transaction {
  bitcoin_tx: {
    toHex(): string;
  };
}

export class Lrc20AnnouncementWallet {
  public pubkey: Buffer;
  private keyPair: ECPairInterface;
  private network: Network;
  private electrsApi: ElectrsApi;
  private btcUtxos: BitcoinUtxo[] = [];

  constructor(
    privateKeyHex: string,
    btcNetwork: Network,
    _networkType: NetworkType,
    config: Lrc20AnnouncementWalletConfig,
  ) {
    this.network = btcNetwork;
    this.keyPair = ECPair.fromPrivateKey(Buffer.from(privateKeyHex, "hex"), {
      network: this.network,
    });
    this.pubkey = this.keyPair.publicKey;
    this.electrsApi = new ElectrsApi(
      config.electrsUrl,
      config.electrsCredentials ?? null,
    );
  }

  public async syncWallet(): Promise<void> {
    const p2wpkhPayment = payments.p2wpkh({
      pubkey: this.keyPair.publicKey,
      network: this.network,
    });
    const address = p2wpkhPayment.address!;
    this.btcUtxos = await this.electrsApi.fetchUtxos(address);
  }

  public async prepareAnnouncement(
    announcement: TokenPubkeyAnnouncement,
    feeRateSatsPerVb: number,
  ): Promise<Lrc20Transaction> {
    const psbt = new Psbt({ network: this.network });
    psbt.setVersion(2);
    psbt.setLocktime(0);

    // Select UTXOs for fees (need enough for OP_RETURN + change output + fees)
    const selectedUtxos = this.selectUtxosForFee(feeRateSatsPerVb, 2);

    // Add inputs
    for (const utxo of selectedUtxos) {
      psbt.addInput({
        hash: utxo.txid,
        index: Number(utxo.vout),
        nonWitnessUtxo: Buffer.from(utxo.hex, "hex"),
      });
    }

    // Build OP_RETURN output with announcement data
    const opReturnData = Buffer.concat([
      OP_RETURN_ANNOUNCEMENT_PREFIX,
      announcement.toBuffer(),
    ]);
    const opReturnPayment = payments.embed({ data: [opReturnData] });

    psbt.addOutput({
      script: opReturnPayment.output!,
      value: 0,
    });

    // Calculate fee and add change output
    const inputSum = selectedUtxos.reduce(
      (sum, utxo) => sum + utxo.satoshis,
      0,
    );
    const estimatedVsize = this.estimateVsize(selectedUtxos.length, 2);
    const fee = Math.ceil(estimatedVsize * feeRateSatsPerVb);
    const changeAmount = inputSum - fee;

    if (changeAmount < DUST_AMOUNT) {
      throw new Error(
        `Not enough funds for announcement. Need at least ${fee + DUST_AMOUNT} sats, have ${inputSum} sats`,
      );
    }

    // Add change output (P2WPKH back to self)
    const changePayment = payments.p2wpkh({
      pubkey: this.keyPair.publicKey,
      network: this.network,
    });

    psbt.addOutput({
      script: changePayment.output!,
      value: changeAmount,
    });

    // Sign all inputs
    for (let i = 0; i < selectedUtxos.length; i++) {
      psbt.signInput(i, this.keyPair);
    }

    psbt.finalizeAllInputs();

    const tx = psbt.extractTransaction();

    return {
      bitcoin_tx: tx,
    };
  }

  public async broadcastRawBtcTransaction(txHex: string): Promise<string> {
    return this.electrsApi.sendTransaction(txHex);
  }

  private selectUtxosForFee(
    feeRateSatsPerVb: number,
    numOutputs: number,
  ): BitcoinUtxo[] {
    if (this.btcUtxos.length === 0) {
      throw new Error("No UTXOs available");
    }

    // Sort by value descending
    const sortedUtxos = [...this.btcUtxos].sort(
      (a, b) => b.satoshis - a.satoshis,
    );

    const selected: BitcoinUtxo[] = [];
    let totalSelected = 0;

    for (const utxo of sortedUtxos) {
      selected.push(utxo);
      totalSelected += utxo.satoshis;

      // Estimate if we have enough
      const estimatedVsize = this.estimateVsize(selected.length, numOutputs);
      const estimatedFee = Math.ceil(estimatedVsize * feeRateSatsPerVb);
      const requiredAmount = estimatedFee + DUST_AMOUNT;

      if (totalSelected >= requiredAmount) {
        return selected;
      }
    }

    throw new Error("Not enough UTXOs to cover fees");
  }

  private estimateVsize(numInputs: number, numOutputs: number): number {
    // P2WPKH input: ~68 vbytes, P2WPKH output: ~31 vbytes, OP_RETURN: ~40-80 vbytes
    // Overhead: ~11 vbytes
    return numInputs * 68 + numOutputs * 31 + 11 + 50; // 50 extra for OP_RETURN data
  }
}
