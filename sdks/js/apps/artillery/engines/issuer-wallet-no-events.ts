import { SparkWalletProps } from "@buildonspark/spark-sdk";
import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";

/**
 * IssuerSparkWallet wrapper that disables background event streams for better performance in load testing
 * and exposes testing methods for manual transfer management
 */
export class IssuerSparkWalletNoEvents extends IssuerSparkWallet {
  private disableEvents: boolean;

  constructor(props: SparkWalletProps, disableEvents = true) {
    super(props.options, props.signer);
    this.disableEvents = disableEvents;
  }

  static async initialize(
    props: SparkWalletProps & { skipBackgroundStream?: boolean },
  ) {
    const { skipBackgroundStream = false, ...walletProps } = props;
    const wallet = new IssuerSparkWalletNoEvents(walletProps, true);

    const result = await wallet.initWallet(
      walletProps.mnemonicOrSeed,
      walletProps.accountNumber,
    );

    // Note: skipBackgroundStream is handled by disableEvents in the constructor
    // The background stream setup is controlled by the setupBackgroundStream override below
    if (skipBackgroundStream) {
      console.log(
        `[IssuerWallet] Background stream disabled (skipBackgroundStream=true)`,
      );
    }

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

  // Override the cleanupConnections method to ensure connections are closed
  async cleanupConnections(): Promise<void> {
    if (super.cleanupConnections) {
      return super.cleanupConnections();
    }
    if (this.connectionManager) {
      await this.connectionManager.closeConnections();
    }
  }

  public async queryPendingTransfers(): Promise<any> {
    return await (this as any).transferService.queryPendingTransfers();
  }

  public async claimPendingTransfer(transfer: any): Promise<void> {
    const leafPubKeyMap = await this.verifyPendingTransfer(transfer);

    const claimingNodes = [];
    for (const leaf of transfer.leaves) {
      if (leaf.leaf) {
        const leafPubKey = leafPubKeyMap.get(leaf.leaf.id);
        if (leafPubKey) {
          claimingNodes.push({
            leaf: {
              ...leaf.leaf,
              refundTx: leaf.intermediateRefundTx,
            },
            signingPubKey: leafPubKey,
            newSigningPubKey: await (
              this as any
            ).config.signer.generatePublicKey(
              (await import("@noble/hashes/sha256")).sha256(leaf.leaf.id),
            ),
          });
        }
      }
    }

    await (this as any).transferService.claimTransfer(transfer, claimingNodes);
  }

  public async verifyPendingTransfer(
    transfer: any,
  ): Promise<Map<string, Uint8Array>> {
    return await (this as any).transferService.verifyPendingTransfer(transfer);
  }
}
