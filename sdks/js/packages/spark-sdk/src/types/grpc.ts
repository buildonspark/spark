import type { CallOptions, ClientError, Status } from "nice-grpc-common";

export interface RetryOptions {
  retry?: boolean;
  retryMaxAttempts?: number;
  retryBaseDelayMs?: number;
  retryMaxDelayMs?: number;
  retryableStatuses?: Array<Status | keyof typeof Status>;
  onRetryableError?: (
    error: ClientError,
    attempt: number,
    delayMs: number,
  ) => void;
}

export type SparkCallOptions = CallOptions & RetryOptions;
