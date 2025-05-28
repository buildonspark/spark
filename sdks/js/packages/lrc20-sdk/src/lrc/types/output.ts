import { address, networks, opcodes, type Payment, type Network, payments, script } from "bitcoinjs-lib";
import * as bitcoin from "bitcoinjs-lib";
import { Receipt } from "./receipt.ts";
import { ReceiptProof, ReceiptProofType } from "./receipt-proof.ts";
import { toXOnly } from "../../utils.ts";
import { SparkExitMetadata } from "./spark.ts";

export class TxOutput {
  type: string;
  satoshis: number;

  constructor(type: string, satoshis: number) {
    this.type = type;
    this.satoshis = satoshis;
  }
}

export class BitcoinOutput extends TxOutput {
  receiverPubKey: Buffer;
  bech32Result: bitcoin.address.Bech32Result;

  constructor(receiverPubKey: Buffer, satoshis: number) {
    super("BitcoinOutput", satoshis); // Initialize the base class with the type
    this.receiverPubKey = receiverPubKey;
  }

  public static createFromRaw(receiverBech32: string, satoshis: number): BitcoinOutput {
    const receiverAddr = address.fromBech32(receiverBech32);
    const bitcoinOutput = new BitcoinOutput(receiverAddr.data, satoshis);
    bitcoinOutput.bech32Result = receiverAddr;
    return bitcoinOutput;
  }
}

export class ReceiptOutput extends TxOutput {
  receiverPubKey: Buffer;
  receipt: Receipt;

  constructor(receiverPubKey: Buffer, satoshis: number, receipt: Receipt) {
    super("ReceiptOutput", satoshis);
    this.receiverPubKey = receiverPubKey;
    this.receipt = receipt;
  }

  public static createFromRaw(receiverInnerKey: Buffer, satoshis: number, receipt: Receipt): ReceiptOutput {
    // TODO: check on receiver data
    return new ReceiptOutput(receiverInnerKey, satoshis, receipt);
  }

  public toReceiptProof(donotconvert?: boolean): ReceiptProof {
    if (!donotconvert && this.receipt.isEmptyReceipt()) {
      return {
        type: ReceiptProofType.EmptyReceipt,
        data: {
          receipt: this.receipt,
          innerKey: this.receiverPubKey.toString("hex"),
        },
      };
    }
    return {
      type: ReceiptProofType.Sig,
      data: {
        innerKey: this.receiverPubKey.toString("hex"),
        receipt: this.receipt,
      },
    };
  }
}

export class SparkExitOutput extends TxOutput {
  revocationPubkey: Buffer;
  delayPubkey: Buffer;
  locktime: number;
  receipt: Receipt;
  metadata?: SparkExitMetadata;

  constructor(
    revocationPubkey: Buffer,
    delayPubley: Buffer,
    locktime: number,
    satoshis: number,
    receipt: Receipt,
    metadata?: SparkExitMetadata,
  ) {
    super("SparkExitOutput", satoshis);
    this.revocationPubkey = revocationPubkey;
    this.delayPubkey = delayPubley;
    this.locktime = locktime;
    this.receipt = receipt;
    this.metadata = metadata; // Initialize metadata
  }

  public static createFromRaw(
    revocationPubkey: Buffer,
    delayPubley: Buffer,
    locktime: number,
    satoshis: number,
    receipt: Receipt,
    metadata?: SparkExitMetadata,
  ) {
    return new SparkExitOutput(revocationPubkey, delayPubley, locktime, satoshis, receipt, metadata); // Pass metadata
  }

  public toReceiptProof(): ReceiptProof {
    return {
      type: ReceiptProofType.SparkExit,
      data: {
        script: {
          revocation_key: this.revocationPubkey.toString("hex"),
          delay_key: this.delayPubkey.toString("hex"),
          locktime: this.locktime,
        },
        receipt: this.receipt,
        metadata: this.metadata,
      },
    };
  }
}

export class MultisigReceiptOutput extends TxOutput {
  receiversPubKeys: Array<Buffer>;
  m: number;
  receipt: Receipt;
  cltvOutputLocktime?: number;
  expiryKey?: Buffer;

  constructor(
    receiversPubKeys: Array<Buffer>,
    m: number,
    satoshis: number,
    receipt: Receipt,
    locktime?: number,
    expiryKey?: Buffer,
  ) {
    super("MultisigReceiptOutput", satoshis);
    this.receiversPubKeys = receiversPubKeys;
    this.m = m;
    this.receipt = receipt;
    this.cltvOutputLocktime = locktime;
    this.expiryKey = expiryKey;
  }

  public static createFromRaw(
    receiversPubKeys: Array<Buffer>,
    m: number,
    satoshis: number,
    receipt: Receipt,
    locktime?: number,
    expiryKey?: Buffer,
  ): MultisigReceiptOutput {
    return new MultisigReceiptOutput(receiversPubKeys, m, satoshis, receipt, locktime, expiryKey);
  }

  public toReceiptProof(network: Network): ReceiptProof {
    return {
      type: ReceiptProofType.P2WSH,
      data: {
        innerKey: this.receiversPubKeys.sort()[0].toString("hex"),
        receipt: this.receipt,
        script: this.toScript(network).redeem.output.toString("hex"),
      },
    };
  }

  public toScript(network: Network): Payment {
    let pubkeys = Array.from(this.receiversPubKeys);
    pubkeys.sort((a, b) => toXOnly(a).compare(toXOnly(b)));

    const multisigReceiptKey = Receipt.receiptPublicKey(pubkeys[0], this.receipt);
    pubkeys[0] = multisigReceiptKey;
    return this.multisigScriptPubKey(network, this.m, pubkeys, this.expiryKey || pubkeys[1], this.cltvOutputLocktime);
  }

  private multisigScriptPubKey(
    network: Network,
    m: number,
    pubkeys: Buffer[],
    expirySpenderKey: Buffer,
    cltvOutputLocktime?: number,
  ): Payment {
    const p2ms = payments.p2ms({
      m: m,
      pubkeys: pubkeys,
      network: network,
    });

    if (!cltvOutputLocktime || cltvOutputLocktime == 0) {
      return payments.p2wsh({
        redeem: p2ms,
        network: network,
      });
    }

    const cltvScript = script.compile([
      script.number.encode(cltvOutputLocktime),
      opcodes.OP_CHECKLOCKTIMEVERIFY,
      opcodes.OP_DROP,
      expirySpenderKey,
      opcodes.OP_CHECKSIG,
    ]);

    const redeemScript = script.compile([
      opcodes.OP_IF,
      ...p2ms.output,
      opcodes.OP_ELSE,
      ...cltvScript,
      opcodes.OP_ENDIF,
    ]);

    return payments.p2wsh({
      redeem: {
        output: redeemScript,
        network: network,
      },
      network: network,
    });
  }
}

export class OPReturnOutput extends TxOutput {
  data: Buffer[];

  constructor(satoshis: number, data: Buffer[]) {
    super("OPReturnOutput", satoshis);
    this.data = data;
  }
}
