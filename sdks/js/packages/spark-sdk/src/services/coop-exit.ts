import { Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { uuidv7 } from "uuidv7";
import { NetworkError, ValidationError } from "../errors/types.js";
import {
  CooperativeExitResponse,
  LeafRefundTxSigningJob,
  Transfer,
} from "../proto/spark.js";
import { getTxFromRawTxBytes } from "../utils/bitcoin.js";
import { Network } from "../utils/network.js";
import { createConnectorRefundTxs } from "../utils/transaction.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection/connection.js";
import { SigningService } from "./signing.js";
import type { LeafKeyTweak } from "./transfer.js";
import { BaseTransferService, LeafRefundSigningData } from "./transfer.js";

export type GetConnectorRefundSignaturesParams = {
  leaves: LeafKeyTweak[];
  exitTxId: Uint8Array;
  connectorOutputs: TransactionInput[];
  receiverPubKey: Uint8Array;
  transferId: string;
};

export class CoopExitService extends BaseTransferService {
  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager,
    signingService: SigningService,
  ) {
    super(config, connectionManager, signingService);
  }

  async getConnectorRefundSignatures({
    leaves,
    exitTxId,
    connectorOutputs,
    receiverPubKey,
    transferId,
  }: GetConnectorRefundSignaturesParams): Promise<{
    transfer: Transfer;
    signaturesMap: Map<string, Uint8Array>;
    directSignaturesMap: Map<string, Uint8Array>;
    directFromCpfpSignaturesMap: Map<string, Uint8Array>;
  }> {
    const {
      transfer,
      signaturesMap,
      directSignaturesMap,
      directFromCpfpSignaturesMap,
    } = await this.signCoopExitRefunds(
      leaves,
      exitTxId,
      connectorOutputs,
      receiverPubKey,
      transferId,
    );

    const transferTweak = await this.deliverTransferPackage(
      transfer,
      leaves,
      signaturesMap,
      directSignaturesMap,
      directFromCpfpSignaturesMap,
    );

    return {
      transfer: transferTweak,
      signaturesMap,
      directSignaturesMap,
      directFromCpfpSignaturesMap,
    };
  }

  private async signCoopExitRefunds(
    leaves: LeafKeyTweak[],
    exitTxId: Uint8Array,
    connectorOutputs: TransactionInput[],
    receiverPubKey: Uint8Array,
    transferId: string,
  ): Promise<{
    transfer: Transfer;
    signaturesMap: Map<string, Uint8Array>;
    directSignaturesMap: Map<string, Uint8Array>;
    directFromCpfpSignaturesMap: Map<string, Uint8Array>;
  }> {
    if (leaves.length !== connectorOutputs.length) {
      throw new ValidationError(
        "Mismatch between leaves and connector outputs",
        {
          field: "leaves/connectorOutputs",
          value: {
            leavesCount: leaves.length,
            outputsCount: connectorOutputs.length,
          },
          expected: "Equal length",
        },
      );
    }

    const signingJobs: LeafRefundTxSigningJob[] = [];
    const leafDataMap: Map<string, LeafRefundSigningData> = new Map();

    for (let i = 0; i < leaves.length; i++) {
      const leaf = leaves[i];
      if (!leaf) {
        throw new ValidationError("Missing leaf", {
          field: "leaf",
          value: leaf,
          expected: "Valid leaf object",
        });
      }
      const connectorOutput = connectorOutputs[i];
      if (!connectorOutput) {
        throw new ValidationError("Missing connector output", {
          field: "connectorOutput",
          value: connectorOutput,
          expected: "Valid connector output",
        });
      }

      const nodeTx = getTxFromRawTxBytes(leaf.leaf.nodeTx);

      let directNodeTx: Transaction | undefined;
      if (leaf.leaf.directTx.length > 0) {
        directNodeTx = getTxFromRawTxBytes(leaf.leaf.directTx);
      }

      const currentRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);
      if (!currentRefundTx) {
        throw new ValidationError("Invalid refund transaction", {
          field: "currentRefundTx",
          value: currentRefundTx,
          expected: "Non-null refund transaction",
        });
      }

      const currentSequence = currentRefundTx.getInput(0).sequence;
      if (!currentSequence) {
        throw new ValidationError("Invalid refund transaction", {
          field: "sequence",
          value: currentRefundTx.getInput(0),
          expected: "Non-null sequence",
        });
      }

      let currentDirectRefundTx: Transaction | undefined;
      if (leaf.leaf.directRefundTx.length > 0) {
        currentDirectRefundTx = getTxFromRawTxBytes(leaf.leaf.directRefundTx);
      }

      const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
        createConnectorRefundTxs({
          nodeTx,
          directNodeTx,
          sequence: currentSequence,
          connectorOutput,
          receivingPubkey: receiverPubKey,
          network: this.config.getNetwork(),
        });

      const signingNonceCommitment =
        await this.config.signer.getRandomSigningCommitment();
      const directSigningNonceCommitment =
        await this.config.signer.getRandomSigningCommitment();
      const directFromCpfpSigningNonceCommitment =
        await this.config.signer.getRandomSigningCommitment();
      const signingPublicKey =
        await this.config.signer.getPublicKeyFromDerivation(leaf.keyDerivation);

      const signingJob: LeafRefundTxSigningJob = {
        leafId: leaf.leaf.id,
        refundTxSigningJob: {
          signingPublicKey: await this.config.signer.getPublicKeyFromDerivation(
            leaf.keyDerivation,
          ),
          rawTx: cpfpRefundTx.toBytes(),
          signingNonceCommitment: signingNonceCommitment.commitment,
        },
        directRefundTxSigningJob: directRefundTx
          ? {
              signingPublicKey,
              rawTx: directRefundTx.toBytes(),
              signingNonceCommitment: directSigningNonceCommitment.commitment,
            }
          : undefined,
        directFromCpfpRefundTxSigningJob: directFromCpfpRefundTx
          ? {
              signingPublicKey,
              rawTx: directFromCpfpRefundTx.toBytes(),
              signingNonceCommitment:
                directFromCpfpSigningNonceCommitment.commitment,
            }
          : undefined,
      };

      signingJobs.push(signingJob);
      const tx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
      const directTx =
        leaf.leaf.directTx.length > 0
          ? getTxFromRawTxBytes(leaf.leaf.directTx)
          : undefined;

      leafDataMap.set(leaf.leaf.id, {
        keyDerivation: leaf.keyDerivation,
        receivingPubkey: receiverPubKey,
        signingNonceCommitment,
        directSigningNonceCommitment,
        tx,
        directTx,
        refundTx: cpfpRefundTx,
        directRefundTx: directRefundTx,
        directFromCpfpRefundTx: directFromCpfpRefundTx,
        directFromCpfpRefundSigningNonceCommitment:
          directFromCpfpSigningNonceCommitment,
        vout: leaf.leaf.vout,
      });
    }

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let response: CooperativeExitResponse;
    try {
      response = await sparkClient.cooperative_exit_v2({
        transfer: {
          transferId,
          leavesToSend: signingJobs,
          ownerIdentityPublicKey:
            await this.config.signer.getIdentityPublicKey(),
          receiverIdentityPublicKey: receiverPubKey,
          expiryTime:
            this.config.getNetwork() == Network.MAINNET
              ? new Date(Date.now() + 7 * 24 * 60 * 60 * 1000 + 5 * 60 * 1000)
              : new Date(Date.now() + 35 * 60 * 1000), // 1 week 5 min for mainnet, 35 min otherwise
        },
        exitId: uuidv7(),
        exitTxid: exitTxId,
      });
    } catch (error) {
      throw new NetworkError(
        "Failed to initiate cooperative exit",
        {
          operation: "cooperative_exit",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
    }

    if (!response.transfer) {
      throw new NetworkError("Failed to initiate cooperative exit", {
        operation: "cooperative_exit",
        errors: "No transfer in response",
      });
    }

    const signatures = await this.signRefunds(
      leafDataMap,
      response.signingResults,
    );

    const signaturesMap: Map<string, Uint8Array> = new Map();
    const directSignaturesMap: Map<string, Uint8Array> = new Map();
    const directFromCpfpSignaturesMap: Map<string, Uint8Array> = new Map();
    for (const signature of signatures) {
      signaturesMap.set(signature.nodeId, signature.refundTxSignature);
      directSignaturesMap.set(
        signature.nodeId,
        signature.directRefundTxSignature,
      );
      directFromCpfpSignaturesMap.set(
        signature.nodeId,
        signature.directFromCpfpRefundTxSignature,
      );
    }

    return {
      transfer: response.transfer,
      signaturesMap,
      directSignaturesMap,
      directFromCpfpSignaturesMap,
    };
  }
}
