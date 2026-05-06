import {
  type AuthProvider,
  bytesToHex,
  DefaultCrypto,
  isError,
  type Logger,
  NodeKeyCache,
  type Query,
  Requester,
} from "@lightsparkdev/core";
import { sha256 } from "@noble/hashes/sha2";
import {
  SparkAuthenticationError,
  SparkRequestError,
} from "../errors/index.js";
import { type SparkSigner } from "../signer/signer.js";
import { type UserRequestType } from "../types/sdk-types.js";
import { getFetch } from "../utils/fetch.js";
import { NoopLogger } from "../utils/logging.js";
import type { LoggingService } from "../utils/logging-service.js";
import { ClaimInstantStaticDeposit } from "./mutations/ClaimInstantStaticDeposit.js";
import { ClaimStaticDeposit } from "./mutations/ClaimStaticDeposit.js";
import { CompleteCoopExit } from "./mutations/CompleteCoopExit.js";
import { DeleteSparkWalletWebhook } from "./mutations/DeleteSparkWalletWebhook.js";
import { GetChallenge } from "./mutations/GetChallenge.js";
import { RegisterSparkWalletWebhook } from "./mutations/RegisterSparkWalletWebhook.js";
import { RequestCoopExit } from "./mutations/RequestCoopExit.js";
import { RequestLightningReceive } from "./mutations/RequestLightningReceive.js";
import { RequestLightningSend } from "./mutations/RequestLightningSend.js";
import { RequestSwapLeaves } from "./mutations/RequestSwapLeaves.js";
import { VerifyChallenge } from "./mutations/VerifyChallenge.js";
import { ListSparkWalletWebhooks } from "./queries/ListSparkWalletWebhooks.js";
import { ClaimStaticDepositFromJson } from "./objects/ClaimStaticDeposit.js";
import type InstantStaticDepositClaimOutput from "./objects/InstantStaticDepositClaimOutput.js";
import { InstantStaticDepositClaimOutputFromJson } from "./objects/InstantStaticDepositClaimOutput.js";
import type InstantStaticDepositQuoteOutput from "./objects/InstantStaticDepositQuoteOutput.js";
import { InstantStaticDepositQuoteOutputFromJson } from "./objects/InstantStaticDepositQuoteOutput.js";
import type ClaimStaticDepositOutput from "./objects/ClaimStaticDepositOutput.js";
import { ClaimStaticDepositOutputFromJson } from "./objects/ClaimStaticDepositOutput.js";
import ClaimStaticDepositRequestType from "./objects/ClaimStaticDepositRequestType.js";
import { CoopExitFeeEstimatesOutputFromJson } from "./objects/CoopExitFeeEstimatesOutput.js";
import { CoopExitFeeQuoteFromJson } from "./objects/CoopExitFeeQuote.js";
import type CoopExitRequest from "./objects/CoopExitRequest.js";
import { CoopExitRequestFromJson } from "./objects/CoopExitRequest.js";
import { GetChallengeOutputFromJson } from "./objects/GetChallengeOutput.js";
import {
  type BitcoinNetwork,
  type CompleteCoopExitInput,
  type CoopExitFeeEstimatesInput,
  type CoopExitFeeEstimatesOutput,
  type CoopExitFeeQuote,
  type CoopExitFeeQuoteInput,
  type GetChallengeOutput,
  type LeavesSwapFeeEstimateOutput,
  type LightningSendRequest,
  type RequestCoopExitInput,
  type RequestLightningReceiveInput,
  type RequestLightningSendInput,
  type RequestSwapInput,
  type SparkUserRequestStatus,
  type SparkUserRequestType,
  type Transfer,
} from "./objects/index.js";
import { LeavesSwapFeeEstimateOutputFromJson } from "./objects/LeavesSwapFeeEstimateOutput.js";
import type LeavesSwapRequest from "./objects/LeavesSwapRequest.js";
import { LeavesSwapRequestFromJson } from "./objects/LeavesSwapRequest.js";
import type LightningReceiveRequest from "./objects/LightningReceiveRequest.js";
import { LightningReceiveRequestFromJson } from "./objects/LightningReceiveRequest.js";
import type LightningSendFeeEstimateOutput from "./objects/LightningSendFeeEstimateOutput.js";
import { LightningSendFeeEstimateOutputFromJson } from "./objects/LightningSendFeeEstimateOutput.js";
import { LightningSendRequestFromJson } from "./objects/LightningSendRequest.js";
import type DeleteSparkWalletWebhookInput from "./objects/DeleteSparkWalletWebhookInput.js";
import {
  DeleteSparkWalletWebhookOutputFromJson,
  type default as DeleteSparkWalletWebhookOutput,
} from "./objects/DeleteSparkWalletWebhookOutput.js";
import {
  ListSparkWalletWebhooksOutputFromJson,
  type default as ListSparkWalletWebhooksOutput,
} from "./objects/ListSparkWalletWebhooksOutput.js";
import type RegisterSparkWalletWebhookInput from "./objects/RegisterSparkWalletWebhookInput.js";
import {
  RegisterSparkWalletWebhookOutputFromJson,
  type default as RegisterSparkWalletWebhookOutput,
} from "./objects/RegisterSparkWalletWebhookOutput.js";
import type SparkWalletUserToUserRequestsConnection from "./objects/SparkWalletUserToUserRequestsConnection.js";
import { SparkWalletUserToUserRequestsConnectionFromJson } from "./objects/SparkWalletUserToUserRequestsConnection.js";
import type StaticDepositQuoteInput from "./objects/StaticDepositQuoteInput.js";
import type StaticDepositQuoteOutput from "./objects/StaticDepositQuoteOutput.js";
import { StaticDepositQuoteOutputFromJson } from "./objects/StaticDepositQuoteOutput.js";
import { TransferFromJson } from "./objects/Transfer.js";
import type VerifyChallengeOutput from "./objects/VerifyChallengeOutput.js";
import { VerifyChallengeOutputFromJson } from "./objects/VerifyChallengeOutput.js";
import { CoopExitFeeEstimate } from "./queries/CoopExitFeeEstimate.js";
import { FetchCurrentUserToUserRequestsConnection } from "./queries/FetchCurrentUserToUserRequestsConnection.js";
import { GetInstantStaticDepositQuote } from "./mutations/GetInstantStaticDepositQuote.js";
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

