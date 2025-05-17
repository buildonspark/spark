import {
  bytesToNumberBE,
  hexToBytes,
  numberToBytesBE,
} from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { sha256 } from "@scure/btc-signer/utils";
import { decode } from "light-bolt11-decoder";
import { uuidv7 } from "uuidv7";
import { NetworkError, ValidationError } from "../errors/types.js";
import LightningReceiveRequest from "../graphql/objects/LightningReceiveRequest.js";
import {
  GetSigningCommitmentsResponse,
  InitiatePreimageSwapRequest_Reason,
  InitiatePreimageSwapResponse,
  ProvidePreimageResponse,
  QueryUserSignedRefundsResponse,
  RequestedSigningCommitments,
  Transfer,
  UserSignedRefund,
  UserSignedTxSigningJob,
} from "../proto/spark.js";
import {
  getSigHashFromTx,
  getTxFromRawTxBytes,
  getTxId,
} from "../utils/bitcoin.js";
import { getCrypto } from "../utils/crypto.js";
import {
  createRefundTx,
  getNextTransactionSequence,
} from "../utils/transaction.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";
import { LeafKeyTweak } from "./transfer.js";

const crypto = getCrypto();

export type CreateLightningInvoiceParams = {
  invoiceCreator: (
    amountSats: number,
    paymentHash: Uint8Array,
    memo?: string,
  ) => Promise<LightningReceiveRequest | null>;
  amountSats: number;
  memo?: string;
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
};

export class LightningService {
  private readonly config: WalletConfigService;
  private readonly connectionManager: ConnectionManager;

  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager,
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
  }

  async createLightningInvoice({
    invoiceCreator,
    amountSats,
    memo,
  }: CreateLightningInvoiceParams): Promise<LightningReceiveRequest> {
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
    });
  }

  async createLightningInvoiceWithPreImage({
    invoiceCreator,
    amountSats,
    memo,
    preimage,
  }: CreateLightningInvoiceWithPreimageParams): Promise<LightningReceiveRequest> {
    const paymentHash = sha256(preimage);
    const invoice = await invoiceCreator(amountSats, paymentHash, memo);
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

        try {
          await sparkClient.store_preimage_share({
            paymentHash,
            preimageShare: {
              secretShare: numberToBytesBE(share.share, 32),
              proofs: share.proofs,
            },
            threshold: this.config.getThreshold(),
            invoiceString: invoice.invoice.encodedInvoice,
            userIdentityPublicKey:
              await this.config.signer.getIdentityPublicKey(),
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
  }: SwapNodesForPreimageParams): Promise<InitiatePreimageSwapResponse> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let signingCommitments: GetSigningCommitmentsResponse;
    try {
      signingCommitments = await sparkClient.get_signing_commitments({
        nodeIds: leaves.map((leaf) => leaf.leaf.id),
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

    const leafSigningJobs = await this.signRefunds(
      leaves,
      signingCommitments.signingCommitments,
      receiverIdentityPubkey,
    );

    const transferId = uuidv7();
    let bolt11String = "";
    let amountSats: number = 0;
    if (invoiceString) {
      const decodedInvoice = decode(invoiceString);
      let amountMsats = 0;
      try {
        amountMsats = Number(
          decodedInvoice.sections.find((section) => section.name === "amount")
            ?.value,
        );
      } catch (error) {
        console.error("Error decoding invoice", error);
      }

      amountSats = amountMsats / 1000;
      bolt11String = invoiceString;
    }

    const reason = isInboundPayment
      ? InitiatePreimageSwapRequest_Reason.REASON_RECEIVE
      : InitiatePreimageSwapRequest_Reason.REASON_SEND;

    let response: InitiatePreimageSwapResponse;
    try {
      response = await sparkClient.initiate_preimage_swap({
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
          leavesToSend: leafSigningJobs,
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

  private async signRefunds(
    leaves: LeafKeyTweak[],
    signingCommitments: RequestedSigningCommitments[],
    receiverIdentityPubkey: Uint8Array,
  ): Promise<UserSignedTxSigningJob[]> {
    const leafSigningJobs: UserSignedTxSigningJob[] = [];
    for (let i = 0; i < leaves.length; i++) {
      const leaf = leaves[i];
      if (!leaf?.leaf) {
        throw new ValidationError("Leaf not found in signRefunds", {
          field: "leaf",
          value: leaf,
          expected: "Non-null leaf",
        });
      }

      const nodeTx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
      const nodeOutPoint: TransactionInput = {
        txid: hexToBytes(getTxId(nodeTx)),
        index: 0,
      };

      const currRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);
      const { nextSequence } = getNextTransactionSequence(
        currRefundTx.getInput(0).sequence,
      );
      const amountSats = currRefundTx.getOutput(0).amount;
      if (amountSats === undefined) {
        throw new ValidationError("Invalid refund transaction", {
          field: "amount",
          value: currRefundTx.getOutput(0),
          expected: "Non-null amount",
        });
      }

      const refundTx = createRefundTx(
        nextSequence,
        nodeOutPoint,
        amountSats,
        receiverIdentityPubkey,
        this.config.getNetwork(),
      );

      const sighash = getSigHashFromTx(refundTx, 0, nodeTx.getOutput(0));

      const signingCommitment =
        await this.config.signer.getRandomSigningCommitment();

      const signingNonceCommitments =
        signingCommitments[i]?.signingNonceCommitments;
      if (!signingNonceCommitments) {
        throw new ValidationError("Invalid signing commitments", {
          field: "signingNonceCommitments",
          value: signingCommitments[i],
          expected: "Non-null signing nonce commitments",
        });
      }
      const signingResult = await this.config.signer.signFrost({
        message: sighash,
        publicKey: leaf.signingPubKey,
        privateAsPubKey: leaf.signingPubKey,
        selfCommitment: signingCommitment,
        statechainCommitments: signingNonceCommitments,
        adaptorPubKey: new Uint8Array(),
        verifyingKey: leaf.leaf.verifyingPublicKey,
      });

      leafSigningJobs.push({
        leafId: leaf.leaf.id,
        signingPublicKey: leaf.signingPubKey,
        rawTx: refundTx.toBytes(),
        signingNonceCommitment: signingCommitment,
        userSignature: signingResult,
        signingCommitments: {
          signingCommitments: signingNonceCommitments,
        },
      });
    }

    return leafSigningJobs;
  }
}
