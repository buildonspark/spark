import {
  retryMiddleware,
  RetryOptions,
} from "nice-grpc-client-middleware-retry";
import type { ClientMiddleware } from "nice-grpc-common";
import { Metadata, Status } from "nice-grpc-common";
import {
  createChannel,
  createClientFactory,
  FetchTransport,
  type Channel as ChannelWeb,
  type ClientFactory as ClientFactoryWeb,
} from "nice-grpc-web";
import { clientEnv } from "../../constants.js";
import { SparkRequestError } from "../../errors/types.js";
import type { SparkServiceDefinition } from "../../proto/spark.js";
import type { SparkAuthnServiceDefinition } from "../../proto/spark_authn.js";
import type { SparkTokenServiceDefinition } from "../../proto/spark_token.js";
import { WalletConfigService } from "../config.js";
import { getMonotonicTime } from "../time-sync.js";
import { AuthMode, ConnectionManager } from "./connection.js";

export type Transport = NonNullable<Parameters<typeof createChannel>[1]>;

export class ConnectionManagerBrowser extends ConnectionManager {
  protected transport: Transport;

  constructor(
    config: WalletConfigService,
    authMode: AuthMode = "identity",
    transport = FetchTransport(),
  ) {
    super(config, authMode);
    this.transport = transport;
  }

  protected getMonotonicTime(): number {
    return getMonotonicTime();
  }

  protected prepareMetadata(metadata: Metadata): Metadata {
    return metadata
      .set("X-Requested-With", "XMLHttpRequest")
      .set("X-Grpc-Web", "1")
      .set("X-Client-Env", clientEnv)
      .set("Content-Type", "application/grpc-web+proto");
  }

  protected async createChannelWithTLS(address: string) {
    try {
      return createChannel(address, this.transport);
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
    channel: ChannelWeb,
    withRetries: boolean,
    middleware?: ClientMiddleware<RetryOptions, {}>,
    channelKey?: string,
  ) {
    let clientFactory: ClientFactoryWeb;

    const retryOptions = {
      retry: true,
      retryMaxAttempts: 3,
      retryBaseDelayMs: 1000,
      retryMaxDelayMs: 10000,
      retryableStatuses: [Status.UNAVAILABLE, Status.CANCELLED],
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
