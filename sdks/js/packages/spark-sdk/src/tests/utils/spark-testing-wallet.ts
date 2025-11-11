import { QueryTransfersResponse, Transfer } from "../../proto/spark.js";
import { ConfigOptions } from "../../services/wallet-config.js";
import { SparkSigner } from "../../signer/signer.js";
import type { SparkWalletProps } from "../../spark-wallet/types.js";
import { BitcoinFaucet } from "./test-faucet.js";
import { Transaction } from "@scure/btc-signer";
import { SparkWallet, NetworkType } from "../../index.node.js";
import type { ConnectionManagerNodeJS } from "../../services/connection/connection.node.js";

export class SparkWalletTesting extends SparkWallet {
  protected override async setupBackgroundStream() {
    // Background stream is disabled by default, use SparkWalletTestingWithStream to enable it
    return;
  }

  public override async *optimizeLeaves(
    multiplicity: number | undefined = undefined,
  ): AsyncGenerator<
    {
      step: number;
      total: number;
      controller: AbortController;
    },
    void,
    void
  > {
    // Optimize is disabled by default.
    return;
  }

  protected async proxyParentSetupBackgroundStream() {
    return super.setupBackgroundStream();
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

export class SparkWalletTestingWithStream extends SparkWalletTesting {
  protected override async setupBackgroundStream() {
    return this.proxyParentSetupBackgroundStream();
  }
}

export class SparkWalletTestingIntegration extends SparkWalletTesting {
  public getConfigService() {
    return this.config;
  }

  public getConnectionManager() {
    return this.connectionManager as ConnectionManagerNodeJS;
  }

  public getTransferService() {
    return this.transferService;
  }

  public getDepositService() {
    return this.depositService;
  }

  public getLightningService() {
    return this.lightningService;
  }

  public getCoopExitService() {
    return this.coopExitService;
  }

  public getSigningService() {
    return this.signingService;
  }

  public getTokenTransactionService() {
    return this.tokenTransactionService;
  }
}

export class SparkWalletTestingIntegrationWithStream extends SparkWalletTestingIntegration {
  protected override async setupBackgroundStream() {
    return this.proxyParentSetupBackgroundStream();
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
  const { wallet: userWallet } = await SparkWalletTestingWithStream.initialize({
    options: {
      network: network,
    },
  });

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
