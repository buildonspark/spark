import { WalletConfigService } from "../services/config.js";
import { BareHttpTransport } from "../services/connection/bare-http-transport.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { AuthMode } from "../services/index.js";
import type { LoggingService } from "../utils/logging-service.js";
import { SparkReadonlyClient as BaseSparkReadonlyClient } from "./spark-readonly-client.js";

export class SparkReadonlyClientBare extends BaseSparkReadonlyClient {
  protected buildConnectionManager(
    config: WalletConfigService,
    authMode: AuthMode,
    logging: LoggingService,
  ) {
    return new ConnectionManagerBrowser(
      config,
      authMode,
      BareHttpTransport({ logging }),
      logging,
    );
  }
}

export { SparkReadonlyClientBare as SparkReadonlyClient };
