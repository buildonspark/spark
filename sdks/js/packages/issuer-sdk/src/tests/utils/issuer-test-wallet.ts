import { SparkWalletProps } from "@buildonspark/spark-sdk";
import { IssuerSparkWallet } from "../../issuer-wallet/issuer-spark-wallet.node.js";

export class IssuerSparkWalletTesting extends IssuerSparkWallet {
  private disableEvents: boolean;

  constructor(props: SparkWalletProps, disableEvents = true) {
    super(props.options);
    this.disableEvents = disableEvents;
  }

  static async initialize(props: SparkWalletProps) {
    const wallet = new IssuerSparkWalletTesting(props, true);
    const initResponse = await wallet.initWallet(
      props.mnemonicOrSeed,
      props.accountNumber,
      props.options,
    );
    return initResponse;
  }

  protected override async setupBackgroundStream() {
    if (!this.disableEvents) {
      return super.setupBackgroundStream();
    }
    return;
  }
}
