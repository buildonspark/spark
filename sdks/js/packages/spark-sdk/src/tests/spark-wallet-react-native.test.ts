import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  jest,
} from "@jest/globals";
import type { SubscribeToEventsResponse } from "../proto/spark.js";
import type { ConnectionManager } from "../services/connection/connection.js";

type NativeModulesMock = {
  SparkGrpcModule?: Record<string, unknown>;
};

const nativeModules: NativeModulesMock = {};

jest.unstable_mockModule("react-native", () => ({
  NativeModules: nativeModules,
}));

const { SparkWalletReactNative } =
  await import("../spark-wallet/spark-wallet.react-native.js");

type SparkWalletReactNativeInstance = InstanceType<
  typeof SparkWalletReactNative
>;

type SubscribeToEvents = (
  address: string,
  signal: AbortSignal,
) => Promise<AsyncIterable<SubscribeToEventsResponse>>;

type ConnectionManagerStub = {
  createClients: jest.Mock<() => Promise<void>>;
  closeConnections: jest.Mock<() => Promise<void>>;
  subscribeToEvents: jest.Mock<SubscribeToEvents>;
  getSessionId: jest.Mock<() => string>;
};

type ReactNativeWalletInternals = {
  claimTransfers: () => Promise<string[]>;
  createClientsAndSyncWallet: () => Promise<void>;
  syncTokenOutputs: () => Promise<void>;
  syncWallet: () => Promise<void>;
};

function reactNativeWalletInternals(
  wallet: SparkWalletReactNativeInstance,
): ReactNativeWalletInternals {
  return wallet as unknown as ReactNativeWalletInternals;
}

class ReactNativeStreamInitTestWallet extends SparkWalletReactNative {
  constructor(private readonly connectionManagerStub: ConnectionManagerStub) {
    super({
      network: "LOCAL",
    });
    this.connectionManager =
      connectionManagerStub as unknown as ConnectionManager;
    const internals = reactNativeWalletInternals(this);
    internals.claimTransfers = jest.fn(() => Promise.resolve([]));
    internals.syncTokenOutputs = jest.fn(async () => {
      await Promise.resolve();
    });
    internals.syncWallet = jest.fn(async () => {
      await Promise.resolve();
    });
  }

  protected override buildConnectionManager() {
    return {
      createClients: async () => {
        await Promise.resolve();
      },
      closeConnections: async () => {
        await Promise.resolve();
      },
      subscribeToEvents: () =>
        Promise.resolve(createOpenStream(new AbortController().signal)),
      getSessionId: () => "placeholder-session",
    } as unknown as ConnectionManager;
  }

  public async initializeSignerForTest() {
    await this.config.signer.createSparkWalletFromSeed(
      new Uint8Array(32).fill(1),
      0,
    );
  }

  public async createClientsAndSyncWalletForTest() {
    await reactNativeWalletInternals(this).createClientsAndSyncWallet();
  }
}

function createConnectionManagerStub(): ConnectionManagerStub {
  return {
    createClients: jest.fn(async () => {
      await Promise.resolve();
    }),
    closeConnections: jest.fn(async () => {
      await Promise.resolve();
    }),
    subscribeToEvents: jest.fn((_address, signal) =>
      Promise.resolve(createOpenStream(signal)),
    ),
    getSessionId: jest.fn(() => "test-session"),
  };
}

async function* createOpenStream(
  signal: AbortSignal,
): AsyncGenerator<SubscribeToEventsResponse> {
  yield {
    event: {
      $case: "connected",
      connected: {},
    },
  };
  await waitForAbort(signal);
}

async function waitForAbort(signal: AbortSignal): Promise<void> {
  if (signal.aborted) {
    return;
  }

  await new Promise<void>((resolve) => {
    signal.addEventListener("abort", () => resolve(), { once: true });
  });
}

describe("SparkWalletReactNative background stream setup", () => {
  beforeEach(() => {
    delete nativeModules.SparkGrpcModule;
  });

  afterEach(() => {
    delete nativeModules.SparkGrpcModule;
  });

  it("skips the background stream when the native gRPC module is unavailable", async () => {
    const connectionManager = createConnectionManagerStub();
    const wallet = new ReactNativeStreamInitTestWallet(connectionManager);
    await wallet.initializeSignerForTest();

    await wallet.createClientsAndSyncWalletForTest();

    expect(connectionManager.createClients).toHaveBeenCalledTimes(1);
    expect(connectionManager.subscribeToEvents).not.toHaveBeenCalled();
  });

  it("starts the background stream when the native gRPC module is available", async () => {
    nativeModules.SparkGrpcModule = {};
    const connectionManager = createConnectionManagerStub();
    const wallet = new ReactNativeStreamInitTestWallet(connectionManager);

    try {
      await wallet.initializeSignerForTest();
      await wallet.createClientsAndSyncWalletForTest();

      expect(connectionManager.createClients).toHaveBeenCalledTimes(1);
      expect(connectionManager.subscribeToEvents).toHaveBeenCalledTimes(1);
    } finally {
      await wallet.cleanup();
    }
  });
});
