export interface RetryConfig {
  maxAttempts: number;
  baseDelayMs: number;
  maxDelayMs: number;
  backoffFactor: number;
}

export interface RetryContext<T, TData = unknown> {
  attempt: number;
  maxAttempts: number;
  error: Error;
  delayMs: number;
  result?: T;
  data?: TData;
}

export interface RetryCallbacks<T, TData = unknown> {
  fetchData?: (context: RetryContext<T, TData>) => Promise<TData | undefined>;
  onRetry?: (context: RetryContext<T, TData>) => Promise<void> | void;
  onError?: (
    context: RetryContext<T, TData>,
  ) => Promise<T | undefined> | T | undefined;
  onMaxAttemptsReached?: (
    context: RetryContext<T, TData>,
  ) => Promise<T | undefined> | T | undefined;
  onStart?: () => Promise<void> | void;
}

export const DEFAULT_RETRY_CONFIG: RetryConfig = {
  maxAttempts: 5,
  baseDelayMs: 1000,
  maxDelayMs: 10000,
  backoffFactor: 2,
};

function calculateBackoffDelay(attempt: number, config: RetryConfig): number {
  const delay =
    config.baseDelayMs * Math.pow(config.backoffFactor, attempt - 1);
  return Math.min(delay, config.maxDelayMs);
}

export interface WithRetryOptions<T, TData = unknown> {
  config?: RetryConfig;
  callbacks?: RetryCallbacks<T, TData>;
}

export async function withRetry<T, TData = unknown>(
  operation: (data?: TData) => Promise<T>,
  options: WithRetryOptions<T, TData> = {},
): Promise<T> {
  const config = options.config ?? DEFAULT_RETRY_CONFIG;
  const callbacks = options.callbacks ?? {};

  const { fetchData, onRetry, onError, onMaxAttemptsReached, onStart } =
    callbacks;

  if (onStart) {
    await onStart();
  }

  let currentData: TData | undefined = undefined;

  for (let attempt = 1; attempt <= config.maxAttempts; attempt++) {
    try {
      if (attempt > 1 && fetchData) {
        const context: RetryContext<T, TData> = {
          attempt,
          maxAttempts: config.maxAttempts,
          error: new Error("Placeholder"),
          delayMs: calculateBackoffDelay(attempt, config),
          data: currentData,
        };
        currentData = await fetchData(context);
      }

      return await operation(currentData);
    } catch (error) {
      const lastError =
        error instanceof Error ? error : new Error(String(error));
      const delayMs = calculateBackoffDelay(attempt, config);

      const context: RetryContext<T, TData> = {
        attempt,
        maxAttempts: config.maxAttempts,
        error: lastError,
        delayMs,
        data: currentData,
      };

      if (onError) {
        const result = await onError(context);
        if (result) {
          return result;
        }
      }

      if (attempt === config.maxAttempts) {
        if (onMaxAttemptsReached) {
          const result = await onMaxAttemptsReached(context);
          if (result) {
            return result;
          }
        }
        throw lastError;
      }

      if (onRetry) {
        await onRetry(context);
      }

      await new Promise((resolve) => setTimeout(resolve, delayMs));
    }
  }

  throw new Error("Unexpected retry loop exit");
}
