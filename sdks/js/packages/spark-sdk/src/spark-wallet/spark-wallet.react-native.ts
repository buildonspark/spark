import { XHRTransport } from "../services/xhr-transport.js";
import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import type { WalletConfigService } from "../services/config.js";

export class SparkWalletReactNative extends BaseSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerBrowser(config, XHRTransport());
  }
}

export { SparkWalletReactNative as SparkWallet };
