import { bytesToNumberBE, numberToBytesBE } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { sha256 } from "@scure/btc-signer/utils";
import { randomUUID } from "crypto";
import {
  GetSigningCommitmentsResponse,
  InitiatePreimageSwapRequest_Reason,
  InitiatePreimageSwapResponse,
  ProvidePreimageResponse,
  QueryUserSignedRefundsResponse,
  RequestedSigningCommitments,
  Transfer,
  UserSignedRefund,
} from "../proto/spark";
import { getTxFromRawTxBytes } from "../utils/bitcoin";
import { splitSecretWithProofs } from "../utils/secret-sharing";
import {
  copySigningCommitment,
  getRandomSigningNonce,
  getSigningCommitmentFromNonce,
} from "../utils/signing";
import { createRefundTx } from "../utils/transaction";
import { signFrost } from "../utils/wasm";
import { KeyPackage } from "../wasm/spark_bindings";
import { WalletConfigService } from "./config";
import { ConnectionManager } from "./connection";
import { LeafKeyTweak } from "./transfer";

export type CreateLightningInvoiceParams = {
  invoiceCreator: (
    amountSats: number,
    paymentHash: Uint8Array,
    memo: string
  ) => Promise<string>;
  amountSats: number;
  memo: string;
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
    connectionManager: ConnectionManager
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
  }

  async createLightningInvoice({
    invoiceCreator,
    amountSats,
    memo,
  }: CreateLightningInvoiceParams): Promise<string> {
    const preimagePrivKey = secp256k1.utils.randomPrivateKey();
    return await this.createLightningInvoiceWithPreImage({
      invoiceCreator,
      amountSats,
      memo,
      preimage: preimagePrivKey,
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

    const preimageAsInt = bytesToNumberBE(preimage);
    const shares = splitSecretWithProofs(
      preimageAsInt,
      secp256k1.CURVE.n,
      this.config.getConfig().threshold,
      Object.keys(this.config.getConfig().signingOperators).length
    );

    const errors: Error[] = [];
    const promises = Object.entries(
      this.config.getConfig().signingOperators
    ).map(async ([_, operator]) => {
      const share = shares[operator.id];

      const sparkClient = await this.connectionManager.createSparkClient(
        operator.address,
        this.config
      );

      try {
        await sparkClient.store_preimage_share({
          paymentHash,
          preimageShare: {
            secretShare: numberToBytesBE(share.share, 32),
            proofs: share.proofs,
          },
          threshold: this.config.getConfig().threshold,
          invoiceString: invoice,
          userIdentityPublicKey: this.config.getIdentityPublicKey(),
        });
      } catch (e: any) {
        errors.push(e);
      } finally {
        sparkClient.close?.();
      }
    });

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
      this.config
    );

    let signingCommitments: GetSigningCommitmentsResponse;
    try {
      signingCommitments = await sparkClient.get_signing_commitments({
        nodeIds: leaves.map((leaf) => leaf.leaf.id),
      });
    } catch (error) {
      sparkClient.close?.();
      throw new Error(`Error getting signing commitments: ${error}`);
    }

    const userSignedRefunds = this.signRefunds(
      leaves,
      signingCommitments.signingCommitments,
      receiverIdentityPubkey
    );

    const transferId = randomUUID();
    const bolt11String = invoiceString ?? "";

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
        },
        transfer: {
          transferId,
          ownerIdentityPublicKey: this.config.getIdentityPublicKey(),
          receiverIdentityPublicKey: receiverIdentityPubkey,
        },
        receiverIdentityPublicKey: receiverIdentityPubkey,
      });
    } catch (error) {
      throw new Error(`Error initiating preimage swap: ${error}`);
    } finally {
      sparkClient.close?.();
    }

    return response;
  }

  async queryUserSignedRefunds(
    paymentHash: Uint8Array
  ): Promise<UserSignedRefund[]> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
      this.config
    );

    let response: QueryUserSignedRefundsResponse;
    try {
      response = await sparkClient.query_user_signed_refunds({
        paymentHash,
      });
    } catch (error) {
      throw new Error(`Error querying user signed refunds: ${error}`);
    } finally {
      sparkClient.close?.();
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
      this.config
    );

    const paymentHash = sha256(preimage);
    let response: ProvidePreimageResponse;
    try {
      response = await sparkClient.provide_preimage({
        preimage,
        paymentHash,
      });
    } catch (error) {
      sparkClient.close?.();
      throw new Error(`Error providing preimage: ${error}`);
    }

    if (!response.transfer) {
      throw new Error("No transfer returned from coordinator");
    }

    return response.transfer;
  }

  private signRefunds(
    leaves: LeafKeyTweak[],
    signingCommitments: RequestedSigningCommitments[],
    receiverIdentityPubkey: Uint8Array
  ): UserSignedRefund[] {
    const userSignedRefunds: UserSignedRefund[] = [];
    for (let i = 0; i < leaves.length; i++) {
      const leaf = leaves[i];
      const { refundTx, sighash } = createRefundTx(
        leaf.leaf,
        receiverIdentityPubkey,
        this.config.getConfig().network
      );

      const signingNonce = getRandomSigningNonce();
      const signingCommitment = getSigningCommitmentFromNonce(signingNonce);

      const signingPubKey = secp256k1.getPublicKey(leaf.signingPrivKey);
      const keyPackage = new KeyPackage(
        leaf.signingPrivKey,
        signingPubKey,
        leaf.leaf.verifyingPublicKey
      );

      const signingResult = signFrost({
        msg: sighash,
        keyPackage,
        nonce: signingNonce,
        selfCommitment: copySigningCommitment(signingCommitment),
        statechainCommitments: signingCommitments[i].signingNonceCommitments,
      });

      userSignedRefunds.push({
        nodeId: leaf.leaf.id,
        refundTx: refundTx.toBytes(),
        userSignature: signingResult,
        userSignatureCommitment: signingCommitment,
        signingCommitments: {
          signingCommitments: signingCommitments[i].signingNonceCommitments,
        },
      });
    }

    return userSignedRefunds;
  }
}
