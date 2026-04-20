import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { ConnectionManagerNodeJS } from "../services/connection/connection.node.js";
import { WalletConfigService } from "../services/config.js";

export class SparkWalletNodeJS extends BaseSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerNodeJS(config);
  }
}

export { SparkWalletNodeJS as SparkWallet };
