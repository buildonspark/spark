import type { WalletConfigService } from "../services/config.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { type AuthMode } from "../services/index.js";
import { XHRTransport } from "../services/xhr-transport.js";
import type { LoggingService } from "../utils/logging-service.js";
import { SparkReadonlyClient } from "./spark-readonly-client.js";

export class SparkReadonlyClientReactNative extends SparkReadonlyClient {
  protected buildConnectionManager(
    config: WalletConfigService,
    authMode: AuthMode,
    logging: LoggingService,
  ) {
    return new ConnectionManagerBrowser(
      config,
      authMode,
      XHRTransport({ logging }),
      logging,
    );
  }
}

export { SparkReadonlyClientReactNative as SparkReadonlyClient };
