import { plainToInstance } from "class-transformer";
import { TokenAmount } from "./token-amount.ts";
import { Receipt, ReceiptDto } from "./receipt.ts";
import { SparkExitMetadata } from "./spark.ts";

export type ReceiptProof =
  | EmptyReceiptProof
  | SigReceiptProof
  | MultisigReceiptProof
  | LightningCommitmentProof
  | LightningHtlc
  | P2WSH
  | SparkExit;

export enum ReceiptProofType {
  EmptyReceipt = "EmptyReceipt",
  Sig = "Sig",
  Multisig = "Multisig",
  Lightning = "Lightning",
  LightningHtlc = "LightningHtlc",
  P2WSH = "P2WSH",
  SparkExit = "SparkExit",
}

export interface EmptyReceiptProof {
  type: ReceiptProofType.EmptyReceipt;
  data: EmptyReceiptProofData;
}

export interface SigReceiptProof {
  type: ReceiptProofType.Sig;
  data: SigReceiptProofData;
}

export interface MultisigReceiptProof {
  type: ReceiptProofType.Multisig;
  data: MultisigReceiptProofData;
}

export interface LightningCommitmentProof {
  type: ReceiptProofType.Lightning;
  data: LightningCommitmentProofData;
}

export interface LightningHtlc {
  type: ReceiptProofType.LightningHtlc;
  data: LightningHtlcProofData;
}

export interface P2WSH {
  type: ReceiptProofType.P2WSH;
  data: P2WSHProofData;
}

export interface SparkExit {
  type: ReceiptProofType.SparkExit;
  data: SparkExitProofData;
}

export type ReceiptProofData =
  | EmptyReceiptProofData
  | SigReceiptProofData
  | MultisigReceiptProofData
  | LightningCommitmentProofData
  | LightningHtlcProofData
  | P2WSHProofData
  | SparkExitProofData;

export interface EmptyReceiptProofData {
  receipt: Receipt;
  innerKey: string;
}

export interface SigReceiptProofData {
  receipt: Receipt;
  innerKey: string;
}

export interface MultisigReceiptProofData {
  receipt: Receipt;
  innerKeys: Array<string>;
  m: number;
}

export interface LightningCommitmentProofData {
  receipt: Receipt;
  revocationPubkey: string;
  toSelfDelay: number;
  localDelayedPubkey: string;
}

export interface LightningHtlcProofData {
  receipt: Receipt;
  data: LightningHtlcData;
}

export interface LightningHtlcData {
  revocationKeyHash: string;
  remoteHtlcKey: string;
  localHtlcKey: string;
  paymentHash: string;
  kind: HtlcScriptKind;
}

export interface P2WSHProofData {
  receipt: Receipt;
  innerKey: string;
  script: string;
}

export interface SparkExitProofData {
  receipt: Receipt;
  script: SparkExitProofDataScript;
  metadata?: SparkExitMetadata;
}

export interface SparkExitProofDataScript {
  revocation_key: string;
  delay_key: string;
  locktime: number;
}

export type HtlcScriptKind = "offered" | ReceivedHtlc;

export interface ReceivedHtlc {
  cltv_expiry: number;
}

export function getReceiptDataFromProof(receiptProof: ReceiptProof) {
  switch (receiptProof.type) {
    case ReceiptProofType.Sig:
      const sigTokenAmountAmount =
        typeof receiptProof.data.receipt.tokenAmount.amount === "string"
          ? // @ts-ignore
            BigInt(receiptProof.data.receipt.tokenAmount.amount.replace("n", ""))
          : receiptProof.data.receipt.tokenAmount.amount;
      const sigReceipt = new Receipt(
        new TokenAmount(
          sigTokenAmountAmount,
          receiptProof.data.receipt.tokenAmount.blindingFactor.length === 0
            ? Uint8Array.from([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])
            : receiptProof.data.receipt.tokenAmount.blindingFactor,
        ),
        receiptProof.data.receipt.tokenPubkey,
      );
      return { receipt: sigReceipt, innerKey: receiptProof.data.innerKey };
    case ReceiptProofType.P2WSH:
      const p2wshTokenAmountAmount =
        typeof receiptProof.data.receipt.tokenAmount.amount === "string"
          ? // @ts-ignore
            BigInt(receiptProof.data.receipt.tokenAmount.amount.replace("n", ""))
          : receiptProof.data.receipt.tokenAmount.amount;
      const p2wshReceipt = new Receipt(
        new TokenAmount(
          p2wshTokenAmountAmount,
          receiptProof.data.receipt.tokenAmount.blindingFactor.length === 0
            ? Uint8Array.from([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])
            : receiptProof.data.receipt.tokenAmount.blindingFactor,
        ),
        receiptProof.data.receipt.tokenPubkey,
      );
      return { receipt: p2wshReceipt, innerKey: receiptProof.data.innerKey, script: receiptProof.data.script };
    case ReceiptProofType.Multisig:
      const multisigTokenAmountAmount =
        typeof receiptProof.data.receipt.tokenAmount.amount === "string"
          ? // @ts-ignore
            BigInt(receiptProof.data.receipt.tokenAmount.amount.replace("n", ""))
          : receiptProof.data.receipt.tokenAmount.amount;
      const m =
        typeof receiptProof.data.m === "string"
          ? // @ts-ignore
            BigInt(receiptProof.data.m.replace("n", ""))
          : receiptProof.data.m;
      const multisigReceipt = new Receipt(
        new TokenAmount(
          multisigTokenAmountAmount,
          receiptProof.data.receipt.tokenAmount.blindingFactor.length === 0
            ? Uint8Array.from([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])
            : receiptProof.data.receipt.tokenAmount.blindingFactor,
        ),
        receiptProof.data.receipt.tokenPubkey,
      );
      return { receipt: multisigReceipt, innerKeys: receiptProof.data.innerKeys, m: m as number };
    case ReceiptProofType.EmptyReceipt:
      return { receipt: Receipt.emptyReceipt(), innerKey: receiptProof.data.innerKey };
    default:
      throw new Error("Receipt data is not supported");
  }
}

