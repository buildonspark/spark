import { BareHttpTransport } from "../services/connection/bare-http-transport.js";
import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { WalletConfigService } from "../services/config.js";

export class SparkWalletBare extends BaseSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerBrowser(config, BareHttpTransport());
  }
}

export { SparkWalletBare as SparkWallet };
