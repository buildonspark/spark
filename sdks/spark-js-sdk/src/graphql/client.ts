import {
  AuthProvider,
  DefaultCrypto,
  NodeKeyCache,
  Query,
  Requester,
} from "@lightsparkdev/core";
import { CompleteCoopExit } from "./mutations/CompleteCoopExit";
import { CompleteLeavesSwap } from "./mutations/CompleteLeavesSwap";
import { RequestCoopExit } from "./mutations/RequestCoopExit";
import { RequestLightningReceive } from "./mutations/RequestLightningReceive";
import { RequestLightningSend } from "./mutations/RequestLightningSend";
import { RequestSwapLeaves } from "./mutations/RequestSwapLeaves";
import {
  BitcoinNetwork,
  CompleteCoopExitInput,
  CompleteCoopExitOutput,
  CompleteLeavesSwapInput,
  CompleteLeavesSwapOutput,
  CoopExitFeeEstimateInput,
  CoopExitFeeEstimateOutput,
  LightningSendRequest,
  RequestCoopExitInput,
  RequestCoopExitOutput,
  RequestLeavesSwapInput,
  RequestLightningReceiveInput,
  RequestLightningSendInput,
} from "./objects";
import { CompleteCoopExitOutputFromJson } from "./objects/CompleteCoopExitOutput";
import { CompleteLeavesSwapOutputFromJson } from "./objects/CompleteLeavesSwapOutput";
import { CoopExitFeeEstimateOutputFromJson } from "./objects/CoopExitFeeEstimateOutput";
import LeavesSwapRequest, {
  LeavesSwapRequestFromJson,
} from "./objects/LeavesSwapRequest";
import LightningReceiveFeeEstimateOutput, {
  LightningReceiveFeeEstimateOutputFromJson,
} from "./objects/LightningReceiveFeeEstimateOutput";
import LightningReceiveRequest, {
  LightningReceiveRequestFromJson,
} from "./objects/LightningReceiveRequest";
import LightningSendFeeEstimateOutput, {
  LightningSendFeeEstimateOutputFromJson,
} from "./objects/LightningSendFeeEstimateOutput";
import { LightningSendRequestFromJson } from "./objects/LightningSendRequest";
import { RequestCoopExitOutputFromJson } from "./objects/RequestCoopExitOutput";
import { CoopExitFeeEstimate } from "./queries/CoopExitFeeEstimate";
import { LightningReceiveFeeEstimate } from "./queries/LightningReceiveFeeEstimate";
import { LightningSendFeeEstimate } from "./queries/LightningSendFeeEstimate";

export default class SspClient {
  private readonly requester: Requester;
  private identityPublicKey: string;

  constructor(identityPublicKey: string) {
    this.identityPublicKey = identityPublicKey;
    this.requester = new Requester(
      new NodeKeyCache(DefaultCrypto),
      "graphql/spark/rc",
      "spark-js-sdk/v1.0.0",
      new SparkAuthProvider(identityPublicKey),
      "https://api.dev.dev.sparkinfra.net",
      DefaultCrypto
    );
  }

  async executeRawQuery<T>(query: Query<T>): Promise<T | null> {
    return await this.requester.executeQuery(query);
  }

  async getLightningReceiveFeeEstimate(
    amountSats: number,
    network: BitcoinNetwork
  ): Promise<LightningReceiveFeeEstimateOutput | null> {
    return await this.executeRawQuery({
      queryPayload: LightningReceiveFeeEstimate,
      variables: {
        amount_sats: amountSats,
        network: network,
      },
      constructObject: (response: { lightning_receive_fee_estimate: any }) => {
        return LightningReceiveFeeEstimateOutputFromJson(
          response.lightning_receive_fee_estimate
        );
      },
    });
  }

