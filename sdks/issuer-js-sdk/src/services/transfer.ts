import { SparkSDK } from "spark-js-sdk/src/spark-sdk";

export class TransferService {
  private sparkSdk: SparkSDK;

  constructor(sparkSdk: SparkSDK) {
    this.sparkSdk = sparkSdk;
  }

  async constructTransfer(
    fromAddress: string,
    toAddress: string
  ): Promise<Uint8Array> {
    // TODO: Implement
    throw new Error("Not implemented");
  }

  async finalizeTransfer(tx: Uint8Array): Promise<Uint8Array> {
    const isValid = await this.validateOperation(tx);
    if (!isValid) {
      throw new Error("Invalid transfer operation");
    }

    // TODO: Implement
    throw new Error("Not implemented");
  }

  private async validateOperation(tx: Uint8Array): Promise<boolean> {
    // TODO: Implement validation against LRC20 rules
    return true;
  }
}
