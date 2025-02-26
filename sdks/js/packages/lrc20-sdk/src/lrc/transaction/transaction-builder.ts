import { ECPairInterface } from "ecpair";
import {
  BitcoinOutput,
  MultisigReceiptOutput,
  OPReturnOutput,
  ReceiptOutput,
  SparkExitOutput,
  TxOutput,
} from "../types/output";
import {
  TokenPubkeyAnnouncement,
  TxFreezeAnnouncement,
  FreezeTxToggle,
  TransferOwnershipAnnouncement,
  PubkeyFreezeAnnouncement,
} from "../types/lrc20-transaction";
import { findNotFirstUsingFind } from "../utils/array";
import { reverseBuffer, toEvenParity } from "../utils/buffer";
import { Psbt, Payment, payments, networks, Transaction, address, script, opcodes } from "bitcoinjs-lib";
import { BitcoinInput, MultisigReceiptInput, ReceiptInput, TxInput } from "../types/input";
import { Receipt } from "../types/receipt";
import { PARITY } from "../utils/constants";
import { TokenPubkey } from "../types/token-pubkey";
import { TokenAmount } from "../types/token-amount";
import { ReceiptProof } from "../types/receipt-proof";
import * as varuint from "varuint-bitcoin";
import { toXOnly } from "../../utils";
import { privateNegate } from "@bitcoinerlab/secp256k1";
import { ECPair } from "../../bitcoin-core";
import { Tapleaf } from "bitcoinjs-lib/src/types";

export class TransactionBuilder {
  private keyPair: ECPairInterface;
  private network: networks.Network;

  constructor(keyPair: ECPairInterface, network: networks.Network) {
    this.keyPair = keyPair;
    this.network = network;
  }

  async buildTransferOwnershipOutput(transferAnnouncement: TransferOwnershipAnnouncement) {
    const opReturnPrefixBuff = Buffer.from([121, 117, 118, 0, 3]);
    console.log("buildTransferOwnershipOutput: ", transferAnnouncement);
    return {
      type: "OPReturnOutput",
      satoshis: 0,
      data: [opReturnPrefixBuff, transferAnnouncement.toBuffer()],
    };
  }

  async buildAnnouncementOutput(tokenPubkeyAnnouncement: TokenPubkeyAnnouncement) {
    const opReturnPrefixBuff = Buffer.from([121, 117, 118, 0, 0]);
    console.log("buildAnnouncementOutput: ", tokenPubkeyAnnouncement);
    return {
      type: "OPReturnOutput",
      satoshis: 0,
      data: [opReturnPrefixBuff, tokenPubkeyAnnouncement.toBuffer()],
    };
  }

  async buildIssuanceOutput(futureOutputs: Array<TxOutput>) {
    const receiptsOutputs = futureOutputs
      .filter((item) => item.type === "ReceiptOutput" || item.type === "MultisigReceiptOutput")
      .map((item) => item as ReceiptOutput);

    if (findNotFirstUsingFind(receiptsOutputs.map((receipt) => receipt.receipt.tokenPubkey.pubkey))) {
      throw new Error("Found other tokenPubkeys");
    }

    const opReturnPrefixBuff = Buffer.from([121, 117, 118, 0, 2]);
    const receiptsSum = receiptsOutputs.reduce(
      (acc, currentValue) => acc + (currentValue as ReceiptOutput).receipt.tokenAmount.amount,
      BigInt(0)
    );
    const receiptsSumLEBuff = reverseBuffer(
      Buffer.from(receiptsSum.toString(16).padStart(32, "0").slice(0, 32), "hex")
    );
    const tokenPubkeyBuff = receiptsOutputs[0].receipt.tokenPubkey.pubkey;
    return {
      type: "OPReturnOutput",
      satoshis: 0,
      data: [Buffer.concat([opReturnPrefixBuff, tokenPubkeyBuff, receiptsSumLEBuff])],
    };
  }

