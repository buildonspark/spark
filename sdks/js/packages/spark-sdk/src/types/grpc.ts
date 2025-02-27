import { CallOptions } from 'nice-grpc';

export interface RetryOptions {
  retry?: boolean;
  retryMaxAttempts?: number;
}

export type SparkCallOptions = CallOptions & RetryOptions; 