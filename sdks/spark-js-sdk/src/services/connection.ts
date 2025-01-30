import { bytesToHex } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { sha256 } from "@scure/btc-signer/utils";
import {
  CallOptions,
  ClientMiddlewareCall,
  createChannel,
  createClient,
  createClientFactory,
  Metadata,
} from "nice-grpc";
import {
  createClient as createWebClient,
  createChannel as createWebChannel,
  createClientFactory as createWebClientFactory,
} from "nice-grpc-web";
import { MockServiceClient, MockServiceDefinition } from "../proto/mock";
import { SparkServiceClient } from "../proto/spark";
import { SparkServiceDefinition } from "../proto/spark";
import {
  Challenge,
  SparkAuthnServiceClient,
  SparkAuthnServiceDefinition,
} from "../proto/spark_authn";
import { WalletConfig, WalletConfigService } from "./config";

export class ConnectionManager {
  static createMockClient(address: string): MockServiceClient & {
    close: () => void;
  } {
    const channel = createChannel(address);
    const client = createClient(MockServiceDefinition, channel);
    return { ...client, close: () => channel.close() };
  }

  async createSparkClient(
    address: string,
    config: WalletConfigService
  ): Promise<SparkServiceClient & { close?: () => void }> {
    const authToken = await this.authenticate(
      address,
      config.getIdentityPublicKey(),
      config.getConfig().identityPrivateKey
    );

    const middleWare = (
      call: ClientMiddlewareCall<any, any>,
      options: CallOptions
    ) =>
      call.next(call.request, {
        ...options,
        metadata: Metadata(options.metadata).set(
          "Authorization",
          `Bearer ${authToken}`
        ),
      });
    if (typeof window === "undefined") {
      // Node.js environment
      const channel = createChannel(address);
      const client = createClientFactory()
        .use(middleWare)
        .create(SparkServiceDefinition, channel);
      return { ...client, close: () => channel.close() };
    } else {
      // Browser environment
      // Channel connection is handled by the browser therefore we don't need to close it
      const channel = createWebChannel(address);
      return createWebClientFactory()
        .use(middleWare)
        .create(SparkServiceDefinition, channel);
    }
  }
  private async authenticate(
    address: string,
    identityPublicKey: Uint8Array,
    identityPrivateKey: Uint8Array
  ) {
    const sparkAuthnClient = this.createSparkAuthnGrpcConnection(address);
    const challengeResp = await sparkAuthnClient.get_challenge({
      publicKey: identityPublicKey,
    });

    const challengeBytes = Challenge.encode(
      challengeResp.protectedChallenge!.challenge!
    ).finish();
    const hash = sha256(challengeBytes);

    const signature = secp256k1.sign(hash, identityPrivateKey);

    const verifyResp = await sparkAuthnClient.verify_challenge({
      protectedChallenge: challengeResp.protectedChallenge,
      signature: signature.toDERRawBytes(),
      publicKey: identityPublicKey,
    });
    sparkAuthnClient.close?.();

    return verifyResp.sessionToken;
  }

  private createSparkAuthnGrpcConnection(
    address: string
  ): SparkAuthnServiceClient & { close?: () => void } {
    if (typeof window === "undefined") {
      const channel = createChannel(address);
      const client = createClient(SparkAuthnServiceDefinition, channel);
      return { ...client, close: () => channel.close() };
    } else {
      const channel = createWebChannel(address);
      return createWebClient(SparkAuthnServiceDefinition, channel);
    }
  }
}