  // TODO: build freeze
  async buildFreezeOutput(freeze: TxFreezeAnnouncement | PubkeyFreezeAnnouncement) {
    const opReturnPrefixBuff = Buffer.from([121, 117, 118, 0, 1]);

    if (freeze instanceof PubkeyFreezeAnnouncement) {
      const tokenPubkeyBuff = freeze.tokenPubkey.inner;
      const ownerPubkeyBuff = freeze.ownerPubkey;

      return {
        type: "OPReturnOutput",
        satoshis: 0,
        data: [Buffer.concat([opReturnPrefixBuff, ownerPubkeyBuff, tokenPubkeyBuff])],
      };
    } else {
      const { txid, vout } = freeze.outpoint;
      const txidBuff = Buffer.from(txid, "hex");
      const indexBuff = Buffer.from(vout.toString(16).padStart(8, "0"), "hex");
      const tokenPubkeyBuff = freeze.tokenPubkey.inner;

      return {
        type: "OPReturnOutput",
        satoshis: 0,
        data: [Buffer.concat([opReturnPrefixBuff, txidBuff, indexBuff, tokenPubkeyBuff])],
      };
    }
  }

  buildAndSignTransaction(
    inputs: TxInput[],
    outputs: TxOutput[],
    changeOutput: TxOutput,
    feeRateVb: number,
    privateKeys?: Array<ECPairInterface>,
    locktime: number = 0,
    sequence?: number
  ): Transaction {
    const psbt = new Psbt({ network: this.network });
    psbt.setVersion(2);
    psbt.setLocktime(locktime);

    let changeOutputConstructed = this.updateChangeOutput(
      psbt.clone(),
      inputs,
      outputs,
      changeOutput,
      feeRateVb,
      sequence,
      privateKeys
    );

    this.constructPsbtFromInsAndOuts(psbt, [...inputs], [...outputs, changeOutputConstructed], privateKeys, sequence);

    return psbt.extractTransaction();
  }

  private constructPsbtFromInsAndOuts(
    psbt: Psbt,
    inputs: TxInput[],
    outputs: TxOutput[],
    privateKeys: Array<ECPairInterface> = [],
    sequence?: number
  ): Psbt {
    outputs.forEach((output) => {
      psbt.addOutput({
        script: this.outputToPayment(output).output!,
        value: output.satoshis,
      });
    });

    inputs.forEach((input, i) => {
      psbt.addInput({
        hash: input.txId,
        index: input.index,
        nonWitnessUtxo: Buffer.from(input.hex, "hex"),
      });

      if (sequence) {
        psbt.setInputSequence(i, sequence);
      }
    });

    inputs.forEach((input, i) => {
      switch (input.type) {
        case "BitcoinInput":
          psbt.signInput(i, this.keyPair).finalizeInput(i);
          break;
        case "ReceiptInput":
          if (!(input as ReceiptInput).isP2WSH) {
            const sigReceiptPrivateKey = Receipt.receiptPrivateKey(this.keyPair, (input as ReceiptInput).proof);
            const tweakedKeyPair = ECPair.fromPrivateKey(sigReceiptPrivateKey);
            psbt.signInput(i, tweakedKeyPair).finalizeInput(i);
            break;
          }
        case "MultisigReceiptInput":
          privateKeys = privateKeys.concat([this.keyPair]);
          privateKeys.sort((a, b) => {
            const aPubKey = a.publicKey || a.getPublicKey();
            const bPubKey = b.publicKey || b.getPublicKey();
            return toXOnly(aPubKey).compare(toXOnly(bPubKey));
          });

          // Negate private keys with odd pubkeys
          for (let j = 1; j < privateKeys.length; j++) {
            const currentPk = privateKeys[j];
            const pubkeyParity = Buffer.from([currentPk.publicKey[0]]);
            if (!pubkeyParity.equals(PARITY)) {
              let privKey = currentPk.privateKey!;
              let negatedPrivKey = privateNegate(privKey);
              privateKeys[j] = ECPair.fromPrivateKey(Buffer.from(negatedPrivKey));
            }
          }

          const multisigReceiptPrivateKey = Receipt.receiptPrivateKey(
            privateKeys[0],
            (input as MultisigReceiptInput).proof
          );

          const multisigTweakedKeyPair = ECPair.fromPrivateKey(multisigReceiptPrivateKey, { network: this.network });
          privateKeys[0] = multisigTweakedKeyPair;

          const witnessScript = Buffer.from((input as ReceiptInput).script!, "hex");

          console.log("SCRIPT:", script.toASM(witnessScript));
          psbt.updateInput(i, {
            witnessScript: witnessScript,
            witnessUtxo: { script: witnessScript, value: input.satoshis },
          });

          for (let keyPair of privateKeys) {
            psbt.signInput(i, keyPair);
          }

          const signatures = privateKeys
            .map((keyPair) => {
              const sig = psbt.data.inputs[i].partialSig.find((sig) => sig.pubkey.equals(keyPair.publicKey));
              return sig ? sig.signature : undefined;
            })
            .filter((sig) => sig);

          const witnessStack =
            signatures.length == 1
              ? [...signatures, Buffer.from([]), witnessScript]
              : [Buffer.from([]), ...signatures, Buffer.from([1]), witnessScript];

          psbt.finalizeInput(i, () => ({
            finalScriptSig: undefined,
            finalScriptWitness: this.witnessStackToScriptWitness(witnessStack),
          }));
      }
    });

    return psbt;
  }

