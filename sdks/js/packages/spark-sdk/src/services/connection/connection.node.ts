import fs from "fs";
import {
  ChannelCredentials,
  createChannel,
  createClient,
  createClientFactory,
  type Channel,
} from "nice-grpc";
import {
  retryMiddleware,
  RetryOptions,
} from "nice-grpc-client-middleware-retry";
import type { ClientMiddleware } from "nice-grpc-common";
import { Metadata, Status } from "nice-grpc-common";
import { getClientEnv } from "../../constants.js";
import { SparkRequestError } from "../../errors/types.js";
import { MockServiceClient, MockServiceDefinition } from "../../proto/mock.js";
import { SparkServiceDefinition } from "../../proto/spark.js";
import { SparkAuthnServiceDefinition } from "../../proto/spark_authn.js";
import { SparkTokenServiceDefinition } from "../../proto/spark_token.js";
import { WalletConfigService } from "../config.js";
import { getMonotonicTime } from "../time-sync.js";
import type { LoggingService } from "../../utils/logging-service.js";
import { AuthMode, ConnectionManager } from "./connection.js";

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

export class ConnectionManagerNodeJS extends ConnectionManager {
  private certPath: string | null = null;

  constructor(
    config: WalletConfigService,
    authMode: AuthMode = "identity",
    logging?: LoggingService,
  ) {
    super(config, authMode, logging);
  }

  protected getMonotonicTime(): number {
    return getMonotonicTime();
  }

  protected prepareMetadata(metadata: Metadata): Metadata {
    return super.prepareMetadata(metadata).set("X-Client-Env", getClientEnv());
  }

  public async createMockClient(address: string): Promise<
    MockServiceClient & {
      close: () => void;
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

  protected async createChannelWithTLS(
    address: string,
    isStreamClientType: boolean = false,
  ) {
    try {
      if (this.certPath) {
        try {
          const cert = fs.readFileSync(this.certPath);
          return createChannel(
            address,
            ChannelCredentials.createSsl(cert),
            CHANNEL_OPTIONS,
          );
        } catch (error) {
          this.logger.warn(
            `Error reading certificate: ${
              error instanceof Error ? error.message : String(error)
            }`,
          );
          return createChannel(
            address,
            ChannelCredentials.createSsl(null, null, null, {
              rejectUnauthorized: false,
            }),
            CHANNEL_OPTIONS,
          );
        }
      } else {
        const ch = createChannel(
          address,
          ChannelCredentials.createSsl(null, null, null, {
            rejectUnauthorized: false,
          }),
          CHANNEL_OPTIONS,
        );
        return ch;
      }
    } catch (error) {
      throw new SparkRequestError("Failed to create channel", {
        url: address,
        error,
      });
    }
  }

  protected async createGrpcClient<T>(
    definition:
      | SparkAuthnServiceDefinition
      | SparkServiceDefinition
      | SparkTokenServiceDefinition,
    channel: Channel,
    withRetries: boolean,
    middleware?: ClientMiddleware<RetryOptions, {}>,
    channelKey?: string,
  ) {
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
    return {
      ...client,
      close: channelKey
        ? () => ConnectionManager.releaseChannel(channelKey)
        : channel.close.bind(channel),
    };
  }

  override async subscribeToEvents(address: string, signal: AbortSignal) {
    const stream = await super.subscribeToEvents(address, signal);
    const channel = await this.getChannelForClient("stream", address);

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
    const maybeUnref = () => {
      const internalChannel = (channel as any).internalChannel;
      if (
        internalChannel?.currentPicker?.subchannel?.child?.transport?.session
          ?.socket
      ) {
        internalChannel.currentPicker.subchannel.child.transport.session.socket.unref();
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
