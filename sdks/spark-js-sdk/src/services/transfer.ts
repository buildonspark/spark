import {
  ClaimLeafKeyTweak,
  LeafRefundTxSigningJob,
  LeafRefundTxSigningResult,
  NodeSignatures,
  QueryPendingTransfersResponse,
  SendLeafKeyTweak,
  Transfer,
  TreeNode,
} from "proto/spark";
import * as ecies from "eciesjs";
import { WalletConfigService } from "./config";
import {
  bytesToHex,
  bytesToNumberBE,
  equalBytes,
  numberToBytesBE,
} from "@noble/curves/abstract/utils";
import {
  splitSecretWithProofs,
  VerifiableSecretShare,
} from "../utils/secret-sharing";
import { SignatureIntent } from "../proto/common";
import { KeyPackage, SigningNonce } from "../wasm/spark_bindings";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { secp256k1 } from "@noble/curves/secp256k1";
import {
  getP2TRAddressFromPublicKey,
  getSigHashFromTx,
  getTxFromRawTxBytes,
} from "../utils/bitcoin";
import {
  getSigningCommitmentFromNonce,
  copySigningCommitment,
  getRandomSigningNonce,
} from "../utils/signing";
import { signFrost, aggregateFrost } from "../utils/wasm";
import { NetworkConfig } from "../utils/network";
import { subtractPrivateKeys } from "../utils/keys";
import { sha256 } from "@scure/btc-signer/utils";
import { randomUUID } from "crypto";
import { ConnectionManager } from "./connection";
const TIME_LOCK_INTERVAL = 100;

export type LeafKeyTweak = {
  leaf: TreeNode;
  signingPrivKey: Uint8Array;
  newSigningPrivKey: Uint8Array;
};

export type ClaimLeafData = {
  signingPrivKey: Uint8Array;
  tx?: Transaction;
  refundTx?: Transaction;
  nonce: SigningNonce;
  vout?: number;
};

export type LeafRefundSigningData = {
  signingPrivKey: Uint8Array;
  receivingPubkey: Uint8Array;
  tx: Transaction;
  refundTx?: Transaction;
  nonce: SigningNonce;
  vout: number;
};