export class ReceiptProofDto {
  constructor(
    public type: ReceiptProofType,
    public data: ReceiptProofDataDto,
  ) {}

  public static fromReceiptProof(proof: ReceiptProof): ReceiptProofDto {
    let data: ReceiptProofDataDto;
    switch (proof.type) {
      case ReceiptProofType.EmptyReceipt:
        data = EmptyReceiptProofDataDto.fromReceiptProofData(proof.data);
        break;
      case ReceiptProofType.Sig:
        data = SigReceiptProofDataDto.fromReceiptProofData(proof.data);
        break;
      case ReceiptProofType.Multisig:
        data = MultisigReceiptProofDataDto.fromReceiptProofData(proof.data);
        break;
      case ReceiptProofType.Lightning:
        data = LightningCommitmentProofDataDto.fromReceiptProofData(proof.data);
        break;
      case ReceiptProofType.LightningHtlc:
        data = LightningHtlcProofDataDto.fromReceiptProofData(proof.data);
        break;
      case ReceiptProofType.P2WSH:
        data = P2WSHProofDataDto.fromReceiptProofData(proof.data);
        break;
      case ReceiptProofType.SparkExit:
        data = SparkExitProofDataDto.fromReceiptProofData(proof.data);
        break;
    }

    return new ReceiptProofDto(proof.type, data);
  }

  public static toReceiptProof(dto: ReceiptProofDto): ReceiptProof {
    let data: ReceiptProofDataDto;
    switch (dto.type) {
      case ReceiptProofType.EmptyReceipt:
        data = plainToInstance(EmptyReceiptProofDataDto, dto.data);
        break;
      case ReceiptProofType.Sig:
        data = plainToInstance(SigReceiptProofDataDto, dto.data);
        break;
      case ReceiptProofType.Multisig:
        data = plainToInstance(MultisigReceiptProofDataDto, dto.data);
        break;
      case ReceiptProofType.Lightning:
        data = plainToInstance(LightningCommitmentProofDataDto, dto.data);
        break;
      case ReceiptProofType.LightningHtlc:
        data = plainToInstance(LightningHtlcProofDataDto, dto.data);
        break;
      case ReceiptProofType.P2WSH:
        data = plainToInstance(P2WSHProofDataDto, dto.data);
        break;
      case ReceiptProofType.SparkExit:
        data = plainToInstance(SparkExitProofDataDto, dto.data);
        break;
    }
    return {
      type: dto.type,
      data: data.toReceiptProofData(),
    } as ReceiptProof;
  }
}

export type ReceiptProofDataDto =
  | EmptyReceiptProofDataDto
  | SigReceiptProofDataDto
  | MultisigReceiptProofDataDto
  | LightningCommitmentProofDataDto
  | LightningHtlcProofDataDto
  | P2WSHProofDataDto
  | SparkExitProofDataDto;

export class EmptyReceiptProofDataDto {
  constructor(public inner_key: string) {}

  public static fromReceiptProofData(data: ReceiptProofData): ReceiptProofDataDto {
    let proofData = data as EmptyReceiptProofData;
    return new EmptyReceiptProofDataDto(proofData.innerKey);
  }

  public toReceiptProofData(): ReceiptProofData {
    return {
      innerKey: this.inner_key,
    } as EmptyReceiptProofData;
  }
}

