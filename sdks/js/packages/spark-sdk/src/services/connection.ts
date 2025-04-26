import { sha256 } from "@scure/btc-signer/utils";

import { isNode } from "@lightsparkdev/core";
import {
  Channel,
  ClientMiddlewareCall,
  createChannel,
  createClient,
  createClientFactory,
} from "nice-grpc";
import { retryMiddleware } from "nice-grpc-client-middleware-retry";
import { Metadata } from "nice-grpc-common";
import { AuthenticationError, NetworkError } from "../errors/types.js";
import { MockServiceClient, MockServiceDefinition } from "../proto/mock.js";
import { SparkServiceClient, SparkServiceDefinition } from "../proto/spark.js";
import {
  Challenge,
  SparkAuthnServiceClient,
  SparkAuthnServiceDefinition,
} from "../proto/spark_authn.js";
import { isHermeticTest } from "../tests/test-util.js";
import { SparkCallOptions } from "../types/grpc.js";
import { Network } from "../utils/network.js";
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

  public async closeConnections() {
    await Promise.all(
      Array.from(this.clients.values()).map((client) =>
        client.client.close?.(),
      ),
    );
    this.clients.clear();
  }

  async createMockClient(address: string): Promise<
    MockServiceClient & {
      close: () => void;
    }
  > {
    const channel = await this.createChannelWithTLS(address);

    const client = createClient(MockServiceDefinition, channel);
    return { ...client, close: () => channel.close() };
  }

  private async createChannelWithTLS(address: string, certPath?: string) {
    try {
      if (isNode) {
        const { ChannelCredentials } = await import("nice-grpc");
        const insecureCredentials =
          this.config.getNetwork() === Network.LOCAL && !isHermeticTest()
            ? ChannelCredentials.createInsecure()
            : ChannelCredentials.createSsl(null, null, null, {
                rejectUnauthorized: false,
              });
        if (certPath) {
          try {
            // Dynamic import for Node.js only
            const fs = await import("fs");
            const cert = fs.readFileSync(certPath);
            return createChannel(address, ChannelCredentials.createSsl(cert));
          } catch (error) {
            console.error("Error reading certificate:", error);
            // Fallback to insecure for development
            return createChannel(address, insecureCredentials);
          }
        } else {
          // No cert provided, use insecure SSL for development
          return createChannel(address, insecureCredentials);
        }
      } else {
        // Browser environment - nice-grpc-web handles TLS automatically
        return createChannel(address);
      }
    } catch (error) {
      console.error("Channel creation error:", error);
      throw new NetworkError(
        "Failed to create channel",
        {
          url: address,
          operation: "createChannel",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error instanceof Error ? error : undefined,
      );
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
    const channel = await this.createChannelWithTLS(address, certPath);

    const authMiddleware = this.createAuthMiddleWare(address, authToken);
    const client = this.createGrpcClient<SparkServiceClient>(
      SparkServiceDefinition,
      channel,
      authMiddleware,
    );

    this.clients.set(address, { client, authToken });
    return client;
  }

  private async authenticate(address: string, certPath?: string) {
    try {
      const identityPublicKey = await this.config.signer.getIdentityPublicKey();
      const sparkAuthnClient = await this.createSparkAuthnGrpcConnection(
        address,
        certPath,
      );

      const challengeResp = await sparkAuthnClient.get_challenge({
        publicKey: identityPublicKey,
      });

      if (!challengeResp.protectedChallenge?.challenge) {
        throw new AuthenticationError("Invalid challenge response", {
          endpoint: "get_challenge",
          reason: "Missing challenge in response",
        });
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
      throw new AuthenticationError(
        "Authentication failed",
        {
          endpoint: "authenticate",
          reason: error.message,
        },
        error,
      );
    }
  }

  private async createSparkAuthnGrpcConnection(
    address: string,
    certPath?: string,
  ): Promise<SparkAuthnServiceClient & { close?: () => void }> {
    const channel = await this.createChannelWithTLS(address, certPath);
    return this.createGrpcClient<SparkAuthnServiceClient>(
      SparkAuthnServiceDefinition,
      channel,
    );
  }

  private createAuthMiddleWare(address: string, authToken: string) {
    if (isNode) {
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
        return yield* call.next(call.request, {
          ...options,
          metadata: Metadata(options.metadata)
            .set(
              "Authorization",
              `Bearer ${this.clients.get(address)?.authToken || initialAuthToken}`,
            )
            .set("User-Agent", "spark-js-sdk"),
        });
      } catch (error: any) {
        if (error.message?.includes("token has expired")) {
          const newAuthToken = await this.authenticate(address);
          // @ts-ignore - We can only get here if the client exists
          this.clients.get(address).authToken = newAuthToken;

          return yield* call.next(call.request, {
            ...options,
            metadata: Metadata(options.metadata)
              .set("Authorization", `Bearer ${newAuthToken}`)
              .set("User-Agent", "spark-js-sdk"),
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
        return yield* call.next(call.request, {
          ...options,
          metadata: Metadata(options.metadata)
            .set(
              "Authorization",
              `Bearer ${this.clients.get(address)?.authToken || initialAuthToken}`,
            )
            .set("X-Requested-With", "XMLHttpRequest")
            .set("X-Grpc-Web", "1")
            .set("Content-Type", "application/grpc-web+proto")
            .set("User-Agent", "spark-js-sdk"),
        });
      } catch (error: any) {
        if (error.message?.includes("token has expired")) {
          const newAuthToken = await this.authenticate(address);
          // @ts-ignore - We can only get here if the client exists
          this.clients.get(address).authToken = newAuthToken;

          return yield* call.next(call.request, {
            ...options,
            metadata: Metadata(options.metadata)
              .set("Authorization", `Bearer ${newAuthToken}`)
              .set("X-Requested-With", "XMLHttpRequest")
              .set("X-Grpc-Web", "1")
              .set("Content-Type", "application/grpc-web+proto")
              .set("User-Agent", "spark-js-sdk"),
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
    let clientFactory = createClientFactory().use(retryMiddleware);
    if (middleware) {
      clientFactory = clientFactory.use(middleware);
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
