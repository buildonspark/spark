import { IssuerSparkWallet as BaseIssuerSparkWallet } from "./issuer-spark-wallet.js";
import {
  ConnectionManager,
  type WalletConfigService,
} from "@buildonspark/spark-sdk";

export class IssuerSparkWalletBrowser extends BaseIssuerSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManager(config);
  }
}

export { IssuerSparkWalletBrowser as IssuerSparkWallet };
