// Only define the interface/types here
import type { SparkServiceClient } from '../../../proto/rpc/v1/service.js';

// Define the interface for type checking
export interface ILrc20ConnectionManager {
  lrc20ApiUrl: string;
  createLrc20Client(): Promise<SparkServiceClient & { close?: () => void }>;
}

// Create a base class that implements the interface
export abstract class Lrc20ConnectionManager implements ILrc20ConnectionManager {
  constructor(public lrc20ApiUrl: string) {}

  abstract createLrc20Client(): Promise<SparkServiceClient & { close?: () => void }>;
}

// Export the client type for convenience
export type Lrc20SparkClient = SparkServiceClient & { close?: () => void };

// Export a factory function that will be implemented differently in each environment
export declare function createLrc20ConnectionManager(lrc20ApiUrl: string): Lrc20ConnectionManager;