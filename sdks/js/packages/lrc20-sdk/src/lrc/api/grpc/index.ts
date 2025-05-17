import { isNode } from "@lightsparkdev/core";
import type { Channel, ClientFactory } from "nice-grpc";
import { retryMiddleware, RetryOptions } from "nice-grpc-client-middleware-retry";
import type { Channel as ChannelWeb, ClientFactory as ClientFactoryWeb } from "nice-grpc-web";
import { SparkServiceClient, SparkServiceDefinition } from "../../../proto/rpc/v1/service.js";
import { Lrc20ConnectionManager } from "./types.ts";

// Node-specific implementation of ConnectionManager functionality
class NodeLrc20ConnectionManager extends Lrc20ConnectionManager {
  private lrc20Client: SparkServiceClient | undefined;

  constructor(lrc20ApiUrl: string) {
    super(lrc20ApiUrl);
  }

  // TODO: Web transport handles TLS differently, verify that we don't need to do anything
  private async createChannelWithTLS(address: string, certPath?: string) {
    try {
      if (isNode) {
        const grpcModule = await import("nice-grpc");
        const { ChannelCredentials, createChannel } = "default" in grpcModule ? grpcModule.default : grpcModule;

        if (certPath) {
          try {
            // Dynamic import for Node.js only
            const fs = require("fs");
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
      } else {
        // Browser environment - nice-grpc-web handles TLS automatically
        const grpcModule = await import("nice-grpc-web");
        const { createChannel } = "default" in grpcModule ? grpcModule.default : grpcModule;
        return createChannel(address);
      }
    } catch (error) {
      console.error("Channel creation error:", error);
      throw new Error("Failed to create channel");
    }
  }

  public async createLrc20Client(): Promise<SparkServiceClient & { close?: () => void }> {
    if (this.lrc20Client) {
      return this.lrc20Client;
    }

    const channel = await this.createChannelWithTLS(this.lrc20ApiUrl);
    const client = await this.createGrpcClient<SparkServiceClient>(SparkServiceDefinition, channel, true);
    this.lrc20Client = client;
    return client;
  }

  private async createGrpcClient<T>(
    defintion: SparkServiceDefinition,
    channel: Channel | ChannelWeb,
    withRetries: boolean,
    middleware?: any,
  ): Promise<T & { close?: () => void }> {
    let clientFactory: ClientFactory | ClientFactoryWeb;

    const retryOptions = {
      retry: true,
      retryMaxAttempts: 3,
    };
    let options: RetryOptions = {};
    const isNodeChannel = "close" in channel;

    if (isNode && isNodeChannel) {
      const grpcModule = await import("nice-grpc");
      const { createClientFactory } = "default" in grpcModule ? grpcModule.default : grpcModule;

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
        close: channel.close.bind(channel),
      };
    } else if (!isNodeChannel) {
      const grpcModule = await import("nice-grpc-web");
      const { createClientFactory } = "default" in grpcModule ? grpcModule.default : grpcModule;

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
    } else {
      throw new Error("Channel does not have close in NodeJS environment");
    }
  }
}

// Export the factory function for Node.js environments
export function createLrc20ConnectionManager(lrc20ApiUrl: string): Lrc20ConnectionManager {
  return new NodeLrc20ConnectionManager(lrc20ApiUrl);
}
