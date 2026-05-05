import { type WalletConfigService } from "../services/config.js";
import { BareHttpTransport } from "../services/connection/bare-http-transport.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import type { LoggingService } from "../utils/logging-service.js";
import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";

export class SparkWalletBare extends BaseSparkWallet {
  protected buildConnectionManager(
    config: WalletConfigService,
    logging: LoggingService,
  ) {
    return new ConnectionManagerBrowser(
      config,
      "identity",
      BareHttpTransport({ logging }),
      logging,
    );
  }
}

export { SparkWalletBare as SparkWallet };
