import { ConnectionManager } from "./connection.js";
import {
  createChannel,
  FetchTransport,
  createClientFactory,
  type Channel as ChannelWeb,
  type ClientFactory as ClientFactoryWeb,
} from "nice-grpc-web";
import { ClientMiddlewareCall, Metadata } from "nice-grpc-common";
import type { ClientMiddleware } from "nice-grpc-common";
import { retryMiddleware } from "nice-grpc-client-middleware-retry";
import { RetryOptions, SparkCallOptions } from "../../types/grpc.js";
import { WalletConfigService } from "../config.js";
import { clientEnv } from "../../constants.js";
import { NetworkError } from "../../errors/types.js";
import type { SparkAuthnServiceDefinition } from "../../proto/spark_authn.js";
import type { SparkServiceDefinition } from "../../proto/spark.js";
import type { SparkTokenServiceDefinition } from "../../proto/spark_token.js";

export type Transport = NonNullable<Parameters<typeof createChannel>[1]>;

export class ConnectionManagerBrowser extends ConnectionManager {
  protected transport: Transport;

  constructor(config: WalletConfigService, transport = FetchTransport()) {
    super(config);
    this.transport = transport;
  }

  protected async createChannelWithTLS(address: string) {
    try {
      return createChannel(address, this.transport);
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
      this: ConnectionManagerBrowser,
      call: ClientMiddlewareCall<Req, Res>,
      options: SparkCallOptions,
    ) {
      const metadata = Metadata(options.metadata)
        .set("X-Requested-With", "XMLHttpRequest")
        .set("X-Grpc-Web", "1")
        .set("X-Client-Env", clientEnv)
        .set("Content-Type", "application/grpc-web+proto");
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
      this: ConnectionManagerBrowser,
      call: ClientMiddlewareCall<Req, Res>,
      options: SparkCallOptions,
    ) {
      const metadata = Metadata(options.metadata)
        .set("X-Requested-With", "XMLHttpRequest")
        .set("X-Grpc-Web", "1")
        .set("X-Client-Env", clientEnv)
        .set("Content-Type", "application/grpc-web+proto");

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
    channel: ChannelWeb,
    withRetries: boolean,
    middleware?: ClientMiddleware<RetryOptions, {}>,
    channelKey?: string,
  ) {
    let clientFactory: ClientFactoryWeb;

    const retryOptions = {
      retry: true,
      retryMaxAttempts: 3,
    };
    let options: RetryOptions = {};

    clientFactory = createClientFactory();
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
        : undefined,
    };
  }
}
