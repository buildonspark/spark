import { QueryPendingTransfersResponse, Transfer } from "../../proto/spark.js";
import { SparkSigner } from "../../signer/signer.js";
import { SparkWallet, SparkWalletProps } from "../../spark-sdk.js";

interface ISparkWalletTesting extends SparkWallet {
  getSigner(): SparkSigner;
  queryPendingTransfers(): Promise<QueryPendingTransfersResponse>;
  verifyPendingTransfer(transfer: Transfer): Promise<Map<string, Uint8Array>>;
}

export class SparkWalletTesting
  extends SparkWallet
  implements ISparkWalletTesting
{
  static async create(props: SparkWalletProps) {
    const wallet = new SparkWalletTesting(props.options, props.signer);
    const initResponse = await wallet.initWallet(props.mnemonicOrSeed);
    return {
      wallet,
      mnemonic: initResponse?.mnemonic,
    };
  }

  public getSigner(): SparkSigner {
    return this.config.signer;
  }

  public async queryPendingTransfers(): Promise<QueryPendingTransfersResponse> {
    return await this.transferService.queryPendingTransfers();
  }

  public async verifyPendingTransfer(
    transfer: Transfer,
  ): Promise<Map<string, Uint8Array>> {
    return await this.transferService.verifyPendingTransfer(transfer);
  }
}
