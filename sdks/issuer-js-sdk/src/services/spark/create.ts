import { ChromaAnnouncement, Chroma, YuvTransaction, YuvTransactionDto, LRCWallet } from "@wcbd/yuv-js-sdk/src/index";
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
  
    const announcement = new ChromaAnnouncement(
      new Chroma(tokenId),
      tokenName,
      tokenTicker,
      decimals,
      maxSupply,
      isFreezeable,
    );
  
    const tx: YuvTransaction = await lrcWallet.prepareAnnouncement(announcement, feeRateSatsPerVb);
    const txDto = YuvTransactionDto.fromYuvTransaction(tx);
    return await lrcWallet.broadcast(txDto);
  }