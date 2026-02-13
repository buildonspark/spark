import { WalletConfigService } from "../services/config.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { AuthMode } from "../services/index.js";
import { SparkReadonlyClient } from "./spark-readonly-client.js";

export class SparkReadonlyClientBrowser extends SparkReadonlyClient {
  protected buildConnectionManager(
    config: WalletConfigService,
    authMode: AuthMode,
  ) {
    return new ConnectionManagerBrowser(config, authMode);
  }
}

export { SparkReadonlyClientBrowser as SparkReadonlyClient };
