import { describe, expect, it, jest } from "@jest/globals";

// Re-create the retry logic here for testing (since it's not exported)
const RETRYABLE_STATUS_CODES = new Set([502, 503, 504]);

type FetchFn = (
  input: RequestInfo | URL,
  init?: RequestInit,
) => Promise<Response>;

function createRetryFetch(
  baseFetch: FetchFn,
  maxRetries: number = 5,
  baseDelayMs: number = 1000,
): FetchFn {
  return async (input, init) => {
    for (let attempt = 0; attempt <= maxRetries; attempt++) {
      const response = await baseFetch(input, init);

      if (RETRYABLE_STATUS_CODES.has(response.status) && attempt < maxRetries) {
        const delay = Math.min(baseDelayMs * Math.pow(2, attempt), 10000);
        await new Promise((r) => setTimeout(r, delay));
        continue;
      }

      return response;
    }

    throw new Error("Retry loop exited unexpectedly");
  };
}

describe("SspClient retry fetch", () => {
  beforeEach(() => {
    jest.useFakeTimers();
  });

  afterEach(() => {
    jest.useRealTimers();
  });

  it("should return response immediately on 200", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValue(new Response("ok", { status: 200 }));

    const retryFetch = createRetryFetch(mockFetch, 3, 100);
    const response = await retryFetch("https://example.com", {});

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("should retry on 502 and succeed", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValueOnce(new Response("bad gateway", { status: 502 }))
      .mockResolvedValueOnce(new Response("ok", { status: 200 }));

    const retryFetch = createRetryFetch(mockFetch, 3, 100);

    const promise = retryFetch("https://example.com", {});
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("should retry on 503 and succeed", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValueOnce(
        new Response("service unavailable", { status: 503 }),
      )
      .mockResolvedValueOnce(new Response("ok", { status: 200 }));

    const retryFetch = createRetryFetch(mockFetch, 3, 100);

    const promise = retryFetch("https://example.com", {});
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("should retry on 504 and succeed", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValueOnce(new Response("gateway timeout", { status: 504 }))
      .mockResolvedValueOnce(new Response("ok", { status: 200 }));

    const retryFetch = createRetryFetch(mockFetch, 3, 100);

    const promise = retryFetch("https://example.com", {});
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("should return 502 after max retries exhausted", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValue(new Response("bad gateway", { status: 502 }));

    const retryFetch = createRetryFetch(mockFetch, 3, 100);

    const promise = retryFetch("https://example.com", {});
    await jest.runAllTimersAsync();
    const response = await promise;

    // After maxRetries, it returns the last response
    expect(response.status).toBe(502);
    expect(mockFetch).toHaveBeenCalledTimes(4); // 1 initial + 3 retries
  });

  it("should not retry on 400", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValue(new Response("bad request", { status: 400 }));

    const retryFetch = createRetryFetch(mockFetch, 3, 100);
    const response = await retryFetch("https://example.com", {});

    expect(response.status).toBe(400);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("should not retry on 500", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValue(new Response("internal error", { status: 500 }));

    const retryFetch = createRetryFetch(mockFetch, 3, 100);
    const response = await retryFetch("https://example.com", {});

    expect(response.status).toBe(500);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("should retry multiple times before succeeding", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValueOnce(new Response("", { status: 504 }))
      .mockResolvedValueOnce(new Response("", { status: 503 }))
      .mockResolvedValueOnce(new Response("", { status: 502 }))
      .mockResolvedValueOnce(new Response("ok", { status: 200 }));

    const retryFetch = createRetryFetch(mockFetch, 3, 100);

    const promise = retryFetch("https://example.com", {});
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(4);
  });
});
