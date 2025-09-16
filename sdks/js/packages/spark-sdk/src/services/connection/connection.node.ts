import { ConnectionManager } from "./connection.js";
import {
  createClient,
  createChannel,
  createClientFactory,
  ChannelCredentials,
  type Channel,
} from "nice-grpc";
import { ClientMiddlewareCall, Metadata } from "nice-grpc-common";
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
  constructor(config: WalletConfigService) {
    super(config);
  }

  public async createMockClient(address: string): Promise<
    MockServiceClient & {
      close: () => void;
    }
  > {
    const channel = await this.createChannelWithTLS(address);

    const client = createClient(MockServiceDefinition, channel);
    return { ...client, close: () => channel.close() };
  }

  protected async createChannelWithTLS(address: string, certPath?: string) {
    try {
      if (certPath) {
        try {
          const cert = fs.readFileSync(certPath);
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
        return createChannel(
          address,
          ChannelCredentials.createSsl(null, null, null, {
            rejectUnauthorized: false,
          }),
        );
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
    return async function* (
      this: ConnectionManagerNodeJS,
      call: ClientMiddlewareCall<any, any>,
      options: SparkCallOptions,
    ) {
      const metadata = Metadata(options.metadata).set(
        "X-Client-Env",
        clientEnv,
      );
      return yield* call.next(call.request, {
        ...options,
        metadata,
      });
    }.bind(this);
  }

  protected createMiddleware(address: string, initialAuthToken: string) {
    return async function* (
      this: ConnectionManagerNodeJS,
      call: ClientMiddlewareCall<any, any>,
      options: SparkCallOptions,
    ) {
      const metadata = Metadata(options.metadata).set(
        "X-Client-Env",
        clientEnv,
      );
      try {
        return yield* call.next(call.request, {
          ...options,
          metadata: metadata.set(
            "Authorization",
            `Bearer ${this.clients.get(address)?.authToken || initialAuthToken}`,
          ),
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
    }.bind(this);
  }

  protected async createGrpcClient<T>(
    defintion:
      | SparkAuthnServiceDefinition
      | SparkServiceDefinition
      | SparkTokenServiceDefinition,
    channel: Channel,
    withRetries: boolean,
    middleware?: any,
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
    const client = clientFactory.create(defintion, channel, {
      "*": options,
    }) as T;
    return {
      ...client,
      close: channel.close.bind(channel),
    };
  }
}
