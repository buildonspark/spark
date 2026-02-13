import type { WalletConfigService } from "../services/config.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { AuthMode } from "../services/index.js";
import { XHRTransport } from "../services/xhr-transport.js";
import { SparkReadonlyClient } from "./spark-readonly-client.js";

export class SparkReadonlyClientReactNative extends SparkReadonlyClient {
  protected buildConnectionManager(
    config: WalletConfigService,
    authMode: AuthMode,
  ) {
    return new ConnectionManagerBrowser(config, authMode, XHRTransport());
  }
}

export { SparkReadonlyClientReactNative as SparkReadonlyClient };
