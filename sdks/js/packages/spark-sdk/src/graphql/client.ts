import {
  AuthProvider,
  bytesToHex,
  DefaultCrypto,
  isError,
  NodeKeyCache,
  Query,
  Requester,
} from "@lightsparkdev/core";
import { sha256 } from "@noble/hashes/sha2";
import { AuthenticationError, NetworkError } from "../errors/index.js";
import { SparkSigner } from "../signer/signer.js";
import { UserRequestType } from "../types/sdk-types.js";
import { getFetch } from "../utils/fetch.js";
import { ClaimStaticDeposit } from "./mutations/ClaimStaticDeposit.js";
import { CompleteCoopExit } from "./mutations/CompleteCoopExit.js";
import { CompleteLeavesSwap } from "./mutations/CompleteLeavesSwap.js";
import { GetChallenge } from "./mutations/GetChallenge.js";
import { RequestCoopExit } from "./mutations/RequestCoopExit.js";
import { RequestLightningReceive } from "./mutations/RequestLightningReceive.js";
import { RequestLightningSend } from "./mutations/RequestLightningSend.js";
import { RequestSwapLeaves } from "./mutations/RequestSwapLeaves.js";
import { VerifyChallenge } from "./mutations/VerifyChallenge.js";
import { ClaimStaticDepositFromJson } from "./objects/ClaimStaticDeposit.js";
import ClaimStaticDepositOutput, {
  ClaimStaticDepositOutputFromJson,
} from "./objects/ClaimStaticDepositOutput.js";
import ClaimStaticDepositRequestType from "./objects/ClaimStaticDepositRequestType.js";
import { CoopExitFeeEstimatesOutputFromJson } from "./objects/CoopExitFeeEstimatesOutput.js";
import { CoopExitFeeQuoteFromJson } from "./objects/CoopExitFeeQuote.js";
import CoopExitRequest, {
  CoopExitRequestFromJson,
} from "./objects/CoopExitRequest.js";
import { GetChallengeOutputFromJson } from "./objects/GetChallengeOutput.js";
import {
  BitcoinNetwork,
  CompleteCoopExitInput,
  CompleteLeavesSwapInput,
  CoopExitFeeEstimatesInput,
  CoopExitFeeEstimatesOutput,
  CoopExitFeeQuote,
  CoopExitFeeQuoteInput,
  GetChallengeOutput,
  LeavesSwapFeeEstimateOutput,
  LightningSendRequest,
  RequestCoopExitInput,
  RequestLeavesSwapInput,
  RequestLightningReceiveInput,
  RequestLightningSendInput,
  Transfer,
} from "./objects/index.js";
import { LeavesSwapFeeEstimateOutputFromJson } from "./objects/LeavesSwapFeeEstimateOutput.js";
import LeavesSwapRequest, {
  LeavesSwapRequestFromJson,
} from "./objects/LeavesSwapRequest.js";
import LightningReceiveRequest, {
  LightningReceiveRequestFromJson,
} from "./objects/LightningReceiveRequest.js";
import LightningSendFeeEstimateOutput, {
  LightningSendFeeEstimateOutputFromJson,
} from "./objects/LightningSendFeeEstimateOutput.js";
import { LightningSendRequestFromJson } from "./objects/LightningSendRequest.js";
import StaticDepositQuoteInput from "./objects/StaticDepositQuoteInput.js";
import StaticDepositQuoteOutput, {
  StaticDepositQuoteOutputFromJson,
} from "./objects/StaticDepositQuoteOutput.js";
import { TransferFromJson } from "./objects/Transfer.js";
import VerifyChallengeOutput, {
  VerifyChallengeOutputFromJson,
} from "./objects/VerifyChallengeOutput.js";
import { CoopExitFeeEstimate } from "./queries/CoopExitFeeEstimate.js";
import { GetClaimDepositQuote } from "./queries/GetClaimDepositQuote.js";
import { GetCoopExitFeeQuote } from "./queries/GetCoopExitFeeQuote.js";
import { LeavesSwapFeeEstimate } from "./queries/LeavesSwapFeeEstimate.js";
import { LightningSendFeeEstimate } from "./queries/LightningSendFeeEstimate.js";
import { GetTransfers } from "./queries/Transfers.js";
import { UserRequest } from "./queries/UserRequest.js";

export interface SspClientOptions {
  baseUrl: string;
  identityPublicKey: string;
  schemaEndpoint?: string;
}

