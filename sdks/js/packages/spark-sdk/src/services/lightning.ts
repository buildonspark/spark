import { secp256k1 } from "@noble/curves/secp256k1";
import {
  bytesToNumberBE,
  hexToBytes,
  numberToBytesBE,
} from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { uuidv7 } from "uuidv7";
import { SparkRequestError, SparkValidationError } from "../errors/types.js";
import LightningReceiveRequest from "../graphql/objects/LightningReceiveRequest.js";
import {
  GetSigningCommitmentsResponse,
  InitiatePreimageSwapRequest_Reason,
  InitiatePreimageSwapResponse,
  ProvidePreimageResponse,
  QueryUserSignedRefundsResponse,
  SecretShare as SecretShareProto,
  Transfer,
  StartTransferRequest,
  UserSignedRefund,
} from "../proto/spark.js";
import { getSparkFrost } from "../spark-bindings/spark-bindings.js";
import { getTxFromRawTxBytes } from "../utils/bitcoin.js";
import { getCrypto } from "../utils/crypto.js";
import { newHasher } from "../utils/hashstructure.js";
import {
  optionsWithIdempotencyKey,
  type IdempotencyOptions,
} from "../utils/idempotency.js";
import { decodeInvoice } from "./bolt11-spark.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection/connection.js";
import { SigningService } from "./signing.js";
import type { LeafKeyTweak } from "./transfer.js";

export type CreateLightningInvoiceParams = {
  invoiceCreator: (
    amountSats: number,
    paymentHash: Uint8Array,
    memo?: string,
    receiverIdentityPubkey?: string,
    descriptionHash?: string,
  ) => Promise<LightningReceiveRequest | null>;
  amountSats: number;
  memo?: string;
  receiverIdentityPubkey?: string;
  descriptionHash?: string;
};

export type CreateLightningInvoiceWithPreimageParams = {
  preimage: Uint8Array;
} & CreateLightningInvoiceParams;

export type SwapNodesForPreimageParams = {
  leaves: LeafKeyTweak[];
  receiverIdentityPubkey: Uint8Array;
  paymentHash: Uint8Array;
  invoiceString?: string;
  isInboundPayment: boolean;
  feeSats?: number;
  amountSatsToSend?: number;
  startTransferRequest?: StartTransferRequest;
  expiryTime?: Date;
  transferID?: string;
} & IdempotencyOptions;

