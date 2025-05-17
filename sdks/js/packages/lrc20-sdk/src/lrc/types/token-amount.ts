import { BLINDING_FACTOR_SIZE, TOKEN_AMOUNT_SIZE } from "../utils/constants.ts";

export class TokenAmount {
  amount: bigint;
  blindingFactor = new Uint8Array([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]);

  constructor(amount: bigint, blindingFactor?: Uint8Array) {
    this.amount = BigInt(amount);
    if (blindingFactor) {
      this.blindingFactor = Buffer.from(blindingFactor);
    }
  }

  toBytes() {
    const buffer = new Uint8Array(BLINDING_FACTOR_SIZE);
    const view = new DataView(buffer.buffer);
    view.setBigUint64(0, this.amount >> 64n, false);
    view.setBigUint64(8, this.amount & 0xffffffffffffffffn, false);
    const extendedBuffer = new Uint8Array(TOKEN_AMOUNT_SIZE);
    extendedBuffer.set(buffer, 0);
    return extendedBuffer;
  }
}
