import lrc20sdk from "@buildonspark/lrc20-sdk";

export async function announceTokenL1(
  lrcWallet: lrc20sdk.LRCWallet,
  tokenName: string,
  tokenTicker: string,
  decimals: number,
  maxSupply: bigint,
  isFreezable: boolean,
  feeRateSatsPerVb: number = 2.0,
): Promise<{txid: string}> {
  await lrcWallet.syncWallet();

  const tokenPublicKey = new lrc20sdk.TokenPubkey(lrcWallet.pubkey);

  const announcement = new lrc20sdk.TokenPubkeyAnnouncement(
    tokenPublicKey,
    tokenName,
    tokenTicker,
    decimals,
    maxSupply,
    isFreezable,
  );

  const tx = await lrcWallet.prepareAnnouncement(
    announcement,
    feeRateSatsPerVb,
  );

  let txid = await lrcWallet.broadcastRawBtcTransaction(tx.bitcoin_tx.toHex());

  return { txid }
}

