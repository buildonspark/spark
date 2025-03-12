import { sha256 } from "@scure/btc-signer/utils";
import * as fs from "fs";
import {
  Channel,
  ChannelCredentials,
  ClientMiddlewareCall,
  createChannel,
  createClient,
  createClientFactory,
  Metadata,
} from "nice-grpc";
import { retryMiddleware } from "nice-grpc-client-middleware-retry";
import { MockServiceClient, MockServiceDefinition } from "../proto/mock.js";
import { SparkServiceClient, SparkServiceDefinition } from "../proto/spark.js";
import {
  Challenge,
  SparkAuthnServiceClient,
  SparkAuthnServiceDefinition,
} from "../proto/spark_authn.js";
import { SparkCallOptions } from "../types/grpc.js";
import { WalletConfigService } from "./config.js";

// TODO: Some sort of client cleanup
export class ConnectionManager {
  private config: WalletConfigService;
  private clients: Map<
    string,
    {
      client: SparkServiceClient & { close?: () => void };
      authToken: string;
    }
  > = new Map();

  constructor(config: WalletConfigService) {
    this.config = config;
  }

  // When initializing wallet, go ahead and instantiate all clients
  public async createClients() {
    await Promise.all(
      Object.values(this.config.getSigningOperators()).map((operator) => {
        this.createSparkClient(operator.address);
      }),
    );
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
      if (certPath && typeof window === "undefined") {
        // TODO: Verify that this is the correct way to create a channel with TLS
        const cert = fs.readFileSync(certPath);
        return createChannel(address, ChannelCredentials.createSsl(cert));
      } else {
        // Fallback to insecure for development
        return createChannel(
          address,
          typeof window === "undefined"
            ? ChannelCredentials.createSsl(null, null, null, {
                rejectUnauthorized: false,
              })
            : undefined,
        );
      }
    } catch (error) {
      console.error("Channel creation error:", error);
      throw new Error("Failed to create channel");
    }
  }

  async createSparkClient(
    address: string,
    certPath?: string,
  ): Promise<SparkServiceClient & { close?: () => void }> {
    if (this.clients.has(address)) {
      return this.clients.get(address)!.client;
    }

    const authToken = await this.authenticate(address);
    const channel = ConnectionManager.createChannelWithTLS(address, certPath);

    const authMiddleware = this.createAuthMiddleWare(address, authToken);
    const client = this.createGrpcClient<SparkServiceClient>(
      SparkServiceDefinition,
      channel,
      authMiddleware,
    );

    this.clients.set(address, { client, authToken });
    return client;
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
        challengeResp.protectedChallenge.challenge,
      ).finish();
      const hash = sha256(challengeBytes);

      const derSignatureBytes =
        await this.config.signer.signMessageWithIdentityKey(hash);

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
    certPath?: string,
  ): SparkAuthnServiceClient & { close?: () => void } {
    const channel = ConnectionManager.createChannelWithTLS(address, certPath);
    return this.createGrpcClient<SparkAuthnServiceClient>(
      SparkAuthnServiceDefinition,
      channel,
    );
  }

  private createAuthMiddleWare(address: string, authToken: string) {
    if (typeof window === "undefined") {
      return this.createNodeMiddleware(address, authToken);
    } else {
      return this.createBrowserMiddleware(address, authToken);
    }
  }

  private createNodeMiddleware(address: string, initialAuthToken: string) {
    return async function* (
      this: ConnectionManager,
      call: ClientMiddlewareCall<any, any>,
      options: SparkCallOptions,
    ) {
      try {
        yield* call.next(call.request, {
          ...options,
          metadata: Metadata(options.metadata).set(
            "Authorization",
            `Bearer ${this.clients.get(address)?.authToken || initialAuthToken}`,
          ),
        });
      } catch (error: any) {
        if (error.message?.includes("token has expired")) {
          const newAuthToken = await this.authenticate(address);
          // @ts-ignore - We can only get here if the client exists
          this.clients.get(address).authToken = newAuthToken;

          yield* call.next(call.request, {
            ...options,
            metadata: Metadata(options.metadata).set(
              "Authorization",
              `Bearer ${newAuthToken}`,
            ),
          });
        }
        throw error;
      }
    }.bind(this);
  }

  private createBrowserMiddleware(address: string, initialAuthToken: string) {
    return async function* (
      this: ConnectionManager,
      call: ClientMiddlewareCall<any, any>,
      options: SparkCallOptions,
    ) {
      try {
        yield* call.next(call.request, {
          ...options,
          metadata: Metadata(options.metadata)
            .set(
              "Authorization",
              `Bearer ${this.clients.get(address)?.authToken || initialAuthToken}`,
            )
            .set("X-Requested-With", "XMLHttpRequest")
            .set("X-Grpc-Web", "1")
            .set("Content-Type", "application/grpc-web+proto"),
        });
      } catch (error: any) {
        if (error.message?.includes("token has expired")) {
          const newAuthToken = await this.authenticate(address);
          // @ts-ignore - We can only get here if the client exists
          this.clients.get(address).authToken = newAuthToken;

          yield* call.next(call.request, {
            ...options,
            metadata: Metadata(options.metadata)
              .set("Authorization", `Bearer ${newAuthToken}`)
              .set("X-Requested-With", "XMLHttpRequest")
              .set("X-Grpc-Web", "1")
              .set("Content-Type", "application/grpc-web+proto"),
          });
        }
        throw error;
      }
    }.bind(this);
  }

  private createGrpcClient<T>(
    defintion: SparkAuthnServiceDefinition | SparkServiceDefinition,
    channel: Channel,
    middleware?: any,
  ): T & { close?: () => void } {
    const clientFactory = createClientFactory().use(retryMiddleware);
    if (middleware) {
      clientFactory.use(middleware);
    }

    const client = clientFactory.create(defintion, channel, {
      "*": {
        retry: true,
        retryMaxAttempts: 3,
      },
    }) as T;
    return {
      ...client,
      close: channel.close?.bind(channel),
    };
  }
}
