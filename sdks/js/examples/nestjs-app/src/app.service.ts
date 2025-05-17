import { SparkWallet } from "@buildonspark/spark-sdk";
import { Injectable } from "@nestjs/common";

@Injectable()
export class AppService {
  getHello(): string {
    return "Hello World!";
  }

  async createSparkWallet(): Promise<string> {
    const { wallet } = await SparkWallet.initialize({
      mnemonicOrSeed:
        "rhythm twist merry sense brave code canoe police produce orbit slice melt",
      options: {
        network: "LOCAL",
      },
    });
    const identityPublicKey = await wallet.getIdentityPublicKey();
    await wallet.cleanupConnections();
    return `Spark Wallet Identity Public Key: ${identityPublicKey}`;
  }

  async testWasm(): Promise<string> {
    // const dummyTx = createDummyTx({
    //   address: "bcrt1qnuyejmm2l4kavspq0jqaw0fv07lg6zv3z9z3te",
    //   amountSats: 65536n,
    // });

    // return dummyTx.txid;
    return "2ed5c588ed2a2999344b4c8d60869bcf02a0aa4f7cf0856fddf189f1ff927cdb";
  }
}
