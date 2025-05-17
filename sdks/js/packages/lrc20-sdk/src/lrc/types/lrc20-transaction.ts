import { address, type Network, type Transaction } from "bitcoinjs-lib";
import { instanceToPlain, plainToInstance } from "class-transformer";
import { BitcoinTransactionDto } from "./bitcoin-transaction.ts";
import { TokenPubkey } from "./token-pubkey.ts";
import { ReceiptProof, ReceiptProofDto, ReceiptProofType } from "./receipt-proof.ts";
// import { Buffer } from "buffer";
import { TokenAmount } from "./token-amount.ts";
type Browser = any;

export class Lrc20Transaction {
  constructor(
    public bitcoin_tx: Transaction,
    public tx_type: Lrc20TransactionType,
  ) {}

  public static fromLrc20TransactionDto(dto: Lrc20TransactionDto): Lrc20Transaction {
    let bitcoinTx = BitcoinTransactionDto.toTransaction(dto.bitcoin_tx);
    let lrc20TxType = Lrc20TransactionTypeDto.toLrc20TransactionType(dto.tx_type);

    return new Lrc20Transaction(bitcoinTx, lrc20TxType);
  }
}

export type Lrc20TransactionType =
  | {
      type: Lrc20TransactionTypeEnum.Issue;
      data: IssueData;
    }
  | {
      type: Lrc20TransactionTypeEnum.Transfer;
      data: TransferData;
    }
  | {
      type: Lrc20TransactionTypeEnum.SparkExit;
      data: SparkExitData;
    }
  | {
      type: Lrc20TransactionTypeEnum.Announcement;
      data: AnnouncementData;
    };

export type Lrc20TransactionTypeData = IssueData | TransferData | AnnouncementData | SparkExitData;

export enum Lrc20TransactionTypeEnum {
  Announcement = "Announcement",
  Issue = "Issue",
  Transfer = "Transfer",
  SparkExit = "SparkExit",
}

export enum AnnouncementDataType {
  TokenPubkey = "TokenPubkey",
  Issue = "Issue",
  TxFreeze = "TxFreeze",
  PubkeyFreeze = "PubkeyFreeze",
  TransferOwnership = "TransferOwnership",
}

export interface Lrc20TransactionStatusDto {
  status: Lrc20TransactionStatus;
  data: Lrc20TransactionDto;
}

export type AnnouncementData =
  | TokenPubkeyAnnouncement
  | IssueAnnouncement
  | TxFreezeAnnouncement
  | PubkeyFreezeAnnouncement
  | TransferOwnershipAnnouncement;

export class TokenPubkeyAnnouncement {
  constructor(
    public tokenPubkey: TokenPubkey,
    public name: string,
    public symbol: string,
    public decimal: number,
    public maxSupply: bigint,
    public isFreezable: boolean,
  ) {
    const MAX_NAME_SIZE = 17;
    const MIN_NAME_SIZE = 3;
    const MAX_SYMBOL_SIZE = 6;
    const MIN_SYMBOL_SIZE = 3;

    const nameBytes = Buffer.from(name, "utf-8").length;
    if (nameBytes < MIN_NAME_SIZE || nameBytes > MAX_NAME_SIZE) {
      throw new Error(
        `Byte length of token name: ${name} is out of range. ${nameBytes}, must be between ${MIN_NAME_SIZE} and ${MAX_NAME_SIZE}`,
      );
    }

    const symbolBytes = Buffer.from(symbol, "utf-8").length;
    if (symbolBytes < MIN_SYMBOL_SIZE || symbolBytes > MAX_SYMBOL_SIZE) {
      throw new Error(
        `Byte length of token ticker: ${symbol} is out of range. ${symbolBytes}, must be between ${MIN_SYMBOL_SIZE} and ${MAX_SYMBOL_SIZE}`,
      );
    }
    this.tokenPubkey = tokenPubkey;
    this.name = name;
    this.symbol = symbol;
    this.decimal = decimal;
    this.maxSupply = maxSupply;
    this.isFreezable = isFreezable;
  }

