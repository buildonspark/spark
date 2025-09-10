import { secp256k1 } from "@noble/curves/secp256k1";
import {
  bytesToHex,
  equalBytes,
  hexToBytes,
  numberToBytesBE,
} from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { Transaction } from "@scure/btc-signer";
import { TransactionInput, TransactionOutput } from "@scure/btc-signer/psbt";
import * as ecies from "eciesjs";
import { uuidv7 } from "uuidv7";
import {
  NetworkError,
  RPCError,
  SparkSDKError,
  ValidationError,
} from "../errors/index.js";
import { SignatureIntent } from "../proto/common.js";
import {
  ClaimLeafKeyTweak,
  ClaimTransferSignRefundsResponse,
  CounterLeafSwapResponse,
  LeafRefundTxSigningJob,
  LeafRefundTxSigningResult,
  NodeSignatures,
  QueryTransfersResponse,
  SecretProof,
  SendLeafKeyTweak,
  SendLeafKeyTweaks,
  SigningJob,
  StartTransferResponse,
  Transfer,
  TransferPackage,
  TransferStatus,
  TransferType,
  TreeNode,
} from "../proto/spark.js";
import {
  KeyDerivation,
  KeyDerivationType,
  SigningCommitmentWithOptionalNonce,
} from "../signer/types.js";
import { SparkAddressFormat } from "../utils/address.js";
import {
  getSigHashFromTx,
  getTxFromRawTxBytes,
  getTxId,
} from "../utils/bitcoin.js";
import { NetworkToProto } from "../utils/network.js";
import { VerifiableSecretShare } from "../utils/secret-sharing.js";
import {
  createNodeTxs,
  createRefundTxs,
  DIRECT_TIMELOCK_OFFSET,
  getCurrentTimelock,
  getEphemeralAnchorOutput,
  getNextTransactionSequence,
  getTransactionSequence,
  INITIAL_DIRECT_SEQUENCE,
  INITIAL_SEQUENCE,
  maybeApplyFee,
  TEST_UNILATERAL_DIRECT_SEQUENCE,
  TEST_UNILATERAL_SEQUENCE,
} from "../utils/transaction.js";
import { getTransferPackageSigningPayload } from "../utils/transfer_package.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";
import { SigningService } from "./signing.js";
const DEFAULT_EXPIRY_TIME = 10 * 60 * 1000;

export type LeafKeyTweak = {
  leaf: TreeNode;
  keyDerivation: KeyDerivation;
  newKeyDerivation: KeyDerivation;
};

export type ClaimLeafData = {
  keyDerivation: KeyDerivation;
  tx?: Transaction;
  refundTx?: Transaction;
  signingNonceCommitment: SigningCommitmentWithOptionalNonce;
  vout?: number;
};

export type LeafRefundSigningData = {
  keyDerivation: KeyDerivation;
  receivingPubkey: Uint8Array;
  signingNonceCommitment: SigningCommitmentWithOptionalNonce;
  directSigningNonceCommitment: SigningCommitmentWithOptionalNonce;
  tx: Transaction;
  directTx?: Transaction;
  refundTx?: Transaction;
  directRefundTx?: Transaction;
  directFromCpfpRefundTx?: Transaction;
  directFromCpfpRefundSigningNonceCommitment: SigningCommitmentWithOptionalNonce;
  vout: number;
};

export type SigningJobWithOptionalNonce = {
  signingPublicKey: Uint8Array;
  rawTx: Uint8Array;
  signingNonceCommitment: SigningCommitmentWithOptionalNonce;
};

