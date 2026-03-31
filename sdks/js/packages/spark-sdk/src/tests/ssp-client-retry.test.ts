import { describe, expect, it, jest } from "@jest/globals";
import { createRetryFetch } from "../graphql/client.js";

type FetchFn = typeof globalThis.fetch;

describe("createRetryFetch", () => {
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

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 100);
    const response = await retryFetch("https://example.com", {});

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("should retry on 502 and succeed", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValueOnce(new Response("bad gateway", { status: 502 }))
      .mockResolvedValueOnce(new Response("ok", { status: 200 }));

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 100);

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

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 100);

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

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 100);

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

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 100);

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

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 100);
    const response = await retryFetch("https://example.com", {});

    expect(response.status).toBe(400);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("should not retry on 500", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValue(new Response("internal error", { status: 500 }));

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 100);
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

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 100);

    const promise = retryFetch("https://example.com", {});
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(4);
  });

  // --- Network error (thrown fetch) tests ---

  it("should retry on thrown network error and succeed", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockRejectedValueOnce(new Error("ECONNRESET"))
      .mockResolvedValueOnce(new Response("ok", { status: 200 }));

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 100);

    const promise = retryFetch("https://example.com", {});
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("should rethrow after max retries on network errors", async () => {
    jest.useRealTimers();
    const networkError = new Error("ECONNREFUSED");
    const mockFetch = jest.fn<FetchFn>().mockRejectedValue(networkError);

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 2, 1);

    await expect(retryFetch("https://example.com", {})).rejects.toThrow(
      "ECONNREFUSED",
    );
    expect(mockFetch).toHaveBeenCalledTimes(3); // 1 initial + 2 retries
  });

  it("should retry mixed network errors and HTTP errors", async () => {
    const mockFetch = jest
      .fn<FetchFn>()
      .mockRejectedValueOnce(new Error("ECONNRESET"))
      .mockResolvedValueOnce(new Response("", { status: 503 }))
      .mockResolvedValueOnce(new Response("ok", { status: 200 }));

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 100);

    const promise = retryFetch("https://example.com", {});
    await jest.runAllTimersAsync();
    const response = await promise;

    expect(response.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(3);
  });

  // --- AbortError / TimeoutError tests ---

  it("should not retry on AbortError", async () => {
    const abortError = Object.assign(new Error("The operation was aborted"), {
      name: "AbortError",
    });
    const mockFetch = jest.fn<FetchFn>().mockRejectedValue(abortError);

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 100);

    await expect(retryFetch("https://example.com", {})).rejects.toThrow(
      "The operation was aborted",
    );
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("should not retry AbortError even with retries remaining", async () => {
    jest.useRealTimers();
    const abortError = Object.assign(new Error("signal is aborted"), {
      name: "AbortError",
    });
    const mockFetch = jest
      .fn<FetchFn>()
      .mockResolvedValueOnce(new Response("", { status: 502 }))
      .mockRejectedValueOnce(abortError);

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 1);

    await expect(retryFetch("https://example.com", {})).rejects.toThrow(
      "signal is aborted",
    );
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("should not retry on TimeoutError", async () => {
    const timeoutError = Object.assign(new Error("The operation timed out"), {
      name: "TimeoutError",
    });
    const mockFetch = jest.fn<FetchFn>().mockRejectedValue(timeoutError);

    const retryFetch = createRetryFetch(mockFetch as FetchFn, 3, 100);

    await expect(retryFetch("https://example.com", {})).rejects.toThrow(
      "The operation timed out",
    );
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });
});
