import { toXOnly } from "../utils/buffer";
import { EMPTY_TOKEN_PUBKEY } from "../utils/constants";
import { TokenPubkeyAnnouncement, TokenPubkeyAnnouncementDto } from "./lrc20-transaction";

export class TokenPubkey {
  pubkey: Buffer;
  constructor(pubkey?: Buffer) {
    this.pubkey = pubkey || EMPTY_TOKEN_PUBKEY;
  }

  get inner() {
    return this.pubkey;
  }
}

export class TokenPubkeyInfo {
  constructor(public announcement: TokenPubkeyAnnouncement | null, public totalSupply: bigint) {}

  public static fromTokenPubkeyInfoDto(info: TokenPubkeyInfoDto): TokenPubkeyInfo {
    const announcement = info.announcement ? TokenPubkeyAnnouncement.fromTokenPubkeyAnnouncementDto(info.announcement) : null;
    return new TokenPubkeyInfo(announcement, info.total_supply);
  }
}

export interface TokenPubkeyInfoDto {
  announcement: TokenPubkeyAnnouncementDto | null;
  total_supply: bigint;
}
