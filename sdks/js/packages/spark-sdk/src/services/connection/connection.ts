import { isError } from "@lightsparkdev/core";
import { sha256 } from "@noble/hashes/sha2";
import type { Channel } from "nice-grpc";
import { ClientMiddlewareCall, Metadata } from "nice-grpc-common";
import type { ClientMiddleware } from "nice-grpc-common";
import { type Channel as ChannelWeb } from "nice-grpc-web";
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
import { SparkCallOptions } from "../../types/grpc.js";
import { WalletConfigService } from "../config.js";
import { SparkSDKError } from "../../errors/base.js";
import type { RetryOptions } from "nice-grpc-client-middleware-retry";

// Module-level types used by shared caches
type ChannelKey = string;
type BrowserOrNodeJSChannel = Channel | ChannelWeb;

type TokenKey = string;

type SparkAuthnServiceClientWithClose = SparkAuthnServiceClient & {
  close?: () => void;
};

type ClientWithClose<T> = T & {
  close?: () => void;
};

export type SparkClientType = "spark" | "stream" | "tokens";

/* From nice-grpc/lib/client/channel.d.ts: The address of the server,
 * in the form `protocol://host:port`, where `protocol` is one of `http`
 * or `https`. If the port is not specified, it will be inferred from the protocol. */
type Address = string;

export abstract class ConnectionManager {
  // Static caches shared across all instances
  private static channelCache: Map<
    ChannelKey,
    { channel: BrowserOrNodeJSChannel; refCount: number }
  > = new Map();
  private static channelInflight: Map<
    ChannelKey,
    Promise<BrowserOrNodeJSChannel>
  > = new Map();
  private static authTokenCache: Map<TokenKey, string> = new Map();
  private static authInflight: Map<TokenKey, Promise<string>> = new Map();

  protected makeChannelKey(address: Address, stream?: boolean): ChannelKey {
    return [address, stream ? "stream" : "unary"].join("|");
  }

  protected static async acquireChannel<T extends BrowserOrNodeJSChannel>(
    key: ChannelKey,
    create: () => Promise<T>,
  ): Promise<T> {
    const existing = ConnectionManager.channelCache.get(key);
    if (existing) {
      existing.refCount++;
      return existing.channel as T;
    }
    let channelPromise = ConnectionManager.channelInflight.get(key);
    if (!channelPromise) {
      channelPromise = (async () => {
        const ch = (await create()) as BrowserOrNodeJSChannel;
        ConnectionManager.channelCache.set(key, { channel: ch, refCount: 1 });
        return ch as BrowserOrNodeJSChannel;
      })();
      ConnectionManager.channelInflight.set(key, channelPromise);
    }
    try {
      return (await channelPromise) as T;
    } finally {
      ConnectionManager.channelInflight.delete(key);
    }
  }

  protected static releaseChannel(key: ChannelKey) {
    const entry = ConnectionManager.channelCache.get(key);
    if (!entry) return;
    entry.refCount--;
    if (entry.refCount <= 0) {
      const ch = entry.channel;
      if ("close" in ch && typeof ch.close === "function") {
        try {
          ch.close();
        } catch {}
      }
      ConnectionManager.channelCache.delete(key);
    }
  }

  private static makeAuthTokenKey(
    address: Address,
    identityHex: string,
  ): TokenKey {
    return `${address}|${identityHex}`;
  }

  private static getCachedAuthToken(address: Address, identityHex: string) {
    return ConnectionManager.authTokenCache.get(
      ConnectionManager.makeAuthTokenKey(address, identityHex),
    );
  }

  private static setCachedAuthToken(
    address: Address,
    identityHex: string,
    authToken: string,
  ) {
    ConnectionManager.authTokenCache.set(
      ConnectionManager.makeAuthTokenKey(address, identityHex),
      authToken,
    );
  }

  private static invalidateCachedAuthToken(
    address: Address,
    identityHex: string,
  ) {
    ConnectionManager.authTokenCache.delete(
      ConnectionManager.makeAuthTokenKey(address, identityHex),
    );
  }

  private static async getOrCreateAuthToken(
    address: Address,
    identityHex: string,
    authenticate: () => Promise<string>,
  ): Promise<string> {
    const cached = ConnectionManager.getCachedAuthToken(address, identityHex);
    if (cached) {
      return cached;
    }

    const tokenKey = ConnectionManager.makeAuthTokenKey(address, identityHex);
    let authPromise = ConnectionManager.authInflight.get(tokenKey);
    if (!authPromise) {
      authPromise = (async () => {
        const authToken = await authenticate();
        ConnectionManager.setCachedAuthToken(address, identityHex, authToken);
        return authToken;
      })();
      ConnectionManager.authInflight.set(tokenKey, authPromise);
    }
    try {
      return await authPromise;
    } finally {
      ConnectionManager.authInflight.delete(tokenKey);
    }
  }