export interface TransferWithUserRequest extends Transfer {
  userRequest?: UserRequestType;
}

export interface MayHaveSspClientOptions {
  readonly sspClientOptions?: SspClientOptions;
}

export interface HasSspClientOptions {
  readonly sspClientOptions: SspClientOptions;
}

export default class SspClient {
  private readonly requester: Requester;

  private readonly signer: SparkSigner;
  private readonly authProvider: SparkAuthProvider;
  private authPromise?: Promise<void>;

  constructor(
    config: HasSspClientOptions & {
      signer: SparkSigner;
    },
  ) {
    this.signer = config.signer;
    this.authProvider = new SparkAuthProvider();

    const { fetch } = getFetch();
    const options = config.sspClientOptions;

    this.requester = new Requester(
      new NodeKeyCache(DefaultCrypto),
      options.schemaEndpoint || `graphql/spark/2025-03-19`,
      `spark-sdk/0.0.0`,
      this.authProvider,
      options.baseUrl,
      DefaultCrypto,
      undefined,
      fetch as typeof globalThis.fetch,
    );
  }

  async executeRawQuery<T>(
    query: Query<T>,
    needsAuth: boolean = true,
  ): Promise<T | null> {
    if (needsAuth && !(await this.authProvider.isAuthorized())) {
      await this.authenticate();
    }

    try {
      return await this.requester.executeQuery(query);
    } catch (error) {
      if (
        error instanceof Error &&
        error.message.toLowerCase().includes("unauthorized")
      ) {
        try {
          await this.authenticate();
          return await this.requester.executeQuery(query);
        } catch (authError) {
          throw new AuthenticationError(
            "Failed to authenticate after unauthorized response",
            {
              endpoint: "graphql",
              reason: error.message,
            },
            authError as Error,
          );
        }
      }
      throw new NetworkError(
        "Failed to execute GraphQL query",
        {
          method: "POST",
        },
        error as Error,
      );
    }
  }

  async getSwapFeeEstimate(
    amountSats: number,
  ): Promise<LeavesSwapFeeEstimateOutput | null> {
    return await this.executeRawQuery({
      queryPayload: LeavesSwapFeeEstimate,
      variables: {
        total_amount_sats: amountSats,
      },
      constructObject: (response: { leaves_swap_fee_estimate: any }) => {
        return LeavesSwapFeeEstimateOutputFromJson(
          response.leaves_swap_fee_estimate,
        );
      },
    });
  }

  async getLightningSendFeeEstimate(
    encodedInvoice: string,
    amountSats?: number,
  ): Promise<LightningSendFeeEstimateOutput | null> {
    return await this.executeRawQuery({
      queryPayload: LightningSendFeeEstimate,
      variables: {
        encoded_invoice: encodedInvoice,
        amount_sats: amountSats,
      },
      constructObject: (response: { lightning_send_fee_estimate: any }) => {
        return LightningSendFeeEstimateOutputFromJson(
          response.lightning_send_fee_estimate,
        );
      },
    });
  }

  async getCoopExitFeeEstimate({
    leafExternalIds,
    withdrawalAddress,
  }: CoopExitFeeEstimatesInput): Promise<CoopExitFeeEstimatesOutput | null> {
    return await this.executeRawQuery({
      queryPayload: CoopExitFeeEstimate,
      variables: {
        leaf_external_ids: leafExternalIds,
        withdrawal_address: withdrawalAddress,
      },
      constructObject: (response: { coop_exit_fee_estimates: any }) => {
        return CoopExitFeeEstimatesOutputFromJson(
          response.coop_exit_fee_estimates,
        );
      },
    });
  }

  // TODO: Might not need
  async getCurrentUser() {
    throw new Error("Not implemented");
  }

  async completeCoopExit({
    userOutboundTransferExternalId,
  }: CompleteCoopExitInput): Promise<CoopExitRequest | null> {
    return await this.executeRawQuery({
      queryPayload: CompleteCoopExit,
      variables: {
        user_outbound_transfer_external_id: userOutboundTransferExternalId,
      },
      constructObject: (response: { complete_coop_exit: any }) => {
        return CoopExitRequestFromJson(response.complete_coop_exit.request);
      },
    });
  }

