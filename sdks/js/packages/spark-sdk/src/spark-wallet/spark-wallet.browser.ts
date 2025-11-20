import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { WebTracerProvider } from "@opentelemetry/sdk-trace-web";
import {
  ConnectionManagerBrowser,
  type Transport,
} from "../services/connection/connection.browser.js";
import { WalletConfigService } from "../services/config.js";

export class SparkWalletBrowser extends BaseSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerBrowser(config);
  }
}

export { SparkWalletBrowser as SparkWallet };
