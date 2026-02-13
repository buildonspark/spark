import { isError } from "@lightsparkdev/core";
import { sha256 } from "@noble/hashes/sha2";
import type { Channel } from "nice-grpc";
import type { RetryOptions } from "nice-grpc-client-middleware-retry";
import type { ClientMiddleware } from "nice-grpc-common";
import {
  ClientError,
  ClientMiddlewareCall,
  Metadata,
  Status,
} from "nice-grpc-common";
import { type Channel as ChannelWeb } from "nice-grpc-web";
import { SparkError } from "../../errors/base.js";
import { SparkAuthenticationError } from "../../errors/types.js";
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
import { ServerTimeSync, getMonotonicTime } from "../time-sync.js";

// Module-level types used by shared caches
type ChannelKey = string;
type BrowserOrNodeJSChannel = Channel | ChannelWeb;

type TokenKey = string;

/**
 * Track both monotonic and wall clock for redundancy
 *
 * Monotonic is used to prevent clock skew issues
 * but it does not tick during sleep
 * https://developer.mozilla.org/en-US/docs/Web/API/Performance/now#ticking_during_sleep
 *
 * Wall clock is used to handle device sleep/app backgrounding
 * but it is not as precise as monotonic
 */
type CachedToken = {
  token: string;
  expiresAtMono: number;
  expiresAtWallMs: number;
};

/**
 * Safety margin (in seconds) to proactively refresh tokens before server-side
 * expiry. This prevents sending a valid-looking but about-to-expire token to
 * unauthenticated endpoints (like query_nodes) where the server silently drops
 * the session instead of returning UNAUTHENTICATED.
 */
const TOKEN_EXPIRY_BUFFER_SEC = 60;

type SparkAuthnServiceClientWithClose = SparkAuthnServiceClient & {
  close?: () => void;
};

type ClientWithClose<T> = T & {
  close?: () => void;
};

export type SparkClientType = "spark" | "stream" | "tokens";
/**
 * 'none' means that the client will not authenticate with the SOs.
 * 'identity' means that the client will authenticate and sign the challenge with the identity key.
 */
export type AuthMode = "none" | "identity";

/* From nice-grpc/lib/client/channel.d.ts: The address of the server,
 * in the form `protocol://host:port`, where `protocol` is one of `http`
 * or `https`. If the port is not specified, it will be inferred from the protocol. */
type Address = string;

export abstract class ConnectionManager {
  protected static readonly DATE_HEADER = "date";
  protected static readonly PROCESSING_TIME_HEADER = "x-processing-time-ms";

  // Static caches shared across all instances
  private static channelCache: Map<
    ChannelKey,
    { channel: BrowserOrNodeJSChannel; refCount: number }
  > = new Map();
  private static channelInflight: Map<
    ChannelKey,
    Promise<BrowserOrNodeJSChannel>
  > = new Map();
  private static authTokenCache: Map<TokenKey, CachedToken> = new Map();
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

  private static getCachedAuthToken(
    address: Address,
    identityHex: string,
  ): string | undefined {
    const key = ConnectionManager.makeAuthTokenKey(address, identityHex);
    const entry = ConnectionManager.authTokenCache.get(key);
    if (!entry) return undefined;

    // Proactively evict tokens that are within the buffer of server-side expiry.
    // Two complementary checks:
    //   - Monotonic: immune to clock skew, but freezes during device sleep
    //   - Wall-clock: survives device sleep, but vulnerable to clock adjustments
    const bufferMs = TOKEN_EXPIRY_BUFFER_SEC * 1000;
    if (
      getMonotonicTime() >= entry.expiresAtMono - bufferMs ||
      Date.now() >= entry.expiresAtWallMs - bufferMs
    ) {
      ConnectionManager.authTokenCache.delete(key);
      return undefined;
    }

    return entry.token;
  }

