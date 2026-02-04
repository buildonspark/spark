import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { sha256 } from "@noble/hashes/sha2";
import { hexToBytes } from "@noble/hashes/utils";
import { p2tr, Transaction } from "@scure/btc-signer";
import { SparkRequestError, SparkValidationError } from "../errors/types.js";
import {
  Address,
  FinalizeDepositTreeCreationResponse,
  GenerateDepositAddressResponse,
  GetSigningCommitmentsResponse,
  HashVariant,
} from "../proto/spark.js";
import { KeyDerivation } from "../signer/types.js";
import { getSigHashFromTx } from "../utils/bitcoin.js";
import { subtractPublicKeys } from "../utils/keys.js";
import { getNetwork } from "../utils/network.js";
import { proofOfPossessionMessageHashForDepositAddress } from "../utils/proof.js";
import {
  createInitialTimelockRefundTxs,
  createRootNodeTx,
} from "../utils/transaction.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection/connection.js";

type ValidateDepositAddressParams = {
  address: Address;
  userPubkey: Uint8Array;
  verifyCoordinatorProof?: boolean;
};

export type GenerateStaticDepositAddressParams = {
  signingPubkey: Uint8Array;
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
    verifyCoordinatorProof = false,
  }: ValidateDepositAddressParams) {
    if (
      !address.depositAddressProof ||
      !address.depositAddressProof.proofOfPossessionSignature ||
      !address.depositAddressProof.addressSignatures
    ) {
      throw new SparkValidationError(
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
      throw new SparkValidationError(
        "Proof of possession signature verification failed",
        {
          field: "proofOfPossessionSignature",
          value: address.depositAddressProof.proofOfPossessionSignature,
        },
      );
    }

    const addrHash = sha256(address.address);
    for (const operator of Object.values(this.config.getSigningOperators())) {
      if (
        operator.identifier === this.config.getCoordinatorIdentifier() &&
        !verifyCoordinatorProof
      ) {
        continue;
      }

      const operatorPubkey = hexToBytes(operator.identityPublicKey);
      const operatorSig =
        address.depositAddressProof.addressSignatures[operator.identifier];
      if (!operatorSig) {
        throw new SparkValidationError("Operator signature not found", {
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
        throw new SparkValidationError(
          "Operator signature verification failed",
          {
            field: "operatorSignature",
            value: operatorSig,
          },
        );
      }
    }
  }

  async generateStaticDepositAddress({
    signingPubkey,
  }: GenerateStaticDepositAddressParams): Promise<GenerateDepositAddressResponse> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let depositResp: GenerateDepositAddressResponse;
    try {
      depositResp = await sparkClient.generate_static_deposit_address({
        signingPublicKey: signingPubkey,
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
        network: this.config.getNetworkProto(),
        hashVariant: HashVariant.HASH_VARIANT_V2,
      });
    } catch (error) {
      throw new SparkRequestError("Failed to generate static deposit address", {
        operation: "generate_static_deposit_address",
        error,
      });
    }

    if (!depositResp.depositAddress) {
      throw new SparkValidationError(
        "No static deposit address response from coordinator",
        {
          field: "depositAddress",
          value: depositResp,
        },
      );
    }

    await this.validateDepositAddress({
      address: depositResp.depositAddress,
      userPubkey: signingPubkey,
      verifyCoordinatorProof: true,
    });

    return depositResp;
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
        hashVariant: HashVariant.HASH_VARIANT_V2,
      });
    } catch (error) {
      throw new SparkRequestError("Failed to generate deposit address", {
        operation: "generate_deposit_address",
        error,
      });
    }

    if (!depositResp.depositAddress) {
      throw new SparkValidationError(
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
      throw new SparkValidationError("Invalid deposit transaction output", {
        field: "vout",
        value: vout,
        expected: "Valid output index",
      });
    }

    const { nodeTx: cpfpRootTx } = createRootNodeTx(depositTx, vout);

    // Create nonce commitments for root transactions
    const cpfpRootNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();

    // Get sighashes for root transactions
    const cpfpRootTxSighash = getSigHashFromTx(cpfpRootTx, 0, output);

    const signingPubKey =
      await this.config.signer.getPublicKeyFromDerivation(keyDerivation);

    const { cpfpRefundTx, directFromCpfpRefundTx } =
      createInitialTimelockRefundTxs({
        nodeTx: cpfpRootTx,
        receivingPubkey: signingPubKey,
        network: this.config.getNetwork(),
      });

    // Create nonce commitments for refund transactions
    const cpfpRefundNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();
    const directFromCpfpRefundNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();

    // Get sighashes for refund transactions
    const cpfpRefundTxSighash = getSigHashFromTx(
      cpfpRefundTx,
      0,
      cpfpRootTx.getOutput(0),
    );

    if (!directFromCpfpRefundTx) {
      throw new SparkValidationError(
        "Expected direct from cpfp refund transaction for tree creation",
        {
          field: "directFromCpfpRefundTx",
          value: directFromCpfpRefundTx,
        },
      );
    }

    const directFromCpfpRefundTxSighash = getSigHashFromTx(
      directFromCpfpRefundTx,
      0,
      cpfpRootTx.getOutput(0),
    );

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let signingCommittmentResp: GetSigningCommitmentsResponse;

    try {
      signingCommittmentResp = await sparkClient.get_signing_commitments({
        count: 3,
        nodeIdCount: 1,
      });
    } catch (error) {
      throw new SparkRequestError("Failed to start deposit tree creation", {
        operation: "get_signing_commitments",
        error,
      });
    }

    if (signingCommittmentResp.signingCommitments.length !== 3) {
      throw new SparkValidationError(
        "Incorrect number of signing commitments returned",
        {
          field: "signingCommitments",
          value: signingCommittmentResp.signingCommitments.length,
          expected: 3,
        },
      );
    }

    const [
      cpfpRootCommitment,
      cpfpRefundCommitment,
      directFromCpfpRefundCommitment,
    ] = signingCommittmentResp.signingCommitments;
    if (cpfpRootCommitment === undefined) {
      throw new SparkValidationError(
        "Empty root commitment returned from get_signing_commitments",
      );
    }
    if (cpfpRefundCommitment === undefined) {
      throw new SparkValidationError(
        "Empty refund commitment returned from get_signing_commitments",
      );
    }
    if (directFromCpfpRefundCommitment === undefined) {
      throw new SparkValidationError(
        "Empty direct from cpfp refund commitment returned from get_signing_commitments",
      );
    }

    // Sign all three transactions
    const cpfpRootSignature = await this.config.signer.signFrost({
      message: cpfpRootTxSighash,
      publicKey: signingPubKey,
      keyDerivation,
      verifyingKey,
      selfCommitment: cpfpRootNonceCommitment,
      statechainCommitments: cpfpRootCommitment.signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    const cpfpRefundSignature = await this.config.signer.signFrost({
      message: cpfpRefundTxSighash,
      publicKey: signingPubKey,
      keyDerivation,
      verifyingKey,
      selfCommitment: cpfpRefundNonceCommitment,
      statechainCommitments: cpfpRefundCommitment.signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    const directFromCpfpRefundSignature = await this.config.signer.signFrost({
      message: directFromCpfpRefundTxSighash,
      publicKey: signingPubKey,
      keyDerivation,
      verifyingKey,
      selfCommitment: directFromCpfpRefundNonceCommitment,
      statechainCommitments:
        directFromCpfpRefundCommitment.signingNonceCommitments,
      adaptorPubKey: new Uint8Array(),
    });

    let finalizeResp: FinalizeDepositTreeCreationResponse;

    try {
      finalizeResp = await sparkClient.finalize_deposit_tree_creation({
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
        onChainUtxo: {
          vout: vout,
          rawTx: depositTx.toBytes(true),
          network: this.config.getNetworkProto(),
        },
        rootTxSigningJob: {
          signingPublicKey: signingPubKey,
          rawTx: cpfpRootTx.toBytes(),
          signingNonceCommitment: cpfpRootNonceCommitment.commitment,
          userSignature: cpfpRootSignature,
          signingCommitments: {
            signingCommitments: cpfpRootCommitment.signingNonceCommitments,
          },
        },
        refundTxSigningJob: {
          signingPublicKey: signingPubKey,
          rawTx: cpfpRefundTx.toBytes(),
          signingNonceCommitment: cpfpRefundNonceCommitment.commitment,
          userSignature: cpfpRefundSignature,
          signingCommitments: {
            signingCommitments: cpfpRefundCommitment.signingNonceCommitments,
          },
        },
        directFromCpfpRefundTxSigningJob: {
          signingPublicKey: signingPubKey,
          rawTx: directFromCpfpRefundTx.toBytes(),
          signingNonceCommitment:
            directFromCpfpRefundNonceCommitment.commitment,
          userSignature: directFromCpfpRefundSignature,
          signingCommitments: {
            signingCommitments:
              directFromCpfpRefundCommitment.signingNonceCommitments,
          },
        },
      });
    } catch (error) {
      throw new SparkRequestError("Failed to finalize tree creation", {
        operation: "finalize_deposit_tree_creation",
        error,
      });
    }

    if (finalizeResp.rootNode === undefined) {
      throw new SparkRequestError(
        "root node not returned from finalize tree request",
        {
          operation: "finalize_deposit_tree_creation",
        },
      );
    }
    return { nodes: [finalizeResp.rootNode] };
  }
}
