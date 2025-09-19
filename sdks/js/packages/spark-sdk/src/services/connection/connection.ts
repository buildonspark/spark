import { isBare, isError } from "@lightsparkdev/core";
import { sha256 } from "@noble/hashes/sha2";
import type { Channel, ClientFactory } from "nice-grpc";
import { retryMiddleware } from "nice-grpc-client-middleware-retry";
import { ClientMiddlewareCall, Metadata } from "nice-grpc-common";
import {
  type Channel as ChannelWeb,
  type ClientFactory as ClientFactoryWeb,
} from "nice-grpc-web";
import { clientEnv } from "../../constants.js";
import { AuthenticationError, NetworkError } from "../../errors/types.js";
import { MockServiceClient, MockServiceDefinition } from "../../proto/mock.js";
import {
  SparkServiceClient,
  SparkServiceDefinition,
} from "../../proto/spark.js";
import {
  Challenge,
  SparkAuthnServiceClient,
  SparkAuthnServiceDefinition,
} from "../../proto/spark_authn.js";
import {
  SparkTokenServiceClient,
  SparkTokenServiceDefinition,
} from "../../proto/spark_token.js";
import { RetryOptions, SparkCallOptions } from "../../types/grpc.js";
import { WalletConfigService } from "../config.js";

type SparkAuthnServiceClientWithClose = SparkAuthnServiceClient & {
  close?: () => void;
};

export class ConnectionManager {
  private config: WalletConfigService;
  protected clients: Map<
    string,
    {
      client: SparkServiceClient & { close?: () => void };
      authToken: string;
    }
  > = new Map();
  private tokenClients: Map<
    string,
    {
      client: SparkTokenServiceClient & { close?: () => void };
      authToken: string;
    }
  > = new Map();

  // We are going to .unref() the underlying channels for stream clients
  // to prevent the stream from keeping the process alive
  // Using a different map to avoid unforeseen problems with unary calls
  private streamClients: Map<
    string,
    {
      client: SparkServiceClient & { close?: () => void };
      authToken: string;
      channel: Channel | ChannelWeb;
    }
  > = new Map();

  // Tracks in-flight authenticate() promises so concurrent callers share one
  private authPromises: Map<string, Promise<string>> = new Map();

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

  protected createChannelWithTLS(
    address: string,
    certPath?: string,
  ): Promise<Channel | ChannelWeb> {
    throw new Error("createChannelWithTLS: Not implemented");
  }

  async createSparkStreamClient(
    address: string,
    certPath?: string,
  ): Promise<SparkServiceClient & { close?: () => void }> {
    if (this.streamClients.has(address)) {
      return this.streamClients.get(address)!.client;
    }
    const authToken = await this.authenticate(address);
    const channel = await this.createChannelWithTLS(address, certPath);

    if (!channel) {
      throw new NetworkError("Failed to create channel", {
        url: address,
        operation: "createChannel",
        errorCount: 1,
        errors: "Channel is undefined",
      });
    }

    const middleware = this.createMiddleware(address, authToken);
    const client = await this.createGrpcClient<SparkServiceClient>(
      SparkServiceDefinition,
      channel,
      true,
      middleware,
    );

    this.streamClients.set(address, { client, authToken, channel });
    return client;
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

    const middleware = this.createMiddleware(address, authToken);
    const client = await this.createGrpcClient<SparkServiceClient>(
      SparkServiceDefinition,
      channel,
      true,
      middleware,
    );

    this.clients.set(address, { client, authToken });
    return client;
  }

  async createSparkTokenClient(
    address: string,
    certPath?: string,
  ): Promise<SparkTokenServiceClient & { close?: () => void }> {
    if (this.tokenClients.has(address)) {
      return this.tokenClients.get(address)!.client;
    }
    const authToken = await this.authenticate(address);
    const channel = await this.createChannelWithTLS(address, certPath);

    const middleware = this.createMiddleware(address, authToken);
    const tokenClient = await this.createGrpcClient<SparkTokenServiceClient>(
      SparkTokenServiceDefinition,
      channel,
      true,
      middleware,
    );

    this.tokenClients.set(address, { client: tokenClient, authToken });
    return tokenClient;
  }

