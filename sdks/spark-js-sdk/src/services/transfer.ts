import {
  bytesToHex,
  equalBytes,
  numberToBytesBE,
} from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { Transaction } from "@scure/btc-signer";
import { sha256 } from "@scure/btc-signer/utils";
import * as ecies from "eciesjs";
import { SignatureIntent } from "../proto/common.js";
import {
  ClaimLeafKeyTweak,
  ClaimTransferSignRefundsResponse,
  CompleteSendTransferResponse,
  LeafRefundTxSigningJob,
  LeafRefundTxSigningResult,
  LeafSwapResponse,
  NodeSignatures,
  QueryPendingTransfersResponse,
  SendLeafKeyTweak,
  StartSendTransferResponse,
  Transfer,
  TreeNode,
} from "../proto/spark.js";
import { SigningCommitment } from "../signer/signer.js";
import { getSigHashFromTx, getTxFromRawTxBytes } from "../utils/bitcoin.js";
import { getCrypto } from "../utils/crypto.js";
import { VerifiableSecretShare } from "../utils/secret-sharing.js";
import { createRefundTx } from "../utils/transaction.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";

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
    connectionManager: ConnectionManager
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
  }

  async sendTransferTweakKey(
    transfer: Transfer,
    leaves: LeafKeyTweak[],
    refundSignatureMap: Map<string, Uint8Array>
  ): Promise<Transfer> {
    const keyTweakInputMap = await this.prepareSendTransferKeyTweaks(
      transfer,
      leaves,
      refundSignatureMap
    );

    let updatedTransfer: Transfer | undefined;
    const errors: Error[] = [];
    const promises = Object.entries(
      this.config.getConfig().signingOperators
    ).map(async ([identifier, operator]) => {
      const sparkClient = await this.connectionManager.createSparkClient(
        operator.address
      );

      const leavesToSend = keyTweakInputMap.get(identifier);
      if (!leavesToSend) {
        errors.push(new Error(`No leaves to send for operator ${identifier}`));
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
      } finally {
        sparkClient.close?.();
      }

      if (!updatedTransfer) {
        updatedTransfer = transferResp.transfer;
      } else {
        if (!transferResp.transfer) {
          errors.push(
            new Error(`No transfer response from operator ${identifier}`)
          );
          return;
        }

        if (!this.compareTransfers(updatedTransfer, transferResp.transfer)) {
          errors.push(
            new Error(`Inconsistent transfer response from operators`)
          );
        }
      }
    });

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
    adaptorPubKey?: Uint8Array
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
          `Leaf data not found for leaf ${operatorSigningResult.leafId}`
        );
      }

      const txOutput = leafData.tx?.getOutput(0);
      if (!txOutput) {
        throw new Error(
          `Output not found for leaf ${operatorSigningResult.leafId}`
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
    refundSignatureMap: Map<string, Uint8Array>
  ): Promise<Map<string, SendLeafKeyTweak[]>> {
    const receiverEciesPubKey = ecies.PublicKey.fromHex(
      bytesToHex(transfer.receiverIdentityPublicKey)
    );

    const leavesTweaksMap = new Map<string, SendLeafKeyTweak[]>();

    for (const leaf of leaves) {
      const refundSignature = refundSignatureMap.get(leaf.leaf.id);
      const leafTweaksMap = await this.prepareSingleSendTransferKeyTweak(
        transfer.id,
        leaf,
        receiverEciesPubKey,
        refundSignature
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
    refundSignature?: Uint8Array
  ): Promise<Map<string, SendLeafKeyTweak>> {
    const pubKeyTweak =
      await this.config.signer.subtractPrivateKeysGivenPublicKeys(
        leaf.signingPubKey,
        leaf.newSigningPubKey
      );

    const shares = await this.config.signer.splitSecretWithProofs({
      secret: pubKeyTweak,
      curveOrder: secp256k1.CURVE.n,
      threshold: this.config.getConfig().threshold,
      numShares: Object.keys(this.config.getConfig().signingOperators).length,
      isSecretPubkey: true,
    });

    const pubkeySharesTweak = new Map<string, Uint8Array>();
    for (const [identifier, operator] of Object.entries(
      this.config.getConfig().signingOperators
    )) {
      const share = this.findShare(shares, operator.id);
      if (!share) {
        throw new Error(`Share not found for operator ${operator.id}`);
      }

      const pubkeyTweak = secp256k1.getPublicKey(
        numberToBytesBE(share.share, 32),
        true
      );
      pubkeySharesTweak.set(identifier, pubkeyTweak);
    }

    const secretCipher = await this.config.signer.encryptLeafPrivateKeyEcies(
      receiverEciesPubKey.toBytes(),
      leaf.newSigningPubKey
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
      true
    );

    const leafTweaksMap = new Map<string, SendLeafKeyTweak>();
    for (const [identifier, operator] of Object.entries(
      this.config.getConfig().signingOperators
    )) {
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
        transfer2.senderIdentityPublicKey
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
    connectionManager: ConnectionManager
  ) {
    super(config, connectionManager);
  }

  async sendTransfer(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date
  ): Promise<Transfer> {
    const { transfer, signatureMap } = await this.sendTransferSignRefund(
      leaves,
      receiverIdentityPubkey,
      expiryTime
    );

    const transferWithTweakedKeys = await this.sendTransferTweakKey(
      transfer,
      leaves,
      signatureMap
    );

    return transferWithTweakedKeys;
  }

  async claimTransfer(transfer: Transfer, leaves: LeafKeyTweak[]) {
    await this.claimTransferTweakKeys(transfer, leaves);

    const signatures = await this.claimTransferSignRefunds(transfer, leaves);

    return await this.finalizeTransfer(signatures);
  }

  async queryPendingTransfers(): Promise<QueryPendingTransfersResponse> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );
    let pendingTransfersResp: QueryPendingTransfersResponse;
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
    } finally {
      sparkClient.close?.();
    }
    return pendingTransfersResp;
  }

  async verifyPendingTransfer(
    transfer: Transfer
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
          transfer.senderIdentityPublicKey
        )
      ) {
        throw new Error("Signature verification failed");
      }

      const leafSecret = await this.config.signer.decryptEcies(
        leaf.secretCipher
      );

      leafPubKeyMap.set(leaf.leaf.id, leafSecret);
    }
    return leafPubKeyMap;
  }

  async sendSwapSignRefund(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date,
    adaptorPubKey?: Uint8Array
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
      this.config.getCoordinatorAddress()
    );

    let response: LeafSwapResponse;
    try {
      response = await sparkClient.leaf_swap({
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
    } catch (error) {
      throw new Error(`Error initiating leaf swap: ${error}`);
    } finally {
      sparkClient.close?.();
    }

    if (!response.transfer) {
      throw new Error("No transfer response from coordinator");
    }

    const signatures = await this.signRefunds(
      leafDataMap,
      response.signingResults,
      adaptorPubKey
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

  async sendTransferSignRefund(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date
  ): Promise<{
    transfer: Transfer;
    signatureMap: Map<string, Uint8Array>;
    leafDataMap: Map<string, LeafRefundSigningData>;
  }> {
    const transferID = crypto.randomUUID();

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
      this.config.getCoordinatorAddress()
    );

    let response: StartSendTransferResponse;
    try {
      response = await sparkClient.start_send_transfer({
        transferId: transferID,
        leavesToSend: signingJobs,
        ownerIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
        receiverIdentityPublicKey: receiverIdentityPubkey,
        expiryTime: expiryTime,
      });
    } catch (error) {
      throw new Error(`Error starting send transfer: ${error}`);
    } finally {
      sparkClient.close?.();
    }

    const signatures = await this.signRefunds(
      leafDataMap,
      response.signingResults
    );

    const signatureMap = new Map<string, Uint8Array>();
    for (const signature of signatures) {
      signatureMap.set(signature.nodeId, signature.refundTxSignature);
    }

    if (!response.transfer) {
      throw new Error("No transfer response from coordinator");
    }

    return {
      transfer: response.transfer,
      signatureMap,
      leafDataMap,
    };
  }

  private prepareRefundSoSigningJobs(
    leaves: LeafKeyTweak[],
    leafDataMap: Map<string, LeafRefundSigningData>
  ): LeafRefundTxSigningJob[] {
    const signingJobs: LeafRefundTxSigningJob[] = [];
    for (const leaf of leaves) {
      const refundSigningData = leafDataMap.get(leaf.leaf.id);
      if (!refundSigningData) {
        throw new Error(`Leaf data not found for leaf ${leaf.leaf.id}`);
      }

      const { refundTx } = createRefundTx(
        leaf.leaf,
        refundSigningData.receivingPubkey,
        this.config.getNetwork()
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

  private async claimTransferTweakKeys(
    transfer: Transfer,
    leaves: LeafKeyTweak[]
  ) {
    const leavesTweaksMap = await this.prepareClaimLeavesKeyTweaks(leaves);

    const errors: Error[] = [];

    const promises = Object.entries(
      this.config.getConfig().signingOperators
    ).map(async ([identifier, operator]) => {
      const sparkClient = await this.connectionManager.createSparkClient(
        operator.address
      );

      const leavesToReceive = leavesTweaksMap.get(identifier);
      if (!leavesToReceive) {
        errors.push(
          new Error(`No leaves to receive for operator ${identifier}`)
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
        errors.push(new Error(`Error claiming transfer tweak keys: ${error}`));
        return;
      } finally {
        sparkClient.close?.();
      }
    });

    await Promise.all(promises);

    if (errors.length > 0) {
      throw new Error(`Error claiming transfer tweak keys: ${errors[0]}`);
    }
  }

  private async prepareClaimLeavesKeyTweaks(
    leaves: LeafKeyTweak[]
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
    leaf: LeafKeyTweak
  ): Promise<Map<string, ClaimLeafKeyTweak>> {
    const pubKeyTweak =
      await this.config.signer.subtractPrivateKeysGivenPublicKeys(
        leaf.signingPubKey,
        leaf.newSigningPubKey
      );

    const shares = await this.config.signer.splitSecretWithProofs({
      secret: pubKeyTweak,
      curveOrder: secp256k1.CURVE.n,
      threshold: this.config.getConfig().threshold,
      numShares: Object.keys(this.config.getConfig().signingOperators).length,
      isSecretPubkey: true,
    });

    const pubkeySharesTweak = new Map<string, Uint8Array>();

    for (const [identifier, operator] of Object.entries(
      this.config.getConfig().signingOperators
    )) {
      const share = this.findShare(shares, operator.id);
      if (!share) {
        throw new Error(`Share not found for operator ${operator.id}`);
      }
      const pubkeyTweak = secp256k1.getPublicKey(
        numberToBytesBE(share.share, 32)
      );
      pubkeySharesTweak.set(identifier, pubkeyTweak);
    }

    const leafTweaksMap = new Map<string, ClaimLeafKeyTweak>();
    for (const [identifier, operator] of Object.entries(
      this.config.getConfig().signingOperators
    )) {
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

  private async claimTransferSignRefunds(
    transfer: Transfer,
    leafKeys: LeafKeyTweak[]
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
      this.config.getCoordinatorAddress()
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
    } finally {
      sparkClient.close?.();
    }
    return this.signRefunds(leafDataMap, resp.signingResults);
  }

  private async finalizeTransfer(nodeSignatures: NodeSignatures[]) {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );
    try {
      return await sparkClient.finalize_node_signatures({
        intent: SignatureIntent.TRANSFER,
        nodeSignatures,
      });
    } catch (error) {
      throw new Error(`Error finalizing node signatures in transfer: ${error}`);
    } finally {
      sparkClient.close?.();
    }
  }
}
