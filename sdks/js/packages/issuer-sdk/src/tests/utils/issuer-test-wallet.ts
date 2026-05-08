import { IssuerSparkWallet } from "../../issuer-wallet/issuer-spark-wallet.node.js";

export class IssuerSparkWalletTesting extends IssuerSparkWallet {
  protected override setupBackgroundStream(): Promise<void> {
    return new Promise((resolve) => {
      console.log("IssuerSparkWalletTesting.setupBackgroundStream disabled");
      resolve();
    });
  }

  public syncTokenOutputsForTesting(): Promise<void> {
    return this.syncTokenOutputs();
  }
}