  private witnessStackToScriptWitness(witness: Buffer[]): Buffer {
    let buffer = Buffer.from([]);

    buffer = this.writeVarInt(witness.length, buffer);
    witness.forEach((witnessElement) => {
      buffer = this.writeVarInt(witnessElement.length, buffer);
      buffer = Buffer.concat([buffer, Buffer.from(witnessElement)]);
    });

    return buffer;
  }

  private writeVarInt(i: number, buffer: Buffer): Buffer {
    const currentLen = buffer.length;
    const varintLen = varuint.encodingLength(i);

    buffer = Buffer.concat([buffer, Buffer.allocUnsafe(varintLen)]);
    varuint.encode(i, buffer, currentLen);
    return buffer;
  }

  private updateChangeOutput(
    psbt: Psbt,
    inputs: TxInput[],
    outputs: TxOutput[],
    changeOutput: TxOutput,
    feeRateVb: number,
    sequence?: number,
    privateKeys?: Array<ECPairInterface>
  ) {
    const psbtToEstimate = this.constructPsbtFromInsAndOuts(
      psbt,
      inputs,
      [...outputs, changeOutput],
      privateKeys,
      sequence
    );
    const fee = Math.ceil(this.estimateFee(psbtToEstimate, feeRateVb));

    const inputsSum = this.sumSatoshis(inputs);
    const outputsSum = this.sumSatoshis(outputs);

    const change = inputsSum - outputsSum - changeOutput.satoshis - fee;

    if (change < 0) {
      throw new Error("Not enough satoshis to pay fees");
    }

    changeOutput.satoshis = changeOutput.satoshis + change;
    return changeOutput;
  }

  private estimateFee(feeEstimationPsbt: Psbt, feeRateVb: number): number {
    // feeEstimationPsbt.txInputs.forEach(input => {
    //   console.log(reverseBuffer(input.hash).toString("hex"), input.index)
    // })

    // feeEstimationPsbt.txOutputs.forEach(output => {
    //   console.log(output);
    // })

    const feeEstimationTx = feeEstimationPsbt.extractTransaction(true);
    return (feeEstimationTx.virtualSize() + feeEstimationTx.ins.length) * feeRateVb;
  }

