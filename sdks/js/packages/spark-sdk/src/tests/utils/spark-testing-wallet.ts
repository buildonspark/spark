import { QueryPendingTransfersResponse, Transfer } from "../../proto/spark.js";
import { SparkSigner } from "../../signer/signer.js";
import { SparkWallet } from "../../spark-sdk.js";

interface ISparkWalletTesting extends SparkWallet {
  getSigner(): SparkSigner;
  queryPendingTransfers(): Promise<QueryPendingTransfersResponse>;
  verifyPendingTransfer(transfer: Transfer): Promise<Map<string, Uint8Array>>;
}

export class SparkWalletTesting
  extends SparkWallet
  implements ISparkWalletTesting
{
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
