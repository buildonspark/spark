import {
  bytesToHex,
  equalBytes,
  hexToBytes,
  numberToBytesBE,
} from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { sha256 } from "@scure/btc-signer/utils";
import * as ecies from "eciesjs";
import { SignatureIntent } from "../proto/common.js";
import {
  ClaimLeafKeyTweak,
  ClaimTransferSignRefundsResponse,
  CompleteSendTransferResponse,
  LeafRefundTxSigningJob,
  LeafRefundTxSigningResult,
  CounterLeafSwapResponse,
  NodeSignatures,
  QueryTransfersResponse,
  SendLeafKeyTweak,
  SigningJob,
  StartSendTransferResponse,
  Transfer,
  TransferStatus,
  TreeNode,
} from "../proto/spark.js";
import { SigningCommitment } from "../signer/signer.js";
import {
  getSigHashFromTx,
  getTxFromRawTxBytes,
  getTxId,
} from "../utils/bitcoin.js";
import { getCrypto } from "../utils/crypto.js";
import { VerifiableSecretShare } from "../utils/secret-sharing.js";
import {
  createRefundTx,
  getEphemeralAnchorOutput,
  getNextTransactionSequence,
} from "../utils/transaction.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";
const INITIAL_TIME_LOCK = 2000;

const DEFAULT_EXPIRY_TIME = 10 * 60 * 1000;

function initialSequence() {
  return (1 << 30) | INITIAL_TIME_LOCK;
}

const crypto = getCrypto();

export type LeafKeyTweak = {
  leaf: TreeNode;
  signingPubKey: Uint8Array;
  newSigningPubKey: Uint8Array;
};

export type ClaimLeafData = {
  signingPubKey: Uint8Array;
  tx?: Transaction;
  refundTx?: Transaction;
  signingNonceCommitment: SigningCommitment;
  vout?: number;
};

export type LeafRefundSigningData = {
  signingPubKey: Uint8Array;
  receivingPubkey: Uint8Array;
  tx: Transaction;
  refundTx?: Transaction;
  signingNonceCommitment: SigningCommitment;
  vout: number;
};

