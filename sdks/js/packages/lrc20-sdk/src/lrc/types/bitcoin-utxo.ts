import { TokenPubkey } from "./token-pubkey.ts";
import { TokenAmount } from "./token-amount.ts";
import { Receipt } from "./receipt.ts";
import {
  EmptyReceiptProof,
  EmptyReceiptProofData,
  ReceiptProof,
  ReceiptProofType,
  SigReceiptProof,
  SigReceiptProofData,
  getReceiptDataFromProof,
} from "./receipt-proof.ts";
import { Lrc20Transaction, Lrc20TransactionTypeEnum, IssueData, TransferData } from "./lrc20-transaction.ts";

export interface BitcoinUtxoDto {
  txid: string;
  hex: string;
  vout: bigint;
  value: number;
  status: BitcoinUtxoStatusDto;
}

export interface BitcoinUtxoStatusDto {
  confirmed: boolean;
  block_height: number;
  block_hash: string;
  block_time: number;
}

export class BitcoinUtxo {
  constructor(
    public txid: string,
    public vout: bigint,
    public satoshis: number,
    public status: BitcoinUtxoStatus,
    public hex: string = "",
  ) {}

  public static fromBitcoinUtxoDto(utxo: BitcoinUtxoDto): BitcoinUtxo {
    return new BitcoinUtxo(
      utxo.txid,
      utxo.vout,
      utxo.value,
      utxo.status ? BitcoinUtxoStatus.fromBitcoinUtxoStatusDto(utxo.status) : new BitcoinUtxoStatus(),
    );
  }
}

export interface Lrc20UtxoDto {
  txid: string;
  vout: bigint;
  value: number;
  index: number;
  tokenPubkey: string;
  amount: string;
  commitment: string | null;
  is_bulletproof: boolean;
  proof_type: string;
}

export class Lrc20Utxo extends BitcoinUtxo {
  innerKey: string;
  script?: string;

  constructor(
    txid: string,
    vout: bigint,
    satoshis: number,
    status: BitcoinUtxoStatus,
    public receipt: ReceiptProof,
    innerKey: string,
    script?: string,
  ) {
    super(txid, vout, satoshis, status);
    this.innerKey = innerKey;
    this.script = script;
  }

  public isEmptyReceipt() {
    return this.receipt.type === ReceiptProofType.EmptyReceipt || this.receipt.data.receipt.isEmptyReceipt();
  }

  public static fromLrc20UtxoDto(utxo: Lrc20UtxoDto, innerKey: string): Lrc20Utxo {
    const { txid, vout, index, value, tokenPubkey, amount, commitment, is_bulletproof, proof_type } = utxo;
    let proof: ReceiptProof;

    switch (proof_type) {
      case "Empty": {
        proof = {
          type: ReceiptProofType.EmptyReceipt,
          data: {
            receipt: Receipt.emptyReceipt(),
            innerKey,
          } as EmptyReceiptProofData,
        } as EmptyReceiptProof;
        break;
      }
      case "Sig": {
        proof = {
          type: ReceiptProofType.Sig,
          data: {
            receipt: new Receipt(new TokenAmount(BigInt(amount)), new TokenPubkey(Buffer.from(tokenPubkey, "hex"))),
            innerKey,
          } as SigReceiptProofData,
        } as SigReceiptProof;
        break;
      }
      default: {
        throw "Unsupported receipt proof type";
      }
    }

    let status = new BitcoinUtxoStatus(true);

    return new Lrc20Utxo(txid, BigInt(vout), value, status, proof, innerKey);
  }

  public static fromLrc20Transaction(tx: Lrc20Transaction, innerKey?: string): [Array<Lrc20Utxo>, Array<Lrc20Utxo>] {
    let data = undefined;
    switch (tx.tx_type.type) {
      case Lrc20TransactionTypeEnum.Issue:
        data = tx.tx_type.data as IssueData;
        break;
      case Lrc20TransactionTypeEnum.Transfer:
        data = tx.tx_type.data as TransferData;
        break;
    }

    let lrc20Utxos = Array<Lrc20Utxo>();
    let emptyUtxos = Array<Lrc20Utxo>();
    if (data) {
      data.output_proofs.forEach((value, key) => {
        let txid = tx.bitcoin_tx.getId();
        let vout = BigInt(key);
        let satoshis = tx.bitcoin_tx.outs[key].value;
        let status = new BitcoinUtxoStatus(true);

        let receiptData = getReceiptDataFromProof(value);
        if (receiptData && innerKey && receiptData.innerKey != innerKey) {
          return;
        }

        if (!receiptData?.receipt || receiptData.receipt.tokenAmount.amount == 0n) {
          // FIXME: do in a more pretty way
          // if(value.type === ReceiptProofType.EmptyReceipt) {
          //     value = {
          //         type: ReceiptProofType.Sig,
          //         data: {
          //             receipt: receiptData.receipt,
          //             innerKey: innerKey
          //         }
          //     }
          // }

          emptyUtxos.push(new Lrc20Utxo(txid, vout, satoshis, status, value, innerKey));
          return;
        }

        lrc20Utxos.push(new Lrc20Utxo(txid, vout, satoshis, status, value, receiptData.innerKey, receiptData.script));
      });
    }

    return [lrc20Utxos, emptyUtxos];
  }
}

export class BitcoinUtxoStatus {
  constructor(public confirmed: boolean = true) {}

  public static fromBitcoinUtxoStatusDto(status: BitcoinUtxoStatusDto): BitcoinUtxoStatus {
    return new BitcoinUtxoStatus(status.confirmed);
  }
}

export interface BitcoinTxOut {
  bestblock: string;
  confirmations: string;
  value: bigint;
  scriptPubKey: ScriptPubKey;
  coinbase: boolean;
}

export interface ScriptPubKey {
  asm: string;
  desc: string;
  hex: string;
  address: string;
  type: string;
}

export class BitcoinUtxoSpentStatus {
  constructor(
    public spent: boolean,
    public txid: string,
    public vin: bigint,
    public status: BitcoinUtxoStatus,
  ) {}
}
