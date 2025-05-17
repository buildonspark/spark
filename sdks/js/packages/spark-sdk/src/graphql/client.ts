import {
  AuthProvider,
  bytesToHex,
  DefaultCrypto,
  NodeKeyCache,
  Query,
  Requester,
} from "@lightsparkdev/core";
import { sha256 } from "@noble/hashes/sha256";
import { AuthenticationError, NetworkError } from "../errors/index.js";
import { SparkSigner } from "../signer/signer.js";
import { CompleteCoopExit } from "./mutations/CompleteCoopExit.js";
import { CompleteLeavesSwap } from "./mutations/CompleteLeavesSwap.js";
import { GetChallenge } from "./mutations/GetChallenge.js";
import { RequestCoopExit } from "./mutations/RequestCoopExit.js";
import { RequestLightningReceive } from "./mutations/RequestLightningReceive.js";
import { RequestLightningSend } from "./mutations/RequestLightningSend.js";
import { RequestSwapLeaves } from "./mutations/RequestSwapLeaves.js";
import { VerifyChallenge } from "./mutations/VerifyChallenge.js";
import { CoopExitFeeEstimatesOutputFromJson } from "./objects/CoopExitFeeEstimatesOutput.js";
import CoopExitRequest, {
  CoopExitRequestFromJson,
} from "./objects/CoopExitRequest.js";
import { GetChallengeOutputFromJson } from "./objects/GetChallengeOutput.js";
import type {
  CompleteCoopExitInput,
  CompleteLeavesSwapInput,
  CoopExitFeeEstimatesInput,
  CoopExitFeeEstimatesOutput,
  GetChallengeOutput,
  LeavesSwapFeeEstimateOutput,
  LightningSendRequest,
  RequestCoopExitInput,
  RequestLeavesSwapInput,
  RequestLightningReceiveInput,
  RequestLightningSendInput,
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
import VerifyChallengeOutput, {
  VerifyChallengeOutputFromJson,
} from "./objects/VerifyChallengeOutput.js";
import { CoopExitFeeEstimate } from "./queries/CoopExitFeeEstimate.js";
import { LeavesSwapFeeEstimate } from "./queries/LeavesSwapFeeEstimate.js";
import { LightningSendFeeEstimate } from "./queries/LightningSendFeeEstimate.js";
import { UserRequest } from "./queries/UserRequest.js";

export interface SspClientOptions {
  baseUrl: string;
  identityPublicKey: string;
  schemaEndpoint?: string;
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

  constructor(
    config: HasSspClientOptions & {
      signer: SparkSigner;
    },
  ) {
    this.signer = config.signer;
    this.authProvider = new SparkAuthProvider();

    const fetchFunction =
      typeof window !== "undefined" ? window.fetch.bind(window) : fetch;
    const options = config.sspClientOptions;

    this.requester = new Requester(
      new NodeKeyCache(DefaultCrypto),
      options.schemaEndpoint || `graphql/spark/2025-03-19`,
      `spark-sdk/0.0.0`,
      this.authProvider,
      options.baseUrl,
      DefaultCrypto,
      undefined,
      fetchFunction,
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
  ): Promise<LightningSendFeeEstimateOutput | null> {
    return await this.executeRawQuery({
      queryPayload: LightningSendFeeEstimate,
      variables: {
        encoded_invoice: encodedInvoice,
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
    coopExitRequestId,
  }: CompleteCoopExitInput): Promise<CoopExitRequest | null> {
    return await this.executeRawQuery({
      queryPayload: CompleteCoopExit,
      variables: {
        user_outbound_transfer_external_id: userOutboundTransferExternalId,
        coop_exit_request_id: coopExitRequestId,
      },
      constructObject: (response: { complete_coop_exit: any }) => {
        return CoopExitRequestFromJson(response.complete_coop_exit.request);
      },
    });
  }

  async requestCoopExit({
    leafExternalIds,
    withdrawalAddress,
    idempotencyKey,
    exitSpeed,
  }: RequestCoopExitInput): Promise<CoopExitRequest | null> {
    return await this.executeRawQuery({
      queryPayload: RequestCoopExit,
      variables: {
        leaf_external_ids: leafExternalIds,
        withdrawal_address: withdrawalAddress,
        idempotency_key: idempotencyKey,
        exit_speed: exitSpeed,
      },
      constructObject: (response: { request_coop_exit: any }) => {
        return CoopExitRequestFromJson(response.request_coop_exit.request);
      },
    });
  }

  // TODO: Lets name this better
  async requestLightningReceive({
    amountSats,
    network,
    paymentHash,
    expirySecs,
    memo,
  }: RequestLightningReceiveInput): Promise<LightningReceiveRequest | null> {
    return await this.executeRawQuery({
      queryPayload: RequestLightningReceive,
      variables: {
        amount_sats: amountSats,
        network: network,
        payment_hash: paymentHash,
        expiry_secs: expirySecs,
        memo: memo,
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
    idempotencyKey,
  }: RequestLightningSendInput): Promise<LightningSendRequest | null> {
    return await this.executeRawQuery({
      queryPayload: RequestLightningSend,
      variables: {
        encoded_invoice: encodedInvoice,
        idempotency_key: idempotencyKey,
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
    totalAmountSats,
    targetAmountSats,
    feeSats,
    userLeaves,
    idempotencyKey,
  }: RequestLeavesSwapInput): Promise<LeavesSwapRequest | null> {
    const query = {
      queryPayload: RequestSwapLeaves,
      variables: {
        adaptor_pubkey: adaptorPubkey,
        total_amount_sats: totalAmountSats,
        target_amount_sats: targetAmountSats,
        fee_sats: feeSats,
        user_leaves: userLeaves,
        idempotency_key: idempotencyKey,
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
    userOutboundTransferExternalId,
    leavesSwapRequestId,
  }: CompleteLeavesSwapInput): Promise<LeavesSwapRequest | null> {
    return await this.executeRawQuery({
      queryPayload: CompleteLeavesSwap,
      variables: {
        adaptor_secret_key: adaptorSecretKey,
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
    this.authProvider.removeAuth();

    const challenge = await this.getChallenge();
    if (!challenge) {
      throw new Error("Failed to get challenge");
    }

    const challengeBytes = Buffer.from(challenge.protectedChallenge, "base64");
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