export class LightningService {
  private readonly config: WalletConfigService;
  private readonly connectionManager: ConnectionManager;
  private readonly signingService: SigningService;
  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager,
    signingService: SigningService,
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
    this.signingService = signingService;
  }

  async createLightningInvoice({
    invoiceCreator,
    amountSats,
    memo,
    receiverIdentityPubkey,
    descriptionHash,
  }: CreateLightningInvoiceParams): Promise<LightningReceiveRequest> {
    const crypto = getCrypto();
    const randBytes = crypto.getRandomValues(new Uint8Array(32));
    const preimage = numberToBytesBE(
      bytesToNumberBE(randBytes) % secp256k1.CURVE.n,
      32,
    );
    return await this.createLightningInvoiceWithPreImage({
      invoiceCreator,
      amountSats,
      memo,
      preimage,
      receiverIdentityPubkey,
      descriptionHash,
    });
  }

  async createLightningInvoiceWithPreImage({
    invoiceCreator,
    amountSats,
    memo,
    preimage,
    receiverIdentityPubkey,
    descriptionHash,
  }: CreateLightningInvoiceWithPreimageParams): Promise<LightningReceiveRequest> {
    const paymentHash = sha256(preimage);
    const invoice = await invoiceCreator(
      amountSats,
      paymentHash,
      memo,
      receiverIdentityPubkey,
      descriptionHash,
    );
    if (!invoice) {
      throw new SparkValidationError("Failed to create lightning invoice", {
        field: "invoice",
        value: null,
        expected: "Non-null invoice",
      });
    }

    const signingOperators = this.config.getSigningOperators();
    const shares = await this.config.signer.splitSecretWithProofs({
      secret: preimage,
      curveOrder: secp256k1.CURVE.n,
      threshold: this.config.getThreshold(),
      numShares: Object.keys(signingOperators).length,
    });

    const sparkFrost = getSparkFrost();
    const encryptedShares: Record<string, Uint8Array> = {};
    for (const [identifier, operator] of Object.entries(signingOperators)) {
      const share = shares[operator.id];
      if (!share) {
        throw new SparkValidationError("Share not found for operator", {
          field: "share",
          value: operator.id,
          expected: "Non-null share",
        });
      }

      const shareProto: SecretShareProto = {
        secretShare: numberToBytesBE(share.share, 32),
        proofs: share.proofs,
      };
      const shareBytes = SecretShareProto.encode(shareProto).finish();

      const encrypted = await sparkFrost.encryptEcies(
        shareBytes,
        hexToBytes(operator.identityPublicKey),
      );
      encryptedShares[identifier] = Uint8Array.from(encrypted);
    }

    const invoiceString = invoice.invoice.encodedInvoice;
    const threshold = this.config.getThreshold();
    const signingPayload = newHasher([
      "spark",
      "store_preimage_share",
      "signing payload",
    ])
      .addBytes(paymentHash)
      .addMapStringToBytes(encryptedShares)
      .addUint32(threshold)
      .addString(invoiceString)
      .hash();

    const userSignature =
      await this.config.signer.signMessageWithIdentityKey(signingPayload);

    const userIdentityPublicKey =
      await this.config.signer.getIdentityPublicKey();

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      await sparkClient.store_preimage_share_v2({
        paymentHash,
        encryptedPreimageShares: encryptedShares,
        threshold,
        invoiceString,
        userIdentityPublicKey,
        userSignature,
      });
    } catch (error) {
      throw new SparkRequestError("Failed to store preimage shares", {
        operation: "store_preimage_share_v2",
        error,
      });
    }

    return invoice;
  }

  /**
   * Swap nodes for preimage
   * @param leaves - The leaves to swap for preimage
   * @param receiverIdentityPubkey - The receiver identity public key
   * @param paymentHash - The payment hash
   * @param invoiceString - The invoice string
   * @param isInboundPayment - Whether the payment is inbound
   * @param feeSats - The fee in sats
   * @param amountSatsToSend - The amount in sats to send
   * @param expiryTime - The expiry time
   * @param startTransferRequest - The start transfer request, do not populate if is inbound payment
   * @param transferID - The transfer ID, do not populate if is inbound payment
   */
  async swapNodesForPreimage({
    leaves,
    receiverIdentityPubkey,
    paymentHash,
    invoiceString,
    isInboundPayment,
    feeSats = 0,
    amountSatsToSend,
    expiryTime,
    startTransferRequest,
    transferID,
    idempotencyKey,
  }: SwapNodesForPreimageParams): Promise<InitiatePreimageSwapResponse> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    // Get signing commitments for all transaction types in one coordinated call
    let signingCommitments: GetSigningCommitmentsResponse;
    try {
      signingCommitments = await sparkClient.get_signing_commitments({
        nodeIds: leaves.map((leaf) => leaf.leaf.id),
        count: 3,
      });
    } catch (error) {
      throw new SparkRequestError("Failed to get signing commitments", {
        operation: "get_signing_commitments",
        error,
      });
    }

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

    const transferId = transferID ? transferID : uuidv7();
    let bolt11String = "";
    let amountSats: number = 0;
    if (invoiceString) {
      const decodedInvoice = decodeInvoice(invoiceString);
      let amountMsats = 0;
      try {
        amountMsats = Number(decodedInvoice.amountMSats);
      } catch (error) {
        console.error("Error decoding invoice", error);
      }

      const isZeroAmountInvoice = !amountMsats;

      if (isZeroAmountInvoice && amountSatsToSend === undefined) {
        throw new SparkValidationError(
          "Invalid amount. User must specify amountSatsToSend for 0 amount lightning invoice",
          {
            field: "amountSatsToSend",
            value: amountSatsToSend,
            expected: "positive number",
          },
        );
      }

      amountSats = isZeroAmountInvoice
        ? amountSatsToSend!
        : Math.ceil(amountMsats / 1000);

      if (isNaN(amountSats) || amountSats <= 0) {
        throw new SparkValidationError("Invalid amount", {
          field: "amountSats",
          value: amountSats,
          expected: "greater than 0",
        });
      }

      bolt11String = invoiceString;
    }

    const reason = isInboundPayment
      ? InitiatePreimageSwapRequest_Reason.REASON_RECEIVE
      : InitiatePreimageSwapRequest_Reason.REASON_SEND;

    let response: InitiatePreimageSwapResponse;
    // TODO(LIG-8126): Remove transfer inputs once SDK upgrade is complete
    try {
      response = await sparkClient.initiate_preimage_swap_v3(
        {
          paymentHash,
          invoiceAmount: {
            invoiceAmountProof: {
              bolt11Invoice: bolt11String,
            },
            valueSats: amountSats,
          },
          reason,
          transfer: {
            transferId,
            ownerIdentityPublicKey:
              await this.config.signer.getIdentityPublicKey(),
            leavesToSend: cpfpLeafSigningJobs,
            directLeavesToSend: startTransferRequest
              ? undefined
              : directLeafSigningJobs,
            directFromCpfpLeavesToSend: startTransferRequest
              ? undefined
              : directFromCpfpLeafSigningJobs,
            receiverIdentityPublicKey: receiverIdentityPubkey,
            expiryTime,
          },
          receiverIdentityPublicKey: receiverIdentityPubkey,
          feeSats,
          transferRequest: startTransferRequest,
        },
        idempotencyKey ? optionsWithIdempotencyKey(idempotencyKey) : undefined,
      );
    } catch (error) {
      throw new SparkRequestError("Failed to initiate preimage swap", {
        operation: "initiate_preimage_swap_v3",
        error,
      });
    }

    return response;
  }

  async queryUserSignedRefunds(
    paymentHash: Uint8Array,
  ): Promise<UserSignedRefund[]> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let response: QueryUserSignedRefundsResponse;
    try {
      response = await sparkClient.query_user_signed_refunds({
        paymentHash,
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
      });
    } catch (error) {
      throw new SparkRequestError("Failed to query user signed refunds", {
        operation: "query_user_signed_refunds",
        error,
      });
    }

    return response.userSignedRefunds;
  }

  validateUserSignedRefund(userSignedRefund: UserSignedRefund): bigint {
    const refundTx = getTxFromRawTxBytes(userSignedRefund.refundTx);
    // TODO: Should we assert that the amount is always defined here?
    return refundTx.getOutput(0).amount || 0n;
  }

  async providePreimage(preimage: Uint8Array): Promise<Transfer> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const paymentHash = sha256(preimage);
    let response: ProvidePreimageResponse;
    try {
      response = await sparkClient.provide_preimage({
        preimage,
        paymentHash,
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
      });
    } catch (error) {
      throw new SparkRequestError("Failed to provide preimage", {
        operation: "provide_preimage",
        error,
      });
    }

    if (!response.transfer) {
      throw new SparkValidationError("No transfer returned from coordinator", {
        field: "transfer",
        value: response,
        expected: "Non-null transfer",
      });
    }

    return response.transfer;
  }
}
