import { afterEach, describe, expect, it, jest } from "@jest/globals";
import type { SubscribeToEventsResponse } from "../proto/spark.js";
import { type ConnectionManager } from "../services/connection/connection.js";
import { SparkWallet } from "../spark-wallet/spark-wallet.js";
import {
  SPARK_WALLET_CLEANUP_DISCONNECT_REASON,
  SparkWalletEvent,
} from "../spark-wallet/types.js";

type SubscribeToEvents = (
  address: string,
  signal: AbortSignal,
) => Promise<AsyncIterable<SubscribeToEventsResponse>>;

type ConnectionManagerStub = {
  closeConnections: jest.Mock<() => Promise<void>>;
  subscribeToEvents: jest.Mock<SubscribeToEvents>;
};

type BackgroundStreamWalletInternals = {
  claimTransfers: () => Promise<string[]>;
  connectionManager: ConnectionManagerStub;
  handleStreamEvent: (data: SubscribeToEventsResponse) => Promise<void>;
  syncTokenOutputs: () => Promise<void>;
};

function backgroundStreamWalletInternals(
  wallet: SparkWallet,
): BackgroundStreamWalletInternals {
  return wallet as unknown as BackgroundStreamWalletInternals;
}

class BackgroundStreamRetryTestWallet extends SparkWallet {
  constructor(
    private readonly connectionManagerStub: ConnectionManagerStub,
    private readonly claimTransfersStub: jest.Mock<
      () => Promise<string[]>
    > = jest.fn(() => Promise.resolve([])),
  ) {
    super({
      network: "LOCAL",
    });
    this.connectionManager =
      connectionManagerStub as unknown as ConnectionManager;
    const internals = backgroundStreamWalletInternals(this);
    internals.claimTransfers = claimTransfersStub;
    internals.syncTokenOutputs = jest.fn(async () => {
      await Promise.resolve();
    });
  }

  protected override buildConnectionManager() {
    return {
      closeConnections: async () => {
        await Promise.resolve();
      },
      subscribeToEvents: async () => {
        await Promise.resolve();
        throw new Error("placeholder connection manager should be replaced");
      },
    } as unknown as ConnectionManager;
  }