  async getStreamChannel(address: string) {
    return this.streamClients.get(address)?.channel;
  }

  private async authenticate(address: string, certPath?: string) {
    const existing = this.authPromises.get(address);
    if (existing) {
      return existing;
    }

    const authPromise = (async () => {
      const MAX_ATTEMPTS = 3;
      let lastError: Error | undefined;

      /* React Native can cause some outgoing requests to be paused which can result
         in challenges expiring, so we'll retry any authentication failures: */
      for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
        console.log(`connection attempt ${attempt}`);
        let sparkAuthnClient: SparkAuthnServiceClientWithClose | undefined;
        try {
          const identityPublicKey =
            await this.config.signer.getIdentityPublicKey();
          sparkAuthnClient = await this.createSparkAuthnGrpcConnection(
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
        } catch (error: unknown) {
          if (isError(error)) {
            sparkAuthnClient?.close?.();

            if (error.message.includes("challenge expired")) {
              console.warn(
                `Authentication attempt ${attempt + 1} failed due to expired challenge, retrying...`,
              );
              lastError = error;
              continue;
            }

            if (
              error.message.includes("UNAVAILABLE: No connection established.")
            ) {
              console.warn(
                `Authentication attempt ${attempt + 1} failed due to unavailable status, retrying...`,
              );
              lastError = error;
              continue;
            }

            throw new AuthenticationError(
              "Authentication failed",
              {
                endpoint: "authenticate",
                reason: error.message,
              },
              error,
            );
          } else {
            lastError = new Error(
              `Unknown error during authentication: ${String(error)}`,
            );
          }
        }
      }

      throw new AuthenticationError(
        "Authentication failed after retrying expired challenges",
        {
          endpoint: "authenticate",
          reason: lastError?.message ?? "Unknown error",
        },
        lastError,
      );
    })();

    this.authPromises.set(address, authPromise);

    try {
      return await authPromise;
    } finally {
      this.authPromises.delete(address);
    }
  }

  private async createSparkAuthnGrpcConnection(
    address: string,
    certPath?: string,
  ): Promise<SparkAuthnServiceClientWithClose> {
    const channel = await this.createChannelWithTLS(address, certPath);
    const authnMiddleware = this.createAuthnMiddleware();
    return this.createGrpcClient<SparkAuthnServiceClient>(
      SparkAuthnServiceDefinition,
      channel,
      false,
      authnMiddleware,
    );
  }

  protected createAuthnMiddleware() {
    return async function* (
      this: ConnectionManager,
      call: ClientMiddlewareCall<any, any>,
      options: SparkCallOptions,
    ) {
      return yield* call.next(call.request, options);
    }.bind(this);
  }

  protected createMiddleware(
    address: string,
    authToken: string,
  ):
    | ((
        call: ClientMiddlewareCall<any, any>,
        options: SparkCallOptions,
      ) => AsyncGenerator<any, any, undefined>)
    | undefined {
    return undefined;
  }

  protected async *handleMiddlewareError(
    error: unknown,
    address: string,
    call: ClientMiddlewareCall<any, any>,
    metadata: Metadata,
    options: SparkCallOptions,
  ) {
    if (isError(error)) {
      if (error.message.includes("token has expired")) {
        const newAuthToken = await this.authenticate(address);
        const clientData = this.clients.get(address);
        if (!clientData) {
          throw new Error(`No client found for address: ${address}`);
        }
        clientData.authToken = newAuthToken;

        return yield* call.next(call.request, {
          ...options,
          metadata: metadata.set("Authorization", `Bearer ${newAuthToken}`),
        });
      }
    }

    throw error;
  }

  protected async createGrpcClient<T>(
    defintion:
      | SparkAuthnServiceDefinition
      | SparkServiceDefinition
      | SparkTokenServiceDefinition,
    channel: Channel | ChannelWeb,
    withRetries: boolean,
    middleware?: any,
  ): Promise<T & { close?: () => void }> {
    throw new Error("createGrpcClient: Not implemented");
  }
}
