import { Buffer } from "buffer";
import { TokenPubkey } from "./types.js";

const MAX_NAME_SIZE = 17;
const MIN_NAME_SIZE = 3;
const MAX_SYMBOL_SIZE = 6;
const MIN_SYMBOL_SIZE = 3;

export class TokenPubkeyAnnouncement {
  constructor(
    public tokenPubkey: TokenPubkey,
    public name: string,
    public symbol: string,
    public decimal: number,
    public maxSupply: bigint,
    public isFreezable: boolean,
  ) {
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
  }

  public toBuffer(): Buffer {
    const decimalBytes = Buffer.alloc(1, this.decimal);
    const isFreezableBytes = Buffer.alloc(1, this.isFreezable ? 1 : 0);

    const maxSupplyBytes = Buffer.alloc(16);
    let value = this.maxSupply;
    for (let i = 15; i >= 0; i--) {
      maxSupplyBytes[i] = Number(value & BigInt(0xff));
      value = value >> BigInt(8);
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
