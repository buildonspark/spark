import { SparkWalletProps } from "@buildonspark/spark-sdk";
import { IssuerSparkWallet } from "../../issuer-spark-wallet.js";

export class IssuerSparkWalletTesting extends IssuerSparkWallet {
  private disableEvents: boolean;

  constructor(props: SparkWalletProps, disableEvents = true) {
    super(props.options);
    this.disableEvents = disableEvents;
  }

  static async initialize(props: SparkWalletProps): Promise<{
    mnemonic?: string;
    wallet: IssuerSparkWalletTesting;
  }> {
    const wallet = new IssuerSparkWalletTesting(props, true);

    const result = await wallet.initWallet(props.mnemonicOrSeed);
    return {
      wallet,
      mnemonic: result?.mnemonic,
    };
  }

  protected override async setupBackgroundStream() {
    if (!this.disableEvents) {
      return super.setupBackgroundStream();
    }
    return;
  }
}
