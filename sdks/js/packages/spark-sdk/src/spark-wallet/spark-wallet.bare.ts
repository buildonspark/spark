import { BareHttpTransport } from "../services/connection/bare-http-transport.js";
import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { WalletConfigService } from "../services/config.js";
import { initializeTracerEnvBrowser } from "../otel/initializeTracerEnv.browser.js";

export class SparkWalletBare extends BaseSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerBrowser(config, BareHttpTransport());
  }

  protected initializeTracerEnv({
    spanProcessors,
    traceUrls,
  }: Parameters<BaseSparkWallet["initializeTracerEnv"]>[0]) {
    /* Use Browser otel wrapper for now (more compatible with bare-fetch): */
    initializeTracerEnvBrowser({ spanProcessors, traceUrls });
  }
}

export { SparkWalletBare as SparkWallet };
