import { secp256k1 } from "@noble/curves/secp256k1";
import { sha256 } from "@scure/btc-signer/utils";
import * as fs from "fs";
import {
  CallOptions,
  ChannelCredentials,
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
    const channel = this.createChannelWithTLS(address);

    const client = createClient(MockServiceDefinition, channel);
    return { ...client, close: () => channel.close() };
  }

  // TODO: Web transport handles TLS differently, verify that we don't need to do anything
  private static createChannelWithTLS(address: string, certPath?: string) {
    try {
      if (certPath) {
        // TODO: Verify that this is the correct way to create a channel with TLS
        const cert = fs.readFileSync(certPath);
        return createChannel(address, ChannelCredentials.createSsl(cert));
      } else {
        // Fallback to insecure for development
        return createChannel(
          address,
          ChannelCredentials.createSsl(null, null, null, {
            rejectUnauthorized: false,
          })
        );
      }
    } catch (error) {
      console.error("Channel creation error:", error);
      throw new Error("Failed to create channel");
    }
  }

  async createSparkClient(
    address: string,
    certPath?: string
  ): Promise<SparkServiceClient & { close?: () => void }> {
    try {
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
        const channel = ConnectionManager.createChannelWithTLS(
          address,
          certPath
        );

        const client = createClientFactory()
          .use(middleWare)
          .create(SparkServiceDefinition, channel);
        return { ...client, close: () => channel.close() };
      } else {
        const channel = createWebChannel(address);
        return createWebClientFactory()
          .use(middleWare)
          .create(SparkServiceDefinition, channel);
      }
    } catch (error) {
      console.error("Spark client creation error:", error);
      throw error;
    }
  }

  private async authenticate(address: string) {
    try {
      const identityPublicKey = await this.config.signer.getIdentityPublicKey();
      const sparkAuthnClient = this.createSparkAuthnGrpcConnection(address);

      const challengeResp = await sparkAuthnClient.get_challenge({
        publicKey: identityPublicKey,
      });

      if (!challengeResp.protectedChallenge?.challenge) {
        throw new Error("Invalid challenge response");
      }

      const challengeBytes = Challenge.encode(
        challengeResp.protectedChallenge.challenge
      ).finish();
      const hash = sha256(challengeBytes);

      const compactSignatureBytes =
        await this.config.signer.signEcdsaWithIdentityPrivateKey(hash);
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
    } catch (error: any) {
      console.error("Authentication error:", error);
      throw new Error(`Authentication failed: ${error.message}`);
    }
  }

  private createSparkAuthnGrpcConnection(
    address: string,
    certPath?: string
  ): SparkAuthnServiceClient & { close?: () => void } {
    try {
      if (typeof window === "undefined") {
        const channel = ConnectionManager.createChannelWithTLS(
          address,
          certPath
        );
        const client = createClient(SparkAuthnServiceDefinition, channel);
        return { ...client, close: () => channel.close() };
      } else {
        const channel = createWebChannel(address);
        return createWebClient(SparkAuthnServiceDefinition, channel);
      }
    } catch (error) {
      console.error("Authn client creation error:", error);
      throw error;
    }
  }
}
