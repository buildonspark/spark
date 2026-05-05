import { describe, expect, it, jest } from "@jest/globals";
import type { Logger } from "@lightsparkdev/core";
import { getFetch, setFetch, type SparkFetch } from "../utils/fetch.js";

function withBytes(response: Response): Response {
  (response as unknown as { bytes?: () => Promise<Uint8Array> }).bytes =
    async () => new Uint8Array(await response.arrayBuffer());
  return response;
}

type FetchFn = (input: RequestInfo | URL, init?: any) => Promise<any>;

describe("SparkFetch", () => {
  beforeEach(() => {
    jest.useFakeTimers();
  });

  afterEach(() => {
    jest.useRealTimers();
  });

  it("returns response immediately on 200", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValue(withBytes(new Response("ok", { status: 200 })));

    setFetch(mockFetch, globalThis.Headers);
    const { fetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });
    const response = await fetch("https://example.com", {
      headers: new Headers(),
    });

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("does not retry unless retry is enabled", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockRejectedValue(new Error("ECONNRESET"));

    setFetch(mockFetch, globalThis.Headers);
    const { fetch } = getFetch();

    await expect(fetch("https://example.com")).rejects.toThrow("ECONNRESET");
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("logs GET when no request method is provided", async () => {
    const logger = {
      debug: jest.fn(),
    };
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValue(withBytes(new Response("ok", { status: 200 })));

    setFetch(mockFetch, globalThis.Headers);
    const { fetch } = getFetch({
      logger: logger as unknown as Logger,
    });

    await fetch("https://example.com");

    expect(logger.debug).toHaveBeenCalledWith(
      expect.stringMatching(
        /^HTTP GET \[path https:\/\/example\.com\/\] -> start \(attempt 1\/1\)/,
      ),
    );
    expect(logger.debug).toHaveBeenCalledWith(
      expect.stringMatching(
        /^HTTP GET \[path https:\/\/example\.com\/\] -> 200 \(\+\d+ms, attempt 1\/1\)/,
      ),
    );
  });

  it("retries on 502 and succeeds", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValueOnce(
        withBytes(new Response("bad gateway", { status: 502 })),
      )
      .mockResolvedValueOnce(withBytes(new Response("ok", { status: 200 })));

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });

    const promise = retryFetch("https://example.com", {
      headers: new Headers(),
    });
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("retries on 503 and succeeds", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValueOnce(
        withBytes(new Response("service unavailable", { status: 503 })),
      )
      .mockResolvedValueOnce(withBytes(new Response("ok", { status: 200 })));

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });

    const promise = retryFetch("https://example.com", {
      headers: new Headers(),
    });
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("clones Request bodies before retrying", async () => {
    const bodies: string[] = [];
    const mockFetch = jest.fn<FetchFn>(async (input) => {
      bodies.push(await (input as Request).text());
      return withBytes(
        new Response(bodies.length === 1 ? "service unavailable" : "ok", {
          status: bodies.length === 1 ? 503 : 200,
        }),
      );
    });

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch } = getFetch({
      retry: { maxRetries: 1, baseDelayMs: 100 },
    });
    const request = new Request("https://example.com", {
      method: "POST",
      body: "payload",
    });

    const promise = retryFetch(request);
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(2);
    expect(bodies).toEqual(["payload", "payload"]);
    expect(request.bodyUsed).toBe(false);
  });

  it("retries on 504 and succeeds", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValueOnce(
        withBytes(new Response("gateway timeout", { status: 504 })),
      )
      .mockResolvedValueOnce(withBytes(new Response("ok", { status: 200 })));

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });

    const promise = retryFetch("https://example.com", {
      headers: new Headers(),
    });
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("returns 502 after max retries are exhausted", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValue(
        withBytes(new Response("bad gateway", { status: 502 })),
      );

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });

    const promise = retryFetch("https://example.com", {
      headers: new Headers(),
    });
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(502);
    expect(mockFetch).toHaveBeenCalledTimes(4);
  });

  it("does not retry on 400", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValue(
        withBytes(new Response("bad request", { status: 400 })),
      );

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });
    const response = await retryFetch("https://example.com", {
      headers: new Headers(),
    });

    expect(response.status).toBe(400);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("does not retry on 500", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValue(
        withBytes(new Response("internal error", { status: 500 })),
      );

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });
    const response = await retryFetch("https://example.com", {
      headers: new Headers(),
    });

    expect(response.status).toBe(500);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("retries multiple times before succeeding", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValueOnce(withBytes(new Response("", { status: 504 })))
      .mockResolvedValueOnce(withBytes(new Response("", { status: 503 })))
      .mockResolvedValueOnce(withBytes(new Response("", { status: 502 })))
      .mockResolvedValueOnce(withBytes(new Response("ok", { status: 200 })));

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });

    const promise = retryFetch("https://example.com", {
      headers: new Headers(),
    });
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(4);
  });

  it("retries on thrown fetch errors by default", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(withBytes(new Response("ok", { status: 200 })));

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });

    const promise = retryFetch("https://example.com", {
      headers: new Headers(),
    });
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("rethrows after max retries on network errors", async () => {
    jest.useRealTimers();
    const networkError = new Error("ECONNREFUSED");
    const mockFetch = jest.fn<FetchFn>().mockRejectedValue(networkError);

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 2, baseDelayMs: 1 },
    });

    await expect(
      retryFetch("https://example.com", {
        headers: new Headers(),
      }),
    ).rejects.toThrow("ECONNREFUSED");
    expect(mockFetch).toHaveBeenCalledTimes(3);
  });

  it("retries mixed network errors and HTTP errors", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockRejectedValueOnce(new Error("ECONNRESET"))
      .mockResolvedValueOnce(
        withBytes(new Response("service unavailable", { status: 503 })),
      )
      .mockResolvedValueOnce(withBytes(new Response("ok", { status: 200 })));

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });

    const promise = retryFetch("https://example.com", {
      headers: new Headers(),
    });
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(3);
  });

  it("does not retry on AbortError", async () => {
    const abortError = Object.assign(new Error("The operation was aborted"), {
      name: "AbortError",
    });
    const mockFetch = jest.fn<FetchFn>().mockRejectedValue(abortError);

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });

    await expect(
      retryFetch("https://example.com", {
        headers: new Headers(),
      }),
    ).rejects.toThrow("The operation was aborted");
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("does not retry AbortError even with retries remaining", async () => {
    jest.useRealTimers();
    const abortError = Object.assign(new Error("signal is aborted"), {
      name: "AbortError",
    });
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValueOnce(
        withBytes(new Response("bad gateway", { status: 502 })),
      )
      .mockRejectedValueOnce(abortError);

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 1 },
    });

    await expect(
      retryFetch("https://example.com", {
        headers: new Headers(),
      }),
    ).rejects.toThrow("signal is aborted");
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("does not retry on TimeoutError", async () => {
    const timeoutError = Object.assign(new Error("The operation timed out"), {
      name: "TimeoutError",
    });
    const mockFetch = jest.fn<FetchFn>().mockRejectedValue(timeoutError);

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch, Headers } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });

    await expect(
      retryFetch("https://example.com", {
        headers: new Headers(),
      }),
    ).rejects.toThrow("The operation timed out");
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("does not retry when Request input carries an aborted signal", async () => {
    const controller = new AbortController();
    controller.abort();
    const request = new Request("https://example.com", {
      signal: controller.signal,
    });
    const mockFetch = jest
      .fn<FetchFn>()
      .mockRejectedValue(new Error("request was canceled"));

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });

    await expect(retryFetch(request)).rejects.toThrow("request was canceled");
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("does not retry after the caller aborts during HTTP retry backoff", async () => {
    const controller = new AbortController();
    const request = new Request("https://example.com", {
      signal: controller.signal,
    });
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValueOnce(
        withBytes(new Response("bad gateway", { status: 502 })),
      )
      .mockResolvedValueOnce(withBytes(new Response("ok", { status: 200 })));

    setFetch(mockFetch, globalThis.Headers);
    const { fetch: retryFetch } = getFetch({
      retry: { maxRetries: 3, baseDelayMs: 100 },
    });

    const promise = retryFetch(request);
    await Promise.resolve();
    controller.abort(new Error("request canceled during backoff"));

    await expect(promise).rejects.toThrow("request canceled during backoff");
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });
});
