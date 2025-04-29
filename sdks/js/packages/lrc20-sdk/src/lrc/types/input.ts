import { Receipt } from "./receipt.ts";
import { ReceiptProof, ReceiptProofType } from "./receipt-proof.ts";

export class TxInput {
  type: string;
  txId: string;
  index: number;
  hex: string;
  satoshis: number;

  constructor(type: string, txId: string, index: number, hex: string, satoshis: number) {
    this.type = type;
    this.txId = txId;
    this.index = index;
    this.hex = hex;
    this.satoshis = satoshis;
  }
}

export class BitcoinInput extends TxInput {
  constructor(txId: string, index: number, hex: string, satoshis: number) {
    super("BitcoinInput", txId, index, hex, satoshis);
  }

  public static createFromRaw(txId: string, index: number, hex: string, satoshis: number) {
    return new BitcoinInput(txId, index, hex, satoshis);
  }
}

export class ReceiptInput extends TxInput {
  proof: Receipt;
  innerKey: string;
  isP2WSH: boolean;
  script?: string;

  constructor(
    txId: string,
    index: number,
    hex: string,
    satoshis: number,
    proof: Receipt,
    innerKey: string,
    isP2WSH = false,
    script?: string,
  ) {
    super("ReceiptInput", txId, index, hex, satoshis);
    this.proof = proof;
    this.innerKey = innerKey;
    this.isP2WSH = isP2WSH;
    this.script = script;
  }

  public static createFromRaw(
    txId: string,
    index: number,
    hex: string,
    satoshis: number,
    proof: Receipt,
    innerKey: string,
    isP2WSH = false,
    script?: string,
  ) {
    return new ReceiptInput(txId, index, hex, satoshis, proof, innerKey, isP2WSH, script);
  }

  toReceiptProofs(donotconvert?: boolean): ReceiptProof {
    if (!donotconvert && this.proof.isEmptyReceipt && this.proof.tokenAmount.amount === 0n) {
      return {
        type: ReceiptProofType.EmptyReceipt,
        data: {
          innerKey: this.innerKey,
          receipt: this.proof,
        },
      };
    }

    if (this.isP2WSH) {
      return {
        type: ReceiptProofType.P2WSH,
        data: {
          innerKey: this.innerKey,
          receipt: this.proof,
          script: this.script,
        },
      };
    }

    return {
      type: ReceiptProofType.Sig,
      data: {
        innerKey: this.innerKey,
        receipt: this.proof,
      },
    };
  }
}

export class MultisigReceiptInput extends TxInput {
  proof: Receipt;
  innerKeys: Array<string>;
  m: number;
  public script: Buffer;

  constructor(
    txId: string,
    index: number,
    hex: string,
    satoshis: number,
    proof: Receipt,
    innerKeys: Array<string>,
    m: number,
    script: Buffer,
  ) {
    super("MultisigReceiptInput", txId, index, hex, satoshis);
    this.proof = proof;
    this.innerKeys = innerKeys;
    this.m = m;
    this.script = script;
  }

  public static createFromRaw(
    txId: string,
    index: number,
    hex: string,
    satoshis: number,
    proof: Receipt,
    innerKeys: Array<string>,
    m: number,
    script: Buffer,
  ) {
    return new MultisigReceiptInput(txId, index, hex, satoshis, proof, innerKeys, m, script);
  }

  toReceiptProofs(): ReceiptProof {
    return {
      type: ReceiptProofType.Multisig,
      data: {
        innerKeys: this.innerKeys,
        m: this.m as number,
        receipt: this.proof,
      },
    };
  }
}