export class BaseTransferService {
  protected readonly config: WalletConfigService;
  protected readonly connectionManager: ConnectionManager;

  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager,
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
  }

  async sendTransferTweakKey(
    transfer: Transfer,
    leaves: LeafKeyTweak[],
    refundSignatureMap: Map<string, Uint8Array>,
  ): Promise<Transfer> {
    const keyTweakInputMap = await this.prepareSendTransferKeyTweaks(
      transfer,
      leaves,
      refundSignatureMap,
    );

    let updatedTransfer: Transfer | undefined;
    const errors: Error[] = [];
    const promises = Object.entries(this.config.getSigningOperators()).map(
      async ([identifier, operator]) => {
        const sparkClient = await this.connectionManager.createSparkClient(
          operator.address,
        );

        const leavesToSend = keyTweakInputMap.get(identifier);
        if (!leavesToSend) {
          errors.push(
            new Error(`No leaves to send for operator ${identifier}`),
          );
          return;
        }
        let transferResp: CompleteSendTransferResponse;
        try {
          transferResp = await sparkClient.complete_send_transfer({
            transferId: transfer.id,
            ownerIdentityPublicKey:
              await this.config.signer.getIdentityPublicKey(),
            leavesToSend,
          });
        } catch (error) {
          errors.push(new Error(`Error completing send transfer: ${error}`));
          return;
        }

        if (!updatedTransfer) {
          updatedTransfer = transferResp.transfer;
        } else {
          if (!transferResp.transfer) {
            errors.push(
              new Error(`No transfer response from operator ${identifier}`),
            );
            return;
          }

          if (!this.compareTransfers(updatedTransfer, transferResp.transfer)) {
            errors.push(
              new Error(`Inconsistent transfer response from operators`),
            );
          }
        }
      },
    );

    await Promise.all(promises);

    if (errors.length > 0) {
      throw new Error(`Error completing send transfer: ${errors[0]}`);
    }

    if (!updatedTransfer) {
      throw new Error("No updated transfer found");
    }

    return updatedTransfer;
  }

  async signRefunds(
    leafDataMap: Map<string, ClaimLeafData>,
    operatorSigningResults: LeafRefundTxSigningResult[],
    adaptorPubKey?: Uint8Array,
  ): Promise<NodeSignatures[]> {
    const nodeSignatures: NodeSignatures[] = [];
    for (const operatorSigningResult of operatorSigningResults) {
      const leafData = leafDataMap.get(operatorSigningResult.leafId);
      if (
        !leafData ||
        !leafData.tx ||
        leafData.vout === undefined ||
        !leafData.refundTx
      ) {
        throw new Error(
          `Leaf data not found for leaf ${operatorSigningResult.leafId}`,
        );
      }

      const txOutput = leafData.tx?.getOutput(0);
      if (!txOutput) {
        throw new Error(
          `Output not found for leaf ${operatorSigningResult.leafId}`,
        );
      }

      const refundTxSighash = getSigHashFromTx(leafData.refundTx, 0, txOutput);

      const userSignature = await this.config.signer.signFrost({
        message: refundTxSighash,
        publicKey: leafData.signingPubKey,
        privateAsPubKey: leafData.signingPubKey,
        selfCommitment: leafData.signingNonceCommitment,
        statechainCommitments:
          operatorSigningResult.refundTxSigningResult?.signingNonceCommitments,
        adaptorPubKey: adaptorPubKey,
        verifyingKey: operatorSigningResult.verifyingKey,
      });

      const refundAggregate = await this.config.signer.aggregateFrost({
        message: refundTxSighash,
        statechainSignatures:
          operatorSigningResult.refundTxSigningResult?.signatureShares,
        statechainPublicKeys:
          operatorSigningResult.refundTxSigningResult?.publicKeys,
        verifyingKey: operatorSigningResult.verifyingKey,
        statechainCommitments:
          operatorSigningResult.refundTxSigningResult?.signingNonceCommitments,
        selfCommitment: leafData.signingNonceCommitment,
        publicKey: leafData.signingPubKey,
        selfSignature: userSignature,
        adaptorPubKey: adaptorPubKey,
      });

      nodeSignatures.push({
        nodeId: operatorSigningResult.leafId,
        refundTxSignature: refundAggregate,
        nodeTxSignature: new Uint8Array(),
      });
    }

    return nodeSignatures;
  }

  private async prepareSendTransferKeyTweaks(
    transfer: Transfer,
    leaves: LeafKeyTweak[],
    refundSignatureMap: Map<string, Uint8Array>,
  ): Promise<Map<string, SendLeafKeyTweak[]>> {
    const receiverEciesPubKey = ecies.PublicKey.fromHex(
      bytesToHex(transfer.receiverIdentityPublicKey),
    );

    const leavesTweaksMap = new Map<string, SendLeafKeyTweak[]>();

    for (const leaf of leaves) {
      const refundSignature = refundSignatureMap.get(leaf.leaf.id);
      const leafTweaksMap = await this.prepareSingleSendTransferKeyTweak(
        transfer.id,
        leaf,
        receiverEciesPubKey,
        refundSignature,
      );
      for (const [identifier, leafTweak] of leafTweaksMap) {
        leavesTweaksMap.set(identifier, [
          ...(leavesTweaksMap.get(identifier) || []),
          leafTweak,
        ]);
      }
    }

    return leavesTweaksMap;
  }

  private async prepareSingleSendTransferKeyTweak(
    transferID: string,
    leaf: LeafKeyTweak,
    receiverEciesPubKey: ecies.PublicKey,
    refundSignature?: Uint8Array,
  ): Promise<Map<string, SendLeafKeyTweak>> {
    const signingOperators = this.config.getSigningOperators();
    const pubKeyTweak =
      await this.config.signer.subtractPrivateKeysGivenPublicKeys(
        leaf.signingPubKey,
        leaf.newSigningPubKey,
      );

    const shares = await this.config.signer.splitSecretWithProofs({
      secret: pubKeyTweak,
      curveOrder: secp256k1.CURVE.n,
      threshold: this.config.getThreshold(),
      numShares: Object.keys(signingOperators).length,
      isSecretPubkey: true,
    });

    const pubkeySharesTweak = new Map<string, Uint8Array>();
    for (const [identifier, operator] of Object.entries(signingOperators)) {
      const share = this.findShare(shares, operator.id);
      if (!share) {
        throw new Error(`Share not found for operator ${operator.id}`);
      }

      const pubkeyTweak = secp256k1.getPublicKey(
        numberToBytesBE(share.share, 32),
        true,
      );
      pubkeySharesTweak.set(identifier, pubkeyTweak);
    }

    const secretCipher = await this.config.signer.encryptLeafPrivateKeyEcies(
      receiverEciesPubKey.toBytes(),
      leaf.newSigningPubKey,
    );

    const encoder = new TextEncoder();
    const payload = new Uint8Array([
      ...encoder.encode(leaf.leaf.id),
      ...encoder.encode(transferID),
      ...secretCipher,
    ]);

    const payloadHash = sha256(payload);
    const signature = await this.config.signer.signMessageWithIdentityKey(
      payloadHash,
      true,
    );

    const leafTweaksMap = new Map<string, SendLeafKeyTweak>();
    for (const [identifier, operator] of Object.entries(signingOperators)) {
      const share = this.findShare(shares, operator.id);
      if (!share) {
        throw new Error(`Share not found for operator ${operator.id}`);
      }

      leafTweaksMap.set(identifier, {
        leafId: leaf.leaf.id,
        secretShareTweak: {
          secretShare: numberToBytesBE(share.share, 32),
          proofs: share.proofs,
        },
        pubkeySharesTweak: Object.fromEntries(pubkeySharesTweak),
        secretCipher,
        signature,
        refundSignature: refundSignature ?? new Uint8Array(),
      });
    }

    return leafTweaksMap;
  }

  protected findShare(shares: VerifiableSecretShare[], operatorID: number) {
    const targetShareIndex = BigInt(operatorID + 1);
    for (const s of shares) {
      if (s.index === targetShareIndex) {
        return s;
      }
    }
    return undefined;
  }

  private compareTransfers(transfer1: Transfer, transfer2: Transfer) {
    return (
      transfer1.id === transfer2.id &&
      equalBytes(
        transfer1.senderIdentityPublicKey,
        transfer2.senderIdentityPublicKey,
      ) &&
      transfer1.status === transfer2.status &&
      transfer1.totalValue === transfer2.totalValue &&
      transfer1.expiryTime?.getTime() === transfer2.expiryTime?.getTime() &&
      transfer1.leaves.length === transfer2.leaves.length
    );
  }
}

