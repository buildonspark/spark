import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { WalletConfigService } from "../services/config.js";
import type { LoggingService } from "../utils/logging-service.js";

export class SparkWalletBrowser extends BaseSparkWallet {
  protected buildConnectionManager(
    config: WalletConfigService,
    logging: LoggingService,
  ) {
    return new ConnectionManagerBrowser(config, "identity", undefined, logging);
  }
}

export { SparkWalletBrowser as SparkWallet };
