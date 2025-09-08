/* Essentially copied from bare-fetch version to meet requirements of both interfaces.
   The bare-fetch version is more minimal than standard Headers class: */
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

type SparkFetchRequestInit = Omit<RequestInit, "headers"> & {
  headers?: SparkFetchHeaders;
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

/* Minimal API supporting Bare interface and standard fetch */
export type SparkFetch = (
  input: RequestInfo | URL,
  init?: SparkFetchRequestInit,
) => Promise<SparkFetchResponse>;

let fetchImpl: SparkFetch | null =
  typeof window !== "undefined" && window.fetch
    ? (window.fetch.bind(window) as SparkFetch)
    : globalThis.fetch
      ? (globalThis.fetch.bind(globalThis) as SparkFetch)
      : null;
let Headers: SparkHeadersConstructor | null = globalThis.Headers ?? null;

export const getFetch = () => {
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

  const val = {
    fetch: fetchImpl,
    Headers,
  };

  return val;
};

export const setFetch = (
  fetchImplParam: SparkFetch | null,
  headersParam: SparkHeadersConstructor | null,
): void => {
  fetchImpl = fetchImplParam;
  Headers = headersParam;
  globalThis.fetch = fetchImpl as typeof globalThis.fetch;
  globalThis.Headers = Headers as typeof globalThis.Headers;
};