export class TransferService extends BaseTransferService {
  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager,
  ) {
    super(config, connectionManager);
  }

  async sendTransfer(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
  ): Promise<Transfer> {
    const { transfer, signatureMap } = await this.sendTransferSignRefund(
      leaves,
      receiverIdentityPubkey,
      new Date(Date.now() + DEFAULT_EXPIRY_TIME),
    );

    const transferWithTweakedKeys = await this.sendTransferTweakKey(
      transfer,
      leaves,
      signatureMap,
    );

    return transferWithTweakedKeys;
  }

  async claimTransfer(transfer: Transfer, leaves: LeafKeyTweak[]) {
    if (transfer.status === TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED) {
      await this.claimTransferTweakKeys(transfer, leaves);
    }
    const signatures = await this.claimTransferSignRefunds(transfer, leaves);

    return await this.finalizeTransfer(signatures);
  }

  async queryPendingTransfers(): Promise<QueryTransfersResponse> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    let pendingTransfersResp: QueryTransfersResponse;
    try {
      pendingTransfersResp = await sparkClient.query_pending_transfers({
        participant: {
          $case: "receiverIdentityPublicKey",
          receiverIdentityPublicKey:
            await this.config.signer.getIdentityPublicKey(),
        },
      });
    } catch (error) {
      throw new Error(`Error querying pending transfers: ${error}`);
    }
    return pendingTransfersResp;
  }

  async queryAllTransfers(
    limit: number,
    offset: number,
  ): Promise<QueryTransfersResponse> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let allTransfersResp: QueryTransfersResponse;
    try {
      allTransfersResp = await sparkClient.query_all_transfers({
        participant: {
          $case: "receiverIdentityPublicKey",
          receiverIdentityPublicKey:
            await this.config.signer.getIdentityPublicKey(),
        },
        limit,
        offset,
      });
    } catch (error) {
      throw new Error(`Error querying all transfers: ${error}`);
    }
    return allTransfersResp;
  }

  async verifyPendingTransfer(
    transfer: Transfer,
  ): Promise<Map<string, Uint8Array>> {
    const leafPubKeyMap = new Map<string, Uint8Array>();
    for (const leaf of transfer.leaves) {
      if (!leaf.leaf) {
        throw new Error("Leaf is undefined");
      }
      const encoder = new TextEncoder();
      const leafIdBytes = encoder.encode(leaf.leaf.id);
      const transferIdBytes = encoder.encode(transfer.id);
      const payload = new Uint8Array([
        ...leafIdBytes,
        ...transferIdBytes,
        ...leaf.secretCipher,
      ]);
      const payloadHash = sha256(payload);
      if (
        !secp256k1.verify(
          leaf.signature,
          payloadHash,
          transfer.senderIdentityPublicKey,
        )
      ) {
        throw new Error("Signature verification failed");
      }

      const leafSecret = await this.config.signer.decryptEcies(
        leaf.secretCipher,
      );

      leafPubKeyMap.set(leaf.leaf.id, leafSecret);
    }
    return leafPubKeyMap;
  }

  async sendTransferSignRefund(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date,
  ): Promise<{
    transfer: Transfer;
    signatureMap: Map<string, Uint8Array>;
    leafDataMap: Map<string, LeafRefundSigningData>;
  }> {
    const { transfer, signatureMap, leafDataMap } =
      await this.sendTransferSignRefundInternal(
        leaves,
        receiverIdentityPubkey,
        expiryTime,
        false,
      );

    return {
      transfer,
      signatureMap,
      leafDataMap,
    };
  }

  async startSwapSignRefund(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date,
  ): Promise<{
    transfer: Transfer;
    signatureMap: Map<string, Uint8Array>;
    leafDataMap: Map<string, LeafRefundSigningData>;
  }> {
    const { transfer, signatureMap, leafDataMap } =
      await this.sendTransferSignRefundInternal(
        leaves,
        receiverIdentityPubkey,
        expiryTime,
        true,
      );

    return {
      transfer,
      signatureMap,
      leafDataMap,
    };
  }

  async counterSwapSignRefund(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date,
    adaptorPubKey?: Uint8Array,
  ): Promise<{
    transfer: Transfer;
    signatureMap: Map<string, Uint8Array>;
    leafDataMap: Map<string, LeafRefundSigningData>;
    signingResults: LeafRefundTxSigningResult[];
  }> {
    return this.sendTransferSignRefundInternal(
      leaves,
      receiverIdentityPubkey,
      expiryTime,
      true,
      adaptorPubKey,
    );
  }

  async sendTransferSignRefundInternal(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date,
    forSwap: boolean,
    adaptorPubKey?: Uint8Array,
  ): Promise<{
    transfer: Transfer;
    signatureMap: Map<string, Uint8Array>;
    leafDataMap: Map<string, LeafRefundSigningData>;
    signingResults: LeafRefundTxSigningResult[];
  }> {
    const transferId = crypto.randomUUID();

    const leafDataMap = new Map<string, LeafRefundSigningData>();
    for (const leaf of leaves) {
      const signingNonceCommitment =
        await this.config.signer.getRandomSigningCommitment();

      const tx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
      const refundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);
      leafDataMap.set(leaf.leaf.id, {
        signingPubKey: leaf.signingPubKey,
        receivingPubkey: receiverIdentityPubkey,
        signingNonceCommitment,
        tx,
        refundTx,
        vout: leaf.leaf.vout,
      });
    }

    const signingJobs = this.prepareRefundSoSigningJobs(leaves, leafDataMap);

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let response: CounterLeafSwapResponse;
    try {
      if (adaptorPubKey !== undefined) {
        response = await sparkClient.counter_leaf_swap({
          transfer: {
            transferId,
            leavesToSend: signingJobs,
            ownerIdentityPublicKey:
              await this.config.signer.getIdentityPublicKey(),
            receiverIdentityPublicKey: receiverIdentityPubkey,
            expiryTime: expiryTime,
          },
          swapId: crypto.randomUUID(),
          adaptorPublicKey: adaptorPubKey || new Uint8Array(),
        });
      } else if (forSwap) {
        response = await sparkClient.start_leaf_swap({
          transferId,
          leavesToSend: signingJobs,
          ownerIdentityPublicKey:
            await this.config.signer.getIdentityPublicKey(),
          receiverIdentityPublicKey: receiverIdentityPubkey,
          expiryTime: expiryTime,
        });
      } else {
        response = await sparkClient.start_send_transfer({
          transferId,
          leavesToSend: signingJobs,
          ownerIdentityPublicKey:
            await this.config.signer.getIdentityPublicKey(),
          receiverIdentityPublicKey: receiverIdentityPubkey,
          expiryTime: expiryTime,
        });
      }
    } catch (error) {
      throw new Error(`Error starting send transfer: ${error}`);
    }

    if (!response.transfer) {
      throw new Error("No transfer response from coordinator");
    }

    const signatures = await this.signRefunds(
      leafDataMap,
      response.signingResults,
      adaptorPubKey,
    );

    const signatureMap = new Map<string, Uint8Array>();
    for (const signature of signatures) {
      signatureMap.set(signature.nodeId, signature.refundTxSignature);
    }

    return {
      transfer: response.transfer,
      signatureMap,
      leafDataMap,
      signingResults: response.signingResults,
    };
  }

  private prepareRefundSoSigningJobs(
    leaves: LeafKeyTweak[],
    leafDataMap: Map<string, LeafRefundSigningData>,
  ): LeafRefundTxSigningJob[] {
    const signingJobs: LeafRefundTxSigningJob[] = [];
    for (const leaf of leaves) {
      const refundSigningData = leafDataMap.get(leaf.leaf.id);
      if (!refundSigningData) {
        throw new Error(`Leaf data not found for leaf ${leaf.leaf.id}`);
      }

      const nodeTx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
      const nodeOutPoint: TransactionInput = {
        txid: hexToBytes(getTxId(nodeTx)),
        index: 0,
      };

      const currRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);
      const { nextSequence } = getNextTransactionSequence(
        currRefundTx.getInput(0).sequence,
      );
      const amountSats = currRefundTx.getOutput(0).amount;
      if (amountSats === undefined) {
        throw new Error("Amount not found in signRefunds");
      }

      const refundTx = createRefundTx(
        nextSequence,
        nodeOutPoint,
        amountSats,
        refundSigningData.receivingPubkey,
        this.config.getNetwork(),
      );

      refundSigningData.refundTx = refundTx;

      const refundNonceCommitmentProto =
        refundSigningData.signingNonceCommitment;

      signingJobs.push({
        leafId: leaf.leaf.id,
        refundTxSigningJob: {
          signingPublicKey: refundSigningData.signingPubKey,
          rawTx: refundTx.toBytes(),
          signingNonceCommitment: refundNonceCommitmentProto,
        },
      });
    }

    return signingJobs;
  }

  async claimTransferTweakKeys(transfer: Transfer, leaves: LeafKeyTweak[]) {
    const leavesTweaksMap = await this.prepareClaimLeavesKeyTweaks(leaves);

    const errors: Error[] = [];

    const promises = Object.entries(this.config.getSigningOperators()).map(
      async ([identifier, operator]) => {
        const sparkClient = await this.connectionManager.createSparkClient(
          operator.address,
        );

        const leavesToReceive = leavesTweaksMap.get(identifier);
        if (!leavesToReceive) {
          errors.push(
            new Error(`No leaves to receive for operator ${identifier}`),
          );
          return;
        }

        try {
          await sparkClient.claim_transfer_tweak_keys({
            transferId: transfer.id,
            ownerIdentityPublicKey:
              await this.config.signer.getIdentityPublicKey(),
            leavesToReceive,
          });
        } catch (error) {
          errors.push(
            new Error(`Error claiming transfer tweak keys: ${error}`),
          );
          return;
        }
      },
    );

    await Promise.all(promises);

    if (errors.length > 0) {
      throw new Error(`Error claiming transfer tweak keys: ${errors[0]}`);
    }
  }

  private async prepareClaimLeavesKeyTweaks(
    leaves: LeafKeyTweak[],
  ): Promise<Map<string, ClaimLeafKeyTweak[]>> {
    const leafDataMap = new Map<string, ClaimLeafKeyTweak[]>();
    for (const leaf of leaves) {
      const leafData = await this.prepareClaimLeafKeyTweaks(leaf);
      for (const [identifier, leafTweak] of leafData) {
        leafDataMap.set(identifier, [
          ...(leafDataMap.get(identifier) || []),
          leafTweak,
        ]);
      }
    }
    return leafDataMap;
  }

  private async prepareClaimLeafKeyTweaks(
    leaf: LeafKeyTweak,
  ): Promise<Map<string, ClaimLeafKeyTweak>> {
    const signingOperators = this.config.getSigningOperators();

    const pubKeyTweak =
      await this.config.signer.subtractPrivateKeysGivenPublicKeys(
        leaf.signingPubKey,
        leaf.newSigningPubKey,
      );

    const shares = await this.config.signer.splitSecretWithProofs({
      secret: pubKeyTweak,
      curveOrder: secp256k1.CURVE.n,
      threshold: this.config.getThreshold(),
      numShares: Object.keys(signingOperators).length,
      isSecretPubkey: true,
    });

    const pubkeySharesTweak = new Map<string, Uint8Array>();

    for (const [identifier, operator] of Object.entries(signingOperators)) {
      const share = this.findShare(shares, operator.id);
      if (!share) {
        throw new Error(`Share not found for operator ${operator.id}`);
      }
      const pubkeyTweak = secp256k1.getPublicKey(
        numberToBytesBE(share.share, 32),
      );
      pubkeySharesTweak.set(identifier, pubkeyTweak);
    }

    const leafTweaksMap = new Map<string, ClaimLeafKeyTweak>();
    for (const [identifier, operator] of Object.entries(signingOperators)) {
      const share = this.findShare(shares, operator.id);
      if (!share) {
        throw new Error(`Share not found for operator ${operator.id}`);
      }

      leafTweaksMap.set(identifier, {
        leafId: leaf.leaf.id,
        secretShareTweak: {
          secretShare: numberToBytesBE(share.share, 32),
          proofs: share.proofs,
        },
        pubkeySharesTweak: Object.fromEntries(pubkeySharesTweak),
      });
    }

    return leafTweaksMap;
  }

  async claimTransferSignRefunds(
    transfer: Transfer,
    leafKeys: LeafKeyTweak[],
  ): Promise<NodeSignatures[]> {
    const leafDataMap: Map<string, LeafRefundSigningData> = new Map();
    for (const leafKey of leafKeys) {
      const tx = getTxFromRawTxBytes(leafKey.leaf.nodeTx);
      leafDataMap.set(leafKey.leaf.id, {
        signingPubKey: leafKey.newSigningPubKey,
        receivingPubkey: leafKey.newSigningPubKey,
        signingNonceCommitment:
          await this.config.signer.getRandomSigningCommitment(),
        tx,
        vout: leafKey.leaf.vout,
      });
    }

    const signingJobs = this.prepareRefundSoSigningJobs(leafKeys, leafDataMap);

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    let resp: ClaimTransferSignRefundsResponse;
    try {
      resp = await sparkClient.claim_transfer_sign_refunds({
        transferId: transfer.id,
        ownerIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
        signingJobs,
      });
    } catch (error) {
      throw new Error(`Error claiming transfer sign refunds: ${error}`);
    }
    return this.signRefunds(leafDataMap, resp.signingResults);
  }

  private async finalizeTransfer(nodeSignatures: NodeSignatures[]) {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    try {
      return await sparkClient.finalize_node_signatures({
        intent: SignatureIntent.TRANSFER,
        nodeSignatures,
      });
    } catch (error) {
      throw new Error(`Error finalizing node signatures in transfer: ${error}`);
    }
  }

  async cancelSendTransfer(
    transfer: Transfer,
    operatorAddress: string,
  ): Promise<Transfer | undefined> {
    const sparkClient =
      await this.connectionManager.createSparkClient(operatorAddress);

    try {
      const response = await sparkClient.cancel_send_transfer({
        transferId: transfer.id,
        senderIdentityPublicKey:
          await this.config.signer.getIdentityPublicKey(),
      });

      return response.transfer;
    } catch (error) {
      throw new Error(`Error canceling send transfer: ${error}`);
    }
  }

  async queryPendingTransfersBySender(
    operatorAddress: string,
  ): Promise<QueryTransfersResponse> {
    const sparkClient =
      await this.connectionManager.createSparkClient(operatorAddress);
    try {
      return await sparkClient.query_pending_transfers({
        participant: {
          $case: "senderIdentityPublicKey",
          senderIdentityPublicKey:
            await this.config.signer.getIdentityPublicKey(),
        },
      });
    } catch (error) {
      throw new Error(`Error querying pending transfers by sender: ${error}`);
    }
  }

  async refreshTimelockNodes(
    nodes: TreeNode[],
    parentNode: TreeNode,
    signingPubKey: Uint8Array,
  ) {
    if (nodes.length === 0) {
      throw Error("no nodes to refresh");
    }

    const signingJobs: SigningJob[] = [];
    const newNodeTxs: Transaction[] = [];

    for (let i = 0; i < nodes.length; i++) {
      const node = nodes[i];
      if (!node) {
        throw Error("could not get node");
      }
      const nodeTx = getTxFromRawTxBytes(node?.nodeTx);
      const input = nodeTx.getInput(0);

      if (!input) {
        throw Error("Could not fetch tx input");
      }

      const newTx = new Transaction({ allowUnknownOutputs: true });
      for (let j = 0; j < nodeTx.outputsLength; j++) {
        newTx.addOutput(nodeTx.getOutput(j));
      }
      if (i === 0) {
        const currSequence = input.sequence;

        newTx.addInput({
          ...input,
          sequence: getNextTransactionSequence(currSequence).nextSequence,
        });
      } else {
        newTx.addInput({
          ...input,
          sequence: initialSequence(),
          txid: newNodeTxs[i - 1]?.id,
        });
      }

      signingJobs.push({
        signingPublicKey: signingPubKey,
        rawTx: newTx.toBytes(),
        signingNonceCommitment:
          await this.config.signer.getRandomSigningCommitment(),
      });
      newNodeTxs[i] = newTx;
    }

    const leaf = nodes[nodes.length - 1];
    if (!leaf?.refundTx) {
      throw Error("leaf does not have refund tx");
    }
    const refundTx = getTxFromRawTxBytes(leaf?.refundTx);
    const newRefundTx = new Transaction({ allowUnknownOutputs: true });

    for (let j = 0; j < refundTx.outputsLength; j++) {
      newRefundTx.addOutput(refundTx.getOutput(j));
    }

    const refundTxInput = refundTx.getInput(0);
    if (!refundTxInput) {
      throw Error("refund tx doesn't have input");
    }

    if (!newNodeTxs[newNodeTxs.length - 1]) {
      throw Error("Could not get last node tx");
    }
    newRefundTx.addInput({
      ...refundTxInput,
      sequence: initialSequence(),
      txid: getTxId(newNodeTxs[newNodeTxs.length - 1]!),
    });

    const refundSigningJob = {
      signingPublicKey: signingPubKey,
      rawTx: newRefundTx.toBytes(),
      signingNonceCommitment:
        await this.config.signer.getRandomSigningCommitment(),
    };

    signingJobs.push(refundSigningJob);

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const response = await sparkClient.refresh_timelock({
      leafId: leaf.id,
      ownerIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
      signingJobs,
    });

    if (signingJobs.length !== response.signingResults.length) {
      throw Error(
        `number of signing jobs and signing results do not match: ${signingJobs.length} !== ${response.signingResults.length}`,
      );
    }

    let nodeSignatures: NodeSignatures[] = [];
    let leafSignature: Uint8Array | undefined;
    let refundSignature: Uint8Array | undefined;
    let leafNodeId: string | undefined;
    for (let i = 0; i < response.signingResults.length; i++) {
      const signingResult = response.signingResults[i];
      const signingJob = signingJobs[i];
      if (!signingJob || !signingResult) {
        throw Error("Signing job does not exist");
      }

      if (!signingJob.signingNonceCommitment) {
        throw Error("nonce commitment does not exist");
      }
      const rawTx = getTxFromRawTxBytes(signingJob.rawTx);

      let parentTx: Transaction | undefined;
      let nodeId: string | undefined;
      let vout: number | undefined;

      if (i === nodes.length) {
        nodeId = nodes[i - 1]?.id;
        parentTx = newNodeTxs[i - 1];
        vout = 0;
      } else if (i === 0) {
        nodeId = nodes[i]?.id;
        parentTx = getTxFromRawTxBytes(parentNode.nodeTx);
        vout = nodes[i]?.vout;
      } else {
        nodeId = nodes[i]?.id;
        parentTx = newNodeTxs[i - 1];
        vout = nodes[i]?.vout;
      }

      if (!parentTx || !nodeId || vout === undefined) {
        throw Error("Could not parse signing results");
      }

      const txOut = parentTx.getOutput(vout);

      const rawTxSighash = getSigHashFromTx(rawTx, 0, txOut);

      const userSignature = await this.config.signer.signFrost({
        message: rawTxSighash,
        privateAsPubKey: signingPubKey,
        publicKey: signingPubKey,
        verifyingKey: signingResult.verifyingKey,
        selfCommitment: signingJob.signingNonceCommitment,
        statechainCommitments:
          signingResult.signingResult?.signingNonceCommitments,
        adaptorPubKey: new Uint8Array(),
      });

      const signature = await this.config.signer.aggregateFrost({
        message: rawTxSighash,
        statechainSignatures: signingResult.signingResult?.signatureShares,
        statechainPublicKeys: signingResult.signingResult?.publicKeys,
        verifyingKey: signingResult.verifyingKey,
        statechainCommitments:
          signingResult.signingResult?.signingNonceCommitments,
        selfCommitment: signingJob.signingNonceCommitment,
        publicKey: signingPubKey,
        selfSignature: userSignature,
        adaptorPubKey: new Uint8Array(),
      });

      if (i !== nodes.length && i !== nodes.length - 1) {
        nodeSignatures.push({
          nodeId: nodeId,
          nodeTxSignature: signature,
          refundTxSignature: new Uint8Array(),
        });
      } else if (i === nodes.length) {
        refundSignature = signature;
      } else if (i === nodes.length - 1) {
        leafNodeId = nodeId;
        leafSignature = signature;
      }
    }

    if (!leafSignature || !refundSignature || !leafNodeId) {
      throw Error("leaf or refund signature does not exist");
    }

    nodeSignatures.push({
      nodeId: leafNodeId,
      nodeTxSignature: leafSignature,
      refundTxSignature: refundSignature,
    });

    return await sparkClient.finalize_node_signatures({
      intent: SignatureIntent.REFRESH,
      nodeSignatures,
    });
  }

  async extendTimelock(node: TreeNode, signingPubKey: Uint8Array) {
    const nodeTx = getTxFromRawTxBytes(node.nodeTx);
    const refundTx = getTxFromRawTxBytes(node.refundTx);

    const refundSequence = refundTx.getInput(0).sequence || 0;
    const newNodeOutPoint: TransactionInput = {
      txid: hexToBytes(nodeTx.id),
      index: 0,
    };

    const { nextSequence: newNodeSequence } =
      getNextTransactionSequence(refundSequence);
    const newNodeTx = new Transaction({ allowUnknownOutputs: true });
    newNodeTx.addInput({ ...newNodeOutPoint, sequence: newNodeSequence });
    newNodeTx.addOutput(nodeTx.getOutput(0));
    newNodeTx.addOutput(getEphemeralAnchorOutput());

    const newRefundOutPoint: TransactionInput = {
      txid: hexToBytes(getTxId(newNodeTx)!),
      index: 0,
    };

    const amountSats = refundTx.getOutput(0).amount;
    if (amountSats === undefined) {
      throw new Error("Amount not found in extendTimelock");
    }

    const newRefundTx = createRefundTx(
      initialSequence(),
      newRefundOutPoint,
      amountSats,
      signingPubKey,
      this.config.getNetwork(),
    );

    const nodeSighash = getSigHashFromTx(newNodeTx, 0, nodeTx.getOutput(0));
    const refundSighash = getSigHashFromTx(newRefundTx, 0, nodeTx.getOutput(0));

    const newNodeSigningJob = {
      signingPublicKey: signingPubKey,
      rawTx: newNodeTx.toBytes(),
      signingNonceCommitment:
        await this.config.signer.getRandomSigningCommitment(),
    };

    const newRefundSigningJob = {
      signingPublicKey: signingPubKey,
      rawTx: newRefundTx.toBytes(),
      signingNonceCommitment:
        await this.config.signer.getRandomSigningCommitment(),
    };

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const response = await sparkClient.extend_leaf({
      leafId: node.id,
      ownerIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
      nodeTxSigningJob: newNodeSigningJob,
      refundTxSigningJob: newRefundSigningJob,
    });

    if (!response.nodeTxSigningResult || !response.refundTxSigningResult) {
      throw new Error("Signing result does not exist");
    }

    const nodeUserSig = await this.config.signer.signFrost({
      message: nodeSighash,
      privateAsPubKey: signingPubKey,
      publicKey: signingPubKey,
      verifyingKey: response.nodeTxSigningResult.verifyingKey,
      selfCommitment: newNodeSigningJob.signingNonceCommitment,
      statechainCommitments:
        response.nodeTxSigningResult.signingResult?.signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    const refundUserSig = await this.config.signer.signFrost({
      message: refundSighash,
      privateAsPubKey: signingPubKey,
      publicKey: signingPubKey,
      verifyingKey: response.refundTxSigningResult.verifyingKey,
      selfCommitment: newRefundSigningJob.signingNonceCommitment,
      statechainCommitments:
        response.refundTxSigningResult.signingResult?.signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    const nodeSig = await this.config.signer.aggregateFrost({
      message: nodeSighash,
      statechainSignatures:
        response.nodeTxSigningResult.signingResult?.signatureShares,
      statechainPublicKeys:
        response.nodeTxSigningResult.signingResult?.publicKeys,
      verifyingKey: response.nodeTxSigningResult.verifyingKey,
      statechainCommitments:
        response.nodeTxSigningResult.signingResult?.signingNonceCommitments,
      selfCommitment: newNodeSigningJob.signingNonceCommitment,
      publicKey: signingPubKey,
      selfSignature: nodeUserSig,
      adaptorPubKey: new Uint8Array(),
    });

    const refundSig = await this.config.signer.aggregateFrost({
      message: refundSighash,
      statechainSignatures:
        response.refundTxSigningResult.signingResult?.signatureShares,
      statechainPublicKeys:
        response.refundTxSigningResult.signingResult?.publicKeys,
      verifyingKey: response.refundTxSigningResult.verifyingKey,
      statechainCommitments:
        response.refundTxSigningResult.signingResult?.signingNonceCommitments,
      selfCommitment: newRefundSigningJob.signingNonceCommitment,
      publicKey: signingPubKey,
      selfSignature: refundUserSig,
      adaptorPubKey: new Uint8Array(),
    });

    return await sparkClient.finalize_node_signatures({
      intent: SignatureIntent.EXTEND,
      nodeSignatures: [
        {
          nodeId: response.leafId,
          nodeTxSignature: nodeSig,
          refundTxSignature: refundSig,
        },
      ],
    });
  }
}
