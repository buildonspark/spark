import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { WalletConfigService } from "../services/config.js";

export class SparkWalletBrowser extends BaseSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerBrowser(config);
  }
}

export { SparkWalletBrowser as SparkWallet };