  public static fromTokenPubkeyAnnouncementDto(announcement: TokenPubkeyAnnouncementDto): TokenPubkeyAnnouncement {
    let { token_pubkey, name, symbol, decimal, max_supply, is_freezable } = announcement;
    return new TokenPubkeyAnnouncement(
      new TokenPubkey(Buffer.from(token_pubkey, "hex")),
      name,
      symbol,
      decimal,
      max_supply,
      is_freezable,
    );
  }

  public toBuffer(): Buffer {
    const decimalBytes: Buffer = Buffer.alloc(1, this.decimal);

    const isFreezableBytes: Buffer = Buffer.alloc(1, this.isFreezable ? 1 : 0);
    const maxSupplyBytes = Buffer.alloc(16);
    let value = this.maxSupply;

    for (let i = 15; i >= 0; i--) {
      maxSupplyBytes[i] = Number(value & BigInt(0xff));
      value = value >> BigInt(8);
    }

    const verifyValue = BigInt("0x" + maxSupplyBytes.toString("hex"));
    if (verifyValue !== this.maxSupply) {
      console.error("Value mismatch:", {
        original: this.maxSupply.toString(),
        encoded: verifyValue.toString(),
        buffer: maxSupplyBytes.toString("hex"),
      });

      throw new Error(`MaxSupply value corruption: ${this.maxSupply} became ${verifyValue}`);
    }

    const nameBytes = Buffer.from(this.name, "utf-8");
    const symbolBytes = Buffer.from(this.symbol, "utf-8");

    return Buffer.concat([
      this.tokenPubkey.inner,
      Buffer.from([nameBytes.length]),
      nameBytes,
      Buffer.from([symbolBytes.length]),
      symbolBytes,
      decimalBytes,
      maxSupplyBytes,
      isFreezableBytes,
    ]);
  }
}

export class TransferOwnershipAnnouncement {
  constructor(
    public tokenPubkey: TokenPubkey,
    public new_owner: Buffer,
  ) {}

  public toBuffer(): Buffer {
    return Buffer.concat([this.tokenPubkey.inner, this.new_owner]);
  }

  getAddress(network: Network): string {
    return address.toBech32(this.new_owner, this.new_owner.length === 32 ? 1 : 0, network.bech32);
  }
}

export class IssueAnnouncement {
  constructor(
    public tokenPubkey: TokenPubkey,
    public amount: bigint,
  ) {}

  public toReceiptAnnouncementData() {
    return { tokenPubkey: this.tokenPubkey.pubkey.toString("hex"), amount: this.amount };
  }
}

export class TxFreezeAnnouncement {
  constructor(
    public tokenPubkey: TokenPubkey,
    public outpoint: FreezeTxToggle,
  ) {}
}

export class PubkeyFreezeAnnouncement {
  constructor(
    public tokenPubkey: TokenPubkey,
    public ownerPubkey: Buffer,
  ) {}
}

export interface IssueData {
  announcement: { tokenPubkey: string; amount: bigint };
  input_proofs: Map<number, ReceiptProof>;
  output_proofs: Map<number, ReceiptProof>;
}

export interface TransferData {
  input_proofs: Map<number, ReceiptProof>;
  output_proofs: Map<number, ReceiptProof>;
}

export interface SparkExitData {
  output_proofs: Map<number, ReceiptProof>;
}

export interface FreezeTxToggle {
  txid: string;
  vout: number;
}

export enum Lrc20TransactionStatus {
  None = "none",
  Pending = "pending",
  Checked = "checked",
  Attached = "attached",
}

export class Lrc20TransactionDto {
  constructor(
    public bitcoin_tx: BitcoinTransactionDto,
    public tx_type: Lrc20TransactionTypeDto,
  ) {}