  public outputToPayment(output: TxOutput): Payment {
    let payment: Payment;

    switch (output.type) {
      case "BitcoinOutput":
        const { bech32Result, receiverPubKey } = output as BitcoinOutput;
        const hash = bech32Result?.data?.length ? bech32Result.data : undefined;
        const pubkey = hash ? undefined : receiverPubKey;
        if (hash) {
          payment = payments.p2wpkh({
            hash,
            network: this.network,
          });
        } else {
          payment = payments.p2wpkh({
            pubkey,
            network: this.network,
          });
        }
        break;
      case "ReceiptOutput":
        const receiptKey = Receipt.receiptPublicKey(
          (output as ReceiptOutput).receiverPubKey,
          (output as ReceiptOutput).receipt
        );

        payment = payments.p2wpkh({
          pubkey: Buffer.from(receiptKey),
          network: this.network,
        });
        break;
      case "MultisigReceiptOutput":
        payment = (output as MultisigReceiptOutput).toScript(this.network);

        break;
      case "OPReturnOutput":
        payment = payments.embed({ data: (output as OPReturnOutput).data });
        break;
      case "SparkExitOutput":
        const { revocationPubkey, delayPubkey, locktime, receipt } = output as SparkExitOutput;
        const tweakedDelayKey = Receipt.receiptPublicKey(delayPubkey, receipt);

        const scriptPathScript = script.compile([
          script.number.encode(locktime),
          opcodes.OP_CHECKLOCKTIMEVERIFY,
          opcodes.OP_DROP,
          toXOnly(tweakedDelayKey),
          opcodes.OP_CHECKSIG,
        ]);

        const tapLeaf: Tapleaf = { output: scriptPathScript, version: 0 };

        payment = payments.p2tr({
          internalPubkey: toXOnly(revocationPubkey),
          scriptTree: tapLeaf,
          network: this.network,
        });
        break;
      default:
        throw new Error("Output type is unknown");
    }

    return payment;
  }

  private sumSatoshis(data: (TxInput | TxOutput)[]): number {
    return data.reduce((accumulator, currentValue) => accumulator + (currentValue as any).satoshis, 0);
  }

  buildAndSignMakerPsbt(inputs: TxInput[], output: TxOutput): Transaction {
    const psbt = this.constructMakerPsbtFromInsAndOuts(inputs, output);

    return psbt.extractTransaction();
  }

  buildAndSignOneInputTx(input: TxInput): Transaction {
    const psbt = new Psbt({ network: this.network });
    psbt.setVersion(2);
    psbt.setLocktime(0);

    psbt.addInput({
      hash: input.txId,
      index: input.index,
      nonWitnessUtxo: Buffer.from(input.hex, "hex"),
      sighashType: Transaction.SIGHASH_NONE + Transaction.SIGHASH_ANYONECANPAY,
    });

    switch (input.type) {
      case "BitcoinInput":
        psbt.signInput(0, this.keyPair, [Transaction.SIGHASH_NONE + Transaction.SIGHASH_ANYONECANPAY]).finalizeInput(0);
        break;
      case "ReceiptInput":
        const receiptPrivateKey = Receipt.receiptPrivateKey(this.keyPair, (input as ReceiptInput).proof);
        const tweakedKeyPair = ECPair.fromPrivateKey(receiptPrivateKey);
        psbt
          .signInput(0, tweakedKeyPair, [Transaction.SIGHASH_NONE + Transaction.SIGHASH_ANYONECANPAY])
          .finalizeInput(0);
    }

    return psbt.extractTransaction(true);
  }

  constructTakerSingePsbtFromInsAndOuts(input: TxInput, output: TxOutput): Psbt {
    const psbt = new Psbt({ network: this.network });
    psbt.setVersion(2);
    psbt.setLocktime(0);

    psbt.addOutput({
      script: this.outputToPayment(output).output!,
      value: output.satoshis,
    });

    psbt.addInput({
      hash: input.txId,
      index: input.index,
      nonWitnessUtxo: Buffer.from(input.hex, "hex"),
      sighashType: Transaction.SIGHASH_SINGLE + Transaction.SIGHASH_ANYONECANPAY,
    });

    switch (input.type) {
      case "BitcoinInput":
        psbt.signInput(0, this.keyPair, [Transaction.SIGHASH_SINGLE + Transaction.SIGHASH_ANYONECANPAY]);
        break;
      case "ReceiptInput":
        const receiptPrivateKey = Receipt.receiptPrivateKey(this.keyPair, (input as ReceiptInput).proof);
        const tweakedKeyPair = ECPair.fromPrivateKey(receiptPrivateKey);
        psbt.signInput(0, tweakedKeyPair, [Transaction.SIGHASH_SINGLE + Transaction.SIGHASH_ANYONECANPAY]);
    }

    psbt.finalizeInput(0);

    return psbt;
  }