function getSigningJobProto(
  signingJob: SigningJobWithOptionalNonce,
): SigningJob {
  return {
    signingPublicKey: signingJob.signingPublicKey,
    rawTx: signingJob.rawTx,
    signingNonceCommitment: signingJob.signingNonceCommitment.commitment,
  };
}
export class BaseTransferService {
  protected readonly config: WalletConfigService;
  protected readonly connectionManager: ConnectionManager;
  protected readonly signingService: SigningService;

  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager,
    signingService: SigningService,
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
    this.signingService = signingService;
  }

  async deliverTransferPackage(
    transfer: Transfer,
    leaves: LeafKeyTweak[],
    cpfpRefundSignatureMap: Map<string, Uint8Array>,
    directRefundSignatureMap: Map<string, Uint8Array>,
    directFromCpfpRefundSignatureMap: Map<string, Uint8Array>,
  ): Promise<Transfer> {
    const keyTweakInputMap = await this.prepareSendTransferKeyTweaks(
      transfer.id,
      transfer.receiverIdentityPublicKey,
      leaves,
      cpfpRefundSignatureMap,
      directRefundSignatureMap,
      directFromCpfpRefundSignatureMap,
    );

    for (const [key, operator] of Object.entries(
      this.config.getSigningOperators(),
    )) {
      const tweaks = keyTweakInputMap.get(key);
      if (!tweaks) {
        throw new ValidationError("No tweaks for operator", {
          field: "operator",
          value: key,
        });
      }
    }

    const transferPackage = await this.prepareTransferPackage(
      transfer.id,
      keyTweakInputMap,
      leaves,
      transfer.receiverIdentityPublicKey,
    );

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const response = await sparkClient.finalize_transfer_with_transfer_package({
      transferId: transfer.id,
      ownerIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
      transferPackage,
    });

    if (!response.transfer) {
      throw new ValidationError("No transfer response from operator");
    }

    return response.transfer;
  }

  async sendTransferWithKeyTweaks(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    sparkInvoice?: SparkAddressFormat,
  ): Promise<Transfer> {
    const transferID = uuidv7();

    const keyTweakInputMap = await this.prepareSendTransferKeyTweaks(
      transferID,
      receiverIdentityPubkey,
      leaves,
      new Map<string, Uint8Array>(),
      new Map<string, Uint8Array>(),
      new Map<string, Uint8Array>(),
    );

    const transferPackage = await this.prepareTransferPackage(
      transferID,
      keyTweakInputMap,
      leaves,
      receiverIdentityPubkey,
    );

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let response: StartTransferResponse;

    try {
      response = await sparkClient.start_transfer_v2({
        transferId: transferID,
        ownerIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
        receiverIdentityPublicKey: receiverIdentityPubkey,
        transferPackage,
        sparkInvoice,
      });
    } catch (error) {
      throw new NetworkError(
        "Failed to start transfer",
        {
          method: "POST",
        },
        error as Error,
      );
    }

    if (!response.transfer) {
      throw new ValidationError("No transfer response from operator");
    }

    return response.transfer;
  }

  private async prepareTransferPackage(
    transferID: string,
    keyTweakInputMap: Map<string, SendLeafKeyTweak[]>,
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
  ): Promise<TransferPackage> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const nodes: string[] = [];

    for (const leaf of leaves) {
      nodes.push(leaf.leaf.id);
    }
    const signingCommitments = await sparkClient.get_signing_commitments({
      nodeIds: nodes,
      count: 3,
    });

    const {
      cpfpLeafSigningJobs,
      directLeafSigningJobs,
      directFromCpfpLeafSigningJobs,
    } = await this.signingService.signRefunds(
      leaves,
      receiverIdentityPubkey,
      signingCommitments.signingCommitments.slice(0, leaves.length),
      signingCommitments.signingCommitments.slice(
        leaves.length,
        2 * leaves.length,
      ),
      signingCommitments.signingCommitments.slice(2 * leaves.length),
    );

    const encryptedKeyTweaks: { [key: string]: Uint8Array } = {};
    for (const [key, value] of keyTweakInputMap) {
      const protoToEncrypt: SendLeafKeyTweaks = {
        leavesToSend: value,
      };

      const protoToEncryptBinary =
        SendLeafKeyTweaks.encode(protoToEncrypt).finish();

      const operator = this.config.getSigningOperators()[key];
      if (!operator) {
        throw new ValidationError("Operator not found");
      }

      const soPublicKey = ecies.PublicKey.fromHex(operator.identityPublicKey);

      const encryptedProto = ecies.encrypt(
        soPublicKey.toBytes(),
        protoToEncryptBinary,
      );
      encryptedKeyTweaks[key] = Uint8Array.from(encryptedProto);
    }

    const transferPackage: TransferPackage = {
      leavesToSend: cpfpLeafSigningJobs,
      keyTweakPackage: encryptedKeyTweaks,
      userSignature: new Uint8Array(),
      directLeavesToSend: directLeafSigningJobs,
      directFromCpfpLeavesToSend: directFromCpfpLeafSigningJobs,
    };

    const transferPackageSigningPayload = getTransferPackageSigningPayload(
      transferID,
      transferPackage,
    );
    const signature = await this.config.signer.signMessageWithIdentityKey(
      transferPackageSigningPayload,
    );
    transferPackage.userSignature = new Uint8Array(signature);

    return transferPackage;
  }

  async signRefunds(
    leafDataMap: Map<string, LeafRefundSigningData>,
    operatorSigningResults: LeafRefundTxSigningResult[],
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

      // Sign CPFP refund transaction
      const cpfpRefundTxSighash = getSigHashFromTx(
        leafData.refundTx,
        0,
        txOutput,
      );
      const publicKey = await this.config.signer.getPublicKeyFromDerivation(
        leafData.keyDerivation,
      );
      const cpfpUserSignature = await this.config.signer.signFrost({
        message: cpfpRefundTxSighash,
        publicKey,
        keyDerivation: leafData.keyDerivation,
        selfCommitment: leafData.signingNonceCommitment,
        statechainCommitments:
          operatorSigningResult.refundTxSigningResult?.signingNonceCommitments,
        verifyingKey: operatorSigningResult.verifyingKey,
      });

      const cpfpRefundAggregate = await this.config.signer.aggregateFrost({
        message: cpfpRefundTxSighash,
        statechainSignatures:
          operatorSigningResult.refundTxSigningResult?.signatureShares,
        statechainPublicKeys:
          operatorSigningResult.refundTxSigningResult?.publicKeys,
        verifyingKey: operatorSigningResult.verifyingKey,
        statechainCommitments:
          operatorSigningResult.refundTxSigningResult?.signingNonceCommitments,
        selfCommitment: leafData.signingNonceCommitment,
        publicKey,
        selfSignature: cpfpUserSignature,
      });

      // Sign direct refund transaction

      let directRefundAggregate: Uint8Array | undefined;
      let directFromCpfpRefundAggregate: Uint8Array | undefined;
      if (leafData.directTx) {
        const directTxOutput = leafData.directTx.getOutput(0);

        if (leafData.directRefundTx) {
          const directRefundTxSighash = getSigHashFromTx(
            leafData.directRefundTx,
            0,
            directTxOutput,
          );

          const directUserSignature = await this.config.signer.signFrost({
            message: directRefundTxSighash,
            publicKey,
            keyDerivation: leafData.keyDerivation,
            selfCommitment: leafData.directSigningNonceCommitment,
            statechainCommitments:
              operatorSigningResult.directRefundTxSigningResult
                ?.signingNonceCommitments,
            verifyingKey: operatorSigningResult.verifyingKey,
          });

          directRefundAggregate = await this.config.signer.aggregateFrost({
            message: directRefundTxSighash,
            statechainSignatures:
              operatorSigningResult.directRefundTxSigningResult
                ?.signatureShares,
            statechainPublicKeys:
              operatorSigningResult.directRefundTxSigningResult?.publicKeys,
            verifyingKey: operatorSigningResult.verifyingKey,
            statechainCommitments:
              operatorSigningResult.directRefundTxSigningResult
                ?.signingNonceCommitments,
            selfCommitment: leafData.directSigningNonceCommitment,
            publicKey,
            selfSignature: directUserSignature,
          });
        }

        if (leafData.directFromCpfpRefundTx) {
          const directFromCpfpRefundTxSighash = getSigHashFromTx(
            leafData.directFromCpfpRefundTx,
            0,
            txOutput,
          );

          const directFromCpfpUserSignature =
            await this.config.signer.signFrost({
              message: directFromCpfpRefundTxSighash,
              publicKey,
              keyDerivation: leafData.keyDerivation,
              selfCommitment:
                leafData.directFromCpfpRefundSigningNonceCommitment,
              statechainCommitments:
                operatorSigningResult.directFromCpfpRefundTxSigningResult
                  ?.signingNonceCommitments,
              verifyingKey: operatorSigningResult.verifyingKey,
            });

          directFromCpfpRefundAggregate =
            await this.config.signer.aggregateFrost({
              message: directFromCpfpRefundTxSighash,
              statechainSignatures:
                operatorSigningResult.directFromCpfpRefundTxSigningResult
                  ?.signatureShares,
              statechainPublicKeys:
                operatorSigningResult.directFromCpfpRefundTxSigningResult
                  ?.publicKeys,
              verifyingKey: operatorSigningResult.verifyingKey,
              statechainCommitments:
                operatorSigningResult.directFromCpfpRefundTxSigningResult
                  ?.signingNonceCommitments,
              selfCommitment:
                leafData.directFromCpfpRefundSigningNonceCommitment,
              publicKey,
              selfSignature: directFromCpfpUserSignature,
            });
        }
      }

      nodeSignatures.push({
        nodeId: operatorSigningResult.leafId,
        nodeTxSignature: new Uint8Array(),
        directNodeTxSignature: new Uint8Array(),
        refundTxSignature: cpfpRefundAggregate,
        directRefundTxSignature: directRefundAggregate ?? new Uint8Array(),
        directFromCpfpRefundTxSignature:
          directFromCpfpRefundAggregate ?? new Uint8Array(),
      });
    }
    return nodeSignatures;
  }

  private async prepareSendTransferKeyTweaks(
    transferID: string,
    receiverIdentityPubkey: Uint8Array,
    leaves: LeafKeyTweak[],
    cpfpRefundSignatureMap: Map<string, Uint8Array>,
    directRefundSignatureMap: Map<string, Uint8Array>,
    directFromCpfpRefundSignatureMap: Map<string, Uint8Array>,
  ): Promise<Map<string, SendLeafKeyTweak[]>> {
    const receiverEciesPubKey = ecies.PublicKey.fromHex(
      bytesToHex(receiverIdentityPubkey),
    );

    const leavesTweaksMap = new Map<string, SendLeafKeyTweak[]>();

    for (const leaf of leaves) {
      const cpfpRefundSignature = cpfpRefundSignatureMap.get(leaf.leaf.id);
      const directRefundSignature = directRefundSignatureMap.get(leaf.leaf.id);
      const directFromCpfpRefundSignature =
        directFromCpfpRefundSignatureMap.get(leaf.leaf.id);

      const leafTweaksMap = await this.prepareSingleSendTransferKeyTweak(
        transferID,
        leaf,
        receiverEciesPubKey,
        cpfpRefundSignature,
        directRefundSignature,
        directFromCpfpRefundSignature,
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
    cpfpRefundSignature?: Uint8Array,
    directRefundSignature?: Uint8Array,
    directFromCpfpRefundSignature?: Uint8Array,
  ): Promise<Map<string, SendLeafKeyTweak>> {
    const signingOperators = this.config.getSigningOperators();

    const { shares, secretCipher } =
      await this.config.signer.subtractSplitAndEncrypt({
        first: leaf.keyDerivation,
        second: leaf.newKeyDerivation,
        receiverPublicKey: receiverEciesPubKey.toBytes(),
        curveOrder: secp256k1.CURVE.n,
        threshold: this.config.getThreshold(),
        numShares: Object.keys(signingOperators).length,
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
        refundSignature: cpfpRefundSignature ?? new Uint8Array(),
        directRefundSignature: directRefundSignature ?? new Uint8Array(),
        directFromCpfpRefundSignature:
          directFromCpfpRefundSignature ?? new Uint8Array(),
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
    signingService: SigningService,
  ) {
    super(config, connectionManager, signingService);
  }

  async claimTransfer(transfer: Transfer, leaves: LeafKeyTweak[]) {
    let proofMap: Map<string, Uint8Array[]> | undefined;
    if (transfer.status === TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED) {
      proofMap = await this.claimTransferTweakKeys(transfer, leaves);
    }
    const signatures = await this.claimTransferSignRefunds(
      transfer,
      leaves,
      proofMap,
    );
    return await this.finalizeNodeSignatures(signatures);
  }

  // When transferIds is not provided, all pending transfers for the receiver will be returned.
  async queryPendingTransfers(
    transferIds?: string[],
  ): Promise<QueryTransfersResponse> {
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
        transferIds,
        network: this.config.getNetworkProto(),
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
          $case: "senderOrReceiverIdentityPublicKey",
          senderOrReceiverIdentityPublicKey:
            await this.config.signer.getIdentityPublicKey(),
        },
        limit,
        offset,
        types: [
          TransferType.TRANSFER,
          TransferType.PREIMAGE_SWAP,
          TransferType.COOPERATIVE_EXIT,
          TransferType.UTXO_SWAP,
        ],
        network: NetworkToProto[this.config.getNetwork()],
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

  async queryTransfer(transferId: string): Promise<Transfer | undefined> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    const transferResp = await sparkClient.query_all_transfers({
      participant: {
        $case: "senderOrReceiverIdentityPublicKey",
        senderOrReceiverIdentityPublicKey:
          await this.config.signer.getIdentityPublicKey(),
      },
      transferIds: [transferId],
      network: NetworkToProto[this.config.getNetwork()],
    });
    return transferResp.transfers[0];
  }

  async sendTransferSignRefund(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date,
  ): Promise<{
    transfer: Transfer;
    signatureMap: Map<string, Uint8Array>;
    directSignatureMap: Map<string, Uint8Array>;
    directFromCpfpSignatureMap: Map<string, Uint8Array>;
    leafDataMap: Map<string, LeafRefundSigningData>;
  }> {
    const {
      transfer,
      signatureMap,
      directSignatureMap,
      directFromCpfpSignatureMap,
      leafDataMap,
    } = await this.sendTransferSignRefundInternal(
      leaves,
      receiverIdentityPubkey,
      expiryTime,
      false,
    );

    return {
      transfer,
      signatureMap,
      directSignatureMap,
      directFromCpfpSignatureMap,
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
    directSignatureMap: Map<string, Uint8Array>;
    directFromCpfpSignatureMap: Map<string, Uint8Array>;
    leafDataMap: Map<string, LeafRefundSigningData>;
  }> {
    const {
      transfer,
      signatureMap,
      directSignatureMap,
      directFromCpfpSignatureMap,
      leafDataMap,
    } = await this.sendTransferSignRefundInternal(
      leaves,
      receiverIdentityPubkey,
      expiryTime,
      true,
    );

    return {
      transfer,
      signatureMap,
      directSignatureMap,
      directFromCpfpSignatureMap,
      leafDataMap,
    };
  }

  async sendTransferSignRefundInternal(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date,
    forSwap: boolean,
  ): Promise<{
    transfer: Transfer;
    signatureMap: Map<string, Uint8Array>;
    directSignatureMap: Map<string, Uint8Array>;
    directFromCpfpSignatureMap: Map<string, Uint8Array>;
    leafDataMap: Map<string, LeafRefundSigningData>;
    signingResults: LeafRefundTxSigningResult[];
  }> {
    const transferId = uuidv7();
    const leafDataMap = new Map<string, LeafRefundSigningData>();
    for (const leaf of leaves) {
      const signingNonceCommitment =
        await this.config.signer.getRandomSigningCommitment();
      const directSigningNonceCommitment =
        await this.config.signer.getRandomSigningCommitment();
      const directFromCpfpRefundSigningNonceCommitment =
        await this.config.signer.getRandomSigningCommitment();

      const tx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
      const refundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);

      const directTx =
        leaf.leaf.directTx.length > 0
          ? getTxFromRawTxBytes(leaf.leaf.directTx)
          : undefined;

      const directRefundTx =
        leaf.leaf.directRefundTx.length > 0
          ? getTxFromRawTxBytes(leaf.leaf.directRefundTx)
          : undefined;
      const directFromCpfpRefundTx =
        leaf.leaf.directFromCpfpRefundTx.length > 0
          ? getTxFromRawTxBytes(leaf.leaf.directFromCpfpRefundTx)
          : undefined;

      leafDataMap.set(leaf.leaf.id, {
        keyDerivation: leaf.keyDerivation,
        receivingPubkey: receiverIdentityPubkey,
        signingNonceCommitment,
        directSigningNonceCommitment,
        tx,
        directTx,
        refundTx,
        directRefundTx,
        directFromCpfpRefundTx,
        directFromCpfpRefundSigningNonceCommitment,
        vout: leaf.leaf.vout,
      });
    }

    const signingJobs = await this.prepareRefundSoSigningJobs(
      leaves,
      leafDataMap,
    );

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let response: CounterLeafSwapResponse;
    try {
      if (forSwap) {
        response = await sparkClient.start_leaf_swap_v2({
          transferId,
          leavesToSend: signingJobs,
          ownerIdentityPublicKey:
            await this.config.signer.getIdentityPublicKey(),
          receiverIdentityPublicKey: receiverIdentityPubkey,
          expiryTime: expiryTime,
        });
      } else {
        response = await sparkClient.start_transfer_v2({
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
    );

    const cpfpSignatureMap = new Map<string, Uint8Array>();
    const directSignatureMap = new Map<string, Uint8Array>();
    const directFromCpfpSignatureMap = new Map<string, Uint8Array>();
    for (const signature of signatures) {
      cpfpSignatureMap.set(signature.nodeId, signature.refundTxSignature);
      directSignatureMap.set(
        signature.nodeId,
        signature.directRefundTxSignature,
      );
      directFromCpfpSignatureMap.set(
        signature.nodeId,
        signature.directFromCpfpRefundTxSignature,
      );
    }

    return {
      transfer: response.transfer,
      signatureMap: cpfpSignatureMap,
      directSignatureMap: directSignatureMap,
      directFromCpfpSignatureMap: directFromCpfpSignatureMap,
      leafDataMap,
      signingResults: response.signingResults,
    };
  }

  private async prepareRefundSoSigningJobs(
    leaves: LeafKeyTweak[],
    leafDataMap: Map<string, LeafRefundSigningData>,
    isForClaim?: boolean,
  ): Promise<LeafRefundTxSigningJob[]> {
    const signingJobs: LeafRefundTxSigningJob[] = [];
    for (const leaf of leaves) {
      const refundSigningData = leafDataMap.get(leaf.leaf.id);
      if (!refundSigningData) {
        throw new Error(`Leaf data not found for leaf ${leaf.leaf.id}`);
      }

      const nodeTx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
      const cpfpNodeOutPoint: TransactionInput = {
        txid: hexToBytes(getTxId(nodeTx)),
        index: 0,
      };

      let directNodeTx: Transaction | undefined;
      let directNodeOutPoint: TransactionInput | undefined;
      if (leaf.leaf.directTx.length > 0) {
        directNodeTx = getTxFromRawTxBytes(leaf.leaf.directTx);
        directNodeOutPoint = {
          txid: hexToBytes(getTxId(directNodeTx)),
          index: 0,
        };
      }

      const currRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);

      const sequence = currRefundTx.getInput(0).sequence;
      if (!sequence) {
        throw new ValidationError("Invalid refund transaction", {
          field: "sequence",
          value: currRefundTx.getInput(0),
          expected: "Non-null sequence",
        });
      }
      const { nextSequence, nextDirectSequence } = isForClaim
        ? getTransactionSequence(sequence)
        : getNextTransactionSequence(sequence);

      const amountSats = currRefundTx.getOutput(0).amount;
      if (amountSats === undefined) {
        throw new Error("Amount not found in signRefunds");
      }

      const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
        createRefundTxs({
          sequence: nextSequence,
          directSequence: nextDirectSequence,
          input: cpfpNodeOutPoint,
          directInput: directNodeOutPoint,
          amountSats,
          receivingPubkey: refundSigningData.receivingPubkey,
          network: this.config.getNetwork(),
        });

      refundSigningData.refundTx = cpfpRefundTx;
      refundSigningData.directRefundTx = directRefundTx;
      refundSigningData.directFromCpfpRefundTx = directFromCpfpRefundTx;

      const cpfpRefundNonceCommitmentProto =
        refundSigningData.signingNonceCommitment;
      const directRefundNonceCommitmentProto =
        refundSigningData.directSigningNonceCommitment;
      const directFromCpfpRefundNonceCommitmentProto =
        refundSigningData.directFromCpfpRefundSigningNonceCommitment;

      const signingPublicKey =
        await this.config.signer.getPublicKeyFromDerivation(
          refundSigningData.keyDerivation,
        );
      signingJobs.push({
        leafId: leaf.leaf.id,
        refundTxSigningJob: {
          signingPublicKey,
          rawTx: cpfpRefundTx.toBytes(),
          signingNonceCommitment: cpfpRefundNonceCommitmentProto.commitment,
        },
        directRefundTxSigningJob: directRefundTx
          ? {
              signingPublicKey,
              rawTx: directRefundTx.toBytes(),
              signingNonceCommitment:
                directRefundNonceCommitmentProto.commitment,
            }
          : undefined,
        directFromCpfpRefundTxSigningJob: directFromCpfpRefundTx
          ? {
              signingPublicKey,
              rawTx: directFromCpfpRefundTx.toBytes(),
              signingNonceCommitment:
                directFromCpfpRefundNonceCommitmentProto.commitment,
            }
          : undefined,
      });
    }

    return signingJobs;
  }

  async claimTransferTweakKeys(
    transfer: Transfer,
    leaves: LeafKeyTweak[],
  ): Promise<Map<string, Uint8Array[]>> {
    const { leafDataMap: leavesTweaksMap, proofMap } =
      await this.prepareClaimLeavesKeyTweaks(leaves);

    const errors: Error[] = [];

    const promises = Object.entries(this.config.getSigningOperators()).map(
      async ([identifier, operator]) => {
        const sparkClient = await this.connectionManager.createSparkClient(
          operator.address,
        );

        const leavesToReceive = leavesTweaksMap.get(identifier);
        if (!leavesToReceive) {
          errors.push(
            new ValidationError("No leaves to receive for operator", {
              field: "operator",
              value: identifier,
            }) as SparkSDKError,
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
        } catch (error: any) {
          errors.push(
            new RPCError(
              "Failed to claim transfer tweak keys",
              {
                method: "POST",
              },
              error,
            ),
          );
          return;
        }
      },
    );

    await Promise.all(promises);

    if (errors.length > 0) {
      throw errors[0];
    }

    return proofMap;
  }

  private async prepareClaimLeavesKeyTweaks(leaves: LeafKeyTweak[]): Promise<{
    leafDataMap: Map<string, ClaimLeafKeyTweak[]>;
    proofMap: Map<string, Uint8Array[]>;
  }> {
    const leafDataMap = new Map<string, ClaimLeafKeyTweak[]>();
    const proofMap = new Map<string, Uint8Array[]>();
    for (const leaf of leaves) {
      const { leafKeyTweaks: leafData, proofs } =
        await this.prepareClaimLeafKeyTweaks(leaf);
      proofMap.set(leaf.leaf.id, proofs);

      for (const [identifier, leafTweak] of leafData) {
        leafDataMap.set(identifier, [
          ...(leafDataMap.get(identifier) || []),
          leafTweak,
        ]);
      }
    }
    return { leafDataMap, proofMap };
  }

  private async prepareClaimLeafKeyTweaks(leaf: LeafKeyTweak): Promise<{
    leafKeyTweaks: Map<string, ClaimLeafKeyTweak>;
    proofs: Uint8Array[];
  }> {
    const signingOperators = this.config.getSigningOperators();

    const shares =
      await this.config.signer.subtractAndSplitSecretWithProofsGivenDerivations(
        {
          first: leaf.keyDerivation,
          second: leaf.newKeyDerivation,
          curveOrder: secp256k1.CURVE.n,
          threshold: this.config.getThreshold(),
          numShares: Object.keys(signingOperators).length,
        },
      );

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

    if (!shares[0]?.proofs) {
      throw new ValidationError("Proofs not found", {
        field: "proofs",
        value: shares[0]?.proofs,
      }) as SparkSDKError;
    }

    return { leafKeyTweaks: leafTweaksMap, proofs: shares[0].proofs };
  }

  async claimTransferSignRefunds(
    transfer: Transfer,
    leafKeys: LeafKeyTweak[],
    proofMap?: Map<string, Uint8Array[]>,
  ): Promise<NodeSignatures[]> {
    const leafDataMap: Map<string, LeafRefundSigningData> = new Map();
    for (const leafKey of leafKeys) {
      const tx = getTxFromRawTxBytes(leafKey.leaf.nodeTx);
      const directTx =
        leafKey.leaf.directTx.length > 0
          ? getTxFromRawTxBytes(leafKey.leaf.directTx)
          : undefined;

      leafDataMap.set(leafKey.leaf.id, {
        keyDerivation: leafKey.newKeyDerivation,
        receivingPubkey: await this.config.signer.getPublicKeyFromDerivation(
          leafKey.newKeyDerivation,
        ),
        signingNonceCommitment:
          await this.config.signer.getRandomSigningCommitment(),
        directSigningNonceCommitment:
          await this.config.signer.getRandomSigningCommitment(),
        directFromCpfpRefundSigningNonceCommitment:
          await this.config.signer.getRandomSigningCommitment(),
        tx,
        directTx,
        vout: leafKey.leaf.vout,
      });
    }

    const signingJobs = await this.prepareRefundSoSigningJobs(
      leafKeys,
      leafDataMap,
      true,
    );

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    let resp: ClaimTransferSignRefundsResponse;

    const secretProofMap: { [key: string]: SecretProof } = {};
    if (proofMap) {
      for (const [leafId, proof] of proofMap.entries()) {
        secretProofMap[leafId] = {
          proofs: proof,
        };
      }
    }
    try {
      resp = await sparkClient.claim_transfer_sign_refunds_v2({
        transferId: transfer.id,
        ownerIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
        signingJobs,
      });
    } catch (error: any) {
      throw new RPCError(
        "Failed to claim transfer sign refunds",
        {
          method: "POST",
        },
        error,
      );
    }
    return this.signRefunds(leafDataMap, resp.signingResults);
  }

  private async finalizeNodeSignatures(nodeSignatures: NodeSignatures[]) {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    try {
      return await sparkClient.finalize_node_signatures_v2({
        intent: SignatureIntent.TRANSFER,
        nodeSignatures,
      });
    } catch (error) {
      throw new Error(`Error finalizing node signatures in transfer: ${error}`);
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

  private async refreshTimelockNodesInternal(
    node: TreeNode,
    parentNode: TreeNode,
    useTestUnilateralSequence?: boolean,
  ) {
    const signingJobs: (SigningJobWithOptionalNonce & {
      type: "node" | "directNode" | "cpfp" | "direct" | "directFromCpfp";
      parentTxOut: TransactionOutput;
    })[] = [];

    const parentNodeTx = getTxFromRawTxBytes(parentNode.nodeTx);
    const parentNodeOutput: TransactionOutput = parentNodeTx.getOutput(0);
    if (!parentNodeOutput) {
      throw Error("Could not get parent node output");
    }

    const nodeTx = getTxFromRawTxBytes(node.nodeTx);
    const nodeInput = nodeTx.getInput(0);

    const nodeOutput = nodeTx.getOutput(0);
    if (!nodeOutput) {
      throw Error("Could not get node output");
    }

    let directNodeTx: Transaction | undefined;
    let directNodeInput: TransactionInput | undefined;
    if (node.directTx.length > 0) {
      directNodeTx = getTxFromRawTxBytes(node.directTx);
      directNodeInput = directNodeTx.getInput(0);
    }

    const currSequence = nodeInput.sequence;

    if (!currSequence) {
      throw new ValidationError("Invalid node transaction", {
        field: "sequence",
        value: nodeInput,
        expected: "Non-null sequence",
      });
    }

    let { nextSequence, nextDirectSequence } = getNextTransactionSequence(
      currSequence,
      true,
    );

    const output = {
      script: parentNodeOutput.script!,
      amount: parentNodeOutput.amount!,
    };

    const newNodeInput: TransactionInput = {
      txid: nodeInput.txid,
      index: nodeInput.index,
      sequence: useTestUnilateralSequence
        ? TEST_UNILATERAL_SEQUENCE
        : nextSequence,
    };

    const newDirectInput: TransactionInput | undefined =
      directNodeTx && directNodeInput
        ? {
            txid: directNodeInput.txid,
            index: directNodeInput.index,
            sequence: useTestUnilateralSequence
              ? TEST_UNILATERAL_DIRECT_SEQUENCE
              : nextDirectSequence,
          }
        : undefined;

    const { cpfpNodeTx, directNodeTx: newDirectNodeTx } = createNodeTxs(
      output,
      newNodeInput,
      newDirectInput,
    );

    const newCpfpNodeOutput: TransactionOutput = cpfpNodeTx.getOutput(0);
    if (!newCpfpNodeOutput) {
      throw Error("Could not get new cpfp node output");
    }

    const newDirectNodeOutput: TransactionOutput | undefined =
      newDirectNodeTx?.getOutput(0);

    const signingPublicKey =
      await this.config.signer.getPublicKeyFromDerivation({
        type: KeyDerivationType.LEAF,
        path: node.id,
      });

    signingJobs.push({
      signingPublicKey,
      rawTx: cpfpNodeTx.toBytes(),
      signingNonceCommitment:
        await this.config.signer.getRandomSigningCommitment(),
      type: "node",
      parentTxOut: parentNodeOutput,
    });

    if (newDirectNodeTx) {
      signingJobs.push({
        signingPublicKey,
        rawTx: newDirectNodeTx.toBytes(),
        signingNonceCommitment:
          await this.config.signer.getRandomSigningCommitment(),
        type: "directNode",
        parentTxOut: parentNodeOutput,
      });
    }

    const newCpfpRefundOutPoint: TransactionInput = {
      txid: hexToBytes(getTxId(cpfpNodeTx)!),
      index: 0,
    };

    let newDirectRefundOutPoint: TransactionInput | undefined;
    if (newDirectNodeTx) {
      newDirectRefundOutPoint = {
        txid: hexToBytes(getTxId(newDirectNodeTx)!),
        index: 0,
      };
    }

    const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
      createRefundTxs({
        sequence: INITIAL_SEQUENCE,
        directSequence: INITIAL_DIRECT_SEQUENCE,
        input: newCpfpRefundOutPoint,
        directInput: newDirectRefundOutPoint,
        amountSats: nodeOutput.amount!,
        receivingPubkey: await this.config.signer.getPublicKeyFromDerivation({
          type: KeyDerivationType.LEAF,
          path: node.id,
        }),
        network: this.config.getNetwork(),
      });

    signingJobs.push({
      signingPublicKey,
      rawTx: cpfpRefundTx.toBytes(),
      signingNonceCommitment:
        await this.config.signer.getRandomSigningCommitment(),
      type: "cpfp",
      parentTxOut: newCpfpNodeOutput,
    });

    if (directRefundTx && newDirectNodeOutput) {
      signingJobs.push({
        signingPublicKey,
        rawTx: directRefundTx.toBytes(),
        signingNonceCommitment:
          await this.config.signer.getRandomSigningCommitment(),
        type: "direct",
        parentTxOut: newDirectNodeOutput,
      });
    }

    if (directFromCpfpRefundTx && newCpfpNodeOutput) {
      signingJobs.push({
        signingPublicKey,
        rawTx: directFromCpfpRefundTx.toBytes(),
        signingNonceCommitment:
          await this.config.signer.getRandomSigningCommitment(),
        type: "directFromCpfp",
        parentTxOut: newCpfpNodeOutput,
      });
    }

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const response = await sparkClient.refresh_timelock_v2({
      leafId: node.id,
      ownerIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
      signingJobs: signingJobs.map(getSigningJobProto),
    });

    if (signingJobs.length !== response.signingResults.length) {
      throw Error(
        `number of signing jobs and signing results do not match: ${signingJobs.length} !== ${response.signingResults.length}`,
      );
    }

    let nodeSignatures: NodeSignatures[] = [];
    let leafCpfpSignature: Uint8Array | undefined;
    let leafDirectSignature: Uint8Array | undefined;
    let cpfpRefundSignature: Uint8Array | undefined;
    let directRefundSignature: Uint8Array | undefined;
    let directFromCpfpRefundSignature: Uint8Array | undefined;

    for (const [i, signingResult] of response.signingResults.entries()) {
      const signingJob = signingJobs[i];
      if (!signingJob || !signingResult) {
        throw Error("Signing job does not exist");
      }

      const rawTx = getTxFromRawTxBytes(signingJob.rawTx);
      const txOut = signingJob.parentTxOut;
      if (!txOut) {
        throw Error("Could not get tx out");
      }

      const rawTxSighash = getSigHashFromTx(rawTx, 0, txOut);

      const userSignature = await this.config.signer.signFrost({
        message: rawTxSighash,
        keyDerivation: {
          type: KeyDerivationType.LEAF,
          path: node.id,
        },
        publicKey: signingJob.signingPublicKey,
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
        publicKey: signingJob.signingPublicKey,
        selfSignature: userSignature,
        adaptorPubKey: new Uint8Array(),
      });

      if (signingJob.type === "node") {
        leafCpfpSignature = signature;
      } else if (signingJob.type === "directNode") {
        leafDirectSignature = signature;
      } else if (signingJob.type === "cpfp") {
        cpfpRefundSignature = signature;
      } else if (signingJob.type === "direct") {
        directRefundSignature = signature;
      } else if (signingJob.type === "directFromCpfp") {
        directFromCpfpRefundSignature = signature;
      }
    }

    nodeSignatures.push({
      nodeId: node.id,
      nodeTxSignature: leafCpfpSignature || new Uint8Array(),
      directNodeTxSignature: leafDirectSignature || new Uint8Array(),
      refundTxSignature: cpfpRefundSignature || new Uint8Array(),
      directRefundTxSignature: directRefundSignature || new Uint8Array(),
      directFromCpfpRefundTxSignature:
        directFromCpfpRefundSignature || new Uint8Array(),
    });

    const result = await sparkClient.finalize_node_signatures_v2({
      intent: SignatureIntent.REFRESH,
      nodeSignatures,
    });

    return result;
  }

  async refreshTimelockNodes(node: TreeNode, parentNode: TreeNode) {
    return await this.refreshTimelockNodesInternal(node, parentNode);
  }

  async extendTimelock(node: TreeNode) {
    const nodeTx = getTxFromRawTxBytes(node.nodeTx);
    const refundTx = getTxFromRawTxBytes(node.refundTx);

    const refundSequence = refundTx.getInput(0).sequence || 0;
    const newNodeOutPoint: TransactionInput = {
      txid: hexToBytes(getTxId(nodeTx)),
      index: 0,
    };

    const {
      nextSequence: newNodeSequence,
      nextDirectSequence: newDirectNodeSequence,
    } = getNextTransactionSequence(refundSequence);

    // Create new CPFP node tx
    const newNodeTx = new Transaction({
      version: 3,
      allowUnknownOutputs: true,
    });
    newNodeTx.addInput({ ...newNodeOutPoint, sequence: newNodeSequence });

    // Apply fee to the new node transaction output
    const originalOutput = nodeTx.getOutput(0);
    if (!originalOutput) {
      throw Error("Could not get original node output");
    }

    newNodeTx.addOutput({
      script: originalOutput.script!,
      amount: originalOutput.amount!,
    });

    newNodeTx.addOutput(getEphemeralAnchorOutput());

    // Create new direct node tx

    let newDirectNodeTx: Transaction | undefined;
    if (node.directTx.length > 0) {
      newDirectNodeTx = new Transaction({
        version: 3,
        allowUnknownOutputs: true,
      });

      newDirectNodeTx.addInput({
        ...newNodeOutPoint,
        sequence: newDirectNodeSequence,
      });

      newDirectNodeTx.addOutput({
        script: originalOutput.script!,
        amount: maybeApplyFee(originalOutput.amount!),
      });
    }

    const newCpfpRefundOutPoint: TransactionInput = {
      txid: hexToBytes(getTxId(newNodeTx)!),
      index: 0,
    };

    let newDirectRefundOutPoint: TransactionInput | undefined;
    if (newDirectNodeTx) {
      newDirectRefundOutPoint = {
        txid: hexToBytes(getTxId(newDirectNodeTx)!),
        index: 0,
      };
    }

    const amountSats = refundTx.getOutput(0).amount;
    if (amountSats === undefined) {
      throw new Error("Amount not found in extendTimelock");
    }

    const signingPublicKey =
      await this.config.signer.getPublicKeyFromDerivation({
        type: KeyDerivationType.LEAF,
        path: node.id,
      });
    // Create both CPFP and direct refund transactions

    const {
      cpfpRefundTx: newCpfpRefundTx,
      directRefundTx: newDirectRefundTx,
      directFromCpfpRefundTx: newDirectFromCpfpRefundTx,
    } = createRefundTxs({
      sequence: INITIAL_SEQUENCE,
      directSequence: INITIAL_DIRECT_SEQUENCE,
      input: newCpfpRefundOutPoint,
      directInput: newDirectRefundOutPoint,
      amountSats,
      receivingPubkey: signingPublicKey,
      network: this.config.getNetwork(),
    });

    if (!newCpfpRefundTx) {
      throw new ValidationError(
        "Failed to create refund transactions in extendTimelock",
      );
    }

    const nodeSighash = getSigHashFromTx(newNodeTx, 0, nodeTx.getOutput(0));

    const directNodeSighash = newDirectNodeTx
      ? getSigHashFromTx(newDirectNodeTx, 0, nodeTx.getOutput(0))
      : undefined;

    const cpfpRefundSighash = getSigHashFromTx(
      newCpfpRefundTx,
      0,
      newNodeTx.getOutput(0),
    );
    const directRefundSighash =
      newDirectNodeTx && newDirectRefundTx
        ? getSigHashFromTx(newDirectRefundTx, 0, newDirectNodeTx.getOutput(0))
        : undefined;

    const directFromCpfpRefundSighash = newDirectFromCpfpRefundTx
      ? getSigHashFromTx(newDirectFromCpfpRefundTx, 0, newNodeTx.getOutput(0))
      : undefined;

    const newNodeSigningJob = {
      signingPublicKey,
      rawTx: newNodeTx.toBytes(),
      signingNonceCommitment:
        await this.config.signer.getRandomSigningCommitment(),
    };

    const newDirectNodeSigningJob: SigningJobWithOptionalNonce | undefined =
      newDirectNodeTx
        ? {
            signingPublicKey,
            rawTx: newDirectNodeTx.toBytes(),
            signingNonceCommitment:
              await this.config.signer.getRandomSigningCommitment(),
          }
        : undefined;

    const newCpfpRefundSigningJob = {
      signingPublicKey,
      rawTx: newCpfpRefundTx.toBytes(),
      signingNonceCommitment:
        await this.config.signer.getRandomSigningCommitment(),
    };

    const newDirectRefundSigningJob: SigningJobWithOptionalNonce | undefined =
      newDirectRefundTx
        ? {
            signingPublicKey,
            rawTx: newDirectRefundTx.toBytes(),
            signingNonceCommitment:
              await this.config.signer.getRandomSigningCommitment(),
          }
        : undefined;

    const newDirectFromCpfpRefundSigningJob:
      | SigningJobWithOptionalNonce
      | undefined = newDirectFromCpfpRefundTx
      ? {
          signingPublicKey,
          rawTx: newDirectFromCpfpRefundTx.toBytes(),
          signingNonceCommitment:
            await this.config.signer.getRandomSigningCommitment(),
        }
      : undefined;

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const response = await sparkClient.extend_leaf_v2({
      leafId: node.id,
      ownerIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
      nodeTxSigningJob: getSigningJobProto(newNodeSigningJob),
      directNodeTxSigningJob: newDirectNodeSigningJob
        ? getSigningJobProto(newDirectNodeSigningJob)
        : undefined,
      refundTxSigningJob: getSigningJobProto(newCpfpRefundSigningJob),
      directRefundTxSigningJob: newDirectRefundSigningJob
        ? getSigningJobProto(newDirectRefundSigningJob)
        : undefined,
      directFromCpfpRefundTxSigningJob: newDirectFromCpfpRefundSigningJob
        ? getSigningJobProto(newDirectFromCpfpRefundSigningJob)
        : undefined,
    });

    if (!response.nodeTxSigningResult || !response.refundTxSigningResult) {
      throw new Error("Signing result does not exist");
    }

    const keyDerivation: KeyDerivation = {
      type: KeyDerivationType.LEAF,
      path: node.id,
    };

    // Sign CPFP node transaction
    const nodeUserSig = await this.config.signer.signFrost({
      message: nodeSighash,
      keyDerivation,
      publicKey: signingPublicKey,
      verifyingKey: response.nodeTxSigningResult.verifyingKey,
      selfCommitment: newNodeSigningJob.signingNonceCommitment,
      statechainCommitments:
        response.nodeTxSigningResult.signingResult?.signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    // Aggregate CPFP node signature
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
      publicKey: signingPublicKey,
      selfSignature: nodeUserSig,
      adaptorPubKey: new Uint8Array(),
    });

    let directNodeSig: Uint8Array | undefined;
    if (
      directNodeSighash &&
      newDirectNodeSigningJob &&
      response.directNodeTxSigningResult
    ) {
      // Sign direct node transaction
      const directNodeUserSig = await this.config.signer.signFrost({
        message: directNodeSighash,
        keyDerivation,
        publicKey: signingPublicKey,
        verifyingKey: response.directNodeTxSigningResult.verifyingKey,
        selfCommitment: newDirectNodeSigningJob.signingNonceCommitment,
        statechainCommitments:
          response.directNodeTxSigningResult.signingResult
            ?.signingNonceCommitments,
        adaptorPubKey: new Uint8Array(),
      });

      // Aggregate direct node signature
      directNodeSig = await this.config.signer.aggregateFrost({
        message: directNodeSighash,
        statechainSignatures:
          response.directNodeTxSigningResult.signingResult?.signatureShares,
        statechainPublicKeys:
          response.directNodeTxSigningResult.signingResult?.publicKeys,
        verifyingKey: response.directNodeTxSigningResult.verifyingKey,
        statechainCommitments:
          response.directNodeTxSigningResult.signingResult
            ?.signingNonceCommitments,
        selfCommitment: newDirectNodeSigningJob.signingNonceCommitment,
        publicKey: signingPublicKey,
        selfSignature: directNodeUserSig,
        adaptorPubKey: new Uint8Array(),
      });
    }

    // Sign CPFP refund transaction
    const cpfpRefundUserSig = await this.config.signer.signFrost({
      message: cpfpRefundSighash,
      keyDerivation,
      publicKey: signingPublicKey,
      verifyingKey: response.refundTxSigningResult.verifyingKey,
      selfCommitment: newCpfpRefundSigningJob.signingNonceCommitment,
      statechainCommitments:
        response.refundTxSigningResult.signingResult?.signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    // Aggregate CPFP refund signature
    const cpfpRefundSig = await this.config.signer.aggregateFrost({
      message: cpfpRefundSighash,
      statechainSignatures:
        response.refundTxSigningResult.signingResult?.signatureShares,
      statechainPublicKeys:
        response.refundTxSigningResult.signingResult?.publicKeys,
      verifyingKey: response.refundTxSigningResult.verifyingKey,
      statechainCommitments:
        response.refundTxSigningResult.signingResult?.signingNonceCommitments,
      selfCommitment: newCpfpRefundSigningJob.signingNonceCommitment,
      publicKey: signingPublicKey,
      selfSignature: cpfpRefundUserSig,
      adaptorPubKey: new Uint8Array(),
    });

    let directRefundSig: Uint8Array | undefined;
    if (
      directRefundSighash &&
      newDirectRefundSigningJob &&
      response.directRefundTxSigningResult
    ) {
      // Sign direct refund transaction
      const directRefundUserSig = await this.config.signer.signFrost({
        message: directRefundSighash,
        keyDerivation,
        publicKey: signingPublicKey,
        verifyingKey: response.directRefundTxSigningResult.verifyingKey,
        selfCommitment: newDirectRefundSigningJob.signingNonceCommitment,
        statechainCommitments:
          response.directRefundTxSigningResult.signingResult
            ?.signingNonceCommitments,
        adaptorPubKey: new Uint8Array(),
      });

      // Aggregate direct refund signature
      directRefundSig = await this.config.signer.aggregateFrost({
        message: directRefundSighash,
        statechainSignatures:
          response.directRefundTxSigningResult.signingResult?.signatureShares,
        statechainPublicKeys:
          response.directRefundTxSigningResult.signingResult?.publicKeys,
        verifyingKey: response.directRefundTxSigningResult.verifyingKey,
        statechainCommitments:
          response.directRefundTxSigningResult.signingResult
            ?.signingNonceCommitments,
        selfCommitment: newDirectRefundSigningJob.signingNonceCommitment,
        publicKey: signingPublicKey,
        selfSignature: directRefundUserSig,
        adaptorPubKey: new Uint8Array(),
      });
    }

    let directFromCpfpRefundSig: Uint8Array | undefined;
    if (
      directFromCpfpRefundSighash &&
      newDirectFromCpfpRefundSigningJob &&
      response.directFromCpfpRefundTxSigningResult
    ) {
      // Sign direct from CPFP refund transaction
      const directFromCpfpRefundUserSig = await this.config.signer.signFrost({
        message: directFromCpfpRefundSighash,
        keyDerivation,
        publicKey: signingPublicKey,
        verifyingKey: response.directFromCpfpRefundTxSigningResult.verifyingKey,
        selfCommitment:
          newDirectFromCpfpRefundSigningJob.signingNonceCommitment,
        statechainCommitments:
          response.directFromCpfpRefundTxSigningResult.signingResult
            ?.signingNonceCommitments,
        adaptorPubKey: new Uint8Array(),
      });

      // Aggregate direct from CPFP refund signature
      directFromCpfpRefundSig = await this.config.signer.aggregateFrost({
        message: directFromCpfpRefundSighash,
        statechainSignatures:
          response.directFromCpfpRefundTxSigningResult.signingResult
            ?.signatureShares,
        statechainPublicKeys:
          response.directFromCpfpRefundTxSigningResult.signingResult
            ?.publicKeys,
        verifyingKey: response.directFromCpfpRefundTxSigningResult.verifyingKey,
        statechainCommitments:
          response.directFromCpfpRefundTxSigningResult.signingResult
            ?.signingNonceCommitments,
        selfCommitment:
          newDirectFromCpfpRefundSigningJob.signingNonceCommitment,
        publicKey: signingPublicKey,
        selfSignature: directFromCpfpRefundUserSig,
        adaptorPubKey: new Uint8Array(),
      });
    }

    return await sparkClient.finalize_node_signatures_v2({
      intent: SignatureIntent.EXTEND,
      nodeSignatures: [
        {
          nodeId: response.leafId,
          nodeTxSignature: nodeSig,
          directNodeTxSignature: directNodeSig,
          refundTxSignature: cpfpRefundSig,
          directRefundTxSignature: directRefundSig,
          directFromCpfpRefundTxSignature: directFromCpfpRefundSig,
        },
      ],
    });
  }

  async testonly_expireTimeLockNodeTx(node: TreeNode, parentNode: TreeNode) {
    return await this.refreshTimelockNodesInternal(node, parentNode, true);
  }

  async testonly_expireTimeLockRefundtx(node: TreeNode) {
    const nodeTx = getTxFromRawTxBytes(node.nodeTx);
    const directNodeTx =
      node.directTx.length > 0 ? getTxFromRawTxBytes(node.directTx) : undefined;

    const cpfpRefundTx = getTxFromRawTxBytes(node.refundTx);

    const currSequence = cpfpRefundTx.getInput(0).sequence || 0;
    const currTimelock = getCurrentTimelock(currSequence);
    if (currTimelock <= 100) {
      throw new ValidationError("Cannot expire timelock below 100", {
        field: "currTimelock",
        value: currTimelock,
        expected: "Timelock greater than 100",
      });
    }
    const nextSequence = TEST_UNILATERAL_SEQUENCE;
    const nextDirectSequence =
      TEST_UNILATERAL_SEQUENCE + DIRECT_TIMELOCK_OFFSET;

    const nodeOutput = nodeTx.getOutput(0);
    if (!nodeOutput) {
      throw Error("Could not get node output");
    }

    const keyDerivation: KeyDerivation = {
      type: KeyDerivationType.LEAF,
      path: node.id,
    };
    const signingPublicKey =
      await this.config.signer.getPublicKeyFromDerivation(keyDerivation);

    const cpfpRefundOutPoint: TransactionInput = {
      txid: hexToBytes(getTxId(nodeTx)!),
      index: 0,
    };

    let directRefundOutPoint: TransactionInput | undefined;
    if (directNodeTx) {
      directRefundOutPoint = {
        txid: hexToBytes(getTxId(directNodeTx)!),
        index: 0,
      };
    }

    const {
      cpfpRefundTx: newCpfpRefundTx,
      directRefundTx: newDirectRefundTx,
      directFromCpfpRefundTx: newDirectFromCpfpRefundTx,
    } = createRefundTxs({
      sequence: nextSequence,
      directSequence: nextDirectSequence,
      input: cpfpRefundOutPoint,
      directInput: directRefundOutPoint,
      amountSats: nodeOutput.amount!,
      receivingPubkey: signingPublicKey,
      network: this.config.getNetwork(),
    });

    const signingJobs: (SigningJobWithOptionalNonce & {
      type: "cpfp" | "direct" | "directFromCpfp";
      parentTxOut: TransactionOutput;
    })[] = [];

    signingJobs.push({
      signingPublicKey,
      rawTx: newCpfpRefundTx.toBytes(),
      signingNonceCommitment:
        await this.config.signer.getRandomSigningCommitment(),
      type: "cpfp",
      parentTxOut: nodeOutput,
    });

    const directNodeTxOut = directNodeTx?.getOutput(0);
    if (newDirectRefundTx && directNodeTxOut) {
      signingJobs.push({
        signingPublicKey,
        rawTx: newDirectRefundTx.toBytes(),
        signingNonceCommitment:
          await this.config.signer.getRandomSigningCommitment(),
        type: "direct",
        parentTxOut: directNodeTxOut,
      });
    }

    if (newDirectFromCpfpRefundTx) {
      signingJobs.push({
        signingPublicKey,
        rawTx: newDirectFromCpfpRefundTx.toBytes(),
        signingNonceCommitment:
          await this.config.signer.getRandomSigningCommitment(),
        type: "directFromCpfp",
        parentTxOut: nodeOutput,
      });
    }

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const response = await sparkClient.refresh_timelock_v2({
      leafId: node.id,
      ownerIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
      signingJobs: signingJobs.map(getSigningJobProto),
    });

    if (response.signingResults.length !== signingJobs.length) {
      throw Error(
        `Expected ${signingJobs.length} signing results, got ${response.signingResults.length}`,
      );
    }

    let cpfpRefundSignature: Uint8Array | undefined;
    let directRefundSignature: Uint8Array | undefined;
    let directFromCpfpRefundSignature: Uint8Array | undefined;

    for (const [i, signingJob] of signingJobs.entries()) {
      const signingResult = response.signingResults[i];
      if (!signingResult) {
        throw Error("Signing result does not exist");
      }

      const rawTx = getTxFromRawTxBytes(signingJob.rawTx);
      const txOut = signingJob.parentTxOut;
      const rawTxSighash = getSigHashFromTx(rawTx, 0, txOut);

      const userSignature = await this.config.signer.signFrost({
        message: rawTxSighash,
        keyDerivation,
        publicKey: signingPublicKey,
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
        publicKey: signingPublicKey,
        selfSignature: userSignature,
        adaptorPubKey: new Uint8Array(),
      });

      if (signingJob.type === "cpfp") {
        cpfpRefundSignature = signature;
      } else if (signingJob.type === "direct") {
        directRefundSignature = signature;
      } else if (signingJob.type === "directFromCpfp") {
        directFromCpfpRefundSignature = signature;
      }
    }

    const result = await sparkClient.finalize_node_signatures_v2({
      intent: SignatureIntent.REFRESH,
      nodeSignatures: [
        {
          nodeId: node.id,
          nodeTxSignature: new Uint8Array(),
          directNodeTxSignature: new Uint8Array(),
          refundTxSignature: cpfpRefundSignature,
          directRefundTxSignature: directRefundSignature,
          directFromCpfpRefundTxSignature: directFromCpfpRefundSignature,
        },
      ],
    });

    return result;
  }
}
