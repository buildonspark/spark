import fs from "fs";
import { createHash } from "crypto";
import {
  ChannelCredentials,
  createChannel,
  createClient,
  createClientFactory,
  type Channel,
} from "nice-grpc";
import {
  retryMiddleware,
  type RetryOptions,
} from "nice-grpc-client-middleware-retry";
import type { ClientMiddleware } from "nice-grpc-common";
import { type Metadata, Status } from "nice-grpc-common";
import { getClientEnv } from "../../constants.js";
import { SparkRequestError } from "../../errors/types.js";
import {
  type MockServiceClient,
  MockServiceDefinition,
} from "../../proto/mock.js";
import { type SparkServiceDefinition } from "../../proto/spark.js";
import { type SparkAuthnServiceDefinition } from "../../proto/spark_authn.js";
import { type SparkTokenServiceDefinition } from "../../proto/spark_token.js";
import { type WalletConfigService } from "../config.js";
import { getMonotonicTime } from "../time-sync.js";
import type { LoggingService } from "../../utils/logging-service.js";
import type { ResolvedTlsOptions } from "../wallet-config.js";
import { type AuthMode, ConnectionManager } from "./connection.js";

// The default @grpc/grpc-js message size limit is 4 MB. Wallets with many
// leaves can exceed this — e.g. start_transfer_v2 responses have been observed
// at ~5 MB. Bump to 20 MB to provide headroom. This only affects Node.js;
// browser and Bare runtimes use fetch-based transports with no client-side
// message size limit.
const MAX_MESSAGE_SIZE = 20 * 1024 * 1024; // 20 MB

// grpc-js advertises a 64 KB HTTP/2 window by default. On high-RTT links, a
// multi-MB response exhausts the window repeatedly and can get torn down by
// the server's stall detector with RST_STREAM INTERNAL before the response
// finishes delivering. `grpc-node.flow_control_window` is the grpc-js-specific
// knob: a single value that's applied as the per-stream initial window size
// (advertised in the HTTP/2 SETTINGS frame) AND, via session.setLocalWindowSize,
// as the connection-level window. 16 MB eliminates the repeated-stall class for
// any realistic response size and matches `WithInitialConnWindowSize` on the
// SO-to-SO internal client (spark/so/operator.go).
const HTTP2_FLOW_CONTROL_WINDOW = 16 * 1024 * 1024; // 16 MB

const CHANNEL_OPTIONS = {
  "grpc.max_receive_message_length": MAX_MESSAGE_SIZE,
  "grpc.max_send_message_length": MAX_MESSAGE_SIZE,
  "grpc-node.flow_control_window": HTTP2_FLOW_CONTROL_WINDOW,
};

type GrpcChannelWithInternalSocket = {
  internalChannel?: {
    currentPicker?: {
      subchannel?: {
        child?: {
          transport?: {
            session?: {
              socket?: {
                unref: () => void;
              };
            };
          };
        };
      };
    };
  };
};

export class ConnectionManagerNodeJS extends ConnectionManager {
  private readonly walletConfig: WalletConfigService;

  constructor(
    config: WalletConfigService,
    authMode: AuthMode = "identity",
    logging?: LoggingService,
  ) {
    super(config, authMode, logging);
    this.walletConfig = config;
  }

  protected getMonotonicTime(): number {
    return getMonotonicTime();
  }

  protected prepareMetadata(metadata: Metadata): Metadata {
    return super.prepareMetadata(metadata).set("X-Client-Env", getClientEnv());
  }

  protected override makeChannelKey(address: string, stream?: boolean): string {
    return [
      super.makeChannelKey(address, stream),
      "tls",
      this.makeTlsChannelCacheKey(address),
    ].join("|");
  }

  public async createMockClient(address: string): Promise<
    MockServiceClient & {
      close: () => void | Promise<void>;
    }
  > {
    const key = this.makeChannelKey(address, false);
    const channel = await ConnectionManager.acquireChannel<Channel>(key, () =>
      this.createChannelWithTLS(address, false),
    );
    const client = createClient(MockServiceDefinition, channel);
    return {
      ...client,
      close: () => ConnectionManager.releaseChannel(key),
    };
  }

  protected createChannelWithTLS(
    address: string,
    isStreamClientType: boolean = false,
  ): Promise<Channel> {
    try {
      return Promise.resolve(
        createChannel(
          address,
          this.createChannelCredentials(address),
          CHANNEL_OPTIONS,
        ),
      );
    } catch (error) {
      if (error instanceof SparkRequestError) {
        throw error;
      }

      throw new SparkRequestError("Failed to create channel", {
        url: address,
        error,
      });
    }
  }

  protected createChannelCredentials(address: string): ChannelCredentials {
    const tlsOptions = this.walletConfig.getTlsOptionsForAddress(address);

    if (tlsOptions.dangerouslyDisableCertificateVerification) {
      if (!this.isLocalCertificateVerificationBypassAllowed(address)) {
        throw new SparkRequestError(
          "Refusing to disable TLS certificate verification for non-local signing operator",
          { url: address },
        );
      }

      return ChannelCredentials.createSsl(null, null, null, {
        rejectUnauthorized: false,
      });
    }

    const rootCa = this.loadRootCa(tlsOptions);

    if (rootCa) {
      return ChannelCredentials.createSsl(rootCa);
    }

    return ChannelCredentials.createSsl();
  }