  constructTakerSingleSignature(input: MultisigReceiptInput, bePubkey: Buffer): Psbt {
    const psbt = new Psbt({ network: this.network });
    psbt.setVersion(2);
    psbt.setLocktime(0);

    psbt.addInput({
      hash: input.txId,
      index: input.index,
      nonWitnessUtxo: Buffer.from(input.hex, "hex"),
      sighashType: Transaction.SIGHASH_NONE + Transaction.SIGHASH_ANYONECANPAY,
    });

    let keyPair = this.keyPair;
    let pubkey = keyPair.publicKey;
    let pubkeys = [pubkey, bePubkey].sort((a, b) => {
      return toXOnly(a).compare(toXOnly(b));
    });
    if (pubkeys[0].equals(keyPair.publicKey)) {
      const multisigReceiptPrivateKey = Receipt.receiptPrivateKey(keyPair, input.proof);

      keyPair = ECPair.fromPrivateKey(multisigReceiptPrivateKey, { network: this.network });
    } else {
      const pubkeyParity = Buffer.from([pubkey[0]]);
      if (!pubkeyParity.equals(PARITY)) {
        let privKey = keyPair.privateKey!;
        let negatedPrivKey = privateNegate(privKey);
        keyPair = ECPair.fromPrivateKey(Buffer.from(negatedPrivKey));
      }
    }

    const witnessScript = input.script;

    psbt.updateInput(0, {
      witnessScript: witnessScript,
      witnessUtxo: { script: witnessScript, value: input.satoshis },
    });

    psbt.signInput(0, keyPair, [Transaction.SIGHASH_NONE + Transaction.SIGHASH_ANYONECANPAY]);

    return psbt;
  }

  private constructMakerPsbtFromInsAndOuts(inputs: TxInput[], output: TxOutput): Psbt {
    const psbt = new Psbt({ network: this.network });
    psbt.setVersion(2);
    psbt.setLocktime(0);

    psbt.addOutput({
      script: this.outputToPayment(output).output!,
      value: output.satoshis,
    });

    inputs.forEach((input, i) => {
      psbt.addInput({
        hash: input.txId,
        index: input.index,
        nonWitnessUtxo: Buffer.from(input.hex, "hex"),
        sighashType: Transaction.SIGHASH_SINGLE + Transaction.SIGHASH_ANYONECANPAY,
      });
    });

    inputs.forEach((input, i) => {
      switch (input.type) {
        case "BitcoinInput":
          psbt.signInput(i, this.keyPair, [Transaction.SIGHASH_SINGLE + Transaction.SIGHASH_ANYONECANPAY]);
          break;
        case "ReceiptInput":
          const receiptPrivateKey = Receipt.receiptPrivateKey(this.keyPair, (input as ReceiptInput).proof);
          const tweakedKeyPair = ECPair.fromPrivateKey(receiptPrivateKey);
          psbt.signInput(i, tweakedKeyPair, [Transaction.SIGHASH_SINGLE + Transaction.SIGHASH_ANYONECANPAY]);
      }

      psbt.finalizeInput(i);
    });

    return psbt;
  }

