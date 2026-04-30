import { WalletConfigService } from "../services/config.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { AuthMode } from "../services/index.js";
import type { LoggingService } from "../utils/logging-service.js";
import { SparkReadonlyClient } from "./spark-readonly-client.js";

export class SparkReadonlyClientBrowser extends SparkReadonlyClient {
  protected buildConnectionManager(
    config: WalletConfigService,
    authMode: AuthMode,
    logging: LoggingService,
  ) {
    return new ConnectionManagerBrowser(config, authMode, undefined, logging);
  }
}

export { SparkReadonlyClientBrowser as SparkReadonlyClient };