  private static setCachedAuthToken(
    address: Address,
    identityHex: string,
    authToken: string,
    expiresAtSec: number,
    nowSec: number,
  ) {
    // Convert server-relative expiry to a monotonic deadline so that all
    // future cache reads are instance-independent and clock-skew-safe.
    const ttlMs = (expiresAtSec - nowSec) * 1000;
    ConnectionManager.authTokenCache.set(
      ConnectionManager.makeAuthTokenKey(address, identityHex),
      {
        token: authToken,
        expiresAtMono: getMonotonicTime() + ttlMs,
        expiresAtWallMs: Date.now() + ttlMs,
      },
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
    getNowSec: () => number,
    authenticate: () => Promise<{ token: string; expiresAtSec: number }>,
  ): Promise<string> {
    const cached = ConnectionManager.getCachedAuthToken(address, identityHex);
    if (cached) {
      return cached;
    }

    const tokenKey = ConnectionManager.makeAuthTokenKey(address, identityHex);
    let authPromise = ConnectionManager.authInflight.get(tokenKey);
    if (!authPromise) {
      authPromise = (async () => {
        const result = await authenticate();
        ConnectionManager.setCachedAuthToken(
          address,
          identityHex,
          result.token,
          result.expiresAtSec,
          getNowSec(),
        );
        return result.token;
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
  private timeSync: ServerTimeSync;
  private authMode: AuthMode;

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

  constructor(config: WalletConfigService, authMode: AuthMode = "identity") {
    this.config = config;
    this.timeSync = new ServerTimeSync();
    this.authMode = authMode;
  }

  public getCurrentServerTime(): Date {
    const serverTime = this.timeSync.getCurrentServerTime();
    if (!serverTime) {
      return new Date();
    }

    return serverTime;
  }

  public isTimeSynced(): boolean {
    return this.timeSync.isSynced();
  }

  protected getMonotonicTime(): number {
    return getMonotonicTime();
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

    if (this.authMode === "identity") {
      await this.authenticate(address);
    }
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
    // Use server-synced time when available to avoid clock-skew issues.
    // The server sets token expiry based on its own clock, so we must compare
    // against that same reference to avoid premature or late eviction.
    return ConnectionManager.getOrCreateAuthToken(
      address,
      identityHex,
      () => Math.floor(this.getCurrentServerTime().getTime() / 1000),
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
              throw new SparkAuthenticationError("Invalid challenge response", {
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
            return {
              token: verifyResp.sessionToken,
              expiresAtSec: verifyResp.expirationTimestamp,
            };
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

              throw new SparkAuthenticationError("Authentication failed", {
                endpoint: "authenticate",
                reason: error.message,
                error,
              });
            } else {
              lastError = new Error(
                `Unknown error during authentication: ${String(error)}`,
              );
            }
          }
        }

        throw new SparkAuthenticationError(
          "Authentication failed after retrying expired challenges",
          {
            endpoint: "authenticate",
            reason: lastError?.message ?? "Unknown error",
            error: lastError,
          },
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
      throw new SparkError("Failed to create Spark Authn gRPC connection", {
        error,
      });
    }
  }

  protected prepareMetadata(metadata: Metadata): Metadata {
    return metadata;
  }

  protected createAuthnMiddleware() {
    return async function* <Req, Res>(
      this: ConnectionManager,
      call: ClientMiddlewareCall<Req, Res>,
      options: SparkCallOptions,
    ) {
      const metadata = this.prepareMetadata(Metadata(options.metadata));
      return yield* call.next(call.request as Req, {
        ...options,
        metadata,
      });
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
      const metadata = this.prepareMetadata(Metadata(options.metadata));
      const authToken =
        this.authMode === "identity"
          ? await this.authenticate(address)
          : undefined;
      const sendTime = this.getMonotonicTime();
      const receiveTime = { value: 0 };

      try {
        const generator = call.next(call.request as Req, {
          ...options,
          metadata: metadata.set("Authorization", `Bearer ${authToken}`),
          onHeader: (header: Metadata) => {
            receiveTime.value = this.getMonotonicTime();

            const dateHeader = header.get(ConnectionManager.DATE_HEADER);
            const processingTimeHeader = header.get(
              ConnectionManager.PROCESSING_TIME_HEADER,
            );

            if (dateHeader && processingTimeHeader) {
              const wasSynced = this.timeSync.isSynced();

              const serverProcessingTimeMs = parseFloat(processingTimeHeader);
              this.timeSync.recordSync(
                dateHeader,
                serverProcessingTimeMs,
                sendTime,
                receiveTime.value,
              );

              // Since the server time isn't known at the time,
              // the first auth call computes TTL from the client clock (monotonic + wall clock)
              // If the client clock is skewed these tokens may expire before the eviction check can catch them
              // Invalidate any tokens that were cached before the server time was known.
              if (!wasSynced && this.timeSync.isSynced()) {
                ConnectionManager.authTokenCache.clear();
              }
            }
          },
        });

        let result = await generator.next();

        while (!result.done) {
          yield result.value;
          result = await generator.next();
        }

        if (result.value !== undefined) {
          return result.value;
        }
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
        const newAuthToken =
          this.authMode === "identity"
            ? await this.authenticate(address)
            : undefined;

        return yield* call.next(call.request as Req, {
          ...options,
          metadata: metadata.set("Authorization", `Bearer ${newAuthToken}`),
        });
      } else if (error instanceof ClientError) {
        if (error.code === Status.RESOURCE_EXHAUSTED) {
          throw new Error("Server is busy, please try again later.");
        }
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