  public async runBackgroundStreamForTesting() {
    await Promise.resolve();
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
    };

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
    };

    for (let i = 0; i < heartbeatCount; i += 1) {
      await waitForDelayOrAbort(signal, intervalMs);
      yield {
        event: {
          $case: "heartbeat",
          heartbeat: {},
        },
      };
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
    };

    await waitForDelayOrAbort(signal, intervalMs);
    yield {
      event: {
        $case: "heartbeat",
        heartbeat: {},
      },
    };

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
    };

    yield {
      event: {
        $case: "heartbeat",
        heartbeat: {},
      },
    };

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
    };

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
      closeConnections: jest.fn(async () => {
        await Promise.resolve();
      }),
      subscribeToEvents: jest.fn<SubscribeToEvents>(async () => {
        await Promise.resolve();
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
    void backgroundStreamPromise.then(() => {
      settled = true;
    });
    await Promise.resolve();
    expect(settled).toBe(false);

    await wallet.cleanupConnections();
    await jest.runOnlyPendingTimersAsync();
    await backgroundStreamPromise;
    expect(disconnected).toHaveBeenCalledTimes(1);
    expect(disconnected).toHaveBeenCalledWith(
      SPARK_WALLET_CLEANUP_DISCONNECT_REASON,
    );
    expect(connectionManagerStub.closeConnections).toHaveBeenCalledTimes(1);
  });

  it("unrefs the background stream retry delay when supported by the runtime", async () => {
    const unref = jest.fn();
    jest.spyOn(global, "setTimeout").mockImplementation(
      ((
        callback: TimerHandler,
        delay?: number,
      ): ReturnType<typeof setTimeout> =>
        ({
          callback,
          delay,
          unref,
        }) as unknown as ReturnType<typeof setTimeout>) as typeof setTimeout,
    );
    jest.spyOn(global, "clearTimeout").mockImplementation(() => {});

    const connectionManagerStub = {
      closeConnections: jest.fn(async () => {
        await Promise.resolve();
      }),
      subscribeToEvents: jest.fn<SubscribeToEvents>(async () => {
        await Promise.resolve();
        throw new Error("offline");
      }),
    };
    const wallet = new BackgroundStreamRetryTestWallet(connectionManagerStub);

    const backgroundStreamPromise = wallet.runBackgroundStreamForTesting();
    await flushMicrotasks();

    expect(connectionManagerStub.subscribeToEvents).toHaveBeenCalledTimes(1);
    expect(unref).toHaveBeenCalledTimes(1);

    await wallet.cleanupConnections();
    await backgroundStreamPromise;
    expect(connectionManagerStub.closeConnections).toHaveBeenCalledTimes(1);
  });

  it("unrefs the token optimization interval when supported by the runtime", async () => {
    const unref = jest.fn();
    const setIntervalSpy = jest.spyOn(global, "setInterval").mockImplementation(
      ((
        callback: TimerHandler,
        interval?: number,
      ): ReturnType<typeof setInterval> =>
        ({
          callback,
          interval,
          unref,
        }) as unknown as ReturnType<typeof setInterval>) as typeof setInterval,
    );
    jest.spyOn(global, "clearInterval").mockImplementation(() => {});

    const connectionManagerStub = {
      closeConnections: jest.fn(async () => {
        await Promise.resolve();
      }),
      subscribeToEvents: jest.fn<SubscribeToEvents>(async () => {
        await Promise.resolve();
        throw new Error("offline");
      }),
    };
    const wallet = new BackgroundStreamRetryTestWallet(connectionManagerStub);

    (
      wallet as unknown as {
        startPeriodicTokenOptimization: () => void;
      }
    ).startPeriodicTokenOptimization();

    expect(setIntervalSpy).toHaveBeenCalledWith(expect.any(Function), 300_000);
    expect(unref).toHaveBeenCalledTimes(1);

    await wallet.cleanupConnections();
    expect(connectionManagerStub.closeConnections).toHaveBeenCalledTimes(1);
  });

  it("unrefs the claim transfers interval when supported by the runtime", async () => {
    const unref = jest.fn();
    const setIntervalSpy = jest.spyOn(global, "setInterval").mockImplementation(
      ((
        callback: TimerHandler,
        interval?: number,
      ): ReturnType<typeof setInterval> =>
        ({
          callback,
          interval,
          unref,
        }) as unknown as ReturnType<typeof setInterval>) as typeof setInterval,
    );
    jest.spyOn(global, "clearInterval").mockImplementation(() => {});

    const connectionManagerStub = {
      closeConnections: jest.fn(async () => {
        await Promise.resolve();
      }),
      subscribeToEvents: jest.fn<SubscribeToEvents>(async () => {
        await Promise.resolve();
        throw new Error("offline");
      }),
    };
    const claimTransfers = jest.fn(() => Promise.resolve([]));
    const wallet = new BackgroundStreamRetryTestWallet(
      connectionManagerStub,
      claimTransfers,
    );

    await (
      wallet as unknown as {
        startPeriodicClaimTransfers: () => Promise<void>;
      }
    ).startPeriodicClaimTransfers();

    expect(claimTransfers).toHaveBeenCalledTimes(1);
    expect(setIntervalSpy).toHaveBeenCalledWith(expect.any(Function), 10_000);
    expect(unref).toHaveBeenCalledTimes(1);

    await wallet.cleanupConnections();
    expect(connectionManagerStub.closeConnections).toHaveBeenCalledTimes(1);
  });

  it("retries when the stream stops receiving heartbeats", async () => {
    jest.useFakeTimers();

    const connectionManagerStub = {
      closeConnections: jest.fn(async () => {
        await Promise.resolve();
      }),
      subscribeToEvents: jest
        .fn<SubscribeToEvents>()
        .mockImplementationOnce((_address, signal) =>
          Promise.resolve(
            createConnectedThenHeartbeatThenIdleStream(signal, 5_000),
          ),
        )
        .mockImplementationOnce((_address, signal) =>
          Promise.resolve(createConnectedThenHeartbeatStream(signal, 3, 5_000)),
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
      closeConnections: jest.fn(async () => {
        await Promise.resolve();
      }),
      subscribeToEvents: jest.fn<SubscribeToEvents>((_address, signal) =>
        Promise.resolve(createConnectedThenIdleStream(signal)),
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
      closeConnections: jest.fn(async () => {
        await Promise.resolve();
      }),
      subscribeToEvents: jest.fn<SubscribeToEvents>((_address, signal) =>
        Promise.resolve(createConnectedThenReceiverTransferStream(signal)),
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

    jest
      .spyOn(backgroundStreamWalletInternals(wallet), "handleStreamEvent")
      .mockImplementation(async (data: SubscribeToEventsResponse) => {
        if (data.event?.$case !== "receiverTransfer") {
          return;
        }

        await new Promise((resolve) => {
          setTimeout(resolve, 20_000);
        });
      });

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
    jest.spyOn(global, "clearTimeout").mockImplementation(() => {});

    const connectionManagerStub = {
      closeConnections: jest.fn(async () => {
        await Promise.resolve();
      }),
      subscribeToEvents: jest.fn<SubscribeToEvents>((_address, signal) =>
        Promise.resolve(createConnectedThenImmediateHeartbeatStream(signal)),
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
