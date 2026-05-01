/* Essentially copied from bare-fetch version to meet requirements of both interfaces.
   The bare-fetch version is more minimal than standard Headers class: */
import type { Logger } from "@lightsparkdev/core";
import { formatUrlForLogs } from "./logging.js";

interface SparkFetchHeaders extends Iterable<[name: string, value: string]> {
  append(name: string, value: string): void;
  delete(name: string): void;
  get(name: string): string | null;
  has(name: string): boolean;
  set(name: string, value: string): void;
}

export type SparkHeadersConstructor = new (
  init?: Record<string, string> | undefined,
) => SparkFetchHeaders;

export type SparkFetchRetryOptions = {
  maxRetries?: number;
  baseDelayMs?: number;
  maxDelayMs?: number;
  retryableStatusCodes?: readonly number[];
  retryOnNetworkError?: boolean;
};

export type SparkFetchOptions = {
  logger?: Logger;
  retry?: boolean | SparkFetchRetryOptions;
};

type SparkFetchRequestInit = Omit<RequestInit, "headers"> & {
  headers?: SparkFetchHeaders;
  sparkOptions?: SparkFetchOptions;
};

type SparkFetchResponse = {
  readonly body: ReadableStream<Uint8Array> | null;
  readonly bodyUsed: boolean;

  readonly ok: boolean;
  readonly redirected: boolean;
  readonly status: number;
  readonly statusText: string;
  readonly url: string | null;

  headers: SparkFetchHeaders;
  json: () => Promise<any>;
  text: () => Promise<string>;
  arrayBuffer: () => Promise<ArrayBuffer>;
  bytes: () => Promise<Uint8Array>;
};

export type SparkFetch = (
  input: RequestInfo | URL,
  init?: SparkFetchRequestInit,
) => Promise<SparkFetchResponse>;

type ResolvedRetryOptions = {
  maxRetries: number;
  baseDelayMs: number;
  maxDelayMs: number;
  retryableStatusCodes: Set<number>;
  retryOnNetworkError: boolean;
};

const DEFAULT_RETRYABLE_STATUS_CODES = [502, 503, 504] as const;
const DEFAULT_MAX_RETRIES = 5;
const DEFAULT_BASE_DELAY_MS = 1000;
const DEFAULT_MAX_DELAY_MS = 10000;

function resolveRetryOptions(
  retry: boolean | SparkFetchRetryOptions | undefined,
) {
  let result: ResolvedRetryOptions = {
    maxRetries: 0,
    baseDelayMs: 0,
    maxDelayMs: 0,
    retryableStatusCodes: new Set(),
    retryOnNetworkError: false,
  };
  if (!retry) {
    return result;
  }

  const options = retry === true ? {} : retry;
  const maxRetries = Math.max(0, options.maxRetries ?? DEFAULT_MAX_RETRIES);
  const baseDelayMs = Math.max(0, options.baseDelayMs ?? DEFAULT_BASE_DELAY_MS);
  const maxDelayMs = Math.max(
    baseDelayMs,
    options.maxDelayMs ?? DEFAULT_MAX_DELAY_MS,
  );
  const retryableStatusCodes = new Set<number>(
    options.retryableStatusCodes ?? DEFAULT_RETRYABLE_STATUS_CODES,
  );
  const retryOnNetworkError = options.retryOnNetworkError ?? true;

  result = {
    maxRetries,
    baseDelayMs,
    maxDelayMs,
    retryableStatusCodes,
    retryOnNetworkError,
  };
  return result;
}

function getRetryDelayMs(
  options: ResolvedRetryOptions,
  attempt: number,
): number {
  const delay = options.baseDelayMs * Math.pow(2, attempt);
  return Math.min(delay, options.maxDelayMs);
}

let fetchImpl: SparkFetch | null =
  typeof window !== "undefined" && window.fetch
    ? (window.fetch.bind(window) as SparkFetch)
    : globalThis.fetch
      ? (globalThis.fetch.bind(globalThis) as SparkFetch)
      : null;
let Headers: SparkHeadersConstructor | null = globalThis.Headers ?? null;

export const getFetch = (options?: SparkFetchOptions) => {
  if (!fetchImpl) {
    throw new Error(
      "Fetch implementation is not set. Please set it using setFetch().",
    );
  }

  if (!Headers) {
    throw new Error(
      "Headers implementation is not set. Please set it using setFetch().",
    );
  }

  return {
    fetch: createSparkFetch(fetchImpl, options),
    Headers,
  };
};

