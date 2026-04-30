import type { WalletConfigService } from "../services/config.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { XHRTransport } from "../services/xhr-transport.js";
import type { LoggingService } from "../utils/logging-service.js";
import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";

export class SparkWalletReactNative extends BaseSparkWallet {
  protected buildConnectionManager(
    config: WalletConfigService,
    logging: LoggingService,
  ) {
    return new ConnectionManagerBrowser(
      config,
      "identity",
      XHRTransport({ logging }),
      logging,
    );
  }
}

export { SparkWalletReactNative as SparkWallet };
