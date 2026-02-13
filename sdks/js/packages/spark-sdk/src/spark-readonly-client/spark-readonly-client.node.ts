import { WalletConfigService } from "../services/config.js";
import { ConnectionManagerNodeJS } from "../services/connection/connection.node.js";
import { AuthMode } from "../services/index.js";
import { SparkReadonlyClient } from "./spark-readonly-client.js";

export class SparkReadonlyClientNodeJS extends SparkReadonlyClient {
  protected buildConnectionManager(
    config: WalletConfigService,
    authMode: AuthMode,
  ) {
    return new ConnectionManagerNodeJS(config, authMode);
  }
}

export { SparkReadonlyClientNodeJS as SparkReadonlyClient };