  buildAndSignTakerPsbt(
    psbt: Psbt,
    makerInputs: TxInput[],
    takerInputs: TxInput[],
    outputs: TxOutput[],
    feeRateVb: number
  ): Transaction {
    let inputsToSign = Array.from({ length: takerInputs.length }, (_, index) => index);
    this.updateTakerPsbtChangeOutput(psbt.clone(), makerInputs, takerInputs, outputs, inputsToSign, feeRateVb);

    const psbtWithChange = this.constructTakerPsbtFromInsAndOuts(psbt, takerInputs, outputs, inputsToSign);

    psbtWithChange.txInputs.map((input, i) => {
      if (psbtWithChange.data.inputs[i].finalScriptWitness === undefined) {
        psbtWithChange.data.inputs[i].finalScriptWitness =
          psbtWithChange.data.inputs[psbtWithChange.data.inputs.length - 1].finalScriptWitness;
      }
    });

    return psbtWithChange.extractTransaction();
  }

  private updateTakerPsbtChangeOutput(
    psbt: Psbt,
    makerInputs: TxInput[],
    takerInputs: TxInput[],
    outputs: TxOutput[],
    inputsToSign: number[],
    feeRateVb: number
  ) {
    const psbtToEstimate = this.constructTakerPsbtFromInsAndOuts(psbt, takerInputs, outputs, inputsToSign);

    psbtToEstimate.txInputs.map((input, i) => {
      if (psbtToEstimate.data.inputs[i].finalScriptWitness === undefined) {
        psbtToEstimate.data.inputs[i].finalScriptWitness =
          psbtToEstimate.data.inputs[psbtToEstimate.data.inputs.length - 1].finalScriptWitness;
      }
    });

    const fee = Math.ceil(this.estimateFee(psbtToEstimate, feeRateVb));

    const inputsSum = this.sumSatoshis([...makerInputs, ...takerInputs]);
    const outputsSum = this.sumSatoshis(outputs);

    const change = inputsSum - outputsSum - fee - psbt.txOutputs[0].value;
    if (change < 0) {
      throw new Error("Not enough satoshis to pay fees");
    }

    outputs[outputs.length - 1].satoshis = change;
  }

  private constructTakerPsbtFromInsAndOuts(
    psbt: Psbt,
    inputs: TxInput[],
    outputs: TxOutput[],
    inputsToSign: number[]
  ): Psbt {
    outputs.forEach((output) => {
      psbt.addOutput({
        script: this.outputToPayment(output).output!,
        value: output.satoshis,
      });
    });

    const takerInputs = psbt.txInputs.length;
    inputs.forEach((input, i) => {
      psbt.addInput({
        hash: input.txId,
        index: input.index,
        nonWitnessUtxo: Buffer.from(input.hex, "hex"),
      });
    });

    inputs.forEach((input, i) => {
      if (inputsToSign.includes(i)) {
        switch (input.type) {
          case "BitcoinInput":
            psbt.signInput(takerInputs + i, this.keyPair);
            break;
          case "ReceiptInput":
            const receiptPrivateKey = Receipt.receiptPrivateKey(this.keyPair, (input as ReceiptInput).proof);
            const tweakedKeyPair = ECPair.fromPrivateKey(receiptPrivateKey);
            psbt.signInput(takerInputs + i, tweakedKeyPair);
        }
      }
    });

    inputsToSign.map((value) => {
      psbt.finalizeInput(takerInputs + value);
    });

    return psbt;
  }

  public signRawTransaction(unsignedTx: Transaction, prevouts: Map<String, String>): Transaction {
    let psbt = new Psbt({ network: this.network });
    psbt.setVersion(unsignedTx.version);
    psbt.setLocktime(unsignedTx.locktime);

    unsignedTx.outs.forEach((out) => {
      psbt.addOutput(out);
    });

    unsignedTx.ins.forEach((input) => {
      psbt.addInput({
        hash: input.hash,
        index: input.index,
        nonWitnessUtxo: Buffer.from(prevouts.get(input.hash.toString("hex")), "hex"),
      });
    });

    psbt.txInputs.forEach((_, index) => {
      psbt.signInput(index, this.keyPair);
    });

    psbt.finalizeAllInputs();

    return psbt.extractTransaction();
  }
}
