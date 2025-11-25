import { IssuerSparkWallet } from "../../issuer-wallet/issuer-spark-wallet.node.js";

export class IssuerSparkWalletTesting extends IssuerSparkWallet {
  protected override async setupBackgroundStream() {
    console.log("IssuerSparkWalletTesting.setupBackgroundStream disabled");
    return;
  }
}