function createSparkFetch(
  baseFetch: SparkFetch,
  defaults?: SparkFetchOptions,
): SparkFetch {
  return (async (input: RequestInfo | URL, init?: SparkFetchRequestInit) => {
    const { sparkOptions, ...restInit } = init ?? {};
    const fetchInit =
      init && sparkOptions !== undefined
        ? (restInit as SparkFetchRequestInit)
        : init;

    const logger = sparkOptions?.logger ?? defaults?.logger;
    const retryOptions = resolveRetryOptions(
      sparkOptions?.retry ?? defaults?.retry ?? false,
    );

    if (!logger && !retryOptions.maxRetries) {
      return baseFetch(input, fetchInit);
    }

    const method = (
      fetchInit?.method ??
      (isRequest(input) ? input.method : undefined) ??
      "GET"
    ).toUpperCase();
    const signal =
      fetchInit?.signal ?? (isRequest(input) ? input.signal : null);
    const url = resolveUrl(input);
    const safeUrl = formatUrlForLogs(url);
    const maxRetries = retryOptions.maxRetries;

    for (let attempt = 0; attempt <= maxRetries; attempt++) {
      const startTime = Date.now();
      logger?.debug?.(
        `HTTP ${method} ${safeUrl} -> start (attempt ${attempt + 1}/${
          maxRetries + 1
        })`,
      );

      try {
        const response = await baseFetch(cloneRequestInput(input), fetchInit);
        const durationMs = Date.now() - startTime;
        logger?.debug?.(
          `HTTP ${method} ${safeUrl} -> ${response.status} (+${durationMs}ms, attempt ${
            attempt + 1
          }/${maxRetries + 1})`,
        );

        if (
          retryOptions.retryableStatusCodes.has(response.status) &&
          attempt < maxRetries
        ) {
          const delay = getRetryDelayMs(retryOptions, attempt);
          logger?.debug?.(
            `HTTP ${method} ${safeUrl} -> ${response.status} (attempt ${
              attempt + 1
            }/${maxRetries + 1}), retrying in ${delay}ms`,
          );
          await waitForRetryDelay(delay, signal);
          continue;
        }

        return response;
      } catch (error) {
        const durationMs = Date.now() - startTime;
        const message =
          error instanceof Error ? error.message : String(error ?? "unknown");
        logger?.debug?.(
          `HTTP ${method} ${safeUrl} -> error (+${durationMs}ms, attempt ${
            attempt + 1
          }/${maxRetries + 1}): ${message}`,
        );

        if (shouldRethrowFetchError(error, signal)) {
          throw error;
        }

        if (!retryOptions.retryOnNetworkError || attempt >= maxRetries) {
          throw error;
        }

        const delay = getRetryDelayMs(retryOptions, attempt);
        logger?.debug?.(
          `HTTP ${method} ${safeUrl} -> error (attempt ${attempt + 1}/${
            maxRetries + 1
          }), retrying in ${delay}ms: ${message}`,
        );
        await waitForRetryDelay(delay, signal);
      }
    }

    throw new Error("Retry loop exited unexpectedly");
  }) as SparkFetch;
}

function shouldRethrowFetchError(
  error: unknown,
  signal: AbortSignal | null | undefined,
): boolean {
  if (signal?.aborted) {
    return true;
  }

  return (
    error instanceof Error &&
    (error.name === "AbortError" || error.name === "TimeoutError")
  );
}

function waitForRetryDelay(
  delayMs: number,
  signal: AbortSignal | null | undefined,
): Promise<void> {
  if (!signal) {
    return new Promise((resolve) => setTimeout(resolve, delayMs));
  }

  if (signal.aborted) {
    return Promise.reject(getAbortError(signal));
  }

  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, delayMs);

    const onAbort = () => {
      clearTimeout(timeout);
      signal.removeEventListener("abort", onAbort);
      reject(getAbortError(signal));
    };

    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function getAbortError(signal: AbortSignal): Error {
  if (signal.reason instanceof Error) {
    return signal.reason;
  }

  const message =
    typeof signal.reason === "string" && signal.reason.length > 0
      ? signal.reason
      : "The operation was aborted";
  const error = new Error(message);
  error.name = "AbortError";
  return error;
}

function isRequest(input: RequestInfo | URL): input is Request {
  return typeof Request !== "undefined" && input instanceof Request;
}

function cloneRequestInput(input: RequestInfo | URL): RequestInfo | URL {
  if (!isRequest(input)) {
    return input;
  }

  // Request bodies are single-use; retries need a fresh clone for each attempt.
  return input.clone();
}

function resolveUrl(input: RequestInfo | URL): string {
  if (typeof input === "string") {
    return input;
  }
  if (input instanceof URL) {
    return input.toString();
  }
  if (isRequest(input)) {
    return input.url;
  }
  if (typeof input === "object" && input && "url" in input) {
    try {
      return String((input as { url?: unknown }).url ?? "");
    } catch {
      return "";
    }
  }
  return "";
}

export const setFetch = (
  fetchImplParam: SparkFetch | null,
  headersParam: SparkHeadersConstructor | null,
): void => {
  fetchImpl = fetchImplParam;
  Headers = headersParam;
  globalThis.fetch = fetchImpl as typeof globalThis.fetch;
  globalThis.Headers = Headers as typeof globalThis.Headers;
};
