import { afterEach, describe, expect, it, jest } from "@jest/globals";
import type { SubscribeToEventsResponse } from "../proto/spark.js";
import { SparkWallet } from "../spark-wallet/spark-wallet.js";
import { SparkWalletEvent } from "../spark-wallet/types.js";

class BackgroundStreamRetryTestWallet extends SparkWallet {
  constructor(
    private readonly connectionManagerStub: {
      closeConnections: ReturnType<typeof jest.fn>;
      subscribeToEvents: ReturnType<typeof jest.fn>;
    },
    private readonly claimTransfersStub: ReturnType<typeof jest.fn> = jest.fn(
      async () => [],
    ),
  ) {
    super({
      network: "LOCAL",
    });
    this.connectionManager = connectionManagerStub as any;
    (this as any).claimTransfers = claimTransfersStub;
    (this as any).syncTokenOutputs = jest.fn(async () => {});
  }

  protected override buildConnectionManager() {
    return {
      closeConnections: async () => {},
      subscribeToEvents: async () => {
        throw new Error("placeholder connection manager should be replaced");
      },
    } as any;
  }

  public async runBackgroundStreamForTesting() {
    return this.setupBackgroundStream();
  }
}

function createConnectedThenIdleStream(signal: AbortSignal) {
  return (async function* (): AsyncGenerator<SubscribeToEventsResponse> {
    yield {
      event: {
        $case: "connected",
        connected: {},
      },
    } as SubscribeToEventsResponse;

    await waitForAbort(signal);
  })();
}

function createConnectedThenHeartbeatStream(
  signal: AbortSignal,
  heartbeatCount: number,
  intervalMs: number,
) {
  return (async function* (): AsyncGenerator<SubscribeToEventsResponse> {
    yield {
      event: {
        $case: "connected",
        connected: {},
      },
    } as SubscribeToEventsResponse;

    for (let i = 0; i < heartbeatCount; i += 1) {
      await waitForDelayOrAbort(signal, intervalMs);
      yield {
        event: {
          $case: "heartbeat",
          heartbeat: {},
        },
      } as SubscribeToEventsResponse;
    }

    await waitForAbort(signal);
  })();
}

function createConnectedThenHeartbeatThenIdleStream(
  signal: AbortSignal,
  intervalMs: number,
) {
  return (async function* (): AsyncGenerator<SubscribeToEventsResponse> {
    yield {
      event: {
        $case: "connected",
        connected: {},
      },
    } as SubscribeToEventsResponse;

    await waitForDelayOrAbort(signal, intervalMs);
    yield {
      event: {
        $case: "heartbeat",
        heartbeat: {},
      },
    } as SubscribeToEventsResponse;

    await waitForAbort(signal);
  })();
}

function createConnectedThenImmediateHeartbeatStream(signal: AbortSignal) {
  return (async function* (): AsyncGenerator<SubscribeToEventsResponse> {
    yield {
      event: {
        $case: "connected",
        connected: {},
      },
    } as SubscribeToEventsResponse;

    yield {
      event: {
        $case: "heartbeat",
        heartbeat: {},
      },
    } as SubscribeToEventsResponse;

    await waitForAbort(signal);
  })();
}

function createConnectedThenReceiverTransferStream(signal: AbortSignal) {
  return (async function* (): AsyncGenerator<SubscribeToEventsResponse> {
    yield {
      event: {
        $case: "connected",
        connected: {},
      },
    } as SubscribeToEventsResponse;

    yield {
      event: {
        $case: "receiverTransfer",
        receiverTransfer: {
          transfer: {
            id: "transfer-1",
          },
        },
      },
    } as SubscribeToEventsResponse;

    await waitForAbort(signal);
  })();
}

function waitForAbort(signal: AbortSignal) {
  return new Promise<never>((_, reject) => {
    const abort = () => {
      signal.removeEventListener("abort", abort);
      reject(new Error("request aborted"));
    };

    if (signal.aborted) {
      abort();
      return;
    }

    signal.addEventListener("abort", abort);
  });
}

function waitForDelayOrAbort(signal: AbortSignal, delayMs: number) {
  return new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", abort);
      resolve();
    }, delayMs);

    const abort = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", abort);
      reject(new Error("request aborted"));
    };

    if (signal.aborted) {
      abort();
      return;
    }

    signal.addEventListener("abort", abort);
  });
}

