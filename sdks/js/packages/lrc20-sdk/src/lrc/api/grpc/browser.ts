import { createChannel, createClientFactory } from 'nice-grpc-web';
import { retryMiddleware } from 'nice-grpc-client-middleware-retry';
import { SparkServiceClient, SparkServiceDefinition } from '../../../proto/rpc/v1/service.js';
import { Lrc20ConnectionManager } from './interface.js';

// Browser-specific implementation of ConnectionManager functionality
class BrowserLrc20ConnectionManager extends Lrc20ConnectionManager {
  private lrc20Client: SparkServiceClient | undefined;

  constructor(lrc20ApiUrl: string) {
    super(lrc20ApiUrl);
  }

  private createChannel(address: string) {
    try {
      return createChannel(address);
    } catch (error) {
      console.error("Channel creation error:", error);
      throw new Error("Failed to create channel");
    }
  }

  public async createLrc20Client(): Promise<SparkServiceClient & { close?: () => void }> {
    if (this.lrc20Client) {
      return this.lrc20Client;
    }

    const channel = this.createChannel(this.lrc20ApiUrl);
    const client = this.createGrpcClient<SparkServiceClient>(SparkServiceDefinition, channel);
    this.lrc20Client = client;
    return client;
  }

  private createGrpcClient<T>(
    definition: typeof SparkServiceDefinition,
    channel: ReturnType<typeof createChannel>,
    middleware?: any,
  ): T & { close?: () => void } {
    const clientFactory = createClientFactory().use(retryMiddleware);
    if (middleware) {
      clientFactory.use(middleware);
    }

    const client = clientFactory.create(definition, channel, {
      "*": { retry: true, retryMaxAttempts: 3 }
    }) as T;
    
    // Note: gRPC-web doesn't have a close method, but we maintain the same interface
    return {
      ...client,
      close: () => {
        // No-op for browser implementation
        console.log("Note: close() is a no-op in browser environment");
      }
    };
  }
}

// Export the factory function for browser environments
export function createLrc20ConnectionManager(lrc20ApiUrl: string): Lrc20ConnectionManager {
  return new BrowserLrc20ConnectionManager(lrc20ApiUrl);
}