export interface GetUserRequestsParams {
  first?: number;
  after?: string;
  types?: SparkUserRequestType[];
  statuses?: SparkUserRequestStatus[];
  networks?: BitcoinNetwork[];
}

type TransferGraphqlResponse = Record<string, unknown> & {
  transfer_user_request?:
    | (Record<string, unknown> & { __typename?: string })
    | null;
};

function asTransferResponse(value: unknown): TransferGraphqlResponse {
  if (typeof value !== "object" || value === null) {
    throw new SparkRequestError("Invalid transfer response", {
      field: "transfers",
      value,
    });
  }
  return value as TransferGraphqlResponse;
}

export default class SspClient {
  private readonly requester: Requester;

  private readonly signer: SparkSigner;
  private readonly authProvider: SparkAuthProvider;
  private authPromise?: Promise<void>;
  private readonly logger?: Logger;
  private readonly logging?: LoggingService;

  constructor(
    config: HasSspClientOptions & {
      signer: SparkSigner;
    },
    options?: {
      logging?: LoggingService;
      logger?: Logger;
    },
  ) {
    this.signer = config.signer;
    this.logging = options?.logging;
    this.authProvider = new SparkAuthProvider(this.logging);
    this.logger = options?.logging?.logger("SspClient") ?? options?.logger;

    const { fetch } = getFetch({ logger: this.logger, retry: true });
    const sspOptions = config.sspClientOptions;
    const schemaEndpoint =
      sspOptions.schemaEndpoint || `graphql/spark/2025-03-19`;

    const authAwareFetch: typeof globalThis.fetch = async (
      input: RequestInfo | URL,
      init?: RequestInit,
    ) => {
      const sparkFetch = fetch as unknown as typeof globalThis.fetch;
      const response = await sparkFetch(input, init);
      if (response.status === 401) {
        throw new Error("Request unauthorized");
      }
      return response;
    };

    this.requester = new Requester(
      new NodeKeyCache(DefaultCrypto),
      schemaEndpoint,
      `spark-sdk/0.0.0`,
      this.authProvider,
      sspOptions.baseUrl,
      DefaultCrypto,
      undefined,
      authAwareFetch,
    );

    this.logging?.wrapPrototypeMethods("SspClient", this);
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
          throw new SparkAuthenticationError(
            "Failed to authenticate after unauthorized response",
            {
              endpoint: "graphql",
              reason: error.message,
              error: authError,
            },
          );
        }
      }
      throw new SparkRequestError("Failed to execute GraphQL query", {
        method: "POST",
        error: error,
      });
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
      constructObject: (response: { leaves_swap_fee_estimate: unknown }) => {
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
      constructObject: (response: { lightning_send_fee_estimate: unknown }) => {
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
      constructObject: (response: { coop_exit_fee_estimates: unknown }) => {
        return CoopExitFeeEstimatesOutputFromJson(
          response.coop_exit_fee_estimates,
        );
      },
    });
  }

  // TODO: Might not need
  getCurrentUser() {
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
      constructObject: (response: {
        complete_coop_exit: { request: unknown };
      }) => {
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
      constructObject: (response: {
        request_coop_exit: { request: unknown };
      }) => {
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
    sparkInvoice,
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
        spark_invoice: sparkInvoice,
      },
      constructObject: (response: {
        request_lightning_receive: { request: unknown };
      }) => {
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
      constructObject: (response: {
        request_lightning_send: { request: unknown };
      }) => {
        return LightningSendRequestFromJson(
          response.request_lightning_send.request,
        );
      },
    });
  }

  async requestLeavesSwap({
    adaptorPubkey,
    totalAmountSats,
    targetAmountSats,
    feeSats,
    userLeaves,
    userOutboundTransferExternalId,
  }: RequestSwapInput): Promise<LeavesSwapRequest | null> {
    const query = {
      queryPayload: RequestSwapLeaves,
      variables: {
        adaptor_pubkey: adaptorPubkey,
        total_amount_sats: totalAmountSats,
        target_amount_sats: targetAmountSats,
        fee_sats: feeSats,
        user_leaves: userLeaves,
        user_outbound_transfer_external_id: userOutboundTransferExternalId,
      },
      constructObject: (response: {
        request_swap: { request: unknown } | null;
      }) => {
        if (!response.request_swap) {
          return null;
        }

        return LeavesSwapRequestFromJson(response.request_swap.request);
      },
    };
    return await this.executeRawQuery(query);
  }

  async getLightningReceiveRequest(
    id: string,
  ): Promise<LightningReceiveRequest | null> {
    return await this.executeRawQuery({
      queryPayload: UserRequest,
      variables: {
        request_id: id,
      },
      constructObject: (response: { user_request: unknown }) => {
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
      constructObject: (response: { user_request: unknown }) => {
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
      constructObject: (response: { user_request: unknown }) => {
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
      constructObject: (response: { user_request: unknown }) => {
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
      constructObject: (response: { static_deposit_quote: unknown }) => {
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
      constructObject: (response: { claim_static_deposit: unknown }) => {
        return ClaimStaticDepositOutputFromJson(response.claim_static_deposit);
      },
    });
  }

  async getInstantStaticDepositQuote({
    transactionId,
    outputIndex,
    network,
    partnerId,
  }: StaticDepositQuoteInput): Promise<InstantStaticDepositQuoteOutput | null> {
    return await this.executeRawQuery({
      queryPayload: GetInstantStaticDepositQuote,
      variables: {
        transaction_id: transactionId,
        output_index: outputIndex,
        network: network,
        partner_id: partnerId,
      },
      constructObject: (response: {
        create_instant_static_deposit_quote: unknown;
      }) => {
        return InstantStaticDepositQuoteOutputFromJson(
          response.create_instant_static_deposit_quote,
        );
      },
    });
  }

  async claimInstantStaticDeposit({
    quoteId,
    depositSecretKey,
    signature,
  }: {
    quoteId: string;
    depositSecretKey: string;
    signature: string;
  }): Promise<InstantStaticDepositClaimOutput | null> {
    return await this.executeRawQuery({
      queryPayload: ClaimInstantStaticDeposit,
      variables: {
        static_deposit_quote_id: quoteId,
        static_deposit_address_private_key_share: depositSecretKey,
        signature: signature,
      },
      constructObject: (response: {
        create_claim_instant_static_deposit: unknown;
      }) => {
        return InstantStaticDepositClaimOutputFromJson(
          response.create_claim_instant_static_deposit,
        );
      },
    });
  }

  async getTransfers(ids: string[]): Promise<TransferWithUserRequest[]> {
    return (
      (await this.executeRawQuery({
        queryPayload: GetTransfers,
        variables: {
          transfer_spark_ids: ids,
        },
        constructObject: (response: { transfers: unknown[] }) => {
          return response.transfers.map((transfer) => {
            const transferRecord = asTransferResponse(transfer);
            const transferObj: TransferWithUserRequest =
              TransferFromJson(transferRecord);
            const userRequest = transferRecord.transfer_user_request;

            switch (userRequest?.__typename) {
              case "ClaimStaticDeposit":
                transferObj.userRequest =
                  ClaimStaticDepositFromJson(userRequest);
                break;
              case "CoopExitRequest":
                transferObj.userRequest = CoopExitRequestFromJson(userRequest);
                break;
              case "LeavesSwapRequest":
                transferObj.userRequest =
                  LeavesSwapRequestFromJson(userRequest);
                break;
              case "LightningReceiveRequest":
                transferObj.userRequest =
                  LightningReceiveRequestFromJson(userRequest);
                break;
              case "LightningSendRequest":
                transferObj.userRequest =
                  LightningSendRequestFromJson(userRequest);
                break;
            }

            const { userRequestId: _ignored, ...transferResult } =
              transferObj as TransferWithUserRequest & {
                userRequestId?: unknown;
              };
            void _ignored;
            return transferResult as Omit<
              TransferWithUserRequest,
              "userRequestId"
            >;
          });
        },
      })) ?? []
    );
  }

  async getChallenge(): Promise<GetChallengeOutput | null> {
    return await this.executeRawQuery(
      {
        queryPayload: GetChallenge,
        variables: {
          public_key: bytesToHex(await this.signer.getIdentityPublicKey()),
        },
        constructObject: (response: { get_challenge: unknown }) => {
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
        constructObject: (response: { verify_challenge: unknown }) => {
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
      constructObject: (response: {
        coop_exit_fee_quote: { quote: unknown };
      }) => {
        return CoopExitFeeQuoteFromJson(response.coop_exit_fee_quote.quote);
      },
    });
  }

  async getUserRequests({
    first,
    after,
    types,
    statuses,
    networks,
  }: GetUserRequestsParams): Promise<SparkWalletUserToUserRequestsConnection | null> {
    return await this.executeRawQuery({
      queryPayload: FetchCurrentUserToUserRequestsConnection,
      variables: {
        first: first,
        after: after,
        types: types,
        statuses: statuses,
        networks: networks,
      },
      constructObject: (response: {
        current_user?: { user_requests?: unknown };
      }) => {
        if (!response.current_user?.user_requests) {
          return null;
        }
        return SparkWalletUserToUserRequestsConnectionFromJson(
          response.current_user?.user_requests,
        );
      },
    });
  }
  async registerSparkWalletWebhook(
    input: RegisterSparkWalletWebhookInput,
  ): Promise<RegisterSparkWalletWebhookOutput | null> {
    return await this.executeRawQuery({
      queryPayload: RegisterSparkWalletWebhook,
      variables: {
        input: {
          secret: input.secret,
          url: input.url,
          event_types: input.event_types,
        },
      },
      constructObject: (response: { register_wallet_webhook: unknown }) => {
        return RegisterSparkWalletWebhookOutputFromJson(
          response.register_wallet_webhook,
        );
      },
    });
  }

  async deleteSparkWalletWebhook(
    input: DeleteSparkWalletWebhookInput,
  ): Promise<DeleteSparkWalletWebhookOutput | null> {
    return await this.executeRawQuery({
      queryPayload: DeleteSparkWalletWebhook,
      variables: {
        input: {
          webhook_id: input.webhook_id,
        },
      },
      constructObject: (response: { delete_wallet_webhook: unknown }) => {
        return DeleteSparkWalletWebhookOutputFromJson(
          response.delete_wallet_webhook,
        );
      },
    });
  }

  async listSparkWalletWebhooks(): Promise<ListSparkWalletWebhooksOutput | null> {
    return await this.executeRawQuery({
      queryPayload: ListSparkWalletWebhooks,
      variables: {},
      constructObject: (response: { wallet_webhooks: unknown }) => {
        return ListSparkWalletWebhooksOutputFromJson(response.wallet_webhooks);
      },
    });
  }
}

class SparkAuthProvider implements AuthProvider {
  private sessionToken: string | undefined;
  private validUntil: Date | undefined;
  private readonly logger: Logger;

  constructor(logging?: LoggingService) {
    this.logger = logging?.logger("SparkAuthProvider") ?? NoopLogger;
    logging?.wrapPrototypeMethods("SparkAuthProvider", this);
  }

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

  isAuthorized(): Promise<boolean> {
    return Promise.resolve(
      !!this.sessionToken && !!this.validUntil && this.validUntil > new Date(),
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
