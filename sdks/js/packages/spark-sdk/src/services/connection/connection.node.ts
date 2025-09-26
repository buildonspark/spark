import { ConnectionManager } from "./connection.js";
import {
  createClient,
  createChannel,
  createClientFactory,
  ChannelCredentials,
  type Channel,
} from "nice-grpc";
import { ClientMiddlewareCall, Metadata } from "nice-grpc-common";
import type { ClientMiddleware } from "nice-grpc-common";
import { RetryOptions, SparkCallOptions } from "../../types/grpc.js";
import { MockServiceClient, MockServiceDefinition } from "../../proto/mock.js";
import { SparkServiceDefinition } from "../../proto/spark.js";
import { SparkAuthnServiceDefinition } from "../../proto/spark_authn.js";
import { SparkTokenServiceDefinition } from "../../proto/spark_token.js";
import { openTelemetryClientMiddleware } from "nice-grpc-opentelemetry";
import { retryMiddleware } from "nice-grpc-client-middleware-retry";
import { WalletConfigService } from "../config.js";
import { NetworkError } from "../../errors/types.js";
import { clientEnv } from "../../constants.js";
import fs from "fs";

export class ConnectionManagerNodeJS extends ConnectionManager {
  private certPath: string | null = null;

  constructor(config: WalletConfigService) {
    super(config);
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
          return createChannel(address, ChannelCredentials.createSsl(cert));
        } catch (error) {
          console.error("Error reading certificate:", error);
          // Fallback to insecure for development
          return createChannel(
            address,
            ChannelCredentials.createSsl(null, null, null, {
              rejectUnauthorized: false,
            }),
          );
        }
      } else {
        // No cert provided, use insecure SSL for development
        const ch = createChannel(
          address,
          ChannelCredentials.createSsl(null, null, null, {
            rejectUnauthorized: false,
          }),
        );
        return ch;
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
        error as Error,
      );
    }
  }

  protected createAuthnMiddleware() {
    return async function* <Req, Res>(
      this: ConnectionManagerNodeJS,
      call: ClientMiddlewareCall<Req, Res>,
      options: SparkCallOptions,
    ) {
      const metadata = Metadata(options.metadata).set(
        "X-Client-Env",
        clientEnv,
      );
      return yield* call.next(call.request as Req, {
        ...options,
        metadata,
      });
    }.bind(this) as <Req, Res>(
      call: ClientMiddlewareCall<Req, Res>,
      options: SparkCallOptions,
    ) => AsyncGenerator<Res, Res | void, undefined>;
  }

  protected createMiddleware(address: string) {
    return async function* <Req, Res>(
      this: ConnectionManagerNodeJS,
      call: ClientMiddlewareCall<Req, Res>,
      options: SparkCallOptions,
    ) {
      const metadata = Metadata(options.metadata).set(
        "X-Client-Env",
        clientEnv,
      );
      try {
        const token = await this.authenticate(address);
        return yield* call.next(call.request as Req, {
          ...options,
          metadata: metadata.set("Authorization", `Bearer ${token}`),
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
    const retryOptions = {
      retry: true,
      retryMaxAttempts: 3,
    };
    let options: RetryOptions = {};

    let clientFactory = createClientFactory();
    if (withRetries) {
      options = retryOptions;
      clientFactory = clientFactory
        .use(openTelemetryClientMiddleware())
        .use(retryMiddleware);
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
    const stream = super.subscribeToEvents(address, signal);
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
        setTimeout(maybeUnref, 100);
      }
    };

    // Only need to unref in Node environments.
    // In the browser and React Native, the runtime handles shutdown when the tab/app closes.
    maybeUnref();
    return stream;
  }
}