  async requestCoopExit({
    leafExternalIds,
    withdrawalAddress,
    exitSpeed,
    feeLeafExternalIds,
    feeQuoteId,
    withdrawAll,
    userOutboundTransferExternalId,
  }: RequestCoopExitInput): Promise<CoopExitRequest | null> {
    return await this.executeRawQuery({
      queryPayload: RequestCoopExit,
      variables: {
        leaf_external_ids: leafExternalIds,
        withdrawal_address: withdrawalAddress,
        exit_speed: exitSpeed,
        fee_leaf_external_ids: feeLeafExternalIds,
        fee_quote_id: feeQuoteId,
        withdraw_all: withdrawAll,
        user_outbound_transfer_external_id: userOutboundTransferExternalId,
      },
      constructObject: (response: { request_coop_exit: any }) => {
        return CoopExitRequestFromJson(response.request_coop_exit.request);
      },
    });
  }

  async requestLightningReceive({
    amountSats,
    network,
    paymentHash,
    expirySecs,
    memo,
    includeSparkAddress,
    receiverIdentityPubkey,
    descriptionHash,
  }: RequestLightningReceiveInput): Promise<LightningReceiveRequest | null> {
    return await this.executeRawQuery({
      queryPayload: RequestLightningReceive,
      variables: {
        amount_sats: amountSats,
        network: network,
        payment_hash: paymentHash,
        expiry_secs: expirySecs,
        memo: memo,
        include_spark_address: includeSparkAddress,
        receiver_identity_pubkey: receiverIdentityPubkey,
        description_hash: descriptionHash,
      },
      constructObject: (response: { request_lightning_receive: any }) => {
        return LightningReceiveRequestFromJson(
          response.request_lightning_receive.request,
        );
      },
    });
  }

  async requestLightningSend({
    encodedInvoice,
    amountSats,
    userOutboundTransferExternalId,
  }: RequestLightningSendInput): Promise<LightningSendRequest | null> {
    return await this.executeRawQuery({
      queryPayload: RequestLightningSend,
      variables: {
        encoded_invoice: encodedInvoice,
        amount_sats: amountSats,
        user_outbound_transfer_external_id: userOutboundTransferExternalId,
      },
      constructObject: (response: { request_lightning_send: any }) => {
        return LightningSendRequestFromJson(
          response.request_lightning_send.request,
        );
      },
    });
  }

  async requestLeaveSwap({
    adaptorPubkey,
    directAdaptorPubkey,
    directFromCpfpAdaptorPubkey,
    totalAmountSats,
    targetAmountSats,
    feeSats,
    userLeaves,
    idempotencyKey,
    targetAmountSatsList,
  }: RequestLeavesSwapInput): Promise<LeavesSwapRequest | null> {
    const query = {
      queryPayload: RequestSwapLeaves,
      variables: {
        adaptor_pubkey: adaptorPubkey,
        direct_adaptor_pubkey: directAdaptorPubkey,
        direct_from_cpfp_adaptor_pubkey: directFromCpfpAdaptorPubkey,
        total_amount_sats: totalAmountSats,
        target_amount_sats: targetAmountSats,
        fee_sats: feeSats,
        user_leaves: userLeaves,
        idempotency_key: idempotencyKey,
        target_amount_sats_list: targetAmountSatsList,
      },
      constructObject: (response: { request_leaves_swap: any }) => {
        if (!response.request_leaves_swap) {
          return null;
        }

        return LeavesSwapRequestFromJson(response.request_leaves_swap.request);
      },
    };
    return await this.executeRawQuery(query);
  }

  async completeLeaveSwap({
    adaptorSecretKey,
    directAdaptorSecretKey,
    directFromCpfpAdaptorSecretKey,
    userOutboundTransferExternalId,
    leavesSwapRequestId,
  }: CompleteLeavesSwapInput): Promise<LeavesSwapRequest | null> {
    return await this.executeRawQuery({
      queryPayload: CompleteLeavesSwap,
      variables: {
        adaptor_secret_key: adaptorSecretKey,
        direct_adaptor_secret_key: directAdaptorSecretKey,
        direct_from_cpfp_adaptor_secret_key: directFromCpfpAdaptorSecretKey,
        user_outbound_transfer_external_id: userOutboundTransferExternalId,
        leaves_swap_request_id: leavesSwapRequestId,
      },
      constructObject: (response: { complete_leaves_swap: any }) => {
        return LeavesSwapRequestFromJson(response.complete_leaves_swap.request);
      },
    });
  }

