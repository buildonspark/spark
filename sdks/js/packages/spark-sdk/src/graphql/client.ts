import {
  AuthProvider,
  DefaultCrypto,
  NodeKeyCache,
  Query,
  Requester,
  SigningKey,
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
  LightningSendRequest,
  RequestCoopExitInput,
  RequestLeavesSwapInput,
  RequestLightningReceiveInput,
  RequestLightningSendInput,
} from "./objects/index.js";
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
import { LightningReceiveFeeEstimate } from "./queries/LightningReceiveFeeEstimate.js";
import { LightningSendFeeEstimate } from "./queries/LightningSendFeeEstimate.js";

export default class SspClient {
  private readonly requester: Requester;
  private identityPublicKey: string;
  private readonly signingKey?: SigningKey;

  constructor(identityPublicKey: string) {
    this.identityPublicKey = identityPublicKey;

    const fetchFunction =
      typeof window !== "undefined" ? window.fetch.bind(window) : fetch;

    this.requester = new Requester(
      new NodeKeyCache(DefaultCrypto),
      "graphql/spark/rc",
      `spark-sdk/0.0.0`,
      new SparkAuthProvider(identityPublicKey),
      "https://api.dev.dev.sparkinfra.net",
      DefaultCrypto,
      this.signingKey,
      fetchFunction,
    );
  }

  async executeRawQuery<T>(query: Query<T>): Promise<T | null> {
    return await this.requester.executeQuery(query);
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
  }: RequestCoopExitInput): Promise<CoopExitRequest | null> {
    return await this.executeRawQuery({
      queryPayload: RequestCoopExit,
      variables: {
        leaf_external_ids: leafExternalIds,
        withdrawal_address: withdrawalAddress,
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
  }: RequestLeavesSwapInput): Promise<LeavesSwapRequest | null> {
    const query = {
      queryPayload: RequestSwapLeaves,
      variables: {
        adaptor_pubkey: adaptorPubkey,
        total_amount_sats: totalAmountSats,
        target_amount_sats: targetAmountSats,
        fee_sats: feeSats,
        user_leaves: userLeaves,
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
