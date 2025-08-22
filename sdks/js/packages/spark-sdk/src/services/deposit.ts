import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { sha256 } from "@noble/hashes/sha2";
import { hexToBytes } from "@noble/hashes/utils";
import { p2tr, Transaction } from "@scure/btc-signer";
import { equalBytes } from "@scure/btc-signer/utils";
import { NetworkError, ValidationError } from "../errors/types.js";
import { SignatureIntent } from "../proto/common.js";
import {
  Address,
  FinalizeNodeSignaturesResponse,
  GenerateDepositAddressResponse,
  StartDepositTreeCreationResponse,
} from "../proto/spark.js";
import { KeyDerivation } from "../signer/types.js";
import { getSigHashFromTx, getTxId } from "../utils/bitcoin.js";
import { subtractPublicKeys } from "../utils/keys.js";
import { getNetwork } from "../utils/network.js";
import { proofOfPossessionMessageHashForDepositAddress } from "../utils/proof.js";
import {
  createRefundTxs,
  createRootTx,
  INITIAL_DIRECT_SEQUENCE,
  INITIAL_SEQUENCE,
} from "../utils/transaction.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";

type ValidateDepositAddressParams = {
  address: Address;
  userPubkey: Uint8Array;
};

export type GenerateDepositAddressParams = {
  signingPubkey: Uint8Array;
  leafId: string;
  isStatic?: boolean;
};

