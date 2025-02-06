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
  createChannel as createWebChannel,
  createClient as createWebClient,
  createClientFactory as createWebClientFactory,
} from "nice-grpc-web";
import { MockServiceClient, MockServiceDefinition } from "../proto/mock";
import { SparkServiceClient, SparkServiceDefinition } from "../proto/spark";
import {
  Challenge,
  SparkAuthnServiceClient,
  SparkAuthnServiceDefinition,
} from "../proto/spark_authn";
import { WalletConfigService } from "./config";

export class ConnectionManager {
  private config: WalletConfigService;
  constructor(config: WalletConfigService) {
    this.config = config;
  }
  static createMockClient(address: string): MockServiceClient & {
    close: () => void;
  } {
    const channel = createChannel(address);
    const client = createClient(MockServiceDefinition, channel);
    return { ...client, close: () => channel.close() };
  }

  async createSparkClient(
    address: string
  ): Promise<SparkServiceClient & { close?: () => void }> {
    const authToken = await this.authenticate(address);

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
  private async authenticate(address: string) {
    const identityPublicKey = this.config.signer.getIdentityPublicKey();

    const sparkAuthnClient = this.createSparkAuthnGrpcConnection(address);
    const challengeResp = await sparkAuthnClient.get_challenge({
      publicKey: identityPublicKey,
    });

    const challengeBytes = Challenge.encode(
      challengeResp.protectedChallenge!.challenge!
    ).finish();
    const hash = sha256(challengeBytes);

    const compactSignatureBytes =
      this.config.signer.signEcdsaWithIdentityPrivateKey(hash);
    const derSignatureBytes = secp256k1.Signature.fromCompact(
      compactSignatureBytes
    ).toDERRawBytes();

    const verifyResp = await sparkAuthnClient.verify_challenge({
      protectedChallenge: challengeResp.protectedChallenge,
      signature: derSignatureBytes,
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
