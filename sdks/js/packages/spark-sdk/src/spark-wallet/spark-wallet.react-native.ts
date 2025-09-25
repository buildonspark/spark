import { XHRTransport } from "../services/xhr-transport.js";
import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { ReactNativeSparkSigner } from "../signer/signer.react-native.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import type { WalletConfigService } from "../services/config.js";

export class SparkWalletReactNative extends BaseSparkWallet {
  protected buildSigner() {
    return new ReactNativeSparkSigner();
  }

  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerBrowser(config, XHRTransport());
  }
}

export { SparkWalletReactNative as SparkWallet };
