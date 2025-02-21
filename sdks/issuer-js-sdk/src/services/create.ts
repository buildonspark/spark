import { TokenPubkeyAnnouncement, TokenPubkey, Lrc20Transaction, Lrc20TransactionDto, LRCWallet } from "lrc20-js-sdk";
import { randomBytes } from "crypto";

export async function announceToken(
    lrcWallet: LRCWallet,
    tokenName: string,
    tokenTicker: string,
    maxSupply: bigint,
    decimals: number,
    isFreezeable: boolean,
    feeRateSatsPerVb: number = 2,
  ): Promise<string> {
    const tokenId = randomBytes(32);
  
    const announcement = new TokenPubkeyAnnouncement(
      new TokenPubkey(tokenId),
      tokenName,
      tokenTicker,
      decimals,
      maxSupply,
      isFreezeable,
    );
  
    const tx: Lrc20Transaction = await lrcWallet.prepareAnnouncement(announcement, feeRateSatsPerVb);
    const txDto = Lrc20TransactionDto.fromLrc20Transaction(tx);
    return await lrcWallet.broadcast(txDto);
  }