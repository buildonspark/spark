import { IssuerSparkWallet as BaseIssuerSparkWallet } from "./issuer-spark-wallet.js";
import {
  ConnectionManager,
  type WalletConfigService,
} from "@buildonspark/spark-sdk";

export class IssuerSparkWalletNodeJS extends BaseIssuerSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManager(config);
  }
}

export { IssuerSparkWalletNodeJS as IssuerSparkWallet };
