import {
  GenerateDepositAddressResponse,
  QueryPendingTransfersResponse,
  Transfer,
} from "./proto/spark";
import { initWasm } from "./utils/wasm-wrapper";

import * as btc from "@scure/btc-signer";
import {
  frost_nonce,
  InitOutput,
  KeyPackage,
  NonceResult,
} from "./wasm/spark_bindings";
import { createNewGrpcConnection } from "./utils/connection";
import { Network, NetworkConfig } from "./utils/network";
import { secp256k1 } from "@noble/curves/secp256k1";
import * as ecies from "eciesjs";
import { bytesToHex, equalBytes } from "@noble/curves/abstract/utils";
import { sha256 } from "@scure/btc-signer/utils";
import { SigningOperator, validateDepositAddress } from "./utils/deposit";
import { Transaction } from "@scure/btc-signer";
import {
  finalizeTransfer,
  sendTransferSignRefund,
  sendTransferTweakKey,
} from "./utils/transfer";
import {
  getP2TRAddressFromPublicKey,
  getSigHashFromTx,
  getTxId,
} from "./utils/bitcoin";
import { SignatureIntent } from "./proto/common";
import {
  claimTransferSignRefunds,
  claimTransferTweakKeys,
  LeafKeyTweak,
} from "./utils/transfer";
import {
  copySigningCommitment,
  copySigningNonce,
  getRandomSigningNonce,
  getSigningCommitmentFromNonce,
} from "./utils/signing";
import { aggregateFrost, signFrost } from "./utils/wasm";

const INITIAL_TIME_LOCK = 100;

export type Config = {
  network: Network;
  signingOperators: Record<string, SigningOperator>;
  coodinatorIdentifier: string;
  frostSignerAddress: string;
  identityPrivateKey: Uint8Array;
  threshold: number;
};

export class SparkWallet {
  private wasmModule: InitOutput | null = null;
  private config: Config;

  constructor(config: Config) {
    this.config = config;
    this.initAsync();
  }

  getCoordinatorAddress() {
    return this.config.signingOperators[this.config.coodinatorIdentifier]
      .address;
  }

  private getIdentityPublicKey() {
    return secp256k1.getPublicKey(this.config.identityPrivateKey, true);
  }

  private async initAsync() {
    this.wasmModule = await initWasm();
  }

  private async ensureInitialized() {
    if (!this.wasmModule) {
      await this.initAsync();
    }
  }

  private async frostNonce({
    keyPackage,
  }: {
    keyPackage: KeyPackage;
  }): Promise<NonceResult> {
    await this.ensureInitialized();
    return frost_nonce(keyPackage);
  }

  async sendTransfer(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date
  ): Promise<Transfer> {
    const { transfer, signatureMap } = await sendTransferSignRefund(
      this.config,
      leaves,
      receiverIdentityPubkey,
      expiryTime
    );

    const transferWithTweakedKeys = await sendTransferTweakKey(
      this.config,
      transfer,
      leaves,
      signatureMap
    );

    return transferWithTweakedKeys;
  }

  async claimTransfer(transfer: Transfer, leaves: LeafKeyTweak[]) {
    await claimTransferTweakKeys(transfer, leaves, this.config);

    const signatures = await claimTransferSignRefunds(
      transfer,
      leaves,
      this.config
    );

    return await finalizeTransfer(signatures, this.getCoordinatorAddress());
  }

  async queryPendingTransfers(): Promise<QueryPendingTransfersResponse> {
    const sparkClient = createNewGrpcConnection(this.getCoordinatorAddress());
    const pendingTransfersResp = await sparkClient.query_pending_transfers({
      receiverIdentityPublicKey: this.getIdentityPublicKey(),
    });
    sparkClient.close?.();
    return pendingTransfersResp;
  }

