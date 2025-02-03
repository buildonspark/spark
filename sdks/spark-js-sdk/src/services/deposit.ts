import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import * as btc from "@scure/btc-signer";
import { NETWORK, p2tr, Transaction } from "@scure/btc-signer";
import { equalBytes, sha256 } from "@scure/btc-signer/utils";
import { SignatureIntent } from "../proto/common";
import { Address, GenerateDepositAddressResponse } from "../proto/spark";
import {
  getP2TRAddressFromPublicKey,
  getSigHashFromTx,
  getTxId,
} from "../utils/bitcoin";
import { subtractPublicKeys } from "../utils/keys";
import { getNetwork } from "../utils/network";
import { proofOfPossessionMessageHashForDepositAddress } from "../utils/proof";
import {
  copySigningCommitment,
  copySigningNonce,
  getRandomSigningNonce,
  getSigningCommitmentFromNonce,
} from "../utils/signing";
import { aggregateFrost, signFrost } from "../utils/wasm";
import { KeyPackage } from "../wasm/spark_bindings";
import { WalletConfigService } from "./config";
import { ConnectionManager } from "./connection";
type ValidateDepositAddressParams = {
  address: Address;
  userPubkey: Uint8Array;
};

export type GenerateDepositAddressParams = {
  signingPubkey: Uint8Array;
};

export type CreateTreeRootParams = {
  signingPrivkey: Uint8Array;
  verifyingKey: Uint8Array;
  depositTx: Transaction;
  vout: number;
};

const INITIAL_TIME_LOCK = 100;

export class DepositService {
  private readonly config: WalletConfigService;
  private readonly connectionManager: ConnectionManager;

  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
  }

  private validateDepositAddress({
    address,
    userPubkey,
  }: ValidateDepositAddressParams) {
    if (
      !address.depositAddressProof ||
      !address.depositAddressProof.proofOfPossessionSignature ||
      !address.depositAddressProof.addressSignatures
    ) {
      throw new Error(
        "proof of possession signature or address signatures is null"
      );
    }

    const operatorPubkey = subtractPublicKeys(address.verifyingKey, userPubkey);
    const msg = proofOfPossessionMessageHashForDepositAddress(
      this.config.getIdentityPublicKey(),
      operatorPubkey,
      address.address
    );

    const taprootKey = p2tr(
      operatorPubkey.slice(1, 33),
      undefined,
      NETWORK
    ).tweakedPubkey;

    const isVerified = schnorr.verify(
      address.depositAddressProof.proofOfPossessionSignature,
      msg,
      taprootKey
    );

    if (!isVerified) {
      throw new Error("proof of possession signature verification failed");
    }

    const addrHash = sha256(address.address);
    for (const operator of Object.values(
      this.config.getConfig().signingOperators
    )) {
      if (
        operator.identifier === this.config.getConfig().coodinatorIdentifier
      ) {
        continue;
      }

      const operatorPubkey = operator.identityPublicKey;
      const operatorSig =
        address.depositAddressProof.addressSignatures[operator.identifier];

      const sig = secp256k1.Signature.fromDER(operatorSig);

      const isVerified = secp256k1.verify(sig, addrHash, operatorPubkey);
      if (!isVerified) {
        throw new Error("signature verification failed");
      }
    }
  }

  async generateDepositAddress({
    signingPubkey,
  }: GenerateDepositAddressParams): Promise<GenerateDepositAddressResponse> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
      this.config
    );

    const depositResp = await sparkClient.generate_deposit_address({
      signingPublicKey: signingPubkey,
      identityPublicKey: this.config.getIdentityPublicKey(),
    });

    sparkClient.close?.();

    if (!depositResp.depositAddress) {
      throw new Error("No deposit address response from coordinator");
    }

    this.validateDepositAddress({
      address: depositResp.depositAddress,
      userPubkey: signingPubkey,
    });

    return depositResp;
  }

  async createTreeRoot({
    signingPrivkey,
    verifyingKey,
    depositTx,
    vout,
  }: CreateTreeRootParams) {
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
      this.config.getConfig().network
    );
    const refundAddress = btc
      .Address(getNetwork(this.config.getConfig().network))
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

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
      this.config
    );

    const treeResp = await sparkClient.start_tree_creation({
      identityPublicKey: this.config.getIdentityPublicKey(),
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
