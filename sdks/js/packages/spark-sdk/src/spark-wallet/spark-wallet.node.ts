import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { ConnectionManagerNodeJS } from "../services/connection/connection.node.js";
import { type WalletConfigService } from "../services/config.js";
import type { LoggingService } from "../utils/logging-service.js";

export class SparkWalletNodeJS extends BaseSparkWallet {
  protected buildConnectionManager(
    config: WalletConfigService,
    logging: LoggingService,
  ) {
    return new ConnectionManagerNodeJS(config, "identity", logging);
  }
}

export { SparkWalletNodeJS as SparkWallet };
