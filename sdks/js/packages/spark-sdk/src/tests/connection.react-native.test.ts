import { jest } from "@jest/globals";
import { Status } from "nice-grpc-common";
import { WalletConfigService } from "../services/config.js";
import { DefaultSparkSigner } from "../signer/signer.js";

type NativeUnaryCallResponse = {
  statusCode: number;
  statusMessage?: string;
};

type NativeCallParams = Record<string, unknown>;

const nativeGrpcModule = {
  grpcUnaryCall:
    jest.fn<(params: NativeCallParams) => Promise<NativeUnaryCallResponse>>(),
  grpcUnaryCallCancel: jest.fn<(params: NativeCallParams) => Promise<void>>(),
  grpcServerStreamStart:
    jest.fn<(params: NativeCallParams) => Promise<NativeCallParams>>(),
  grpcServerStreamNext:
    jest.fn<(params: NativeCallParams) => Promise<NativeCallParams>>(),
  grpcServerStreamCancel:
    jest.fn<(params: NativeCallParams) => Promise<void>>(),
  grpcCloseChannel: jest.fn<(params: NativeCallParams) => Promise<void>>(),
};

jest.unstable_mockModule("react-native", () => ({
  NativeModules: {
    SparkGrpcModule: nativeGrpcModule,
  },
}));

const { ConnectionManagerReactNative } =
  await import("../services/connection/connection.react-native.js");

class TestConnectionManagerReactNative extends ConnectionManagerReactNative {
  public createTestClient<T>(
    channel: { address: string; isStreamClientType: boolean },
    withRetries: boolean,
  ): Promise<T> {
    return this.createGrpcClient<T>(
      {
        fullName: "spark.TestService",
        methods: {
          testUnary: {
            name: "TestUnary",
            requestStream: false,
            responseStream: false,
            options: {},
            requestType: {
              fromPartial: (value: unknown) => value,
              encode: () => ({ finish: () => new Uint8Array(0) }),
              decode: (value: Uint8Array) => value,
            },
            responseType: {
              fromPartial: (value: unknown) => value,
              encode: () => ({ finish: () => new Uint8Array(0) }),
              decode: (value: Uint8Array) => value,
            },
          },
        },
      } as never,
      channel as never,
      withRetries,
    );
  }
}

describe("ConnectionManagerReactNative retry deadlines", () => {
  beforeEach(() => {
    jest.useFakeTimers();
    jest.setSystemTime(new Date("2026-01-01T00:00:00Z"));
    nativeGrpcModule.grpcUnaryCall.mockResolvedValue({
      statusCode: Status.UNAVAILABLE,
      statusMessage: "try again",
    });
    nativeGrpcModule.grpcUnaryCallCancel.mockResolvedValue(undefined);
    nativeGrpcModule.grpcCloseChannel.mockResolvedValue(undefined);
  });

  afterEach(() => {
    jest.useRealTimers();
    jest.clearAllMocks();
  });

  test("returns DEADLINE_EXCEEDED when the deadline expires during retry backoff", async () => {
    const manager = new TestConnectionManagerReactNative(
      new WalletConfigService(
        {
          network: "REGTEST",
        },
        new DefaultSparkSigner(),
      ),
    );

    const client = await manager.createTestClient<{
      testUnary: (
        request: unknown,
        options: {
          deadline: number;
          retryBaseDelayMs: number;
          retryMaxDelayMs: number;
          retryMaxAttempts: number;
        },
      ) => Promise<unknown>;
    }>(
      {
        address: "https://spark.test",
        isStreamClientType: false,
      },
      true,
    );

    const call = client.testUnary(
      {},
      {
        deadline: Date.now() + 50,
        retryBaseDelayMs: 1_000,
        retryMaxDelayMs: 1_000,
        retryMaxAttempts: 2,
      },
    );

    const expectation = expect(call).rejects.toMatchObject({
      code: Status.DEADLINE_EXCEEDED,
    });

    await jest.advanceTimersByTimeAsync(50);

    await expectation;
    expect(nativeGrpcModule.grpcUnaryCall).toHaveBeenCalledTimes(1);
  });
});
