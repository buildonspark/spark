import type { SparkContext } from "../types";
import type { IssuerSparkWallet } from "@buildonspark/issuer-sdk";

export function getWalletFromContextOrGlobal(
  context: SparkContext,
  globalNamedWallets: Map<string, { wallet: IssuerSparkWallet }>,
  walletName?: string
): { wallet: IssuerSparkWallet; name: string } {
  if (walletName) {
    const walletInfo = globalNamedWallets.get(walletName);
    if (!walletInfo) {
      throw new Error(`Wallet ${walletName} not found. Initialize it first with initWallet`);
    }
    return { wallet: walletInfo.wallet, name: walletName };
  } else if (context.sparkWallet) {
    return { wallet: context.sparkWallet, name: "anonymous" };
  } else {
    throw new Error("No sender wallet specified or found in context");
  }
}
