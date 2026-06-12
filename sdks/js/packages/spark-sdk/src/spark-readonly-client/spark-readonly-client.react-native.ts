import type { WalletConfigService } from "../services/config.js";
import { type AuthMode } from "../services/index.js";
import type { LoggingService } from "../utils/logging-service.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import {
  ConnectionManagerReactNative,
  hasNativeGrpcModule,
} from "../services/connection/connection.react-native.js";
import { XHRTransport } from "../services/xhr-transport.js";
import { SparkReadonlyClient } from "./spark-readonly-client.js";

export class SparkReadonlyClientReactNative extends SparkReadonlyClient {
  protected buildConnectionManager(
    config: WalletConfigService,
    authMode: AuthMode,
    logging: LoggingService,
  ) {
    if (!hasNativeGrpcModule()) {
      return new ConnectionManagerBrowser(
        config,
        authMode,
        XHRTransport({ logging }),
        logging,
      );
    }

    return new ConnectionManagerReactNative(config, authMode, logging);
  }
}

export { SparkReadonlyClientReactNative as SparkReadonlyClient };
