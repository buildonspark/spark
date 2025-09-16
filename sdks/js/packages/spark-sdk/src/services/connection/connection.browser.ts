import { ConnectionManager } from "./connection.js";
import {
  createChannel,
  FetchTransport,
  createClientFactory,
  type Channel as ChannelWeb,
  type ClientFactory as ClientFactoryWeb,
} from "nice-grpc-web";
import { ClientMiddlewareCall, Metadata } from "nice-grpc-common";
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

  protected async createChannelWithTLS(address: string, certPath?: string) {
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
    return async function* (
      this: ConnectionManagerBrowser,
      call: ClientMiddlewareCall<any, any>,
      options: SparkCallOptions,
    ) {
      const metadata = Metadata(options.metadata)
        .set("X-Requested-With", "XMLHttpRequest")
        .set("X-Grpc-Web", "1")
        .set("X-Client-Env", clientEnv)
        .set("Content-Type", "application/grpc-web+proto");
      return yield* call.next(call.request, {
        ...options,
        metadata,
      });
    }.bind(this);
  }

  protected createMiddleware(address: string, initialAuthToken: string) {
    return async function* (
      this: ConnectionManagerBrowser,
      call: ClientMiddlewareCall<any, any>,
      options: SparkCallOptions,
    ) {
      const metadata = Metadata(options.metadata)
        .set("X-Requested-With", "XMLHttpRequest")
        .set("X-Grpc-Web", "1")
        .set("X-Client-Env", clientEnv)
        .set("Content-Type", "application/grpc-web+proto");

      try {
        return yield* call.next(call.request, {
          ...options,
          metadata: metadata.set(
            "Authorization",
            `Bearer ${this.clients.get(address)?.authToken || initialAuthToken}`,
          ),
        });
      } catch (error: any) {
        return yield* this.handleMiddlewareError(
          error,
          address,
          call,
          metadata,
          options,
        );
      }
    }.bind(this);
  }

  protected async createGrpcClient<T>(
    defintion:
      | SparkAuthnServiceDefinition
      | SparkServiceDefinition
      | SparkTokenServiceDefinition,
    channel: ChannelWeb,
    withRetries: boolean,
    middleware?: any,
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
    const client = clientFactory.create(defintion, channel, {
      "*": options,
    }) as T;
    return {
      ...client,
      close: undefined,
    };
  }
}
