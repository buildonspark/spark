import { EMPTY_TOKEN_PUBKEY } from "../utils/constants.ts";

export class TokenPubkey {
  pubkey: Buffer;
  constructor(pubkey?: Buffer) {
    this.pubkey = pubkey || EMPTY_TOKEN_PUBKEY;
  }

  get inner() {
    return this.pubkey;
  }
}
