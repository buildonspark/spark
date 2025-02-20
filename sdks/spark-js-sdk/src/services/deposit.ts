import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import * as btc from "@scure/btc-signer";
import { p2tr, Transaction } from "@scure/btc-signer";
import { equalBytes, sha256 } from "@scure/btc-signer/utils";
import { SignatureIntent } from "../proto/common";
import {
  Address,
  FinalizeNodeSignaturesResponse,
  GenerateDepositAddressResponse,
  StartTreeCreationResponse,
} from "../proto/spark";
import {
  getP2TRAddressFromPublicKey,
  getSigHashFromTx,
  getTxId,
} from "../utils/bitcoin";
import { subtractPublicKeys } from "../utils/keys";
import { getNetwork } from "../utils/network";
import { proofOfPossessionMessageHashForDepositAddress } from "../utils/proof";
import { createWasmSigningCommitment } from "../utils/signing";
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
  signingPubKey: Uint8Array;
  verifyingKey: Uint8Array;
  depositTx: Transaction;
  vout: number;
};

const INITIAL_TIME_LOCK = 2000;

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

  private async validateDepositAddress({
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
      await this.config.signer.getIdentityPublicKey(),
      operatorPubkey,
      address.address
    );

    const taprootKey = p2tr(
      operatorPubkey.slice(1, 33),
      undefined,
      getNetwork(this.config.getNetwork())
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
      this.config.getCoordinatorAddress()
    );

    let depositResp: GenerateDepositAddressResponse;
    try {
      depositResp = await sparkClient.generate_deposit_address({
        signingPublicKey: signingPubkey,
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
        network: this.config.getNetwork(),
      });
    } catch (error) {
      throw new Error(`Error generating deposit address: ${error}`);
    } finally {
      sparkClient.close?.();
    }
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
    signingPubKey,
    verifyingKey,
    depositTx,
    vout,
  }: CreateTreeRootParams) {
    // Create a root tx
    const rootTx = new Transaction();
    const output = depositTx.getOutput(vout);
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

    const rootNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();
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
      this.config.getNetwork()
    );
    const refundAddress = btc
      .Address(getNetwork(this.config.getNetwork()))
      .decode(refundP2trAddress);
    const refundPkScript = btc.OutScript.encode(refundAddress);

    refundTx.addOutput({
      script: refundPkScript,
      amount: amount,
    });

    const refundNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();
    const refundTxSighash = getSigHashFromTx(refundTx, 0, output);

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );

    let treeResp: StartTreeCreationResponse;

    try {
      treeResp = await sparkClient.start_tree_creation({
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
        onChainUtxo: {
          vout: vout,
          rawTx: depositTx.toBytes(),
          network: this.config.getNetwork(),
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
    } catch (error) {
      sparkClient.close?.();
      throw new Error(`Error starting tree creation: ${error}`);
    }

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

    const rootSignature = await this.config.signer.signFrost({
      message: rootTxSighash,
      publicKey: signingPubKey,
      privateAsPubKey: signingPubKey,
      verifyingKey,
      selfCommitment: rootNonceCommitment,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult
          .signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    const refundSignature = await this.config.signer.signFrost({
      message: refundTxSighash,
      publicKey: signingPubKey,
      privateAsPubKey: signingPubKey,
      verifyingKey,
      selfCommitment: refundNonceCommitment,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.refundTxSigningResult
          .signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    const rootAggregate = await this.config.signer.aggregateFrost({
      message: rootTxSighash,
      statechainSignatures:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult.signatureShares,
      statechainPublicKeys:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult.publicKeys,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult
          .signingNonceCommitments,
      selfCommitment: createWasmSigningCommitment(rootNonceCommitment),
      publicKey: signingPubKey,
      selfSignature: rootSignature!,
      adaptorPubKey: new Uint8Array(),
    });

    const refundAggregate = await this.config.signer.aggregateFrost({
      message: refundTxSighash,
      statechainSignatures:
        treeResp.rootNodeSignatureShares.refundTxSigningResult.signatureShares,
      statechainPublicKeys:
        treeResp.rootNodeSignatureShares.refundTxSigningResult.publicKeys,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.refundTxSigningResult
          .signingNonceCommitments,
      selfCommitment: createWasmSigningCommitment(refundNonceCommitment),
      publicKey: signingPubKey,
      selfSignature: refundSignature,
      adaptorPubKey: new Uint8Array(),
    });

    let finalizeResp: FinalizeNodeSignaturesResponse;
    try {
      finalizeResp = await sparkClient.finalize_node_signatures({
        intent: SignatureIntent.CREATION,
        nodeSignatures: [
          {
            nodeId: treeResp.rootNodeSignatureShares.nodeId,
            nodeTxSignature: rootAggregate,
            refundTxSignature: refundAggregate,
          },
        ],
      });
    } catch (error) {
      sparkClient.close?.();
      throw new Error(`Error finalizing node signatures in deposit: ${error}`);
    } finally {
      sparkClient.close?.();
    }

    return finalizeResp;
  }
}
