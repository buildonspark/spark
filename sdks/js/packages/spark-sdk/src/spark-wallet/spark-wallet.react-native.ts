import type { WalletConfigService } from "../services/config.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import type { LoggingService } from "../utils/logging-service.js";
import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import {
  ConnectionManagerReactNative,
  hasNativeGrpcModule,
} from "../services/connection/connection.react-native.js";
import { XHRTransport } from "../services/xhr-transport.js";

export class SparkWalletReactNative extends BaseSparkWallet {
  protected buildConnectionManager(
    config: WalletConfigService,
    logging: LoggingService,
  ) {
    if (!hasNativeGrpcModule()) {
      return new ConnectionManagerBrowser(
        config,
        "identity",
        XHRTransport({ logging }),
        logging,
      );
    }

    return new ConnectionManagerReactNative(config, "identity", logging);
  }
}

export { SparkWalletReactNative as SparkWallet };
