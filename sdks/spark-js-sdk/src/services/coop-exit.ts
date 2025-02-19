import { Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { randomUUID } from "crypto";
import {
  CooperativeExitResponse,
  LeafRefundTxSigningJob,
  Transfer,
} from "../proto/spark";
import {
  getP2TRScriptFromPublicKey,
  getTxFromRawTxBytes,
} from "../utils/bitcoin";
import { getNextTransactionSequence } from "../utils/transaction";
import { WalletConfigService } from "./config";
import { ConnectionManager } from "./connection";
import {
  BaseTransferService,
  LeafKeyTweak,
  LeafRefundSigningData,
} from "./transfer";

const TIME_LOCK_INTERVAL = 100;

export type GetConnectorRefundSignaturesParams = {
  leaves: LeafKeyTweak[];
  exitTxId: Uint8Array;
  connectorOutputs: TransactionInput[];
  receiverPubKey: Uint8Array;
};

export class CoopExitService extends BaseTransferService {
  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager
  ) {
    super(config, connectionManager);
  }

  async getConnectorRefundSignatures({
    leaves,
    exitTxId,
    connectorOutputs,
    receiverPubKey,
  }: GetConnectorRefundSignaturesParams): Promise<{
    transfer: Transfer;
    signaturesMap: Map<string, Uint8Array>;
  }> {
    const { transfer, signaturesMap } = await this.signCoopExitRefunds(
      leaves,
      exitTxId,
      connectorOutputs,
      receiverPubKey
    );
    const transferTweak = await this.sendTransferTweakKey(
      transfer,
      leaves,
      signaturesMap
    );

    return { transfer: transferTweak, signaturesMap };
  }

  private createConnectorRefundTransaction(
    sequence: number,
    nodeOutPoint: TransactionInput,
    connectorOutput: TransactionInput,
    amountSats: bigint,
    receiverPubKey: Uint8Array
  ): Transaction {
    const refundTx = new Transaction();
    if (!nodeOutPoint.txid || nodeOutPoint.index === undefined) {
      throw new Error("Node outpoint txid or index is undefined");
    }
    refundTx.addInput({
      txid: nodeOutPoint.txid,
      index: nodeOutPoint.index,
      sequence,
    });

    refundTx.addInput(connectorOutput);
    const receiverScript = getP2TRScriptFromPublicKey(
      receiverPubKey,
      this.config.getNetwork()
    );

    refundTx.addOutput({
      script: receiverScript,
      amount: amountSats,
    });

    return refundTx;
  }
  private async signCoopExitRefunds(
    leaves: LeafKeyTweak[],
    exitTxId: Uint8Array,
    connectorOutputs: TransactionInput[],
    receiverPubKey: Uint8Array
  ): Promise<{ transfer: Transfer; signaturesMap: Map<string, Uint8Array> }> {
    if (leaves.length !== connectorOutputs.length) {
      throw new Error("Number of leaves and connector outputs must match");
    }

    const signingJobs: LeafRefundTxSigningJob[] = [];
    const leafDataMap: Map<string, LeafRefundSigningData> = new Map();

    for (let i = 0; i < leaves.length; i++) {
      const leaf = leaves[i];
      const connectorOutput = connectorOutputs[i];
      const currentRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);

      const sequence = getNextTransactionSequence(
        currentRefundTx.getInput(0).sequence
      );

      const refundTx = this.createConnectorRefundTransaction(
        sequence,
        currentRefundTx.getInput(0),
        connectorOutput,
        BigInt(leaf.leaf.value),
        receiverPubKey
      );

      const signingNonceCommitment =
        await this.config.signer.getRandomSigningCommitment();
      const signingJob: LeafRefundTxSigningJob = {
        leafId: leaf.leaf.id,
        refundTxSigningJob: {
          signingPublicKey: leaf.signingPubKey,
          rawTx: refundTx.toBytes(),
          signingNonceCommitment: signingNonceCommitment,
        },
      };

      signingJobs.push(signingJob);
      const tx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
      leafDataMap.set(leaf.leaf.id, {
        signingPubKey: leaf.signingPubKey,
        refundTx,
        signingNonceCommitment,
        tx,
        vout: leaf.leaf.vout,
        receivingPubkey: receiverPubKey,
      });
    }

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );

    let response: CooperativeExitResponse;
    try {
      response = await sparkClient.cooperative_exit({
        transfer: {
          transferId: randomUUID(),
          leavesToSend: signingJobs,
          ownerIdentityPublicKey:
            await this.config.signer.getIdentityPublicKey(),
          receiverIdentityPublicKey: receiverPubKey,
          expiryTime: new Date(Date.now() + 24 * 60 * 1000),
        },
        exitId: randomUUID(),
        exitTxid: exitTxId,
      });
    } catch (error) {
      throw new Error(`Error initiating cooperative exit: ${error}`);
    } finally {
      sparkClient.close?.();
    }

    if (!response.transfer) {
      throw new Error("Failed to initiate cooperative exit");
    }

    const signatures = await this.signRefunds(
      leafDataMap,
      response.signingResults
    );

    const signaturesMap: Map<string, Uint8Array> = new Map();
    for (const signature of signatures) {
      signaturesMap.set(signature.nodeId, signature.refundTxSignature);
    }

    return { transfer: response.transfer, signaturesMap };
  }
}
