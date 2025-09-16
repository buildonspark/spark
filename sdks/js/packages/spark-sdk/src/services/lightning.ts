import { secp256k1 } from "@noble/curves/secp256k1";
import {
  bytesToNumberBE,
  hexToBytes,
  numberToBytesBE,
} from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { uuidv7 } from "uuidv7";
import { NetworkError, ValidationError } from "../errors/types.js";
import LightningReceiveRequest from "../graphql/objects/LightningReceiveRequest.js";
import {
  GetSigningCommitmentsResponse,
  InitiatePreimageSwapRequest_Reason,
  InitiatePreimageSwapResponse,
  ProvidePreimageResponse,
  QueryUserSignedRefundsResponse,
  Transfer,
  UserSignedRefund,
} from "../proto/spark.js";
import { getTxFromRawTxBytes } from "../utils/bitcoin.js";
import { getCrypto } from "../utils/crypto.js";
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
};

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
      throw new ValidationError("Failed to create lightning invoice", {
        field: "invoice",
        value: null,
        expected: "Non-null invoice",
      });
    }

    const shares = await this.config.signer.splitSecretWithProofs({
      secret: preimage,
      curveOrder: secp256k1.CURVE.n,
      threshold: this.config.getThreshold(),
      numShares: Object.keys(this.config.getSigningOperators()).length,
    });

    const errors: Error[] = [];
    const promises = Object.entries(this.config.getSigningOperators()).map(
      async ([_, operator]) => {
        const share = shares[operator.id];
        if (!share) {
          throw new ValidationError("Share not found for operator", {
            field: "share",
            value: operator.id,
            expected: "Non-null share",
          });
        }

        const sparkClient = await this.connectionManager.createSparkClient(
          operator.address,
        );

        const userIdentityPublicKey = receiverIdentityPubkey
          ? hexToBytes(receiverIdentityPubkey)
          : await this.config.signer.getIdentityPublicKey();

        try {
          await sparkClient.store_preimage_share({
            paymentHash,
            preimageShare: {
              secretShare: numberToBytesBE(share.share, 32),
              proofs: share.proofs,
            },
            threshold: this.config.getThreshold(),
            invoiceString: invoice.invoice.encodedInvoice,
            userIdentityPublicKey,
          });
        } catch (e: any) {
          errors.push(e);
        }
      },
    );

    await Promise.all(promises);

    if (errors.length > 0) {
      throw new NetworkError(
        "Failed to store preimage shares",
        {
          operation: "store_preimage_share",
          errorCount: errors.length,
          errors: errors.map((e) => e.message).join(", "),
        },
        errors[0],
      );
    }

    return invoice;
  }

  async swapNodesForPreimage({
    leaves,
    receiverIdentityPubkey,
    paymentHash,
    invoiceString,
    isInboundPayment,
    feeSats = 0,
    amountSatsToSend,
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
      throw new NetworkError(
        "Failed to get signing commitments",
        {
          operation: "get_signing_commitments",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
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

    const transferId = uuidv7();
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
        throw new ValidationError(
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
        throw new ValidationError("Invalid amount", {
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
    try {
      response = await sparkClient.initiate_preimage_swap_v2({
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
          directLeavesToSend: directLeafSigningJobs,
          directFromCpfpLeavesToSend: directFromCpfpLeafSigningJobs,
          receiverIdentityPublicKey: receiverIdentityPubkey,
          expiryTime: new Date(Date.now() + 2 * 60 * 1000),
        },
        receiverIdentityPublicKey: receiverIdentityPubkey,
        feeSats,
      });
    } catch (error) {
      throw new NetworkError(
        "Failed to initiate preimage swap",
        {
          operation: "initiate_preimage_swap",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
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
      throw new NetworkError(
        "Failed to query user signed refunds",
        {
          operation: "query_user_signed_refunds",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
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
      throw new NetworkError(
        "Failed to provide preimage",
        {
          operation: "provide_preimage",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
    }

    if (!response.transfer) {
      throw new ValidationError("No transfer returned from coordinator", {
        field: "transfer",
        value: response,
        expected: "Non-null transfer",
      });
    }

    return response.transfer;
  }
}