  private makeTlsChannelCacheKey(address: string): string {
    const tlsOptions = this.walletConfig.getTlsOptionsForAddress(address);
    const dangerouslyDisableCertificateVerification =
      tlsOptions.dangerouslyDisableCertificateVerification === true;
    const tlsIdentity = {
      rootCa: this.getRootCaCacheIdentity(tlsOptions),
      dangerouslyDisableCertificateVerification,
      ...(dangerouslyDisableCertificateVerification
        ? {
            network: this.walletConfig.getNetworkType(),
            bypassAllowed:
              this.isLocalCertificateVerificationBypassAllowed(address),
          }
        : {}),
    };

    return createHash("sha256")
      .update(JSON.stringify(tlsIdentity))
      .digest("hex");
  }

  private getRootCaCacheIdentity(tlsOptions: ResolvedTlsOptions):
    | {
        path: string;
        sha256: string;
      }
    | {
        path: string;
        unreadable: true;
      }
    | null {
    if (!tlsOptions.rootCaPath) {
      return null;
    }

    try {
      const rootCa = fs.readFileSync(tlsOptions.rootCaPath);
      return {
        path: tlsOptions.rootCaPath,
        sha256: createHash("sha256").update(rootCa).digest("hex"),
      };
    } catch (error) {
      if (isMissingFileError(error)) {
        return null;
      }

      return {
        path: tlsOptions.rootCaPath,
        unreadable: true,
      };
    }
  }

  private loadRootCa(tlsOptions: ResolvedTlsOptions): Buffer | undefined {
    if (tlsOptions.rootCaPath) {
      try {
        return fs.readFileSync(tlsOptions.rootCaPath);
      } catch (error) {
        if (isMissingFileError(error)) {
          return undefined;
        }

        throw error;
      }
    }

    return undefined;
  }

  private isLocalCertificateVerificationBypassAllowed(
    address: string,
  ): boolean {
    if (this.walletConfig.getNetworkType() !== "LOCAL") {
      return false;
    }

    try {
      const hostname = new URL(address).hostname.toLowerCase();
      return (
        hostname === "localhost" ||
        hostname === "127.0.0.1" ||
        hostname === "::1" ||
        hostname === "[::1]" ||
        hostname.endsWith(".minikube.local")
      );
    } catch {
      return false;
    }
  }

  protected async createGrpcClient<T>(
    definition:
      | SparkAuthnServiceDefinition
      | SparkServiceDefinition
      | SparkTokenServiceDefinition,
    channel: Channel,
    withRetries: boolean,
    middleware?: ClientMiddleware<RetryOptions, object>,
    channelKey?: string,
  ): Promise<T & { close?: () => void | Promise<void> }> {
    const retryOptions: RetryOptions = {
      retry: true,
      retryMaxAttempts: 3,
      retryBaseDelayMs: 1000,
      retryMaxDelayMs: 10000,
      retryableStatuses: [Status.UNAVAILABLE, Status.CANCELLED],
    };
    let options: RetryOptions = {};

    let clientFactory = createClientFactory();
    if (withRetries) {
      options = retryOptions;
      clientFactory = clientFactory.use(retryMiddleware);
    }
    if (middleware) {
      clientFactory = clientFactory.use(middleware);
    }
    const client = clientFactory.create(definition, channel, {
      "*": options,
    }) as T;
    return Promise.resolve({
      ...client,
      close: channelKey
        ? () => ConnectionManager.releaseChannel(channelKey)
        : channel.close.bind(channel),
    });
  }

  override async subscribeToEvents(address: string, signal: AbortSignal) {
    const stream = await super.subscribeToEvents(address, signal);
    const channel = this.getChannelForClient("stream", address);

    if (!channel) {
      throw new Error("Failed to get channel for client");
    }

    // In Node.js, long-lived gRPC streams keep the underlying socket "ref'd",
    // which prevents the process from exiting. To avoid that (e.g. in CLI tools),
    // we manually unref the socket so Node can shut down when nothing else is active.
    //
    // The gRPC client doesn't expose the socket directly, so we dig through
    // internal fields to find it. This is a bit of a hack and may break if the
    // internals change.
    //
    // Since the socket isn't always immediately available, we retry with setTimeout
    // until it shows up.
    const maybeUnref = (): void => {
      const internalChannel = (channel as GrpcChannelWithInternalSocket)
        .internalChannel;
      const socket =
        internalChannel?.currentPicker?.subchannel?.child?.transport?.session
          ?.socket;
      if (socket) {
        socket.unref();
      } else {
        const retryTimer = setTimeout(maybeUnref, 100);
        (retryTimer as unknown as NodeJS.Timeout).unref?.();
      }
    };

    // Only need to unref in Node environments.
    // In the browser and React Native, the runtime handles shutdown when the tab/app closes.
    maybeUnref();
    return stream;
  }
}

function isMissingFileError(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    (error as NodeJS.ErrnoException).code === "ENOENT"
  );
}
