import {
  bytesToNumberBE,
  hexToBytes,
  numberToBytesBE,
} from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { sha256 } from "@scure/btc-signer/utils";
import { decode } from "light-bolt11-decoder";
import {
  GetSigningCommitmentsResponse,
  InitiatePreimageSwapRequest_Reason,
  InitiatePreimageSwapResponse,
  ProvidePreimageResponse,
  QueryUserSignedRefundsResponse,
  RequestedSigningCommitments,
  Transfer,
  UserSignedRefund,
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
  ) => Promise<string | undefined>;
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
  }: CreateLightningInvoiceParams): Promise<string> {
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
  }: CreateLightningInvoiceWithPreimageParams): Promise<string> {
    const paymentHash = sha256(preimage);
    const invoice = await invoiceCreator(amountSats, paymentHash, memo);
    if (!invoice) {
      throw new Error("Error creating lightning invoice");
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
          throw new Error("Share not found");
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
            invoiceString: invoice,
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
      throw new Error(`Error creating lightning invoice: ${errors[0]}`);
    }

    return invoice;
  }

  async swapNodesForPreimage({
    leaves,
    receiverIdentityPubkey,
    paymentHash,
    invoiceString,
    isInboundPayment,
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
      throw new Error(`Error getting signing commitments: ${error}`);
    }

    const userSignedRefunds = await this.signRefunds(
      leaves,
      signingCommitments.signingCommitments,
      receiverIdentityPubkey,
    );

    const transferId = crypto.randomUUID();
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
        userSignedRefunds,
        reason,
        invoiceAmount: {
          invoiceAmountProof: {
            bolt11Invoice: bolt11String,
          },
          valueSats: amountSats,
        },
        transfer: {
          transferId,
          ownerIdentityPublicKey:
            await this.config.signer.getIdentityPublicKey(),
          receiverIdentityPublicKey: receiverIdentityPubkey,
          expiryTime: new Date(Date.now() + 2 * 60 * 1000),
        },
        receiverIdentityPublicKey: receiverIdentityPubkey,
        feeSats: 0,
      });
    } catch (error) {
      throw new Error(`Error initiating preimage swap: ${error}`);
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
      });
    } catch (error) {
      throw new Error(`Error querying user signed refunds: ${error}`);
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
      });
    } catch (error) {
      throw new Error(`Error providing preimage: ${error}`);
    }

    if (!response.transfer) {
      throw new Error("No transfer returned from coordinator");
    }

    return response.transfer;
  }

  private async signRefunds(
    leaves: LeafKeyTweak[],
    signingCommitments: RequestedSigningCommitments[],
    receiverIdentityPubkey: Uint8Array,
  ): Promise<UserSignedRefund[]> {
    const userSignedRefunds: UserSignedRefund[] = [];
    for (let i = 0; i < leaves.length; i++) {
      const leaf = leaves[i];
      if (!leaf?.leaf) {
        throw new Error("Leaf not found in signRefunds");
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
        throw new Error("Amount not found in signRefunds");
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
        throw new Error("Signing nonce commitments not found in signRefunds");
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

      userSignedRefunds.push({
        nodeId: leaf.leaf.id,
        refundTx: refundTx.toBytes(),
        userSignature: signingResult,
        userSignatureCommitment: signingCommitment,
        signingCommitments: {
          signingCommitments: signingNonceCommitments,
        },
        network: this.config.getNetworkProto(),
      });
    }

    return userSignedRefunds;
  }
}
