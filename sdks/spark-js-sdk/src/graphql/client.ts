import {
  AuthProvider,
  DefaultCrypto,
  NodeKeyCache,
  Query,
  Requester,
} from "@lightsparkdev/core";
import { RequestLightningReceive } from "./mutations/RequestLightningReceive";
import {
  BitcoinNetwork,
  CompleteCoopExitInput,
  CompleteCoopExitOutput,
  CoopExitFeeEstimateInput,
  CoopExitFeeEstimateOutput,
  RequestCoopExitInput,
  RequestCoopExitOutput,
  RequestLeavesSwapInput,
  RequestLeavesSwapOutput,
  RequestLightningReceiveInput,
  RequestLightningSendInput,
  RequestLightningSendOutput,
} from "./objects";
import LightningReceiveFeeEstimateOutput, {
  LightningReceiveFeeEstimateOutputFromJson,
} from "./objects/LightningReceiveFeeEstimateOutput";
import LightningReceiveRequest, {
  LightningReceiveRequestFromJson,
} from "./objects/LightningReceiveRequest";
import { LightningReceiveFeeEstimate } from "./queries/LightningReceiveFeeEstimate";

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
    try {
      // Log the full query details before sending
      // console.log("Sending GraphQL request:", {
      //   query: query.queryPayload,
      //   variables: query.variables,
      //   signingNodeId: query.signingNodeId,
      //   skipAuth: query.skipAuth,
      // });

      return await this.requester.executeQuery(query);
    } catch (error: any) {
      console.error("Query failed with error:", error);
      if (error.response) {
        console.error("Response details:", {
          status: error.response.status,
          statusText: error.response.statusText,
          data: error.response.data,
        });
      }
      console.error("Full error object:", JSON.stringify(error, null, 2));
      throw error;
    }
  }

  async getLightningReceiveFeeEstimate(
    amountSats: number,
    network: BitcoinNetwork
  ): Promise<LightningReceiveFeeEstimateOutput | null> {
    const query = {
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
    };
    // console.log("Sending query:", JSON.stringify(query, null, 2));
    const response = await this.executeRawQuery(query);
    // console.log("Received response:", response);
    return response;
  }

  async getLightningSendFeeEstimate() {
    throw new Error("Not implemented");
  }

  // TODO: Might not need
  async getCurrentUser() {
    throw new Error("Not implemented");
  }

  async getCoopExitFeeEstimate({
    leafExternalIds,
    withdrawalAddress,
  }: CoopExitFeeEstimateInput): Promise<CoopExitFeeEstimateOutput> {
    throw new Error("Not implemented");
  }

  async completeCoopExit({
    userOutboundTransferExternalId,
    coopExitRequestId,
  }: CompleteCoopExitInput): Promise<CompleteCoopExitOutput> {
    throw new Error("Not implemented");
  }

  async requestCoopExit({
    leafExternalIds,
    withdrawalAddress,
  }: RequestCoopExitInput): Promise<RequestCoopExitOutput> {
    throw new Error("Not implemented");
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
  }: RequestLightningSendInput): Promise<RequestLightningSendOutput> {
    throw new Error("Not implemented");
  }

  async requestLeaveSwap({
    adaptorPubkey,
    totalAmountSats,
    targetAmountSats,
    network,
  }: RequestLeavesSwapInput): Promise<RequestLeavesSwapOutput> {
    throw new Error("Not implemented");
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
