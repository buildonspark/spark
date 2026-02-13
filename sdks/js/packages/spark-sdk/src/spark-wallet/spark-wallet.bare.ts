import { WalletConfigService } from "../services/config.js";
import { BareHttpTransport } from "../services/connection/bare-http-transport.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";

export class SparkWalletBare extends BaseSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerBrowser(
      config,
      "identity",
      BareHttpTransport(),
    );
  }
}

export { SparkWalletBare as SparkWallet };
