import { QueryTransfersResponse, Transfer } from "../../proto/spark.js";
import { ConfigOptions } from "../../services/wallet-config.js";
import { SparkSigner } from "../../signer/signer.js";
import type { SparkWalletProps } from "../../spark-wallet/types.js";
import { BitcoinFaucet } from "./test-faucet.js";
import { Transaction } from "@scure/btc-signer";
import { NetworkType } from "../../index.node.js";
import { SparkWalletNodeJS } from "../../spark-wallet/spark-wallet.node.js";

interface ISparkWalletTesting extends SparkWalletNodeJS {
  getSigner(): SparkSigner;
  queryPendingTransfers(): Promise<QueryTransfersResponse>;
  verifyPendingTransfer(transfer: Transfer): Promise<Map<string, Uint8Array>>;
}

export class SparkWalletTesting
  extends SparkWalletNodeJS
  implements ISparkWalletTesting
{
  public disableEvents = true;

  static async initialize(props: SparkWalletProps, disableEvents = true) {
    const wallet = new SparkWalletTesting(props.options, props.signer);
    wallet.disableEvents = disableEvents;
    const initWalletResponse = await wallet.initWallet(
      props.mnemonicOrSeed,
      props.accountNumber,
      props.options,
    );
    return initWalletResponse;
  }

  protected override async setupBackgroundStream() {
    console.log(
      "tmp SparkWalletTesting.setupBackgroundStream disableEvents",
      this.disableEvents,
    );
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

export async function initTestingWallet(
  amount: bigint,
  network: NetworkType,
): Promise<{
  wallet: SparkWalletTesting;
  depositAddress: string;
  signedTx: Transaction;
  vout?: number;
  faucet: BitcoinFaucet;
}> {
  const faucet = BitcoinFaucet.getInstance();
  const { wallet: userWallet } = await SparkWalletTesting.initialize(
    {
      options: {
        network: network,
      },
    },
    false,
  );

  const depositAddress = await userWallet.getStaticDepositAddress();

  const signedTx = await faucet.sendToAddress(depositAddress, amount);

  const outputs = Array.from({ length: signedTx.outputsLength }, (_, i) => ({
    output: signedTx.getOutput(i),
    index: i,
  }));
  const match = outputs.find(({ output }) => output.amount === amount);
  const vout = match ? match.index : undefined;
  return {
    wallet: userWallet,
    depositAddress,
    signedTx,
    vout,
    faucet,
  };
}