  protected abstract createChannelWithTLS(
    address: Address,
    isStreamClientType?: boolean,
  ): Promise<Channel | ChannelWeb>;

  protected abstract createGrpcClient<T>(
    definition:
      | SparkAuthnServiceDefinition
      | SparkServiceDefinition
      | SparkTokenServiceDefinition,
    channel: Channel | ChannelWeb,
    withRetries: boolean,
    middleware?: ClientMiddleware<RetryOptions, {}>,
    channelKey?: ChannelKey,
  ): Promise<T & { close?: () => void }>;

  private config: WalletConfigService;

  // Note clientsByType is a per instance cache whereas channelCache is static and shared by all instances
  private clientsByType: Map<
    SparkClientType,
    Map<Address, { client: ClientWithClose<unknown>; channelKey: ChannelKey }>
  > = new Map([
    ["spark", new Map()],
    ["stream", new Map()],
    ["tokens", new Map()],
  ]);

  private identityPublicKeyHex?: string;

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
    const sparkMap = this.clientsByType.get("spark");
    if (!sparkMap) return;
    await Promise.all(
      Array.from(sparkMap.values()).map((entry) => entry.client.close?.()),
    );
    sparkMap.clear();
  }

  private getDefinitionForClientType(
    type: SparkClientType,
  ): SparkServiceDefinition | SparkTokenServiceDefinition {
    return type === "tokens"
      ? SparkTokenServiceDefinition
      : SparkServiceDefinition;
  }

  protected static isStreamClientType(type: SparkClientType) {
    return type === "stream";
  }

  private getAddressToClientMap(type: SparkClientType) {
    return this.clientsByType.get(type)!;
  }

  private async getOrCreateClientInternal<T>(
    type: SparkClientType,
    address: Address,
  ): Promise<ClientWithClose<T>> {
    const addressToClientMap = this.getAddressToClientMap(type);
    const existing = addressToClientMap.get(address);
    if (existing) {
      return existing.client as ClientWithClose<T>;
    }

    await this.authenticate(address);
    const isStreamClientType = ConnectionManager.isStreamClientType(type);
    const key = this.makeChannelKey(address, isStreamClientType);
    const channel = await ConnectionManager.acquireChannel(key, () =>
      this.createChannelWithTLS(address, isStreamClientType),
    );
    const middleware = this.createMiddleware(address);
    const def = this.getDefinitionForClientType(type);
    const client = (await this.createGrpcClient<T>(
      def,
      channel,
      true,
      middleware,
      key,
    )) as ClientWithClose<T>;

    addressToClientMap.set(address, { client, channelKey: key });
    return client;
  }

  async createSparkStreamClient(
    address: string,
  ): Promise<SparkServiceClient & { close?: () => void }> {
    return this.getOrCreateClientInternal<SparkServiceClient>(
      "stream",
      address,
    );
  }

  async createSparkClient(
    address: string,
  ): Promise<SparkServiceClient & { close?: () => void }> {
    return this.getOrCreateClientInternal<SparkServiceClient>("spark", address);
  }

  async createSparkTokenClient(
    address: string,
  ): Promise<SparkTokenServiceClient & { close?: () => void }> {
    return this.getOrCreateClientInternal<SparkTokenServiceClient>(
      "tokens",
      address,
    );
  }

  async getChannelForClient(clientType: SparkClientType, address: Address) {
    const key = this.getAddressToClientMap(clientType).get(address)?.channelKey;
    if (!key) return undefined;
    return ConnectionManager.channelCache.get(key)?.channel;
  }

  private async getIdentityPublicKeyHex(): Promise<string> {
    if (this.identityPublicKeyHex) return this.identityPublicKeyHex;
    const identityPublicKey = await this.config.signer.getIdentityPublicKey();
    const hex = Array.from(identityPublicKey)
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("");
    this.identityPublicKeyHex = hex;
    return hex;
  }

  protected async authenticate(address: string) {
    const identityHex = await this.getIdentityPublicKeyHex();
    return ConnectionManager.getOrCreateAuthToken(
      address,
      identityHex,
      async () => {
        const MAX_ATTEMPTS = 8;
        let lastError: Error | undefined;

        const identityPublicKey =
          await this.config.signer.getIdentityPublicKey();
        const sparkAuthnClient =
          await this.createSparkAuthnGrpcConnection(address);

        for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
          try {
            const challengeResp = await sparkAuthnClient.get_challenge({
              publicKey: identityPublicKey,
            });
            const protectedChallenge = challengeResp.protectedChallenge;
            const challenge = protectedChallenge?.challenge;

            if (!challenge) {
              throw new AuthenticationError("Invalid challenge response", {
                endpoint: "get_challenge",
                reason: "Missing challenge in response",
              });
            }

            const challengeBytes = Challenge.encode(challenge).finish();
            const hash = sha256(challengeBytes);

            const derSignatureBytes =
              await this.config.signer.signMessageWithIdentityKey(hash);

            const verifyResp = await sparkAuthnClient.verify_challenge({
              protectedChallenge,
              signature: derSignatureBytes,
              publicKey: identityPublicKey,
            });

            if (sparkAuthnClient.close) {
              sparkAuthnClient.close();
            }
            return verifyResp.sessionToken;
          } catch (error: unknown) {
            if (isError(error)) {
              if (sparkAuthnClient.close) {
                sparkAuthnClient.close();
              }

              if (isExpiredChallengeError(error, attempt)) {
                lastError = error;
                continue;
              }

              if (isConnectionError(error, attempt)) {
                lastError = error;
                await new Promise((resolve) => setTimeout(resolve, 250));
                continue;
              }

              throw new AuthenticationError(
                "Authentication failed",
                { endpoint: "authenticate", reason: error.message },
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
      },
    );
  }

  private async createSparkAuthnGrpcConnection(
    address: string,
  ): Promise<SparkAuthnServiceClientWithClose> {
    try {
      const key = this.makeChannelKey(address, false);
      const channel = await ConnectionManager.acquireChannel(key, () =>
        this.createChannelWithTLS(address, false),
      );
      const authnMiddleware = this.createAuthnMiddleware();
      const client = await this.createGrpcClient<SparkAuthnServiceClient>(
        SparkAuthnServiceDefinition,
        channel,
        false,
        authnMiddleware,
        key,
      );
      return client;
    } catch (error) {
      throw new SparkSDKError(
        "Failed to create Spark Authn gRPC connection",
        {},
        error instanceof Error ? error : new Error(String(error)),
      );
    }
  }

  protected createAuthnMiddleware() {
    return async function* <Req, Res>(
      this: ConnectionManager,
      call: ClientMiddlewareCall<Req, Res>,
      options: SparkCallOptions,
    ) {
      return yield* call.next(call.request, options);
    }.bind(this) as <Req, Res>(
      call: ClientMiddlewareCall<Req, Res>,
      options: SparkCallOptions,
    ) => AsyncGenerator<Res, Res | void, undefined>;
  }

  protected createMiddleware(address: Address) {
    return async function* <Req, Res>(
      this: ConnectionManager,
      call: ClientMiddlewareCall<Req, Res>,
      options: SparkCallOptions,
    ) {
      const metadata = Metadata(options.metadata);
      const authToken = await this.authenticate(address);
      try {
        return yield* call.next(call.request as Req, {
          ...options,
          metadata: metadata.set("Authorization", `Bearer ${authToken}`),
        });
      } catch (error: unknown) {
        return yield* this.handleMiddlewareError(
          error,
          address,
          call,
          metadata,
          options,
        );
      }
    }.bind(this) as <Req, Res>(
      call: ClientMiddlewareCall<Req, Res>,
      options: SparkCallOptions,
    ) => AsyncGenerator<Res, Res | void, undefined>;
  }

  protected async *handleMiddlewareError<Req, Res>(
    error: unknown,
    address: string,
    call: ClientMiddlewareCall<Req, Res>,
    metadata: Metadata,
    options: SparkCallOptions,
  ) {
    if (isError(error)) {
      if (error.message.includes("token has expired")) {
        const identityHex = await this.getIdentityPublicKeyHex();
        ConnectionManager.invalidateCachedAuthToken(address, identityHex);
        const newAuthToken = await this.authenticate(address);

        return yield* call.next(call.request as Req, {
          ...options,
          metadata: metadata.set("Authorization", `Bearer ${newAuthToken}`),
        });
      }
    }

    throw error;
  }

  async subscribeToEvents(address: string, signal: AbortSignal) {
    const sparkStreamClient = await this.createSparkStreamClient(address);
    const identityPublicKey = await this.config.signer.getIdentityPublicKey();
    const stream = sparkStreamClient.subscribe_to_events(
      { identityPublicKey },
      { signal },
    );
    return stream;
  }
}

function isExpiredChallengeError(error: Error, attempt: number) {
  const isExpired = error.message.includes("challenge expired");
  if (isExpired) {
    console.warn(
      `Authentication attempt ${attempt + 1} failed due to expired challenge, retrying...`,
    );
  }
  return isExpired;
}

function isConnectionError(error: Error, attempt: number) {
  const isConnectionError =
    error.message.includes("RST_STREAM") ||
    error.message.includes("INTERNAL") ||
    error.message.includes("Internal server error") ||
    error.message.includes("unavailable") ||
    error.message.includes("UNAVAILABLE") ||
    error.message.includes("UNKNOWN") ||
    error.message.includes("Received HTTP status code");
  if (isConnectionError) {
    console.warn(`Connection error: ${error.message}, retrying...`);
  }
  return isConnectionError;
}