export class SigReceiptProofDataDto {
  constructor(
    public receipt: ReceiptDto,
    public inner_key: string,
  ) {}

  public static fromReceiptProofData(data: ReceiptProofData): ReceiptProofDataDto {
    let proofData = data as SigReceiptProofData;
    let receipt = ReceiptDto.fromReceipt(proofData.receipt);

    return new SigReceiptProofDataDto(receipt, proofData.innerKey);
  }

  public toReceiptProofData(): ReceiptProofData {
    let receipt = plainToInstance(ReceiptDto, this.receipt);
    return {
      receipt: receipt.toReceipt(),
      innerKey: this.inner_key,
    } as SigReceiptProofData;
  }
}

export class MultisigReceiptProofDataDto {
  constructor(
    public receipt: ReceiptDto,
    public inner_keys: Array<string>,
    public m: number,
  ) {}

  public static fromReceiptProofData(data: ReceiptProofData): ReceiptProofDataDto {
    let proofData = data as MultisigReceiptProofData;
    let receipt = ReceiptDto.fromReceipt(proofData.receipt);

    return new MultisigReceiptProofDataDto(receipt, proofData.innerKeys, proofData.m);
  }

  public toReceiptProofData(): ReceiptProofData {
    let receipt = plainToInstance(ReceiptDto, this.receipt);
    return {
      receipt: receipt.toReceipt(),
      innerKeys: this.inner_keys,
      m: this.m,
    } as MultisigReceiptProofData;
  }
}

export class LightningCommitmentProofDataDto {
  constructor(
    public receipt: ReceiptDto,
    public revocationPubkey: string,
    public toSelfDelay: number,
    public localDelayedPubkey: string,
  ) {}

  public static fromReceiptProofData(data: ReceiptProofData): ReceiptProofDataDto {
    let proofData = data as LightningCommitmentProofData;
    let receipt = ReceiptDto.fromReceipt(proofData.receipt);

    return new LightningCommitmentProofDataDto(
      receipt,
      proofData.revocationPubkey,
      proofData.toSelfDelay,
      proofData.localDelayedPubkey,
    );
  }

  public toReceiptProofData(): ReceiptProofData {
    let receipt = plainToInstance(ReceiptDto, this.receipt);
    return {
      receipt: receipt.toReceipt(),
      revocationPubkey: this.revocationPubkey,
      toSelfDelay: this.toSelfDelay,
      localDelayedPubkey: this.localDelayedPubkey,
    } as LightningCommitmentProofData;
  }
}

export class LightningHtlcProofDataDto {
  constructor(
    public receipt: ReceiptDto,
    public data: LightningHtlcData,
  ) {}

  public static fromReceiptProofData(data: ReceiptProofData): ReceiptProofDataDto {
    let proofData = data as LightningHtlcProofData;
    let receipt = ReceiptDto.fromReceipt(proofData.receipt);

    return new LightningHtlcProofDataDto(receipt, proofData.data);
  }

  public toReceiptProofData(): ReceiptProofData {
    let receipt = plainToInstance(ReceiptDto, this.receipt);
    return {
      receipt: receipt.toReceipt(),
      data: this.data,
    } as LightningHtlcProofData;
  }
}

export class P2WSHProofDataDto {
  constructor(
    public receipt: ReceiptDto,
    public inner_key: string,
    public script: string,
  ) {}

  public static fromReceiptProofData(data: ReceiptProofData): ReceiptProofDataDto {
    let proofData = data as P2WSHProofData;
    let receipt = ReceiptDto.fromReceipt(proofData.receipt);

    return new P2WSHProofDataDto(receipt, proofData.innerKey, proofData.script);
  }

  public toReceiptProofData(): ReceiptProofData {
    let receipt = plainToInstance(ReceiptDto, this.receipt);
    return {
      receipt: receipt.toReceipt(),
      innerKey: this.inner_key,
      script: this.script,
    } as P2WSHProofData;
  }
}

export class SparkExitProofDataDto {
  constructor(
    public receipt: ReceiptDto,
    public script: SparkExitProofDataScript,
    public metadata: SparkExitMetadata,
  ) {}

  public static fromReceiptProofData(data: ReceiptProofData): ReceiptProofDataDto {
    let proofData = data as SparkExitProofData;
    let receipt = ReceiptDto.fromReceipt(proofData.receipt);

    return new SparkExitProofDataDto(receipt, proofData.script, proofData.metadata);
  }

  public toReceiptProofData(): ReceiptProofData {
    let receipt = plainToInstance(ReceiptDto, this.receipt);
    return {
      receipt: receipt.toReceipt(),
      script: this.script,
      metadata: this.metadata,
    } as SparkExitProofData;
  }
}
