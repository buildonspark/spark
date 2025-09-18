import { IssuerSparkWallet as BaseIssuerSparkWallet } from "./issuer-spark-wallet.js";
import {
  initializeTracerEnv as initializeTracerEnvNodeJS,
  ConnectionManager,
  type WalletConfigService,
} from "@buildonspark/spark-sdk";

export class IssuerSparkWalletNodeJS extends BaseIssuerSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManager(config);
  }

  protected initializeTracerEnv({
    spanProcessors,
    traceUrls,
  }: Parameters<BaseIssuerSparkWallet["initializeTracerEnv"]>[0]) {
    initializeTracerEnvNodeJS({ spanProcessors, traceUrls });
  }
}

export { IssuerSparkWalletNodeJS as IssuerSparkWallet };