export type CreateTreeRootParams = {
  keyDerivation: KeyDerivation;
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
    connectionManager: ConnectionManager,
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
      throw new ValidationError(
        "Proof of possession signature or address signatures is null",
        {
          field: "depositAddressProof",
          value: address.depositAddressProof,
        },
      );
    }

    const operatorPubkey = subtractPublicKeys(address.verifyingKey, userPubkey);
    const msg = proofOfPossessionMessageHashForDepositAddress(
      await this.config.signer.getIdentityPublicKey(),
      operatorPubkey,
      address.address,
    );

    const taprootKey = p2tr(
      operatorPubkey.slice(1, 33),
      undefined,
      getNetwork(this.config.getNetwork()),
    ).tweakedPubkey;

    const isVerified = schnorr.verify(
      address.depositAddressProof.proofOfPossessionSignature,
      msg,
      taprootKey,
    );

    if (!isVerified) {
      throw new ValidationError(
        "Proof of possession signature verification failed",
        {
          field: "proofOfPossessionSignature",
          value: address.depositAddressProof.proofOfPossessionSignature,
        },
      );
    }

    const addrHash = sha256(address.address);
    for (const operator of Object.values(this.config.getSigningOperators())) {
      if (operator.identifier === this.config.getCoordinatorIdentifier()) {
        continue;
      }

      const operatorPubkey = hexToBytes(operator.identityPublicKey);
      const operatorSig =
        address.depositAddressProof.addressSignatures[operator.identifier];
      if (!operatorSig) {
        throw new ValidationError("Operator signature not found", {
          field: "addressSignatures",
          value: operator.identifier,
        });
      }
      const sig = secp256k1.Signature.fromDER(operatorSig);

      const isVerified = secp256k1.verify(
        sig.toCompactRawBytes(),
        addrHash,
        operatorPubkey,
      );
      if (!isVerified) {
        throw new ValidationError("Operator signature verification failed", {
          field: "operatorSignature",
          value: operatorSig,
        });
      }
    }
  }

  async generateDepositAddress({
    signingPubkey,
    leafId,
    isStatic = false,
  }: GenerateDepositAddressParams): Promise<GenerateDepositAddressResponse> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let depositResp: GenerateDepositAddressResponse;
    try {
      depositResp = await sparkClient.generate_deposit_address({
        signingPublicKey: signingPubkey,
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
        network: this.config.getNetworkProto(),
        leafId: leafId,
        isStatic: isStatic,
      });
    } catch (error) {
      throw new NetworkError(
        "Failed to generate deposit address",
        {
          operation: "generate_deposit_address",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
    }

    if (!depositResp.depositAddress) {
      throw new ValidationError(
        "No deposit address response from coordinator",
        {
          field: "depositAddress",
          value: depositResp,
        },
      );
    }

    await this.validateDepositAddress({
      address: depositResp.depositAddress,
      userPubkey: signingPubkey,
    });

    return depositResp;
  }

  async createTreeRoot({
    keyDerivation,
    verifyingKey,
    depositTx,
    vout,
  }: CreateTreeRootParams) {
    // Create root transactions (CPFP and direct)
    const output = depositTx.getOutput(vout);
    if (!output) {
      throw new ValidationError("Invalid deposit transaction output", {
        field: "vout",
        value: vout,
        expected: "Valid output index",
      });
    }
    const script = output.script;
    const amount = output.amount;
    if (!script || !amount) {
      throw new ValidationError("No script or amount found in deposit tx", {
        field: "output",
        value: output,
        expected: "Output with script and amount",
      });
    }

    const depositOutPoint = {
      txid: hexToBytes(getTxId(depositTx)),
      index: vout,
    };
    const depositTxOut = {
      script,
      amount,
    };

    const [cpfpRootTx, directRootTx] = createRootTx(
      depositOutPoint,
      depositTxOut,
    );

    // Create nonce commitments for root transactions
    const cpfpRootNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();
    const directRootNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();

    // Get sighashes for root transactions
    const cpfpRootTxSighash = getSigHashFromTx(cpfpRootTx, 0, output);
    const directRootTxSighash = getSigHashFromTx(directRootTx, 0, output);

    const signingPubKey =
      await this.config.signer.getPublicKeyFromDerivation(keyDerivation);

    // Create refund transactions (CPFP and direct)
    const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
      createRefundTxs({
        sequence: INITIAL_SEQUENCE,
        directSequence: INITIAL_DIRECT_SEQUENCE,
        input: { txid: hexToBytes(getTxId(cpfpRootTx)), index: 0 },
        directInput: { txid: hexToBytes(getTxId(directRootTx)), index: 0 },
        amountSats: amount,
        receivingPubkey: signingPubKey,
        network: this.config.getNetwork(),
      });

    // Create nonce commitments for refund transactions
    const cpfpRefundNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();
    const directRefundNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();
    const directFromCpfpRefundNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();

    // Get sighashes for refund transactions
    const cpfpRefundTxSighash = getSigHashFromTx(
      cpfpRefundTx,
      0,
      cpfpRootTx.getOutput(0),
    );

    if (!directRefundTx || !directFromCpfpRefundTx) {
      throw new ValidationError(
        "Expected direct refund transactions for tree creation",
        {
          field: "directRefundTx",
          value: directRefundTx,
        },
      );
    }

    const directRefundTxSighash = getSigHashFromTx(
      directRefundTx,
      0,
      directRootTx.getOutput(0),
    );
    const directFromCpfpRefundTxSighash = getSigHashFromTx(
      directFromCpfpRefundTx,
      0,
      cpfpRootTx.getOutput(0),
    );

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let treeResp: StartDepositTreeCreationResponse;

    try {
      treeResp = await sparkClient.start_deposit_tree_creation({
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
        onChainUtxo: {
          vout: vout,
          rawTx: depositTx.toBytes(true),
          network: this.config.getNetworkProto(),
        },
        rootTxSigningJob: {
          rawTx: cpfpRootTx.toBytes(),
          signingPublicKey: signingPubKey,
          signingNonceCommitment: cpfpRootNonceCommitment.commitment,
        },
        refundTxSigningJob: {
          rawTx: cpfpRefundTx.toBytes(),
          signingPublicKey: signingPubKey,
          signingNonceCommitment: cpfpRefundNonceCommitment.commitment,
        },
        directRootTxSigningJob: {
          rawTx: directRootTx.toBytes(),
          signingPublicKey: signingPubKey,
          signingNonceCommitment: directRootNonceCommitment.commitment,
        },
        directRefundTxSigningJob: {
          rawTx: directRefundTx.toBytes(),
          signingPublicKey: signingPubKey,
          signingNonceCommitment: directRefundNonceCommitment.commitment,
        },
        directFromCpfpRefundTxSigningJob: {
          rawTx: directFromCpfpRefundTx.toBytes(),
          signingPublicKey: signingPubKey,
          signingNonceCommitment:
            directFromCpfpRefundNonceCommitment.commitment,
        },
      });
    } catch (error) {
      throw new NetworkError(
        "Failed to start deposit tree creation",
        {
          operation: "start_deposit_tree_creation",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
    }

    if (!treeResp.rootNodeSignatureShares?.verifyingKey) {
      throw new ValidationError("No verifying key found in tree response", {
        field: "verifyingKey",
        value: treeResp.rootNodeSignatureShares,
        expected: "Non-null verifying key",
      });
    }

    if (
      !treeResp.rootNodeSignatureShares.nodeTxSigningResult
        ?.signingNonceCommitments
    ) {
      throw new ValidationError(
        "No signing nonce commitments found in tree response",
        {
          field: "nodeTxSigningResult.signingNonceCommitments",
          value: treeResp.rootNodeSignatureShares.nodeTxSigningResult,
          expected: "Non-null signing nonce commitments",
        },
      );
    }

    if (
      !treeResp.rootNodeSignatureShares.refundTxSigningResult
        ?.signingNonceCommitments
    ) {
      throw new ValidationError(
        "No signing nonce commitments found in tree response",
        {
          field: "refundTxSigningResult.signingNonceCommitments",
        },
      );
    }

    if (
      !treeResp.rootNodeSignatureShares.directNodeTxSigningResult
        ?.signingNonceCommitments
    ) {
      throw new ValidationError(
        "No direct node signing nonce commitments found in tree response",
        {
          field: "directNodeTxSigningResult.signingNonceCommitments",
        },
      );
    }

    if (
      !treeResp.rootNodeSignatureShares.directRefundTxSigningResult
        ?.signingNonceCommitments
    ) {
      throw new ValidationError(
        "No direct refund signing nonce commitments found in tree response",
        {
          field: "directRefundTxSigningResult.signingNonceCommitments",
        },
      );
    }

    if (
      !treeResp.rootNodeSignatureShares.directFromCpfpRefundTxSigningResult
        ?.signingNonceCommitments
    ) {
      throw new ValidationError(
        "No direct from CPFP refund signing nonce commitments found in tree response",
        {
          field: "directFromCpfpRefundTxSigningResult.signingNonceCommitments",
        },
      );
    }

    if (
      !equalBytes(treeResp.rootNodeSignatureShares.verifyingKey, verifyingKey)
    ) {
      throw new ValidationError("Verifying key mismatch", {
        field: "verifyingKey",
        value: treeResp.rootNodeSignatureShares.verifyingKey,
        expected: verifyingKey,
      });
    }

    // Sign all four transactions
    const cpfpRootSignature = await this.config.signer.signFrost({
      message: cpfpRootTxSighash,
      publicKey: signingPubKey,
      keyDerivation,
      verifyingKey,
      selfCommitment: cpfpRootNonceCommitment,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult
          .signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    const directRootSignature = await this.config.signer.signFrost({
      message: directRootTxSighash,
      publicKey: signingPubKey,
      keyDerivation,
      verifyingKey,
      selfCommitment: directRootNonceCommitment,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.directNodeTxSigningResult
          .signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    const cpfpRefundSignature = await this.config.signer.signFrost({
      message: cpfpRefundTxSighash,
      publicKey: signingPubKey,
      keyDerivation,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      selfCommitment: cpfpRefundNonceCommitment,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.refundTxSigningResult
          .signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    const directRefundSignature = await this.config.signer.signFrost({
      message: directRefundTxSighash,
      publicKey: signingPubKey,
      keyDerivation,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      selfCommitment: directRefundNonceCommitment,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.directRefundTxSigningResult
          .signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    const directFromCpfpRefundSignature = await this.config.signer.signFrost({
      message: directFromCpfpRefundTxSighash,
      publicKey: signingPubKey,
      keyDerivation,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      selfCommitment: directFromCpfpRefundNonceCommitment,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.directFromCpfpRefundTxSigningResult
          .signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    // Aggregate all four signatures
    const cpfpRootAggregate = await this.config.signer.aggregateFrost({
      message: cpfpRootTxSighash,
      statechainSignatures:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult.signatureShares,
      statechainPublicKeys:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult.publicKeys,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult
          .signingNonceCommitments,
      selfCommitment: cpfpRootNonceCommitment,
      publicKey: signingPubKey,
      selfSignature: cpfpRootSignature!,
      adaptorPubKey: new Uint8Array(),
    });

    const directRootAggregate = await this.config.signer.aggregateFrost({
      message: directRootTxSighash,
      statechainSignatures:
        treeResp.rootNodeSignatureShares.directNodeTxSigningResult
          .signatureShares,
      statechainPublicKeys:
        treeResp.rootNodeSignatureShares.directNodeTxSigningResult.publicKeys,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.directNodeTxSigningResult
          .signingNonceCommitments,
      selfCommitment: directRootNonceCommitment,
      publicKey: signingPubKey,
      selfSignature: directRootSignature!,
      adaptorPubKey: new Uint8Array(),
    });

    const cpfpRefundAggregate = await this.config.signer.aggregateFrost({
      message: cpfpRefundTxSighash,
      statechainSignatures:
        treeResp.rootNodeSignatureShares.refundTxSigningResult.signatureShares,
      statechainPublicKeys:
        treeResp.rootNodeSignatureShares.refundTxSigningResult.publicKeys,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.refundTxSigningResult
          .signingNonceCommitments,
      selfCommitment: cpfpRefundNonceCommitment,
      publicKey: signingPubKey,
      selfSignature: cpfpRefundSignature!,
      adaptorPubKey: new Uint8Array(),
    });

    const directRefundAggregate = await this.config.signer.aggregateFrost({
      message: directRefundTxSighash,
      statechainSignatures:
        treeResp.rootNodeSignatureShares.directRefundTxSigningResult
          .signatureShares,
      statechainPublicKeys:
        treeResp.rootNodeSignatureShares.directRefundTxSigningResult.publicKeys,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.directRefundTxSigningResult
          .signingNonceCommitments,
      selfCommitment: directRefundNonceCommitment,
      publicKey: signingPubKey,
      selfSignature: directRefundSignature!,
      adaptorPubKey: new Uint8Array(),
    });

    const directFromCpfpRefundAggregate =
      await this.config.signer.aggregateFrost({
        message: directFromCpfpRefundTxSighash,
        statechainSignatures:
          treeResp.rootNodeSignatureShares.directFromCpfpRefundTxSigningResult
            .signatureShares,
        statechainPublicKeys:
          treeResp.rootNodeSignatureShares.directFromCpfpRefundTxSigningResult
            .publicKeys,
        verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
        statechainCommitments:
          treeResp.rootNodeSignatureShares.directFromCpfpRefundTxSigningResult
            .signingNonceCommitments,
        selfCommitment: directFromCpfpRefundNonceCommitment,
        publicKey: signingPubKey,
        selfSignature: directFromCpfpRefundSignature!,
        adaptorPubKey: new Uint8Array(),
      });

    let finalizeResp: FinalizeNodeSignaturesResponse;
    try {
      finalizeResp = await sparkClient.finalize_node_signatures_v2({
        intent: SignatureIntent.CREATION,
        nodeSignatures: [
          {
            nodeId: treeResp.rootNodeSignatureShares.nodeId,
            nodeTxSignature: cpfpRootAggregate,
            refundTxSignature: cpfpRefundAggregate,
            directNodeTxSignature: directRootAggregate,
            directRefundTxSignature: directRefundAggregate,
            directFromCpfpRefundTxSignature: directFromCpfpRefundAggregate,
          },
        ],
      });
    } catch (error) {
      throw new NetworkError(
        "Failed to finalize node signatures",
        {
          operation: "finalize_node_signatures",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
    }

    return finalizeResp;
  }

  /**
   * @deprecated
   * Use createTreeRoot instead.
   * This is currently only used to test backwards compatibility.
   */
  async createTreeWithoutDirectTx({
    keyDerivation,
    verifyingKey,
    depositTx,
    vout,
  }: CreateTreeRootParams) {
    // Create root transactions (CPFP and direct)
    const output = depositTx.getOutput(vout);
    if (!output) {
      throw new ValidationError("Invalid deposit transaction output", {
        field: "vout",
        value: vout,
        expected: "Valid output index",
      });
    }
    const script = output.script;
    const amount = output.amount;
    if (!script || !amount) {
      throw new ValidationError("No script or amount found in deposit tx", {
        field: "output",
        value: output,
        expected: "Output with script and amount",
      });
    }

    const depositOutPoint = {
      txid: hexToBytes(getTxId(depositTx)),
      index: vout,
    };
    const depositTxOut = {
      script,
      amount,
    };

    const [cpfpRootTx, _] = createRootTx(depositOutPoint, depositTxOut);

    // Create nonce commitments for root transactions
    const cpfpRootNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();

    // Get sighashes for root transactions
    const cpfpRootTxSighash = getSigHashFromTx(cpfpRootTx, 0, output);

    const signingPubKey =
      await this.config.signer.getPublicKeyFromDerivation(keyDerivation);

    // Create refund transactions (CPFP and direct)
    const { cpfpRefundTx } = createRefundTxs({
      sequence: INITIAL_SEQUENCE,
      input: { txid: hexToBytes(getTxId(cpfpRootTx)), index: 0 },
      amountSats: amount,
      receivingPubkey: signingPubKey,
      network: this.config.getNetwork(),
    });

    // Create nonce commitments for refund transactions
    const cpfpRefundNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();

    // Get sighashes for refund transactions
    const cpfpRefundTxSighash = getSigHashFromTx(
      cpfpRefundTx,
      0,
      cpfpRootTx.getOutput(0),
    );

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let treeResp: StartDepositTreeCreationResponse;

    try {
      treeResp = await sparkClient.start_deposit_tree_creation({
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
        onChainUtxo: {
          vout: vout,
          rawTx: depositTx.toBytes(true),
          network: this.config.getNetworkProto(),
        },
        rootTxSigningJob: {
          rawTx: cpfpRootTx.toBytes(),
          signingPublicKey: signingPubKey,
          signingNonceCommitment: cpfpRootNonceCommitment.commitment,
        },
        refundTxSigningJob: {
          rawTx: cpfpRefundTx.toBytes(),
          signingPublicKey: signingPubKey,
          signingNonceCommitment: cpfpRefundNonceCommitment.commitment,
        },
      });
    } catch (error) {
      throw new NetworkError(
        "Failed to start deposit tree creation",
        {
          operation: "start_deposit_tree_creation",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
    }

    if (!treeResp.rootNodeSignatureShares?.verifyingKey) {
      throw new ValidationError("No verifying key found in tree response", {
        field: "verifyingKey",
        value: treeResp.rootNodeSignatureShares,
        expected: "Non-null verifying key",
      });
    }

    if (
      !treeResp.rootNodeSignatureShares.nodeTxSigningResult
        ?.signingNonceCommitments
    ) {
      throw new ValidationError(
        "No signing nonce commitments found in tree response",
        {
          field: "nodeTxSigningResult.signingNonceCommitments",
          value: treeResp.rootNodeSignatureShares.nodeTxSigningResult,
          expected: "Non-null signing nonce commitments",
        },
      );
    }

    if (
      !treeResp.rootNodeSignatureShares.refundTxSigningResult
        ?.signingNonceCommitments
    ) {
      throw new ValidationError(
        "No signing nonce commitments found in tree response",
        {
          field: "refundTxSigningResult.signingNonceCommitments",
        },
      );
    }

    if (
      !equalBytes(treeResp.rootNodeSignatureShares.verifyingKey, verifyingKey)
    ) {
      throw new ValidationError("Verifying key mismatch", {
        field: "verifyingKey",
        value: treeResp.rootNodeSignatureShares.verifyingKey,
        expected: verifyingKey,
      });
    }

    const cpfpRootSignature = await this.config.signer.signFrost({
      message: cpfpRootTxSighash,
      publicKey: signingPubKey,
      keyDerivation,
      verifyingKey,
      selfCommitment: cpfpRootNonceCommitment,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult
          .signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    const cpfpRefundSignature = await this.config.signer.signFrost({
      message: cpfpRefundTxSighash,
      publicKey: signingPubKey,
      keyDerivation,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      selfCommitment: cpfpRefundNonceCommitment,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.refundTxSigningResult
          .signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    const cpfpRootAggregate = await this.config.signer.aggregateFrost({
      message: cpfpRootTxSighash,
      statechainSignatures:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult.signatureShares,
      statechainPublicKeys:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult.publicKeys,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.nodeTxSigningResult
          .signingNonceCommitments,
      selfCommitment: cpfpRootNonceCommitment,
      publicKey: signingPubKey,
      selfSignature: cpfpRootSignature!,
      adaptorPubKey: new Uint8Array(),
    });

    const cpfpRefundAggregate = await this.config.signer.aggregateFrost({
      message: cpfpRefundTxSighash,
      statechainSignatures:
        treeResp.rootNodeSignatureShares.refundTxSigningResult.signatureShares,
      statechainPublicKeys:
        treeResp.rootNodeSignatureShares.refundTxSigningResult.publicKeys,
      verifyingKey: treeResp.rootNodeSignatureShares.verifyingKey,
      statechainCommitments:
        treeResp.rootNodeSignatureShares.refundTxSigningResult
          .signingNonceCommitments,
      selfCommitment: cpfpRefundNonceCommitment,
      publicKey: signingPubKey,
      selfSignature: cpfpRefundSignature!,
      adaptorPubKey: new Uint8Array(),
    });

    let finalizeResp: FinalizeNodeSignaturesResponse;
    try {
      finalizeResp = await sparkClient.finalize_node_signatures_v2({
        intent: SignatureIntent.CREATION,
        nodeSignatures: [
          {
            nodeId: treeResp.rootNodeSignatureShares.nodeId,
            nodeTxSignature: cpfpRootAggregate,
            refundTxSignature: cpfpRefundAggregate,
          },
        ],
      });
    } catch (error) {
      throw new NetworkError(
        "Failed to finalize node signatures",
        {
          operation: "finalize_node_signatures",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
    }

    return finalizeResp;
  }
}
