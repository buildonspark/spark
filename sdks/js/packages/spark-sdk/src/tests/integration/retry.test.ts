import { describe, expect, it, jest } from "@jest/globals";
import { DEFAULT_RETRY_CONFIG, withRetry } from "../../utils/retry.js";

describe("Retry Test", () => {
  beforeEach(() => {
    jest.useFakeTimers();
  });

  afterEach(() => {
    jest.useRealTimers();
  });

  it("should succeed on first attempt", async () => {
    const operation = jest
      .fn<() => Promise<string>>()
      .mockResolvedValue("success");
    const onRetry = jest.fn<() => Promise<void>>();
    const onError = jest
      .fn<() => string | undefined>()
      .mockReturnValue(undefined);

    const result = await withRetry<string>(operation, {
      callbacks: {
        onRetry,
        onError,
      },
    });

    expect(result).toBe("success");
    expect(operation).toHaveBeenCalledTimes(1);
    expect(onRetry).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
  });

  it("should retry on failure and then succeed", async () => {
    const operation = jest
      .fn<() => Promise<string>>()
      .mockRejectedValueOnce(new Error("Network error"))
      .mockRejectedValueOnce(new Error("Network error"))
      .mockResolvedValue("success");

    const onRetry = jest.fn<() => Promise<void>>();
    const onError = jest
      .fn<() => string | undefined>()
      .mockReturnValue(undefined);

    const promise = withRetry<string>(operation, {
      callbacks: {
        onRetry,
        onError,
      },
    });

    await jest.runAllTimersAsync();

    const result = await promise;

    expect(result).toBe("success");
    expect(operation).toHaveBeenCalledTimes(3);
    expect(onRetry).toHaveBeenCalledTimes(2);
    expect(onError).toHaveBeenCalledTimes(2);
  }, 10000);

  it("should fail after max attempts", async () => {
    let operation = jest.fn<() => Promise<string>>();

    for (let i = 0; i < DEFAULT_RETRY_CONFIG.maxAttempts; i++) {
      operation = operation.mockRejectedValueOnce(new Error("Network error"));
    }

    const promise = withRetry<string>(operation);
    const rejection = expect(promise).rejects.toThrow("Network error");

    await jest.runAllTimersAsync();

    await rejection;
    expect(operation).toHaveBeenCalledTimes(DEFAULT_RETRY_CONFIG.maxAttempts);
  });
});
