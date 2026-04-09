import { afterEach, describe, expect, it, jest } from "@jest/globals";
import { SparkWallet } from "../spark-wallet/spark-wallet.js";
import { SparkWalletEvent } from "../spark-wallet/types.js";

class BackgroundStreamRetryTestWallet extends SparkWallet {
  constructor(
    private readonly connectionManagerStub: {
      closeConnections: ReturnType<typeof jest.fn>;
      subscribeToEvents: ReturnType<typeof jest.fn>;
    },
  ) {
    super({
      network: "LOCAL",
    });
    this.connectionManager = connectionManagerStub as any;
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
});
