import { Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { uuidv7 } from "uuidv7";
import { NetworkError, ValidationError } from "../errors/types.js";
import {
  CooperativeExitResponse,
  LeafRefundTxSigningJob,
  Transfer,
} from "../proto/spark.js";
import {
  getP2TRScriptFromPublicKey,
  getTxFromRawTxBytes,
} from "../utils/bitcoin.js";
import { Network } from "../utils/network.js";
import {
  getNextTransactionSequence,
  maybeApplyFee,
} from "../utils/transaction.js";
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

  private createConnectorRefundTransactions(
    sequence: number,
    directSequence: number,
    cpfpNodeOutPoint: TransactionInput,
    directNodeOutPoint: TransactionInput | undefined,
    connectorOutput: TransactionInput,
    amountSats: bigint,
    receiverPubKey: Uint8Array,
  ): {
    cpfpRefundTx: Transaction;
    directRefundTx?: Transaction;
    directFromCpfpRefundTx?: Transaction;
  } {
    // Create CPFP refund transaction
    const cpfpRefundTx = new Transaction();
    if (!cpfpNodeOutPoint.txid || cpfpNodeOutPoint.index === undefined) {
      throw new ValidationError("Invalid CPFP node outpoint", {
        field: "cpfpNodeOutPoint",
        value: { txid: cpfpNodeOutPoint.txid, index: cpfpNodeOutPoint.index },
        expected: "Both txid and index must be defined",
      });
    }
    cpfpRefundTx.addInput({
      txid: cpfpNodeOutPoint.txid,
      index: cpfpNodeOutPoint.index,
      sequence,
    });

    cpfpRefundTx.addInput(connectorOutput);
    const receiverScript = getP2TRScriptFromPublicKey(
      receiverPubKey,
      this.config.getNetwork(),
    );

    cpfpRefundTx.addOutput({
      script: receiverScript,
      amount: amountSats,
    });

    // Create direct refund transaction
    let directRefundTx: Transaction | undefined;
    let directFromCpfpRefundTx: Transaction | undefined;
    if (directNodeOutPoint) {
      if (!directNodeOutPoint.txid || directNodeOutPoint.index === undefined) {
        throw new ValidationError("Invalid direct node outpoint", {
          field: "directNodeOutPoint",
          value: {
            txid: directNodeOutPoint.txid,
            index: directNodeOutPoint.index,
          },
          expected: "Both txid and index must be defined",
        });
      }
      directRefundTx = new Transaction();
      directRefundTx.addInput({
        txid: directNodeOutPoint.txid,
        index: directNodeOutPoint.index,
        sequence: directSequence,
      });

      directRefundTx.addInput(connectorOutput);
      directRefundTx.addOutput({
        script: receiverScript,
        amount: maybeApplyFee(amountSats),
      });

      directFromCpfpRefundTx = new Transaction();
      directFromCpfpRefundTx.addInput({
        txid: cpfpNodeOutPoint.txid,
        index: cpfpNodeOutPoint.index,
        sequence: directSequence,
      });

      directFromCpfpRefundTx.addInput(connectorOutput);
      directFromCpfpRefundTx.addOutput({
        script: receiverScript,
        amount: maybeApplyFee(amountSats),
      });
    }

    return {
      cpfpRefundTx,
      directRefundTx,
      directFromCpfpRefundTx,
    };
  }

  private async signCoopExitRefunds(
    leaves: LeafKeyTweak[],
    exitTxId: Uint8Array,
    connectorOutputs: TransactionInput[],
    receiverPubKey: Uint8Array,
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
      const currentRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);

      const sequence = currentRefundTx.getInput(0).sequence;
      if (!sequence) {
        throw new ValidationError("Invalid refund transaction", {
          field: "sequence",
          value: currentRefundTx.getInput(0),
          expected: "Non-null sequence",
        });
      }
      const { nextSequence, nextDirectSequence } =
        getNextTransactionSequence(sequence);

      let currentDirectRefundTx: Transaction | undefined;
      if (leaf.leaf.directRefundTx.length > 0) {
        currentDirectRefundTx = getTxFromRawTxBytes(leaf.leaf.directRefundTx);
      }

      const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
        this.createConnectorRefundTransactions(
          nextSequence,
          nextDirectSequence,
          currentRefundTx.getInput(0),
          currentDirectRefundTx?.getInput(0),
          connectorOutput,
          BigInt(leaf.leaf.value),
          receiverPubKey,
        );

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
          transferId: uuidv7(),
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