  async verifyPendingTransfer(
    transfer: Transfer
  ): Promise<Map<string, Uint8Array>> {
    const leafPrivKeyMap = new Map<string, Uint8Array>();
    const receiverEciesPrivKey = ecies.PrivateKey.fromHex(
      bytesToHex(this.config.identityPrivateKey)
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

  async generateDepositAddress(
    signingPubkey: Uint8Array
  ): Promise<GenerateDepositAddressResponse> {
    const sparkClient = createNewGrpcConnection(this.getCoordinatorAddress());

    const depositResp = await sparkClient.generate_deposit_address({
      signingPublicKey: signingPubkey,
      identityPublicKey: this.getIdentityPublicKey(),
    });

    sparkClient.close?.();

    if (!depositResp.depositAddress) {
      throw new Error("No deposit address response from coordinator");
    }

    validateDepositAddress(
      depositResp.depositAddress,
      signingPubkey,
      this.getIdentityPublicKey(),
      Object.values(this.config.signingOperators),
      this.config.coodinatorIdentifier
    );

    return depositResp;
  }

  async createTreeRoot(
    signingPrivkey: Uint8Array,
    verifyingKey: Uint8Array,
    depositTx: Transaction,
    vout: number
  ) {
    await this.ensureInitialized();
    const signingPubKey = secp256k1.getPublicKey(signingPrivkey);

    // Create a root tx
    const rootTx = new Transaction();
    const output = depositTx.getOutput(0);
    if (!output) {
      throw new Error("No output found in deposit tx");
    }
    const script = output.script;
    const amount = output.amount;
    if (!script || !amount) {
      throw new Error("No script or amount found in deposit tx");
    }

    rootTx.addInput({
      txid: getTxId(depositTx),
      index: vout,
    });

    rootTx.addOutput({
      script,
      amount,
    });

    rootTx.updateInput(0, {
      finalScriptSig: script,
    });

    const rootNonce = getRandomSigningNonce();
    const rootNonceCommitment = getSigningCommitmentFromNonce(rootNonce);
    const rootTxSighash = getSigHashFromTx(rootTx, 0, output);

    // Create a refund tx
    const refundTx = new Transaction();
    const sequence = (1 << 30) | INITIAL_TIME_LOCK;
    refundTx.addInput({
      txid: getTxId(rootTx),
      index: 0,
      sequence,
    });

    const refundP2trAddress = getP2TRAddressFromPublicKey(
      signingPubKey,
      this.config.network
    );
    const refundAddress = btc
      .Address(NetworkConfig[this.config.network])
      .decode(refundP2trAddress);
    const refundPkScript = btc.OutScript.encode(refundAddress);

    refundTx.addOutput({
      script: refundPkScript,
      amount: amount,
    });

    rootTx.updateInput(0, {
      finalScriptSig: script,
    });

    const refundNonce = getRandomSigningNonce();
    const refundNonceCommitment = getSigningCommitmentFromNonce(refundNonce);
    const refundTxSighash = getSigHashFromTx(refundTx, 0, output);

    const sparkClient = createNewGrpcConnection(this.getCoordinatorAddress());
    const treeResp = await sparkClient.start_tree_creation({
      identityPublicKey: this.getIdentityPublicKey(),
      onChainUtxo: {
        txid: getTxId(depositTx),
        vout: vout,
        rawTx: depositTx.toBytes(),
      },
      rootTxSigningJob: {
        rawTx: rootTx.toBytes(),
        signingPublicKey: signingPubKey,
        signingNonceCommitment: rootNonceCommitment,
      },
      refundTxSigningJob: {
        rawTx: refundTx.toBytes(),
        signingPublicKey: signingPubKey,
        signingNonceCommitment: refundNonceCommitment,
      },
    });

    treeResp.rootNodeSignatureShares?.refundTxSigningResult;

    if (!treeResp.rootNodeSignatureShares?.verifyingKey) {
      throw new Error("No verifying key found in tree response");
    }

    if (
      !treeResp.rootNodeSignatureShares.nodeTxSigningResult
        ?.signingNonceCommitments
    ) {
      throw new Error("No signing nonce commitments found in tree response");
    }

    if (
      !treeResp.rootNodeSignatureShares.refundTxSigningResult
        ?.signingNonceCommitments
    ) {
      throw new Error("No signing nonce commitments found in tree response");
    }

    if (
      !equalBytes(treeResp.rootNodeSignatureShares.verifyingKey, verifyingKey)
    ) {
      throw new Error("Verifying key does not match");
    }

    const userKeyPackage = new KeyPackage(
      signingPrivkey,
      signingPubKey,
      verifyingKey
    );

    const refundKeyPackage = new KeyPackage(
      signingPrivkey,
      signingPubKey,
      verifyingKey
    );

    const rootSignature = signFrost({
      msg: rootTxSighash,
      keyPackage: userKeyPackage,
      nonce: copySigningNonce(rootNonce),
      selfCommitment: copySigningCommitment(rootNonceCommitment),
      statechainCommitments:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult
          .signingNonceCommitments,
    });

    const refundSignature = signFrost({
      msg: refundTxSighash,
      keyPackage: refundKeyPackage,
      nonce: copySigningNonce(refundNonce),
      selfCommitment: copySigningCommitment(refundNonceCommitment),
      statechainCommitments:
        treeResp.rootNodeSignatureShares.refundTxSigningResult
          .signingNonceCommitments,
    });

    const rootAggregate = aggregateFrost({
      msg: rootTxSighash,
      statechainSignatures:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult.signatureShares,
      statechainPublicKeys:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult.publicKeys,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult
          .signingNonceCommitments,
      selfCommitment: copySigningCommitment(rootNonceCommitment),
      selfPublicKey: signingPubKey,
      selfSignature: rootSignature!,
    });

    const refundAggregate = aggregateFrost({
      msg: refundTxSighash,
      statechainSignatures:
        treeResp.rootNodeSignatureShares.refundTxSigningResult.signatureShares,
      statechainPublicKeys:
        treeResp.rootNodeSignatureShares.refundTxSigningResult.publicKeys,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.refundTxSigningResult
          .signingNonceCommitments,
      selfCommitment: copySigningCommitment(refundNonceCommitment),
      selfPublicKey: signingPubKey,
      selfSignature: refundSignature,
    });

    const finalizeResp = await sparkClient.finalize_node_signatures({
      intent: SignatureIntent.CREATION,
      nodeSignatures: [
        {
          nodeId: treeResp.rootNodeSignatureShares.nodeId,
          nodeTxSignature: rootAggregate,
          refundTxSignature: refundAggregate,
        },
      ],
    });

    sparkClient.close?.();

    return finalizeResp;
  }
}
