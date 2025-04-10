import {
  AuthProvider,
  DefaultCrypto,
  NodeKeyCache,
  Query,
  Requester,
} from "@lightsparkdev/core";
import { CompleteCoopExit } from "./mutations/CompleteCoopExit.js";
import { CompleteLeavesSwap } from "./mutations/CompleteLeavesSwap.js";
import { RequestCoopExit } from "./mutations/RequestCoopExit.js";
import { RequestLightningReceive } from "./mutations/RequestLightningReceive.js";
import { RequestLightningSend } from "./mutations/RequestLightningSend.js";
import { RequestSwapLeaves } from "./mutations/RequestSwapLeaves.js";
import { CoopExitFeeEstimateOutputFromJson } from "./objects/CoopExitFeeEstimateOutput.js";
import CoopExitRequest, {
  CoopExitRequestFromJson,
} from "./objects/CoopExitRequest.js";
import {
  BitcoinNetwork,
  CompleteCoopExitInput,
  CompleteLeavesSwapInput,
  CoopExitFeeEstimateInput,
  CoopExitFeeEstimateOutput,
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
import LightningReceiveFeeEstimateOutput, {
  LightningReceiveFeeEstimateOutputFromJson,
} from "./objects/LightningReceiveFeeEstimateOutput.js";
import LightningReceiveRequest, {
  LightningReceiveRequestFromJson,
} from "./objects/LightningReceiveRequest.js";
import LightningSendFeeEstimateOutput, {
  LightningSendFeeEstimateOutputFromJson,
} from "./objects/LightningSendFeeEstimateOutput.js";
import { LightningSendRequestFromJson } from "./objects/LightningSendRequest.js";
import { CoopExitFeeEstimate } from "./queries/CoopExitFeeEstimate.js";
import { LeavesSwapFeeEstimate } from "./queries/LeavesSwapFeeEstimate.js";
import { LightningReceiveFeeEstimate } from "./queries/LightningReceiveFeeEstimate.js";
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

  constructor(identityPublicKey: string, config: HasSspClientOptions) {
    const fetchFunction =
      typeof window !== "undefined" ? window.fetch.bind(window) : fetch;
    const options = config.sspClientOptions;

    this.requester = new Requester(
      new NodeKeyCache(DefaultCrypto),
      options.schemaEndpoint || "graphql/spark/rc",
      `spark-sdk/0.0.0`,
      new SparkAuthProvider(identityPublicKey),
      options.baseUrl,
      DefaultCrypto,
      undefined,
      fetchFunction,
    );
  }

  async executeRawQuery<T>(query: Query<T>): Promise<T | null> {
    return await this.requester.executeQuery(query);
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

  async getLightningReceiveFeeEstimate(
    amountSats: number,
    network: BitcoinNetwork,
  ): Promise<LightningReceiveFeeEstimateOutput | null> {
    return await this.executeRawQuery({
      queryPayload: LightningReceiveFeeEstimate,
      variables: {
        amount_sats: amountSats,
        network: network,
      },
      constructObject: (response: { lightning_receive_fee_estimate: any }) => {
        return LightningReceiveFeeEstimateOutputFromJson(
          response.lightning_receive_fee_estimate,
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
  }: CoopExitFeeEstimateInput): Promise<CoopExitFeeEstimateOutput | null> {
    return await this.executeRawQuery({
      queryPayload: CoopExitFeeEstimate,
      variables: {
        leaf_external_ids: leafExternalIds,
        withdrawal_address: withdrawalAddress,
      },
      constructObject: (response: { coop_exit_fee_estimate: any }) => {
        return CoopExitFeeEstimateOutputFromJson(
          response.coop_exit_fee_estimate,
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
  }: RequestCoopExitInput): Promise<CoopExitRequest | null> {
    return await this.executeRawQuery({
      queryPayload: RequestCoopExit,
      variables: {
        leaf_external_ids: leafExternalIds,
        withdrawal_address: withdrawalAddress,
        idempotency_key: idempotencyKey,
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
}

class SparkAuthProvider implements AuthProvider {
  private publicKey: string;

  constructor(publicKey: string) {
    this.publicKey = publicKey;
  }

  async addAuthHeaders(
    headers: Record<string, string>,
  ): Promise<Record<string, string>> {
    const _headers = {
      "Spark-Identity-Public-Key": this.publicKey,
      "Content-Type": "application/json",
    };
    return Promise.resolve(_headers);
  }

  async isAuthorized(): Promise<boolean> {
    return Promise.resolve(true);
  }

  async addWsConnectionParams(
    params: Record<string, unknown>,
  ): Promise<Record<string, unknown>> {
    return Promise.resolve({
      ...params,
      "Spark-Identity-Public-Key": this.publicKey,
    });
  }
}
