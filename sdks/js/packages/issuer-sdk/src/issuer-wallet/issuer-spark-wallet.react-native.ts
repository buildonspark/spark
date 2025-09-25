import { IssuerSparkWallet as BaseIssuerSparkWallet } from "./issuer-spark-wallet.js";
import type { WalletConfigService } from "@buildonspark/spark-sdk";

/* We just need this path to exist to prevent RN from importing the default browser version.
   This extends @buildonspark/spark-sdk ReactNativeSparkWallet due to export paths there. */
export class IssuerSparkWalletReactNative extends BaseIssuerSparkWallet {}

export { IssuerSparkWalletReactNative as IssuerSparkWallet };