  public static fromLrc20Transaction(tx: Lrc20Transaction): Lrc20TransactionDto {
    return new Lrc20TransactionDto(
      BitcoinTransactionDto.fromBitcoinTransaction(tx.bitcoin_tx),
      Lrc20TransactionTypeDto.fromLrc20TransactionType(tx.tx_type),
    );
  }

  public toLrc20Transaction(): Lrc20Transaction {
    let txType = plainToInstance(Lrc20TransactionTypeDto, this.tx_type);
    let bitcoinTx = plainToInstance(BitcoinTransactionDto, this.bitcoin_tx);
    return {
      tx_type: Lrc20TransactionTypeDto.toLrc20TransactionType(txType),
      bitcoin_tx: BitcoinTransactionDto.toTransaction(bitcoinTx),
    } as Lrc20Transaction;
  }
}

export class Lrc20TransactionTypeDto {
  constructor(
    public type: Lrc20TransactionTypeEnum,
    public data: Lrc20TransactionTypeDataDto,
  ) {}

  public static fromLrc20TransactionType(txType: Lrc20TransactionType): Lrc20TransactionTypeDto {
    let data: Lrc20TransactionTypeDataDto;

    switch (txType.type) {
      case Lrc20TransactionTypeEnum.Issue: {
        let txData = txType.data as IssueData;
        let outputProofs = new Map<number, ReceiptProofDto>();
        for (let [key, value] of txData.output_proofs.entries()) {
          const receiptProof = ReceiptProofDto.fromReceiptProof(value);
          outputProofs = outputProofs.set(key, receiptProof);
        }

        let announcement = {
          token_pubkey: txData.announcement.tokenPubkey,
          amount: txData.announcement.amount,
        } as IssueAnnouncementDto;

        data = {
          output_proofs: outputProofs,
          announcement: announcement,
        };
        break;
      }
      case Lrc20TransactionTypeEnum.Transfer: {
        let txData = txType.data as TransferData;

        let inputProofs = new Map<number, ReceiptProofDto>();
        for (let [key, value] of txData.input_proofs.entries()) {
          let receiptProof = ReceiptProofDto.fromReceiptProof(value);
          inputProofs = inputProofs.set(key, receiptProof);
        }

        let outputProofs = new Map<number, ReceiptProofDto>();
        for (let [key, value] of txData.output_proofs.entries()) {
          let receiptProof = ReceiptProofDto.fromReceiptProof(value);
          outputProofs = outputProofs.set(key, receiptProof);
        }

        data = {
          input_proofs: inputProofs,
          output_proofs: outputProofs,
        };
        break;
      }
      case Lrc20TransactionTypeEnum.SparkExit: {
        let txData = txType.data as SparkExitData;

        let outputProofs = new Map<number, ReceiptProofDto>();
        for (let [key, value] of txData.output_proofs.entries()) {
          let receiptProof = ReceiptProofDto.fromReceiptProof(value);
          outputProofs = outputProofs.set(key, receiptProof);
        }

        data = {
          output_proofs: outputProofs,
        };
        break;
      }
      case Lrc20TransactionTypeEnum.Announcement: {
        let txData = txType.data as AnnouncementData;

        if (txData instanceof TokenPubkeyAnnouncement) {
          let { name, symbol, decimal } = txData;
          data = {
            [AnnouncementDataType.TokenPubkey]: {
              token_pubkey: txData.tokenPubkey.pubkey.toString("hex"),
              name,
              symbol,
              decimal,
              max_supply: txData.maxSupply,
              is_freezable: txData.isFreezable,
            },
          } as AnnouncementDataDto;
        } else if (txData instanceof IssueAnnouncement) {
          data = {
            [AnnouncementDataType.Issue]: {
              token_pubkey: txData.tokenPubkey.pubkey.toString("hex"),
            },
          } as AnnouncementDataDto;
        } else if (txData instanceof TxFreezeAnnouncement) {
          const tokenPubkey = txData.tokenPubkey;
          const { txid, vout } = txData.outpoint;
          data = {
            [AnnouncementDataType.TxFreeze]: {
              token_pubkey: tokenPubkey.inner.toString("hex"),
              outpoint: `${txid}:${vout}`,
            },
          } as AnnouncementDataDto;
        } else if (txData instanceof PubkeyFreezeAnnouncement) {
          const tokenPubkey = txData.tokenPubkey;
          data = {
            [AnnouncementDataType.PubkeyFreeze]: {
              token_pubkey: tokenPubkey.inner.toString("hex"),
              pubkey: txData.ownerPubkey.toString("hex"),
            },
          } as AnnouncementDataDto;
        } else if (txData instanceof TransferOwnershipAnnouncement) {
          const { tokenPubkey, new_owner } = txData;
          data = {
            [AnnouncementDataType.TransferOwnership]: {
              token_pubkey: tokenPubkey.inner.toString("hex"),
              new_owner: new_owner.toString("hex"),
            },
          } as AnnouncementDataDto;
        }

        break;
      }
    }

    return new Lrc20TransactionTypeDto(txType.type, data!);
  }

