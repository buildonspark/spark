import { Channel, createClientFactory, createChannel } from 'nice-grpc';
import { retryMiddleware } from 'nice-grpc-client-middleware-retry';
import { SparkServiceClient, SparkServiceDefinition } from '../../../proto/rpc/v1/service.js';
import * as fs from 'fs';
import { Lrc20ConnectionManager } from './types.ts';
import { isNode } from '@lightsparkdev/core';

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
        const { ChannelCredentials } = await import("nice-grpc");
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
    const client = this.createGrpcClient<SparkServiceClient>(SparkServiceDefinition, channel);
    this.lrc20Client = client;
    return client;
  }

  private createGrpcClient<T>(
    definition: typeof SparkServiceDefinition,
    channel: Channel,
    middleware?: any,
  ): T & { close?: () => void } {
    const clientFactory = createClientFactory().use(retryMiddleware);
    if (middleware) {
      clientFactory.use(middleware);
    }

    const client = clientFactory.create(definition, channel, {
      "*": { retry: true, retryMaxAttempts: 3 }
    }) as T;
    
    return {
      ...client,
      close: channel.close?.bind(channel)
    };
  }
}

// Export the factory function for Node.js environments
export function createLrc20ConnectionManager(lrc20ApiUrl: string): Lrc20ConnectionManager {
  return new NodeLrc20ConnectionManager(lrc20ApiUrl);
}
