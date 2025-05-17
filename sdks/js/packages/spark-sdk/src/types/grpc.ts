import { type CallOptions } from "nice-grpc-common";

export interface RetryOptions {
  retry?: boolean;
  retryMaxAttempts?: number;
}

export type SparkCallOptions = CallOptions & RetryOptions;