  public static toLrc20TransactionType(dto: Lrc20TransactionTypeDto): Lrc20TransactionType {
    let data: Lrc20TransactionTypeData;

    switch (dto.type) {
      case Lrc20TransactionTypeEnum.Issue: {
        let txData = dto.data as IssueDataDto;
        let outputProofs = new Map<number, ReceiptProof>();
        if (txData.output_proofs instanceof Map) {
          txData.output_proofs.forEach((proof, index) => {
            outputProofs = outputProofs.set(index, ReceiptProofDto.toReceiptProof(proof));
          });
        } else {
          for (let key in txData.output_proofs) {
            let receiptProof = txData.output_proofs[key] as ReceiptProofDto;
            receiptProof = plainToInstance(ReceiptProofDto, receiptProof);
            outputProofs = outputProofs.set(+key, ReceiptProofDto.toReceiptProof(receiptProof));
          }
        }

        let announcement = txData.announcement as IssueAnnouncementDto;
        data = {
          announcement: {
            tokenPubkey: announcement.token_pubkey,
            amount: announcement.amount,
          },
          output_proofs: outputProofs,
        } as IssueData;
        break;
      }
      case Lrc20TransactionTypeEnum.Transfer: {
        let txData = dto.data as TransferDataDto;

        let inputProofs = new Map<number, ReceiptProof>();
        if (txData.input_proofs instanceof Map) {
          txData.input_proofs.forEach((proof, index) => {
            inputProofs = inputProofs.set(index, ReceiptProofDto.toReceiptProof(proof));
          });
        } else {
          for (let key in txData.input_proofs) {
            let receiptProof = txData.input_proofs[key] as ReceiptProofDto;
            receiptProof = plainToInstance(ReceiptProofDto, receiptProof);
            inputProofs = inputProofs.set(+key, ReceiptProofDto.toReceiptProof(receiptProof));
          }
        }

        let outputProofs = new Map<number, ReceiptProof>();
        if (txData.output_proofs instanceof Map) {
          txData.output_proofs.forEach((proof, index) => {
            outputProofs = outputProofs.set(index, ReceiptProofDto.toReceiptProof(proof));
          });
        } else {
          for (let key in txData.output_proofs) {
            let receiptProof = txData.output_proofs[key] as ReceiptProofDto;
            receiptProof = plainToInstance(ReceiptProofDto, receiptProof);
            outputProofs = outputProofs.set(+key, ReceiptProofDto.toReceiptProof(receiptProof));
          }
        }

        data = {
          input_proofs: inputProofs,
          output_proofs: outputProofs,
        } as TransferData;

        break;
      }
      case Lrc20TransactionTypeEnum.SparkExit: {
        let txData = dto.data as SparkExitDataDto;

        let outputProofs = new Map<number, ReceiptProof>();
        if (txData.output_proofs instanceof Map) {
          txData.output_proofs.forEach((proof, index) => {
            outputProofs = outputProofs.set(index, ReceiptProofDto.toReceiptProof(proof));
          });
        } else {
          for (let key in txData.output_proofs) {
            let receiptProof = txData.output_proofs[key] as ReceiptProofDto;
            receiptProof = plainToInstance(ReceiptProofDto, receiptProof);
            outputProofs = outputProofs.set(+key, ReceiptProofDto.toReceiptProof(receiptProof));
          }
        }

        data = {
          output_proofs: outputProofs,
        } as SparkExitData;

        break;
      }
      case Lrc20TransactionTypeEnum.Announcement: {
        let txData = dto.data as any;

        data = parseAnnouncementData(txData);
        break;
      }
    }
    return {
      type: dto.type,
      data: data!,
    } as Lrc20TransactionType;
  }
}