  async getLightningReceiveRequest(
    id: string,
  ): Promise<LightningReceiveRequest | null> {
    return await this.executeRawQuery({
      queryPayload: UserRequest,
      variables: {
        request_id: id,
      },
      constructObject: (response: { user_request: any }) => {
        if (!response.user_request) {
          return null;
        }

        return LightningReceiveRequestFromJson(response.user_request);
      },
    });
  }

  async getLightningSendRequest(
    id: string,
  ): Promise<LightningSendRequest | null> {
    return await this.executeRawQuery({
      queryPayload: UserRequest,
      variables: {
        request_id: id,
      },
      constructObject: (response: { user_request: any }) => {
        if (!response.user_request) {
          return null;
        }

        return LightningSendRequestFromJson(response.user_request);
      },
    });
  }

  async getLeaveSwapRequest(id: string): Promise<LeavesSwapRequest | null> {
    return await this.executeRawQuery({
      queryPayload: UserRequest,
      variables: {
        request_id: id,
      },
      constructObject: (response: { user_request: any }) => {
        if (!response.user_request) {
          return null;
        }

        return LeavesSwapRequestFromJson(response.user_request);
      },
    });
  }

  async getCoopExitRequest(id: string): Promise<CoopExitRequest | null> {
    return await this.executeRawQuery({
      queryPayload: UserRequest,
      variables: {
        request_id: id,
      },
      constructObject: (response: { user_request: any }) => {
        if (!response.user_request) {
          return null;
        }

        return CoopExitRequestFromJson(response.user_request);
      },
    });
  }

  async getClaimDepositQuote({
    transactionId,
    outputIndex,
    network,
  }: StaticDepositQuoteInput): Promise<StaticDepositQuoteOutput | null> {
    return await this.executeRawQuery({
      queryPayload: GetClaimDepositQuote,
      variables: {
        transaction_id: transactionId,
        output_index: outputIndex,
        network: network,
      },
      constructObject: (response: { static_deposit_quote: any }) => {
        return StaticDepositQuoteOutputFromJson(response.static_deposit_quote);
      },
    });
  }

  async claimStaticDeposit({
    transactionId,
    outputIndex,
    network,
    creditAmountSats,
    depositSecretKey,
    signature,
    sspSignature,
  }: {
    transactionId: string;
    outputIndex: number;
    network: BitcoinNetwork;
    creditAmountSats: number;
    depositSecretKey: string;
    signature: string;
    sspSignature: string;
  }): Promise<ClaimStaticDepositOutput | null> {
    return await this.executeRawQuery({
      queryPayload: ClaimStaticDeposit,
      variables: {
        transaction_id: transactionId,
        output_index: outputIndex,
        network: network,
        request_type: ClaimStaticDepositRequestType.FIXED_AMOUNT,
        credit_amount_sats: creditAmountSats,
        deposit_secret_key: depositSecretKey,
        signature: signature,
        quote_signature: sspSignature,
      },
      constructObject: (response: { claim_static_deposit: any }) => {
        return ClaimStaticDepositOutputFromJson(response.claim_static_deposit);
      },
    });
  }

  async getTransfers(ids: string[]): Promise<TransferWithUserRequest[]> {
    return await this.executeRawQuery({
      queryPayload: GetTransfers,
      variables: {
        transfer_spark_ids: ids,
      },
      constructObject: (response: { transfers: any }) => {
        return response.transfers.map((transfer: any) => {
          const transferObj: TransferWithUserRequest = TransferFromJson(
            transfer,
          ) as TransferWithUserRequest;

          switch (transfer.transfer_user_request.__typename) {
            case "ClaimStaticDeposit":
              transferObj.userRequest = ClaimStaticDepositFromJson(
                transfer.transfer_user_request,
              );
              break;
            case "CoopExitRequest":
              transferObj.userRequest = CoopExitRequestFromJson(
                transfer.transfer_user_request,
              );
              break;
            case "LeavesSwapRequest":
              transferObj.userRequest = LeavesSwapRequestFromJson(
                transfer.transfer_user_request,
              );
              break;
            case "LightningReceiveRequest":
              transferObj.userRequest = LightningReceiveRequestFromJson(
                transfer.transfer_user_request,
              );
              break;
            case "LightningSendRequest":
              transferObj.userRequest = LightningSendRequestFromJson(
                transfer.transfer_user_request,
              );
              break;
          }

          const { userRequestId, ...rest } = transferObj;
          return rest;
        });
      },
    });
  }

