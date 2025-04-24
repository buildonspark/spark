import { SparkWallet, SparkWalletProps } from "@buildonspark/spark-sdk";
import {
  QueryTransfersResponse,
  Transfer,
} from "@buildonspark/spark-sdk/proto/spark";
import { ConfigOptions } from "@buildonspark/spark-sdk/services/wallet-config";
import { SparkSigner } from "@buildonspark/spark-sdk/signer";

interface ISparkWalletTesting extends SparkWallet {
  getSigner(): SparkSigner;
  queryPendingTransfers(): Promise<QueryTransfersResponse>;
  verifyPendingTransfer(transfer: Transfer): Promise<Map<string, Uint8Array>>;
}

export class SparkWalletTesting
  extends SparkWallet
  implements ISparkWalletTesting
{
  private disableEvents: boolean;

  constructor(
    options?: ConfigOptions,
    signer?: SparkSigner,
    disableEvents = true,
  ) {
    super(options, signer);
    this.disableEvents = disableEvents;
  }

  static async initialize(props: SparkWalletProps, disableEvents = true) {
    const wallet = new SparkWalletTesting(
      props.options,
      props.signer,
      disableEvents,
    );

    const initResponse = await wallet.initWallet(props.mnemonicOrSeed);
    return {
      wallet,
      mnemonic: initResponse?.mnemonic,
    };
  }

  protected override async setupBackgroundStream() {
    if (!this.disableEvents) {
      await super.setupBackgroundStream();
    }
    return;
  }

  public getSigner(): SparkSigner {
    return this.config.signer;
  }

  public async queryPendingTransfers(): Promise<QueryTransfersResponse> {
    return await this.transferService.queryPendingTransfers();
  }

  public async verifyPendingTransfer(
    transfer: Transfer,
  ): Promise<Map<string, Uint8Array>> {
    return await this.transferService.verifyPendingTransfer(transfer);
  }
}
