import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { ConnectionManagerNodeJS } from "../services/connection/connection.node.js";
import { WalletConfigService } from "../services/config.js";
import { initializeTracerEnvNodeJS } from "../otel/initializeTracerEnv.node.js";

export class SparkWalletNodeJS extends BaseSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerNodeJS(config);
  }

  protected initializeTracerEnv({
    spanProcessors,
    traceUrls,
  }: Parameters<BaseSparkWallet["initializeTracerEnv"]>[0]) {
    initializeTracerEnvNodeJS({ spanProcessors, traceUrls });
  }
}

export { SparkWalletNodeJS as SparkWallet };
