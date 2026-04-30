import { WalletConfigService } from "../services/config.js";
import { ConnectionManagerNodeJS } from "../services/connection/connection.node.js";
import { AuthMode } from "../services/index.js";
import type { LoggingService } from "../utils/logging-service.js";
import { SparkReadonlyClient } from "./spark-readonly-client.js";

export class SparkReadonlyClientNodeJS extends SparkReadonlyClient {
  protected buildConnectionManager(
    config: WalletConfigService,
    authMode: AuthMode,
    logging: LoggingService,
  ) {
    return new ConnectionManagerNodeJS(config, authMode, logging);
  }
}

export { SparkReadonlyClientNodeJS as SparkReadonlyClient };