async function flushMicrotasks(iterations: number = 10) {
  for (let i = 0; i < iterations; i += 1) {
    await Promise.resolve();
  }
}

afterEach(() => {
  jest.restoreAllMocks();
  jest.useRealTimers();
});

describe("SparkWallet background stream reconnects", () => {
  it("keeps retrying after 10 failures and caps the backoff at 15 seconds", async () => {
    jest.useFakeTimers();

    const connectionManagerStub = {
      closeConnections: jest.fn(async () => {}),
      subscribeToEvents: jest.fn(async () => {
        throw new Error("offline");
      }),
    };
    const wallet = new BackgroundStreamRetryTestWallet(connectionManagerStub);
    const reconnectEvents: Array<{
      attempt: number;
      delayMs: number;
      error: string;
      maxAttempts: number;
    }> = [];
    const disconnected = jest.fn();

    wallet.on(
      SparkWalletEvent.StreamReconnecting,
      (attempt, maxAttempts, delayMs, error) => {
        reconnectEvents.push({
          attempt,
          delayMs,
          error,
          maxAttempts,
        });
      },
    );
    wallet.on(SparkWalletEvent.StreamDisconnected, disconnected);

    const backgroundStreamPromise = wallet.runBackgroundStreamForTesting();
    await Promise.resolve();

    await jest.advanceTimersByTimeAsync(
      1_000 + 2_000 + 4_000 + 8_000 + 15_000 * 7,
    );

    expect(reconnectEvents).toHaveLength(12);
    expect(reconnectEvents.slice(0, 6).map((event) => event.delayMs)).toEqual([
      1_000, 2_000, 4_000, 8_000, 15_000, 15_000,
    ]);
    expect(reconnectEvents[10]?.attempt).toBe(11);
    expect(reconnectEvents[11]?.attempt).toBe(12);
    expect(
      reconnectEvents.every(
        (event) => event.maxAttempts === Number.POSITIVE_INFINITY,
      ),
    ).toBe(true);
    expect(reconnectEvents.every((event) => event.error === "offline")).toBe(
      true,
    );
    expect(connectionManagerStub.subscribeToEvents).toHaveBeenCalledTimes(12);
    expect(disconnected).not.toHaveBeenCalled();

    let settled = false;
    backgroundStreamPromise.then(() => {
      settled = true;
    });
    await Promise.resolve();
    expect(settled).toBe(false);

    await wallet.cleanupConnections();
    await jest.runOnlyPendingTimersAsync();
    await backgroundStreamPromise;
    expect(disconnected).toHaveBeenCalledTimes(1);
    expect(disconnected).toHaveBeenCalledWith("Wallet cleanup requested");
    expect(connectionManagerStub.closeConnections).toHaveBeenCalledTimes(1);
  });

  it("retries when the stream stops receiving heartbeats", async () => {
    jest.useFakeTimers();

    const connectionManagerStub = {
      closeConnections: jest.fn(async () => {}),
      subscribeToEvents: jest
        .fn()
        .mockImplementationOnce(async (...args: any[]) =>
          createConnectedThenHeartbeatThenIdleStream(
            args[1] as AbortSignal,
            5_000,
          ),
        )
        .mockImplementationOnce(async (...args: any[]) =>
          createConnectedThenHeartbeatStream(args[1] as AbortSignal, 3, 5_000),
        ),
    };
    const wallet = new BackgroundStreamRetryTestWallet(connectionManagerStub);
    const reconnectEvents: Array<{
      attempt: number;
      delayMs: number;
      error: string;
    }> = [];
    const connected = jest.fn();

    wallet.on(SparkWalletEvent.StreamConnected, connected);
    wallet.on(
      SparkWalletEvent.StreamReconnecting,
      (attempt, _maxAttempts, delayMs, error) => {
        reconnectEvents.push({ attempt, delayMs, error });
      },
    );

    const backgroundStreamPromise = wallet.runBackgroundStreamForTesting();
    await flushMicrotasks();

    expect(connected).toHaveBeenCalledTimes(1);

    await jest.advanceTimersByTimeAsync(20_000);
    await flushMicrotasks();

    expect(reconnectEvents).toHaveLength(1);
    expect(reconnectEvents[0]).toEqual({
      attempt: 1,
      delayMs: 1_000,
      error: "UNAVAILABLE: stream heartbeat timed out after 15000ms",
    });

    await jest.advanceTimersByTimeAsync(1_000);
    await flushMicrotasks();

    expect(connectionManagerStub.subscribeToEvents).toHaveBeenCalledTimes(2);
    expect(connected).toHaveBeenCalledTimes(2);

    await jest.advanceTimersByTimeAsync(12_000);
    expect(reconnectEvents).toHaveLength(1);

    await wallet.cleanupConnections();
    await jest.runOnlyPendingTimersAsync();
    await backgroundStreamPromise;
    expect(connectionManagerStub.closeConnections).toHaveBeenCalledTimes(1);
  });

  it("does not enable the heartbeat listener before the stream observes a heartbeat", async () => {
    const setTimeoutSpy = jest.spyOn(global, "setTimeout");

    const connectionManagerStub = {
      closeConnections: jest.fn(async () => {}),
      subscribeToEvents: jest.fn(async (...args: any[]) =>
        createConnectedThenIdleStream(args[1] as AbortSignal),
      ),
    };
    const wallet = new BackgroundStreamRetryTestWallet(connectionManagerStub);

    const backgroundStreamPromise = wallet.runBackgroundStreamForTesting();
    await flushMicrotasks();

    expect(connectionManagerStub.subscribeToEvents).toHaveBeenCalledTimes(1);
    expect(setTimeoutSpy).not.toHaveBeenCalled();

    await wallet.cleanupConnections();
    await backgroundStreamPromise;
    expect(connectionManagerStub.closeConnections).toHaveBeenCalledTimes(1);
  });

  it("does not time out while processing a stream event", async () => {
    jest.useFakeTimers();

    const connectionManagerStub = {
      closeConnections: jest.fn(async () => {}),
      subscribeToEvents: jest.fn(async (...args: any[]) =>
        createConnectedThenReceiverTransferStream(args[1] as AbortSignal),
      ),
    };
    const wallet = new BackgroundStreamRetryTestWallet(connectionManagerStub);
    const reconnectEvents: Array<{
      attempt: number;
      delayMs: number;
      error: string;
    }> = [];

    wallet.on(
      SparkWalletEvent.StreamReconnecting,
      (attempt, _maxAttempts, delayMs, error) => {
        reconnectEvents.push({ attempt, delayMs, error });
      },
    );

    jest.spyOn(wallet as any, "handleStreamEvent").mockImplementation((async (
      ...args: any[]
    ) => {
      const [data] = args as [SubscribeToEventsResponse];
      if (data.event?.$case !== "receiverTransfer") {
        return;
      }

      await new Promise((resolve) => {
        setTimeout(resolve, 20_000);
      });
    }) as any);

    const backgroundStreamPromise = wallet.runBackgroundStreamForTesting();
    await flushMicrotasks();

    await jest.advanceTimersByTimeAsync(20_000);
    await flushMicrotasks();

    expect(reconnectEvents).toHaveLength(0);
    expect(connectionManagerStub.subscribeToEvents).toHaveBeenCalledTimes(1);

    await wallet.cleanupConnections();
    await jest.runOnlyPendingTimersAsync();
    await backgroundStreamPromise;
    expect(connectionManagerStub.closeConnections).toHaveBeenCalledTimes(1);
  });

  it("unrefs the stream activity timeout when supported by the runtime", async () => {
    const unref = jest.fn();
    const setTimeoutSpy = jest.spyOn(global, "setTimeout").mockImplementation(
      ((callback: TimerHandler): ReturnType<typeof setTimeout> =>
        ({
          callback,
          unref,
        }) as unknown as ReturnType<typeof setTimeout>) as typeof setTimeout,
    );
    jest
      .spyOn(global, "clearTimeout")
      .mockImplementation((() => {}) as typeof clearTimeout);

    const connectionManagerStub = {
      closeConnections: jest.fn(async () => {}),
      subscribeToEvents: jest.fn(async (...args: any[]) =>
        createConnectedThenImmediateHeartbeatStream(args[1] as AbortSignal),
      ),
    };
    const wallet = new BackgroundStreamRetryTestWallet(connectionManagerStub);

    const backgroundStreamPromise = wallet.runBackgroundStreamForTesting();
    await flushMicrotasks();

    expect(setTimeoutSpy).toHaveBeenCalled();
    expect(unref).toHaveBeenCalledTimes(setTimeoutSpy.mock.calls.length);

    await wallet.cleanupConnections();
    await backgroundStreamPromise;
    expect(connectionManagerStub.closeConnections).toHaveBeenCalledTimes(1);
  });
});
