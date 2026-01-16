import { Buffer } from "buffer";

export enum NetworkType {
  MAINNET,
  TESTNET,
  DEVNET,
  REGTEST,
  LOCAL,
}

const EMPTY_TOKEN_PUBKEY = Buffer.from(Array(33).fill(2));

export class TokenPubkey {
  pubkey: Buffer;

  constructor(pubkey?: Buffer) {
    this.pubkey = pubkey || EMPTY_TOKEN_PUBKEY;
  }

  get inner(): Buffer {
    return this.pubkey;
  }
}
