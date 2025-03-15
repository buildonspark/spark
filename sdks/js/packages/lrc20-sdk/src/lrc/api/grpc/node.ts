import { Channel, ChannelCredentials, createChannel, createClientFactory } from 'nice-grpc';
import { retryMiddleware } from 'nice-grpc-client-middleware-retry';
import { SparkServiceClient, SparkServiceDefinition } from '../../../proto/rpc/v1/service.js';
import * as fs from 'fs';
import { Lrc20ConnectionManager } from './interface.js';

// Node-specific implementation of ConnectionManager functionality
class NodeLrc20ConnectionManager extends Lrc20ConnectionManager {
  constructor(lrc20ApiUrl: string) {
    super(lrc20ApiUrl);
  }

  private createChannelWithTLS(address: string, certPath?: string) {
    try {
      if (certPath) {
        try {
          const cert = fs.readFileSync(certPath);
          return createChannel(address, ChannelCredentials.createSsl(cert));
        } catch (error) {
          console.error("Error reading certificate:", error);
          return createChannel(
            address, 
            ChannelCredentials.createSsl(null, null, null, { rejectUnauthorized: false })
          );
        }
      } else {
        return createChannel(
          address,
          ChannelCredentials.createSsl(null, null, null, { rejectUnauthorized: false })
        );
      }
    } catch (error) {
      console.error("Channel creation error:", error);
      throw new Error("Failed to create channel");
    }
  }

  public async createLrc20Client(): Promise<SparkServiceClient & { close?: () => void }> {
    console.log("Creating LRC20 client (Node.js version)");
    const channel = createChannel(this.lrc20ApiUrl);
    const client = this.createGrpcClient<SparkServiceClient>(SparkServiceDefinition, channel);
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