  async getChallenge(): Promise<GetChallengeOutput | null> {
    return await this.executeRawQuery(
      {
        queryPayload: GetChallenge,
        variables: {
          public_key: bytesToHex(await this.signer.getIdentityPublicKey()),
        },
        constructObject: (response: { get_challenge: any }) => {
          return GetChallengeOutputFromJson(response.get_challenge);
        },
      },
      false,
    );
  }

  async verifyChallenge(
    signature: string,
    protectedChallenge: string,
  ): Promise<VerifyChallengeOutput | null> {
    return await this.executeRawQuery(
      {
        queryPayload: VerifyChallenge,
        variables: {
          protected_challenge: protectedChallenge,
          signature: signature,
          identity_public_key: bytesToHex(
            await this.signer.getIdentityPublicKey(),
          ),
        },
        constructObject: (response: any) => {
          return VerifyChallengeOutputFromJson(response.verify_challenge);
        },
      },
      false,
    );
  }

  async authenticate() {
    if (this.authPromise) {
      return this.authPromise;
    }

    const promise = (async (): Promise<void> => {
      const MAX_ATTEMPTS = 3;
      let lastErr: Error | undefined;

      /* React Native can cause some outgoing requests to be paused which can result
         in challenges expiring, so we'll retry any authentication failures: */
      for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
        try {
          this.authProvider.removeAuth();

          const challenge = await this.getChallenge();
          if (!challenge) {
            throw new Error("Failed to get challenge");
          }

          const challengeBytes = Buffer.from(
            challenge.protectedChallenge,
            "base64",
          );
          const signature = await this.signer.signMessageWithIdentityKey(
            sha256(challengeBytes),
          );

          const verifyChallenge = await this.verifyChallenge(
            Buffer.from(signature).toString("base64"),
            challenge.protectedChallenge,
          );
          if (!verifyChallenge) {
            throw new Error("Failed to verify challenge");
          }

          this.authProvider.setAuth(
            verifyChallenge.sessionToken,
            new Date(verifyChallenge.validUntil),
          );
          return;
        } catch (err: unknown) {
          if (
            isError(err) &&
            err.message.toLowerCase().includes("challenge expired")
          ) {
            lastErr = err;
            continue;
          }
          throw err;
        }
      }

      throw lastErr ?? new Error("Failed to authenticate after retries");
    })();

    this.authPromise = promise;
    try {
      return await promise;
    } finally {
      this.authPromise = undefined;
    }
  }

  async getCoopExitFeeQuote({
    leafExternalIds,
    withdrawalAddress,
  }: CoopExitFeeQuoteInput): Promise<CoopExitFeeQuote | null> {
    return await this.executeRawQuery({
      queryPayload: GetCoopExitFeeQuote,
      variables: {
        leaf_external_ids: leafExternalIds,
        withdrawal_address: withdrawalAddress,
      },
      constructObject: (response: { coop_exit_fee_quote: any }) => {
        return CoopExitFeeQuoteFromJson(response.coop_exit_fee_quote.quote);
      },
    });
  }
}

class SparkAuthProvider implements AuthProvider {
  private sessionToken: string | undefined;
  private validUntil: Date | undefined;

  async addAuthHeaders(
    headers: Record<string, string>,
  ): Promise<Record<string, string>> {
    const _headers = {
      "Content-Type": "application/json",
      ...headers,
    };

    if (this.sessionToken) {
      _headers["Authorization"] = `Bearer ${this.sessionToken}`;
    }

    return Promise.resolve(_headers);
  }

  async isAuthorized(): Promise<boolean> {
    return (
      !!this.sessionToken && !!this.validUntil && this.validUntil > new Date()
    );
  }

  async addWsConnectionParams(
    params: Record<string, unknown>,
  ): Promise<Record<string, unknown>> {
    const _params = {
      ...params,
    };

    if (this.sessionToken) {
      _params["Authorization"] = `Bearer ${this.sessionToken}`;
    }

    return Promise.resolve(_params);
  }

  setAuth(sessionToken: string, validUntil: Date) {
    this.sessionToken = sessionToken;
    this.validUntil = validUntil;
  }

  removeAuth() {
    this.sessionToken = undefined;
    this.validUntil = undefined;
  }
}