export class TransferService {
  private readonly config: WalletConfigService;
  private readonly connectionManager: ConnectionManager;

  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
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
      this.config.getCoordinatorAddress(),
      this.config
    );
    const pendingTransfersResp = await sparkClient.query_pending_transfers({
      receiverIdentityPublicKey: this.config.getIdentityPublicKey(),
    });
    sparkClient.close?.();
    return pendingTransfersResp;
  }

  async verifyPendingTransfer(
    transfer: Transfer
  ): Promise<Map<string, Uint8Array>> {
    const leafPrivKeyMap = new Map<string, Uint8Array>();
    const receiverEciesPrivKey = ecies.PrivateKey.fromHex(
      bytesToHex(this.config.getConfig().identityPrivateKey)
    );
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

      const leafSecret = ecies.decrypt(
        receiverEciesPrivKey.toHex(),
        leaf.secretCipher
      );
      leafPrivKeyMap.set(leaf.leaf.id, leafSecret);
    }
    return leafPrivKeyMap;
  }

  private async sendTransferTweakKey(
    transfer: Transfer,
    leaves: LeafKeyTweak[],
    refundSignatureMap: Map<string, Uint8Array>
  ): Promise<Transfer> {
    const keyTweakInputMap = this.prepareSendTransferKeyTweaks(
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
        operator.address,
        this.config
      );

      const leavesToSend = keyTweakInputMap.get(identifier);
      if (!leavesToSend) {
        errors.push(new Error(`No leaves to send for operator ${identifier}`));
        return;
      }
      const transferResp = await sparkClient.complete_send_transfer({
        transferId: transfer.id,
        ownerIdentityPublicKey: this.config.getIdentityPublicKey(),
        leavesToSend,
      });

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

      sparkClient.close?.();
    });

    await Promise.all(promises);

    if (errors.length > 0) {
      throw errors[0];
    }

    if (!updatedTransfer) {
      throw new Error("No updated transfer found");
    }

    return updatedTransfer;
  }

  private async sendTransferSignRefund(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date
  ): Promise<{ transfer: Transfer; signatureMap: Map<string, Uint8Array> }> {
    const transferID = randomUUID();

    const leafDataMap = new Map<string, LeafRefundSigningData>();
    for (const leaf of leaves) {
      const nonce = getRandomSigningNonce();
      const tx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
      const refundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);
      leafDataMap.set(leaf.leaf.id, {
        signingPrivKey: leaf.signingPrivKey,
        receivingPubkey: receiverIdentityPubkey,
        nonce,
        tx,
        refundTx,
        vout: leaf.leaf.vout,
      });
    }

    const signingJobs = this.prepareRefundSoSigningJobs(leaves, leafDataMap);

    const sparkConn = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
      this.config
    );

    const response = await sparkConn.start_send_transfer({
      transferId: transferID,
      leavesToSend: signingJobs,
      ownerIdentityPublicKey: this.config.getIdentityPublicKey(),
      receiverIdentityPublicKey: receiverIdentityPubkey,
      expiryTime: expiryTime,
    });

    sparkConn.close?.();

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
    };
  }

  private prepareSendTransferKeyTweaks(
    transfer: Transfer,
    leaves: LeafKeyTweak[],
    refundSignatureMap: Map<string, Uint8Array>
  ): Map<string, SendLeafKeyTweak[]> {
    const receiverEciesPubKey = ecies.PublicKey.fromHex(
      bytesToHex(transfer.receiverIdentityPublicKey)
    );

    const leavesTweaksMap = new Map<string, SendLeafKeyTweak[]>();

    for (const leaf of leaves) {
      const refundSignature = refundSignatureMap.get(leaf.leaf.id);
      if (!refundSignature) {
        throw new Error(`Refund signature not found for leaf ${leaf.leaf.id}`);
      }
      const leafTweaksMap = this.prepareSingleSendTransferKeyTweak(
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

  private prepareSingleSendTransferKeyTweak(
    transferID: string,
    leaf: LeafKeyTweak,
    receiverEciesPubKey: ecies.PublicKey,
    refundSignature: Uint8Array
  ): Map<string, SendLeafKeyTweak> {
    const privKeyTweak = subtractPrivateKeys(
      leaf.signingPrivKey,
      leaf.newSigningPrivKey
    );

    const shares = splitSecretWithProofs(
      bytesToNumberBE(privKeyTweak),
      secp256k1.CURVE.n,
      this.config.getConfig().threshold,
      Object.keys(this.config.getConfig().signingOperators).length
    );

    const pubkeySharesTweak = new Map<string, Uint8Array>();
    for (const [identifier, operator] of Object.entries(
      this.config.getConfig().signingOperators
    )) {
      const share = this.findShare(shares, operator.id);
      if (!share) {
        throw new Error(`Share not found for operator ${operator.id}`);
      }

      // TODO: check if this is correct
      const pubkeyTweak = secp256k1.getPublicKey(
        numberToBytesBE(share.share, 32),
        true
      );
      pubkeySharesTweak.set(identifier, pubkeyTweak);
    }

    const secretCipher = ecies.encrypt(
      receiverEciesPubKey.toBytes(),
      leaf.newSigningPrivKey
    );

    const encoder = new TextEncoder();
    const payload = new Uint8Array([
      ...encoder.encode(leaf.leaf.id),
      ...encoder.encode(transferID),
      ...secretCipher,
    ]);

    const payloadHash = sha256(payload);
    const signature = secp256k1
      .sign(payloadHash, this.config.getConfig().identityPrivateKey)
      .toCompactRawBytes();

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
        refundSignature,
      });
    }

    return leafTweaksMap;
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
      const signingPubkey = secp256k1.getPublicKey(
        refundSigningData.signingPrivKey
      );
      const refundTx = this.createRefundTx(
        leaf.leaf,
        refundSigningData.receivingPubkey
      );

      refundSigningData.refundTx = refundTx;

      const refundNonceCommitmentProto = getSigningCommitmentFromNonce(
        refundSigningData.nonce
      );

      signingJobs.push({
        leafId: leaf.leaf.id,
        refundTxSigningJob: {
          signingPublicKey: signingPubkey,
          rawTx: refundTx.toBytes(),
          signingNonceCommitment: refundNonceCommitmentProto,
        },
      });
    }

    return signingJobs;
  }

  private createRefundTx(
    leaf: TreeNode,
    receivingPubkey: Uint8Array
  ): Transaction {
    const tx = getTxFromRawTxBytes(leaf.nodeTx);
    const refundTx = getTxFromRawTxBytes(leaf.refundTx);

    const newRefundTx = new Transaction();
    const sequence =
      (1 << 30) |
      ((refundTx.getInput(0).sequence || 0) & (0xffff - TIME_LOCK_INTERVAL));
    newRefundTx.addInput({
      txid: tx.id,
      index: 0,
      sequence,
    });

    const finalScriptSig = refundTx.getInput(0).finalScriptSig;
    if (!finalScriptSig) {
      throw new Error(`Final script sig not found for refund tx`);
    }

    const refundP2trAddress = getP2TRAddressFromPublicKey(
      receivingPubkey,
      this.config.getConfig().network
    );
    const refundAddress = Address(
      NetworkConfig[this.config.getConfig().network]
    ).decode(refundP2trAddress);
    const refundPkScript = OutScript.encode(refundAddress);

    const amount = refundTx.getOutput(0).amount;
    if (!amount) {
      throw new Error(`Amount not found for refund tx`);
    }
    newRefundTx.addOutput({
      script: refundPkScript,
      amount,
    });

    newRefundTx.updateInput(0, {
      finalScriptSig,
    });

    return newRefundTx;
  }

  private async claimTransferTweakKeys(
    transfer: Transfer,
    leaves: LeafKeyTweak[]
  ) {
    const leavesTweaksMap = this.prepareClaimLeavesKeyTweaks(leaves);

    const errors: Error[] = [];

    const promises = Object.entries(
      this.config.getConfig().signingOperators
    ).map(async ([identifier, operator]) => {
      const sparkClient = await this.connectionManager.createSparkClient(
        operator.address,
        this.config
      );

      const leavesToReceive = leavesTweaksMap.get(identifier);
      if (!leavesToReceive) {
        errors.push(
          new Error(`No leaves to receive for operator ${identifier}`)
        );
        return;
      }

      await sparkClient.claim_transfer_tweak_keys({
        transferId: transfer.id,
        ownerIdentityPublicKey: this.config.getIdentityPublicKey(),
        leavesToReceive,
      });
      sparkClient.close?.();
    });

    await Promise.all(promises);

    if (errors.length > 0) {
      throw errors[0];
    }
  }

  private prepareClaimLeavesKeyTweaks(
    leaves: LeafKeyTweak[]
  ): Map<string, ClaimLeafKeyTweak[]> {
    const leafDataMap = new Map<string, ClaimLeafKeyTweak[]>();
    for (const leaf of leaves) {
      const leafData = this.prepareClaimLeafKeyTweaks(leaf);
      for (const [identifier, leafTweak] of leafData) {
        leafDataMap.set(identifier, [
          ...(leafDataMap.get(identifier) || []),
          leafTweak,
        ]);
      }
    }
    return leafDataMap;
  }

  private prepareClaimLeafKeyTweaks(
    leaf: LeafKeyTweak
  ): Map<string, ClaimLeafKeyTweak> {
    const prvKeyTweak = subtractPrivateKeys(
      leaf.signingPrivKey,
      leaf.newSigningPrivKey
    );
    const shares = splitSecretWithProofs(
      bytesToNumberBE(prvKeyTweak),
      secp256k1.CURVE.n,
      this.config.getConfig().threshold,
      Object.keys(this.config.getConfig().signingOperators).length
    );
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
      const nonce = getRandomSigningNonce();
      const tx = getTxFromRawTxBytes(leafKey.leaf.nodeTx);
      leafDataMap.set(leafKey.leaf.id, {
        signingPrivKey: leafKey.newSigningPrivKey,
        receivingPubkey: secp256k1.getPublicKey(leafKey.newSigningPrivKey),
        nonce,
        tx,
        vout: leafKey.leaf.vout,
      });
    }

    const signingJobs = this.prepareRefundSoSigningJobs(leafKeys, leafDataMap);

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
      this.config
    );
    const resp = await sparkClient.claim_transfer_sign_refunds({
      transferId: transfer.id,
      ownerIdentityPublicKey: this.config.getIdentityPublicKey(),
      signingJobs,
    });
    sparkClient.close?.();
    return this.signRefunds(leafDataMap, resp.signingResults);
  }

  private async signRefunds(
    leafDataMap: Map<string, ClaimLeafData>,
    operatorSigningResults: LeafRefundTxSigningResult[]
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

      const txOutput = leafData.tx?.getOutput(leafData.vout);
      if (!txOutput) {
        throw new Error(
          `Output not found for leaf ${operatorSigningResult.leafId}`
        );
      }

      const refundTxSighash = getSigHashFromTx(leafData.refundTx, 0, txOutput);
      const nonceCommitment = getSigningCommitmentFromNonce(leafData.nonce);
      const userKeyPackage = new KeyPackage(
        leafData.signingPrivKey,
        secp256k1.getPublicKey(leafData.signingPrivKey),
        operatorSigningResult.verifyingKey
      );

      const userSignature = signFrost({
        msg: refundTxSighash,
        keyPackage: userKeyPackage,
        nonce: leafData.nonce,
        selfCommitment: copySigningCommitment(nonceCommitment),
        statechainCommitments:
          operatorSigningResult.refundTxSigningResult?.signingNonceCommitments,
      });

      const refundAggregate = aggregateFrost({
        msg: refundTxSighash,
        statechainSignatures:
          operatorSigningResult.refundTxSigningResult?.signatureShares,
        statechainPublicKeys:
          operatorSigningResult.refundTxSigningResult?.publicKeys,
        verifyingKey: operatorSigningResult.verifyingKey,
        statechainCommitments:
          operatorSigningResult.refundTxSigningResult?.signingNonceCommitments,
        selfCommitment: copySigningCommitment(nonceCommitment),
        selfPublicKey: secp256k1.getPublicKey(leafData.signingPrivKey, true),
        selfSignature: userSignature,
      });

      nodeSignatures.push({
        nodeId: operatorSigningResult.leafId,
        refundTxSignature: refundAggregate,
        nodeTxSignature: new Uint8Array(),
      });
    }

    return nodeSignatures;
  }

  private async finalizeTransfer(nodeSignatures: NodeSignatures[]) {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
      this.config
    );
    await sparkClient.finalize_node_signatures({
      intent: SignatureIntent.TRANSFER,
      nodeSignatures,
    });
    sparkClient.close?.();
  }

  private findShare(shares: VerifiableSecretShare[], operatorID: number) {
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