export type Lrc20TransactionTypeDataDto = IssueDataDto | TransferDataDto | AnnouncementDataDto | SparkExitDataDto;

export interface IssueDataDto {
  output_proofs: any;
  announcement: object;
}

export interface TransferDataDto {
  input_proofs: any;
  output_proofs: any;
}

export interface SparkExitDataDto {
  output_proofs: any;
}

export type AnnouncementDataDto =
  | {
      [AnnouncementDataType.TokenPubkey]: TokenPubkeyAnnouncementDto;
    }
  | {
      [AnnouncementDataType.Issue]: IssueAnnouncementDto;
    }
  | {
      [AnnouncementDataType.TxFreeze]: FreezeAnnouncementDto;
    }
  | {
      [AnnouncementDataType.PubkeyFreeze]: PubkeyFreezeAnnouncementDto;
    }
  | {
      [AnnouncementDataType.TransferOwnership]: TransferOwnershipAnnouncementDto;
    };

export interface TokenPubkeyAnnouncementDto {
  token_pubkey: string;
  name: string;
  symbol: string;
  decimal: number;
  max_supply: bigint;
  is_freezable: boolean;
}

export interface IssueAnnouncementDto {
  token_pubkey: string;
  amount: bigint;
}

export interface FreezeAnnouncementDto {
  token_pubkey: string;
  outpoint: string;
}

export interface PubkeyFreezeAnnouncementDto {
  token_pubkey: string;
  pubkey: string;
}

export interface TransferOwnershipAnnouncementDto {
  token_pubkey: string;
  new_owner: string;
}

export class Lrc20TransactionParser {
  static stringify(tx: Lrc20Transaction): string {
    return JSON.stringify(instanceToPlain(tx), (key, value) => (typeof value === "bigint" ? Number(value) : value));
  }

  static fromString(stringTx: string): Lrc20Transaction {
    let lrc20Tx = JSON.parse(stringTx) as Lrc20Transaction;

    lrc20Tx.bitcoin_tx.ins.forEach((input) => {
      input.hash = Buffer.from(input.hash);
      input.script = Buffer.from(input.script);

      const witnessCasted: Buffer[] = [];
      input.witness.map((witness) => {
        witnessCasted.push(Buffer.from(witness));
      });
      input.witness = witnessCasted;
    });

    lrc20Tx.bitcoin_tx.outs.forEach((output) => {
      output.script = Buffer.from(output.script);
    });

    switch (lrc20Tx.tx_type.type) {
      case Lrc20TransactionTypeEnum.Transfer:
        const input_proofs: Map<number, ReceiptProof> = new Map(
          Object.entries(lrc20Tx.tx_type.data.input_proofs).map(([key, value]) => [
            Number(key),
            this.castReceiptProof(value as ReceiptProof),
          ]),
        );
        const output_proofs: Map<number, ReceiptProof> = new Map(
          Object.entries(lrc20Tx.tx_type.data.output_proofs).map(([key, value]) => [
            Number(key),
            this.castReceiptProof(value as ReceiptProof),
          ]),
        );

        lrc20Tx.tx_type = {
          type: Lrc20TransactionTypeEnum.Transfer,
          data: {
            input_proofs: input_proofs,
            output_proofs: output_proofs,
          },
        };
        break;
    }

    return lrc20Tx;
  }

