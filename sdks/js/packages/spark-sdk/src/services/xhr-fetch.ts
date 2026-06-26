const DEFAULT_TIMEOUT_MS = 30_000;

export interface XHRFetchConfig {
  timeoutMs?: number;
}

/**
 * A `fetch`-compatible implementation backed by `XMLHttpRequest` with
 * `responseType = "arraybuffer"`.
 *
 * React Native's `whatwg-fetch` polyfill backs binary response bodies with the
 * native `BlobModule`, which holds bytes in a registry keyed by UUID. Under
 * memory pressure that registry can release a blob before the body is read,
 * producing `Unable to resolve data for blob: <uuid>`. Reading the response as
 * an ArrayBuffer and wrapping it directly in a `Response` keeps the bytes in JS
 * and never creates a blob-registry entry to lose, while preserving the original
 * bytes for `.json()`, `.text()`, and `.arrayBuffer()` callers alike.
 *
 * This is the GraphQL/SSP analogue of the existing `XHRTransport`, which also
 * forces `responseType = "arraybuffer"` for gRPC traffic for the same reason.
 * It is installed as the React Native global `fetch` via `setFetch`, so it
 * behaves as a faithful `fetch`: it honors `Request` inputs and preserves
 * binary response bodies. It also applies a request timeout so a lost response
 * rejects instead of hanging.
 */
export function createXHRFetch(
  config?: XHRFetchConfig,
): typeof globalThis.fetch {
  const timeoutMs = config?.timeoutMs ?? DEFAULT_TIMEOUT_MS;

  return async (
    input: RequestInfo | URL,
    init?: RequestInit,
  ): Promise<Response> => {
    const request =
      typeof Request !== "undefined" && input instanceof Request
        ? input
        : undefined;
    const url = resolveUrl(input);
    const method = (init?.method ?? request?.method ?? "GET").toUpperCase();
    const signal = init?.signal ?? request?.signal;
    const headers = new Headers(init?.headers ?? request?.headers);
    const credentials = init?.credentials ?? request?.credentials;

    // XHR cannot consume a streaming body, so resolve it to a value it accepts.
    // A body provided via `init` is forwarded as-is — the SSP path sends a
    // `Uint8Array` (an ArrayBufferView), which XHR transmits as raw bytes
    // without touching BlobModule. A body carried on a `Request` argument is
    // read out as text.
    let body: XMLHttpRequestBodyInit | null = null;
    if (init?.body != null) {
      body = init.body as XMLHttpRequestBodyInit;
    } else if (request && method !== "GET" && method !== "HEAD") {
      body = await request.text();
    }

    return new Promise<Response>((resolve, reject) => {
      if (signal?.aborted) {
        reject(makeAbortError(signal));
        return;
      }

      const xhr = new XMLHttpRequest();
      xhr.open(method, url, true);
      xhr.responseType = "arraybuffer";
      xhr.timeout = timeoutMs;
      // Match fetch credentials semantics. Only an explicit mode overrides the
      // platform default: this is installed as the global `fetch`, and React
      // Native's XMLHttpRequest defaults `withCredentials` to true (cookies are
      // sent), so leaving the unspecified/"same-origin" case untouched avoids
      // changing the host app's default cookie behavior.
      if (credentials === "include") {
        xhr.withCredentials = true;
      } else if (credentials === "omit") {
        xhr.withCredentials = false;
      }

      headers.forEach((value, key) => {
        xhr.setRequestHeader(key, value);
      });

      const onAbort = () => xhr.abort();
      signal?.addEventListener("abort", onAbort, { once: true });
      const cleanup = () => signal?.removeEventListener("abort", onAbort);

      xhr.onload = () => {
        cleanup();
        // status 0 means the request never completed at the HTTP layer (network
        // error / local abort); surface it as a retryable network error rather
        // than fabricating a 200.
        if (xhr.status === 0) {
          reject(new Error(`Network request to ${url} failed`));
          return;
        }
        const buffer = xhr.response as ArrayBuffer | null;
        const responseBody = buffer && buffer.byteLength > 0 ? buffer : null;
        const response = new Response(responseBody, {
          status: xhr.status,
          statusText: xhr.statusText,
          headers: parseHeaders(xhr.getAllResponseHeaders()),
        });
        // React Native's whatwg-fetch decodes ArrayBuffer-backed bodies as
        // Latin-1 (byte-per-char), not UTF-8, which corrupts non-ASCII text.
        // Decode the raw bytes with TextDecoder for text()/json() while leaving
        // arrayBuffer()/blob() to return the original bytes unchanged.
        const decodeText = () =>
          Promise.resolve(buffer ? new TextDecoder().decode(buffer) : "");
        Object.defineProperties(response, {
          text: { value: decodeText },
          json: {
            value: () => decodeText().then((t) => JSON.parse(t) as unknown),
          },
        });
        resolve(response);
      };

      // No status or body is available on a transport-level failure; reject so
      // the caller's retry can re-issue. GraphQL reads are idempotent.
      xhr.onerror = () => {
        cleanup();
        reject(new Error(`Network request to ${url} failed`));
      };

      xhr.ontimeout = () => {
        cleanup();
        const err = new Error(
          `Request to ${url} timed out after ${timeoutMs}ms`,
        );
        err.name = "TimeoutError";
        reject(err);
      };

      xhr.onabort = () => {
        cleanup();
        reject(makeAbortError(signal));
      };

      try {
        if (body != null) {
          xhr.send(body);
        } else {
          xhr.send();
        }
      } catch (e) {
        cleanup();
        reject(new Error(`Failed to send request to ${url}: ${formatErr(e)}`));
      }
    });
  };
}

function resolveUrl(input: RequestInfo | URL): string {
  if (typeof input === "string") return input;
  if (input instanceof URL) return input.toString();
  return input.url;
}

function parseHeaders(raw: string): Headers {
  const headers = new Headers();
  for (const line of raw.trim().split(/[\r\n]+/)) {
    if (!line) continue;
    const idx = line.indexOf(":");
    if (idx === -1) continue;
    headers.set(line.slice(0, idx).trim(), line.slice(idx + 1).trim());
  }
  return headers;
}

function makeAbortError(signal?: AbortSignal): Error {
  const reason: unknown = signal?.reason;
  if (reason instanceof Error) return reason;
  const err = new Error(
    typeof reason === "string" && reason.length > 0
      ? reason
      : "The operation was aborted",
  );
  err.name = "AbortError";
  return err;
}

function formatErr(e: unknown): string {
  return e instanceof Error ? e.message : "unknown error";
}
