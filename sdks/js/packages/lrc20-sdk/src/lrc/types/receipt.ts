import * as bitcoin from "bitcoinjs-lib";
import { plainToInstance } from "class-transformer";
import { ECPairInterface } from "ecpair";
import { privateNegate, privateAdd, pointMultiply, pointAdd } from "@bitcoinerlab/secp256k1";
import { TokenPubkey } from "./token-pubkey.ts";
import { TokenAmount } from "./token-amount.ts";
import { EMPTY_TOKEN_PUBKEY, G, PARITY } from "../utils/constants.ts";

export class ReceiptDto {
  constructor(
    public token_amount: TokenAmountDto,
    public token_pubkey: string,
  ) {}

  public static fromReceipt(receipt: Receipt): ReceiptDto {
    return new ReceiptDto(
      TokenAmountDto.fromTokenAmount(receipt.tokenAmount),
      receipt.tokenPubkey.pubkey.toString("hex"),
    );
  }

  public toReceipt(): Receipt {
    let tokenAmount = plainToInstance(TokenAmountDto, this.token_amount);
    return new Receipt(tokenAmount.toTokenAmount(), new TokenPubkey(Buffer.from(this.token_pubkey, "hex")));
  }
}

export class TokenAmountDto {
  constructor(
    public amount: bigint,
    public blinding_factor: number[],
  ) {}

  public static fromTokenAmount(tokenAmount: TokenAmount): TokenAmountDto {
    return new TokenAmountDto(
      tokenAmount.amount,
      tokenAmount.blindingFactor.length == 0
        ? [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
        : Array.from(tokenAmount.blindingFactor),
    );
  }

  public toTokenAmount(): TokenAmount {
    return new TokenAmount(
      this.amount,
      this.blinding_factor
        ? Uint8Array.from(this.blinding_factor)
        : Uint8Array.from([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]),
    );
  }
}

export class Receipt {
  constructor(
    public tokenAmount: TokenAmount,
    public tokenPubkey: TokenPubkey,
  ) {}

  public static emptyReceipt(): Receipt {
    return new Receipt(
      new TokenAmount(0n, Uint8Array.from([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])),
      new TokenPubkey(Buffer.from(Uint8Array.from(Array(32).fill(2)))),
    );
  }

  public isEmptyReceipt(): boolean {
    return this.tokenPubkey.pubkey.every((b) => b === 2);
  }

  public static receiptPrivateKey(keyPair: ECPairInterface, receipt: Receipt): Buffer {
    // hash(hash(Y),UV)
    const pxh = Receipt.receiptHash(receipt);
    let innerKey = keyPair.publicKey!;
    let privateKey = keyPair.privateKey!;

    if (innerKey[0] === 3) {
      innerKey = Buffer.concat([PARITY, innerKey.slice(1)]);
      privateKey = Buffer.from(privateNegate(privateKey));
    }

    // hash(pxh, innerKey)
    const pxhPubkey = bitcoin.crypto.sha256(Buffer.concat([pxh, innerKey]));

    const receiptProof = privateAdd(privateKey, pxhPubkey)!;
    return Buffer.from(receiptProof);
  }

  public static receiptHash(receipt: Receipt): Buffer {
    const y = receipt.tokenAmount;
    const uv = receipt.tokenPubkey;

    // hash(Y)
    const yHash = bitcoin.crypto.sha256(Buffer.from(y.toBytes()));
    // Ensure uv.inner is defined
    const uvInner = uv.pubkey || EMPTY_TOKEN_PUBKEY;
    // hash(hash(Y),UV)
    const pxh = bitcoin.crypto.sha256(Buffer.concat([yHash, Buffer.from(uvInner)]));
    return pxh;
  }

  public static receiptPublicKey(innerKey: Buffer, receipt: Receipt): Buffer {
    // hash(hash(Y),UV)
    const pxh = Receipt.receiptHash(receipt);

    // hash(pxh, innerKey)
    const pxhPubkey = bitcoin.crypto.sha256(Buffer.concat([pxh, innerKey]));

    // hash(pxh, innerKey) * G
    const pxhPubkeyPoint = pointMultiply(G, pxhPubkey)!;

    // hash(pxh, innerKey) * G + innerKey
    const receiptKey = pointAdd(pxhPubkeyPoint, innerKey)!;

    return Buffer.from(receiptKey);
  }
}
