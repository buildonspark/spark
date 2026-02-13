import type { WalletConfigService } from "../services/config.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { XHRTransport } from "../services/xhr-transport.js";
import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";

export class SparkWalletReactNative extends BaseSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerBrowser(config, "identity", XHRTransport());
  }
}

export { SparkWalletReactNative as SparkWallet };
