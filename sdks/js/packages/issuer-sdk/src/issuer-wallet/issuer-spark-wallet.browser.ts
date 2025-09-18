import { IssuerSparkWallet as BaseIssuerSparkWallet } from "./issuer-spark-wallet.js";
import {
  initializeTracerEnv as initializeTracerEnvBrowser,
  ConnectionManager,
  type WalletConfigService,
} from "@buildonspark/spark-sdk";

export class IssuerSparkWalletBrowser extends BaseIssuerSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManager(config);
  }

  protected initializeTracerEnv({
    spanProcessors,
    traceUrls,
  }: Parameters<BaseIssuerSparkWallet["initializeTracerEnv"]>[0]) {
    initializeTracerEnvBrowser({ spanProcessors, traceUrls });
  }
}

export { IssuerSparkWalletBrowser as IssuerSparkWallet };