  static toHex(tx: Lrc20Transaction): string {
    return Buffer.from(this.stringify(tx)).toString("hex");
  }

  static fromHex(hex: string): Lrc20Transaction {
    return this.fromString(Buffer.from(hex, "hex").toString());
  }

  private static castReceiptProof(receiptProof: ReceiptProof): ReceiptProof {
    switch (receiptProof.type) {
      case ReceiptProofType.Sig:
        receiptProof.data.receipt.tokenPubkey = new TokenPubkey(
          Buffer.from(receiptProof.data.receipt.tokenPubkey.pubkey),
        );
        const tokenAmount = new TokenAmount(
          receiptProof.data.receipt.tokenAmount.amount,
          receiptProof.data.receipt.tokenAmount.blindingFactor,
        );
        receiptProof.data.receipt.tokenAmount = tokenAmount;

        break;
    }

    return receiptProof;
  }
}

export function parseAnnouncementData(txData: any): AnnouncementData {
  if (txData[AnnouncementDataType.TokenPubkey]) {
    let announcement = txData[AnnouncementDataType.TokenPubkey] as TokenPubkeyAnnouncementDto;
    let { token_pubkey, name, symbol, decimal, max_supply, is_freezable } = announcement;
    return new TokenPubkeyAnnouncement(
      new TokenPubkey(Buffer.from(token_pubkey, "hex")),
      name,
      symbol,
      decimal,
      max_supply,
      is_freezable,
    ) as AnnouncementData;
  } else if (txData[AnnouncementDataType.Issue]) {
    let announcement = txData[AnnouncementDataType.Issue] as IssueAnnouncementDto;
    let { token_pubkey, amount } = announcement;

    return new IssueAnnouncement(new TokenPubkey(Buffer.from(token_pubkey, "hex")), amount) as AnnouncementData;
  } else if (txData[AnnouncementDataType.TxFreeze]) {
    let announcementDto = txData[AnnouncementDataType.TxFreeze] as FreezeAnnouncementDto;
    let [txid, vout] = announcementDto.outpoint.split(":");
    let announcement = new TxFreezeAnnouncement(new TokenPubkey(Buffer.from(announcementDto.token_pubkey, "hex")), {
      txid,
      vout: +vout,
    });
    return announcement as AnnouncementData;
  } else if (txData[AnnouncementDataType.PubkeyFreeze]) {
    let announcementDto = txData[AnnouncementDataType.PubkeyFreeze] as PubkeyFreezeAnnouncementDto;
    let announcement = new PubkeyFreezeAnnouncement(
      new TokenPubkey(Buffer.from(announcementDto.token_pubkey, "hex")),
      Buffer.from(announcementDto.pubkey, "hex"),
    );
    return announcement as AnnouncementData;
  } else if (txData[AnnouncementDataType.TransferOwnership]) {
    let announcement = txData[AnnouncementDataType.TransferOwnership] as TransferOwnershipAnnouncementDto;
    let { token_pubkey, new_owner } = announcement;
    return new TransferOwnershipAnnouncement(
      new TokenPubkey(Buffer.from(token_pubkey, "hex")),
      Buffer.from(new_owner, "hex"),
    ) as AnnouncementData;
  }

  return txData;
}

export class TokenPubkeyInfo {
  constructor(
    public announcement: TokenPubkeyAnnouncement | null,
    public totalSupply: bigint,
  ) {}

  public static fromTokenPubkeyInfoDto(info: TokenPubkeyInfoDto): TokenPubkeyInfo {
    const announcement = info.announcement
      ? TokenPubkeyAnnouncement.fromTokenPubkeyAnnouncementDto(info.announcement)
      : null;
    return new TokenPubkeyInfo(announcement, info.total_supply);
  }
}

export interface TokenPubkeyInfoDto {
  announcement: TokenPubkeyAnnouncementDto | null;
  total_supply: bigint;
}
