import { NodeHttpTransport } from "nice-grpc-web";
import { SparkWalletBrowser } from "./spark-wallet.browser.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { WalletConfigService } from "../services/config.js";

export class SparkWalletBare extends SparkWalletBrowser {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerBrowser(config, NodeHttpTransport());
  }
}

export { SparkWalletBare as SparkWallet };