  async getLightningSendFeeEstimate(
    encodedInvoice: string
  ): Promise<LightningSendFeeEstimateOutput | null> {
    return await this.executeRawQuery({
      queryPayload: LightningSendFeeEstimate,
      variables: {
        encoded_invoice: encodedInvoice,
      },
      constructObject: (response: { lightning_send_fee_estimate: any }) => {
        return LightningSendFeeEstimateOutputFromJson(
          response.lightning_send_fee_estimate
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
          response.coop_exit_fee_estimate
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
  }: CompleteCoopExitInput): Promise<CompleteCoopExitOutput | null> {
    return await this.executeRawQuery({
      queryPayload: CompleteCoopExit,
      variables: {
        user_outbound_transfer_external_id: userOutboundTransferExternalId,
        coop_exit_request_id: coopExitRequestId,
      },
      constructObject: (response: { complete_coop_exit: any }) => {
        return CompleteCoopExitOutputFromJson(
          response.complete_coop_exit.request
        );
      },
    });
  }

  async requestCoopExit({
    leafExternalIds,
    withdrawalAddress,
  }: RequestCoopExitInput): Promise<RequestCoopExitOutput | null> {
    return await this.executeRawQuery({
      queryPayload: RequestCoopExit,
      variables: {
        leaf_external_ids: leafExternalIds,
        withdrawal_address: withdrawalAddress,
      },
      constructObject: (response: { request_coop_exit: any }) => {
        return RequestCoopExitOutputFromJson(
          response.request_coop_exit.request
        );
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
          response.request_lightning_receive.request
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
          response.request_lightning_send.request
        );
      },
    });
  }

  async requestLeaveSwap({
    adaptorPubkey,
    totalAmountSats,
    targetAmountSats,
    feeSats,
    network,
  }: RequestLeavesSwapInput): Promise<LeavesSwapRequest | null> {
    console.log("Request Variables:", {
      adaptor_pubkey: adaptorPubkey,
      total_amount_sats: totalAmountSats,
      target_amount_sats: targetAmountSats,
      fee_sats: feeSats,
      network: network,
    });
    console.log("GraphQL Query:", RequestSwapLeaves);
    const query = {
      queryPayload: RequestSwapLeaves,
      variables: {
        adaptor_pubkey: adaptorPubkey,
        total_amount_sats: totalAmountSats,
        target_amount_sats: targetAmountSats,
        fee_sats: feeSats,
        network: network,
      },
      constructObject: (response: { request_leaves_swap: any }) => {
        console.log("Raw Response:", JSON.stringify(response, null, 2));
        if (!response.request_leaves_swap) {
          console.log("Error: request_leaves_swap is null or undefined");
          return null;
        }
        console.log(
          "Request Leaves Swap Response:",
          JSON.stringify(response.request_leaves_swap, null, 2)
        );
        return LeavesSwapRequestFromJson(response.request_leaves_swap.request);
      },
    };
    return await this.executeRawQuery(query);
  }

  async completeLeaveSwap({
    adaptorSecretKey,
    userOutboundTransferExternalId,
    leavesSwapRequestId,
  }: CompleteLeavesSwapInput): Promise<CompleteLeavesSwapOutput | null> {
    return await this.executeRawQuery({
      queryPayload: CompleteLeavesSwap,
      variables: {
        adaptor_secret_key: adaptorSecretKey,
        user_outbound_transfer_external_id: userOutboundTransferExternalId,
        leaves_swap_request_id: leavesSwapRequestId,
      },
      constructObject: (response: { complete_leaves_swap: any }) => {
        return CompleteLeavesSwapOutputFromJson(
          response.complete_leaves_swap.request
        );
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
    headers: Record<string, string>
  ): Promise<Record<string, string>> {
    const _headers = {
      ...headers,
      "Spark-Identity-Public-Key": this.publicKey,
    };
    return Promise.resolve(_headers);
  }

  async isAuthorized(): Promise<boolean> {
    return Promise.resolve(true);
  }

  async addWsConnectionParams(
    params: Record<string, unknown>
  ): Promise<Record<string, unknown>> {
    return Promise.resolve({
      ...params,
      "Spark-Identity-Public-Key": this.publicKey,
    });
  }
}
